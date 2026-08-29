package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/pluginhost"
	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// activatePlugins crosses the trust boundary only when at least one enabled,
// digest-verified package exists. A missing private runtime degrades those
// plugins while leaving every built-in service usable.
func (a *App) activatePlugins(ctx context.Context, discovered plugins.Runtime) plugins.Runtime {
	runtime := plugins.Runtime{
		Candidates: []plugins.Candidate{},
		Loaded:     []plugins.LoadedPlugin{},
		Failures:   append([]plugins.LoadFailure(nil), discovered.Failures...),
	}
	// Inspect-stage failures (manifest/digest refusals) never reach the host;
	// the supervisor still owns them so doctor/desktop stay authoritative.
	for _, failure := range discovered.Failures {
		a.pluginSup.recordStartupFailure(failure)
	}
	if len(discovered.Candidates) == 0 {
		return runtime
	}
	config, err := pluginhost.ConfigFromEnv()
	if err != nil {
		for _, candidate := range discovered.Candidates {
			failure := pluginFailure(candidate, err)
			runtime.Failures = append(runtime.Failures, failure)
			a.pluginSup.recordStartupFailure(failure)
		}
		return runtime
	}
	a.pluginSup.configure(config)
	a.pluginSup.seed(discovered.Candidates)

	host, err := pluginhost.Start(ctx, config)
	if err != nil {
		for _, candidate := range discovered.Candidates {
			failure := pluginFailure(candidate, err)
			runtime.Failures = append(runtime.Failures, failure)
			a.pluginSup.recordStartupFailure(failure)
		}
		return runtime
	}
	a.pluginSup.attach(host)

	for _, candidate := range discovered.Candidates {
		activationResult, activateErr := host.Activate(ctx, pluginhost.PluginSpec{
			Root:          candidate.Record.Path,
			TrustedDigest: candidate.Record.Digest,
			ExpectedName:  candidate.Record.Name,
		})
		if activateErr != nil {
			a.recordStartupLoadFailure(&runtime, candidate, activateErr)
			continue
		}
		staged, stageErr := a.stagePluginActivationFor(host, initialRuntimeEpoch, activationResult)
		if stageErr != nil {
			a.disposeAndRecordFailure(&runtime, candidate, activationResult, stageErr)
			continue
		}
		if commitErr := a.commitStartupActivation(staged, candidate); commitErr != nil {
			a.disposeAndRecordFailure(&runtime, candidate, activationResult, commitErr)
			continue
		}
		runtime.Loaded = append(runtime.Loaded, plugins.LoadedPlugin{
			Record: candidate.Record, ActivationID: activationResult.ActivationID,
		})
		a.pluginSup.recordLoaded(plugins.LoadedPlugin{
			Record: candidate.Record, ActivationID: activationResult.ActivationID,
		})
	}
	return runtime
}

// initialRuntimeEpoch is the generation stamp for adapters committed by the
// startup activation. pluginSupervisor.attach publishes the same epoch.
const initialRuntimeEpoch uint64 = 1

func (a *App) recordStartupLoadFailure(
	runtime *plugins.Runtime,
	candidate plugins.Candidate,
	err error,
) {
	failure := pluginFailure(candidate, err)
	runtime.Failures = append(runtime.Failures, failure)
	a.pluginSup.recordStartupFailure(failure)
}

func (a *App) disposeAndRecordFailure(
	runtime *plugins.Runtime,
	candidate plugins.Candidate,
	activationResult *pluginhost.Activation,
	err error,
) {
	_ = activationResult.Dispose(context.Background())
	a.recordStartupLoadFailure(runtime, candidate, err)
}

func pluginFailure(candidate plugins.Candidate, err error) plugins.LoadFailure {
	untrusted := false
	var remote *pluginhost.RemoteError
	if errors.As(err, &remote) && remote.Code == "PLUGIN_UNTRUSTED" {
		untrusted = true
	}
	return plugins.LoadFailure{
		Name: candidate.Record.Name, Path: candidate.Record.Path,
		Reason: err.Error(), Untrusted: untrusted,
	}
}

// PluginDiagnostics returns redacted dynamic host health plus the immutable
// startup failures. A bounded status round trip detects a live process whose
// event loop stopped responding after plugin activation. After a successful
// recovery this reports running again — the view is supervisor-authoritative,
// not the frozen startup copy.
func (a *App) PluginDiagnostics(ctx context.Context) plugins.Diagnostics {
	loaded, failures := a.pluginSup.runtimeSnapshot()
	diagnostics := plugins.Diagnostics{
		Host: plugins.HostDiagnostic{
			State:         "inactive",
			LoadedPlugins: len(loaded),
		},
		Failures: failures,
	}
	active, running, errText := a.pluginSup.healthView()
	if !active {
		return diagnostics
	}
	if !running {
		diagnostics.Host.State = "degraded"
		diagnostics.Host.Error = errText
		return diagnostics
	}
	host := a.pluginSup.currentHost()
	if host == nil {
		diagnostics.Host.State = "inactive"
		return diagnostics
	}
	statusCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	status, err := host.Status(statusCtx)
	if err != nil {
		diagnostics.Host.State = "degraded"
		diagnostics.Host.Error = err.Error()
		return diagnostics
	}
	diagnostics.Host.State = "running"
	diagnostics.Host.ActivationCount = status.ActivationCount
	diagnostics.Host.ObjectCount = status.ObjectCount
	return diagnostics
}

// currentHost returns the live host pointer for diagnostics.
func (s *pluginSupervisor) currentHost() *pluginhost.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.host
}

type stagedProvider struct {
	value      provider.Provider
	compatible bool
}

type stagedActivation struct {
	providers []stagedProvider
	clients   []client.Adapter
	catalog   []stagedCatalog
}

type stagedCatalog struct {
	value            catalog.Provider
	preserveExisting bool
}

// commitStartupActivation commits one candidate's staged registrations before
// the registries seal, recording exactly what this runtime now owns so a
// later recovery can swap those entries and nothing else.
func (a *App) commitStartupActivation(staged stagedActivation, candidate plugins.Candidate) error {
	if err := a.validatePluginConflicts(staged); err != nil {
		return err
	}
	ownedProviders := []ownedRegistration{}
	ownedClients := []ownedRegistration{}
	ownedCatalog := []ownedCatalogEntry{}
	for _, item := range staged.providers {
		id := item.value.ID()
		if item.compatible {
			if err := a.Providers.ReplaceCompatible(item.value.(*provider.Compatible)); err != nil {
				return err
			}
		} else if err := a.Providers.Register(item.value); err != nil {
			return err
		}
		ownedProviders = append(ownedProviders, ownedRegistration{id: id, committed: item.value})
		if base, ok := rpcBaseOf(item.value); ok {
			a.pluginSup.trackProviderBase(id, base)
		}
	}
	for _, item := range staged.clients {
		id := item.ID()
		if err := a.Clients.Register(item); err != nil {
			return err
		}
		ownedClients = append(ownedClients, ownedRegistration{id: id, committed: item})
		if base, ok := rpcClientBaseOf(item); ok {
			a.pluginSup.trackClientBase(id, base)
		}
	}
	for _, item := range staged.catalog {
		entry := activation.CatalogEntry{
			Name: item.value.Name, APIStyle: string(item.value.APIStyle),
			Protocols: item.value.Protocols,
		}
		var wrote, wroteDep bool
		// Compatible providers intentionally preserve a richer built-in catalog
		// entry with the same id (OpenRouter is the canonical case).
		if item.preserveExisting {
			wrote = a.Runtime.Catalog.SetIfAbsent(item.value)
			wroteDep = a.Deps.Catalog.SetIfAbsent(item.value.ID, entry)
		} else if a.Runtime.Catalog.SetIfAbsent(item.value) {
			wrote = true
			wroteDep = a.Deps.Catalog.SetIfAbsent(item.value.ID, entry)
		}
		ownedCatalog = append(ownedCatalog, ownedCatalogEntry{
			id: item.value.ID, committed: item.value, wrote: wrote,
			depCommitted: entry, wroteDep: wroteDep,
		})
	}
	a.pluginSup.publishOwnedPlugin(pluginOwnership{
		name:      candidate.Record.Name,
		providers: ownedProviders,
		clients:   ownedClients,
		catalog:   ownedCatalog,
	})
	return nil
}

func (a *App) validatePluginConflicts(staged stagedActivation) error {
	providerIDs := map[string]bool{}
	for _, item := range staged.providers {
		id := item.value.ID()
		if providerIDs[id] {
			return fmt.Errorf("provider already registered by this plugin: %s", id)
		}
		providerIDs[id] = true
		if a.Providers.Has(id) {
			existing, _ := a.Providers.Get(id)
			if !item.compatible {
				return fmt.Errorf("provider already registered: %s", id)
			}
			if _, ok := existing.(*provider.Compatible); !ok {
				return fmt.Errorf("provider already registered: %s", id)
			}
		}
	}
	clientIDs := map[string]bool{}
	for _, item := range staged.clients {
		id := item.ID()
		if clientIDs[id] || a.Clients.Has(id) {
			return fmt.Errorf("client already registered: %s", id)
		}
		clientIDs[id] = true
	}
	catalogIDs := map[string]bool{}
	for _, item := range staged.catalog {
		id := item.value.ID
		if catalogIDs[id] {
			return fmt.Errorf("catalog provider already registered by this plugin: %s", id)
		}
		catalogIDs[id] = true
		if _, exists := a.Runtime.Catalog.Lookup(id); exists && !item.preserveExisting {
			return fmt.Errorf("catalog provider already registered: %s", id)
		}
	}
	return nil
}

// validateRecoveryConflicts mirrors validatePluginConflicts for the recovery
// path, but ownership is owner-aware: only the plugin that committed an id
// may replace it. A previously owned id is not "replaceable by anyone".
func (a *App) validateRecoveryConflicts(
	pluginName string,
	staged stagedActivation,
	prevProviders map[string]ownedRef,
	prevClients map[string]ownedRef,
	prevCatalog map[string]catalogOwnedRef,
	claimedProviders, claimedClients, claimedCatalog map[string]string,
) error {
	providerIDs := map[string]bool{}
	for _, item := range staged.providers {
		id := item.value.ID()
		if providerIDs[id] {
			return fmt.Errorf("provider already registered by this plugin: %s", id)
		}
		providerIDs[id] = true
		if owner, ok := claimedProviders[id]; ok && owner != pluginName {
			return fmt.Errorf("provider already registered: %s", id)
		}
		if prev, owned := prevProviders[id]; owned {
			if prev.plugin != pluginName {
				return fmt.Errorf("provider already registered: %s", id)
			}
			continue
		}
		if a.Providers.Has(id) {
			existing, _ := a.Providers.Get(id)
			if !item.compatible {
				return fmt.Errorf("provider already registered: %s", id)
			}
			if _, ok := existing.(*provider.Compatible); !ok {
				return fmt.Errorf("provider already registered: %s", id)
			}
		}
	}
	clientIDs := map[string]bool{}
	for _, item := range staged.clients {
		id := item.ID()
		if clientIDs[id] {
			return fmt.Errorf("client already registered by this plugin: %s", id)
		}
		clientIDs[id] = true
		if owner, ok := claimedClients[id]; ok && owner != pluginName {
			return fmt.Errorf("client already registered: %s", id)
		}
		if prev, owned := prevClients[id]; owned {
			if prev.plugin != pluginName {
				return fmt.Errorf("client already registered: %s", id)
			}
			continue
		}
		if a.Clients.Has(id) {
			return fmt.Errorf("client already registered: %s", id)
		}
	}
	catalogIDs := map[string]bool{}
	for _, item := range staged.catalog {
		id := item.value.ID
		if catalogIDs[id] {
			return fmt.Errorf("catalog provider already registered by this plugin: %s", id)
		}
		catalogIDs[id] = true
		if owner, ok := claimedCatalog[id]; ok && owner != pluginName {
			return fmt.Errorf("catalog provider already registered: %s", id)
		}
		if prev, owned := prevCatalog[id]; owned {
			if prev.plugin != pluginName {
				return fmt.Errorf("catalog provider already registered: %s", id)
			}
			continue
		}
		if _, exists := a.Runtime.Catalog.Lookup(id); exists && !item.preserveExisting {
			return fmt.Errorf("catalog provider already registered: %s", id)
		}
	}
	return nil
}

// stagePluginActivationFor builds fresh adapters bound to ONE specific host
// generation. Startup passes the initial host; recovery passes the replacement
// so handles from a dead generation are never reused.
func (a *App) stagePluginActivationFor(
	host *pluginhost.Host,
	gen uint64,
	result *pluginhost.Activation,
) (stagedActivation, error) {
	staged := stagedActivation{}
	for _, descriptor := range result.Descriptors {
		switch descriptor.Kind {
		case pluginhost.RegistrationProvider:
			value, err := newRPCProvider(a.Root, host, a.pluginSup, gen, descriptor)
			if err != nil {
				return stagedActivation{}, err
			}
			staged.providers = append(staged.providers, stagedProvider{value: value})
		case pluginhost.RegistrationClient:
			value, err := newRPCClient(host, a.pluginSup, gen, descriptor)
			if err != nil {
				return stagedActivation{}, err
			}
			staged.clients = append(staged.clients, value)
		case pluginhost.RegistrationCatalogProvider:
			value, err := catalogFromDescriptor(descriptor)
			if err != nil {
				return stagedActivation{}, err
			}
			staged.catalog = append(staged.catalog, stagedCatalog{value: value})
		}
	}
	for _, definition := range result.CompatibleProviders {
		value, err := provider.NewCompatible(definition)
		if err != nil {
			return stagedActivation{}, fmt.Errorf("plugin compatible provider %q: %w", definition.ID, err)
		}
		staged.providers = append(staged.providers, stagedProvider{value: value, compatible: true})
		staged.catalog = append(staged.catalog, stagedCatalog{
			value: catalogFromCompatible(definition), preserveExisting: true,
		})
	}
	return staged, nil
}

type rpcProvider struct {
	root string
	host *pluginhost.Host
	// sup + gen bind this adapter to one host generation. On a lost host the
	// recovery helpers resolve the CURRENT-generation base by id — stale
	// handles are never replayed against whatever process happens to exist.
	sup *pluginSupervisor
	gen uint64

	descriptor  pluginhost.Descriptor
	id          string
	name        string
	description string
	shortName   string
	// proxy carries the forwarded lifecycle for start-capable providers;
	// nil when the plugin declares no startProxy.
	proxy *rpcProxyCore

	mu       sync.Mutex
	adapters map[string]transport.Adapter
}

func newRPCProvider(
	root string,
	host *pluginhost.Host,
	sup *pluginSupervisor,
	gen uint64,
	descriptor pluginhost.Descriptor,
) (provider.Provider, error) {
	id, err := descriptor.ID()
	if err != nil {
		return nil, err
	}
	name, err := stringProperty(descriptor, "name", true)
	if err != nil {
		return nil, err
	}
	description, err := stringProperty(descriptor, "description", true)
	if err != nil {
		return nil, err
	}
	shortName, _ := stringProperty(descriptor, "shortName", false)
	base := &rpcProvider{
		root: root, host: host, sup: sup, gen: gen, descriptor: descriptor, id: id, name: name,
		description: description, shortName: shortName, adapters: map[string]transport.Adapter{},
	}
	hasLoginMethods := hasMethod(descriptor.Methods, "loginMethods")
	hasLogin := hasMethod(descriptor.Methods, "login")
	if hasLoginMethods != hasLogin {
		return nil, fmt.Errorf(
			"plugin provider %s must implement loginMethods and login together",
			id,
		)
	}
	hasUsage := hasMethod(descriptor.Methods, "liveUsage")
	hasProxyStart := hasMethod(descriptor.Methods, "startProxy")
	core := &rpcProxyCore{host: host, sup: sup, gen: gen, descriptor: descriptor, id: id}
	base.proxy = core
	if !hasProxyStart {
		switch {
		case hasLogin && hasUsage:
			return &rpcLoginUsageProvider{rpcProvider: base}, nil
		case hasLogin:
			return &rpcLoginProvider{rpcProvider: base}, nil
		case hasUsage:
			return &rpcUsageProvider{rpcProvider: base}, nil
		default:
			return base, nil
		}
	}
	// startProxy presence is the ONLY proxy-capability signal. Concrete
	// embedding (never an interface field) keeps the login/usage method sets
	// visible alongside the forwarded proxy lifecycle; stop/status/logs stay
	// optional behind ErrPluginMethodAbsent inside rpcProxyCore.
	switch {
	case hasLogin && hasUsage:
		return &rpcStartLoginUsageProvider{rpcLoginUsageProvider: rpcLoginUsageProvider{base}, rpcProxyCore: core}, nil
	case hasLogin:
		return &rpcStartLoginProvider{rpcLoginProvider: rpcLoginProvider{base}, rpcProxyCore: core}, nil
	case hasUsage:
		return &rpcStartUsageProvider{rpcUsageProvider: rpcUsageProvider{base}, rpcProxyCore: core}, nil
	default:
		return &rpcStartProvider{rpcProvider: base, rpcProxyCore: core}, nil
	}
}

// rpcBaseProvider exposes the shared base behind one concrete capability
// wrapper so recovery can resolve the current-generation adapter by registry
// id. Unexported on purpose: plugin output never influences dispatch.
type rpcBaseProvider interface {
	rpcBase() *rpcProvider
}

func (p *rpcProvider) rpcBase() *rpcProvider           { return p }
func (p *rpcLoginProvider) rpcBase() *rpcProvider      { return p.rpcProvider }
func (p *rpcUsageProvider) rpcBase() *rpcProvider      { return p.rpcProvider }
func (p *rpcLoginUsageProvider) rpcBase() *rpcProvider { return p.rpcProvider }
func (p *rpcStartProvider) rpcBase() *rpcProvider      { return p.rpcProvider }

func rpcBaseOf(value provider.Provider) (*rpcProvider, bool) {
	base, ok := value.(rpcBaseProvider)
	if !ok {
		return nil, false
	}
	return base.rpcBase(), true
}

func (p *rpcProvider) ID() string          { return p.id }
func (p *rpcProvider) Name() string        { return p.name }
func (p *rpcProvider) Description() string { return p.description }
func (p *rpcProvider) ShortName() string {
	if p.shortName != "" {
		return p.shortName
	}
	return p.name
}
func (p *rpcProvider) CanProxy() bool { return hasMethod(p.descriptor.Methods, "startProxy") }

// hostLossKind classifies an invocation failure against a lost runtime.
// lossNotDispatched means the request provably never left this process
// (replay is always safe); lossAmbiguous means the frame may have been
// delivered, so only read-only work may retry.
type hostLossKind int

const (
	lossNone hostLossKind = iota
	lossNotDispatched
	lossAmbiguous
)

func classifyHostLoss(err error) hostLossKind {
	if err == nil {
		return lossNone
	}
	// Local encode/size rejection never crossed the process boundary.
	// Checked before ErrProtocol so a wrapped pair cannot be promoted to
	// ambiguous host loss (and then ErrUncertainMutation).
	if errors.Is(err, pluginhost.ErrLocalFrameRejected) {
		return lossNone
	}
	if errors.Is(err, pluginhost.ErrNotDispatched) {
		return lossNotDispatched
	}
	if errors.Is(err, pluginhost.ErrWriteInterrupted) ||
		errors.Is(err, pluginhost.ErrHostCrashed) ||
		errors.Is(err, pluginhost.ErrHostClosed) ||
		errors.Is(err, pluginhost.ErrProtocol) {
		return lossAmbiguous
	}
	return lossNone
}

// withRecovery runs fn once on this generation's adapter. On a lost host it
// recovers AT MOST once and re-runs fn against the replacement generation —
// but only when the original call provably never started or was read-only.
// A mutating call lost after dispatch returns ErrUncertainMutation WITHOUT a
// second execution: the effect may already have happened.
func (p *rpcProvider) withRecovery(ctx context.Context, readOnly bool, fn func(*rpcProvider) error) error {
	err := fn(p)
	kind := classifyHostLoss(err)
	if kind == lossNone {
		return err
	}
	replacement, recoverErr := p.sup.replacementProvider(ctx, p.gen, p.id)
	if recoverErr != nil {
		if kind == lossAmbiguous && !readOnly {
			return fmt.Errorf("%w: %w (recovery: %v)", ErrUncertainMutation, err, recoverErr)
		}
		return errors.Join(err, recoverErr)
	}
	if kind == lossAmbiguous && !readOnly {
		// Recovery succeeded; the mutation itself stays unreplayed.
		return fmt.Errorf("%w: %w", ErrUncertainMutation, err)
	}
	return fn(replacement)
}

func (p *rpcProvider) DetectLive() (provider.LiveAuthStatus, error) {
	var result provider.LiveAuthStatus
	err := p.withRecovery(context.Background(), true, func(rp *rpcProvider) error {
		return rp.invokeRequired("detectLive", []any{}, &result)
	})
	return result, err
}

func (p *rpcProvider) Backup(destDir string) (provider.SnapshotInfo, error) {
	var result provider.SnapshotInfo
	err := p.withRecovery(context.Background(), false, func(rp *rpcProvider) error {
		return rp.invokeRequired("backup", []any{destDir}, &result)
	})
	return result, err
}

func (p *rpcProvider) Restore(srcDir string) error {
	return p.withRecovery(context.Background(), false, func(rp *rpcProvider) error {
		return rp.invokeRequired("restore", []any{srcDir}, nil)
	})
}

func (p *rpcProvider) DescribeSnapshot(srcDir string) (provider.SnapshotInfo, error) {
	var result provider.SnapshotInfo
	_ = p.withRecovery(context.Background(), true, func(rp *rpcProvider) error {
		if !hasMethod(rp.descriptor.Methods, "describeSnapshot") {
			return nil
		}
		return rp.host.Invoke(
			context.Background(), rp.descriptor.Handle, "describeSnapshot", []any{srcDir}, &result,
		)
	})
	return result, nil
}

func (p *rpcProvider) AccountAdapter(accountName string, proxyEnabled bool) transport.Adapter {
	key := fmt.Sprintf("%s\x00%t", accountName, proxyEnabled)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.adapters[key]; ok {
		return existing
	}
	fallback := unsupportedPluginAdapter(p.id, accountName)
	if !hasMethod(p.descriptor.Methods, "sourceAdapter") {
		p.adapters[key] = fallback
		return fallback
	}
	account := map[string]any{
		"meta":        map[string]any{"name": accountName, "provider": p.id},
		"snapshotDir": paths.AccountSnapshotDir(p.root, p.id, accountName),
		"accountDir":  paths.AccountDir(p.root, p.id, accountName),
		"proxy":       map[string]any{"enabled": proxyEnabled},
	}
	var built transport.Adapter
	// Building the adapter retains a new host object: idempotent per
	// generation and read-only in effect, so it recovers like a read.
	buildErr := p.withRecovery(context.Background(), true, func(rp *rpcProvider) error {
		remote, err := rp.host.InvokeObject(
			context.Background(), rp.descriptor.Handle, "sourceAdapter", []any{account},
		)
		if err != nil {
			return err
		}
		adapter, err := transportFromRemote(
			rp.host, rp.sup, rp.gen, remote, rp.id, accountName, proxyEnabled,
		)
		if err != nil {
			return err
		}
		built = adapter
		return nil
	})
	if buildErr != nil {
		p.adapters[key] = fallback
		return fallback
	}
	p.adapters[key] = built
	return built
}

func (p *rpcProvider) invokeRequired(method string, args []any, output any) error {
	if !hasMethod(p.descriptor.Methods, method) {
		return fmt.Errorf("plugin provider %s does not implement %s", p.id, method)
	}
	return p.host.Invoke(context.Background(), p.descriptor.Handle, method, args, output)
}

type rpcLoginProvider struct{ *rpcProvider }

func (p *rpcLoginProvider) LoginMethods() []provider.LoginMethod {
	return p.rpcProvider.loginMethods()
}

func (p *rpcLoginProvider) Login(ctx context.Context, methodID, source string) error {
	return p.rpcProvider.login(ctx, methodID, source)
}

type rpcUsageProvider struct{ *rpcProvider }

func (p *rpcUsageProvider) LiveUsage(ctx context.Context) (*provider.LiveUsage, error) {
	return p.rpcProvider.liveUsage(ctx)
}

type rpcLoginUsageProvider struct{ *rpcProvider }

func (p *rpcLoginUsageProvider) LoginMethods() []provider.LoginMethod {
	return p.rpcProvider.loginMethods()
}

func (p *rpcLoginUsageProvider) Login(ctx context.Context, methodID, source string) error {
	return p.rpcProvider.login(ctx, methodID, source)
}

func (p *rpcLoginUsageProvider) LiveUsage(ctx context.Context) (*provider.LiveUsage, error) {
	return p.rpcProvider.liveUsage(ctx)
}

func (p *rpcProvider) loginMethods() []provider.LoginMethod {
	var remote []struct {
		ID                string `json:"id"`
		Label             string `json:"label"`
		Description       string `json:"description"`
		Provenance        string `json:"provenance"`
		Preferred         bool   `json:"preferred"`
		Source            string `json:"source"`
		Available         *bool  `json:"available"`
		UnavailableReason string `json:"unavailableReason"`
	}
	_ = p.withRecovery(context.Background(), true, func(rp *rpcProvider) error {
		return rp.host.Invoke(
			context.Background(), rp.descriptor.Handle, "loginMethods", []any{}, &remote,
		)
	})
	methods := make([]provider.LoginMethod, 0, len(remote))
	seen := map[string]bool{}
	for _, item := range remote {
		if item.ID == "" || item.Label == "" || seen[item.ID] ||
			(item.Provenance != "official" && item.Provenance != "community") {
			continue
		}
		seen[item.ID] = true
		available := item.Available == nil || *item.Available
		methods = append(methods, provider.LoginMethod{
			ID: item.ID, Label: item.Label, Description: item.Description,
			Provenance: item.Provenance, Preferred: item.Preferred, Source: item.Source,
			Available: available, UnavailableReason: item.UnavailableReason,
		})
	}
	return methods
}

func (p *rpcProvider) login(ctx context.Context, methodID, source string) error {
	loginContext := map[string]any{"methodId": methodID}
	if source != "" {
		loginContext["source"] = source
	}
	// Login is a mutating, signal-injected hook: an ambiguous host loss is
	// never replayed (the browser flow may already have started), while a
	// provably undispatched call recovers and retries once. Cancellation and
	// deadline flow through ctx unchanged.
	return p.withRecovery(ctx, false, func(rp *rpcProvider) error {
		return rp.host.InvokeWithSignal(
			ctx, rp.descriptor.Handle, "login", []any{loginContext}, 0, nil,
		)
	})
}

func (p *rpcProvider) liveUsage(ctx context.Context) (*provider.LiveUsage, error) {
	var usage *provider.LiveUsage
	err := p.withRecovery(ctx, true, func(rp *rpcProvider) error {
		return rp.host.Invoke(ctx, rp.descriptor.Handle, "liveUsage", []any{}, &usage)
	})
	if err != nil {
		return nil, err
	}
	return usage, nil
}

// rpcProxyCore carries the forwarded proxy lifecycle for plugin providers.
// The host-owned AbortSignal is injected into the context argument so
// cancellable lifecycle calls abort the plugin coroutine; signals never cross
// the JSON boundary any other way. It is embedded by concrete start-capable
// wrapper types so capability sets compose without interface erasure.
type rpcProxyCore struct {
	host       *pluginhost.Host
	sup        *pluginSupervisor
	gen        uint64
	descriptor pluginhost.Descriptor
	id         string

	servedMu   sync.Mutex
	lastServed proxy.PluginRuntimeIdentity
}

func (p *rpcProxyCore) StartProxy(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
) (provider.ProxyHandle, error) {
	handle, err := p.startProxyOnce(ctx, pluginCtx)
	p.rememberServedFrom(p.host)
	if classifyHostLoss(err) != lossNotDispatched {
		return handle, err
	}
	// Host already gone before this start left the process: recover once and
	// retry. An in-flight or unsettled start is never replayed — that path
	// terminates the exact generation instead.
	replacement, recoverErr := p.sup.replacementProvider(ctx, p.gen, p.id)
	if recoverErr != nil || replacement.proxy == nil {
		return handle, errors.Join(err, recoverErr)
	}
	handle, err = replacement.proxy.startProxyOnce(ctx, pluginCtx)
	p.rememberServedFrom(replacement.proxy.host)
	return handle, err
}

func (p *rpcProxyCore) rememberServedFrom(host *pluginhost.Host) {
	if p == nil || host == nil {
		return
	}
	generation, pid := host.ProcessIdentity()
	p.servedMu.Lock()
	p.lastServed = proxy.PluginRuntimeIdentity{Generation: generation, PID: pid}
	p.servedMu.Unlock()
}

func (p *rpcProxyCore) pluginRuntime() proxy.PluginRuntimeIdentity {
	if p == nil {
		return proxy.PluginRuntimeIdentity{}
	}
	p.servedMu.Lock()
	served := p.lastServed
	p.servedMu.Unlock()
	if served.IsValid() {
		return served
	}
	if p.host == nil {
		return proxy.PluginRuntimeIdentity{}
	}
	generation, pid := p.host.ProcessIdentity()
	return proxy.PluginRuntimeIdentity{Generation: generation, PID: pid}
}

func (p *rpcProxyCore) startProxyOnce(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
) (provider.ProxyHandle, error) {
	var handle provider.ProxyHandle
	// Settlement-aware cancellation: an aborted start must be provably
	// finished before compensation runs. Without the acknowledgement the
	// coroutine may still bind after every timing window has closed.
	settled, err := p.host.InvokeSettling(
		ctx, p.descriptor.Handle, "startProxy",
		[]any{proxyContextPayload(pluginCtx)}, 0, &handle, p.host.CallBudget(),
	)
	if !settled {
		if err != nil {
			return provider.ProxyHandle{}, fmt.Errorf("%w: %w", proxy.ErrUnsettledInvocation, err)
		}
		return provider.ProxyHandle{}, fmt.Errorf("%w: protocol violation: missing settlement proof for %q", proxy.ErrUnsettledInvocation, p.id)
	}
	if err != nil {
		return provider.ProxyHandle{}, err
	}
	started := &proxy.PluginStart{
		Endpoint: handle.Endpoint, Compatibility: handle.Compatibility,
		PID: handle.PID, InstanceID: handle.InstanceID, LogPath: handle.LogPath,
	}
	if err := proxy.ValidatePluginStart(started); err != nil {
		return provider.ProxyHandle{}, err
	}
	// ADR 0006: token generation and persistence are engine-owned. Whatever a
	// plugin reports in these fields is discarded before it can spread.
	handle.Token = ""
	handle.LeaseID = ""
	return handle, nil
}

func (p *rpcProxyCore) StopProxy(ctx context.Context, pluginCtx provider.ProxyContext) error {
	if !hasMethod(p.descriptor.Methods, "stopProxy") {
		return proxy.ErrPluginMethodAbsent
	}
	// Like StartProxy: no automatic recovery. Stop verification runs against
	// the persisted echo, which a dead host already satisfies; silently
	// respawning the runtime to ask a replacement about a dead instance would
	// only widen the failure surface.
	return p.host.InvokeWithSignal(
		ctx, p.descriptor.Handle, "stopProxy",
		[]any{proxyContextPayload(pluginCtx)}, 0, nil,
	)
}

func (p *rpcProxyCore) ProxyStatus(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
) (*proxy.Status, error) {
	if !hasMethod(p.descriptor.Methods, "proxyStatus") {
		return nil, proxy.ErrPluginMethodAbsent
	}
	status, err := p.proxyStatusOnce(ctx, pluginCtx)
	if classifyHostLoss(err) == lossNone {
		return status, err
	}
	// Read-only observation: recover once, then answer from the replacement
	// generation's core if one exists.
	replacement, recoverErr := p.sup.replacementProvider(ctx, p.gen, p.id)
	if recoverErr != nil || replacement.proxy == nil ||
		hasMethod(replacement.descriptor.Methods, "proxyStatus") == false {
		return nil, errors.Join(err, recoverErr)
	}
	return replacement.proxy.proxyStatusOnce(ctx, pluginCtx)
}

func (p *rpcProxyCore) proxyStatusOnce(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
) (*proxy.Status, error) {
	var remote provider.ProxyStatus
	if err := p.host.InvokeWithSignal(
		ctx, p.descriptor.Handle, "proxyStatus",
		[]any{proxyContextPayload(pluginCtx)}, 0, &remote,
	); err != nil {
		return nil, err
	}
	if err := validatePluginProxyStatus(remote); err != nil {
		return nil, err
	}
	return &proxy.Status{
		Enabled: remote.Enabled, Running: remote.Running, Port: remote.Port,
		Host: remote.Host, Endpoint: remote.Endpoint, Compatibility: remote.Compatibility,
		PID: remote.PID, LogPath: remote.LogPath, Detail: remote.Detail,
	}, nil
}

func (p *rpcProxyCore) ReadProxyLogs(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
	lines int,
) (string, error) {
	if !hasMethod(p.descriptor.Methods, "readProxyLogs") {
		return "", proxy.ErrPluginMethodAbsent
	}
	text, err := p.readProxyLogsOnce(ctx, pluginCtx, lines)
	if classifyHostLoss(err) == lossNone {
		return text, err
	}
	replacement, recoverErr := p.sup.replacementProvider(ctx, p.gen, p.id)
	if recoverErr != nil || replacement.proxy == nil ||
		!hasMethod(replacement.descriptor.Methods, "readProxyLogs") {
		return "", errors.Join(err, recoverErr)
	}
	return replacement.proxy.readProxyLogsOnce(ctx, pluginCtx, lines)
}

func (p *rpcProxyCore) readProxyLogsOnce(
	ctx context.Context,
	pluginCtx provider.ProxyContext,
	lines int,
) (string, error) {
	var text string
	if err := p.host.InvokeWithSignal(
		ctx, p.descriptor.Handle, "readProxyLogs",
		[]any{proxyContextPayload(pluginCtx), lines}, 0, &text,
	); err != nil {
		return "", err
	}
	return text, nil
}

// Start-capable wrappers. Each pairs one login/usage combination with the
// forwarded proxy core so optional capabilities remain type-assertable exactly
// as the plugin declared them.
type rpcStartProvider struct {
	*rpcProvider
	*rpcProxyCore
}

type rpcStartLoginProvider struct {
	rpcLoginProvider
	*rpcProxyCore
}

type rpcStartUsageProvider struct {
	rpcUsageProvider
	*rpcProxyCore
}

type rpcStartLoginUsageProvider struct {
	rpcLoginUsageProvider
	*rpcProxyCore
}

// proxyContextPayload serializes exactly the TS ProxyContext keys. The config
// struct already marshals with TS field names (enabled/port/host/options).
func proxyContextPayload(pluginCtx provider.ProxyContext) map[string]any {
	payload := map[string]any{
		"providerId":  pluginCtx.ProviderID,
		"accountName": pluginCtx.AccountName,
		"snapshotDir": pluginCtx.SnapshotDir,
		"runtimeDir":  pluginCtx.RuntimeDir,
		"config":      pluginCtx.Config,
	}
	if pluginCtx.Token != "" {
		payload["token"] = pluginCtx.Token
	}
	return payload
}

// validatePluginProxyStatus bounds a plugin-reported status before it can
// reach Service post-processing or any presentation surface.
func validatePluginProxyStatus(status provider.ProxyStatus) error {
	if status.Port < 0 || status.Port > 65535 || status.PID < 0 {
		return fmt.Errorf("plugin proxy status has an out-of-range port or pid")
	}
	for _, value := range []string{status.Host, status.Endpoint, status.Compatibility, status.LogPath, status.Detail} {
		if len(value) > 4096 {
			return fmt.Errorf("plugin proxy status has an oversized field")
		}
	}
	if status.Endpoint != "" {
		parsed, err := url.Parse(status.Endpoint)
		if err != nil || parsed.Hostname() == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("plugin proxy status has an invalid endpoint %q", status.Endpoint)
		}
		if err := proxy.AssertLoopbackHost(parsed.Hostname()); err != nil {
			return err
		}
	}
	return nil
}

func unsupportedPluginAdapter(providerID, accountName string) transport.Adapter {
	var result transport.Adapter
	result.SourceRef.Kind = "account"
	result.SourceRef.Provider = providerID
	result.SourceRef.Name = accountName
	result.Capabilities = transport.Capabilities{
		SourceKind: "account", Provider: providerID, NativeClients: []string{}, Protocols: []transport.Protocol{},
	}
	result.TransportFor = func(string) transport.Capability { return transport.Unsupported }
	return result
}

func transportFromRemote(
	host *pluginhost.Host,
	sup *pluginSupervisor,
	gen uint64,
	remote *pluginhost.RemoteObject,
	providerID, accountName string,
	proxyEnabled bool,
) (transport.Adapter, error) {
	result := unsupportedPluginAdapter(providerID, accountName)
	if raw, ok := remote.Properties["sourceRef"]; ok {
		if err := json.Unmarshal(raw, &result.SourceRef); err != nil {
			return transport.Adapter{}, fmt.Errorf("plugin sourceRef: %w", err)
		}
	}
	if raw, ok := remote.Properties["capabilities"]; ok {
		if err := json.Unmarshal(raw, &result.Capabilities); err != nil {
			return transport.Adapter{}, fmt.Errorf("plugin source capabilities: %w", err)
		}
	}
	if result.SourceRef.Kind == "" {
		result.SourceRef.Kind = "account"
	}
	if result.SourceRef.Provider == "" {
		result.SourceRef.Provider = providerID
	}
	if result.SourceRef.Name == "" {
		result.SourceRef.Name = accountName
	}
	if !hasMethod(remote.Methods, "transportFor") {
		return transport.Adapter{}, errors.New("plugin source adapter does not implement transportFor")
	}
	invokeTransportFor := func(host *pluginhost.Host, handle string, clientID string) (transport.Capability, error) {
		var capability transport.Capability
		err := host.Invoke(context.Background(), handle, "transportFor", []any{clientID}, &capability)
		return capability, err
	}
	result.TransportFor = func(clientID string) transport.Capability {
		capability, err := invokeTransportFor(host, remote.Handle, clientID)
		if classifyHostLoss(err) != lossNone {
			// The retained object belonged to a dead generation. The
			// replacement provider rebuilds its own adapter; handles are
			// never replayed against the new host.
			if replacement, recoverErr := sup.replacementProvider(context.Background(), gen, providerID); recoverErr == nil {
				return replacement.AccountAdapter(accountName, proxyEnabled).TransportFor(clientID)
			}
			return transport.Unsupported
		}
		if err != nil {
			return transport.Unsupported
		}
		return validatedCapability(capability)
	}
	if hasMethod(remote.Methods, "transportForPolicy") {
		result.TransportForPolicy = func(clientID string, policy transport.Policy) transport.Capability {
			var capability transport.Capability
			err := host.Invoke(
				context.Background(), remote.Handle, "transportForPolicy", []any{clientID, policy}, &capability,
			)
			if classifyHostLoss(err) != lossNone {
				if replacement, recoverErr := sup.replacementProvider(context.Background(), gen, providerID); recoverErr == nil {
					fresh := replacement.AccountAdapter(accountName, proxyEnabled)
					if fresh.TransportForPolicy != nil {
						return fresh.TransportForPolicy(clientID, policy)
					}
				}
				return transport.Unsupported
			}
			if err != nil {
				return transport.Unsupported
			}
			return validatedCapability(capability)
		}
	}
	return result, nil
}

func validatedCapability(value transport.Capability) transport.Capability {
	switch value {
	case transport.Direct, transport.ManagedBuiltinProxy, transport.ManagedExternalProxy,
		transport.ExternalManualProxy, transport.Unsupported:
		return value
	default:
		return transport.Unsupported
	}
}

type rpcClient struct {
	host *pluginhost.Host
	sup  *pluginSupervisor
	gen  uint64

	descriptor   pluginhost.Descriptor
	id           string
	name         string
	capabilities client.Capabilities
}

func newRPCClient(
	host *pluginhost.Host,
	sup *pluginSupervisor,
	gen uint64,
	descriptor pluginhost.Descriptor,
) (client.Adapter, error) {
	id, err := descriptor.ID()
	if err != nil {
		return nil, err
	}
	name, err := stringProperty(descriptor, "name", true)
	if err != nil {
		return nil, err
	}
	caps := client.Capabilities{ID: id, SupportsPersistentConfig: true}
	if raw, ok := descriptor.Properties["capabilities"]; ok {
		if err := json.Unmarshal(raw, &caps); err != nil {
			return nil, fmt.Errorf("plugin client %s capabilities: %w", id, err)
		}
		caps.ID = id
	}
	if len(caps.AcceptedProtocols) == 0 {
		var styles []string
		if raw, ok := descriptor.Properties["supportedApiStyles"]; ok {
			if err := json.Unmarshal(raw, &styles); err != nil {
				return nil, fmt.Errorf("plugin client %s supportedApiStyles: %w", id, err)
			}
		}
		for _, style := range styles {
			switch style {
			case "openai":
				caps.AcceptedProtocols = append(caps.AcceptedProtocols, transport.OpenAI)
			case "anthropic":
				caps.AcceptedProtocols = append(caps.AcceptedProtocols, transport.Anthropic)
			}
		}
	}
	if caps.ProtocolPreference == "" && len(caps.AcceptedProtocols) > 0 {
		caps.ProtocolPreference = caps.AcceptedProtocols[0]
	}
	base := &rpcClient{host: host, sup: sup, gen: gen, descriptor: descriptor, id: id, name: name, capabilities: caps}
	hasPersistent := hasMethod(descriptor.Methods, "applyPersistent")
	hasEphemeral := false
	if caps.SupportsIsolatedHome {
		if !hasMethod(descriptor.Methods, "listIsolatablePaths") ||
			!hasMethod(descriptor.Methods, "createIsolatedRuntime") {
			return nil, fmt.Errorf(
				"plugin client %s declares isolated-home support without both isolation methods",
				id,
			)
		}
		hasEphemeral = true
	} else if caps.SupportsEnvironmentOverlay {
		if !hasMethod(descriptor.Methods, "createEnvironmentOverlay") {
			return nil, fmt.Errorf(
				"plugin client %s declares environment-overlay support without createEnvironmentOverlay",
				id,
			)
		}
		hasEphemeral = true
	}
	switch {
	case hasPersistent && hasEphemeral:
		return &rpcPersistentEphemeralClient{rpcClient: base}, nil
	case hasPersistent:
		return &rpcPersistentClient{rpcClient: base}, nil
	case hasEphemeral:
		return &rpcEphemeralClient{rpcClient: base}, nil
	default:
		return base, nil
	}
}

func (c *rpcClient) ID() string                         { return c.id }
func (c *rpcClient) Name() string                       { return c.name }
func (c *rpcClient) Capabilities() *client.Capabilities { copy := c.capabilities; return &copy }

// rpcBaseClient mirrors rpcBaseProvider for client wrappers.
type rpcBaseClient interface {
	rpcBase() *rpcClient
}

func (c *rpcClient) rpcBase() *rpcClient { return c }

func rpcClientBaseOf(value client.Adapter) (*rpcClient, bool) {
	base, ok := value.(rpcBaseClient)
	if !ok {
		return nil, false
	}
	return base.rpcBase(), true
}

// withRecovery mirrors rpcProvider.withRecovery: at most one recovery attempt
// per caller, replay only for provably-undispatched calls; an ambiguous loss
// on a mutating hook surfaces ErrUncertainMutation without a second run.
func (c *rpcClient) withRecovery(ctx context.Context, readOnly bool, fn func(*rpcClient) error) error {
	err := fn(c)
	kind := classifyHostLoss(err)
	if kind == lossNone {
		return err
	}
	replacement, recoverErr := c.sup.replacementClient(ctx, c.gen, c.id)
	if recoverErr != nil {
		if kind == lossAmbiguous && !readOnly {
			return fmt.Errorf("%w: %w (recovery: %v)", ErrUncertainMutation, err, recoverErr)
		}
		return errors.Join(err, recoverErr)
	}
	if kind == lossAmbiguous && !readOnly {
		return fmt.Errorf("%w: %w", ErrUncertainMutation, err)
	}
	return fn(replacement)
}

func (c *rpcClient) Apply(input *client.ApplyContext) (*client.ApplyResult, error) {
	var result *client.ApplyResult
	err := c.withRecovery(context.Background(), false, func(rc *rpcClient) error {
		out, err := rc.applyOnce(input)
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	return result, err
}

func (c *rpcClient) applyOnce(input *client.ApplyContext) (*client.ApplyResult, error) {
	pluginContext := c.applyContext(input)
	if hasMethod(c.descriptor.Methods, "validate") {
		if err := c.host.Invoke(context.Background(), c.descriptor.Handle, "validate", []any{pluginContext}, nil); err != nil {
			return nil, err
		}
	}
	if !hasMethod(c.descriptor.Methods, "apply") {
		return nil, fmt.Errorf("plugin client %s does not implement apply", c.id)
	}
	var result client.ApplyResult
	if err := c.host.Invoke(context.Background(), c.descriptor.Handle, "apply", []any{pluginContext}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *rpcClient) applyContext(input *client.ApplyContext) map[string]any {
	return map[string]any{
		"profile": map[string]any{
			"meta": map[string]any{
				"endpoint": input.Endpoint, "defaultModel": input.DefaultModel,
				"models": input.ModelRoles,
			},
			"secrets": map[string]any{"apiKey": input.APIKey},
		},
		"clientId": c.id, "dryRun": input.DryRun, "verbose": input.Verbose,
		"proxyEndpoint": input.ProxyEndpoint, "anypickRoot": input.AnyPickRoot,
		"isolatedHome": input.IsolatedHome,
	}
}

func (c *rpcClient) resolvedPlan(
	endpoint, apiKey, defaultModel string,
	modelRoles map[string]string,
	anypickRoot, sourceKind, sourceProvider, sourceName string,
	capability transport.Capability,
	mode string,
	dryRun, verbose bool,
) map[string]any {
	if sourceKind == "" {
		sourceKind = "gateway"
	}
	ref := map[string]any{"kind": sourceKind}
	if sourceProvider != "" {
		ref["provider"] = sourceProvider
	}
	if sourceName != "" {
		ref["name"] = sourceName
	}
	display := sourceName
	if sourceProvider != "" {
		display = sourceProvider + "/" + sourceName
	}
	protocol := c.capabilities.ProtocolPreference
	if protocol == "" && len(c.capabilities.AcceptedProtocols) > 0 {
		protocol = c.capabilities.AcceptedProtocols[0]
	}
	if capability == "" {
		capability = transport.Direct
	}
	model := map[string]any{"mode": "omitted"}
	if defaultModel != "" {
		model = map[string]any{"mode": "explicit", "id": defaultModel}
	}
	profile := map[string]any{
		"meta": map[string]any{
			"name": sourceName, "provider": sourceProvider, "endpoint": endpoint,
			"defaultModel": defaultModel, "models": modelRoles,
		},
		"secrets": map[string]any{"apiKey": apiKey},
	}
	plan := map[string]any{
		"clientId": c.id,
		"source": map[string]any{
			"ref": ref, "kind": sourceKind, "display": display,
		},
		"transport": map[string]any{
			"capability": capability, "protocol": protocol, "endpoint": endpoint,
		},
		"model": model, "mode": mode, "profile": profile,
		"dryRun": dryRun, "verbose": verbose, "anypickRoot": anypickRoot,
	}
	if sourceKind == "account" && sourceProvider != "" && sourceName != "" {
		plan["account"] = map[string]any{
			"meta":        map[string]any{"provider": sourceProvider, "name": sourceName},
			"snapshotDir": paths.AccountSnapshotDir(anypickRoot, sourceProvider, sourceName),
			"accountDir":  paths.AccountDir(anypickRoot, sourceProvider, sourceName),
		}
	}
	return plan
}

type rpcPersistentClient struct{ *rpcClient }

func (c *rpcPersistentClient) ApplyPersistent(
	ctx context.Context,
	input *client.ApplyContext,
) (*client.ApplyResult, error) {
	return c.rpcClient.applyPersistent(ctx, input)
}

type rpcEphemeralClient struct{ *rpcClient }

func (c *rpcEphemeralClient) CreateEphemeralRuntime(
	ctx context.Context,
	input *client.EphemeralContext,
) (*client.IsolatedRuntime, error) {
	return c.rpcClient.createEphemeralRuntime(ctx, input)
}

type rpcPersistentEphemeralClient struct{ *rpcClient }

func (c *rpcPersistentEphemeralClient) ApplyPersistent(
	ctx context.Context,
	input *client.ApplyContext,
) (*client.ApplyResult, error) {
	return c.rpcClient.applyPersistent(ctx, input)
}

func (c *rpcPersistentEphemeralClient) CreateEphemeralRuntime(
	ctx context.Context,
	input *client.EphemeralContext,
) (*client.IsolatedRuntime, error) {
	return c.rpcClient.createEphemeralRuntime(ctx, input)
}

func (c *rpcClient) applyPersistent(
	ctx context.Context,
	input *client.ApplyContext,
) (*client.ApplyResult, error) {
	var result *client.ApplyResult
	err := c.withRecovery(ctx, false, func(rc *rpcClient) error {
		out, err := rc.applyPersistentOnce(ctx, input)
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	return result, err
}

func (c *rpcClient) applyPersistentOnce(
	ctx context.Context,
	input *client.ApplyContext,
) (*client.ApplyResult, error) {
	var result client.ApplyResult
	if err := c.host.InvokeWithSignal(
		ctx,
		c.descriptor.Handle,
		"applyPersistent",
		[]any{c.resolvedPlan(
			input.Endpoint,
			input.APIKey,
			input.DefaultModel,
			input.ModelRoles,
			input.AnyPickRoot,
			input.SourceKind,
			input.SourceProvider,
			input.SourceName,
			input.TransportCapability,
			firstNonEmptyPluginString(input.Mode, "persistent"),
			input.DryRun,
			input.Verbose,
		)},
		0,
		&result,
	); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *rpcClient) createEphemeralRuntime(
	ctx context.Context,
	input *client.EphemeralContext,
) (*client.IsolatedRuntime, error) {
	var runtime *client.IsolatedRuntime
	// Creating an isolated runtime mutates the filesystem through the plugin:
	// an ambiguous host loss must never re-run it against a new generation.
	err := c.withRecovery(ctx, false, func(rc *rpcClient) error {
		out, err := rc.createEphemeralRuntimeOnce(ctx, input)
		if err != nil {
			return err
		}
		runtime = out
		return nil
	})
	return runtime, err
}

func (c *rpcClient) createEphemeralRuntimeOnce(
	ctx context.Context,
	input *client.EphemeralContext,
) (*client.IsolatedRuntime, error) {
	plan := c.resolvedPlan(
		input.Endpoint,
		input.APIKey,
		input.DefaultModel,
		input.ModelRoles,
		input.AnyPickRoot,
		input.SourceKind,
		input.SourceProvider,
		input.SourceName,
		input.TransportCapability,
		"ephemeral",
		false,
		false,
	)
	var remote *pluginhost.RemoteObject
	var err error
	if c.capabilities.SupportsIsolatedHome {
		var paths []client.IsolatablePath
		if err := c.host.InvokeWithSignal(
			ctx,
			c.descriptor.Handle,
			"listIsolatablePaths",
			[]any{map[string]any{"home": input.Home}},
			0,
			&paths,
		); err != nil {
			return nil, err
		}
		for _, path := range paths {
			if path.SourcePath == "" || path.DestinationPath == "" ||
				(path.Kind != "file" && path.Kind != "directory") {
				return nil, fmt.Errorf("plugin client %s returned an invalid isolatable path", c.id)
			}
		}
		remote, err = c.host.InvokeObjectWithSignal(
			ctx,
			c.descriptor.Handle,
			"createIsolatedRuntime",
			[]any{plan, paths},
			0,
		)
	} else {
		remote, err = c.host.InvokeObjectWithSignal(
			ctx,
			c.descriptor.Handle,
			"createEnvironmentOverlay",
			[]any{plan},
			0,
		)
	}
	if err != nil {
		return nil, err
	}
	if !hasMethod(remote.Methods, "cleanup") {
		return nil, fmt.Errorf("plugin client %s isolated runtime has no cleanup method", c.id)
	}
	directory, err := remoteStringProperty(remote, "directory", true)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("plugin client %s isolated runtime directory must be absolute", c.id)
	}
	environment := map[string]string{}
	if err := decodeRemoteProperty(remote, "environment", &environment); err != nil {
		return nil, fmt.Errorf("plugin client %s isolated runtime environment: %w", c.id, err)
	}
	var args []string
	if err := decodeRemoteProperty(remote, "args", &args); err != nil {
		return nil, fmt.Errorf("plugin client %s isolated runtime args: %w", c.id, err)
	}
	home := environment["HOME"]
	if home == "" {
		home = directory
	}
	return client.NewIsolatedRuntime(
		directory,
		home,
		environment,
		args,
		func() error {
			// The cleanup handle belongs to the generation that created it.
			// No recovery: if that host died, the retained object died with
			// it and a replacement has nothing to dispose.
			return c.host.Invoke(
				context.Background(),
				remote.Handle,
				"cleanup",
				[]any{},
				nil,
			)
		},
	), nil
}

func (c *rpcClient) Reset() error {
	return c.withRecovery(context.Background(), false, func(rc *rpcClient) error {
		if !hasMethod(rc.descriptor.Methods, "reset") {
			return fmt.Errorf("plugin client %s does not implement reset", rc.id)
		}
		state := map[string]any{
			"clientId": rc.id, "mode": "none", "updatedAt": "",
			"managedPaths": []string{}, "managedEnvKeys": []string{},
		}
		return rc.host.Invoke(context.Background(), rc.descriptor.Handle, "reset", []any{state}, nil)
	})
}

func (c *rpcClient) Inspect() (*client.InspectResult, error) {
	var result *client.InspectResult
	err := c.withRecovery(context.Background(), true, func(rc *rpcClient) error {
		out, err := rc.inspectOnce()
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	return result, err
}

func (c *rpcClient) inspectOnce() (*client.InspectResult, error) {
	if !hasMethod(c.descriptor.Methods, "inspect") {
		return nil, fmt.Errorf("plugin client %s does not implement inspect", c.id)
	}
	var remote struct {
		Present     bool     `json:"present"`
		Summary     string   `json:"summary"`
		Issues      []string `json:"issues"`
		ConfigPaths []string `json:"configPaths"`
	}
	if err := c.host.Invoke(context.Background(), c.descriptor.Handle, "inspect", []any{}, &remote); err != nil {
		return nil, err
	}
	return &client.InspectResult{
		Present: remote.Present, Summary: remote.Summary, Issues: remote.Issues,
		ConfigPaths: remote.ConfigPaths,
	}, nil
}

func catalogFromDescriptor(descriptor pluginhost.Descriptor) (catalog.Provider, error) {
	id, err := descriptor.ID()
	if err != nil {
		return catalog.Provider{}, err
	}
	name, err := stringProperty(descriptor, "name", true)
	if err != nil {
		return catalog.Provider{}, err
	}
	description, err := stringProperty(descriptor, "description", true)
	if err != nil {
		return catalog.Provider{}, err
	}
	styleValue, err := stringProperty(descriptor, "apiStyle", true)
	if err != nil {
		return catalog.Provider{}, err
	}
	style := catalog.APIStyle(styleValue)
	if style != catalog.StyleOpenAI && style != catalog.StyleAnthropic && style != catalog.StyleCustom {
		return catalog.Provider{}, fmt.Errorf("plugin catalog %s has invalid apiStyle %q", id, style)
	}
	result := catalog.Provider{ID: id, Name: name, Description: description, APIStyle: style}
	if err := decodeOptionalProperty(descriptor, "protocols", &result.Protocols); err != nil {
		return catalog.Provider{}, fmt.Errorf("plugin catalog %s protocols: %w", id, err)
	}
	if err := decodeOptionalProperty(descriptor, "defaultEndpoint", &result.DefaultEndpoint); err != nil {
		return catalog.Provider{}, fmt.Errorf("plugin catalog %s defaultEndpoint: %w", id, err)
	}
	if err := decodeOptionalProperty(descriptor, "firstPartyClientIds", &result.FirstPartyClientIDs); err != nil {
		return catalog.Provider{}, fmt.Errorf("plugin catalog %s firstPartyClientIds: %w", id, err)
	}
	if len(result.Protocols) == 0 {
		if style == catalog.StyleAnthropic {
			result.Protocols = []transport.Protocol{transport.Anthropic}
		} else if style == catalog.StyleOpenAI {
			result.Protocols = []transport.Protocol{transport.OpenAI}
		} else {
			result.Protocols = []transport.Protocol{transport.OpenAI, transport.Anthropic}
		}
	}
	return result, nil
}

func catalogFromCompatible(definition provider.CompatibleDefinition) catalog.Provider {
	result := catalog.Provider{
		ID: definition.ID, Name: definition.Name, Description: definition.Description,
		APIStyle:  catalog.StyleOpenAI,
		Protocols: []transport.Protocol{transport.OpenAI, transport.Anthropic},
	}
	if len(definition.Credentials) > 0 && len(definition.Credentials[0].Profiles) > 0 {
		profile := definition.Credentials[0].Profiles[0]
		result.DefaultEndpoint = profile.OpenAIBase
		result.AnthropicEndpoint = profile.AnthropicBase
	}
	return result
}

func stringProperty(descriptor pluginhost.Descriptor, name string, required bool) (string, error) {
	raw, ok := descriptor.Properties[name]
	if !ok {
		if required {
			return "", fmt.Errorf("plugin %s %q has no %s property", descriptor.Kind, descriptor.Handle, name)
		}
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || required && value == "" {
		return "", fmt.Errorf("plugin %s %q has invalid %s property", descriptor.Kind, descriptor.Handle, name)
	}
	return value, nil
}

func decodeOptionalProperty(descriptor pluginhost.Descriptor, name string, output any) error {
	raw, ok := descriptor.Properties[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, output)
}

func remoteStringProperty(object *pluginhost.RemoteObject, name string, required bool) (string, error) {
	if object == nil {
		return "", errors.New("plugin returned no retained object")
	}
	raw, ok := object.Properties[name]
	if !ok {
		if required {
			return "", fmt.Errorf("plugin object %q has no %s property", object.Handle, name)
		}
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || required && value == "" {
		return "", fmt.Errorf("plugin object %q has invalid %s property", object.Handle, name)
	}
	return value, nil
}

func decodeRemoteProperty(object *pluginhost.RemoteObject, name string, output any) error {
	if object == nil {
		return errors.New("plugin returned no retained object")
	}
	raw, ok := object.Properties[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, output)
}

func firstNonEmptyPluginString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func hasMethod(methods []string, method string) bool { return slices.Contains(methods, method) }

package activation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/transport"
)

// CatalogEntry is the catalog metadata a gateway needs for classification
// (CatalogRegistry essence). Keyed by provider id.
type CatalogEntry struct {
	Name      string
	APIStyle  string // 'openai' | 'anthropic' | 'custom'
	Protocols []transport.Protocol
}

// Deps carries everything the planner and executor need.
type Deps struct {
	Root      string
	Accounts  *accountsView
	Providers *provider.Registry
	Clients   *client.Registry
	// ClientProfiles owns named native route mappings. It is separate from the
	// one-row-per-client global binding.
	ClientProfiles *client.ProfileStore
	Bindings       *bindings.Service
	Presets        *presetsView
	Profiles       *profile.Service
	Pools          *poolsView
	Journal        *journal.OperationJournal
	Runtime        *runtime.Service // optional; nil degrades proxy/gateway applies
	Proxy          proxyStart       // optional; nil makes managed transports fail closed
	Catalog        *CatalogIndex
	// Hub enables Proxy Hub sources; SpawnHubChild launches the detached
	// `_serve-hub <name>` helper during execution. nil → hub sources fail closed.
	Hub           *hub.Service
	SpawnHubChild func(hubName string) (int, error)
}

// proxyStart is the slice of ProxyService the executor needs. Declared here so
// compositions without a proxy service fail closed at StartProxy instead of
// silently skipping a mutating step.
type proxyStart interface {
	StartProxy(ctx context.Context, providerID, accountName string) (*proxy.StartResult, error)
	StopIfLeaseOwned(ctx context.Context, providerID, accountName, instanceID, leaseID string) error
	// Pool sources route through the shared provider pool lifecycle.
	StartPoolProxy(ctx context.Context, providerID string) (*proxy.StartResult, error)
	StopPoolProxy(ctx context.Context, providerID string) error
}

// Options mirrors PlanOptions.
type Options struct {
	DryRun          bool
	SavePreset      string
	ProjectRoot     string
	ModelRoles      map[string]string
	TransportPolicy transport.Policy
	ReapplySpec     *bindings.Spec
	ReapplyProv     *bindings.Provenance
	// SkipNativeAuthWrite builds a restore-only plan: start the proxy and
	// rewrite client config, but never touch the provider's live credential
	// files. Desktop rehydration uses it so a service restart cannot silently
	// replace a login the user changed after the binding was created.
	SkipNativeAuthWrite bool
}

func protocolsFromAPIStyle(style string) []transport.Protocol {
	switch style {
	case "openai":
		return []transport.Protocol{transport.OpenAI}
	case "anthropic":
		return []transport.Protocol{transport.Anthropic}
	default:
		return []transport.Protocol{transport.OpenAI, transport.Anthropic}
	}
}

// MaterializeResolvedSource resolves a ref into the concrete source a plan needs.
func MaterializeResolvedSource(ctx context.Context, ref refs.Ref, deps *Deps) (*ResolvedSource, error) {
	display := refs.DisplayRef(ref)
	switch ref.Kind {
	case refs.KindAccount:
		account, err := deps.Accounts.Get(ctx, ref.Provider, ref.Name)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, anyerr.Newf(
				"Account `"+ref.Provider+"/"+ref.Name+"` was not found.",
				"ACCOUNT_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound),
				anyerr.WithSuggestions(
					"anypick add account "+ref.Provider+" --current --name "+ref.Name,
					"anypick list accounts"))
		}
		pv, err := deps.Providers.Get(ref.Provider)
		if err != nil {
			return nil, err
		}
		adapter := pv.AccountAdapter(account.name, account.proxyEnabled)
		return &ResolvedSource{Ref: ref, Kind: "account", Adapter: adapter, Display: display}, nil

	case refs.KindAccountPool:
		pv, err := deps.Providers.Get(ref.Provider)
		if err != nil {
			return nil, err
		}
		info := providerInfoOf(pv)
		if !info.CanProxy {
			return nil, anyerr.Newf(
				"Provider "+ref.Provider+" has no proxy — cannot use pool:"+ref.Provider+".",
				"PROXY_UNSUPPORTED", anyerr.WithExitCode(anyerr.CapabilityConflict))
		}
		pool, err := deps.Pools.GetContext(ctx, ref.Provider)
		if err != nil {
			return nil, err
		}
		if pool == nil || pool.Mode != "multi" {
			return nil, anyerr.Newf(
				"Multi-account pool is not enabled for "+ref.Provider+".\n\nEnable it:\n  anypick proxy pool enable "+ref.Provider+"\n\nOr bind a single account:\n  anypick use <client> --with "+ref.Provider+"/<name>",
				"POOL_NOT_ENABLED",
				anyerr.WithExitCode(anyerr.InvalidUsage),
				anyerr.WithSuggestions(
					"anypick proxy pool enable "+ref.Provider,
					"anypick use claude --with "+ref.Provider+"/work"))
		}
		// Providers own their pool transport: kiro serves pools through the
		// external kirolink bridge, everything else through the builtin proxy.
		if pa, ok := pv.(interface{ PoolAdapter() transport.Adapter }); ok {
			return &ResolvedSource{Ref: ref, Kind: "account", Adapter: pa.PoolAdapter(), Display: display}, nil
		}
		adapter := transport.PoolAdapterFor(ref.Provider, info, transport.ManagedBuiltinProxy)
		return &ResolvedSource{Ref: ref, Kind: "account", Adapter: adapter, Display: display}, nil

	case refs.KindProxyHub:
		return resolveHubSource(ctx, deps, ref, display)

	case refs.KindGateway:
		pr, err := deps.Profiles.GetContext(ctx, ref.Name)
		if err != nil {
			return nil, err
		}
		if pr == nil {
			return nil, anyerr.Newf("Gateway `"+ref.Name+"` was not found.", "GATEWAY_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound),
				anyerr.WithSuggestions(
					"Accounts use provider/name:  anypick use claude --with grok/work",
					"Gateways use their saved name:  anypick use claude --with openrouter-work"))
		}
		entry, _ := deps.Catalog.Lookup(pr.Meta.Provider)
		protocols := entry.Protocols
		if len(protocols) == 0 {
			protocols = protocolsFromAPIStyle(entry.APIStyle)
		}
		adapter := gatewayAdapter(ref.Name, pr.Meta.Provider, protocols, deps)
		return &ResolvedSource{Ref: ref, Kind: "gateway", Adapter: adapter, Display: display}, nil

	default:
		return nil, anyerr.Newf("Internal error: preset refs must be expanded before materialize.",
			"INVALID_USAGE", anyerr.WithExitCode(anyerr.InvalidUsage))
	}
}

// resolveHubSource validates an enabled Proxy Hub read-only. Planning never
// mutates state: the deterministic port assignment and the per-client route
// attach happen in the executor (startHubProxy) under the hub lock with
// rollback — planned → journalled → executed (invariant #3).
func resolveHubSource(ctx context.Context, deps *Deps, ref refs.Ref, display string) (*ResolvedSource, error) {
	if deps.Hub == nil || deps.SpawnHubChild == nil {
		return nil, anyerr.Newf(
			"Proxy Hub service is unavailable in this composition.",
			"HUB_UNAVAILABLE", anyerr.WithExitCode(anyerr.CapabilityConflict))
	}
	cfg, err := deps.Hub.GetConfigContext(ctx, ref.Name)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, anyerr.Newf(
			"Proxy Hub `"+ref.Name+"` is not enabled.\n\nEnable it:\n  anypick proxy hub enable "+ref.Name,
			"HUB_NOT_ENABLED",
			anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithSuggestions("anypick proxy hub enable "+ref.Name))
	}

	protocols := []transport.Protocol{transport.OpenAI, transport.Anthropic}
	a := transport.Adapter{}
	a.SourceRef.Kind = string(refs.KindProxyHub)
	a.SourceRef.Name = ref.Name
	a.Capabilities = transport.Capabilities{
		SourceKind:    "proxy-hub",
		Provider:      "proxy-hub",
		NativeClients: []string{},
		Protocols:     protocols,
	}
	a.TransportFor = func(cadID string) transport.Capability {
		cad, gerr := deps.Clients.Get(cadID)
		if gerr != nil {
			return transport.Unsupported
		}
		for _, p := range cad.Capabilities().AcceptedProtocols {
			for _, hp := range protocols {
				if p == hp {
					// The hub IS the local managed proxy.
					return transport.ManagedBuiltinProxy
				}
			}
		}
		return transport.Unsupported
	}
	return &ResolvedSource{
		Ref: ref, Kind: "proxy-hub", Adapter: a, Display: display,
	}, nil
}

// gatewayAdapter ports gatewayAdapterFromProfile: direct only when the client
// exists AND its accepted protocols overlap; never a core-maintained id matrix.
func gatewayAdapter(name, providerID string, protocols []transport.Protocol, deps *Deps) transport.Adapter {
	a := transport.Adapter{}
	a.SourceRef.Kind = string(refs.KindGateway)
	a.SourceRef.Name = name
	a.Capabilities = transport.Capabilities{
		SourceKind:              "gateway",
		Provider:                providerID,
		NativeClients:           []string{},
		Protocols:               protocols,
		CanRefresh:              false,
		SupportsModelDiscovery:  false,
		RequiresNativeAuthWrite: false,
	}
	a.TransportFor = func(clientID string) transport.Capability {
		cad, err := deps.Clients.Get(clientID)
		if err != nil {
			return transport.Unsupported
		}
		accepted := cad.Capabilities().AcceptedProtocols
		for _, p := range protocols {
			for _, ap := range accepted {
				if p == ap {
					return transport.Direct
				}
			}
		}
		return transport.Unsupported
	}
	return a
}

func providerInfoOf(pv provider.Provider) transport.ProviderInfo {
	return transport.ProviderInfo{ID: pv.ID(), CanProxy: pv.CanProxy(), HasAdapter: true}
}

// ExpandPreset expands a named preset into its source and binding for clientID.
func ExpandPreset(ctx context.Context, name, clientID string, deps *Deps) (*presetExpansion, error) {
	preset, err := deps.Presets.GetContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if preset == nil {
		return nil, anyerr.Newf("Preset `@"+name+"` was not found.", "PRESET_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound), anyerr.WithSuggestions("anypick list presets"))
	}
	if preset.Spec.Client != clientID {
		presetClientName := preset.Spec.Client
		wantClientName := clientID
		if cad, err := deps.Clients.Get(preset.Spec.Client); err == nil {
			presetClientName = cad.Name()
		}
		if cad, err := deps.Clients.Get(clientID); err == nil {
			wantClientName = cad.Name()
		}
		return nil, anyerr.Newf(
			fmt.Sprintf("Preset @%s is for %s, not %s.\n\nTry:\n  anypick run %s --with @%s",
				name, presetClientName, wantClientName, preset.Spec.Client, name),
			"PRESET_CLIENT_MISMATCH",
			anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithSuggestions("anypick run "+preset.Spec.Client+" --with @"+name))
	}
	sourceRef := preset.Spec.Source
	if sourceRef.Kind == refs.KindPreset {
		return nil, anyerr.Newf("Nested presets are not supported.", "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	source, err := MaterializeResolvedSource(ctx, sourceRef, deps)
	if err != nil {
		return nil, err
	}
	spec := preset.Spec
	spec.Client = clientID
	return &presetExpansion{
		presetID:       preset.ID,
		presetName:     preset.Name,
		presetRevision: preset.Revision,
		source:         source,
		model:          preset.Spec.Model,
		bindingSpec:    spec,
	}, nil
}

// PlanActivation builds the activation plan for req without mutating state.
func PlanActivation(ctx context.Context, req Request, deps *Deps, opts Options) (*Plan, error) {
	clientID := req.Client
	cad, err := deps.Clients.Get(clientID)
	if err != nil {
		return nil, anyerr.Newf("Unknown client \""+clientID+".\"", "CLIENT_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound),
			anyerr.WithSuggestions("Known clients: "+joinIDs(deps.Clients)))
	}
	caps := cad.Capabilities()
	if req.Mode == ModeProfile || req.ProfileName != "" {
		if clientID != "codex" {
			return nil, anyerr.Newf(
				"Named client profiles are currently supported only for Codex.",
				"UNSUPPORTED_PROFILE", anyerr.WithExitCode(anyerr.CapabilityConflict))
		}
		if err := client.ValidateCodexProfileName(req.ProfileName); err != nil {
			return nil, anyerr.Newf(err.Error(), "INVALID_USAGE",
				anyerr.WithExitCode(anyerr.InvalidUsage))
		}
	}

	var steps []PlanStep
	warnings := []Warning{}

	// There is deliberately no planned-rollback list beside `steps`. The
	// planner used to fill a Plan.Rollback that nothing outside tests ever read,
	// so it asserted compensations the executor never performed. Real
	// compensation is the executor's undo stack run in reverse, plus the
	// journal replay in recovery.go; a second description of it could only
	// drift from the first.

	var resolved *ResolvedSource
	var model bindings.ModelSelection
	var bindingSpec *bindings.Spec
	projectRoot := opts.ProjectRoot
	if projectRoot == "" {
		projectRoot = req.ProjectRoot
	}
	provenance := &bindings.Provenance{Kind: "direct"}

	switch {
	case opts.ReapplySpec != nil:
		sourceRef := opts.ReapplySpec.Source
		if sourceRef.Kind == refs.KindPreset {
			return nil, anyerr.Newf("Stored binding has invalid preset source pointer.",
				"STATE_CONFLICT", anyerr.WithExitCode(anyerr.CapabilityConflict))
		}
		steps = append(steps, PlanStep{
			Kind:   StepResolveSource,
			Detail: "Re-apply snapshot " + refs.DisplayRef(sourceRef),
			Params: map[string]interface{}{"ref": refs.SerializeRef(sourceRef)},
		})
		resolved, err = MaterializeResolvedSource(ctx, sourceRef, deps)
		if err != nil {
			return nil, err
		}
		model = opts.ReapplySpec.Model
		specCopy := opts.ReapplySpec
		bindingSpec = specCopy
		provenance = opts.ReapplyProv

	case req.Preset != "" || (req.Source != nil && req.Source.Kind == refs.KindPreset):
		presetName := req.Preset
		if presetName == "" && req.Source != nil {
			presetName = req.Source.Name
		}
		if presetName == "" {
			return nil, anyerr.Newf("Preset name required.", "INVALID_USAGE",
				anyerr.WithExitCode(anyerr.InvalidUsage))
		}
		steps = append(steps, PlanStep{
			Kind: StepExpandPreset, Detail: "@" + presetName,
			Params: map[string]interface{}{"preset": presetName},
		})
		expanded, err := ExpandPreset(ctx, presetName, clientID, deps)
		if err != nil {
			return nil, err
		}
		resolved = expanded.source
		model = modelFromRequest(req.Model, expanded.model)
		spec := expanded.bindingSpec
		spec.Model = model
		spec.ClientOptions = mergeModelRolesIntoClientOptions(
			spec.ClientOptions, firstNonNilMap(req.ModelRoles, opts.ModelRoles), model)
		model = modelFromRolesOrSelection(spec, model)
		spec.Model = model
		bindingSpec = &spec
		provenance = &bindings.Provenance{
			Kind:                     "preset_snapshot",
			PresetID:                 expanded.presetID,
			PresetNameAtSnapshot:     expanded.presetName,
			PresetRevisionAtSnapshot: expanded.presetRevision,
		}

	case req.Source != nil:
		sourceRef := *req.Source
		steps = append(steps, PlanStep{
			Kind:   StepResolveSource,
			Detail: refs.DisplayRef(sourceRef),
			Params: map[string]interface{}{"ref": refs.SerializeRef(sourceRef)},
		})
		resolved, err = MaterializeResolvedSource(ctx, sourceRef, deps)
		if err != nil {
			return nil, err
		}
		model = modelFromRequest(req.Model, bindings.OmittedModel())
		spec := bindings.Spec{
			Client:          clientID,
			Source:          sourceRef,
			Model:           model,
			TransportPolicy: "auto",
		}
		explicit := firstNonNilMap(req.ModelRoles, opts.ModelRoles)
		var base map[string]interface{}
		marker := ""
		if len(explicit) == 0 {
			// No per-connection choice: inherit stored provider defaults so
			// one setting covers every account of the provider. Explicit
			// pins always win and are never merged over.
			if copied, ok := providerModelRoleDefaults(ctx, deps, clientID, sourceRef, req.ModelCatalog); ok {
				base = map[string]interface{}{"modelRoles": rolesToStringInterface(copied)}
				marker = "provider"
			}
		} else {
			marker = "explicit"
		}
		spec.ClientOptions = mergeModelRolesIntoClientOptions(base, explicit, model)
		if marker != "" {
			spec.ClientOptions["modelRolesSource"] = marker
		}
		model = modelFromRolesOrSelection(spec, model)
		spec.Model = model
		bindingSpec = &spec

	default:
		return nil, anyerr.Newf("A source or --current is required.", "MISSING_SOURCE",
			anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithSuggestions(
				"anypick use "+clientID+" --with <source>",
				"anypick use "+clientID+" --current"))
	}

	// Some clients cannot represent a connection until their native config
	// contains a default model. Prefer a model explicitly selected by the user;
	// otherwise choose from the exact source-scoped catalog. A provider-level
	// first model can be unavailable to this entitlement and would make the
	// client send a model that the selected proxy cannot serve.
	if caps != nil && caps.RequiresDefaultModel && model.Mode != "explicit" {
		fallback := bootstrapModelForSource(ctx, resolved, deps, req.ModelCatalog)
		if fallback == "" {
			return nil, modelRequiredError(clientID, resolved, req.ModelCatalogErr)
		}
		model = bindings.ExplicitModel(fallback)
		bindingSpec.Model = model
		bindingSpec.ClientOptions = mergeModelRolesIntoClientOptions(
			bindingSpec.ClientOptions, nil, model)
	}

	transportPolicy := bindingSpec.TransportPolicy
	if req.TransportPolicy != "" {
		transportPolicy = string(req.TransportPolicy)
	} else if opts.TransportPolicy != "" {
		transportPolicy = string(opts.TransportPolicy)
	}
	bindingSpec.TransportPolicy = transportPolicy

	steps = append(steps,
		PlanStep{Kind: StepValidateCompatibility, Detail: clientID + " × " + resolved.Display},
		PlanStep{Kind: StepValidateCredential},
	)

	capability, err := transport.ResolveTransportPolicy(resolved.Adapter, clientID, transport.Policy(transportPolicy))
	if err != nil {
		return nil, anyerr.Newf(err.Error(), "UNSUPPORTED_TRANSPORT",
			anyerr.WithExitCode(anyerr.CapabilityConflict))
	}
	steps = append(steps, PlanStep{
		Kind:   StepResolveTransport,
		Detail: string(capability),
		Params: map[string]interface{}{"capability": string(capability), "policy": transportPolicy},
	})

	if capability == transport.Unsupported {
		return nil, anyerr.Newf(
			"Source "+resolved.Display+" cannot be used with client "+clientID+" (unsupported transport).",
			"UNSUPPORTED_TRANSPORT",
			anyerr.WithExitCode(anyerr.CapabilityConflict),
			anyerr.WithSuggestions("Try a different source or client.", "anypick list"))
	}
	if capability == transport.ExternalManualProxy {
		steps = append(steps, PlanStep{
			Kind: StepValidateExternalDep, Detail: "external proxy executable required",
		})
	}

	account, err := deps.Accounts.loadForRef(ctx, resolved.Ref)
	if err != nil {
		return nil, err
	}
	pr, err := deps.Profiles.GetContext(ctx, loadProfileName(resolved.Ref))
	if err != nil {
		return nil, err
	}

	tr := buildTransport(caps, resolved, capability, account, pr)
	if resolved.Ref.Kind == refs.KindAccountPool && tr.ManagedProxy != nil {
		// Plan/actual port parity (proxy-service.ts startPoolProxy): the
		// executor binds pool.port ?? provider.defaultProxyPort ?? 8080
		// deterministically, so the plan must advertise exactly that endpoint.
		if port, perr := plannedPoolPort(ctx, resolved.Ref.Provider, deps); perr == nil && port > 0 {
			tr.ManagedProxy.Port = port
			tr.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
	}
	nativeAuthSnapshotDir := ""
	// A saved Codex account can safely run directly in an isolated home. Carry
	// its auth.json for both persistent profile materialization and
	// `run --profile`; other providers' snapshots belong to their proxies and
	// are not Codex auth.json files.
	if req.ProfileName != "" && resolved.Ref.Kind == refs.KindAccount &&
		resolved.Ref.Provider == "codex" && account != nil {
		nativeAuthSnapshotDir = account.snapshotDir
	}
	if resolved.Kind == "proxy-hub" {
		// The hub is itself the local managed proxy. Endpoint/token are
		// filled in by startHubProxy during execution — planning stays pure.
		tr.ManagedProxy = &ManagedProxyRef{
			Provider: "proxy-hub", Account: resolved.Ref.Name,
			Port: 0, LeaseID: "pending", Token: "",
		}
	}
	planWarningsFromModel(&model, &warnings)

	// Proxy steps: only persistent use and ephemeral run own shared lifecycle.
	if (req.Mode == ModePersistent || req.Mode == ModeEphemeral || req.Mode == ModeProfile) &&
		(capability == transport.ManagedBuiltinProxy || capability == transport.ManagedExternalProxy) {
		steps = append(steps,
			PlanStep{Kind: StepAllocateProxyLease, Params: map[string]interface{}{"port": tr.ManagedProxy.Port}},
			PlanStep{Kind: StepStartProxy},
			PlanStep{Kind: StepWaitForHealth},
		)
	}

	// Ephemeral safety gate (invariant: ephemeral must never switch live login).
	if req.Mode == ModeEphemeral && req.ProfileName == "" && resolved.Kind == "account" &&
		resolved.Adapter.Capabilities.RequiresNativeAuthWrite &&
		capability == transport.Direct {
		return nil, anyerr.Newf(
			"Safe ephemeral execution is not available for "+resolved.Display+" × "+clientID+
				": this source requires live native auth. Use `anypick use` first or add an isolated-auth adapter.",
			"UNSUPPORTED_TRANSPORT", anyerr.WithExitCode(anyerr.CapabilityConflict))
	}

	// Persistent activation is the only mode allowed to change native auth.
	// Deliberate divergence from TS (activation-planner.ts gated this on
	// capability == direct): the step runs for every persistent account
	// source whose adapter requires a native auth write, regardless of
	// transport, so quota/usage and direct CLI use agree with the binding.
	// Adapters whose proxy serves purely from the snapshot (e.g. grok, whose
	// proxy refreshes per request from saved auth) declare
	// RequiresNativeAuthWrite=false and stay excluded here.
	if req.Mode == ModePersistent && resolved.Kind == "account" &&
		resolved.Adapter.Capabilities.RequiresNativeAuthWrite && !opts.SkipNativeAuthWrite {
		steps = append(steps, PlanStep{
			Kind:   StepWriteNativeAuth,
			Detail: resolved.Display,
			Params: map[string]interface{}{"ref": refs.SerializeRef(resolved.Ref)},
		})
	}

	switch req.Mode {
	case ModeEphemeral:
		if caps.SupportsIsolatedHome {
			steps = append(steps,
				PlanStep{Kind: StepCreateTempClientHome},
				PlanStep{Kind: StepSpawnChild})
		} else if caps.SupportsEnvironmentOverlay {
			steps = append(steps, PlanStep{Kind: StepCreateEnvironmentOverlay})
		} else {
			return nil, anyerr.Newf(
				"Client "+clientID+" cannot run ephemerally: no isolated home or environment overlay support.",
				"UNSUPPORTED_TRANSPORT", anyerr.WithExitCode(anyerr.CapabilityConflict))
		}
	case ModeProject:
		steps = append(steps, PlanStep{
			Kind:   StepCommitProjectBinding,
			Params: map[string]interface{}{"projectRoot": projectRoot},
		})
	case ModeProfile:
		steps = append(steps,
			PlanStep{Kind: StepInspectClientState},
			PlanStep{Kind: StepWriteClientConfig},
			PlanStep{Kind: StepVerifyEffectiveState},
			PlanStep{Kind: StepCommitClientProfile,
				Params: map[string]interface{}{"profile": req.ProfileName}},
		)
	default: // persistent
		steps = append(steps,
			PlanStep{Kind: StepInspectClientState},
			PlanStep{Kind: StepWriteClientConfig},
			PlanStep{Kind: StepVerifyEffectiveState},
			PlanStep{Kind: StepCommitGlobalBinding},
		)
	}

	// Gateway/hub sources must never carry WriteNativeAuth.
	if resolved.Kind != "account" {
		for _, s := range steps {
			if s.Kind == StepWriteNativeAuth {
				return nil, fmt.Errorf("internal planner error: WriteNativeAuth for %s", resolved.Kind)
			}
		}
	}

	planWarningsFromUnpinnedModel(clientID, resolved, model,
		firstNonNilMap(req.ModelRoles, opts.ModelRoles), &warnings)

	return &Plan{
		Mode:                  req.Mode,
		Client:                clientID,
		ProfileName:           req.ProfileName,
		ProjectRoot:           projectRoot,
		NativeAuthSnapshotDir: nativeAuthSnapshotDir,
		ResolvedSource:        resolved,
		Transport:             tr,
		Model:                 model,
		ModelCatalog:          append([]string(nil), req.ModelCatalog...),
		Steps:                 steps,
		Warnings:              warnings,
		BindingSpec:           bindingSpec,
		Provenance:            provenance,
	}, nil
}

func fallbackModelForProvider(deps *Deps, providerID string) string {
	if providerID == "" {
		return ""
	}
	if pol := catalog.Policy(providerID); pol != nil {
		if def := strings.TrimSpace(pol.RoleDefaults["default"]); def != "" {
			return def
		}
		for _, m := range pol.StaticFallback {
			if m = strings.TrimSpace(m); m != "" {
				return m
			}
		}
	}
	if deps != nil && deps.Providers != nil {
		if pv, err := deps.Providers.Get(providerID); err == nil && pv != nil {
			if c, ok := pv.(*provider.Compatible); ok && c != nil {
				for _, m := range c.Def.Models {
					if m = strings.TrimSpace(m); m != "" {
						return m
					}
				}
			}
		}
	}
	return ""
}

// bootstrapModelForSource prefers a gateway profile's own default. Otherwise
// it chooses the first id from the exact source catalog supplied to planning;
// a saved account, pool, or Proxy Hub may safely use that list because it is
// already scoped to the selected source rather than a provider-wide fallback.
// When the discovery catalog is not yet warmed (cold start), it falls back to
// provider role defaults or compatible definition models.
func bootstrapModelForSource(ctx context.Context, resolved *ResolvedSource, deps *Deps, modelCatalog []string) string {
	if resolved == nil || deps == nil {
		for _, model := range modelCatalog {
			if model = strings.TrimSpace(model); model != "" {
				return model
			}
		}
		return ""
	}
	if resolved.Ref.Kind == refs.KindGateway && deps.Profiles != nil {
		if profile, err := deps.Profiles.GetContext(ctx, resolved.Ref.Name); err == nil && profile != nil {
			if model := strings.TrimSpace(profile.Meta.DefaultModel); model != "" {
				return model
			}
		}
	}
	for _, model := range modelCatalog {
		if model = strings.TrimSpace(model); model != "" {
			return model
		}
	}
	if resolved.Ref.Kind == refs.KindProxyHub && deps.Hub != nil {
		if routes, rerr := deps.Hub.RouteModelsContext(ctx, resolved.Ref.Name); rerr == nil {
			for _, r := range routes {
				if m := strings.TrimSpace(r.Model); m != "" {
					return m
				}
			}
		}
		if cfg, err := deps.Hub.GetConfigContext(ctx, resolved.Ref.Name); err == nil && cfg != nil {
			for _, src := range cfg.Sources {
				if !src.IsEnabled() {
					continue
				}
				pID := src.ProviderID
				if p, ok := hub.PoolSourceProvider(src); ok {
					pID = p
				}
				if m := fallbackModelForProvider(deps, pID); m != "" {
					return m
				}
			}
		}
	}
	return fallbackModelForProvider(deps, resolved.Ref.Provider)
}

// modelRequiredError explains a missing default model with the exact next
// command. The discovery failure tailors the advice: an unsupported catalog
// (a provider with no live discovery) can never be refreshed, while a failed
// fetch often means the saved login went stale and the user must sign in again.
func modelRequiredError(clientID string, resolved *ResolvedSource, catalogErr error) error {
	with := refs.DisplayRef(resolved.Ref)
	listCmd := fmt.Sprintf("anypick models %s --with %s", clientID, with)
	switch {
	case errors.Is(catalogErr, proxy.ErrModelDiscoveryUnsupported):
		return anyerr.Newf(
			fmt.Sprintf("%s needs a default model, and %s publishes no live model catalog. Pick an id from `%s`, then re-run with --model <id>.",
				clientID, resolved.Display, listCmd),
			"MODEL_REQUIRED",
			anyerr.WithSuggestions(listCmd))
	case catalogErr != nil && resolved.Ref.Kind == refs.KindAccount:
		return anyerr.Newf(
			fmt.Sprintf("%s needs a default model, but the %s model catalog could not be read (%s). The saved login may be stale — sign in again, save it, then pick a model.",
				clientID, resolved.Display, catalogErr),
			"MODEL_REQUIRED",
			anyerr.WithSuggestions(
				fmt.Sprintf("anypick add account %s --name %s --force", resolved.Ref.Provider, resolved.Ref.Name),
				listCmd+" -- then re-run with --model <id>"))
	default:
		return anyerr.Newf(
			clientID+" needs a default model before this connection can be applied.",
			"MODEL_REQUIRED",
			anyerr.WithSuggestions("Refresh the source models, then choose a Default model.", listCmd))
	}
}

func planWarningsFromModel(model *bindings.ModelSelection, warnings *[]Warning) {
	if model.Mode == "unknown" {
		*warnings = append(*warnings, Warning{
			Code:    "MODEL_UNKNOWN",
			Message: "Migrated binding has unknown model; will not inject a model override.",
		})
	}
}

// clientNativeProvider maps a first-party client to the provider whose model
// ids it emits when AnyPick does not pin one. Provider ids mirror client ids
// for these (claude/claude, gemini/gemini, codex/codex); clients with no
// provider of their own are simply absent, and are never warned about.
//
// Binding a client to an account from another provider is the whole point of
// AnyPick and is never wrong on its own — it only matters when nothing pins a
// model, because then the client sends its own ids and the upstream account
// has to serve them.
var clientNativeProvider = map[string]string{
	"claude": "claude",
	"gemini": "gemini",
	"codex":  "codex",
}

// planWarningsFromUnpinnedModel warns when a client is bound to an account
// from a different vendor and no model and no model role is pinned. Without
// one, the client's own ids reach the upstream verbatim: Claude Code asks for
// claude-sonnet-4-5 / claude-opus-4-5 / claude-haiku-4-5, and a Gemini
// account answers the first two by intent but has no Haiku tier at all.
//
// AnyPick deliberately does not invent a default here. The static catalog's
// Gemini entries are vendor-published API ids, but an OAuth account's Code
// Assist entitlement can expose a different, account-scoped set. Live Gemini
// discovery also speaks Generative Language with an API key, which the OAuth
// (Antigravity / gemini-cli) accounts do not have. So the honest answer is to
// say the model is missing rather than to assume every account serves every
// published id.
func planWarningsFromUnpinnedModel(clientID string, resolved *ResolvedSource, model bindings.ModelSelection, roles map[string]string, warnings *[]Warning) {
	if resolved == nil || resolved.Kind != string(refs.KindAccount) {
		return
	}
	provider := resolved.Ref.Provider
	if provider == "" || explicitModelID(model) != "" || len(roles) > 0 {
		return
	}
	native, ok := clientNativeProvider[clientID]
	if !ok || native == provider {
		return
	}
	*warnings = append(*warnings, Warning{
		Code: "MODEL_NOT_PINNED",
		Message: fmt.Sprintf(
			"%s is bound to a %s account with no model pinned; %s will send its own model ids and this account may not serve them. Re-run with --model <id>.",
			clientID, provider, clientID),
	})
}

func loadProfileName(ref refs.Ref) string {
	if ref.Kind == refs.KindGateway {
		return ref.Name
	}
	return ""
}

func buildTransport(
	caps *client.Capabilities,
	source *ResolvedSource,
	capability transport.Capability,
	account *accountLike,
	pr *profile.Runtime,
) ResolvedTransport {
	protocol := protocolForClient(caps, source.Adapter.Capabilities.Protocols)
	tr := ResolvedTransport{Capability: capability, Protocol: protocol}
	if capability == transport.ManagedBuiltinProxy || capability == transport.ManagedExternalProxy {
		port := defaultProxyPort
		if account != nil && account.proxyPort > 0 {
			port = account.proxyPort
		}
		providerID := source.Adapter.Capabilities.Provider
		accountName := ""
		if source.Ref.Kind == refs.KindAccount {
			accountName = source.Ref.Name
		}
		if source.Ref.Kind == refs.KindAccount || source.Ref.Kind == refs.KindAccountPool {
			providerID = source.Ref.Provider
		}
		tr.ManagedProxy = &ManagedProxyRef{
			Provider: providerID,
			Account:  accountName,
			Port:     port,
			LeaseID:  "pending",
		}
		tr.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	} else if capability == transport.Direct && pr != nil && pr.Meta.Endpoint != "" {
		tr.Endpoint = pr.Meta.Endpoint
	}
	return tr
}

const defaultProxyPort = 8080

// plannedPoolPort mirrors the pool bind decision in proxy startPoolLocked:
// persisted pool port first, then the provider's advertised default, then the
// shared 8080 fallback.
func plannedPoolPort(ctx context.Context, providerID string, deps *Deps) (int, error) {
	pool, err := deps.Pools.GetContext(ctx, providerID)
	if err != nil || pool == nil {
		return 0, err
	}
	if pool.Port > 0 {
		return pool.Port, nil
	}
	pv, err := deps.Providers.Get(providerID)
	if err != nil {
		return 0, err
	}
	if dp, ok := pv.(provider.DefaultProxyPorter); ok && dp.DefaultProxyPort() > 0 {
		return dp.DefaultProxyPort(), nil
	}
	return defaultProxyPort, nil
}

// protocolForClient ports activation-protocol.
func protocolForClient(caps *client.Capabilities, sourceProtocols []transport.Protocol) transport.Protocol {
	preferred := transport.Protocol("")
	if caps != nil && caps.ProtocolPreference != "" {
		preferred = caps.ProtocolPreference
	} else if caps != nil && len(caps.AcceptedProtocols) > 0 {
		preferred = caps.AcceptedProtocols[0]
	} else if len(sourceProtocols) > 0 {
		preferred = sourceProtocols[0]
	}
	if preferred == "" {
		preferred = transport.OpenAI
	}
	for _, p := range sourceProtocols {
		if p == preferred {
			return preferred
		}
	}
	if len(sourceProtocols) > 0 {
		return sourceProtocols[0]
	}
	return transport.OpenAI
}

// providerModelRoleDefaults resolves stored provider defaults for a fresh
// binding with no explicit choice. Roles the client does not declare are
// dropped, and anything outside a known catalog is dropped with them: a
// provider-level model the new entitlement cannot serve must never be
// written into client config. An empty discovery catalog skips only the
// catalog check (mirroring routeDefaultModel's leniency) — dropping the
// copy there would wipe inherited pins on every service restart, when the
// restore path plans without warming.
func providerModelRoleDefaults(
	ctx context.Context, deps *Deps, clientID string, source refs.Ref, catalog []string,
) (map[string]string, bool) {
	if deps == nil || deps.Bindings == nil {
		return nil, false
	}
	key := bindings.ModelRolesProviderKey(source)
	if key == "" {
		return nil, false
	}
	stored, err := deps.Bindings.GetProviderModelRoles(ctx, key)
	if err != nil || len(stored) == 0 {
		return nil, false
	}
	allowed := client.AllowedRoleIDs(clientID)
	inCatalog := map[string]bool{}
	for _, m := range catalog {
		inCatalog[m] = true
	}
	out := map[string]string{}
	for role, model := range stored {
		if !allowed[role] || model == "" {
			continue
		}
		if len(catalog) > 0 && !inCatalog[model] {
			continue
		}
		out[role] = model
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// mergeModelRolesIntoClientOptions ports activation-models.
func mergeModelRolesIntoClientOptions(base map[string]interface{}, modelRoles map[string]string, model bindings.ModelSelection) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range base {
		out[k] = v
	}
	roles := map[string]string{}
	if existing, ok := out["modelRoles"].(map[string]interface{}); ok {
		for k, v := range existing {
			if s, ok := v.(string); ok && s != "" {
				roles[k] = s
			}
		}
	}
	for k, v := range modelRoles {
		if v != "" {
			roles[k] = v
		}
	}
	if model.Mode == "explicit" && model.ID != "" {
		if _, exists := roles["default"]; !exists {
			roles["default"] = model.ID
		}
	}
	if len(roles) > 0 {
		out["modelRoles"] = rolesToStringInterface(roles)
	}
	return out
}

// modelFromRolesOrSelection prefers clientOptions.modelRoles.default when the
// model selection is omitted.
func modelFromRolesOrSelection(spec bindings.Spec, model bindings.ModelSelection) bindings.ModelSelection {
	if model.Mode == "explicit" {
		return model
	}
	roles, ok := spec.ClientOptions["modelRoles"].(map[string]interface{})
	if !ok {
		return model
	}
	if def, ok := roles["default"].(string); ok && def != "" {
		return bindings.ExplicitModel(def)
	}
	return model
}

func modelFromRequest(explicit string, fallback bindings.ModelSelection) bindings.ModelSelection {
	if explicit != "" {
		return bindings.ExplicitModel(explicit)
	}
	return fallback
}

func joinIDs(r *client.Registry) string {
	ids := r.IDs()
	sort.Strings(ids)
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}

func firstNonNilMap(a, b map[string]string) map[string]string {
	if a != nil {
		return a
	}
	return b
}

func rolesToStringInterface(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

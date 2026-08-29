// Plugin-host recovery/restart (GO-PARITY-M7).
//
// The private Node runtime can die three ways: an explicit Terminate issued
// because an unsettled startProxy could not be proven settled, an unexpected
// crash, or a protocol violation. Before this slice the composition root kept
// pointing every RPC-backed adapter at the corpse forever; recovery respawns
// the host from the SAME digest-verified specifications, single-flight, and
// re-commits each plugin's registrations through guarded swaps.
//
// Ownership rules enforced here:
//   - Exactly one Node process per generation. Restarts are single-flighted;
//     concurrent losers join the winner's result and never spawn.
//   - The old generation is terminated before the new one spawns, so two
//     active generations never coexist.
//   - Re-activation replays the startup specs in discovery order onto the new
//     host; retained handles from the old generation are never reused, and
//     every registration swaps against the exact previous committed value.
//   - Ownership is per plugin AND owner-aware: only the plugin that committed
//     an id may replace it. A conflict degrades that candidate (LoadFailure +
//     dispose) and the replacement still publishes healthy siblings, CAS-
//     removing the failed plugin's previous registrations.
//   - Identity and every registry publish under one publication write lock
//     so readers observe all of generation A or all of generation B. The
//     write lock drops before replacement-host Dispose/Close.
//   - App.Close wins deterministically: closing blocks any spawn that had not
//     started yet and drains the in-flight one.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/pluginhost"
	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/pubgate"
)

// ErrRecoveryUnavailable reports that a lost plugin host could not be
// replaced. It wraps the cause; neither ever carries plugin output.
var ErrRecoveryUnavailable = errors.New("plugin host recovery unavailable")

// ErrAppClosing reports that recovery was refused because the app is closing.
var ErrAppClosing = errors.New("app is closing; plugin host will not be replaced")

// ErrUncertainMutation reports that a mutating plugin invocation was
// dispatched to a host that died before responding: the effect may or may not
// have happened, so the call is never replayed automatically.
var ErrUncertainMutation = errors.New("mutating plugin invocation outcome is unknown")

// recoverySpec is one digest-verified activation to replay on a replacement
// host. Specs freeze at startup; the host re-verifies bytes before import, so
// changed packages degrade instead of silently activating.
type recoverySpec struct {
	candidate plugins.Candidate
	spec      pluginhost.PluginSpec
}

// ownedRegistration remembers the exact registry value this runtime committed
// for one plugin id. Recovery swaps through these references; built-ins and
// foreign registrations can never be touched.
type ownedRegistration struct {
	id        string
	committed any
}

// ownedCatalogEntry remembers one catalog id this plugin committed, including
// whether the write actually landed (preserveExisting may leave a built-in).
type ownedCatalogEntry struct {
	id           string
	committed    catalog.Provider
	wrote        bool
	depCommitted activation.CatalogEntry
	wroteDep     bool
}

// pluginOwnership is the committed registration set of ONE plugin. Recovery
// reconciles this set against the replacement activation: successful ids swap,
// failed/omitted ids CAS-remove.
type pluginOwnership struct {
	name      string
	providers []ownedRegistration
	clients   []ownedRegistration
	catalog   []ownedCatalogEntry
}

// ownedRef is one previously committed registry value together with the plugin
// that owns it. Cross-plugin takeover is a conflict, not a swap.
type ownedRef struct {
	plugin    string
	committed any
}

// catalogOwnedRef is the catalog counterpart of ownedRef.
type catalogOwnedRef struct {
	plugin string
	ownedCatalogEntry
}

type pluginSupervisor struct {
	app  *App
	gate *pubgate.Gate

	mu       sync.Mutex
	spawnMu  sync.Mutex
	host     *pluginhost.Host
	identity proxy.PluginRuntimeIdentity
	epoch    uint64
	closing  bool

	// restarting is non-nil while one restart is in flight. Losers block on
	// it and consume the published result — single-flight.
	restarting chan struct{}
	restartErr error

	// config resolves the signed runtime assets exactly once, from the
	// explicit npm/source handoff. Restarts reuse it verbatim: no PATH
	// lookup, no environment re-read.
	config *pluginhost.Config
	specs  []recoverySpec

	owned  []pluginOwnership
	pBases map[string]*rpcProvider
	cBases map[string]*rpcClient

	startupFailures  []plugins.LoadFailure
	recoveryFailures []plugins.LoadFailure
	loaded           []plugins.LoadedPlugin

	// starts counts real pluginhost.Start invocations (diagnostics).
	starts atomic.Uint64

	// hookBeforeSpawn runs after the old generation is killed and before the
	// spawn permit is taken. Tests park here so Close can set closing first.
	hookBeforeSpawn func()
	// hookBeforeStart runs after the spawn permit and closing check, still
	// holding spawnMu, immediately before pluginhost.Start.
	hookBeforeStart func()
	// hookBeforePublish runs after replacement adapters are staged and
	// before the atomic identity+registry commit. Get() must still return
	// generation-A adapters while parked.
	hookBeforePublish func()
	// hookDuringPublish runs after the provider phase of the replacement
	// commit and before the client/catalog phase, still holding the
	// publication write lock. Tests park here to prove readers cannot
	// observe a mixed generation.
	hookDuringPublish func()
	// hookBeforeHostCleanup runs after the publication write lock is
	// released and before Dispose/Close of a replacement host that will
	// not be published. Tests park here to prove built-in Get is not
	// blocked by host cleanup.
	hookBeforeHostCleanup func()
	// hookStart replaces the spawn itself when set (failure injection).
	hookStart func(ctx context.Context) (*pluginhost.Host, error)
}

func newPluginSupervisor(a *App, gate *pubgate.Gate) *pluginSupervisor {
	return &pluginSupervisor{
		app:    a,
		gate:   gate,
		pBases: map[string]*rpcProvider{},
		cBases: map[string]*rpcClient{},
	}
}

// configure stores the explicit runtime assets resolved at startup. A nil
// config means the environment named no private runtime: every recovery
// attempt degrades with a structured reason instead of guessing a binary.
func (s *pluginSupervisor) configure(config pluginhost.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = &config
}

// seed freezes the replay list: once per app lifetime, digest-verified
// startup candidates in discovery order.
func (s *pluginSupervisor) seed(candidates []plugins.Candidate) {
	specs := make([]recoverySpec, 0, len(candidates))
	for _, candidate := range candidates {
		specs = append(specs, recoverySpec{
			candidate: candidate,
			spec: pluginhost.PluginSpec{
				Root:          candidate.Record.Path,
				TrustedDigest: candidate.Record.Digest,
				ExpectedName:  candidate.Record.Name,
			},
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = specs
}

func (s *pluginSupervisor) recordStartupFailure(failure plugins.LoadFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupFailures = append(s.startupFailures, failure)
}

func (s *pluginSupervisor) recordLoaded(loaded plugins.LoadedPlugin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = append(s.loaded, loaded)
}

// publishOwnedPlugin records what one STARTUP activation committed, so
// recovery later reconciles exactly these entries and nothing else.
func (s *pluginSupervisor) publishOwnedPlugin(owned pluginOwnership) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owned = append(s.owned, owned)
}

// attach publishes the first host of this app's lifetime. Activation happens
// afterwards; bases built during startup commits capture epoch 1.
func (s *pluginSupervisor) attach(host *pluginhost.Host) {
	generation, pid := host.ProcessIdentity()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.host = host
	s.identity = proxy.PluginRuntimeIdentity{Generation: generation, PID: pid}
	s.epoch = 1
}

// Identity exposes the engine-supervised runtime identity of the CURRENT host.
// Proxy state stamping treats it as proof only after /health echoes the same
// PID serving the instance. The publication gate is taken first so identity
// cannot advance independently of registry Get/Lookup.
func (s *pluginSupervisor) Identity() proxy.PluginRuntimeIdentity {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity
}

// StartCount reports how many Node processes this supervisor spawned.
func (s *pluginSupervisor) StartCount() uint64 { return s.starts.Load() }

// runtimeSnapshot is the authoritative loaded/failure view for diagnostics;
// recovery updates it in place, unlike the frozen startup copy.
func (s *pluginSupervisor) runtimeSnapshot() (
	loaded []plugins.LoadedPlugin, failures []plugins.LoadFailure,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failures = append(failures, s.startupFailures...)
	failures = append(failures, s.recoveryFailures...)
	return append([]plugins.LoadedPlugin(nil), s.loaded...), failures
}

func (s *pluginSupervisor) healthView() (active, running bool, errText string) {
	s.mu.Lock()
	host, closing := s.host, s.closing
	s.mu.Unlock()
	if host == nil {
		return false, false, ""
	}
	active = true
	if closing {
		return true, false, "app is closing"
	}
	health := host.HealthSnapshot()
	if !health.Running {
		errText = health.Error
		if errText == "" {
			errText = "plugin host is not running"
		}
		return true, false, errText
	}
	return true, true, ""
}

// TerminateGeneration kills the supervised runtime, but ONLY the exact
// generation the caller proved it was talking to. A stale reference — its
// host already replaced by recovery — is refused instead of killing the
// replacement, because other accounts' proxies may be served by it.
func (s *pluginSupervisor) TerminateGeneration(expected proxy.PluginRuntimeIdentity) error {
	if !expected.IsValid() {
		return fmt.Errorf("refusing to terminate plugin runtime without an exact generation")
	}
	s.gate.RLock()
	s.mu.Lock()
	host, current := s.host, s.identity
	s.mu.Unlock()
	s.gate.RUnlock()
	if host == nil {
		return nil
	}
	if current.Generation != expected.Generation {
		return fmt.Errorf(
			"refusing to terminate plugin runtime generation %q: the hosting runtime advanced",
			expected.Generation)
	}
	return host.Terminate()
}

// Close refuses future recovery and closes the current host. An in-flight
// restart drains first: it either aborted before spawning (closing won the
// race) or completed before closing began, and its host closes here.
func (s *pluginSupervisor) Close() {
	// spawnMu first: a restart cannot pass its closing check / Start while
	// Close is declaring shutdown, and Close cannot declare shutdown while a
	// restart is inside Start. That removes the TOCTOU between the check and
	// pluginhost.Start.
	s.spawnMu.Lock()
	s.mu.Lock()
	s.closing = true
	flight := s.restarting
	s.mu.Unlock()
	s.spawnMu.Unlock()
	if flight != nil {
		<-flight
	}
	// Re-read after draining: a restart that committed before closing=true
	// published a replacement this snapshot would otherwise miss.
	s.mu.Lock()
	host := s.host
	s.host = nil
	s.mu.Unlock()
	if host != nil {
		_ = host.Close()
	}
}

// ensure guarantees a live host NEWER than observedEpoch, spawning at most one
// replacement no matter how many callers race here. Returns the epoch
// recovery must use for replacement lookups.
func (s *pluginSupervisor) ensure(ctx context.Context, observedEpoch uint64) (uint64, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return observedEpoch, fmt.Errorf("%w: %w", ErrRecoveryUnavailable, ErrAppClosing)
	}
	// Another caller already completed a newer generation: join it.
	if s.epoch > observedEpoch && s.host != nil && s.host.HealthSnapshot().Running {
		epoch := s.epoch
		s.mu.Unlock()
		return epoch, nil
	}
	if s.host != nil && s.epoch == observedEpoch && s.host.HealthSnapshot().Running {
		generation := s.identity.Generation
		s.mu.Unlock()
		return observedEpoch, fmt.Errorf(
			"%w: host reported lost but supervised runtime %q is still healthy",
			ErrRecoveryUnavailable, generation)
	}
	flight := s.restarting
	if flight == nil {
		flight = make(chan struct{})
		s.restarting = flight
		go s.runRestart(flight)
	}
	s.mu.Unlock()

	select {
	case <-flight:
	case <-ctx.Done():
		return observedEpoch, fmt.Errorf("%w: %w", ErrRecoveryUnavailable, ctx.Err())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch > observedEpoch && s.host != nil && s.host.HealthSnapshot().Running {
		return s.epoch, nil
	}
	if s.restartErr != nil {
		return observedEpoch, fmt.Errorf("%w: %w", ErrRecoveryUnavailable, s.restartErr)
	}
	return observedEpoch, fmt.Errorf("%w: recovery produced no runtime", ErrRecoveryUnavailable)
}

func (s *pluginSupervisor) runRestart(flight chan struct{}) {
	// Detached from every caller: a cancelled waiter must not abort the
	// single-flight replacement other callers are still joining.
	err := s.restart(context.Background())
	s.mu.Lock()
	s.restartErr = err
	s.restarting = nil
	s.mu.Unlock()
	close(flight)
}

// restart performs the actual generation replacement. No supervisor locks are
// held; exclusivity comes from publishing s.restarting first.
func (s *pluginSupervisor) restart(ctx context.Context) error {
	s.mu.Lock()
	config := s.config
	var cfgCopy pluginhost.Config
	if config != nil {
		cfgCopy = *config
	}
	specs := append([]recoverySpec(nil), s.specs...)
	prevOwned := cloneOwnership(s.owned)
	oldHost := s.host
	nextEpoch := s.epoch + 1
	hookBefore := s.hookBeforeSpawn
	hookBeforeStart := s.hookBeforeStart
	hookBeforePublish := s.hookBeforePublish
	hookDuringPublish := s.hookDuringPublish
	hookBeforeHostCleanup := s.hookBeforeHostCleanup
	hookStart := s.hookStart
	s.mu.Unlock()

	if config == nil {
		return errors.New("no private plugin runtime is configured")
	}
	if oldHost == nil {
		return errors.New("no plugin runtime to replace")
	}
	// Kill the old generation BEFORE spawning: two active generations must
	// never coexist, even briefly. Idempotent for already-dead hosts.
	_ = oldHost.Terminate()

	// Close-vs-recovery barrier BEFORE the spawn permit: Close can set
	// closing here, and the permit check below will refuse to Start.
	if hookBefore != nil {
		hookBefore()
	}

	s.spawnMu.Lock()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.spawnMu.Unlock()
		return ErrAppClosing
	}
	s.mu.Unlock()
	if hookBeforeStart != nil {
		hookBeforeStart()
	}

	budget := config.StartupTimeout + time.Duration(len(specs)+1)*config.CallTimeout
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	var host *pluginhost.Host
	var err error
	// Counted here — after every abort gate, still under spawnMu — so Close
	// cannot have begun, and the counter reports Node processes actually
	// spawned rather than attempts Close legitimately won.
	s.starts.Add(1)
	if hookStart != nil {
		host, err = hookStart(startCtx)
	} else {
		host, err = pluginhost.Start(startCtx, cfgCopy)
	}
	s.spawnMu.Unlock()
	if err != nil {
		return fmt.Errorf("restart private Node runtime: %w", err)
	}
	generation, pid := host.ProcessIdentity()

	stagedList := make([]stagedPlugin, 0, len(specs))
	failures := []plugins.LoadFailure{}
	for _, spec := range specs {
		activationResult, activateErr := host.Activate(startCtx, spec.spec)
		if activateErr != nil {
			failures = append(failures, pluginFailure(spec.candidate, activateErr))
			continue
		}
		staged, stageErr := s.app.stagePluginActivationFor(host, nextEpoch, activationResult)
		if stageErr != nil {
			_ = activationResult.Dispose(context.Background())
			failures = append(failures, pluginFailure(spec.candidate, stageErr))
			continue
		}
		stagedList = append(stagedList, stagedPlugin{
			spec: spec, activation: activationResult, staged: staged,
		})
	}

	prevProviders, prevClients, prevCatalog := ownershipLookups(prevOwned)
	claimedProviders := map[string]string{}
	claimedClients := map[string]string{}
	claimedCatalog := map[string]string{}
	healthy := make([]stagedPlugin, 0, len(stagedList))
	for _, item := range stagedList {
		name := item.spec.candidate.Record.Name
		if conflictErr := s.app.validateRecoveryConflicts(
			name, item.staged, prevProviders, prevClients, prevCatalog,
			claimedProviders, claimedClients, claimedCatalog,
		); conflictErr != nil {
			_ = item.activation.Dispose(context.Background())
			failures = append(failures, pluginFailure(item.spec.candidate, conflictErr))
			continue
		}
		claimStagedIDs(name, item.staged, claimedProviders, claimedClients, claimedCatalog)
		healthy = append(healthy, item)
	}
	stagedList = healthy

	if hookBeforePublish != nil {
		hookBeforePublish()
	}

	cleanupReplacement := func() {
		if hookBeforeHostCleanup != nil {
			hookBeforeHostCleanup()
		}
		for _, item := range stagedList {
			_ = item.activation.Dispose(context.Background())
		}
		_ = host.Close()
	}

	// Publication write lock FIRST, then s.mu: every Get/Lookup/Identity
	// takes the matching read lock, so lock order is gate → supervisor.
	// Unlock before Dispose/Close: those are timeout-bounded and must not
	// stall built-in registry readers.
	s.gate.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			s.gate.Unlock()
		}
	}()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.gate.Unlock()
		unlocked = true
		cleanupReplacement()
		return ErrAppClosing
	}
	publishErr := s.publishReplacementLocked(publishReplacement{
		host:              host,
		generation:        generation,
		pid:               pid,
		epoch:             nextEpoch,
		staged:            stagedList,
		prevOwned:         prevOwned,
		failures:          failures,
		hookDuringPublish: hookDuringPublish,
	})
	s.mu.Unlock()
	s.gate.Unlock()
	unlocked = true
	if publishErr != nil {
		cleanupReplacement()
		return publishErr
	}
	return nil
}

type publishReplacement struct {
	host              *pluginhost.Host
	generation        string
	pid               int
	epoch             uint64
	staged            []stagedPlugin
	prevOwned         []pluginOwnership
	failures          []plugins.LoadFailure
	hookDuringPublish func()
}

type stagedPlugin struct {
	spec       recoverySpec
	activation *pluginhost.Activation
	staged     stagedActivation
}

// publishReplacementLocked is the single generation-commit boundary. The
// caller already holds the publication write lock and s.mu. Identity, base
// maps and every registry swap (plus CAS-removal of failed/omitted ids)
// happen before either lock is released, so a reader taking the publication
// read lock observes all of A or all of B. A failed swap rolls the whole
// transaction back before the write lock drops.
func (s *pluginSupervisor) publishReplacementLocked(view publishReplacement) (err error) {
	prevIdentity := s.identity
	prevEpoch := s.epoch
	prevHost := s.host
	prevOwned := s.owned
	prevPBases := s.pBases
	prevCBases := s.cBases
	prevLoaded := s.loaded
	prevFailures := s.recoveryFailures

	newPBases := map[string]*rpcProvider{}
	newCBases := map[string]*rpcClient{}
	ownedByName := map[string]*pluginOwnership{}
	loaded := []plugins.LoadedPlugin{}
	var undos []func()

	revertSupervisor := func() {
		s.host = prevHost
		s.identity = prevIdentity
		s.epoch = prevEpoch
		s.owned = prevOwned
		s.pBases = prevPBases
		s.cBases = prevCBases
		s.loaded = prevLoaded
		s.recoveryFailures = prevFailures
	}

	s.host = view.host
	s.identity = proxy.PluginRuntimeIdentity{Generation: view.generation, PID: view.pid}
	s.epoch = view.epoch
	s.pBases = newPBases
	s.cBases = newCBases

	defer func() {
		if err == nil {
			return
		}
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
		revertSupervisor()
	}()

	ensureOwned := func(name string) *pluginOwnership {
		if ownedByName[name] == nil {
			ownedByName[name] = &pluginOwnership{name: name}
		}
		return ownedByName[name]
	}
	for _, item := range view.staged {
		ensureOwned(item.spec.candidate.Record.Name)
		loaded = append(loaded, plugins.LoadedPlugin{
			Record: item.spec.candidate.Record, ActivationID: item.activation.ActivationID,
		})
	}

	prevProviderByID, prevClientByID, prevCatalogByID := ownershipLookups(view.prevOwned)
	desiredProviders := map[string]struct{}{}
	desiredClients := map[string]struct{}{}
	desiredCatalog := map[string]struct{}{}

	if err := s.swapOwnedProviders(
		view.staged, prevProviderByID, ownedByName, newPBases, desiredProviders, &undos,
	); err != nil {
		return err
	}
	if err := s.removeStaleProviders(view.prevOwned, desiredProviders, &undos); err != nil {
		return err
	}
	if view.hookDuringPublish != nil {
		view.hookDuringPublish()
	}

	if err := s.swapOwnedClients(
		view.staged, prevClientByID, ownedByName, newCBases, desiredClients, &undos,
	); err != nil {
		return err
	}
	if err := s.removeStaleClients(view.prevOwned, desiredClients, &undos); err != nil {
		return err
	}

	if err := s.swapOwnedCatalog(
		view.staged, prevCatalogByID, ownedByName, desiredCatalog, &undos,
	); err != nil {
		return err
	}
	if err := s.removeStaleCatalog(view.prevOwned, desiredCatalog, &undos); err != nil {
		return err
	}

	newOwned := make([]pluginOwnership, 0, len(view.staged))
	for _, item := range view.staged {
		if own := ownedByName[item.spec.candidate.Record.Name]; own != nil {
			newOwned = append(newOwned, *own)
		}
	}
	s.owned = newOwned
	s.loaded = loaded
	s.recoveryFailures = view.failures
	return nil
}

func (s *pluginSupervisor) swapOwnedProviders(
	staged []stagedPlugin,
	prevByID map[string]ownedRef,
	ownedByName map[string]*pluginOwnership,
	pBases map[string]*rpcProvider,
	desired map[string]struct{},
	undos *[]func(),
) error {
	for _, item := range staged {
		pluginName := item.spec.candidate.Record.Name
		own := ownedByName[pluginName]
		for _, stagedProvider := range item.staged.providers {
			id := stagedProvider.value.ID()
			desired[id] = struct{}{}
			var previous provider.Provider
			if raw, ok := prevByID[id]; ok {
				if raw.plugin != pluginName {
					return fmt.Errorf("provider %q is owned by plugin %q", id, raw.plugin)
				}
				typed, ok := raw.committed.(provider.Provider)
				if !ok {
					return fmt.Errorf("owned provider %q has unexpected type", id)
				}
				previous = typed
			}
			if err := s.app.Providers.RecoverSwap(previous, stagedProvider.value); err != nil {
				return err
			}
			next := stagedProvider.value
			*undos = append(*undos, func() {
				_ = s.app.Providers.RecoverUndo(next, previous)
			})
			own.providers = append(own.providers, ownedRegistration{id: id, committed: next})
			if base, ok := rpcBaseOf(next); ok {
				pBases[id] = base
			}
		}
	}
	return nil
}

func (s *pluginSupervisor) removeStaleProviders(
	prev []pluginOwnership, desired map[string]struct{}, undos *[]func(),
) error {
	for _, plug := range prev {
		for _, reg := range plug.providers {
			if _, keep := desired[reg.id]; keep {
				continue
			}
			typed, ok := reg.committed.(provider.Provider)
			if !ok {
				return fmt.Errorf("owned provider %q has unexpected type", reg.id)
			}
			if err := s.app.Providers.RecoverRemove(typed); err != nil {
				return err
			}
			previous := typed
			*undos = append(*undos, func() {
				_ = s.app.Providers.RecoverSwap(nil, previous)
			})
		}
	}
	return nil
}

func (s *pluginSupervisor) swapOwnedClients(
	staged []stagedPlugin,
	prevByID map[string]ownedRef,
	ownedByName map[string]*pluginOwnership,
	cBases map[string]*rpcClient,
	desired map[string]struct{},
	undos *[]func(),
) error {
	for _, item := range staged {
		pluginName := item.spec.candidate.Record.Name
		own := ownedByName[pluginName]
		for _, stagedClient := range item.staged.clients {
			id := stagedClient.ID()
			desired[id] = struct{}{}
			var previous client.Adapter
			if raw, ok := prevByID[id]; ok {
				if raw.plugin != pluginName {
					return fmt.Errorf("client %q is owned by plugin %q", id, raw.plugin)
				}
				typed, ok := raw.committed.(client.Adapter)
				if !ok {
					return fmt.Errorf("owned client %q has unexpected type", id)
				}
				previous = typed
			}
			if err := s.app.Clients.RecoverSwap(previous, stagedClient); err != nil {
				return err
			}
			next := stagedClient
			*undos = append(*undos, func() {
				_ = s.app.Clients.RecoverUndo(next, previous)
			})
			own.clients = append(own.clients, ownedRegistration{id: id, committed: next})
			if base, ok := rpcClientBaseOf(next); ok {
				cBases[id] = base
			}
		}
	}
	return nil
}

func (s *pluginSupervisor) removeStaleClients(
	prev []pluginOwnership, desired map[string]struct{}, undos *[]func(),
) error {
	for _, plug := range prev {
		for _, reg := range plug.clients {
			if _, keep := desired[reg.id]; keep {
				continue
			}
			typed, ok := reg.committed.(client.Adapter)
			if !ok {
				return fmt.Errorf("owned client %q has unexpected type", reg.id)
			}
			if err := s.app.Clients.RecoverRemove(typed); err != nil {
				return err
			}
			previous := typed
			*undos = append(*undos, func() {
				_ = s.app.Clients.RecoverSwap(nil, previous)
			})
		}
	}
	return nil
}

func (s *pluginSupervisor) swapOwnedCatalog(
	staged []stagedPlugin,
	prevByID map[string]catalogOwnedRef,
	ownedByName map[string]*pluginOwnership,
	desired map[string]struct{},
	undos *[]func(),
) error {
	for _, item := range staged {
		pluginName := item.spec.candidate.Record.Name
		own := ownedByName[pluginName]
		for _, stagedCatalog := range item.staged.catalog {
			id := stagedCatalog.value.ID
			desired[id] = struct{}{}
			entry := activation.CatalogEntry{
				Name: stagedCatalog.value.Name, APIStyle: string(stagedCatalog.value.APIStyle),
				Protocols: stagedCatalog.value.Protocols,
			}
			prevEntry, hadPrev := prevByID[id]
			if hadPrev && prevEntry.plugin != pluginName {
				return fmt.Errorf("catalog %q is owned by plugin %q", id, prevEntry.plugin)
			}
			wrotePrev := hadPrev && prevEntry.wrote
			if stagedCatalog.preserveExisting && !wrotePrev {
				wrote := s.app.Runtime.Catalog.SetIfAbsent(stagedCatalog.value)
				wroteDep := s.app.Deps.Catalog.SetIfAbsent(id, entry)
				if wrote {
					undosID := id
					*undos = append(*undos, func() { s.app.Runtime.Catalog.Delete(undosID) })
				}
				if wroteDep {
					undosID := id
					*undos = append(*undos, func() { s.app.Deps.Catalog.Delete(undosID) })
				}
				own.catalog = append(own.catalog, ownedCatalogEntry{
					id: id, committed: stagedCatalog.value, wrote: wrote,
					depCommitted: entry, wroteDep: wroteDep,
				})
				continue
			}
			var previous *catalog.Provider
			var depPrevious *activation.CatalogEntry
			if wrotePrev {
				committed := prevEntry.committed
				previous = &committed
				if prevEntry.wroteDep {
					dep := prevEntry.depCommitted
					depPrevious = &dep
				}
			}
			if err := s.app.Runtime.Catalog.RecoverSwap(previous, stagedCatalog.value); err != nil {
				return err
			}
			nextVal := stagedCatalog.value
			*undos = append(*undos, func() {
				_ = s.app.Runtime.Catalog.RecoverUndo(nextVal, previous)
			})
			if err := s.app.Deps.Catalog.RecoverSwap(id, depPrevious, entry); err != nil {
				return err
			}
			depNext := entry
			depID := id
			*undos = append(*undos, func() {
				_ = s.app.Deps.Catalog.RecoverUndo(depID, depNext, depPrevious)
			})
			own.catalog = append(own.catalog, ownedCatalogEntry{
				id: id, committed: stagedCatalog.value, wrote: true,
				depCommitted: entry, wroteDep: true,
			})
		}
	}
	return nil
}

func (s *pluginSupervisor) removeStaleCatalog(
	prev []pluginOwnership, desired map[string]struct{}, undos *[]func(),
) error {
	for _, plug := range prev {
		for _, reg := range plug.catalog {
			if _, keep := desired[reg.id]; keep {
				continue
			}
			if reg.wrote {
				if err := s.app.Runtime.Catalog.RecoverRemove(reg.committed); err != nil {
					return err
				}
				previous := reg.committed
				*undos = append(*undos, func() {
					_ = s.app.Runtime.Catalog.RecoverSwap(nil, previous)
				})
			}
			if reg.wroteDep {
				if err := s.app.Deps.Catalog.RecoverRemove(reg.id, reg.depCommitted); err != nil {
					return err
				}
				id, dep := reg.id, reg.depCommitted
				*undos = append(*undos, func() {
					_ = s.app.Deps.Catalog.RecoverSwap(id, nil, dep)
				})
			}
		}
	}
	return nil
}

func cloneOwnership(in []pluginOwnership) []pluginOwnership {
	out := make([]pluginOwnership, len(in))
	for i, item := range in {
		out[i] = pluginOwnership{
			name:      item.name,
			providers: append([]ownedRegistration(nil), item.providers...),
			clients:   append([]ownedRegistration(nil), item.clients...),
			catalog:   append([]ownedCatalogEntry(nil), item.catalog...),
		}
	}
	return out
}

func ownershipLookups(owned []pluginOwnership) (
	providers map[string]ownedRef, clients map[string]ownedRef, catalogEntries map[string]catalogOwnedRef,
) {
	providers = map[string]ownedRef{}
	clients = map[string]ownedRef{}
	catalogEntries = map[string]catalogOwnedRef{}
	for _, plug := range owned {
		for _, item := range plug.providers {
			providers[item.id] = ownedRef{plugin: plug.name, committed: item.committed}
		}
		for _, item := range plug.clients {
			clients[item.id] = ownedRef{plugin: plug.name, committed: item.committed}
		}
		for _, item := range plug.catalog {
			catalogEntries[item.id] = catalogOwnedRef{plugin: plug.name, ownedCatalogEntry: item}
		}
	}
	return
}

func claimStagedIDs(
	pluginName string,
	staged stagedActivation,
	providers, clients, catalog map[string]string,
) {
	for _, item := range staged.providers {
		providers[item.value.ID()] = pluginName
	}
	for _, item := range staged.clients {
		clients[item.ID()] = pluginName
	}
	for _, item := range staged.catalog {
		catalog[item.value.ID] = pluginName
	}
}

func (s *pluginSupervisor) trackProviderBase(id string, base *rpcProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pBases[id] = base
}

func (s *pluginSupervisor) trackClientBase(id string, base *rpcClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cBases[id] = base
}

// replacementProvider resolves the CURRENT-generation provider base for one
// id, ensuring recovery has moved past the caller's observed epoch.
func (s *pluginSupervisor) replacementProvider(ctx context.Context, observedEpoch uint64, id string) (*rpcProvider, error) {
	epoch, err := s.ensure(ctx, observedEpoch)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRecoveryUnavailable, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch == observedEpoch {
		return nil, fmt.Errorf("%w: provider %q has no replacement generation", ErrRecoveryUnavailable, id)
	}
	base, ok := s.pBases[id]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q did not reactivate", ErrRecoveryUnavailable, id)
	}
	return base, nil
}

// replacementClient mirrors replacementProvider for plugin clients.
func (s *pluginSupervisor) replacementClient(ctx context.Context, observedEpoch uint64, id string) (*rpcClient, error) {
	epoch, err := s.ensure(ctx, observedEpoch)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRecoveryUnavailable, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch == observedEpoch {
		return nil, fmt.Errorf("%w: client %q has no replacement generation", ErrRecoveryUnavailable, id)
	}
	base, ok := s.cBases[id]
	if !ok {
		return nil, fmt.Errorf("%w: client %q did not reactivate", ErrRecoveryUnavailable, id)
	}
	return base, nil
}

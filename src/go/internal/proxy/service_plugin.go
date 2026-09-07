// Provider-owned proxy lifecycle forwarding (GO-PARITY-M7).
//
// Arbitrary JavaScript plugin providers implement startProxy/stopProxy/
// proxyStatus/readProxyLogs behind the private Node host. The Service keeps
// every ownership duty here — tokens (ADR 0006), instance-identity
// verification through the /health echo (ADR 0003 §3), leases, proxy_state and
// compensating rollback — and only the transport call itself crosses the
// plugin boundary. The built-in engine path is untouched: routing happens per
// provider through PluginLifecycle.ForwardsLifecycle, never on provider ids.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// ErrUnsettledInvocation reports that a cancelled lifecycle invocation could
// not be confirmed settled by the plugin host inside its deadline: the plugin
// coroutine may still be running and bind afterwards. Compensation must never
// trust an empty timing window in this state — the hosting runtime gets
// terminated and darkness is proven instead.
var ErrUnsettledInvocation = errors.New("plugin proxy invocation did not settle")

// lifecycleVerifyTimeout bounds the ownership gates of the forwarded path so
// tests can prove failure compensation deterministically without waiting on
// the production health timeout.
var lifecycleVerifyTimeout = healthTimeout

// compensationBudget bounds the mandatory config-restoration compensation so
// it can never starve on the shared, test-tunable lifecycleVerifyTimeout:
// restoring the caller's exact prior config is the non-negotiable invariant
// whenever a forwarded start fails to commit, yet the planned config carries
// the mutated Enabled/Port from this call. The original code restored it as the
// best-effort tail of the cleanup window, so a loaded run could leave the
// planned config committed when that window expired first. The restore now runs
// on its own independent budget, detached from the test-tunable timeout.
var compensationBudget = 5 * time.Second

// abandonedStartGraceWindow covers the gap between Go abandoning a cancelled
// invocation and the plugin coroutine actually settling (the cancel frame is
// fire-and-forget, so the host may still be inside startProxy when this
// Service starts compensating). Bounded so ordinary pre-bind failures do not
// stall; anything binding later leaves a live echo that the next start adopts
// or stops through the persisted diagnostic record.
var abandonedStartGraceWindow = minDuration(lifecycleVerifyTimeout, 500*time.Millisecond)

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (s *Service) forwardsLifecycle(providerID string) bool {
	return s.PluginLifecycle != nil && s.PluginLifecycle.ForwardsLifecycle(providerID)
}

// forwardedCompensation carries facts a failed forwarded start learned about
// SHARED engine state out to the lock boundary. Host termination is the one
// such fact today: it happens deep inside compensation while the failing
// account still holds its mutation locks, but its consequences (other
// accounts' served proxies dying) must only be acted on AFTER those locks are
// released, so the flag travels up instead of triggering work in place.
type forwardedCompensation struct {
	// hostTerminated reports that this call force-terminated the shared
	// plugin runtime. Other forwarding accounts' proxies may have died with
	// it and need reconciliation once every inherited lock is gone.
	hostTerminated    bool
	terminatedRuntime PluginRuntimeIdentity
}

func (s *Service) pluginRequest(providerID, accountName string, cfg store.ProxyConfig, token string) PluginRequest {
	return PluginRequest{
		ProviderID:  providerID,
		AccountName: accountName,
		SnapshotDir: paths.AccountSnapshotDir(s.Store.Root, providerID, accountName),
		RuntimeDir:  paths.AccountProxyRuntimeDir(s.Store.Root, providerID, accountName),
		Config:      cfg,
		Token:       token,
	}
}

// startForwardedLocked starts (or adopts) a forwarding provider's proxy under
// the caller's [providerScope, proxy/<p>/<a>] locks. Mirrors TS
// startProxyInternal: persist the planned config, generate a fresh engine
// token, forward startProxy, verify the reported listener echoes the reported
// instance id, then commit state + lease. Every failed branch compensates.
func (s *Service) startForwardedLocked(
	ctx context.Context,
	providerID, accountName string,
	originalCfg store.ProxyConfig,
	outcome *forwardedCompensation,
) (*StartResult, error) {
	cfg := originalCfg
	if !cfg.Enabled {
		cfg.Enabled = true
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if err := AssertLoopbackHost(host); err != nil {
		return nil, err
	}
	if cfg.Port == 0 {
		port, err := PickFreePort()
		if err != nil {
			return nil, anyerr.Wrapf(err, "pick port", "PROXY_START_FAILED")
		}
		cfg.Port = port
	}

	// Health-verified idempotent start (PROC-01): adopt the recorded instance
	// only when /health still echoes its id — never because a record or a pid
	// file happens to exist. The persisted token stays valid for this instance.
	state, err := s.Store.ReadProxyState(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}
	if state != nil && state.Endpoint != "" && state.InstanceID != "" && state.Token != "" &&
		healthInstance(state.Endpoint) == state.InstanceID {
		return &StartResult{
			Endpoint: state.Endpoint, Token: state.Token,
			InstanceID: state.InstanceID, Port: portOf(state.Endpoint),
			StartedNow: false,
		}, nil
	}

	token := ""
	if state != nil {
		token = state.Token
	}
	if token == "" {
		// First-ever start mints the stable per-account token; restarts
		// reuse it so client configs written once at activation keep
		// working (same contract as startLocked/pickPoolToken).
		token, err = NewToken()
		if err != nil {
			return nil, anyerr.Wrapf(err, "generate proxy token", "PROXY_START_FAILED")
		}
	}
	if err := s.Store.SetProxyConfigContext(ctx, providerID, accountName, cfg); err != nil {
		return nil, err
	}

	// The serving generation is captured BEFORE dispatch: every downstream
	// decision (state stamping, force-termination, host-loss reconciliation)
	// must name the exact runtime this call went through, never whatever the
	// supervisor happens to point at later (a concurrent recovery may have
	// replaced a dead generation in between).
	runtimeID := runtimeIdentityOf(s.PluginLifecycle)

	req := s.pluginRequest(providerID, accountName, cfg, token)
	started, startErr := s.PluginLifecycle.StartProxy(ctx, req)
	if startErr == nil {
		startErr = ValidatePluginStart(started)
	}
	// A not-dispatched recovery may have replaced the dead pre-dispatch
	// generation. Compensation and in-host stamping must name the host that
	// actually served (or failed to serve) this call, never the corpse.
	servingID := runtimeID
	if started != nil && started.Runtime.valid() {
		servingID = started.Runtime
	}
	if startErr != nil {
		// A plugin can bind successfully and THEN reject, and a cancelled
		// invocation returns before the host coroutine settles — so every
		// post-invocation failure is compensated through the provider's own
		// idempotent stop plus an ownership-echo proof before any record is
		// erased. Unprovable death leaves a diagnostic proxy_state row.
		return nil, s.failForwardedStart(ctx, req, started, startErr, token, originalCfg,
			servingID, outcome)
	}

	// Ownership proof before declaring success (ADR 0003 §3): whatever hosts
	// the listener must echo the instance id the plugin reported. The same
	// compensation path applies: at this point a listener definitely exists.
	if err := waitHealthInstance(started.Endpoint, started.InstanceID, lifecycleVerifyTimeout); err != nil {
		return nil, s.failForwardedStart(ctx, req, started,
			anyerr.Wrapf(err, "verify plugin proxy ownership", "PROXY_START_FAILED"),
			token, originalCfg, servingID, outcome)
	}

	st := store.ProxyRuntimeState{
		AccountName: accountName, Endpoint: started.Endpoint,
		Compatibility: started.Compatibility, PID: started.PID,
		InstanceID: started.InstanceID, LogPath: started.LogPath,
		StartedAt: nowISO(), Token: token,
	}
	// Only the engine lifecycle exposes the supervised runtime generation, and
	// the listener must independently echo that exact PID before the row is
	// marked in-host. Plugin-returned PID fields alone never establish it.
	// Confirmation re-probes within a bounded window: waitHealthInstance just
	// proved the instance echo, and losing ONE probe to scheduling under load
	// must not strip an in-host row of its generation identity.
	if servingID.valid() && started.PID == servingID.PID &&
		confirmInHostStamping(started.Endpoint, started, servingID) {
		st.PluginRuntimeGeneration = servingID.Generation
		st.PluginRuntimePID = servingID.PID
	}
	if err := s.Store.WriteProxyStateContext(ctx, providerID, accountName, st); err != nil {
		return nil, s.failForwardedStart(ctx, req, started, err, token, originalCfg,
			servingID, outcome)
	}

	leaseID, err := NewInstanceID()
	if err != nil {
		leaseID = started.InstanceID
	}
	ownerPID := ownerPidFor(started.PID)
	if err := s.Jobs.CreateContext(ctx, Lease{
		LeaseID: leaseID, Provider: providerID, Account: accountName,
		Port: portOf(started.Endpoint), Host: hostOf(started.Endpoint),
		Endpoint: started.Endpoint, OwnerPID: ownerPID,
		InstanceID: started.InstanceID,
	}); err != nil {
		return nil, s.failForwardedStart(ctx, req, started, err, token, originalCfg,
			servingID, outcome)
	}
	s.startLeaseHeartbeat(leaseID, started.InstanceID, ownerPID)

	return &StartResult{
		Endpoint: started.Endpoint, Token: token,
		LeaseID: leaseID, InstanceID: started.InstanceID,
		Port: portOf(started.Endpoint), StartedNow: true,
	}, nil
}

// failForwardedStart compensates every post-invocation start failure: it asks
// the provider to stop whatever it may have bound (a bind-then-throw or a late
// bind after cancellation is realistic), proves termination through the echo,
// restores the exact prior config, and only then decides between a clean slate
// and a diagnostic record for an orphan we could not verify dead. The expected
// identity is the generation that actually served this call. A not-dispatched
// recovery names the replacement; force termination of a stale generation is
// refused so a concurrent replacement is not killed.
// confirmInHostStamping positively re-probes the listener for the exact
// generation stamp: instance echo AND responder PID equal to the supervised
// runtime captured before dispatch. waitHealthInstance already proved the
// echo, so this is a confirmation, not discovery — but a single probe can
// lose to scheduling under load, and a missed stamp would silently demote an
// in-host row to ambiguous (retained forever). Bounded retries keep the proof
// positive-only: no probe hit, no stamp.
func confirmInHostStamping(
	endpoint string,
	started *PluginStart,
	servingID PluginRuntimeIdentity,
) bool {
	deadline := time.Now().Add(lifecycleVerifyTimeout)
	for {
		health := healthOwnership(endpoint)
		if health.InstanceID == started.InstanceID && health.PID == servingID.PID {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Service) failForwardedStart(
	ctx context.Context,
	req PluginRequest,
	started *PluginStart,
	cause error,
	token string,
	originalCfg store.ProxyConfig,
	expectedRuntime PluginRuntimeIdentity,
	outcome *forwardedCompensation,
) error {
	secrets := s.accountSecrets(req.ProviderID, req.AccountName, token)
	// An unsettled cancellation (the host could not confirm the coroutine
	// finished) is never trusted to a timing window: the runtime itself gets
	// terminated, because a dead process cannot bind afterwards.
	unsettled := errors.Is(cause, ErrUnsettledInvocation)
	proven, settleErr := s.settleAbandonedListener(ctx, req, started, unsettled, expectedRuntime, outcome)

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*lifecycleVerifyTimeout)
	defer cancel()
	var errs []error
	if proven {
		// Release, not full clear: the token stays stable per account so
		// surviving client configs keep working (same contract as stop).
		errs = append(errs, s.Store.ReleaseProxyRuntimeContext(cleanupCtx, req.ProviderID, req.AccountName))
	} else if req.Config.Port > 0 {
		// Fail closed like abortStart: an orphaned credential-bearing proxy is
		// safer to diagnose than to forget. The record carries everything the
		// engine knows — including the engine token (owner-only DB), so a
		// later start adopts or stops the orphan through the same verified
		// paths as a live instance.
		endpoint := fmt.Sprintf("http://%s:%d",
			normalizedLoopback(plannedHost(req.Config.Host)), req.Config.Port)
		record := store.ProxyRuntimeState{
			AccountName: req.AccountName, Endpoint: endpoint,
			StartedAt: nowISO(), Token: token,
		}
		if started != nil {
			record.Compatibility = started.Compatibility
			record.PID = started.PID
			record.InstanceID = started.InstanceID
			record.LogPath = started.LogPath
			if started.Endpoint != "" {
				record.Endpoint = started.Endpoint
			}
		}
		errs = append(errs, s.Store.WriteProxyStateContext(cleanupCtx,
			req.ProviderID, req.AccountName, record))
	}

	// Config restoration is the mandatory invariant: the planned config
	// (Enabled/Port mutated by this call) MUST be reverted regardless of how the
	// proxy-state decision above resolved. It runs on its own independent budget
	// so a loaded cleanup window can never strand the mutation as the account's
	// persisted state — a single idempotent UPDATE finishes long before the
	// budget elapses even under pressure.
	restoreCtx, restoreCancel := context.WithTimeout(context.WithoutCancel(ctx), compensationBudget)
	defer restoreCancel()
	restoreErr := s.Store.SetProxyConfigContext(restoreCtx, req.ProviderID, req.AccountName, originalCfg)

	return errors.Join(
		redactErr(cause, secrets),
		redactErr(settleErr, secrets),
		errors.Join(errs...),
		restoreErr)
}

// reconcileForwardedOnHostLoss reconciles every OTHER forwarding account whose
// proxy may have died with the shared plugin host that was just force-
// terminated. It MUST run only after the failing account's mutation locks are
// released (the compensation path flags the host loss upward instead of doing
// this work in place): each affected account is then reconciled under its own
// sorted [providerScope, proxy/<p>/<a>] scopes — ADR 0009 — so it can never
// race that account's concurrent start/stop/configure, and never violates
// sorted-lock ordering by nesting another account's scopes inside ours.
func (s *Service) reconcileForwardedOnHostLoss(
	ctx context.Context,
	excludeProviderID, excludeAccountName string,
	terminated PluginRuntimeIdentity,
) error {
	rows, err := s.Store.ListProxyStates(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, ref := range rows {
		if ref.Provider == excludeProviderID && ref.Account == excludeAccountName {
			continue // the failing account is reconciled by its own compensation
		}
		if !s.forwardsLifecycle(ref.Provider) {
			continue // built-in proxies are unaffected by a plugin-host kill
		}
		st := ref.State
		if st == nil || st.Endpoint == "" || st.InstanceID == "" {
			continue
		}
		recErr := lock.WithMutationLocks(ctx, s.Store.Root, []string{
			refs.ProviderScope(ref.Provider), proxyScope(ref.Provider, ref.Account),
		}, func(c context.Context) error {
			return s.reconcileAccountAfterHostLoss(c, ref.Provider, ref.Account, *st, terminated)
		}, 0)
		errs = append(errs, recErr)
	}
	return errors.Join(errs...)
}

// reconcileAccountAfterHostLoss re-reads and reconciles ONE account under its
// own held scopes. The row observed during the sweep may be stale: another
// lifecycle could have replaced or removed it before we got the lock, so the
// persisted instance is re-read and compared first.
func (s *Service) reconcileAccountAfterHostLoss(
	ctx context.Context,
	providerID, accountName string,
	observed store.ProxyRuntimeState,
	terminated PluginRuntimeIdentity,
) error {
	state, err := s.Store.ReadProxyState(ctx, providerID, accountName)
	if err != nil {
		return err
	}
	if state == nil {
		// State writes precede lease creation under the same lock hold, so a
		// vanished row cannot hide surviving leases from this lifecycle; any
		// older strays are the stale-lease reaper's job.
		return nil
	}
	if state.InstanceID != observed.InstanceID ||
		!sameEndpointOrigin(state.Endpoint, observed.Endpoint) {
		// Superseded by a newer instance while we waited for the lock: that
		// lifecycle owns the record now, not the terminated host.
		return nil
	}
	dead := s.verifyInstanceDead(state.Endpoint, state.InstanceID)
	if !dead {
		// Alive (or unprovable): a detached survivor answers its own /health
		// echo. Host death proves in-host proxies dead — it says NOTHING about
		// detached children — so an unverifiable row keeps its state, leases
		// and token. Forgetting a live credential-bearing proxy here would be
		// strictly worse than reporting it running one sweep longer.
		return nil
	}
	// Darkness is timing, not death: a detached proxy that was SIGSTOPped,
	// suspended or wedged longer than the verification window misses every
	// probe exactly like a corpse, and would resume afterwards as an
	// untracked credential-bearing listener. Worse, even a PROVABLY dead
	// recorded owner proves nothing about the descendants a detached child
	// may have double-detached (the whole-tree contract below). Only a row
	// classifiable as an in-host listener of a dead runtime generation may be
	// dropped; everything detached or ambiguous keeps its state, leases and
	// token until its echo answers or an explicit stop/start closes it.
	if !inHostGenerationProvablyDead(*state, terminated) {
		return nil
	}
	// Verified dark beyond timing doubt and the recorded owner is provably
	// gone: the store drops the runtime record and every lease as ONE SQLite
	// transaction. A partial failure must never strand half the cleanup,
	// because this sweep enumerates proxy_state — a vanished row ends every
	// later retry for the leases it left behind.
	return s.Store.ClearProxyStateAndLeasesContext(ctx, providerID, accountName)
}

// inHostGenerationProvablyDead accepts only a relation the engine established
// at successful start: the /health responder PID matched the exact supervised
// runtime generation. Plugin-written record shape, validity and numeric PID are
// deliberately irrelevant. Missing metadata (including rows from older builds)
// is ambiguous and therefore retained.
func inHostGenerationProvablyDead(
	st store.ProxyRuntimeState,
	terminated PluginRuntimeIdentity,
) bool {
	return terminated.valid() &&
		st.PluginRuntimeGeneration == terminated.Generation &&
		st.PluginRuntimePID == terminated.PID &&
		st.PID == terminated.PID &&
		!processAlive(terminated.PID)
}

// verifyInstanceDead requires the instance echo to stay absent for a FULL
// verification window — every probe must miss, including a fresh one taken
// after the window boundary. One timed-out or transiently failing probe
// proves nothing: healthInstance returns empty for timeouts, overload and
// response failures exactly as it does for death, and a live detached proxy
// behind one slow moment must not be forgotten because of a single unlucky
// probe. Any hit anywhere in the window keeps the account untouched.
//
// Sustained darkness is still only a HEURISTIC, never proof of death: a
// SIGSTOPped or wedged process misses every probe exactly like a corpse.
// Callers must therefore pair it with durable process-identity evidence
// (inHostGenerationProvablyDead) before dropping detached-capable state.
func (s *Service) verifyInstanceDead(endpoint, instanceID string) bool {
	deadline := time.Now().Add(lifecycleVerifyTimeout)
	for time.Now().Before(deadline) {
		if healthInstance(endpoint) == instanceID {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return healthInstance(endpoint) != instanceID
}

// settleAbandonedListener stops and verifies whatever the failed invocation
// may have bound. proven=false means death could NOT be demonstrated.
//
// With a returned handle the instance echo proves identity. Without one the
// engine only knows where it TOLD the plugin to listen — and for an unsettled
// cancellation that is not enough: the runtime is terminated and the scoped
// ownership record (see proveTerminatedChildrenDark) drives an active hunt —
// strict-verified stops plus adoption of anything that re-binds the recorded
// identity. An empty planned socket alone is never proof, and neither is a
// verified kill: plugin-spawned process trees cannot be proven contained, so
// this path always reports unproven and keeps the diagnostic record behind.
//
// A SETTLED coroutine cannot bind after it ended, so an empty grace window
// counts as proof on the settled paths. An UNSETTLED one (forceTerminate) is
// never trusted to any timing window: the runtime itself is terminated — a
// dead process cannot bind — and only verifiable identities close the record.
func (s *Service) settleAbandonedListener(
	ctx context.Context,
	req PluginRequest,
	started *PluginStart,
	forceTerminate bool,
	expectedRuntime PluginRuntimeIdentity,
	outcome *forwardedCompensation,
) (proven bool, err error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*lifecycleVerifyTimeout)
	defer cancel()

	endpoint, instanceID := "", ""
	if started != nil {
		endpoint, instanceID = started.Endpoint, started.InstanceID
	}

	stopOnce := func() error {
		stopErr := s.PluginLifecycle.StopProxy(cleanupCtx, req)
		if errors.Is(stopErr, ErrPluginMethodAbsent) {
			return nil // nothing this engine can ask for; verification decides
		}
		return stopErr
	}

	if forceTerminate {
		var errs []error
		if terr := s.PluginLifecycle.Terminate(cleanupCtx, expectedRuntime); terr != nil {
			errs = append(errs,
				anyerr.Wrapf(terr, "terminate plugin runtime", "PROXY_STOP_UNVERIFIED"))
		} else if outcome != nil {
			// The shared host is dead and other accounts' proxies may have died
			// with it. Do NOT reconcile them here: this compensation runs while
			// the failing account holds its mutation locks, and touching other
			// accounts' rows without their own scopes would violate ADR 0009.
			// The flag travels up to the lock boundary, which reconciles after
			// releasing them. The identity is the PRE-dispatch capture, so a
			// recovery that replaced the generation in between can never be
			// mistaken for the terminated one.
			outcome.hostTerminated = true
			outcome.terminatedRuntime = expectedRuntime
		}
		if instanceID != "" && endpoint != "" {
			waitHealthGone(endpoint, instanceID, lifecycleVerifyTimeout)
			if healthInstance(endpoint) == instanceID {
				if killErr := killScopedProxyChild(req, endpoint, instanceID); killErr != nil {
					errs = append(errs, killErr)
				}
				waitHealthGone(endpoint, instanceID, lifecycleVerifyTimeout)
				if healthInstance(endpoint) == instanceID {
					errs = append(errs, anyerr.Newf(
						"plugin proxy survived runtime termination", "PROXY_STOP_UNVERIFIED"))
					return false, errors.Join(errs...)
				}
			}
			return len(errs) == 0, errors.Join(errs...)
		}
		// No handle: the scoped ownership record decides (P1: an initially
		// empty planned socket must never stand in for proof — a detached
		// child can sit between spawn and bind with no listener to observe).
		dark, proofErr := s.proveTerminatedChildrenDark(cleanupCtx, req)
		if proofErr != nil {
			errs = append(errs, proofErr)
		}
		return dark && len(errs) == 0, errors.Join(errs...)
	}

	if endpoint != "" && instanceID != "" {
		// Grace window: a cancelled call returns before the host coroutine
		// does; give a late bind a bounded chance to show up. Appearance ends
		// the wait early — the stop+verify below then handles it.
		deadline := time.Now().Add(abandonedStartGraceWindow)
		for !socketOccupied(hostPortOf(endpoint)) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		stopErr := stopOnce()
		if stopErr != nil {
			return false, stopErr
		}
		waitHealthGone(endpoint, instanceID, lifecycleVerifyTimeout)
		if healthInstance(endpoint) == instanceID {
			return false, anyerr.Newf(
				"plugin proxy survived failed-start cleanup", "PROXY_STOP_UNVERIFIED")
		}
		return true, nil
	}

	// No usable identity: watch the planned socket until the grace window ends
	// empty (the planning-time free-port guarantee makes occupancy ours).
	planned := net.JoinHostPort(plannedHost(req.Config.Host), strconv.Itoa(req.Config.Port))
	deadline := time.Now().Add(abandonedStartGraceWindow)
	for {
		occupied := socketOccupied(planned)
		if !occupied && !time.Now().Before(deadline) {
			return true, nil
		}
		if occupied {
			if stopErr := stopOnce(); stopErr != nil {
				return false, stopErr
			}
		}
		if time.Now().After(deadline) {
			if occupied || socketOccupied(planned) {
				return false, anyerr.Newf(
					"failed start left an unverifiable listener on "+planned,
					"PROXY_STOP_UNVERIFIED")
			}
			return true, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func plannedHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// hostPortOf extracts a dialable host:port from an http endpoint; "" when the
// URL is unusable.
func hostPortOf(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

func socketOccupied(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// scopedRecordMaxAge bounds how long a plugin-written ownership record stays
// authoritative for kill decisions. An older record describes a previous
// session's instance; combined with PID reuse its numeric pid must never
// authorize a signal today.
const scopedRecordMaxAge = 24 * time.Hour

// scopedRecordFutureSkew tolerates modest clock differences between the
// plugin host and this process; anything stamped further ahead is malformed.
const scopedRecordFutureSkew = 2 * time.Minute

// scopedOwnershipRecord mirrors the structured proxy.pid document plugins
// persist beside a detached child (recordVersion 1). Unlike the engine's own
// internal/ownership records it carries no command tokens — JS writers cannot
// capture process identity — so the double-probed health echo below is the
// strongest ownership proof available on this boundary.
type scopedOwnershipRecord struct {
	RecordVersion int       `json:"recordVersion"`
	InstanceID    string    `json:"instanceId"`
	PID           int       `json:"pid"`
	Provider      string    `json:"provider"`
	Account       string    `json:"account"`
	Endpoint      string    `json:"endpoint"`
	CreatedAt     time.Time `json:"createdAt"`
}

// loadScopedOwnershipRecord reads and fully validates this account's scoped
// record. An absent file yields (nil, nil). A present-but-invalid record
// yields an error: such a record can neither authorize a signal nor prove
// death, and callers must fail closed on it.
func loadScopedOwnershipRecord(
	req PluginRequest,
	expectedEndpoint, expectedInstanceID string,
) (*scopedOwnershipRecord, error) {
	data, err := os.ReadFile(filepath.Join(req.RuntimeDir, "proxy.pid"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read scoped proxy ownership record: %w", err)
	}
	var rec scopedOwnershipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("scoped proxy ownership record is not valid JSON: %w", err)
	}
	switch {
	case rec.RecordVersion != 1:
		return nil, fmt.Errorf("scoped proxy ownership record version %d is unsupported", rec.RecordVersion)
	case rec.PID <= 0:
		return nil, errors.New("scoped proxy ownership record has no positive pid")
	case len(rec.InstanceID) != 36:
		return nil, errors.New("scoped proxy ownership record has a malformed instance id")
	case rec.Provider != req.ProviderID || rec.Account != req.AccountName:
		return nil, fmt.Errorf("scoped proxy ownership record names %s/%s, not %s/%s",
			rec.Provider, rec.Account, req.ProviderID, req.AccountName)
	case rec.CreatedAt.IsZero():
		return nil, errors.New("scoped proxy ownership record has no creation timestamp")
	case time.Since(rec.CreatedAt) > scopedRecordMaxAge:
		return nil, fmt.Errorf("scoped proxy ownership record is older than %s", scopedRecordMaxAge)
	case time.Until(rec.CreatedAt) > scopedRecordFutureSkew:
		return nil, errors.New("scoped proxy ownership record is stamped in the future")
	}
	parsed, err := url.Parse(rec.Endpoint)
	switch {
	case err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "":
		return nil, fmt.Errorf("scoped proxy ownership record endpoint %q is unusable", rec.Endpoint)
	case parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery ||
		(parsed.Path != "" && parsed.Path != "/") ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/"):
		return nil, fmt.Errorf("scoped proxy ownership record endpoint %q is not a bare origin", rec.Endpoint)
	}
	if err := AssertLoopbackHost(parsed.Hostname()); err != nil {
		return nil, err
	}
	// The record must describe THIS invocation's target. An endpoint or
	// instance that does not match what the engine requested (the planned
	// socket or the returned handle) names someone else's listener, however
	// well formed the record looks.
	if !sameEndpointOrigin(rec.Endpoint, expectedEndpoint) {
		return nil, fmt.Errorf("scoped proxy ownership record endpoint %q does not match requested %q",
			rec.Endpoint, expectedEndpoint)
	}
	if expectedInstanceID != "" && rec.InstanceID != expectedInstanceID {
		return nil, fmt.Errorf("scoped proxy ownership record instance %q does not match requested %q",
			rec.InstanceID, expectedInstanceID)
	}
	return &rec, nil
}

// sameEndpointOrigin compares two http(s) endpoints by scheme, host and
// effective port, ignoring cosmetic spelling (bracketed IPv6, default ports).
func sameEndpointOrigin(a, b string) bool {
	if b == "" {
		return false // without an expectation nothing can be proven equal
	}
	parsedA, errA := url.Parse(a)
	parsedB, errB := url.Parse(b)
	if errA != nil || errB != nil ||
		parsedA.Hostname() == "" || parsedB.Hostname() == "" {
		return false
	}
	return strings.EqualFold(parsedA.Scheme, parsedB.Scheme) &&
		strings.EqualFold(parsedA.Hostname(), parsedB.Hostname()) &&
		effectivePort(parsedA) == effectivePort(parsedB)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// plannedEndpointURL reconstructs the bare origin the engine TOLD the plugin
// to listen on when no handle came back.
func plannedEndpointURL(req PluginRequest) string {
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(plannedHost(req.Config.Host), strconv.Itoa(req.Config.Port)),
	}).String()
}

// killScopedProxyChild stops the detached child named by this account's
// scoped ownership record, but only after that record passes full validation
// against the invocation's expectations. Errors are returned, never swallowed:
// an unverifiable child must surface as PROXY_STOP_UNVERIFIED, not silence.
func killScopedProxyChild(req PluginRequest, expectedEndpoint, expectedInstanceID string) error {
	rec, err := loadScopedOwnershipRecord(req, expectedEndpoint, expectedInstanceID)
	if err != nil || rec == nil {
		return err // absent record: nothing identifiable to stop here
	}
	return stopScopedOwnershipChild(req, rec)
}

// stopScopedOwnershipChild signals ONLY after two consecutive health probes
// positively identify the live listener as the recorded instance served by
// the recorded process — a positive pid match, never a missing or zero pid.
// Every verification gap returns an error and leaves the process untouched:
// a health responder that cannot name its pid, or names another, is exactly
// how an unrelated process gets killed.
//
// Even a verified kill does NOT restore this function to success: positive
// health proves the recorded PID served at probe time, nothing more. The
// recorded child may have detached further processes (a double-detached
// grandchild that binds only after this endpoint goes dark), the engine has
// no job/container handle over plugin-spawned trees, and a dead pid plus one
// dark moment proves neither. The record is therefore deliberately KEPT on
// every path — it stays the engine's identity for future passes — and every
// outcome reports why whole-tree death could not be proven.
func stopScopedOwnershipChild(req PluginRequest, rec *scopedOwnershipRecord) error {
	matches := func(h proxyHealth) bool {
		return h.InstanceID == rec.InstanceID && h.PID > 0 && h.PID == rec.PID
	}
	first := healthOwnership(rec.Endpoint)
	if !matches(first) {
		// A silent health responder with a dead recorded pid is NOT proof the
		// whole process tree is gone: the recorded child may have detached a
		// grandchild that binds only later. Clearing the record here would drop
		// our only handle on that orphan, which then surfaces as a permanent
		// untracked listener. Fail closed — keep the diagnostic record and report
		// unproven so reconciliation can revisit it; never declare the tree dead
		// on a single pid's liveness alone.
		if first.InstanceID == "" && !processAlive(rec.PID) {
			return fmt.Errorf(
				"scoped proxy ownership for pid %d cannot be proven dead: the recorded process is gone but its process tree may still bind later",
				rec.PID)
		}
		return fmt.Errorf("scoped proxy ownership for pid %d cannot be proven at %s (health echoed instance %q pid %d)",
			rec.PID, rec.Endpoint, first.InstanceID, first.PID)
	}
	// Re-check immediately before signalling to close the PID-reuse race.
	second := healthOwnership(rec.Endpoint)
	if !matches(second) {
		return fmt.Errorf("scoped proxy ownership for pid %d changed between probes at %s", rec.PID, rec.Endpoint)
	}
	proc, err := os.FindProcess(rec.PID)
	if err != nil {
		return fmt.Errorf("locate scoped proxy child %d: %w", rec.PID, err)
	}
	if err := proc.Kill(); err != nil && processAlive(rec.PID) {
		return fmt.Errorf("signal scoped proxy child %d: %w", rec.PID, err)
	}
	deadline := time.Now().Add(lifecycleVerifyTimeout)
	for time.Now().Before(deadline) && processAlive(rec.PID) {
		time.Sleep(25 * time.Millisecond)
	}
	if processAlive(rec.PID) {
		return fmt.Errorf("scoped proxy child %d survived SIGKILL", rec.PID)
	}
	for time.Now().Before(deadline) && healthOwnership(rec.Endpoint).InstanceID == rec.InstanceID {
		time.Sleep(25 * time.Millisecond)
	}
	if healthOwnership(rec.Endpoint).InstanceID == rec.InstanceID {
		return fmt.Errorf("scoped proxy endpoint %s still serves instance %q after the recorded child died",
			rec.Endpoint, rec.InstanceID)
	}
	// Verified kill, verified darkness — and STILL not success: containment of
	// everything the child may have spawned is structurally unprovable from
	// here (no job object, no container, escapable process groups), so the
	// record stays behind as the identity for future reconciliation passes.
	return fmt.Errorf(
		"scoped proxy child %d was verifiably stopped but its process tree cannot be proven contained; keeping the scoped record",
		rec.PID)
}

// proveTerminatedChildrenDark closes the no-handle force-termination path.
//
// The hosting runtime is already dead at this point, so only a child it
// managed to detach can bind the planned socket later — and the scoped
// ownership record is the only identity the engine holds for such a child.
// The window below watches that record and its endpoint: a live recorded
// child is strictly stopped (double health-probe, positive pid match), and a
// detached grandchild that re-binds the SAME endpoint and instance after its
// parent dies is adopted into the record — persist first, signal second —
// and then strictly stopped too.
//
// Nothing here ever reports proven. A verified kill of the recorded PID does
// not prove the whole process tree is gone — the engine holds no job,
// container or escape-proof group handle over plugin-spawned trees — so the
// outcome is always unproven and the caller keeps the diagnostic record
// behind (fail closed). The retained record keeps endpoint + instance
// identity alive so later starts and reconciliations can find whatever
// eventually binds.
func (s *Service) proveTerminatedChildrenDark(_ context.Context, req PluginRequest) (bool, error) {
	expected := plannedEndpointURL(req)
	planned := hostPortOf(expected)
	deadline := time.Now().Add(2 * lifecycleVerifyTimeout)
	recordSeen := false
	var lastErr error
	var lastAdoptErr error
	for {
		rec, recErr := loadScopedOwnershipRecord(req, expected, "")
		if recErr != nil {
			lastErr = recErr
		} else if rec != nil {
			recordSeen = true
			// A detached grandchild re-binding the same identity after its
			// parent died claims the instance with ITS OWN pid: adopt it into
			// the record so the strict stop below applies to it too. Only a
			// PERSISTED adoption may steer the signal — until the rewrite is
			// durably on disk, ownership still names the old pid.
			adopted, adoptErr := adoptReboundScopedChild(req, rec)
			if adoptErr != nil {
				// Retried on every pass and surfaced as PROXY_STOP_UNVERIFIED
				// at the end; the rebinder is deliberately NOT signalled.
				lastAdoptErr = adoptErr
			} else {
				lastAdoptErr = nil
				// Verification failures are retried, not fatal: a detached child
				// between spawn and bind serves no health echo yet, but it may
				// still materialise inside the window — or die on its own. A
				// verified kill also reports unproven (tree containment), so the
				// window doubles as a straggler watch either way.
				lastErr = stopScopedOwnershipChild(req, adopted)
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Every outcome here crosses an output boundary as PROXY_STOP_UNVERIFIED;
	// the raw verification detail rides along for diagnosis.
	unproven := func(detail string) error {
		return anyerr.Newf(detail, "PROXY_STOP_UNVERIFIED")
	}
	if !recordSeen {
		if lastErr != nil {
			return false, errors.Join(
				unproven("terminated plugin runtime left no usable scoped ownership record"),
				lastErr)
		}
		if socketOccupied(planned) {
			return false, unproven("failed start left an unverifiable listener on " + planned +
				" and no scoped ownership record identifies it")
		}
		return false, unproven(
			"terminated plugin runtime left no scoped ownership record, so its children cannot be proven dead")
	}
	dark := unproven("terminated plugin runtime left a scoped proxy whose process tree could not be proven contained")
	if lastAdoptErr != nil {
		return false, errors.Join(dark,
			anyerr.Wrapf(lastAdoptErr, "adopt rebound scoped proxy child", "PROXY_STOP_UNVERIFIED"),
			lastErr)
	}
	if lastErr != nil {
		return false, errors.Join(dark, lastErr)
	}
	return false, dark
}

// adoptReboundScopedChild detects an orphaned descendant claiming the
// recorded instance on the recorded endpoint under a NEW live pid. It never
// mutates the loaded record and never signals anything itself: it atomically
// rewrites the scoped record to name the rebinder (staged write + rename) and
// returns that persisted copy for the caller's strict stop. A persistence
// failure returns an error and leaves the old record — and the OLD pid it
// authorizes — untouched on disk, so an unpersisted pid can never be
// signalled while durable ownership still names someone else.
func adoptReboundScopedChild(
	req PluginRequest,
	rec *scopedOwnershipRecord,
) (*scopedOwnershipRecord, error) {
	h := healthOwnership(rec.Endpoint)
	if h.InstanceID != rec.InstanceID || h.PID <= 0 || h.PID == rec.PID || !processAlive(h.PID) {
		return rec, nil // nothing re-bound; the loaded record stays authoritative
	}
	adopted := *rec
	adopted.PID = h.PID
	adopted.CreatedAt = time.Now()
	data, err := json.Marshal(adopted)
	if err != nil {
		return nil, fmt.Errorf("encode adopted scoped proxy record for pid %d: %w", h.PID, err)
	}
	pidPath := filepath.Join(req.RuntimeDir, "proxy.pid")
	if err := atomicfile.Write(pidPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("commit adopted scoped proxy record for pid %d: %w", h.PID, err)
	}
	return &adopted, nil
}

// ValidatePluginStart rejects malformed or unsafe plugin handles at the Go
// boundary: loopback-only endpoints with no credential-bearing URL components
// (userinfo, query, fragment — any of which could smuggle the engine token
// into CLI output), a present instance id, sane fields. A plugin-returned
// token is not part of this struct by construction — the engine owns
// generation and persistence (ADR 0006).
func ValidatePluginStart(started *PluginStart) error {
	if started == nil || started.Endpoint == "" || started.InstanceID == "" {
		return anyerr.Newf(
			"plugin proxy returned an incomplete handle (endpoint and instanceId are required)",
			"PROXY_START_FAILED")
	}
	if len(started.InstanceID) > 128 {
		return anyerr.Newf("plugin proxy returned an oversized instance id", "PROXY_START_FAILED")
	}
	if started.PID < 0 {
		return anyerr.Newf("plugin proxy returned a negative pid", "PROXY_START_FAILED")
	}
	parsed, err := url.Parse(started.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return anyerr.Newf(
			fmt.Sprintf("plugin proxy returned an invalid endpoint %q", started.Endpoint),
			"PROXY_START_FAILED")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return anyerr.Newf(
			fmt.Sprintf("plugin proxy endpoint must be a bare http(s) URL without credentials, query or fragment: %q",
				started.Endpoint),
			"PROXY_START_FAILED")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return anyerr.Newf(
			fmt.Sprintf("plugin proxy endpoint must be a bare http(s) URL without path: %q",
				started.Endpoint),
			"PROXY_START_FAILED")
	}
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return anyerr.Newf(
			fmt.Sprintf("plugin proxy endpoint must be a bare http(s) URL without path: %q",
				started.Endpoint),
			"PROXY_START_FAILED")
	}
	return AssertLoopbackHost(parsed.Hostname())
}

// ownerPidFor keeps the lease reaper honest: OwnerPID names the process that
// HOSTS the listener, so a dead runtime eventually releases its leases. It is
// bookkeeping only — identity decisions always go through the instance echo.
func ownerPidFor(pluginPID int) int {
	if pluginPID > 0 {
		return pluginPID
	}
	return ownPID()
}

// maxRedactedOutput bounds any secret-screened text that crosses an output
// boundary (invariant #2): provider-controlled strings stay bounded frames.
const maxRedactedOutput = 128 << 10

// credentialPattern screens bearer/API-key/token assignments even when the
// exact engine secret is not among the known values.
var credentialPattern = regexp.MustCompile(
	`(?i)(bearer\s+|x-api-key\s*[:=]\s*|authorization\s*[:=]\s*|api[-_]?key\s*[:=]\s*|token\s*[:=]\s*)([a-z0-9._~+/=-]{16,})`)

// RedactSecrets removes the given literal secrets plus generic credential
// assignments from provider-controlled text and bounds its length.
func RedactSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = credentialPattern.ReplaceAllString(text, "${1}[redacted]")
	if len(text) > maxRedactedOutput {
		text = text[:maxRedactedOutput] + "…[truncated]"
	}
	return text
}

// redactErr returns err with its message screened; structured codes and the
// error chain survive so exit codes and errors.As keep working. Unchanged
// messages pass the original error through untouched, preserving sentinels
// like context.Canceled for errors.Is.
func redactErr(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	screened := RedactSecrets(err.Error(), secrets)
	if screened == err.Error() {
		return err
	}
	var structured *anyerr.Error
	if errors.As(err, &structured) {
		clone := *structured
		clone.Message = screened
		return &clone
	}
	return errors.New(screened)
}

// accountSecrets lists every engine-owned secret that could leak through this
// provider/account's output: the persisted runtime token plus in-flight ones.
func (s *Service) accountSecrets(providerID, accountName string, extra ...string) []string {
	secrets := append([]string(nil), extra...)
	if state, err := s.Store.ReadProxyState(context.Background(), providerID, accountName); err == nil &&
		state != nil && state.Token != "" {
		secrets = append(secrets, state.Token)
	}
	return secrets
}

// stopForwardedLocked stops a forwarding provider's proxy. Termination is
// requested through the plugin and VERIFIED through the instance echo — the
// numeric PID is never signalled and never trusted (ADR 0003). Unverified
// termination fails closed and keeps state + leases.
func (s *Service) stopForwardedLocked(ctx context.Context, providerID, accountName string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*lifecycleVerifyTimeout)
	defer cancel()

	cfg, err := s.Store.GetProxyConfig(cleanupCtx, providerID, accountName)
	if err != nil {
		return err
	}
	// TS builds the stop context without a token; nothing here hands the secret
	// back across the boundary on a stop.
	req := s.pluginRequest(providerID, accountName, cfg, "")

	state, err := s.Store.ReadProxyState(cleanupCtx, providerID, accountName)
	if err != nil {
		return err
	}
	var instanceID, endpoint string
	owned := false
	if state != nil && state.Endpoint != "" && state.InstanceID != "" {
		instanceID = state.InstanceID
		endpoint = state.Endpoint
		owned = healthInstance(endpoint) == instanceID
	}

	// TS forwards stopProxy unconditionally; the method must not throw when the
	// proxy is already stopped.
	forwardErr := s.PluginLifecycle.StopProxy(ctx, req)
	if forwardErr != nil && !errors.Is(forwardErr, ErrPluginMethodAbsent) {
		forwardErr = redactErr(forwardErr, s.accountSecrets(providerID, accountName))
	}
	if owned {
		switch {
		case errors.Is(forwardErr, ErrPluginMethodAbsent):
			return anyerr.Newf(
				fmt.Sprintf("Provider %s does not implement stopProxy, so its running proxy for %s cannot be stopped safely.",
					providerID, accountName),
				"PROXY_STOP_UNVERIFIED",
				anyerr.WithSuggestions(
					"Add stopProxy to the plugin provider, or remove the plugin to release its listener."))
		case forwardErr != nil:
			return anyerr.Wrapf(forwardErr, "plugin proxy stop", "PROXY_STOP_FAILED")
		}
		waitHealthGone(endpoint, instanceID, lifecycleVerifyTimeout)
		if healthInstance(endpoint) == instanceID {
			// Fail closed like TS PROXY_STOP_UNVERIFIED: never erase the record
			// of a credential-bearing proxy we could not verify dead.
			return anyerr.Newf(
				"proxy did not stop after the plugin reported success", "PROXY_STOP_UNVERIFIED")
		}
	} else if forwardErr != nil && !errors.Is(forwardErr, ErrPluginMethodAbsent) {
		return anyerr.Wrapf(forwardErr, "plugin proxy stop", "PROXY_STOP_FAILED")
	}

	// Verified stopped (or the records were already stale): drop the runtime
	// state and every lease as ONE SQLite transaction. The host-loss path uses
	// the same store-owned commit; this stop path must too. A half-landed
	// cleanup — state row gone while the leases survive, or vice versa — strands
	// the survivors: reconciliation enumerates proxy_state, so an account whose
	// row vanished is never revisited (ADR 0004 discipline). The helper no-ops
	// the state row when it is absent, so it covers both the state-present and
	// the leases-only cases without a second branch.
	return s.Store.ClearProxyStateAndLeasesContext(cleanupCtx, providerID, accountName)
}

// statusForwarded ports the provider.proxyStatus branch plus the shared
// post-processing: the configured bind address always surfaces; a matched live
// runtimeState overrides endpoint/port/host while that exact process serves.
func (s *Service) statusForwarded(
	ctx context.Context,
	providerID, accountName string,
	cfg store.ProxyConfig,
) (*Status, error) {
	out := &Status{Enabled: cfg.Enabled}
	report, err := s.PluginLifecycle.ProxyStatus(ctx, s.pluginRequest(providerID, accountName, cfg, ""))
	switch {
	case err == nil:
		out.Running = report.Running
		out.Endpoint = report.Endpoint
		out.Compatibility = report.Compatibility
		out.PID = report.PID
		out.LogPath = report.LogPath
		out.Detail = report.Detail
	case errors.Is(err, ErrPluginMethodAbsent):
		// TS falls back to pid-file heuristics; the Go equivalent proves
		// liveness through the persisted instance echo instead.
		state, serr := s.Store.ReadProxyState(ctx, providerID, accountName)
		if serr != nil {
			return nil, serr
		}
		if state != nil && state.Endpoint != "" && state.InstanceID != "" &&
			healthInstance(state.Endpoint) == state.InstanceID {
			out.Running = true
			out.Endpoint = state.Endpoint
			out.Compatibility = state.Compatibility
			out.PID = state.PID
		} else {
			out.Detail = "stopped"
		}
	default:
		return nil, redactErr(err, s.accountSecrets(providerID, accountName))
	}

	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	out.Host = host
	if cfg.Port > 0 {
		out.Port = cfg.Port
	}
	if out.Running && out.PID > 0 {
		if state, serr := s.Store.ReadProxyState(ctx, providerID, accountName); serr == nil &&
			state != nil && state.Endpoint != "" && state.PID == out.PID {
			out.Endpoint = state.Endpoint
			if p := portOf(state.Endpoint); p > 0 {
				out.Port = p
			}
			if parsed, perr := url.Parse(state.Endpoint); perr == nil && parsed.Hostname() != "" {
				out.Host = parsed.Hostname()
			}
		}
	}
	if out.Running && out.Endpoint == "" && out.Port > 0 {
		out.Endpoint = fmt.Sprintf("http://%s:%d", normalizedLoopback(host), out.Port)
	}
	// Secret screening is the LAST step: overlays above can reintroduce raw
	// stored strings (e.g. the runtime-state endpoint) after an earlier pass.
	secrets := s.accountSecrets(providerID, accountName)
	for _, field := range []*string{&out.Endpoint, &out.Compatibility, &out.LogPath, &out.Detail} {
		*field = RedactSecrets(*field, secrets)
	}
	return out, nil
}

// ProxyLogs returns recent, secret-screened proxy log text. Forwarding
// providers answer through readProxyLogs; everyone else falls back to the
// engine's own log file, like src/core/proxy-service.ts proxyLogs. Provider
// text is untrusted: the exact persisted runtime token plus generic
// credential patterns are redacted before anything crosses an output boundary.
func (s *Service) ProxyLogs(ctx context.Context, providerID, accountName string, lines int) (string, error) {
	cfg, err := s.Store.GetProxyConfig(ctx, providerID, accountName)
	if err != nil {
		return "", err
	}
	secrets := s.accountSecrets(providerID, accountName)
	if s.forwardsLifecycle(providerID) {
		text, err := s.PluginLifecycle.ReadProxyLogs(
			ctx, s.pluginRequest(providerID, accountName, cfg, ""), lines)
		if !errors.Is(err, ErrPluginMethodAbsent) {
			if err != nil {
				return "", redactErr(err, secrets)
			}
			return RedactSecrets(text, secrets), nil
		}
	}
	return RedactSecrets(tailLogFile(paths.ProxyLogPath(s.Store.Root, providerID, accountName), lines), secrets), nil
}

// tailLogFile mirrors the CLI's historical fallback: empty output when no log
// exists yet, otherwise the trailing `lines` entries.
func tailLogFile(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

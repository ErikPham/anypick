package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// StartResult mirrors the subset of ProxyHandle the activation flow consumes.
// Token is owner-only material (invariant #2): never print it, never put it
// in JSON output boundaries.
type StartResult struct {
	Endpoint   string
	Token      string
	LeaseID    string
	InstanceID string
	Port       int
	StartedNow bool
}

// Status mirrors ProxyStatus.
type Status struct {
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	Port          int    `json:"port,omitempty"`
	Host          string `json:"host,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
	PID           int    `json:"pid,omitempty"`
	LogPath       string `json:"logPath,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// Upstream describes where the builtin proxy forwards traffic.
// Upstream describes where traffic forwards. OpenAI is the primary surface;
// Anthropic is optional — when present, POST /v1/messages forwards natively
// instead of being translated.
type Upstream struct {
	BaseURL *url.URL
	Auth    string // credential injected into forwarded requests
	// AuthHeader / AuthValuePrefix present the credential per the vendor's
	// scheme; empty = Authorization: Bearer.
	AuthHeader      string
	AuthValuePrefix string
	// Anthropic: native /v1/messages target. When nil and TranslateAnthropic
	// is set, messages requests are translated into chat completions instead.
	Anthropic *UpstreamTarget
	// TranslateAnthropic: upstream speaks OpenAI only; POST /v1/messages is
	// translated into chat completions.
	TranslateAnthropic bool
	// QuotaErrorPatterns: provider-declared regexes that mark a failure as
	// quota-class for pool failover (backend.ts isExhausted's
	// definition.quotaErrorPatterns branch). Empty → classification falls back
	// to the built-in 401/402/balance/credential-quota rules only.
	QuotaErrorPatterns []string
}

// UpstreamTarget is one protocol-specific forwarding target.
type UpstreamTarget struct {
	BaseURL         *url.URL
	Auth            string // empty → inherit Upstream.Auth
	AuthHeader      string // empty → Authorization
	AuthValuePrefix string // empty → "Bearer "
}

// UpstreamResolver lets each provider own its forwarding target without the
// proxy package importing providers (core must not grow per-provider branches).
type UpstreamResolver interface {
	ResolveUpstream(ctx context.Context, providerID, accountName string) (*Upstream, error)
}

// Spawner launches a detached child that serves the proxy after this process
// exits (the Go equivalent of TS detached spawn). nil → in-process serving,
// used by library consumers and tests.
type Spawner func(providerID, account string, st store.ProxyRuntimeState, logPath string) (pid int, err error)

// PoolSpawner launches a detached `_serve-pool` child for a provider pool.
type PoolSpawner func(providerID string) (pid int, err error)

// ErrPluginMethodAbsent reports that a forwarding provider does not implement
// one optional lifecycle method. TS keeps stopProxy/proxyStatus/readProxyLogs
// optional next to a present startProxy, so the caller decides the fallback.
var ErrPluginMethodAbsent = errors.New("plugin provider does not implement this optional proxy method")

// PluginRequest carries exactly the TS ProxyContext fields across the plugin
// boundary. Token is engine-generated (ADR 0006): it is handed OUT to the
// provider and never accepted back from it.
type PluginRequest struct {
	ProviderID  string
	AccountName string
	SnapshotDir string
	RuntimeDir  string
	Config      store.ProxyConfig
	Token       string
}

// PluginStart is the validated subset of a plugin ProxyHandle that the Service
// consumes. The plugin's own token field is deliberately absent: the Service
// persists only its own generated secret. Runtime is engine-owned identity of
// the host that actually served the call (filled by the composition adapter,
// never decoded from plugin JSON) so a not-dispatched recovery cannot stamp
// generation A's identity onto generation B's listener.
type PluginStart struct {
	Endpoint      string
	Compatibility string
	PID           int
	InstanceID    string
	LogPath       string
	Runtime       PluginRuntimeIdentity
}

// PluginRuntimeIdentity identifies one exact engine-supervised plugin host.
// Generation prevents PID reuse from associating state with a later host.
type PluginRuntimeIdentity struct {
	Generation string
	PID        int
}

func (id PluginRuntimeIdentity) valid() bool { return id.Generation != "" && id.PID > 0 }

// IsValid reports whether the identity names an exact supervised runtime.
// Exported for composition-root callers (recovery termination guards) that
// cannot reach the unexported form across package boundaries.
func (id PluginRuntimeIdentity) IsValid() bool { return id.valid() }

// pluginRuntimeIdentitySource is an optional engine-side capability. Third-
// party plugin output cannot implement or influence it.
type pluginRuntimeIdentitySource interface {
	RuntimeIdentity() PluginRuntimeIdentity
}

func runtimeIdentityOf(lifecycle PluginLifecycle) PluginRuntimeIdentity {
	source, ok := lifecycle.(pluginRuntimeIdentitySource)
	if !ok {
		return PluginRuntimeIdentity{}
	}
	return source.RuntimeIdentity()
}

// PluginLifecycle forwards per-account lifecycle calls to providers that own
// their own compatibility-proxy implementation (arbitrary JavaScript plugins).
// It is transport-only by contract: tokens (ADR 0006), instance-identity
// verification (ADR 0003), leases and persisted records stay with the Service.
// The composition root wires an implementation over the sealed provider
// registry; nil keeps every provider on the built-in engine path.
type PluginLifecycle interface {
	// ForwardsLifecycle reports whether the provider owns its proxy lifecycle —
	// presence of startProxy on the adapter, never inferred from clients or
	// protocols.
	ForwardsLifecycle(providerID string) bool
	StartProxy(ctx context.Context, req PluginRequest) (*PluginStart, error)
	// StopProxy, ProxyStatus and ReadProxyLogs return ErrPluginMethodAbsent
	// when the provider leaves the optional method unimplemented.
	StopProxy(ctx context.Context, req PluginRequest) error
	ProxyStatus(ctx context.Context, req PluginRequest) (*Status, error)
	ReadProxyLogs(ctx context.Context, req PluginRequest, lines int) (string, error)
	// Terminate kills whatever runtime hosts the plugin's listeners WITHOUT
	// relying on cooperation. Last resort for cancelled starts whose
	// settlement could not be confirmed: no timing window can prove a
	// coroutine ended, but its process dying can. The expected identity MUST
	// be captured BEFORE dispatching the failed call: a stale generation is
	// refused (never killing a replacement that other accounts now depend on).
	// Must be idempotent.
	Terminate(ctx context.Context, expected PluginRuntimeIdentity) error
}

// Service manages per-account builtin proxies. State persists in proxy_state
// + proxy_leases so crashes leave recoverable records; every start runs under
// sorted scope locks (ADR 0009); ownership across processes is proven through
// an unauthenticated /health echo of the per-instance id (PROC-01) — a stale
// pid record must never adopt someone else's process.
type Service struct {
	Store    *store.AccountStore
	Jobs     *LeaseStore
	Resolver UpstreamResolver
	// Spawner enables detached-child mode. Composition wires ExecSpawner;
	// nil keeps everything in-process.
	Spawner Spawner
	// PoolSpawner enables detached `_serve-pool` children; nil keeps pools
	// in-process (tests/library).
	PoolSpawner PoolSpawner
	// QuotaGuardPolicy reads the validated quota-guard settings; nil → the
	// TS defaults (disabled, 60-minute cooldown).
	QuotaGuardPolicy func(ctx context.Context) (enabled bool, cooldownMs int64)
	// DefaultPort returns a provider's advertised default proxy port
	// (CompatibleProviderDefinition.defaultProxyPort); 0 when unknown or
	// unwired. Composition-root wiring only — core proxy never imports
	// providers (capability routing lives in the adapters, invariant #1).
	DefaultPort func(providerID string) int
	// PluginLifecycle forwards lifecycle operations for providers that own
	// their own proxy implementation (JS plugins). Composition wiring only;
	// nil keeps the built-in engine path for everyone.
	PluginLifecycle PluginLifecycle
	// Pools owns the pools table; required for pool lifecycle methods.
	Pools *pools.Store

	servers     *registry
	poolServers *poolServerRegistry
}

func New(st *store.AccountStore, leases *LeaseStore) *Service {
	return &Service{Store: st, Jobs: leases, servers: newRegistry(), poolServers: newPoolServerRegistry()}
}

// Close shuts down every in-process server this Service owns. Detached
// children are intentionally left running: their leases keep them recoverable.
// Idempotent — safe to call from app.Close on every exit path.
func (s *Service) Close() {
	for _, srv := range s.servers.removeAll() {
		_ = srv.Close()
	}
	for _, srv := range s.poolServers.removeAll() {
		_ = srv.Close()
	}
}

const healthTimeout = 5 * time.Second

// Configure updates one account's proxy preference under the same scopes used
// by start/stop. A zero port asks the kernel for a free loopback port when
// enabling; disabled configurations may keep zero as their auto-port choice.
func (s *Service) Configure(
	ctx context.Context,
	providerID, accountName string,
	enabled bool,
	port int,
) (store.ProxyConfig, error) {
	if port < 0 || port > 65535 {
		return store.ProxyConfig{}, anyerr.Newf(
			"proxy port must be between 0 and 65535",
			"INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage),
		)
	}
	var result store.ProxyConfig
	err := lock.WithMutationLocks(ctx, s.Store.Root, []string{
		refs.ProviderScope(providerID), proxyScope(providerID, accountName),
	}, func(c context.Context) error {
		cfg, err := s.Store.GetProxyConfigContext(c, providerID, accountName)
		if err != nil {
			return err
		}
		cfg.Enabled = enabled
		if port != 0 {
			cfg.Port = port
		}
		if cfg.Enabled && cfg.Port == 0 {
			cfg.Port, err = PickFreePort()
			if err != nil {
				return anyerr.Wrapf(err, "pick port", "PROXY_START_FAILED")
			}
		}
		if err := s.Store.SetProxyConfigContext(c, providerID, accountName, cfg); err != nil {
			return err
		}
		result = cfg
		return nil
	}, 0)
	return result, err
}

// StartProxy starts (or reuses) the builtin proxy for an account under
// [providerScope, proxy/<p>/<a>] locks. Reuse requires the persisted instance
// id to match the running server — a stale pid record must never adopt
// someone else's process (PROC-01).
func (s *Service) StartProxy(ctx context.Context, providerID, accountName string) (*StartResult, error) {
	var result *StartResult
	// Carries shared-state facts (host termination) out of the locked callback:
	// their consequences touch OTHER accounts and may only run once these
	// locks are released.
	outcome := &forwardedCompensation{}
	err := lock.WithMutationLocks(ctx, s.Store.Root, []string{
		refs.ProviderScope(providerID), proxyScope(providerID, accountName),
	}, func(c context.Context) error {
		r, runErr := s.startLocked(c, providerID, accountName, outcome)
		if runErr != nil {
			return runErr
		}
		result = r
		return nil
	}, 0)
	if outcome.hostTerminated {
		// Locks are released: reconcile every other forwarding account whose
		// proxy was served by the terminated runtime. Detached from the
		// caller's context so a cancelled client cannot strand other accounts'
		// rows in the just-killed host's state.
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*lifecycleVerifyTimeout)
		defer cancel()
		err = errors.Join(err, s.reconcileForwardedOnHostLoss(
			reconcileCtx, providerID, accountName, outcome.terminatedRuntime))
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) startLocked(ctx context.Context, providerID, accountName string, outcome *forwardedCompensation) (*StartResult, error) {
	account, err := s.Store.GetContext(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, anyerr.Newf(
			fmt.Sprintf(`No account "%s" for provider "%s".`, accountName, providerID),
			"ACCOUNT_NOT_FOUND", anyerr.WithExitCode(anyerr.NotFound))
	}

	// Providers that own their proxy implementation (JS plugins) keep the same
	// Service contract — locks, tokens, leases, records — while the transport
	// call itself crosses the plugin boundary.
	if s.forwardsLifecycle(providerID) {
		return s.startForwardedLocked(ctx, providerID, accountName, account.Proxy, outcome)
	}

	// Same-process reuse.
	if existing, ok := s.servers.get(providerID, accountName); ok {
		state, stateErr := s.Store.ReadProxyStateContext(ctx, providerID, accountName)
		if stateErr != nil {
			return nil, stateErr
		}
		if state != nil && state.InstanceID == existing.InstanceID {
			return &StartResult{
				Endpoint: existing.Endpoint, Token: existing.Token,
				InstanceID: existing.InstanceID, Port: portOf(existing.Endpoint),
				StartedNow: false,
			}, nil
		}
		// Stale registry entry — drop it.
		_ = existing.Close()
		_, _ = s.servers.remove(providerID, accountName)
	}

	// Cross-process reuse: a detached child from an earlier invocation may
	// still be healthy. Adopt only when /health echoes OUR recorded instance
	// id — never trust the pid alone (PROC-01).
	state, err := s.Store.ReadProxyStateContext(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}
	if state != nil && state.Endpoint != "" && state.InstanceID != "" &&
		state.Token != "" {
		if live := healthInstance(state.Endpoint); live == state.InstanceID {
			return &StartResult{
				Endpoint: state.Endpoint, Token: state.Token,
				InstanceID: state.InstanceID, Port: portOf(state.Endpoint),
				StartedNow: false,
			}, nil
		}
	}

	originalCfg := account.Proxy
	cfg := originalCfg
	if !cfg.Enabled {
		cfg.Enabled = true
	}
	if cfg.Port == 0 {
		port, perr := PickFreePort()
		if perr != nil {
			return nil, anyerr.Wrapf(perr, "pick port", "PROXY_START_FAILED")
		}
		cfg.Port = port
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port

	upstream, err := s.Resolver.ResolveUpstream(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}

	instanceID, err := NewInstanceID()
	if err != nil {
		return nil, anyerr.Wrapf(err, "generate instance id", "PROXY_START_FAILED")
	}
	token, err := NewToken()
	if err != nil {
		return nil, anyerr.Wrapf(err, "generate proxy token", "PROXY_START_FAILED")
	}
	endpoint := fmt.Sprintf("http://%s:%d", normalizedLoopback(host), port)
	if err := s.Store.SetProxyConfigContext(ctx, providerID, accountName, cfg); err != nil {
		return nil, err
	}

	// Persist BEFORE spawning so the child can read its own credentials and
	// so a parent crash leaves a coherent record.
	st := store.ProxyRuntimeState{
		AccountName: accountName, Endpoint: endpoint,
		PID: 0, InstanceID: instanceID, StartedAt: nowISO(), Token: token,
	}
	if err := s.Store.WriteProxyStateContext(ctx, providerID, accountName, st); err != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg)
		return nil, errors.Join(err, cleanupErr)
	}

	var pid int
	if s.Spawner != nil {
		logPath := paths.ProxyLogPath(s.Store.Root, providerID, accountName)
		pid, err = s.Spawner(providerID, accountName, st, logPath)
		if err != nil {
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg)
			return nil, errors.Join(
				anyerr.Wrapf(err, "spawn proxy child", "PROXY_START_FAILED"),
				cleanupErr,
			)
		}
	} else {
		// In-process mode (tests/library): same lifecycle as before.
		server := s.buildServer(providerID, accountName, instanceID, token, upstream)
		if err := server.Listen(host, port); err != nil {
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg)
			return nil, errors.Join(err, cleanupErr)
		}
		if err := server.WaitReady(healthTimeout); err != nil {
			_ = server.Close()
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg)
			return nil, errors.Join(err, cleanupErr)
		}
		s.servers.put(providerID, accountName, server)
		pid = ownPID()
	}

	// Ownership proof before declaring success: the server must echo OUR new
	// instance id, whatever process hosts it.
	if err := waitHealthInstance(endpoint, instanceID, healthTimeout); err != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, "", originalCfg)
		return nil, errors.Join(err, cleanupErr)
	}

	leaseID, lErr2 := NewInstanceID()
	if lErr2 != nil {
		// Lease is bookkeeping; proxy already healthy - don't tear it down.
		leaseID = instanceID
	}
	// Persist the serving pid before publishing a lease or returning success.
	st.PID = pid
	if err := s.Store.WriteProxyStateContext(ctx, providerID, accountName, st); err != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, "", originalCfg)
		return nil, errors.Join(err, cleanupErr)
	}

	if lErr := s.Jobs.CreateContext(ctx, Lease{
		LeaseID: leaseID, Provider: providerID, Account: accountName,
		Port: portOf(endpoint), Host: host, Endpoint: endpoint,
		OwnerPID: pid, InstanceID: instanceID,
	}); lErr != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, leaseID, originalCfg)
		return nil, errors.Join(lErr, cleanupErr)
	}

	return &StartResult{
		Endpoint: endpoint, Token: token,
		LeaseID: leaseID, InstanceID: instanceID,
		Port: portOf(endpoint), StartedNow: true,
	}, nil
}

// abortStart compensates every mutation made by a failed start. The cleanup
// context retains the caller's held-lock identity while ignoring cancellation,
// so rollback can finish without racing another provider mutation.
func (s *Service) abortStart(
	ctx context.Context,
	providerID, accountName, instanceID, endpoint string,
	pid int,
	leaseID string,
	originalCfg store.ProxyConfig,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*healthTimeout)
	defer cancel()

	var cleanupErrs []error
	stopped := true
	if server, ok := s.servers.get(providerID, accountName); ok && server.InstanceID == instanceID {
		_, _ = s.servers.remove(providerID, accountName)
		if err := server.Close(); err != nil {
			cleanupErrs = append(cleanupErrs, err)
			stopped = false
		}
	} else if s.Spawner != nil && pid > 0 && processAlive(pid) {
		if err := signalPID(pid); err != nil {
			cleanupErrs = append(cleanupErrs, err)
			stopped = false
		} else {
			deadline := time.Now().Add(healthTimeout)
			for time.Now().Before(deadline) && healthInstance(endpoint) == instanceID {
				time.Sleep(50 * time.Millisecond)
			}
			if healthInstance(endpoint) == instanceID {
				cleanupErrs = append(cleanupErrs, anyerr.Newf(
					"proxy process survived failed-start cleanup",
					"PROXY_STOP_FAILED",
				))
				stopped = false
			}
		}
	}

	if stopped {
		if leaseID != "" {
			cleanupErrs = append(cleanupErrs, s.Jobs.DeleteContext(cleanupCtx, leaseID))
		}
		cleanupErrs = append(
			cleanupErrs,
			s.Store.ClearProxyStateContext(cleanupCtx, providerID, accountName),
		)
	}
	cleanupErrs = append(
		cleanupErrs,
		s.Store.SetProxyConfigContext(cleanupCtx, providerID, accountName, originalCfg),
	)
	return errors.Join(cleanupErrs...)
}

func (s *Service) buildServer(providerID, accountName, instanceID, token string, upstream *Upstream) *Server {
	return &Server{
		ProviderID:              providerID,
		AccountName:             accountName,
		InstanceID:              instanceID,
		Token:                   token,
		Upstream:                upstream.BaseURL,
		UpstreamAuth:            upstream.Auth,
		UpstreamAuthHeader:      upstream.AuthHeader,
		UpstreamAuthValuePrefix: upstream.AuthValuePrefix,
		TranslateAnthropic:      upstream.TranslateAnthropic,
		Anthropic:               upstream.Anthropic,
	}
}

// Stop stops an account's proxy — in-process when hosted here, otherwise the
// detached child, but ONLY after /health proves the running instance is the
// one our state recorded (PROC-01).
func (s *Service) Stop(ctx context.Context, providerID, accountName string) error {
	return lock.WithMutationLocks(ctx, s.Store.Root, []string{
		refs.ProviderScope(providerID), proxyScope(providerID, accountName),
	}, func(c context.Context) error {
		return s.stopLocked(c, providerID, accountName)
	}, 0)
}

func (s *Service) stopLocked(ctx context.Context, providerID, accountName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.forwardsLifecycle(providerID) {
		return s.stopForwardedLocked(ctx, providerID, accountName)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*healthTimeout)
	defer cancel()

	if server, ok := s.servers.remove(providerID, accountName); ok {
		closeErr := server.Close()
		// Drop the runtime state and every lease as ONE SQLite transaction;
		// a half-landed cleanup would strand the survivors (ADR 0004 discipline).
		clearErr := s.Store.ClearProxyStateAndLeasesContext(cleanupCtx, providerID, accountName)
		return errors.Join(closeErr, clearErr)
	}

	state, err := s.Store.ReadProxyStateContext(cleanupCtx, providerID, accountName)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	health := healthOwnership(state.Endpoint)
	if health.InstanceID != state.InstanceID {
		// Not ours / already dead: drop stale records atomically.
		return s.Store.ClearProxyStateAndLeasesContext(cleanupCtx, providerID, accountName)
	}
	pid := health.PID
	if pid <= 0 {
		pid = state.PID
	}
	if pid > 0 && processAlive(pid) {
		if err := signalPID(pid); err != nil {
			return anyerr.Wrapf(err, "signal proxy child", "PROXY_STOP_FAILED")
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) &&
			healthInstance(state.Endpoint) == state.InstanceID {
			time.Sleep(100 * time.Millisecond)
		}
		if healthInstance(state.Endpoint) == state.InstanceID {
			return anyerr.Newf(
				"proxy process did not stop after SIGTERM",
				"PROXY_STOP_FAILED",
			)
		}
	}
	// Verified dead: drop the runtime state and every lease as ONE SQLite
	// transaction, so neither half can strand the other (ADR 0004 discipline).
	return s.Store.ClearProxyStateAndLeasesContext(cleanupCtx, providerID, accountName)
}

func (s *Service) cleanupLeases(providerID, accountName, instanceID string) error {
	return s.cleanupLeasesContext(context.Background(), providerID, accountName, instanceID)
}

func (s *Service) cleanupLeasesContext(ctx context.Context, providerID, accountName, instanceID string) error {
	leases, lErr := s.Jobs.ListByAccountContext(ctx, providerID, accountName)
	if lErr != nil {
		return lErr
	}
	for _, l := range leases {
		if l.InstanceID == instanceID {
			if err := s.Jobs.DeleteContext(ctx, l.LeaseID); err != nil {
				return err
			}
		}
	}
	return nil
}

// StopIfLeaseOwned ports stopProxyIfLeaseOwned semantics: stop only when the
// caller presents the instance id this activation created.
func (s *Service) StopIfLeaseOwned(ctx context.Context, providerID, accountName, instanceID, leaseID string) error {
	if instanceID == "" {
		return nil
	}
	return lock.WithMutationLocks(ctx, s.Store.Root, []string{
		refs.ProviderScope(providerID), proxyScope(providerID, accountName),
	}, func(c context.Context) error {
		if server, ok := s.servers.get(providerID, accountName); ok && server.InstanceID == instanceID {
			return s.stopLocked(c, providerID, accountName)
		}
		// Detached child: stop only when the recorded instance matches.
		state, err := s.Store.ReadProxyStateContext(c, providerID, accountName)
		if err != nil {
			return err
		}
		if state != nil && state.InstanceID == instanceID {
			return s.stopLocked(c, providerID, accountName)
		}
		if leaseID != "" {
			return s.Jobs.DeleteContext(c, leaseID)
		}
		return nil
	}, 0)
}

// Status reports enablement and liveness without ever exposing the token.
// Liveness crosses processes through the instance echo, never the pid alone.
func (s *Service) Status(providerID, accountName string) (*Status, error) {
	return s.StatusContext(context.Background(), providerID, accountName)
}

func (s *Service) StatusContext(ctx context.Context, providerID, accountName string) (*Status, error) {
	cfg, err := s.Store.GetProxyConfigContext(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}
	if s.forwardsLifecycle(providerID) {
		return s.statusForwarded(ctx, providerID, accountName, cfg)
	}
	out := &Status{Enabled: cfg.Enabled}
	if server, ok := s.servers.get(providerID, accountName); ok {
		out.Running = true
		out.Endpoint = server.Endpoint
		out.Port = portOf(server.Endpoint)
		out.PID = ownPID()
		return out, nil
	}
	state, err := s.Store.ReadProxyStateContext(ctx, providerID, accountName)
	if err != nil {
		return nil, err
	}
	if state != nil && state.Endpoint != "" {
		health := healthOwnership(state.Endpoint)
		if health.InstanceID != state.InstanceID {
			return out, nil
		}
		out.Running = true
		out.Endpoint = state.Endpoint
		out.Port = portOf(state.Endpoint)
		out.PID = health.PID
		if out.PID <= 0 {
			out.PID = state.PID
		}
	}
	return out, nil
}

// healthInstance returns the instanceId a live proxy echoes on its
// unauthenticated /health route; "" when unreachable or different-shaped.
func healthInstance(endpoint string) string {
	return healthOwnership(endpoint).InstanceID
}

type proxyHealth struct {
	InstanceID string `json:"instanceId"`
	PID        int    `json:"pid"`
}

func healthOwnership(endpoint string) proxyHealth {
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/health")
	if err != nil {
		return proxyHealth{}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return proxyHealth{}
	}
	var payload proxyHealth
	if json.Unmarshal(raw, &payload) != nil {
		return proxyHealth{}
	}
	return payload
}

// waitHealthInstance polls until /health echoes want or the timeout expires.
func waitHealthInstance(endpoint, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := healthInstance(endpoint); got == want {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return anyerr.Newf("proxy did not become healthy at "+endpoint, "PROXY_START_FAILED")
}

func portOf(endpoint string) int {
	var port int
	if _, err := fmt.Sscanf(endpoint, "http://127.0.0.1:%d", &port); err == nil {
		return port
	}
	return 0
}

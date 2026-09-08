package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/egress"
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
	// AuthProvider, when set, supplies the live credential per request so the
	// proxy can refresh short-lived tokens (OIDC/OAuth) without a restart.
	// Static Auth is ignored while AuthProvider is non-nil. It must be safe to
	// call concurrently and may block on a token refresh.
	AuthProvider func(ctx context.Context) (string, error)
	// ExtraHeaders are injected into every forwarded request (vendor probes,
	// client-version pins). They never carry the credential.
	ExtraHeaders []ExtraHeader
	// Anthropic: native /v1/messages target. When nil and TranslateAnthropic
	// is set, messages requests are translated into chat completions instead.
	Anthropic *UpstreamTarget
	// TranslateAnthropic: upstream speaks OpenAI only; POST /v1/messages is
	// translated into chat completions.
	TranslateAnthropic bool
	// TranslateGemini: upstream is Google Gemini (generateContent), which is
	// neither OpenAI- nor Anthropic-compatible. POST /v1/chat/completions,
	// /v1/responses and /v1/messages are each translated to a Gemini
	// generateContent request and the response translated back. Gemini is the
	// outlier backend that needs a full translate-and-reissue egress, so it
	// gets its own branch rather than riding TranslateAnthropic.
	TranslateGemini bool
	// OpenAIPath, when non-empty, is the upstream path serving the OpenAI
	// chat surface, joined onto the base path and REPLACING the inbound
	// /v1/chat/completions (or /v1/completions) path. Vendors whose
	// subscription channel lives under a versioned base (WorkBuddy: …/v2 +
	// /chat/completions) need this: plain reverse-proxy joining would send
	// …/v2/v1/chat/completions, which the vendor reads as an API-key
	// channel call and rejects (WorkBuddy code 11128). Empty keeps the
	// default join for vendors whose base carries no version segment.
	OpenAIPath string
	// SanitizeOpenAIBody, when set, rewrites a verbatim-forwarded OpenAI
	// request body before egress (e.g. stripping Anthropic billing metadata
	// the vendor's filter rejects). Nil passes the body through untouched.
	SanitizeOpenAIBody func([]byte) []byte
	// Gemini carries the mode-specific egress facts (api-key vs OAuth/code
	// assist) used by the serveGemini branch.
	Gemini *GeminiTarget
	// QuotaErrorPatterns: provider-declared regexes that mark a failure as
	// quota-class for pool failover (backend.ts isExhausted's
	// definition.quotaErrorPatterns branch). Empty → classification falls back
	// to the built-in 401/402/balance/credential-quota rules only.
	QuotaErrorPatterns []string
}

// GeminiTarget holds the Gemini-specific egress facts consumed by serveGemini.
// It is the Gemini analogue of UpstreamTarget: provider-owned, never inferred
// from a generic flag.
type GeminiTarget struct {
	// APIKeyMode: true → call generativelanguage.googleapis.com with the
	// x-goog-api-key header (static key from Auth or AuthProvider). false →
	// call the Code Assist upstream with Authorization: Bearer + the
	// {model, project, session_id} envelope (OAuth).
	APIKeyMode bool
	// APIKeyBase overrides the API-key upstream (generativelanguage.googleapis.com).
	// Mirrors the TS opts.upstream override; defaults to the Google endpoint.
	APIKeyBase *url.URL
	// CodeAssistBase is the OAuth upstream (cloudcode-pa.googleapis.com or
	// daily-cloudcode-pa.googleapis.com). Ignored in APIKeyMode.
	CodeAssistBase *url.URL
	// AltRoute, when set on an OAuth target, is the secondary Code Assist
	// identity retried once on 401/403/404 (ports callGeminiWithAutoFallback's
	// gemini-cli ↔ antigravity failover). The engine's multi-account pool also
	// covers this, but AltRoute lets one account carry both credentials.
	AltRoute *GeminiAltRoute
}

// GeminiAltRoute is the secondary OAuth identity used by auto auth fallback.
type GeminiAltRoute struct {
	CodeAssistBase *url.URL
	AuthProvider   func(ctx context.Context) (string, error)
}

// UpstreamTarget is one protocol-specific forwarding target.
type UpstreamTarget struct {
	BaseURL         *url.URL
	Auth            string // empty → inherit Upstream.Auth
	AuthHeader      string // empty → Authorization
	AuthValuePrefix string // empty → "Bearer "
	// AuthProvider overrides the parent Upstream.AuthProvider for this target.
	AuthProvider func(ctx context.Context) (string, error)
	// ExtraHeaders are injected into requests to this target, after the
	// parent Upstream.ExtraHeaders.
	ExtraHeaders []ExtraHeader
}

// ExtraHeader is a static header forwarded to the upstream on every request
// (vendor probes, client-version pins). It must never carry the credential.
type ExtraHeader struct {
	Name  string
	Value string
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

	heartbeatMu sync.Mutex
	heartbeats  map[string]context.CancelFunc
}

// ProxyConfig reads the persisted proxy settings for one account. The table
// belongs to internal/store; outer layers ask the service so a proxy setting is
// never reached by holding a handle on the table-owning store.
func (s *Service) ProxyConfig(ctx context.Context, providerID, name string) (store.ProxyConfig, error) {
	return s.Store.GetProxyConfig(ctx, providerID, name)
}

// RuntimeState reads the persisted runtime state of a running proxy, nil when
// nothing is recorded. Same boundary as ProxyConfig: a caller that wants to
// know what a proxy is doing asks the service, not the table.
func (s *Service) RuntimeState(ctx context.Context, providerID, name string) (*store.ProxyRuntimeState, error) {
	return s.Store.ReadProxyState(ctx, providerID, name)
}

func New(st *store.AccountStore, leases *LeaseStore) *Service {
	return &Service{
		Store: st, Jobs: leases, servers: newRegistry(), poolServers: newPoolServerRegistry(),
		heartbeats: map[string]context.CancelFunc{},
	}
}

// Close shuts down every in-process server this Service owns. Detached
// children are intentionally left running: their leases keep them recoverable.
// Idempotent — safe to call from app.Close on every exit path.
func (s *Service) Close() {
	s.heartbeatMu.Lock()
	for leaseID, cancel := range s.heartbeats {
		cancel()
		delete(s.heartbeats, leaseID)
	}
	s.heartbeatMu.Unlock()
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
		cfg, err := s.Store.GetProxyConfig(c, providerID, accountName)
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
	account, err := s.Store.Get(ctx, providerID, accountName)
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
		state, stateErr := s.Store.ReadProxyState(ctx, providerID, accountName)
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
	state, err := s.Store.ReadProxyState(ctx, providerID, accountName)
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
	// The bearer token is stable per account: it is persisted in proxy_state
	// and reused across restarts so client configs written once at
	// activation keep working. Only a first-ever start (or a state row
	// without one) mints a fresh token. Rotating here would 401 every
	// configured app until the next activation rewrote it.
	token := ""
	mintedFresh := false
	if state != nil {
		token = state.Token
	}
	if token == "" {
		mintedFresh = true
		token, err = NewToken()
		if err != nil {
			return nil, anyerr.Wrapf(err, "generate proxy token", "PROXY_START_FAILED")
		}
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
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg, mintedFresh)
		return nil, errors.Join(err, cleanupErr)
	}

	var pid int
	if s.Spawner != nil {
		logPath := paths.ProxyLogPath(s.Store.Root, providerID, accountName)
		pid, err = s.Spawner(providerID, accountName, st, logPath)
		if err != nil {
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg, mintedFresh)
			return nil, errors.Join(
				anyerr.Wrapf(err, "spawn proxy child", "PROXY_START_FAILED"),
				cleanupErr,
			)
		}
	} else {
		// In-process mode (tests/library): same lifecycle as before.
		server := s.buildServer(providerID, accountName, instanceID, token, upstream)
		if err := server.Listen(host, port); err != nil {
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg, mintedFresh)
			return nil, errors.Join(err, cleanupErr)
		}
		if err := server.WaitReady(healthTimeout); err != nil {
			_ = server.Close()
			cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, 0, "", originalCfg, mintedFresh)
			return nil, errors.Join(err, cleanupErr)
		}
		s.servers.put(providerID, accountName, server)
		pid = ownPID()
	}

	// Ownership proof before declaring success: the server must echo OUR new
	// instance id, whatever process hosts it.
	if err := waitHealthInstance(endpoint, instanceID, healthTimeout); err != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, "", originalCfg, mintedFresh)
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
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, "", originalCfg, mintedFresh)
		return nil, errors.Join(err, cleanupErr)
	}

	if lErr := s.Jobs.CreateContext(ctx, Lease{
		LeaseID: leaseID, Provider: providerID, Account: accountName,
		Port: portOf(endpoint), Host: host, Endpoint: endpoint,
		OwnerPID: pid, InstanceID: instanceID,
	}); lErr != nil {
		cleanupErr := s.abortStart(ctx, providerID, accountName, instanceID, endpoint, pid, leaseID, originalCfg, mintedFresh)
		return nil, errors.Join(lErr, cleanupErr)
	}
	s.startLeaseHeartbeat(leaseID, instanceID, pid)

	return &StartResult{
		Endpoint: endpoint, Token: token,
		LeaseID: leaseID, InstanceID: instanceID,
		Port: portOf(endpoint), StartedNow: true,
	}, nil
}

// abortStart compensates every mutation made by a failed start. The cleanup
// context retains the caller's held-lock identity while ignoring cancellation,
// so rollback can finish without racing another provider mutation.
// mintedFresh tells whether this attempt minted the bearer token: only then
// is the state row dropped outright. A reused stable token survives the abort
// because live client configs may still hold it.
func (s *Service) abortStart(
	ctx context.Context,
	providerID, accountName, instanceID, endpoint string,
	pid int,
	leaseID string,
	originalCfg store.ProxyConfig,
	mintedFresh bool,
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
			s.stopLeaseHeartbeat(leaseID)
			cleanupErrs = append(cleanupErrs, s.Jobs.DeleteContext(cleanupCtx, leaseID))
		}
		if mintedFresh {
			// Never served with this token and nothing referenced it:
			// drop it outright instead of persisting a stillborn secret.
			cleanupErrs = append(
				cleanupErrs,
				s.Store.ClearProxyStateContext(cleanupCtx, providerID, accountName),
			)
		} else {
			// Release (not full clear): the token predates this attempt
			// and may still back live client configs.
			cleanupErrs = append(
				cleanupErrs,
				s.Store.ReleaseProxyRuntimeContext(cleanupCtx, providerID, accountName),
			)
		}
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
		UpstreamAuthProvider:    upstream.AuthProvider,
		UpstreamExtraHeaders:    upstream.ExtraHeaders,
		TranslateAnthropic:      upstream.TranslateAnthropic,
		Anthropic:               upstream.Anthropic,
		OpenAIPath:              upstream.OpenAIPath,
		SanitizeOpenAIBody:      upstream.SanitizeOpenAIBody,
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
		// The bearer token survives: client configs written at activation
		// hold it, so stop must not rotate it.
		clearErr := s.Store.ReleaseProxyRuntimeContext(cleanupCtx, providerID, accountName)
		return errors.Join(closeErr, clearErr,
			s.disableProxyConfigIfSet(cleanupCtx, providerID, accountName))
	}

	state, err := s.Store.ReadProxyState(cleanupCtx, providerID, accountName)
	if err != nil {
		return err
	}
	if state == nil {
		return s.disableProxyConfigIfSet(cleanupCtx, providerID, accountName)
	}
	health := healthOwnership(state.Endpoint)
	if health.InstanceID != state.InstanceID {
		// Not ours / already dead: drop stale records atomically, keep token.
		return errors.Join(
			s.Store.ReleaseProxyRuntimeContext(cleanupCtx, providerID, accountName),
			s.disableProxyConfigIfSet(cleanupCtx, providerID, accountName))
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
	// The bearer token survives like every other stop path.
	return errors.Join(
		s.Store.ReleaseProxyRuntimeContext(cleanupCtx, providerID, accountName),
		s.disableProxyConfigIfSet(cleanupCtx, providerID, accountName))
}

// disableProxyConfigIfSet records Enabled=false (preserving port/host) so a
// stopped proxy stays stopped across proxy auto-start (PX1). No-op when
// already disabled, so stopping a never-configured account writes nothing.
func (s *Service) disableProxyConfigIfSet(ctx context.Context, providerID, accountName string) error {
	cfg, err := s.Store.GetProxyConfig(ctx, providerID, accountName)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	cfg.Enabled = false
	return s.Store.SetProxyConfigContext(ctx, providerID, accountName, cfg)
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
			if err := func() error {
				s.stopLeaseHeartbeat(l.LeaseID)
				return s.Jobs.DeleteContext(ctx, l.LeaseID)
			}(); err != nil {
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
		state, err := s.Store.ReadProxyState(c, providerID, accountName)
		if err != nil {
			return err
		}
		if state != nil && state.InstanceID == instanceID {
			return s.stopLocked(c, providerID, accountName)
		}
		if leaseID != "" {
			s.stopLeaseHeartbeat(leaseID)
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
	cfg, err := s.Store.GetProxyConfig(ctx, providerID, accountName)
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
	state, err := s.Store.ReadProxyState(ctx, providerID, accountName)
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
	client := egress.NewClient(750 * time.Millisecond)
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
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return 0
	}
	// Schemeless "127.0.0.1:41401" (dynamic assignment, PX6) never parses as
	// a URL: split host:port instead. Bracketed IPv6 ("[::1]:5150") splits
	// on the last colon the same way.
	if u, err := url.Parse(endpoint); err == nil {
		if p := u.Port(); p != "" {
			if port, perr := strconv.Atoi(p); perr == nil {
				return port
			}
			return 0
		}
	}
	hostPort := endpoint
	if i := strings.Index(hostPort, "://"); i >= 0 {
		hostPort = hostPort[i+3:]
	}
	i := strings.LastIndex(hostPort, ":")
	if i < 0 {
		return 0
	}
	port, err := strconv.Atoi(hostPort[i+1:])
	if err != nil || strings.Contains(hostPort[i+1:], "/") {
		return 0
	}
	return port
}

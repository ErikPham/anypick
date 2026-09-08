package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/modelcache"
	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
	"github.com/outof0/anypick/go/internal/store"
)

// RefreshInterval mirrors the supervisor's 2.5s snapshot cadence.
const RefreshInterval = 2500 * time.Millisecond

// UsageRefreshInterval is intentionally much slower than the legacy full
// snapshot loop. App-service mode emits only a bounded usage event on this
// cadence; state snapshots are initial, explicit, or mutation-driven.
const UsageRefreshInterval = 30 * time.Second

// Deps wires the desktop core to engine services.
type Deps struct {
	Root      string
	Store     *store.AccountStore
	Accounts  *accounts.Service
	Proxy     *proxy.Service
	Providers *provider.Registry
	Clients   *client.Registry
	Profiles  *profile.Service
	Bindings  *bindings.Service
	// Inspections owns staged backup inspection for the backup-import flow. It
	// holds the account store, so the desktop protocol asks the manager rather
	// than reaching the raw store through this Deps struct.
	Inspections *InspectionManager
	// Journal is the operation history backing the Activity snapshot section.
	// Read-only here: the snapshot maps recent entries to display rows and
	// never writes. Nil (legacy tests) yields an honest empty list.
	Journal *journal.OperationJournal
	// Activation drives model-role re-activation and other planned mutations.
	Activation *activation.Deps
	Hub        *hub.Service
	// Config owns global-config mutations; nil in legacy compositions only.
	Config *config.ConfigService
	// Discovery owns model-cache reads and writes for snapshot/catalog paths.
	// Keeping it injected prevents the desktop protocol from reaching through
	// the account store's database handle.
	Discovery *discovery.Service
	// ModelCache is the model-cache store backing gateway-test and snapshot
	// catalog paths. Injected (built in the composition root) so the desktop
	// protocol never reaches through the account store's database handle.
	ModelCache *modelcache.Store
	// HostState is the native host's report channel for platform truth such
	// as login-item registration (ADR 0020 §7); optional until handshake.
	HostState HostStateProvider
	// ShutdownOwned stops every credential-bearing service owned by the app
	// (Hub, account proxies, then pools). Production supplies this from the
	// composition root because Hub child identity is a process-shell concern.
	// Legacy/tests without it retain the account-proxy cleanup below.
	ShutdownOwned func(context.Context) error
	// PluginDiagnostics supplies redacted dynamic host health. It is a callback
	// so the desktop package does not own or restart the private Node process.
	PluginDiagnostics func(context.Context) plugins.Diagnostics
}

// HostStateProvider exposes platform state only the native host can know.
type HostStateProvider interface {
	LaunchAtLoginEnabled() bool
}

// Server is the desktop-app core: it pushes snapshots on an interval and
// executes commands from the app serially, exactly like src/tray/supervisor.ts.
type Server struct {
	Deps Deps
	In   io.Reader
	Out  io.Writer

	revision    int
	actionRows  map[string]ActionRow
	prevActions map[string]func(context.Context) error
	actions     map[string]func(context.Context) error
	actionOrder []string
	actionsMu   sync.Mutex
	inspections *InspectionManager

	// In-flight cancellable mutations (account-login) keyed by requestId.
	cancelMu     sync.Mutex
	cancellables map[string]context.CancelFunc

	writeMu   sync.Mutex
	refreshMu sync.Mutex
	inFlight  sync.WaitGroup

	serviceMode  bool
	snapshotSent bool
	hello        EngineHello

	hostStateMu          sync.Mutex
	hostStateReported    bool
	launchAtLoginEnabled bool
	hostEffectMu         sync.Mutex
	pendingHostEffects   map[string]config.HostEffectRequest
}

// New builds a server. In/Out are the pipes to the native app; when the host
// has no app binary they may be os.Stdin/os.Stdout so any client can attach.
func New(deps Deps, in io.Reader, out io.Writer) *Server {
	return &Server{Deps: deps, In: in, Out: out, inspections: deps.Inspections,
		cancellables:       map[string]context.CancelFunc{},
		pendingHostEffects: map[string]config.HostEffectRequest{}}
}

// NewService builds the app-owned protocol-v2 service. Unlike New (the legacy
// supervisor bridge), it requires a handshake, has no startup sleep, and never
// polls full snapshots.
func NewService(deps Deps, in io.Reader, out io.Writer) *Server {
	s := New(deps, in, out)
	s.serviceMode = true
	s.hello = newEngineHello()
	return s
}

// errShutdown unwinds Run cleanly.
var errShutdown = fmt.Errorf("desktop core shutting down")

func (s *Server) Run(ctx context.Context) error {
	lines := s.scanLines()
	if s.serviceMode {
		if err := s.handshake(ctx, lines); err != nil {
			return err
		}
	} else {
		// Compatibility bridge only. App-owned desktop-service has a real
		// handshake and therefore needs no arbitrary startup delay.
		time.Sleep(1 * time.Second)
	}
	if err := s.refresh(ctx); err != nil && err != errShutdown {
		s.logErr(err)
	}

	interval := RefreshInterval
	if s.serviceMode {
		interval = UsageRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.shutdown(ctx)
		case <-ticker.C:
			var err error
			if s.serviceMode {
				err = s.refreshUsage(ctx)
			} else {
				err = s.refresh(ctx)
			}
			if err != nil {
				if err == errShutdown {
					return s.shutdown(ctx)
				}
				s.logErr(err)
			}
		case scanned, ok := <-lines:
			if !ok {
				return s.shutdown(ctx)
			}
			if scanned.Err != nil {
				if s.serviceMode {
					s.write(FrameProtocolError(ProtocolError{ProtocolVersion: ServiceProtocolVersion,
						Code: "FRAME_TOO_LARGE", Message: "input frame exceeded the configured bound"}))
				}
				return s.shutdown(ctx)
			}
			line := strings.TrimRight(scanned.Line, "\r\n")
			if s.serviceMode && strings.HasPrefix(line, "host-effect-result\t") {
				result, derr := decodeHostEffectResult(line)
				if derr != nil {
					s.write(FrameProtocolError(ProtocolError{ProtocolVersion: ServiceProtocolVersion,
						Code: "MALFORMED_HOST_EFFECT_RESULT", Message: "host-effect result was rejected"}))
					continue
				}
				s.handleHostEffectResult(ctx, result)
				continue
			}
			cmd, derr := DecodeLine(line)
			if derr != nil {
				if s.serviceMode {
					s.write(FrameProtocolError(ProtocolError{ProtocolVersion: ServiceProtocolVersion,
						Code: "MALFORMED_FRAME", Message: "command frame was rejected"}))
				}
				continue // malformed input is ignored, never fatal
			}
			if err := s.handle(ctx, cmd); err != nil {
				if err == errShutdown {
					return s.shutdown(ctx)
				}
				s.logErr(err)
			}
		}
	}
}

type scannedLine struct {
	Line string
	Err  error
}

func (s *Server) scanLines() <-chan scannedLine {
	lines := make(chan scannedLine, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(s.In)
		limit := MaxCommandBytes * 2
		if s.serviceMode {
			limit = MaxServiceFrameBytes
		}
		scanner.Buffer(make([]byte, 0, 64*1024), limit)
		for scanner.Scan() {
			lines <- scannedLine{Line: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			lines <- scannedLine{Err: err}
		}
	}()
	return lines
}

func (s *Server) handshake(ctx context.Context, lines <-chan scannedLine) error {
	s.write(FrameEngineHello(s.hello))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case scanned, ok := <-lines:
		if !ok || scanned.Err != nil {
			err := fmt.Errorf("desktop host closed before handshake")
			s.rejectHandshake("HANDSHAKE_INCOMPLETE", err)
			return err
		}
		host, err := decodeHostHello(strings.TrimRight(scanned.Line, "\r\n"))
		if err != nil {
			s.rejectHandshake("HANDSHAKE_REJECTED", err)
			return err
		}
		s.setReportedHostState(host.HostState)
		s.write(FrameHandshakeReady(HandshakeReady{
			ProtocolVersion: ServiceProtocolVersion,
			Capabilities:    negotiatedCapabilities(host.Capabilities),
		}))
		return nil
	}
}

func (s *Server) rejectHandshake(code string, err error) {
	s.write(FrameHandshakeRejected(HandshakeRejected{
		ProtocolVersion: ServiceProtocolVersion,
		Code:            code,
		Message:         err.Error(),
	}))
}

func (s *Server) setReportedHostState(state HostStateReport) {
	s.hostStateMu.Lock()
	s.hostStateReported = true
	s.launchAtLoginEnabled = state.LaunchAtLogin
	s.hostStateMu.Unlock()
}

func (s *Server) reportedLaunchAtLogin() (bool, bool) {
	s.hostStateMu.Lock()
	defer s.hostStateMu.Unlock()
	return s.launchAtLoginEnabled, s.hostStateReported
}

func (s *Server) requestHostEffect(m Mutate, effect config.HostEffectRequest) error {
	s.hostEffectMu.Lock()
	if _, exists := s.pendingHostEffects[m.RequestID]; exists {
		s.hostEffectMu.Unlock()
		return fmt.Errorf("requestId is already awaiting a host effect")
	}
	s.pendingHostEffects[m.RequestID] = effect
	s.inFlight.Add(1)
	s.hostEffectMu.Unlock()

	s.write(FrameHostEffect(HostEffectFrame{
		ProtocolVersion: ServiceProtocolVersion,
		RequestID:       m.RequestID,
		Effect:          effect,
	}))
	go func(requestID string) {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		<-timer.C
		s.completeHostEffect(context.Background(), HostEffectResult{
			ProtocolVersion: ServiceProtocolVersion,
			RequestID:       requestID,
			Status:          "error",
			Message:         "The native host did not complete the requested system change.",
		})
	}(m.RequestID)
	return nil
}

func (s *Server) handleHostEffectResult(ctx context.Context, result HostEffectResult) {
	s.completeHostEffect(ctx, result)
}

func (s *Server) completeHostEffect(ctx context.Context, result HostEffectResult) {
	s.hostEffectMu.Lock()
	effect, ok := s.pendingHostEffects[result.RequestID]
	if ok {
		delete(s.pendingHostEffects, result.RequestID)
	}
	s.hostEffectMu.Unlock()
	if !ok {
		return // late or duplicate terminal host result
	}
	defer s.inFlight.Done()

	terminal := Result{
		Version:   ProtocolVersion,
		RequestID: result.RequestID,
		Status:    result.Status,
		Message:   result.Message,
	}
	if result.Status == "success" {
		s.setReportedHostState(result.HostState)
		if effect.Kind == string(config.HostEffectLaunchAtLogin) {
			if result.HostState.LaunchAtLogin {
				terminal.Message = "AnyPick will open at login."
			} else {
				terminal.Message = "Launch at login disabled."
			}
		}
	} else if terminal.Message == "" {
		terminal.Message = "The native host could not complete the system change."
	}
	s.write(FrameResult(terminal))
	if terminal.Status == "success" {
		if err := s.refresh(ctx); err != nil && err != errShutdown {
			s.logErr(err)
		}
	}
}

func (s *Server) cancelPendingHostEffects() {
	s.hostEffectMu.Lock()
	count := len(s.pendingHostEffects)
	s.pendingHostEffects = map[string]config.HostEffectRequest{}
	s.hostEffectMu.Unlock()
	for range count {
		s.inFlight.Done()
	}
}

// Wait blocks until every in-flight mutation handler — including its trailing
// refresh — has finished. Tests that drive handle directly (bypassing Run's
// graceful shutdown) must call it before closing the database; otherwise a
// late refresh touches a closed store.
func (s *Server) Wait() { s.inFlight.Wait() }

// shutdown mirrors the TS supervisor: proxies it started are stopped, then
// the stream closes.
func (s *Server) shutdown(_ context.Context) error {
	ctx := context.Background()
	s.cancelMu.Lock()
	for _, cancel := range s.cancellables {
		cancel()
	}
	s.cancelMu.Unlock()
	s.cancelPendingHostEffects()
	// A graceful service exit cannot return while an operation can still emit
	// a late terminal result. The native host owns the outer kill timeout.
	s.inFlight.Wait()
	if s.Deps.ShutdownOwned != nil {
		if err := s.Deps.ShutdownOwned(ctx); err != nil {
			// Quit remains best-effort like the TypeScript supervisor. A verified
			// stop failure stays on stderr and in its ownership record; it must not
			// wedge the native app's outer shutdown forever.
			s.logErr(err)
		}
		return nil
	}
	for _, providerID := range s.Deps.Providers.IDs() {
		list, err := s.Deps.Accounts.List(ctx, providerID)
		if err != nil {
			continue
		}
		for _, acc := range list {
			cfg, _ := s.Deps.Proxy.ProxyConfig(ctx, providerID, acc.Meta.Name)
			if !cfg.Enabled {
				continue
			}
			_ = s.Deps.Proxy.Stop(ctx, providerID, acc.Meta.Name)
		}
	}
	return nil
}

func (s *Server) refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	snap, err := s.buildSnapshot(ctx)
	if err != nil {
		return err
	}
	if s.serviceMode && s.snapshotSent {
		s.write(FrameSnapshotEvent(SnapshotEvent{
			ProtocolVersion: ServiceProtocolVersion,
			Revision:        snap.Revision,
			Reason:          "state-changed",
			Snapshot:        snap,
		}))
	} else {
		s.write(FrameSnapshot(snap))
	}
	s.snapshotSent = true
	return nil
}

func (s *Server) refreshUsage(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.write(FrameUsageEvent(UsageEvent{
		ProtocolVersion: ServiceProtocolVersion,
		Revision:        s.currentRevision(),
		Usage:           s.buildUsage(ctx),
	}))
	return nil
}

func (s *Server) write(line string) {
	if s.serviceMode && len(line) > MaxServiceFrameBytes {
		line = FrameProtocolError(ProtocolError{
			ProtocolVersion: ServiceProtocolVersion,
			Code:            "FRAME_TOO_LARGE",
			Message:         "output frame exceeded the configured bound",
		})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = io.WriteString(s.Out, line+"\n")
}

func (s *Server) logErr(err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	fmt.Fprintf(os.Stderr, "[desktop] %v\n", err)
}

func (s *Server) currentRevision() int {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	return s.revision
}

func (s *Server) actionHandler(id string) (func(context.Context) error, bool) {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	if h, ok := s.actions[id]; ok {
		return h, true
	}
	h, ok := s.prevActions[id]
	return h, ok
}

// routeActivator returns a handler that binds clientID to source through the
// persistent planner/executor — the desktop counterpart of invokeTrayAction's
// app.bindingService.use(target.clientId, { with: target.source }). Route
// actions must never mutate a provider's active account directly and never
// succeed silently.
func (s *Server) routeActivator(clientID string, source refs.Ref) func(context.Context) error {
	return func(c context.Context) error { return s.activateRoute(c, clientID, source) }
}

func (s *Server) activateRoute(ctx context.Context, clientID string, source refs.Ref) error {
	if s.Deps.Activation == nil {
		return anyerr.Newf("activation service is unavailable in this composition",
			"ACTIVATION_UNAVAILABLE")
	}
	plan, err := activation.PlanActivation(ctx, activation.Request{
		Mode:   activation.ModePersistent,
		Client: clientID,
		Source: &source,
	}, s.Deps.Activation, activation.Options{})
	if err != nil {
		return err
	}
	_, err = activation.Execute(ctx, plan, s.Deps.Activation, activation.ExecuteOptions{})
	return err
}

// reapplyClientsBoundTo rewrites every persistent client whose global binding
// is this account. Starting a proxy from the Proxies pane does not go through
// `use`, so without this the live token and ~/.claude/settings.json diverge
// and Claude Code 401s until the next activation.
func (s *Server) reapplyClientsBoundTo(ctx context.Context, source refs.Ref) error {
	if s.Deps.Activation == nil || s.Deps.Bindings == nil || s.Deps.Clients == nil {
		return nil
	}
	want := refs.SerializeRef(source)
	var errs []error
	for _, clientID := range s.Deps.Clients.IDs() {
		b, err := s.Deps.Bindings.GetGlobalContext(ctx, clientID)
		if err != nil || b == nil {
			continue
		}
		if refs.SerializeRef(b.Spec.Source) != want {
			continue
		}
		if err := s.activateRoute(ctx, clientID, source); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) handle(ctx context.Context, cmd *Command) error {
	switch cmd.Kind {
	case "open", "navigate", "restart":
		// Host concerns; nothing for the engine to do.
		return nil

	case "refresh":
		return s.refresh(ctx)

	case "stop", "quit":
		return errShutdown

	case "cancel":
		var p cancelPayload
		_ = json.Unmarshal(cmd.Payload, &p)
		s.cancelMu.Lock()
		cancelFn, ok := s.cancellables[p.RequestID]
		s.cancelMu.Unlock()
		if ok {
			cancelFn()
		}
		return nil

	case "invoke":
		var p invokePayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return err
		}
		if p.Revision < s.currentRevision()-1 {
			s.write(FrameResult(Result{Version: ProtocolVersion, RequestID: p.RequestID,
				Status: "error", Message: "stale action"}))
			return nil
		}
		handler, ok := s.actionHandler(p.ActionID)
		if !ok {
			s.write(FrameResult(Result{Version: ProtocolVersion, RequestID: p.RequestID,
				Status: "error", Message: "unknown action"}))
			return nil
		}
		// Run the action off the main select loop so a long mutation does
		// not stall the 2.5 s ticker or delay other commands (P1). The
		// handler runs with a background context so parent cancellation
		// does not abandon the mutation mid-write.
		s.inFlight.Add(1)
		go func(reqID string, h func(context.Context) error) {
			defer s.inFlight.Done()
			bgCtx := context.Background()
			result := Result{Version: ProtocolVersion, RequestID: reqID, Status: "success"}
			if err := h(bgCtx); err != nil {
				result.Status = "error"
				result.Message = err.Error()
				if ae, ok := err.(*anyerr.Error); ok && ae.Mutated {
					result.Message += " (state was partially changed — run doctor)"
				}
			}
			s.write(FrameResult(result))
			if err := s.refresh(bgCtx); err != nil && err != errShutdown {
				s.logErr(err)
			}
		}(p.RequestID, handler)
		return nil

	case "model-roles":
		var p modelRolesPayload
		_ = json.Unmarshal(cmd.Payload, &p)
		err := s.applyModelRoles(ctx, p.ClientID, p.RoleActionIDs)
		res := Result{Version: ProtocolVersion, RequestID: p.RequestID}
		if err != nil {
			res.Status, res.Message = "error", err.Error()
		} else {
			res.Status = "success"
		}
		s.write(FrameResult(res))
		if res.Status == "success" {
			return s.refresh(ctx)
		}
		return nil

	case "backup-inspect":
		var p backupInspectPayload
		_ = json.Unmarshal(cmd.Payload, &p)
		preview, err := s.inspections.Inspect(s.Deps.Providers, p.RequestID, p.Path)
		if err != nil {
			reason := inspectionFailureReason(err)
			s.write(FrameBackupPreview(BackupPreviewResult{
				Version: ProtocolVersion, RequestID: p.RequestID, Status: "error",
				Error: "inspection-failed", Code: "BACKUP_INSPECT_FAILED", Reason: reason,
				Message: inspectionFailureMessage(reason),
			}))
			return nil
		}
		// The native app drops previews whose requestId does not match the
		// in-flight request — Inspect must echo it on the ready path too.
		s.write(FrameBackupPreview(*preview))
		return nil

	case "logs":
		return s.handleLogs(ctx, cmd.Payload)

	case "mutate":
		return s.handleMutate(ctx, cmd.Payload)
	}
	return nil
}

func (s *Server) handleLogs(ctx context.Context, payload json.RawMessage) error {
	var p logsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	res := ProxyLogsResult{Version: ProtocolVersion, RequestID: p.RequestID,
		ProxyID: p.ProviderID + "/" + p.Name, State: "not-running"}
	st, err := s.Deps.Proxy.Status(p.ProviderID, p.Name)
	running := err == nil && st != nil && st.Running
	if running {
		res.State = "empty"
	}
	// Provider-owned readProxyLogs (plugin proxies) and the engine's own log
	// file both flow through the Service: historical logs survive a stop and
	// provider-controlled text is secret-screened before it is framed to the
	// native app. A read failure reports "error" WITHOUT blanking the tail:
	// the client keeps its last good text (L4). Only a successful read may
	// replace it.
	text, terr := s.Deps.Proxy.ProxyLogs(ctx, p.ProviderID, p.Name, p.Lines)
	if p.ProviderID == "proxy-hub" {
		// Hub children log to runtime/hub/<name>/service.log, not an
		// account runtime dir, so the account tail below would always miss.
		text, terr = tailHubLog(s.Deps.Root, p.Name, p.Lines)
	}
	if terr != nil {
		res.State = "error"
	} else if text != "" {
		res.State = "ready"
		res.Text = text
	}
	s.write(FrameProxyLogs(res))
	return nil
}

func (s *Server) handleMutate(ctx context.Context, payload json.RawMessage) error {
	var m Mutate
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	if m.Operation == "account-login" {
		return s.handleAccountLogin(ctx, m)
	}
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success"}
	switch m.Operation {
	case "account-detect":
		result = s.handleAccountDetect(m)
	case "account-save-current":
		result = s.handleAccountSaveCurrent(ctx, m)
	case "gateway-create":
		result = s.mutateGatewayCreate(ctx, m, result)
	case "account-remove":
		result = s.mutateAccountRemove(ctx, m, result)
	case "account-save":
		result = s.handleAccountSave(ctx, payload, m)
	case "account-save-input":
		result = s.handleAccountSaveInput(ctx, m)
	case "account-edit":
		result = s.handleAccountEdit(ctx, payload, m)
	case "account-refresh":
		result = s.mutateAccountRefresh(ctx, m, result)
	case "gateway-remove":
		result = s.mutateGatewayRemove(ctx, m, result)
	case "gateway-edit":
		result = s.mutateGatewayEdit(ctx, m, result)
	case "account-clear":
		result = s.handleAccountClear(ctx, m)
	case "client-reset":
		result = s.mutateClientReset(ctx, m, result)
	case "account-import", "account-export", "account-backup-all",
		"account-backup-export", "account-backup-import":
		result = s.handleTransferOp(ctx, m)
	case "gateway-test":
		result = s.handleGatewayTest(ctx, m)
	case "gateway-refresh":
		result = s.handleGatewayRefresh(ctx, m)
	case "hub-source-toggle":
		result = s.handleHubSourceToggle(ctx, m)
	case "proxy-stop-all", "proxy-restart-all":
		result = s.mutateProxyStopAll(ctx, m, result)
	case "setting-launch-at-login":
		var terminal bool
		if result, terminal = s.mutateLaunchAtLogin(ctx, m, result); terminal {
			return nil
		}
	case "setting-auto-start-proxies":
		result = s.mutateAutoStartProxies(ctx, m, result)
	case "setting-show-quota":
		result = s.mutateShowQuota(ctx, m, result)
	case "setting-quota-guard":
		result = s.mutateQuotaGuard(ctx, m, result)
	default:
		// Unknown operations must surface as structured errors, never crash
		// or silently succeed (mirrors TS unknown-mutation handling).
		result.Status = "error"
		result.Message = fmt.Sprintf("unknown mutation operation %q", m.Operation)
	}
	s.write(FrameResult(result))
	if result.Status == "success" {
		return s.refresh(ctx)
	}
	return nil
}

func (s *Server) mutateGatewayCreate(ctx context.Context, m Mutate, result Result) Result {
	if m.Name == "" || m.ProviderID == "" {
		result.Status, result.Message = "error",
			"gateway-create needs name and providerId"
		return result
	}
	// grok gateways authenticate with the live x.ai OIDC session shared
	// with the account flow, so no API key is collected. Every other
	// gateway (including gemini) carries its key in the profile.
	needsKey := m.ProviderID != "grok"
	if (needsKey && m.APIKey == "") || m.Endpoint == "" {
		result.Status, result.Message = "error",
			"gateway-create needs endpoint"+map[bool]string{true: " and apiKey", false: ""}[needsKey]
		return result
	}
	if s.Deps.Profiles == nil {
		result.Status, result.Message = "error", "profiles unavailable"
		return result
	}
	// grok's upstream base is fixed and engine-supplied; fall back to it
	// when the UI sends no endpoint (providerSuppliesEndpoint).
	endpoint := m.Endpoint
	if m.ProviderID == "grok" && endpoint == "" {
		endpoint = "https://cli-chat-proxy.grok.com"
	}
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.GatewayRef(m.Name)), func(c context.Context) error {
		_, err := s.Deps.Profiles.UpsertContext(c, profile.Runtime{
			Meta: profile.Meta{
				Name:         m.Name,
				Provider:     orString(m.ProviderID, "custom"),
				Label:        strings.TrimSpace(m.Label),
				Endpoint:     endpoint,
				DefaultModel: m.DefaultModel,
			},
			Secrets: profile.Secrets{APIKey: m.APIKey},
		})
		return err
	})
	if err != nil {
		result.Status, result.Message = "error", err.Error()
	}
	return result
}

func (s *Server) mutateAccountRemove(ctx context.Context, m Mutate, result Result) Result {
	if m.ProviderID == "" || m.Name == "" {
		result.Status, result.Message = "error", "account-remove needs providerId and name"
		return result
	}
	if err := s.Deps.Accounts.Delete(ctx, m.ProviderID, m.Name); err != nil {
		result.Status, result.Message = "error", err.Error()
	}
	return result
}

func (s *Server) mutateAccountRefresh(ctx context.Context, m Mutate, result Result) Result {
	if m.ProviderID == "" || m.Name == "" {
		result.Status, result.Message = "error", "account-refresh needs providerId and name"
		return result
	}
	if _, err := s.Deps.Accounts.RefreshSnapshot(ctx, m.ProviderID, m.Name); err != nil {
		result.Status, result.Message = "error", err.Error()
		if ae, ok := err.(*anyerr.Error); ok && ae.Code == "ACCOUNT_NOT_FOUND" {
			result.Message = "account not found"
		}
	}
	return result
}

func (s *Server) mutateGatewayRemove(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Profiles == nil || m.Name == "" {
		result.Status, result.Message = "error", "gateway-remove needs name"
		return result
	}
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.GatewayRef(m.Name)), func(c context.Context) error {
		return s.Deps.Profiles.DeleteContext(c, m.Name)
	})
	if err != nil {
		result.Status, result.Message = "error", err.Error()
	}
	return result
}

func (s *Server) mutateGatewayEdit(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Profiles == nil || m.Name == "" {
		result.Status, result.Message = "error", "gateway-edit needs name"
		return result
	}
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.GatewayRef(m.Name)), func(c context.Context) error {
		existing, err := s.Deps.Profiles.GetContext(c, m.Name)
		if err != nil {
			return err
		}
		if existing == nil {
			return anyerr.Newf("gateway not found", "GATEWAY_NOT_FOUND")
		}
		if m.Label != "" {
			existing.Meta.Label = m.Label
		}
		if m.Endpoint != "" {
			existing.Meta.Endpoint = m.Endpoint
		}
		if m.APIKey != "" {
			existing.Secrets.APIKey = m.APIKey
		}
		if m.DefaultModel != "" {
			existing.Meta.DefaultModel = m.DefaultModel
		}
		_, err = s.Deps.Profiles.UpsertContext(c, *existing)
		return err
	})
	if err != nil {
		result.Status, result.Message = "error", err.Error()
	}
	return result
}

func (s *Server) mutateClientReset(ctx context.Context, m Mutate, result Result) Result {
	cad, cerr := s.Deps.Clients.Get(m.Name)
	if cerr != nil {
		result.Status, result.Message = "error", cerr.Error()
		return result
	}
	rerr := lock.WithMutationLock(ctx, s.Deps.Root, "client/"+cad.ID(), func(context.Context) error {
		return cad.Reset()
	})
	if rerr != nil {
		result.Status, result.Message = "error", rerr.Error()
	}
	return result
}

func (s *Server) mutateProxyStopAll(ctx context.Context, m Mutate, result Result) Result {
	restart := m.Operation == "proxy-restart-all"
	verb, infinitive := "stopped", "stop"
	if restart {
		verb, infinitive = "restarted", "restart"
	}
	// Aggregate per-proxy failures (PX4): the old loop swallowed every
	// error and always reported success. Per-row progress would need
	// protocol work; naming the failures in the result is what fits.
	var failed []string
	attempted := 0
	// Discovery is read-only and must still identify the concrete proxies
	// when the command context was cancelled. The stop/restart operation
	// itself keeps ctx, so cancellation is reported per proxy instead of
	// being mistaken for "nothing to do".
	enumerateCtx := context.WithoutCancel(ctx)
	for _, providerID := range s.Deps.Providers.IDs() {
		list, lerr := s.Deps.Accounts.List(enumerateCtx, providerID)
		if lerr != nil {
			continue
		}
		for _, acc := range list {
			cfg, _ := s.Deps.Proxy.ProxyConfig(enumerateCtx, providerID, acc.Meta.Name)
			if !cfg.Enabled {
				continue
			}
			attempted++
			label := providerID + "/" + acc.Meta.Name
			if serr := s.Deps.Proxy.Stop(ctx, providerID, acc.Meta.Name); serr != nil {
				failed = append(failed, label+": "+serr.Error())
				continue
			}
			if restart {
				if _, rerr := s.Deps.Proxy.StartProxy(ctx, providerID, acc.Meta.Name); rerr != nil {
					failed = append(failed, label+": "+rerr.Error())
				}
			}
		}
	}
	if len(failed) > 0 {
		result.Status, result.Message = "error", fmt.Sprintf("could not %s %d of %d proxies: %s",
			infinitive, len(failed), attempted, strings.Join(failed, "; "))
	} else if attempted == 0 {
		result.Message = "no enabled proxies to " + infinitive
	} else {
		result.Message = fmt.Sprintf("%s %d of %d proxies", verb, attempted, attempted)
	}
	return result
}

func (s *Server) mutateLaunchAtLogin(ctx context.Context, m Mutate, result Result) (Result, bool) {
	// ADR 0020 §7: the engine validates and emits a typed host effect;
	// the native app executes SMAppService and reflects the new state in
	// its next host report. No OS mutation happens here — on any platform.
	effect := config.NewLaunchAtLoginEffect(m.Enabled)
	if s.serviceMode {
		if err := s.requestHostEffect(m, effect); err != nil {
			result.Status, result.Message = "error", err.Error()
			return result, false
		}
		return result, true
	}
	payload, perr := json.Marshal(effect)
	if perr != nil {
		result.Status, result.Message = "error", perr.Error()
		return result, false
	}
	result.Payload = payload
	if m.Enabled {
		result.Message = "AnyPick will open at login."
	} else {
		result.Message = "Launch at login disabled."
	}
	return result, false
}

func (s *Server) mutateAutoStartProxies(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Config == nil {
		result.Status, result.Message = "error", "config service unavailable"
		return result
	}
	_, uerr := s.Deps.Config.SetTrayPreference(ctx, "startEnabledProxies", m.Enabled)
	if uerr != nil {
		result.Status, result.Message = "error", uerr.Error()
		return result
	}
	if m.Enabled {
		s.startEnabledProxies(ctx)
	}
	if m.Enabled {
		result.Message = "Enabled proxies will start with AnyPick."
	} else {
		result.Message = "Proxy auto-start disabled."
	}
	return result
}

func (s *Server) mutateShowQuota(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Config == nil {
		result.Status, result.Message = "error", "config service unavailable"
		return result
	}
	if _, qerr := s.Deps.Config.SetTrayPreference(ctx, "showQuota", m.Enabled); qerr != nil {
		result.Status, result.Message = "error", qerr.Error()
	} else if m.Enabled {
		result.Message = "Quota is visible."
	} else {
		result.Message = "Quota is hidden."
	}
	return result
}

func (s *Server) mutateQuotaGuard(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Config == nil {
		result.Status, result.Message = "error", "config service unavailable"
		return result
	}
	if _, gerr := s.Deps.Config.SetQuotaGuardEnabled(ctx, m.Enabled); gerr != nil {
		result.Status, result.Message = "error", gerr.Error()
		return result
	}
	if rerr := s.restartRunningPools(ctx); rerr != nil {
		result.Status, result.Message = "error", rerr.Error()
		return result
	}
	if m.Enabled {
		result.Message = "Quota Guard enabled for multi-account compatibility proxy pools."
	} else {
		result.Message = "Quota Guard disabled. Native app logins were not changed."
	}
	return result
}

func errorResult(requestID string, err error) Result {
	return Result{Version: ProtocolVersion, RequestID: requestID, Status: "error", Message: err.Error()}
}

// detectLiveSource ports the TypeScript capability fallback: a source-aware
// provider owns the scoped probe; every other provider has one live store and
// uses DetectLive. Only display identity crosses the desktop boundary.
func detectLiveSource(pv provider.Provider, sourceID string) (provider.LiveAuthStatus, error) {
	if sourceID != "" {
		if scoped, ok := pv.(interface {
			DetectLiveSource(string) (provider.LiveAuthStatus, error)
		}); ok {
			return scoped.DetectLiveSource(sourceID)
		}
	}
	return pv.DetectLive()
}

func (s *Server) handleAccountDetect(m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success"}
	pv, err := s.Deps.Providers.Get(m.ProviderID)
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	live, err := detectLiveSource(pv, strings.TrimSpace(m.SourceID))
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	if !live.Present {
		return errorResult(m.RequestID,
			anyerr.Newf("No live "+pv.Name()+" login was found.", "NO_LIVE_AUTH"))
	}
	location := pv.Name()
	if sourceID := strings.TrimSpace(m.SourceID); sourceID != "" {
		location = sourceID
	}
	if live.Identity != "" {
		result.Message = fmt.Sprintf("Detected %s in %s.", live.Identity, location)
	} else {
		result.Message = fmt.Sprintf("Detected the current login in %s.", location)
	}
	return result
}

func (s *Server) handleAccountSave(ctx context.Context, raw json.RawMessage, m Mutate) Result {
	if m.ProviderID == "" || m.Name == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("account-save needs providerId and name", "INVALID_USAGE"))
	}
	var saved store.Meta
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.ProviderScope(m.ProviderID),
		func(c context.Context) error {
			var saveErr error
			if apiKey := strings.TrimSpace(m.APIKey); apiKey != "" {
				profileID := strings.TrimSpace(m.Region)
				if profileID == "" {
					profileID = "global"
				}
				saved, saveErr = s.Deps.Accounts.SaveInput(c, m.ProviderID, m.Name,
					accounts.CredentialInput{Kind: "api-key", Secret: apiKey, Profile: profileID},
					m.Overwrite)
			} else {
				saved, saveErr = s.Deps.Accounts.Save(c, m.ProviderID, m.Name, m.Overwrite)
			}
			if saveErr != nil {
				return saveErr
			}
			// account-save treats a blank label as absent (cleanOptional in the
			// TS supervisor), while a non-blank label is committed under the
			// same provider scope as the snapshot.
			if jsonFieldPresent(raw, "label") {
				label := strings.TrimSpace(m.Label)
				if label != "" && label != saved.Label {
					saved, saveErr = s.Deps.Accounts.PatchMeta(c, m.ProviderID, saved.Name,
						store.MetaPatch{LabelSet: true, Label: label})
				}
			}
			return saveErr
		})
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	label := saved.Label
	if label == "" {
		label = saved.Name
	}
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: "Saved " + label + "."}
}

// handleAccountSaveInput saves a typed credential (API key / plan key) for a
// compatible vendor — the native Add Account sheet's key path. Unlike
// account-save there is no live login to snapshot; unlike gateway-create the
// credential lands in the account store, so apps can be bound to it. An empty
// profile selects the kind's first endpoint profile.
func (s *Server) handleAccountSaveInput(ctx context.Context, m Mutate) Result {
	if m.ProviderID == "" || m.Name == "" || m.Kind == "" || strings.TrimSpace(m.APIKey) == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("account-save-input needs providerId, name, kind and apiKey", "INVALID_USAGE"))
	}
	if s.Deps.Accounts == nil {
		return errorResult(m.RequestID,
			anyerr.Newf("accounts service unavailable", "SERVICE_UNAVAILABLE"))
	}
	profileID := strings.TrimSpace(m.Profile)
	if profileID == "" {
		first, err := s.defaultCredentialProfile(m.ProviderID, m.Kind)
		if err != nil {
			return errorResult(m.RequestID, err)
		}
		profileID = first
	}
	meta, err := s.Deps.Accounts.SaveInput(ctx, m.ProviderID, m.Name,
		accounts.CredentialInput{Kind: m.Kind, Secret: m.APIKey, Profile: profileID}, false)
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	label := meta.Label
	if label == "" {
		label = meta.Name
	}
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: "Saved " + meta.Provider + "/" + label + "."}
}

// defaultCredentialProfile resolves an omitted endpoint profile to the kind's
// first declared profile, so single-profile vendors stay one field simpler
// for every client. Unknown providers and kinds fail here with the same
// coded error BackupInput would raise.
func (s *Server) defaultCredentialProfile(providerID, kind string) (string, error) {
	pv, err := s.Deps.Providers.Get(providerID)
	if err != nil {
		return "", err
	}
	definer, ok := pv.(interface {
		Definition() *provider.CompatibleDefinition
	})
	if !ok || definer.Definition() == nil {
		return "", anyerr.Newf(
			"Provider "+providerID+" does not accept credential inputs. Sign in natively and use save.",
			"UNSUPPORTED_CREDENTIAL_INPUT")
	}
	for _, c := range definer.Definition().Credentials {
		if c.Kind == kind {
			if len(c.Profiles) == 0 {
				break
			}
			return c.Profiles[0].ID, nil
		}
	}
	return "", anyerr.Newf(
		"Provider "+providerID+" does not accept credential kind \""+kind+"\".",
		"UNSUPPORTED_CREDENTIAL_INPUT")
}

func (s *Server) handleAccountSaveCurrent(ctx context.Context, m Mutate) Result {
	if m.ProviderID == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("account-save-current needs providerId", "INVALID_USAGE"))
	}
	pv, err := s.Deps.Providers.Get(m.ProviderID)
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	var saved store.Meta
	err = lock.WithMutationLock(ctx, s.Deps.Root, refs.ProviderScope(m.ProviderID),
		func(c context.Context) error {
			live, detectErr := detectLiveSource(pv, strings.TrimSpace(m.SourceID))
			if detectErr != nil {
				return detectErr
			}
			if !live.Present {
				return anyerr.Newf("No live "+pv.Name()+" login to save.", "NO_LIVE_AUTH")
			}
			name, nameErr := s.currentSaveName(c, m.ProviderID, strings.TrimSpace(m.Name), live)
			if nameErr != nil {
				return nameErr
			}
			var saveErr error
			saved, saveErr = s.Deps.Accounts.Save(c, m.ProviderID, name, true)
			return saveErr
		})
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	label := saved.Label
	if label == "" {
		label = saved.Name
	}
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: "Saved " + label + "."}
}

// currentSaveName mirrors AccountService.resolveCurrentSaveName: an existing
// identity wins, then an explicit name, a matching active account, and finally
// an identity-derived stable slug. The active pointer alone is never identity.
func (s *Server) currentSaveName(ctx context.Context, providerID, requested string,
	live provider.LiveAuthStatus) (string, error) {
	list, err := s.Deps.Accounts.List(ctx, providerID)
	if err != nil {
		return "", err
	}
	if live.Identity != "" {
		for _, acc := range list {
			if sameIdentity(acc.Meta.Identity, live.Identity) {
				return acc.Meta.Name, nil
			}
		}
	}
	if requested != "" {
		return slug.AccountName(requested)
	}
	active, err := s.Deps.Accounts.Active(ctx, providerID)
	if err != nil {
		return "", err
	}
	if active != "" {
		for _, acc := range list {
			if acc.Meta.Name == active &&
				(live.Identity == "" || sameIdentity(acc.Meta.Identity, live.Identity)) {
				return active, nil
			}
		}
	}
	if live.Identity != "" {
		return accountNameFromIdentity(live.Identity)
	}
	return "", anyerr.Newf("Name the login before saving it.", "MISSING_ACCOUNT_NAME")
}

func (s *Server) handleAccountEdit(ctx context.Context, raw json.RawMessage, m Mutate) Result {
	if m.ProviderID == "" || m.Name == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("account-edit needs providerId and name", "INVALID_USAGE"))
	}
	var edited store.Meta
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.ProviderScope(m.ProviderID),
		func(c context.Context) error {
			account, err := s.Deps.Accounts.Get(c, m.ProviderID, m.Name)
			if err != nil {
				return err
			}
			if account == nil {
				return anyerr.Newf(
					fmt.Sprintf("No account %q for provider %q.", m.Name, m.ProviderID),
					"ACCOUNT_NOT_FOUND")
			}
			edited = account.Meta
			if !jsonFieldPresent(raw, "label") {
				return nil
			}
			edited, err = s.Deps.Accounts.PatchMeta(c, m.ProviderID, m.Name,
				store.MetaPatch{LabelSet: true, Label: strings.TrimSpace(m.Label)})
			return err
		})
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	label := edited.Label
	if label == "" {
		label = edited.Name
	}
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: "Updated " + label + "."}
}

func (s *Server) handleAccountClear(ctx context.Context, m Mutate) Result {
	if m.ProviderID == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("account-clear needs providerId", "INVALID_USAGE"))
	}
	pv, err := s.Deps.Providers.Get(m.ProviderID)
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	sourceID := strings.TrimSpace(m.SourceID)
	var clear func() error
	if sourceID != "" {
		if scoped, ok := pv.(interface{ ClearLiveSource(string) error }); ok {
			clear = func() error { return scoped.ClearLiveSource(sourceID) }
		}
	}
	if clear == nil {
		if clearer, ok := pv.(provider.LiveClearer); ok {
			clear = clearer.ClearLive
		}
	}
	if clear == nil {
		return errorResult(m.RequestID, anyerr.Newf(
			fmt.Sprintf("Provider %q does not support clearing live auth.", m.ProviderID),
			"STASH_UNSUPPORTED"))
	}
	var live provider.LiveAuthStatus
	var backedUpTo string
	var matchedByIdentity bool
	err = lock.WithMutationLock(ctx, s.Deps.Root, refs.ProviderScope(m.ProviderID),
		func(c context.Context) error {
			var detectErr error
			live, detectErr = detectLiveSource(pv, sourceID)
			if detectErr != nil || !live.Present {
				return detectErr
			}
			name, matched, nameErr := s.stashSaveName(c, m.ProviderID, live)
			if nameErr != nil {
				return nameErr
			}
			if _, saveErr := s.Deps.Accounts.Save(c, m.ProviderID, name, true); saveErr != nil {
				return anyerr.Wrapf(saveErr,
					"Could not back up the live "+pv.Name()+" login before clearing it",
					"STASH_BACKUP_FAILED")
			}
			backedUpTo, matchedByIdentity = name, matched
			return clear()
		})
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	location := m.ProviderID
	if sourceID != "" {
		location = sourceID
	}
	message := fmt.Sprintf("No live %s login to clear.", location)
	if live.Present {
		if matchedByIdentity {
			message = fmt.Sprintf("Saved live %s login into %s/%s and cleared local auth.",
				location, m.ProviderID, backedUpTo)
		} else {
			message = fmt.Sprintf("Backed up live %s login as %s/%s and cleared local auth.",
				location, m.ProviderID, backedUpTo)
		}
	}
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: message}
}

func (s *Server) stashSaveName(ctx context.Context, providerID string,
	live provider.LiveAuthStatus) (string, bool, error) {
	list, err := s.Deps.Accounts.List(ctx, providerID)
	if err != nil {
		return "", false, err
	}
	if live.Identity != "" {
		for _, acc := range list {
			if sameIdentity(acc.Meta.Identity, live.Identity) {
				return acc.Meta.Name, true, nil
			}
		}
	}
	active, err := s.Deps.Accounts.Active(ctx, providerID)
	if err != nil {
		return "", false, err
	}
	if active != "" {
		for _, acc := range list {
			if acc.Meta.Name == active &&
				(live.Identity == "" || acc.Meta.Identity == "" ||
					sameIdentity(acc.Meta.Identity, live.Identity)) {
				return active, false, nil
			}
		}
	}
	if live.Identity != "" {
		name, nameErr := accountNameFromIdentity(live.Identity)
		return name, false, nameErr
	}
	return "stash", false, nil
}

func accountNameFromIdentity(identity string) (string, error) {
	local := strings.TrimSpace(identity)
	if at := strings.Index(local, "@"); at >= 0 {
		local = local[:at]
	}
	name, err := slug.AccountName(local)
	if err != nil {
		return "stash", nil
	}
	return name, nil
}

func sameIdentity(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (s *Server) handleGatewayRefresh(ctx context.Context, m Mutate) Result {
	if s.Deps.Profiles == nil || s.Deps.Accounts == nil || m.Name == "" {
		return errorResult(m.RequestID,
			anyerr.Newf("gateway-refresh needs an available profile and name", "INVALID_USAGE"))
	}
	pr, err := s.Deps.Profiles.GetContext(ctx, m.Name)
	if err != nil {
		return errorResult(m.RequestID, err)
	}
	if pr == nil {
		return errorResult(m.RequestID, anyerr.Newf("gateway not found", "GATEWAY_NOT_FOUND"))
	}
	discoveryService := &discovery.Service{
		Root: s.Deps.Root, Cache: s.Deps.ModelCache,
	}
	models := discoveryService.List(ctx, discovery.Request{
		ProviderID: pr.Meta.Provider,
		Endpoint:   pr.Meta.Endpoint,
		APIKey:     pr.Secrets.APIKey,
		Refresh:    true,
	})
	return Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success",
		Message: fmt.Sprintf("Refreshed %s: %d models.", m.Name, len(models.Models))}
}

func jsonFieldPresent(raw json.RawMessage, field string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, ok := fields[field]
	return ok
}

// handleAccountLogin runs the browser sign-in off the main loop with a
// cancellable context registered under its requestId. Cancellation must reach
// the OAuth child while the login is still awaiting it, and a cancelled run
// must publish exactly one terminal result — "cancelled", never a late success.
func (s *Server) handleAccountLogin(_ context.Context, m Mutate) error {
	fail := func(message string) {
		s.write(FrameResult(Result{Version: ProtocolVersion, RequestID: m.RequestID,
			Status: "error", Message: message}))
	}
	if m.ProviderID == "" || m.MethodID == "" {
		fail("account-login needs providerId and methodId")
		return nil
	}
	if s.Deps.Accounts == nil {
		fail("accounts service unavailable")
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancelMu.Lock()
	if _, exists := s.cancellables[m.RequestID]; exists {
		s.cancelMu.Unlock()
		fail("requestId is already in flight")
		cancel()
		return nil
	}
	s.cancellables[m.RequestID] = cancel
	s.cancelMu.Unlock()
	s.inFlight.Add(1)
	go func() {
		defer s.inFlight.Done()
		// The entry MUST stay registered while Login runs — deleting it up
		// front makes the later `cancel` frame a no-op and strands this run
		// with no terminal result. Unregister only once the login finished.
		defer func() {
			s.cancelMu.Lock()
			delete(s.cancellables, m.RequestID)
			s.cancelMu.Unlock()
			cancel()
		}()

		result := Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success"}
		login, err := s.Deps.Accounts.Login(runCtx, m.ProviderID, accounts.LoginOptions{
			MethodID: strings.TrimSpace(m.MethodID),
			Source:   strings.TrimSpace(m.SourceID),
			Name:     strings.TrimSpace(m.Name),
			Label:    strings.TrimSpace(m.Label),
		})
		switch {
		case err == nil:
			result.Message = fmt.Sprintf("Signed in with %s and saved %s/%s.",
				login.Method.Label, login.Account.Provider, login.Account.Name)
		case errors.Is(err, context.Canceled):
			result.Status = "cancelled"
		default:
			var ae *anyerr.Error
			if errors.As(err, &ae) && ae.Code == "PROVIDER_LOGIN_CANCELLED" {
				result.Status = "cancelled"
			} else {
				result.Status = "error"
				result.Message = err.Error()
			}
		}
		s.write(FrameResult(result))
		if result.Status == "success" {
			if rerr := s.refresh(context.Background()); rerr != nil && rerr != errShutdown {
				s.logErr(rerr)
			}
		}
	}()
	return nil
}

func orString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// applyModelRoles re-activates the client's persistent binding with explicit
// per-role models (the Go counterpart of supervisor model-role actions).
func (s *Server) applyModelRoles(ctx context.Context, clientID string, roles map[string]string) error {
	binding, err := s.Deps.Bindings.GetGlobalContext(ctx, clientID)
	if err != nil {
		return err
	}
	if binding == nil {
		return anyerr.Newf("client "+clientID+" has no active binding to apply roles to",
			"BINDING_NOT_FOUND")
	}
	plan, err := activation.PlanActivation(ctx, activation.Request{
		Mode:       activation.ModePersistent,
		Client:     clientID,
		Source:     &binding.Spec.Source,
		ModelRoles: roles,
	}, s.Deps.Activation, activation.Options{})
	if err != nil {
		return err
	}
	_, err = activation.Execute(ctx, plan, s.Deps.Activation, activation.ExecuteOptions{})
	return err
}

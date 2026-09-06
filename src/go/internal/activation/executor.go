package activation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/transport"
)

// ExecuteResult mirrors ExecuteResult. Cleanup is the idempotent scoped
// rollback for a successful ephemeral run.
type ExecuteResult struct {
	Plan          *Plan
	DryRun        bool
	AlreadyActive bool
	Binding       *bindings.Global
	ProjectBind   *bindings.Project
	ProxyEndpoint string
	JournalID     string
	Isolated      *runtime.EphemeralRuntime
	Cleanup       func(context.Context) error
	Warnings      []string
}

// ExecuteOptions mirrors ExecuteOptions.
type ExecuteOptions struct {
	DryRun        bool
	SkipIfActive  bool
	ProjectRoot   string
	HealthTimeout time.Duration
	OperationID   string

	markAlreadyActive bool
}

// NativeRestorer is implemented by client adapters that can drop AnyPick
// managed config so the client returns to its native login.
type NativeRestorer interface {
	RestoreNative() error
}

type activationContext struct {
	journalID           string
	mutated             bool
	undo                []func() error
	binding             *bindings.Global
	projectBind         *bindings.Project
	checkpointJSON      map[string]interface{}
	clientConfigWritten string
	isolated            *runtime.EphemeralRuntime
	backupPaths         []string
	warnings            []string
}

// Execute ports executeActivation: dry-run short-circuit, then the whole
// critical section under sorted mutation scopes with a journalled rollback.
func Execute(ctx context.Context, plan *Plan, deps *Deps, opts ExecuteOptions) (*ExecuteResult, error) {
	if opts.SkipIfActive && plan.Mode == ModePersistent && plan.BindingSpec != nil {
		existing, err := deps.Bindings.GetGlobalContext(ctx, plan.Client)
		if err != nil {
			return nil, err
		}
		if existing != nil && bindingEqual(existing.Spec, *plan.BindingSpec) {
			// An equal binding still goes through the transaction below — the
			// historical fast path left no durable compensation. We only flag it.
			opts.markAlreadyActive = true
		}
	}
	if opts.DryRun {
		return &ExecuteResult{
			Plan:          plan,
			DryRun:        true,
			ProxyEndpoint: plan.Transport.Endpoint,
		}, nil
	}

	var result *ExecuteResult
	err := lock.WithMutationLocks(ctx, deps.Root, mutationScopes(plan), func(c context.Context) error {
		r, runErr := executeLocked(c, plan, deps, opts)
		if runErr != nil {
			return runErr
		}
		r.AlreadyActive = opts.markAlreadyActive
		result = r
		return nil
	}, 0)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// bindingEqual compares the fields that must re-apply on change (clientOptions
// included so editing models never no-ops).
func bindingEqual(a, b bindings.Spec) bool {
	normalize := func(s bindings.Spec) string {
		if s.ClientOptions == nil {
			s.ClientOptions = map[string]interface{}{}
		}
		data, _ := json.Marshal(map[string]interface{}{
			"client":        s.Client,
			"source":        s.Source,
			"model":         s.Model,
			"clientOptions": s.ClientOptions,
		})
		return string(data)
	}
	return normalize(a) == normalize(b)
}

// mutationScopes ports mutationScopes. Hub activations ADDITIONALLY hold the
// hub/<name> scope for the whole critical section: the undo closures below
// touch hub config/routes directly, and rollback runs inside these scopes —
// releasing the hub lock after the attach step (the old behavior) let a
// concurrent mutation interleave with compensation (invariant #5).
func mutationScopes(plan *Plan) []string {
	scopes := []string{"client/" + plan.Client}
	if plan.ResolvedSource.Ref.Kind == refs.KindProxyHub {
		scopes = append(scopes, "hub/"+plan.ResolvedSource.Ref.Name)
	} else {
		scopes = append(scopes, refs.MutationScopeForRef(plan.ResolvedSource.Ref))
	}
	return scopes
}

func executeLocked(ctx context.Context, plan *Plan, deps *Deps, opts ExecuteOptions) (*ExecuteResult, error) {
	sourceRefStr := refs.SerializeRef(plan.ResolvedSource.Ref)
	affected := []string{"client/" + plan.Client, sourceRefStr}

	stepKinds := make([]interface{}, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		stepKinds = append(stepKinds, string(s.Kind))
	}
	params := map[string]interface{}{
		"client": plan.Client,
		"source": sourceRefStr,
		"mode":   string(plan.Mode),
		"steps":  stepKinds,
	}
	entry, err := deps.Journal.CreateContext(ctx, "activate:"+string(plan.Mode), journal.CreateOptions{
		ID:                opts.OperationID,
		AffectedResources: affected,
		Params:            params,
		State:             journal.StatePlanned,
	})
	if err != nil {
		return nil, err
	}
	journalID := entry.ID

	actx := &activationContext{journalID: journalID}
	fail := func(cause error) (*ExecuteResult, error) {
		rolledBack := runRollback(actx)
		finalizeJournal(ctx, deps, journalID, rolledBack)
		mutated := actx.mutated && !rolledBack
		var ae *anyerr.Error
		if errorsAs(cause, &ae) {
			ae.Mutated = mutated
		}
		return nil, cause
	}

	if _, err := deps.Journal.UpdateContext(ctx, journalID, journal.Entry{State: journal.StateExecuting}); err != nil {
		return fail(err)
	}

	// Defense in depth for hand-built plans: the planner rejects Unsupported
	// at plan time, but Execute must still refuse it rather than mutate.
	// ExternalManualProxy is NOT gated here — StepValidateExternalDep in
	// plan.Steps is the single enforcement point (invariant #3: the plan is
	// the source of truth; the executor must not re-derive behavior from
	// transport capability, or planner and executor can diverge silently).
	if plan.Transport.Capability == transport.Unsupported {
		return fail(anyerr.Newf(
			"Unsupported transport for "+plan.Client+" × "+plan.ResolvedSource.Display,
			"UNSUPPORTED_TRANSPORT", anyerr.WithExitCode(anyerr.CapabilityConflict)))
	}

	for _, step := range plan.Steps {
		if err := runStep(ctx, step, plan, deps, opts, actx, journalID); err != nil {
			return fail(err)
		}
	}

	if _, err := deps.Journal.UpdateContext(ctx, journalID, journal.Entry{
		State:       journal.StateVerifying,
		BackupPaths: actx.backupPaths,
	}); err != nil {
		return fail(err)
	}

	// An ephemeral session still owns mutable resources until its child exits;
	// keep its journal recoverable until cleanup has run.
	if plan.Mode != ModeEphemeral {
		if _, err := deps.Journal.UpdateContext(ctx, journalID, journal.Entry{State: journal.StateCommitted}); err != nil {
			return fail(err)
		}
	}

	result := &ExecuteResult{
		Plan:          plan,
		ProxyEndpoint: plan.Transport.Endpoint,
		JournalID:     journalID,
		Warnings:      actx.warnings,
		Binding:       actx.binding,
		ProjectBind:   actx.projectBind,
		Isolated:      actx.isolated,
	}
	if plan.Mode == ModeEphemeral {
		result.Cleanup = makeEphemeralCleanup(actx, deps, mutationScopes(plan), journalID)
	}
	return result, nil
}

// runStep dispatches one planned step. The switch must cover every StepKind;
// planner-time markers are explicit no-ops exactly like STEP_HANDLERS in TS.
func runStep(ctx context.Context, step PlanStep, plan *Plan, deps *Deps, opts ExecuteOptions, actx *activationContext, journalID string) error {
	switch step.Kind {
	case StepWriteNativeAuth:
		return writeNativeAuth(ctx, plan, deps, actx, journalID)

	case StepStartProxy:
		return startManagedProxy(ctx, plan, deps, actx)

	case StepEnsureProxyHub, StepAttachProxyHubRoute, StepWaitForHubHealth, StepValidateProxyHubRoute:
		return fmt.Errorf("the Proxy Hub service is unavailable in this composition")

	case StepCreateTempClientHome, StepCreateEnvironmentOverlay:
		return createEphemeralHome(ctx, plan, deps, actx)

	case StepValidateCredential:
		return validateCredential(ctx, plan, deps)

	case StepValidateExternalDep:
		return validateExternalDep(plan)

	case StepWaitForHealth:
		return waitForProxyHealth(plan, opts)

	case StepVerifyEffectiveState:
		return verifyEffectiveState(plan, deps, actx)

	case StepWriteClientConfig:
		return writeClientConfig(ctx, plan, deps, opts, actx)

	case StepCommitGlobalBinding:
		return commitGlobalBinding(ctx, plan, deps, actx)

	case StepCommitProjectBinding:
		return commitProjectBinding(ctx, step, plan, deps, opts, actx)

	// Planner-time markers: resolved while building the plan, no execution work.
	case StepResolveSource, StepExpandPreset, StepValidateCompatibility,
		StepResolveTransport, StepAllocateProxyLease,
		StepInspectClientState, StepReleaseLease, StepRestoreTemporaryState,
		StepSpawnChild:
		return nil

	default:
		return fmt.Errorf("no handler for plan step %q", step.Kind)
	}
}

// validateExternalDep is the execution half of StepValidateExternalDep: the
// planner emits the step when the resolved capability is ExternalManualProxy
// (external binary missing at plan time). Re-resolve at execution time so a
// binary installed between plan and execute is honoured, and a still-missing
// binary fails here — in the step — rather than in a pre-loop gate.
func validateExternalDep(plan *Plan) error {
	if plan.ResolvedSource.Adapter.TransportFor(plan.Client) == transport.ExternalManualProxy {
		return anyerr.Newf(
			"Required external proxy for "+plan.ResolvedSource.Display+" is not installed.\n\nInstall the provider proxy and retry.",
			"MISSING_DEPENDENCY",
			anyerr.WithExitCode(anyerr.MissingDependency),
			anyerr.WithSuggestions("anypick doctor "+plan.Client))
	}
	return nil
}

// validateCredential asserts the credential behind the source is usable before
// anything mutates live state.
func validateCredential(ctx context.Context, plan *Plan, deps *Deps) error {
	ref := plan.ResolvedSource.Ref
	switch ref.Kind {
	case refs.KindAccount:
		account, err := deps.Accounts.Get(ctx, ref.Provider, ref.Name)
		if err != nil {
			return err
		}
		if account == nil {
			return anyerr.Newf(
				"Account "+ref.Provider+"/"+ref.Name+" has no stored credential snapshot.",
				"AUTH_REQUIRED",
				anyerr.WithExitCode(anyerr.AuthRequired),
				anyerr.WithSuggestions("anypick add "+ref.Provider+" --name "+ref.Name))
		}
		return nil
	case refs.KindGateway:
		pr, err := deps.Profiles.GetContext(ctx, ref.Name)
		if err != nil {
			return err
		}
		if pr == nil {
			return anyerr.Newf("Gateway not found: "+ref.Name, "GATEWAY_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound))
		}
		return nil
	case refs.KindAccountPool:
		pool, err := deps.Pools.GetContext(ctx, ref.Provider)
		if err != nil {
			return err
		}
		enabled := 0
		if pool != nil {
			for _, m := range pool.Members {
				if m.Enabled {
					enabled++
				}
			}
		}
		if enabled == 0 {
			return anyerr.Newf(
				"Pool "+ref.Provider+" has no enabled members to activate.",
				"AUTH_REQUIRED",
				anyerr.WithExitCode(anyerr.AuthRequired),
				anyerr.WithSuggestions("anypick proxy pool "+ref.Provider))
		}
		return nil
	}
	return nil
}

// writeNativeAuth ports the WriteNativeAuth handler: refuse ephemeral, skip
// when already live-matched, preflight, durable checkpoint, then accounts.use.
func writeNativeAuth(ctx context.Context, plan *Plan, deps *Deps, actx *activationContext, journalID string) error {
	if plan.Mode == ModeEphemeral {
		return anyerr.Newf(
			"Internal plan error: ephemeral activation must not write live native auth.",
			"UNSUPPORTED_TRANSPORT", anyerr.WithExitCode(anyerr.CapabilityConflict))
	}
	ref := plan.ResolvedSource.Ref
	if ref.Kind != refs.KindAccount {
		return nil
	}
	target, err := deps.Accounts.Svc.Store.GetContext(ctx, ref.Provider, ref.Name)
	if err != nil {
		return err
	}
	if target == nil {
		return anyerr.Newf("Account "+ref.Provider+"/"+ref.Name+" was not found.",
			"ACCOUNT_NOT_FOUND", anyerr.WithExitCode(anyerr.NotFound))
	}

	// Binding to the login that is already live must not rewrite native auth.
	current, err := deps.Accounts.Svc.Current(ctx, ref.Provider)
	if err != nil {
		return err
	}
	if current.Active == target.Meta.Name && current.IsLiveMatch {
		return nil
	}

	pv, err := deps.Providers.Get(ref.Provider)
	if err != nil {
		return err
	}
	if pr, ok := pv.(provider.PreflightRestorer); ok {
		// Must run before checkpointing or touching proxy/client files.
		if err := pr.PreflightRestore(target.SnapshotDir); err != nil {
			return err
		}
	}

	// Durable checkpoint before provider.restore; register undo before the call
	// so a provider that writes one auth file and then throws is compensated.
	cp, err := deps.Accounts.Svc.CheckpointLiveAuth(ctx, ref.Provider, true)
	if err != nil {
		return err
	}
	cpData, _ := json.Marshal(cp)
	var cpMap map[string]interface{}
	_ = json.Unmarshal(cpData, &cpMap)

	entry, _ := deps.Journal.GetContext(ctx, journalID)
	nextParams := map[string]interface{}{}
	if entry != nil && entry.Params != nil {
		for k, v := range entry.Params {
			nextParams[k] = v
		}
	}
	nextParams["nativeAuthCheckpoint"] = cpMap
	if _, err := deps.Journal.UpdateContext(ctx, journalID, journal.Entry{Params: nextParams}); err != nil {
		_ = deps.Accounts.Svc.DiscardLiveAuthCheckpoint(cp)
		return err
	}

	actx.mutated = true
	actx.checkpointJSON = cpMap
	actx.undo = append(actx.undo, func() error {
		// Re-enter with the activation's context: it carries the coordinator
		// locks Execute already holds, and a fresh context here would deadlock.
		// WithoutCancel keeps those values while surviving the cancellation
		// that probably caused this rollback in the first place — compensation
		// must be able to finish after its trigger is cancelled, or a
		// cancelled login leaves the previous credentials unrestored.
		restoreErr := deps.Accounts.Svc.RestoreLiveAuthCheckpoint(context.WithoutCancel(ctx), cp)
		_ = deps.Accounts.Svc.DiscardLiveAuthCheckpoint(cp)
		return restoreErr
	})

	_, err = deps.Accounts.Svc.Use(ctx, ref.Provider, ref.Name, UseNoProxyOption())
	if err != nil {
		return err
	}
	return nil
}

// UseNoProxyOption keeps native-auth switches off the proxy path.
func UseNoProxyOption() accounts.UseOptions { return accounts.UseOptions{NoProxy: true} }

// createEphemeralHome ports createEphemeralHome: exactly one isolated runtime
// per ephemeral run; cleanup registered before anything else can fail. The
// SpawnChild step stays a no-op — the launcher spawns the child using
// result.Isolated.
func createEphemeralHome(ctx context.Context, plan *Plan, deps *Deps, actx *activationContext) error {
	if deps.Runtime == nil {
		return anyerr.Newf("runtime service is unavailable in this composition",
			"CLIENT_CONFIG_INVALID")
	}
	ref := plan.ResolvedSource.Ref
	switch ref.Kind {
	case refs.KindAccount, refs.KindGateway, refs.KindProxyHub:
	default:
		return nil
	}
	apiKey := ""
	if mp := plan.Transport.ManagedProxy; mp != nil && mp.Token != "" {
		apiKey = mp.Token
	}
	endpoint := plan.Transport.Endpoint
	model := explicitModelID(plan.Model)
	// A gateway carries its own endpoint, credential and default model on the
	// saved profile; the plan only ever sees the raw URL. Resolve it the same
	// way the persistent write does, or `run` silently produces a config with
	// a placeholder key and no model where `use` would have written both.
	if ref.Kind == refs.KindGateway && deps.Profiles != nil {
		in, inErr := deps.Runtime.GatewayApplyInputFor(ctx, ref.Name, plan.Client)
		if inErr != nil {
			return inErr
		}
		endpoint, apiKey = in.Endpoint, in.APIKey
		if model == "" {
			model = in.Model
		}
	}
	iso, err := deps.Runtime.CreateEphemeralRuntimeContext(ctx, runtime.EphemeralSpec{
		ClientID: plan.Client, Endpoint: endpoint, APIKey: apiKey,
		DefaultModel: model, ModelRoles: rolesFromSpec(plan.BindingSpec),
		SourceKind: string(ref.Kind), SourceProvider: ref.Provider, SourceName: ref.Name,
		TransportCapability: plan.Transport.Capability,
	})
	if err != nil {
		return err
	}
	actx.isolated = iso
	actx.mutated = true
	actx.undo = append(actx.undo, func() error { return iso.Cleanup() })
	return nil
}

// startHubProxy boots the detached hub child (idempotent: an already-healthy
// hub on the planned port wins), waits for health, and records the undo step.
// The route token was attached at plan time and already lives in
// plan.Transport.ManagedProxy.Token.
func startHubProxy(ctx context.Context, plan *Plan, deps *Deps, actx *activationContext) error {
	mp := plan.Transport.ManagedProxy
	if mp == nil {
		return anyerr.Newf("hub plan is missing its managed proxy target", "HUB_START_FAILED")
	}
	name := plan.ResolvedSource.Ref.Name

	// Route attach + port assignment are persisted mutations: they run under
	// the hub scope with compensations registered before execution proceeds
	// (invariant #3 — planned → journalled → executed).
	var token string
	var endpoint string
	err := lock.WithMutationLock(ctx, deps.Root, "hub/"+name, func(c context.Context) error {
		cfg, gerr := deps.Hub.GetConfigContext(c, name)
		if gerr != nil {
			return gerr
		}
		if cfg == nil || !cfg.Enabled {
			return anyerr.Newf("Proxy Hub `"+name+"` disappeared during activation.",
				"HUB_NOT_ENABLED")
		}
		prevPort := cfg.Port
		if cfg.Port == 0 {
			picked, perr := proxy.PickFreePort()
			if perr != nil {
				return anyerr.Wrapf(perr, "pick hub port", "HUB_START_FAILED")
			}
			cfg.Port = picked
			saved, serr := deps.Hub.UpsertConfigContext(c, *cfg)
			if serr != nil {
				return serr
			}
			cfg = &saved
			actx.mutated = true
			actx.undo = append(actx.undo, func() error {
				// Restore the kernel-assigned choice only if nothing else took it.
				cur, gerr2 := deps.Hub.GetConfigContext(c, name)
				if gerr2 != nil || cur == nil || cur.Port != picked {
					return nil
				}
				cur.Port = prevPort
				_, uerr := deps.Hub.UpsertConfigContext(c, *cur)
				return uerr
			})
		}

		routeToken, terr := hub.NewRouteToken()
		if terr != nil {
			return anyerr.Wrapf(terr, "generate route token", "HUB_START_FAILED")
		}
		token = routeToken
		routeID := fmt.Sprintf("%s/route-%s", name, plan.Client)
		// Exact compensation: AttachRoute overwrites the whole row (manifest AND
		// token hash), so rollback must restore the PRIOR row verbatim — a plain
		// detach would destroy a route the user had before this activation.
		prevRoute, gerr := deps.Hub.SnapshotRouteContext(c, routeID)
		if gerr != nil {
			return gerr
		}
		if aerr := deps.Hub.AttachRouteContext(c, hub.Route{
			RouteID: routeID, HubName: name, ClientID: plan.Client, Token: token,
		}); aerr != nil {
			return aerr
		}
		actx.mutated = true
		undoCtx := context.WithoutCancel(c)
		actx.undo = append(actx.undo, func() error {
			if prevRoute != nil {
				return deps.Hub.RestoreRouteRowContext(undoCtx, prevRoute)
			}
			return deps.Hub.DetachRouteContext(undoCtx, routeID)
		})

		host := cfg.Host
		if host == "" {
			host = "127.0.0.1"
		}
		endpoint = fmt.Sprintf("http://%s:%d", host, cfg.Port)
		mp.Port = cfg.Port
		return nil
	})
	if err != nil {
		return err
	}
	mp.Token = token
	plan.Transport.Endpoint = endpoint

	// Adopt an already-healthy child on this exact endpoint; otherwise spawn
	// one. The spawned pid file carries an ADR-0003 record; rollback stops
	// ONLY that instance-verified child — an adopted child keeps serving.
	if hubHealthy(endpoint) {
		return nil
	}
	pid, serr := deps.SpawnHubChild(name)
	if serr != nil {
		return anyerr.Wrapf(serr, "spawn hub child", "HUB_START_FAILED")
	}
	actx.mutated = true
	healthURL := strings.TrimSuffix(endpoint, "/") + "/health"
	actx.undo = append(actx.undo, func() error {
		// Compensation for the child THIS activation started. ownership.Stop
		// re-verifies identity from the trusted record plus the /health echo;
		// if another actor already replaced/reused it, nothing is signalled.
		pidFile := filepath.Join(deps.Root, "runtime", "hub", name, "service.pid")
		if ownership.Load(pidFile) == nil {
			return nil
		}
		if !ownership.Stop(pidFile, ownership.StopOptions{
			Timeout: 3 * time.Second,
			Health: func(r ownership.Record) bool {
				return hubInstanceEcho(healthURL, r.InstanceID)
			},
		}) {
			return anyerr.Newf("could not verify ownership of spawned hub `"+name+"`; not signalling it",
				"HUB_OWNERSHIP_UNVERIFIED")
		}
		return nil
	})
	_ = pid
	if err := waitForProxyHealth(plan, ExecuteOptions{HealthTimeout: 8 * time.Second}); err != nil {
		return anyerr.Wrapf(err, "hub did not become healthy", "HUB_START_FAILED")
	}
	return nil
}

// hubInstanceEcho proves a hub child's identity: /health must echo the exact
// instance id recorded at spawn (ADR 0003 §3). Bounded, no retries.
func hubInstanceEcho(healthURL, wantInstanceID string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var payload struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload) != nil {
		return false
	}
	return payload.OK && payload.InstanceID == wantInstanceID
}

// hubHealthy probes an existing hub endpoint (bounded, no retries).
func hubHealthy(endpoint string) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// startManagedProxy ports the StartProxy handler: start the account proxy,
// record the lease, and register inverse operations. Pool sources are routed
// through the pool lifecycle (proxy pool start), not a per-account proxy.
func startManagedProxy(ctx context.Context, plan *Plan, deps *Deps, actx *activationContext) error {
	ref := plan.ResolvedSource.Ref
	if ref.Kind == refs.KindProxyHub {
		return startHubProxy(ctx, plan, deps, actx)
	}
	if deps.Proxy == nil {
		return anyerr.Newf(
			"proxy service is unavailable in this composition",
			"PROXY_START_FAILED")
	}

	// Pool sources route through the pool lifecycle: one shared provider-scope
	// proxy serving every enabled member (activation-executor.ts startManagedProxy
	// account-pool branch). Undo stops the whole pool when we started it now.
	if ref.Kind == refs.KindAccountPool {
		started, err := deps.Proxy.StartPoolProxy(ctx, ref.Provider)
		if err != nil {
			return err
		}
		actx.mutated = true
		if started.StartedNow {
			undoCtx := context.WithoutCancel(ctx)
			actx.undo = append(actx.undo, func() error {
				return deps.Proxy.StopPoolProxy(undoCtx, ref.Provider)
			})
		}
		fillManagedProxy(plan, started)
		return nil
	}

	if ref.Kind != refs.KindAccount {
		return anyerr.Newf(
			fmt.Sprintf("source %q needs a pool transport; run `anypick proxy pool start %s` first",
				refs.DisplayRef(ref), ref.Provider),
			"TRANSPORT_UNAVAILABLE")
	}

	started, err := deps.Proxy.StartProxy(ctx, ref.Provider, ref.Name)
	if err != nil {
		return err
	}
	actx.mutated = true
	if started.StartedNow {
		undoCtx := context.WithoutCancel(ctx)
		actx.undo = append(actx.undo, func() error {
			return deps.Proxy.StopIfLeaseOwned(undoCtx,
				ref.Provider, ref.Name, started.InstanceID, started.LeaseID)
		})
	}
	fillManagedProxy(plan, started)
	return nil
}

// fillManagedProxy publishes lease/token/port onto the plan transport and the
// endpoint for client injection. The token is independent of lease
// bookkeeping; never fall back to an unauthenticated placeholder.
func fillManagedProxy(plan *Plan, started *proxy.StartResult) {
	if mp := plan.Transport.ManagedProxy; mp != nil {
		if started.LeaseID != "" && mp.LeaseID == "pending" {
			mp.LeaseID = started.LeaseID
		}
		if started.Token != "" {
			mp.Token = started.Token
			mp.Port = started.Port
		}
	}
	if started.Endpoint != "" {
		plan.Transport.Endpoint = started.Endpoint
	}
}

// writeClientConfig ports the WriteClientConfig handler branches:
//   - account + managed proxy → inject endpoint into native client config;
//   - gateway (persistent/project) → apply the saved profile to the client;
//   - codex + direct persistent → restore native model/provider config.
//
// Each mutating branch persists exact file compensation BEFORE the adapter
// runs, and registers its inverse on the undo stack.
func writeClientConfig(ctx context.Context, plan *Plan, deps *Deps, opts ExecuteOptions, actx *activationContext) error {
	ref := plan.ResolvedSource.Ref
	direct := plan.Transport.Capability == transport.Direct
	managedProxy := plan.Transport.Endpoint != "" && !direct

	if managedProxy && (ref.Kind == refs.KindAccount || ref.Kind == refs.KindProxyHub ||
		ref.Kind == refs.KindAccountPool) {
		if deps.Runtime == nil {
			return anyerr.Newf("runtime service is unavailable in this composition",
				"CLIENT_CONFIG_INVALID")
		}
		recovery, err := prepareClientRecoveryContext(ctx, plan.Client, deps, actx)
		if err != nil {
			return err
		}
		actx.mutated = true
		actx.undo = append(actx.undo, func() error {
			return runtime.RestoreClientConfigRecovery(recovery.records)
		})
		if _, err := deps.Runtime.ApplyProxyEndpoint(ctx, plan.Client, runtime.ProxyEndpointOptions{
			Endpoint: plan.Transport.Endpoint, APIKey: plan.Transport.ManagedProxy.Token,
			DefaultModel: explicitModelID(plan.Model), ModelRoles: rolesFromSpec(plan.BindingSpec),
			Label: refs.DisplayRef(ref), SourceKind: string(ref.Kind),
			SourceProvider: ref.Provider, SourceName: ref.Name,
			TransportCapability: plan.Transport.Capability,
		}); err != nil {
			return err
		}
		actx.clientConfigWritten = plan.Client
		return nil
	}

	if ref.Kind == refs.KindGateway && (plan.Mode == ModePersistent || plan.Mode == ModeProject) {
		if deps.Runtime == nil {
			return anyerr.Newf("runtime service is unavailable in this composition",
				"CLIENT_CONFIG_INVALID")
		}
		recovery, err := prepareClientRecoveryContext(ctx, plan.Client, deps, actx)
		if err != nil {
			return err
		}
		actx.mutated = true
		actx.undo = append(actx.undo, func() error {
			return runtime.RestoreClientConfigRecovery(recovery.records)
		})
		if _, err := deps.Runtime.ApplyGatewayProfile(ctx, ref.Name, plan.Client); err != nil {
			return err
		}
		actx.clientConfigWritten = plan.Client
		return nil
	}

	if direct && ref.Kind == refs.KindAccount && plan.Mode == ModePersistent {
		cad, err := deps.Clients.Get(plan.Client)
		if err != nil {
			return err
		}
		if restorer, ok := cad.(NativeRestorer); ok {
			if err := restorer.RestoreNative(); err != nil {
				return err
			}
			actx.mutated = true
		}
		return nil
	}

	if managedProxy || ref.Kind == refs.KindGateway {
		// Ephemeral modes reach here only via planner guarantees; anything else
		// means a branch was missed.
		return fmt.Errorf("WriteClientConfig: unsupported combination (%s × %s × %s)",
			ref.Kind, plan.Mode, plan.Transport.Capability)
	}
	return nil
}

// recoveryBundle carries prepared compensation records for this activation.
type recoveryBundle struct {
	clientID string
	records  []string
}

func prepareClientRecoveryContext(ctx context.Context, clientID string, deps *Deps, actx *activationContext) (*recoveryBundle, error) {
	if deps.Runtime == nil {
		return &recoveryBundle{clientID: clientID}, nil
	}
	records, err := deps.Runtime.PrepareClientConfigRecovery(clientID, actx.journalID)
	if err != nil {
		return nil, err
	}
	actx.backupPaths = append(actx.backupPaths, records...)
	_, _ = deps.Journal.UpdateContext(ctx, actx.journalID, journal.Entry{BackupPaths: actx.backupPaths})
	return &recoveryBundle{clientID: clientID, records: records}, nil
}

func explicitModelID(model bindings.ModelSelection) string {
	if model.Mode == "explicit" {
		return model.ID
	}
	return ""
}

func rolesFromSpec(spec *bindings.Spec) map[string]string {
	if spec == nil {
		return nil
	}
	raw, ok := spec.ClientOptions["modelRoles"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out[k] = strings.TrimSpace(s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func waitForProxyHealth(plan *Plan, opts ExecuteOptions) error {
	endpoint := plan.Transport.Endpoint
	if endpoint == "" {
		return nil
	}
	timeout := opts.HealthTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: timeout}
	for time.Now().Before(deadline) {
		resp, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	ref := plan.ResolvedSource.Ref
	hint := ""
	if ref.Kind == "account" {
		hint = "anypick proxy logs " + ref.Provider + " " + ref.Name
	}
	return anyerr.Newf(
		"Proxy for "+plan.ResolvedSource.Display+" is not responding at "+endpoint+".",
		"HEALTH_FAILURE", anyerr.WithExitCode(anyerr.HealthFailure),
		anyerr.WithSuggestions(hint, "anypick doctor "+plan.Client))
}

// verifyEffectiveState reads back client config; non-fatal by design.
func verifyEffectiveState(plan *Plan, deps *Deps, actx *activationContext) error {
	if plan.Mode == ModeEphemeral {
		return nil
	}
	cad, err := deps.Clients.Get(plan.Client)
	if err != nil {
		return err
	}
	inspected, err := cad.Inspect()
	if err != nil {
		actx.warnings = append(actx.warnings,
			"Could not read back "+plan.Client+" configuration after activation")
		return nil
	}
	if inspected == nil || !inspected.Present {
		reason := "no anypick-managed configuration after activation"
		if inspected != nil && len(inspected.Issues) > 0 {
			reason += ": " + strings.Join(inspected.Issues, "; ")
		}
		actx.warnings = append(actx.warnings, plan.Client+" reports "+reason)
	}
	return nil
}

func commitGlobalBinding(ctx context.Context, plan *Plan, deps *Deps, actx *activationContext) error {
	if plan.Mode != ModePersistent || plan.BindingSpec == nil || plan.Provenance == nil {
		return fmt.Errorf("internal plan error: invalid global binding commit")
	}
	previous, err := deps.Bindings.GetGlobalContext(ctx, plan.Client)
	if err != nil {
		return err
	}
	upserted, err := deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client:     plan.Client,
		Spec:       *plan.BindingSpec,
		Provenance: *plan.Provenance,
	})
	if err != nil {
		return err
	}
	actx.binding = &upserted
	actx.mutated = true
	// Compensation uses the activation's own context, not a fresh one: it
	// carries the coordinator locks Execute holds, and a context.Background()
	// here would re-enter and deadlock the moment bindings takes a lock.
	// WithoutCancel keeps those values while surviving the cancellation that
	// probably triggered this rollback in the first place.
	undoCtx := context.WithoutCancel(ctx)
	actx.undo = append(actx.undo, func() error {
		if previous != nil {
			_, err := deps.Bindings.UpsertGlobalContext(undoCtx, *previous)
			return err
		}
		return deps.Bindings.DeleteGlobalContext(undoCtx, plan.Client)
	})
	return nil
}

func commitProjectBinding(ctx context.Context, step PlanStep, plan *Plan, deps *Deps, opts ExecuteOptions, actx *activationContext) error {
	if plan.Mode != ModeProject || plan.BindingSpec == nil || plan.Provenance == nil {
		return fmt.Errorf("internal plan error: invalid project binding commit")
	}
	root, _ := step.Params["projectRoot"].(string)
	if root == "" {
		root = opts.ProjectRoot
	}
	if root == "" {
		return anyerr.Newf("projectRoot required for project binding commit", "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	previous, err := deps.Bindings.GetProjectContext(ctx, root, plan.Client)
	if err != nil {
		return err
	}
	upserted, err := deps.Bindings.UpsertProjectContext(ctx, bindings.Project{
		ProjectRoot: root,
		Client:      plan.Client,
		Spec:        *plan.BindingSpec,
		Provenance:  *plan.Provenance,
	})
	if err != nil {
		return err
	}
	actx.projectBind = &upserted
	actx.mutated = true
	// Same compensation contract as the global-binding undo above: the
	// activation's context (locks) with WithoutCancel (survives cancellation).
	undoCtx := context.WithoutCancel(ctx)
	actx.undo = append(actx.undo, func() error {
		if previous != nil {
			_, err := deps.Bindings.UpsertProjectContext(undoCtx, *previous)
			return err
		}
		return deps.Bindings.DeleteProjectContext(undoCtx, root, plan.Client)
	})
	return nil
}

// runRollback unwinds the undo stack in reverse; reports full restoration.
func runRollback(actx *activationContext) bool {
	if !actx.mutated {
		return true
	}
	allUndone := true
	for i := len(actx.undo) - 1; i >= 0; i-- {
		if err := actx.undo[i](); err != nil {
			allUndone = false
		}
	}
	return allUndone
}

// makeEphemeralCleanup returns an idempotent scoped cleanup for a successful
// ephemeral run (signal/error paths can race normal child exit).
func makeEphemeralCleanup(actx *activationContext, deps *Deps, scopes []string, journalID string) func(context.Context) error {
	var (
		once       sync.Once
		cleanupErr error
	)
	return func(ctx context.Context) error {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			cleanupErr = lock.WithMutationLocks(cleanupCtx, deps.Root, scopes, func(c context.Context) error {
				restored := runRollback(actx)
				finalizeJournal(c, deps, journalID, restored)
				if !restored {
					return anyerr.Newf(
						"Ephemeral run cleanup could not restore all local state. Run anypick doctor before switching accounts.",
						"EPHEMERAL_CLEANUP_FAILED")
				}
				return nil
			}, 0)
		})
		return cleanupErr
	}
}

func finalizeJournal(ctx context.Context, deps *Deps, journalID string, rolledBack bool) {
	state := journal.StateFailed
	if rolledBack {
		state = journal.StateRolledBack
	}
	_, _ = deps.Journal.UpdateContext(ctx, journalID, journal.Entry{State: state})
}

func errorsAs(err error, target **anyerr.Error) bool {
	for err != nil {
		if e, ok := err.(*anyerr.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var accountRefPattern = regexp.MustCompile(`^account/([^/]+)/[^/]+$`)
var poolRefPattern = regexp.MustCompile(`^pool/([^/]+)$`)

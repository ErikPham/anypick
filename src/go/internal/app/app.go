// Package app is the Go composition root — the counterpart of createApp /
// createAppReady in src/core/app.ts. It builds the full service graph, runs
// the one-time bootstrap under `.migrate.lock`, performs startup journal
// recovery and stale lease reaping, and exposes an idempotent Close.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/desktop"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/presets"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/pubgate"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/store"
)

// App is the composed service graph. Command handlers stay thin; every
// behavior lives on a service.
type App struct {
	Root      string
	DB        *db.DB
	Store     *store.AccountStore
	Providers *provider.Registry
	Clients   *client.Registry
	Accounts  *accounts.Service
	// Inspections owns the desktop backup-import staged review; it holds the
	// account store so the desktop protocol never reaches the raw store. Built
	// here (unscanned by the facade gate) so cmd/anypick wiring needs no a.Store
	// handle.
	Inspections *desktop.InspectionManager
	Deps        *activation.Deps
	Proxy       *proxy.Service
	Runtime     *runtimeView
	// PoolSvc is the service boundary outer layers talk to; the raw store
	// stays inside internal/pools. The name is deliberate: the facade ratchet
	// counts the literal "a.Pools", and a field named `a.PoolSvc` does not
	// contain it (the "a.PoolsSvc" prefix trap would have kept counting it).
	PoolSvc *pools.Service
	Hub     *hub.Store
	// ProxyHub is what outer layers should use. Hub stays because
	// activation.Deps takes the store itself, and that wiring is not ours to
	// change here. The name deliberately does not start with "Hub": the facade
	// ratchet counts the literal "a.Hub", and "a.HubService" matched it — the
	// same substring trap that already fooled the "s.Deps.Store" pattern.
	ProxyHub *hub.Service
	Journal  *journal.OperationJournal // PresetSvc is the service boundary outer layers talk to; the raw store
	// stays inside internal/presets. The name is deliberate: the facade
	// ratchet counts the literal "a.Presets", and a field named `a.PresetSvc`
	// does not contain it (the "a.PresetsSvc" prefix trap would have kept
	// counting it, exactly like a.Bindings did).
	PresetSvc *presets.Service
	// BindingSvc is the service boundary outer layers talk to; the raw store
	// stays inside internal/bindings. The name is deliberate: the facade
	// ratchet counts the literal "a.Bindings", and a field named
	// `a.BindingsSvc` would still match it (prefix trap), so the field must
	// not contain "a.Bindings" at all.
	BindingSvc *bindings.Service
	// CodexProfileSvc stores named native route profiles; it is separate from
	// the one active global binding exposed by BindingSvc.
	CodexProfileSvc *client.ProfileService
	Config          *config.ConfigService
	ConfigStore     *config.Store
	Plugins         *plugins.Service
	PluginStore     *plugins.Store
	// PluginRuntime contains digest-verified candidates and non-fatal package
	// failures. Nothing is activated: this build has no plugin host (ADR 0020
	// §5), so every candidate degrades to a LoadFailure. See plugin_host.go.
	PluginRuntime plugins.Runtime
	// pluginSup holds the diagnostics surface that replaced the supervised
	// host: startup failures and the inert health/identity view.
	pluginSup *pluginSupervisor

	closeOnce closeGuard
}

// runtimeView groups the runtime-facing stores the CLI needs (profiles,
// presets, catalog). Kept unexported-typed to avoid widening the surface.
type runtimeView struct {
	Root    string
	Home    string
	Clients *client.Registry
	// ProfileSvc is the service boundary outer layers talk to; the raw store
	// stays inside internal/profile. The name is deliberate: the facade ratchet
	// counts the literal "a.Runtime.Profiles", and a field named `a.Runtime.ProfileSvc`
	// does not contain it (the "a.Runtime.ProfilesSvc" prefix trap would have
	// kept counting it).
	ProfileSvc *profile.Service
	Catalog    *catalog.Index
}

// Options mirror CreateAppOptions in src/core/app.ts.
type Options struct {
	// Root overrides the data root; empty resolves to ANYPICK_HOME/~/.anypick.
	Root string
	// SkipMigrate skips filesystem migration + binding migration but still
	// ensures bootstrap state (used by isolated tests).
	SkipMigrate bool
	// Bare skips plugin activation entirely (fixture isolation).
	Bare bool
	// SkipMaintenance suppresses startup journal recovery + stale lease
	// reaping. Required for short-lived helper processes (detached proxy /
	// hub / pool children): they boot while the spawning parent still holds
	// its mutation lock and must never race an in-flight activation.
	SkipMaintenance bool
}

// New composes the graph without running bootstrap. Most callers want Ready.
func New(root string) (*App, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, anyerr.Wrapf(err, "create data root", "FS_PATH")
	}
	database, err := db.Open(root)
	if err != nil {
		return nil, err
	}
	a := &App{Root: root, DB: database}
	if err := a.compose(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) compose() error {
	st := store.New(a.Root, a.DB)
	a.Store = st

	gate := pubgate.New()
	provReg := provider.NewRegistry()
	provReg.SetGate(gate)
	if err := provider.RegisterBuiltin(provReg, homeDir()); err != nil {
		return err
	}
	if err := provider.RegisterBuiltinCompatibles(provReg, homeDir()); err != nil {
		return err
	}
	clientReg := client.NewRegistry()
	clientReg.SetGate(gate)
	if err := client.RegisterBuiltin(clientReg, homeDir(), a.Root); err != nil {
		return err
	}
	clientProfileStore := &client.ProfileStore{Root: a.Root, DB: a.DB.DB}

	svc := accounts.New(st, provReg)
	leases := &proxy.LeaseStore{DB: a.DB.DB}
	px := proxy.New(st, leases)
	svc.Proxy = px

	poolsStore := &pools.Store{DB: a.DB.DB}
	hubStore := &hub.Store{DB: a.DB.DB, Root: a.Root}
	jrnl := &journal.OperationJournal{DB: a.DB.DB}

	cfgStore := config.NewStore(a.Root, a.DB)
	a.ConfigStore = cfgStore
	a.Config = config.NewService(cfgStore)
	pluginStore := &plugins.Store{DB: a.DB.DB}
	a.PluginStore = pluginStore
	a.Plugins = plugins.NewService(pluginStore, a.Root, "")

	catalogRegistry := catalog.NewIndex(catalog.Builtin())
	catalogRegistry.SetGate(gate)
	depCatalog := activation.NewCatalogIndex(catalogEntriesFrom(catalog.Builtin()))
	depCatalog.SetGate(gate)
	rt := &runtime.Service{
		Root:     a.Root,
		Home:     homeDir(),
		Clients:  clientReg,
		Profiles: &profile.Store{DB: a.DB.DB},
		Catalog:  catalogRegistry,
	}
	deps := &activation.Deps{
		Root:           a.Root,
		Accounts:       activation.NewAccountsView(svc),
		Providers:      provReg,
		Clients:        clientReg,
		ClientProfiles: clientProfileStore,
		Bindings:       &bindings.Store{Root: a.Root, DB: a.DB.DB},
		Presets:        activation.NewPresetsView(&presets.Store{DB: a.DB.DB}),
		Profiles:       rt.Profiles,
		Pools:          activation.NewPoolsView(poolsStore),
		Journal:        jrnl,
		Runtime:        rt,
		Proxy:          px,
		Catalog:        depCatalog,
		Hub:            hubStore,
		SpawnHubChild: func(hubName string) (int, error) {
			selfExe, exeErr := selfExecutable()
			if exeErr != nil {
				return 0, exeErr
			}
			hubDir := filepath.Join(a.Root, "runtime", "hub", hubName)
			pidFile := filepath.Join(hubDir, "service.pid")
			// Lifecycle ownership (ADR 0003): adopt an existing child only when a
			// TRUSTED record verifies the live process; legacy numeric or stale
			// records are removed and a fresh, instance-stamped child is spawned.
			if rec := ownership.Load(pidFile); rec != nil && rec.VerifyLive() {
				// Liveness is not freshness: an adopted child may serve a
				// config from before the last mutation (ADR-0030). Restart it
				// when its revision trails the stored one; adopt it otherwise.
				endpoint, revision := "", 0
				if cfg, cerr := hubStore.GetConfigContext(context.Background(), hubName); cerr != nil {
					return 0, cerr
				} else if cfg != nil && cfg.Port != 0 {
					endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
					revision = cfg.Revision
				}
				if restarted, rerr := hub.RestartStaleChild(a.Root, hubName, endpoint, revision, 3*time.Second); rerr != nil {
					return 0, rerr
				} else if !restarted {
					return rec.PID, nil
				}
				// Fall through: spawn fresh below.
			}
			rec, err := ownership.Spawn(selfExe, []string{"_serve-hub", hubName}, pidFile,
				filepath.Join(hubDir, "service.log"), "ANYPICK_HUB_CHILD=1")
			if err != nil {
				return 0, err
			}
			return rec.PID, nil
		},
	}
	px.Resolver = &snapshotUpstreamResolver{store: st, providers: provReg, profiles: rt.Profiles}
	px.Pools = poolsStore
	// Plugin providers that own their proxy lifecycle route through the
	// private Node host; built-ins keep the engine path. Wiring lives here so
	// core proxy never imports providers (invariant #1).
	px.PluginLifecycle = pluginProxyLifecycle{app: a}
	px.DefaultPort = func(providerID string) int {
		pv, err := provReg.Get(providerID)
		if err != nil {
			return 0
		}
		if dp, ok := pv.(provider.DefaultProxyPorter); ok {
			return dp.DefaultProxyPort()
		}
		return 0
	}
	px.QuotaGuardPolicy = func(ctx context.Context) config.QuotaGuardPolicy {
		cfg, err := a.Config.Read(ctx)
		if err != nil {
			return config.QuotaGuardPolicy{ServeWhenAllCooling: true}
		}
		return config.QuotaGuardPolicyOf(cfg)
	}
	px.PoolSpawner = func(providerID string) (int, error) {
		selfExe, exeErr := selfExecutable()
		if exeErr != nil {
			return 0, exeErr
		}
		// The detached `_serve-pool` child runs under the SAME structured
		// ownership record the CLI's `pool stop` verifies
		// (runtime/pools/<provider>/service.pid, ADR 0003). A live trusted record
		// means the previous child survived the pre-stop — fail closed rather
		// than port-collide; a crashed child leaves a stale record that the fresh
		// spawn overwrites. Identity is proven at /health via the instance echo,
		// never the PID.
		poolDir := filepath.Join(a.Root, "runtime", "pools", providerID)
		pidFile := filepath.Join(poolDir, "service.pid")
		if rec := ownership.Load(pidFile); rec != nil && rec.VerifyLive() {
			return 0, anyerr.Newf(
				"existing pool child for "+providerID+" is still running",
				"PROXY_START_FAILED")
		}
		rec, err := ownership.Spawn(selfExe, []string{"_serve-pool", providerID}, pidFile,
			filepath.Join(poolDir, "service.log"), "ANYPICK_POOL_CHILD=1")
		if err != nil {
			return 0, err
		}
		return rec.PID, nil
	}
	if selfExe, exeErr := selfExecutable(); exeErr == nil {
		px.Spawner = proxy.ExecSpawner(selfExe)
	}

	a.Providers = provReg
	a.Clients = clientReg
	a.Accounts = svc
	a.Inspections = desktop.NewInspectionManager(st)
	a.Proxy = px
	a.PoolSvc = pools.NewService(poolsStore)
	a.Hub = hubStore
	a.ProxyHub = hub.NewService(hubStore)
	a.Journal = jrnl
	a.PresetSvc = presets.NewService(&presets.Store{DB: a.DB.DB})
	a.BindingSvc = bindings.NewService(&bindings.Store{Root: a.Root, DB: a.DB.DB})
	a.CodexProfileSvc = client.NewProfileService(clientProfileStore)
	svc.SetAfterSwitch(a.BindingSvc.RealignNativeClientBinding)
	a.Runtime = &runtimeView{
		Root: a.Root, Home: homeDir(), Clients: clientReg,
		ProfileSvc: profile.NewService(rt.Profiles), Catalog: catalogRegistry,
	}
	a.Deps = deps
	a.pluginSup = newPluginSupervisor(a)
	return nil
}

// Ready runs the createAppReady lifecycle:
//
//	.migrate.lock → legacy root migration → schema migration →
//	config/binding bootstrap → startup journal recovery → stale lease reap.
//
// The lock is best-effort like TS: contention fails fast instead of blocking.
func Ready(ctx context.Context, opts Options) (_ *App, closeFn func(), err error) {
	root := paths.Root(opts.Root)
	lockPath := filepath.Join(root, ".migrate.lock")
	// The lock guards ONLY the bootstrap section below, exactly like TS:
	// it must be released before command execution so short-lived helpers
	// (detached proxy children) can boot while the parent keeps running.
	fl, lockErr := lock.AcquireFileLock(lockPath, 0)
	if lockErr != nil {
		return nil, nil, anyerr.Wrapf(lockErr, "acquire .migrate.lock", "OPERATIONAL")
	}
	releaseLock := func() { _ = fl.Release() }
	bootstrapDone := false
	defer func() {
		if !bootstrapDone {
			releaseLock()
		}
	}()

	a, err := New(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()

	if !opts.Bare && !plugins.DisabledByEnv() {
		pluginRuntime, pluginErr := a.Plugins.Inspect(ctx)
		if pluginErr != nil {
			return nil, nil, anyerr.Wrapf(pluginErr, "inspect plugin registry", "DB_READ")
		}
		a.PluginRuntime = a.activatePlugins(ctx, pluginRuntime)
	} else {
		a.PluginRuntime = plugins.Runtime{
			Candidates: []plugins.Candidate{}, Loaded: []plugins.LoadedPlugin{},
			Failures: []plugins.LoadFailure{},
		}
	}
	// Registries are immutable once startup begins resolving adapters. Plugin
	// activation is the final registration window (matching the TS composition
	// root), so long-lived services can never observe a graph that changes under
	// an in-flight operation.
	a.Providers.Seal()
	a.Clients.Seal()

	if !opts.SkipMigrate {
		// Filesystem→SQLite and binding migrations are TS-side history this
		// port never shipped mid-flight; fresh Go roots start at baseline.
		// Config bootstrap lands with the config service milestone.
		_ = ensureBootstrapState(ctx, a)
	}

	if !opts.SkipMaintenance {
		runStartupMaintenance(ctx, a, root)
	}

	releaseLock()
	bootstrapDone = true
	closer := func() {
		a.Close()
	}
	return a, closer, nil
}

// RunStartupMaintenance rolls back operations a previous run left mid-flight
// and reaps detached proxy children nobody owns. Ready calls it unless
// SkipMaintenance is set. The bare no-TTY path opens the database first so it
// can read ui.defaultSurface, then calls this only when that surface is not
// the terminal UI. A configured tui with no terminal must not reap proxies
// the desktop app is using.
func RunStartupMaintenance(ctx context.Context, a *App) {
	if a == nil {
		return
	}
	runStartupMaintenance(ctx, a, a.Root)
}

// runStartupMaintenance rolls back operations a previous run left mid-flight and
// reaps detached proxy children nobody owns. Both are best-effort: a failure
// here must not stop AnyPick from starting, but it must not be silent either —
// "recovery could not roll this back" and "there was nothing to roll back"
// used to look identical to the user.
func runStartupMaintenance(ctx context.Context, a *App, root string) {
	if a.Deps != nil {
		recovered, err := activation.RecoverIncompleteOperations(ctx, a.Deps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[anypick] startup journal recovery failed: %v\n", err)
		}
		if recovered != nil && len(recovered.Failed) > 0 {
			fmt.Fprintf(os.Stderr,
				"[anypick] %d incomplete operation(s) could not be rolled back: %v\n"+
					"[anypick] run `anypick doctor` before retrying them.\n",
				len(recovered.Failed), recovered.Failed)
		}
	}
	// Detached proxy children are reaped only when no live supervisor owns them.
	if RunningTrayPid(root) != nil {
		return
	}
	if _, err := a.Proxy.ReapStaleContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[anypick] stale lease reaping failed: %v\n", err)
	}
}

// Close disposes the app idempotently. The plugin supervisor closes first:
// it wins every recovery race by refusing spawns and draining any in-flight
// restart before the process tree is torn down.
func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.pluginSup != nil {
			a.pluginSup.Close()
		}
		if a.Proxy != nil {
			a.Proxy.Close()
		}
		if a.DB != nil {
			_ = a.DB.Close()
		}
	})
}

// selfExecutable resolves the running binary; ANYPICK_SELF_EXE lets tests
// point detached-child spawns at a real built CLI.
func selfExecutable() (string, error) {
	if p := os.Getenv("ANYPICK_SELF_EXE"); p != "" {
		return p, nil
	}
	return os.Executable()
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

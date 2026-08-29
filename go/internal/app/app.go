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

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/db"
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
	Root        string
	DB          *db.DB
	Store       *store.AccountStore
	Providers   *provider.Registry
	Clients     *client.Registry
	Accounts    *accounts.Service
	Deps        *activation.Deps
	Proxy       *proxy.Service
	Runtime     *runtimeView
	Pools       *pools.Store
	Hub         *hub.Store
	Journal     *journal.OperationJournal
	Leases      *proxy.LeaseStore
	Presets     *presets.Store
	Bindings    *bindings.Store
	Config      *config.ConfigService
	ConfigStore *config.Store
	Plugins     *plugins.Service
	PluginStore *plugins.Store
	// PluginRuntime contains digest-verified candidates and non-fatal package
	// failures. JavaScript activation is owned by the isolated host, never this
	// core process.
	PluginRuntime plugins.Runtime
	// pluginSup owns the supervised host across generations: single-flight
	// restart, activation replay, and close-vs-recovery arbitration.
	pluginSup *pluginSupervisor

	closeOnce closeGuard
}

// runtimeView groups the runtime-facing stores the CLI needs (profiles,
// presets, catalog). Kept unexported-typed to avoid widening the surface.
type runtimeView struct {
	Root     string
	Home     string
	Clients  *client.Registry
	Profiles *profile.Store
	Catalog  *catalog.Index
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
	if err := provider.RegisterBuiltinCompatibles(provReg); err != nil {
		return err
	}
	clientReg := client.NewRegistry()
	clientReg.SetGate(gate)
	if err := client.RegisterBuiltin(clientReg, homeDir()); err != nil {
		return err
	}

	svc := accounts.New(st, provReg)
	leases := &proxy.LeaseStore{DB: a.DB.DB}
	px := proxy.New(st, leases)
	svc.Proxy = px

	poolsStore := &pools.Store{DB: a.DB.DB}
	hubStore := &hub.Store{DB: a.DB.DB}
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
		Root:      a.Root,
		Accounts:  activation.NewAccountsView(svc),
		Providers: provReg,
		Clients:   clientReg,
		Bindings:  &bindings.Store{DB: a.DB.DB},
		Presets:   activation.NewPresetsView(&presets.Store{DB: a.DB.DB}),
		Profiles:  rt.Profiles,
		Pools:     activation.NewPoolsView(poolsStore),
		Journal:   jrnl,
		Runtime:   rt,
		Proxy:     px,
		Catalog:   depCatalog,
		Hub:       hubStore,
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
				return rec.PID, nil
			}
			rec, err := ownership.Spawn(selfExe, []string{"_serve-hub", hubName}, pidFile,
				filepath.Join(hubDir, "service.log"), "ANYPICK_HUB_CHILD=1")
			if err != nil {
				return 0, err
			}
			return rec.PID, nil
		},
	}
	px.Resolver = &snapshotUpstreamResolver{store: st, providers: provReg}
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
	px.QuotaGuardPolicy = func(ctx context.Context) (bool, int64) {
		cfg, err := a.Config.Read(ctx)
		if err != nil {
			return false, 0
		}
		policy := config.QuotaGuardPolicyOf(cfg)
		return policy.Enabled, int64(policy.CooldownMinutes) * 60_000
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
	a.Proxy = px
	a.Pools = poolsStore
	a.Hub = hubStore
	a.Journal = jrnl
	a.Leases = leases
	a.Presets = &presets.Store{DB: a.DB.DB}
	a.Bindings = &bindings.Store{DB: a.DB.DB}
	a.Runtime = &runtimeView{
		Root: a.Root, Home: homeDir(), Clients: clientReg,
		Profiles: rt.Profiles, Catalog: catalogRegistry,
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
		// Best-effort startup recovery: a failure means a partial operation
		// could not be rolled back automatically — surfaced via doctor.
		if a.Deps != nil {
			if _, rerr := activation.RecoverIncompleteOperations(ctx, a.Deps); rerr != nil {
				fmt.Fprintf(os.Stderr, "[anypick] startup journal recovery failed: %v\n", rerr)
			}
		}
	}

	// Reap detached proxy processes only when no live supervisor owns them.
	if !opts.SkipMaintenance && RunningTrayPid(root) == nil {
		if _, rerr := a.Leases.ReapStaleContext(ctx); rerr != nil {
			fmt.Fprintf(os.Stderr, "[anypick] stale lease reaping failed: %v\n", rerr)
		}
	}

	releaseLock()
	bootstrapDone = true
	closer := func() {
		a.Close()
	}
	return a, closer, nil
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

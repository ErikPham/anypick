package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/pluginhost"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/transport"
)

func writeAppPlugin(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anypick.plugin.json"), []byte(
		`{"name":"app-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(`export default {
  activate(context) {
    context.registerCatalogProvider({
      id: 'app-catalog',
      name: 'App Catalog',
      description: 'Loaded through the private host',
      apiStyle: 'openai',
    });
  },
};
`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func configureAppPluginHost(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for plugin-host integration")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join(cwd, "..", "..", "..", "scripts", "plugin-host.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(pluginhost.EnvNodePath, node)
	t.Setenv(pluginhost.EnvScriptPath, script)
}

func TestReadyExposesPluginCandidatesAndNonFatalFailures(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(t.TempDir(), "plugin")
	writeAppPlugin(t, dir)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), dir)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	appCatalog, catalogLoaded := ready.Runtime.Catalog.Lookup("app-catalog")
	if len(ready.PluginRuntime.Candidates) != 0 || len(ready.PluginRuntime.Loaded) != 1 ||
		len(ready.PluginRuntime.Failures) != 0 || !catalogLoaded ||
		len(appCatalog.Protocols) != 1 || string(appCatalog.Protocols[0]) != "openai" {
		t.Fatalf("plugin runtime = %+v", ready.PluginRuntime)
	}
	closeReady()

	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	degraded, closeDegraded, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatalf("plugin failure must not abort app startup: %v", err)
	}
	if len(degraded.PluginRuntime.Candidates) != 0 || len(degraded.PluginRuntime.Failures) != 1 ||
		!degraded.PluginRuntime.Failures[0].Untrusted {
		t.Fatalf("degraded runtime = %+v", degraded.PluginRuntime)
	}
	closeDegraded()

	bare, closeBare, err := Ready(context.Background(), Options{
		Root: root, SkipMaintenance: true, Bare: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.PluginRuntime.Candidates) != 0 || len(bare.PluginRuntime.Failures) != 0 {
		t.Fatalf("bare app inspected plugins: %+v", bare.PluginRuntime)
	}
	closeBare()

	t.Setenv("ANYPICK_NO_PLUGINS", "true")
	disabled, closeDisabled, err := Ready(context.Background(), Options{
		Root: root, SkipMaintenance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled.PluginRuntime.Candidates) != 0 || len(disabled.PluginRuntime.Failures) != 0 {
		t.Fatalf("disabled-by-env app inspected plugins: %+v", disabled.PluginRuntime)
	}
	closeDisabled()
}

func TestReadyActivatesEveryPluginRegistrationKind(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join(cwd, "..", "pluginhost", "testdata", "full-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.log")
	t.Setenv("ANYPICK_PLUGIN_TEST_EVENTS", events)
	isolationDir := filepath.Join(t.TempDir(), "fixture-isolated")
	t.Setenv("ANYPICK_PLUGIN_TEST_RUNTIME", isolationDir)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ready.PluginRuntime.Loaded) != 1 || len(ready.PluginRuntime.Failures) != 0 {
		closeReady()
		t.Fatalf("plugin runtime = %+v", ready.PluginRuntime)
	}

	remoteProvider, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		closeReady()
		t.Fatal(err)
	}
	live, err := remoteProvider.DetectLive()
	if err != nil || !live.Present || live.Identity != "fixture@example.test" {
		closeReady()
		t.Fatalf("plugin provider live status = %+v, %v", live, err)
	}
	adapter := remoteProvider.AccountAdapter("work", false)
	if got := adapter.TransportFor("fixture-client"); got != transport.Direct {
		closeReady()
		t.Fatalf("plugin source transport = %q, want direct", got)
	}
	methodProvider, methodsOK := remoteProvider.(provider.LoginMethodsProvider)
	loginer, loginOK := remoteProvider.(provider.Loginer)
	usageProvider, usageOK := remoteProvider.(provider.LiveUsageProvider)
	if !methodsOK || !loginOK || !usageOK {
		closeReady()
		t.Fatalf("plugin optional provider capabilities = methods:%t login:%t usage:%t", methodsOK, loginOK, usageOK)
	}
	methods := methodProvider.LoginMethods()
	if len(methods) != 2 || methods[0].ID != "fixture-browser" || !methods[0].Available {
		closeReady()
		t.Fatalf("plugin login methods = %+v", methods)
	}
	if err := loginer.Login(context.Background(), "fixture-browser", ""); err != nil {
		closeReady()
		t.Fatalf("plugin login: %v", err)
	}
	usage, err := usageProvider.LiveUsage(context.Background())
	if err != nil || usage == nil || len(usage.Windows) != 1 ||
		usage.Windows[0].RemainingPercent != 73 {
		closeReady()
		t.Fatalf("plugin live usage = %+v, %v", usage, err)
	}
	cancelCtx, cancelLogin := context.WithCancel(context.Background())
	loginDone := make(chan error, 1)
	go func() { loginDone <- loginer.Login(cancelCtx, "fixture-wait", "") }()
	waitForPluginEvent(t, events, "login:fixture-wait")
	cancelLogin()
	if err := <-loginDone; !errors.Is(err, context.Canceled) {
		closeReady()
		t.Fatalf("cancelled plugin login = %v, want context.Canceled", err)
	}
	waitForPluginEvent(t, events, "login:aborted")

	remoteClient, err := ready.Clients.Get("fixture-client")
	if err != nil {
		closeReady()
		t.Fatal(err)
	}
	applied, err := remoteClient.Apply(&client.ApplyContext{AnyPickRoot: root})
	if err != nil || len(applied.ManagedPaths) != 1 ||
		applied.ManagedPaths[0] != filepath.Join(root, "fixture-client.json") {
		closeReady()
		t.Fatalf("plugin client apply = %+v, %v", applied, err)
	}
	if _, ok := remoteClient.(client.PersistentAdapter); !ok {
		closeReady()
		t.Fatalf("plugin client %T does not expose applyPersistent", remoteClient)
	}
	if _, ok := remoteClient.(client.EphemeralAdapter); !ok {
		closeReady()
		t.Fatalf("plugin client %T does not expose isolated runtime", remoteClient)
	}
	persistent, err := ready.Deps.Runtime.ApplyProxyEndpoint(
		context.Background(),
		"fixture-client",
		runtime.ProxyEndpointOptions{
			Endpoint: "http://127.0.0.1:43199", APIKey: "private-proxy-token",
			SourceKind: "account", SourceProvider: "fixture-provider", SourceName: "work",
			TransportCapability: transport.ManagedBuiltinProxy,
		},
	)
	if err != nil || len(persistent.ManagedPaths) != 1 ||
		persistent.ManagedPaths[0] != filepath.Join(root, "fixture-client-persistent.json") {
		closeReady()
		t.Fatalf("plugin client persistent apply = %+v, %v", persistent, err)
	}
	waitForPluginEvent(t, events, "persistent:persistent:account")
	isolation, err := ready.Deps.Runtime.CreateEphemeralRuntimeContext(
		context.Background(),
		runtime.EphemeralSpec{
			ClientID: "fixture-client", Endpoint: "http://127.0.0.1:43199",
			APIKey: "private-proxy-token", SourceKind: "account",
			SourceProvider: "fixture-provider", SourceName: "work",
			TransportCapability: transport.ManagedBuiltinProxy,
		},
	)
	if err != nil || isolation.Directory != isolationDir ||
		isolation.Environment["FIXTURE_ENDPOINT"] != "http://127.0.0.1:43199" ||
		len(isolation.Args) != 1 || isolation.Args[0] != "--fixture-isolated" {
		closeReady()
		t.Fatalf("plugin client isolated runtime = %+v, %v", isolation, err)
	}
	if _, err := os.Stat(isolationDir); err != nil {
		closeReady()
		t.Fatalf("plugin isolated directory: %v", err)
	}
	if err := isolation.Cleanup(); err != nil {
		closeReady()
		t.Fatalf("plugin isolated cleanup: %v", err)
	}
	if err := isolation.Cleanup(); err != nil {
		closeReady()
		t.Fatalf("plugin isolated cleanup must be idempotent: %v", err)
	}
	if _, err := os.Stat(isolationDir); !os.IsNotExist(err) {
		closeReady()
		t.Fatalf("plugin isolated directory survived cleanup: %v", err)
	}
	waitForPluginEvent(t, events, "isolation:cleanup")
	wrappedClient, ok := remoteClient.(*rpcPersistentEphemeralClient)
	if !ok {
		closeReady()
		t.Fatalf("plugin client wrapper = %T", remoteClient)
	}
	wrappedClient.capabilities.SupportsIsolatedHome = false
	wrappedClient.capabilities.SupportsEnvironmentOverlay = true
	overlay, err := ready.Deps.Runtime.CreateEphemeralRuntimeContext(
		context.Background(),
		runtime.EphemeralSpec{
			ClientID: "fixture-client", Endpoint: "http://127.0.0.1:43200",
			SourceKind: "gateway", SourceName: "fixture-gateway",
			TransportCapability: transport.Direct,
		},
	)
	if err != nil || overlay.Environment["FIXTURE_OVERLAY"] != "http://127.0.0.1:43200" {
		closeReady()
		t.Fatalf("plugin client environment overlay = %+v, %v", overlay, err)
	}
	if err := overlay.Cleanup(); err != nil {
		closeReady()
		t.Fatalf("plugin overlay cleanup: %v", err)
	}
	waitForPluginEvent(t, events, "overlay:cleanup")
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); !ok {
		closeReady()
		t.Fatal("plugin catalog provider was not registered")
	}
	compatible, err := ready.Providers.Get("fixture-compatible")
	if err != nil || compatible.Description() != "Inert compatible-provider definition" {
		closeReady()
		t.Fatalf("compatible provider = %+v, %v", compatible, err)
	}
	if _, ok := compatible.(*provider.Compatible); !ok {
		closeReady()
		t.Fatalf("compatible provider has type %T", compatible)
	}
	if err := ready.Clients.Register(remoteClient); err == nil {
		closeReady()
		t.Fatal("client registry accepted a late registration")
	}

	closeReady()
	eventBytes, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if eventsText := string(eventBytes); !strings.Contains(eventsText, "dispose:full-plugin") {
		t.Fatalf("plugin disposal events = %q", eventsText)
	}
}

func waitForPluginEvent(t *testing.T, path, event string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), event) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("plugin event %q did not arrive", event)
}

func TestPluginRegistryCommitIsAtomicOnConflict(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	pluginDir := filepath.Join(t.TempDir(), "conflicting-plugin")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "anypick.plugin.json"), []byte(
		`{"name":"conflicting-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "index.mjs"), []byte(`
export default {
  activate(context) {
    context.registerProvider({
      id: 'must-not-leak',
      name: 'Must Not Leak',
      description: 'Rollback sentinel',
      async detectLive() { return { present: false }; },
      async backup() { return {}; },
      async restore() {},
    });
    context.registerCatalogProvider({
      id: 'openai',
      name: 'Forbidden shadow',
      description: 'Conflicts with a built-in catalog id',
      apiStyle: 'openai',
    });
  },
};
`), 0o600); err != nil {
		t.Fatal(err)
	}

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), pluginDir)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeReady()
	if len(ready.PluginRuntime.Loaded) != 0 || len(ready.PluginRuntime.Failures) != 1 {
		t.Fatalf("plugin runtime = %+v", ready.PluginRuntime)
	}
	if ready.Providers.Has("must-not-leak") {
		t.Fatal("provider registration leaked from a rejected plugin commit")
	}
	if !strings.Contains(ready.PluginRuntime.Failures[0].Reason, "catalog provider already registered") {
		t.Fatalf("unexpected failure: %+v", ready.PluginRuntime.Failures[0])
	}
}

func TestPluginDiagnosticsReportsPostActivationCrash(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	pluginDir := filepath.Join(t.TempDir(), "crash-plugin")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "anypick.plugin.json"), []byte(
		`{"name":"crash-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "index.mjs"), []byte(`
export default {
  activate(context) {
    context.registerProvider({
      id: 'crash-provider',
      name: 'Crash Provider',
      description: 'Crash health fixture',
      detectLive() { process.exit(42); },
      async backup() { return {}; },
      async restore() {},
    });
  },
};
`), 0o600); err != nil {
		t.Fatal(err)
	}

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{
		Root: root, SkipMaintenance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), pluginDir)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	ready, closeReady, err := Ready(context.Background(), Options{
		Root: root, SkipMaintenance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeReady()
	before := ready.PluginDiagnostics(context.Background())
	if before.Host.State != "running" || before.Host.ActivationCount != 1 {
		t.Fatalf("plugin diagnostics before crash = %+v", before)
	}
	crasher, err := ready.Providers.Get("crash-provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crasher.DetectLive(); !errors.Is(err, pluginhost.ErrHostCrashed) {
		t.Fatalf("crash invocation = %v, want ErrHostCrashed", err)
	}
	after := ready.PluginDiagnostics(context.Background())
	if after.Host.State != "degraded" || !strings.Contains(after.Host.Error, "host crashed") {
		t.Fatalf("plugin diagnostics after crash = %+v", after)
	}
}

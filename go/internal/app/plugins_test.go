package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(`export default { activate() {} };`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// addEnabledPlugin registers and enables one package through the real
// composition root, then closes the bootstrap app. The registry is the part of
// the plugin subsystem that survived the host removal, so it gets exercised
// end to end even though nothing can execute the package.
func addEnabledPlugin(t *testing.T, root, dir string) {
	t.Helper()
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
}

// TestReadyDegradesEveryPluginCandidateWithoutAHost pins the observable
// contract after the executable host was removed (ADR-0020 §5): the registry
// still knows the package, nothing loads, and the reason names the removal so
// doctor and the desktop protocol never report a healthy empty plugin set.
func TestReadyDegradesEveryPluginCandidateWithoutAHost(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(t.TempDir(), "plugin")
	writeAppPlugin(t, dir)
	addEnabledPlugin(t, root, dir)

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatalf("a degraded plugin must not abort app startup: %v", err)
	}
	defer closeReady()

	if len(ready.PluginRuntime.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the one registered package", ready.PluginRuntime.Candidates)
	}
	if len(ready.PluginRuntime.Loaded) != 0 {
		t.Fatalf("loaded = %+v, want none: no host can commit registrations", ready.PluginRuntime.Loaded)
	}
	if len(ready.PluginRuntime.Failures) != 1 {
		t.Fatalf("failures = %+v, want exactly the removal notice", ready.PluginRuntime.Failures)
	}
	failure := ready.PluginRuntime.Failures[0]
	if failure.Name != "app-plugin" {
		t.Fatalf("failure name = %q, want app-plugin", failure.Name)
	}
	if !strings.Contains(failure.Reason, "no plugin host") {
		t.Fatalf("failure reason = %q, want it to name the missing host", failure.Reason)
	}
	// A package the registry trusts is not untrusted; it simply cannot run.
	if failure.Untrusted {
		t.Fatalf("removal failure must not be reported as untrusted: %+v", failure)
	}
}

// TestPluginDiagnosticsReportsInertHost proves every diagnostic surface agrees
// with the removal rather than claiming degraded health for a host that never
// started. "inactive" is honest; "degraded" would send users chasing a crash.
func TestPluginDiagnosticsReportsInertHost(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(t.TempDir(), "plugin")
	writeAppPlugin(t, dir)
	addEnabledPlugin(t, root, dir)

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeReady()

	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "inactive" {
		t.Fatalf("host state = %q, want inactive", diagnostics.Host.State)
	}
	if diagnostics.Host.LoadedPlugins != 0 {
		t.Fatalf("loadedPlugins = %d, want 0", diagnostics.Host.LoadedPlugins)
	}
	if diagnostics.Host.Error != "" {
		t.Fatalf("host error = %q, want empty: nothing crashed", diagnostics.Host.Error)
	}
	if len(diagnostics.Failures) != 1 {
		t.Fatalf("diagnostic failures = %+v, want the removal notice", diagnostics.Failures)
	}
}

// TestReadyStillRefusesTamperedPluginPackage keeps the trust boundary that
// mattered most while a host existed: a package whose bytes changed after the
// user trusted it is refused, and that refusal outranks the removal notice.
func TestReadyStillRefusesTamperedPluginPackage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(t.TempDir(), "plugin")
	writeAppPlugin(t, dir)
	addEnabledPlugin(t, root, dir)

	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	degraded, closeDegraded, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatalf("an untrusted plugin must not abort app startup: %v", err)
	}
	defer closeDegraded()

	// Tampering is caught during Inspect, so the package never becomes a
	// candidate and the only failure is the untrusted one.
	if len(degraded.PluginRuntime.Candidates) != 0 {
		t.Fatalf("tampered package became a candidate: %+v", degraded.PluginRuntime.Candidates)
	}
	if len(degraded.PluginRuntime.Failures) != 1 || !degraded.PluginRuntime.Failures[0].Untrusted {
		t.Fatalf("degraded runtime = %+v, want one untrusted failure", degraded.PluginRuntime.Failures)
	}
	if !strings.Contains(degraded.PluginRuntime.Failures[0].Reason, "changed") {
		t.Fatalf("failure reason = %q, want it to name the change", degraded.PluginRuntime.Failures[0].Reason)
	}
}

// TestReadySkipsPluginInspectionWhenBareOrDisabled pins the two escape hatches
// that must not regress: a bare app and ANYPICK_NO_PLUGINS never touch the
// registry at all.
func TestReadySkipsPluginInspectionWhenBareOrDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(t.TempDir(), "plugin")
	writeAppPlugin(t, dir)
	addEnabledPlugin(t, root, dir)

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
	disabled, closeDisabled, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDisabled()
	if len(disabled.PluginRuntime.Candidates) != 0 || len(disabled.PluginRuntime.Failures) != 0 {
		t.Fatalf("disabled-by-env app inspected plugins: %+v", disabled.PluginRuntime)
	}
}

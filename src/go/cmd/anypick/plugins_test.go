package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func writeCLIPlugin(t *testing.T, base string) string {
	t.Helper()
	dir := filepath.Join(base, "plugin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anypick.plugin.json"), []byte(
		`{"name":"cli-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"),
		[]byte(`export default { activate() {} };`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runPluginCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	clearGlobalsForParity()
	code := -1
	out, errOut := captureBoth(t, func() { code = run(args) })
	clearGlobalsForParity()
	return code, out, errOut
}

// The registry lifecycle (add → enable → list → trust → disable → remove) is
// still fully supported. What changed with ADR-0020 §5 is that nothing executes
// the package: list reports loaded=false with the removal reason, and doctor
// reports an inactive host rather than a running one.
func TestPluginCLIJSONLifecycleAndUntrustedFailure(t *testing.T) {
	base := isolateEnv(t)
	dir := writeCLIPlugin(t, base)

	code, out, errOut := runPluginCaptured(t, "--json", "plugin", "add", dir)
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("add exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var added map[string]any
	if err := json.Unmarshal([]byte(out), &added); err != nil || added["enabled"] != false ||
		len(added["digest"].(string)) != 64 {
		t.Fatalf("add JSON = %#v err=%v", added, err)
	}

	code, _, errOut = runPluginCaptured(t, "--json", "plugin", "enable", "cli-plugin")
	if code != int(anyerr.InvalidUsage) || !strings.Contains(errOut, "requires --yes") {
		t.Fatalf("non-TTY enable exited %d stderr=%q", code, errOut)
	}

	code, out, errOut = runPluginCaptured(t, "--json", "--yes", "plugin", "enable", "cli-plugin")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("enable exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var enabled map[string]any
	if err := json.Unmarshal([]byte(out), &enabled); err != nil || enabled["enabled"] != true {
		t.Fatalf("enable JSON = %#v err=%v", enabled, err)
	}

	code, out, errOut = runPluginCaptured(t, "--json", "plugin", "list")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("list exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var listed []map[string]any
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("list JSON = %#v err=%v", listed, err)
	}
	// No host can load it, and the reason must say so rather than showing the
	// package as healthy.
	if listed[0]["loaded"] != false {
		t.Fatalf("loaded = %#v, want false: this build has no plugin host", listed[0]["loaded"])
	}
	removalFailure, ok := listed[0]["failure"].(map[string]any)
	if !ok || !strings.Contains(removalFailure["reason"].(string), "no plugin host") {
		t.Fatalf("removal failure = %#v, want a reason naming the missing host", listed[0]["failure"])
	}
	if removalFailure["untrusted"] != false {
		t.Fatalf("removal failure must not be reported as untrusted: %#v", removalFailure)
	}
	code, out, errOut = runPluginCaptured(t, "--json", "doctor")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("doctor exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var doctor map[string]any
	if err := json.Unmarshal([]byte(out), &doctor); err != nil {
		t.Fatal(err)
	}
	pluginDiagnostics, ok := doctor["plugins"].(map[string]any)
	if !ok {
		t.Fatalf("doctor plugins diagnostics = %#v", doctor["plugins"])
	}
	host, ok := pluginDiagnostics["host"].(map[string]any)
	if !ok || host["state"] != "inactive" || host["loadedPlugins"] != float64(0) {
		t.Fatalf("doctor plugin host = %#v, want inactive/0", pluginDiagnostics["host"])
	}
	if host["error"] != nil && host["error"] != "" {
		t.Fatalf("doctor host error = %#v, want empty: nothing crashed", host["error"])
	}
	if len(pluginDiagnostics["failures"].([]any)) != 1 {
		t.Fatalf("doctor plugin failures = %#v, want the removal notice",
			pluginDiagnostics["failures"])
	}

	if err := os.WriteFile(filepath.Join(dir, "helper.mjs"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runPluginCaptured(t, "--json", "plugin", "list")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("tampered list exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	listed = nil
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	failure, ok := listed[0]["failure"].(map[string]any)
	if !ok || failure["untrusted"] != true || !strings.Contains(failure["reason"].(string), "changed") {
		t.Fatalf("untrusted failure JSON = %#v", listed[0]["failure"])
	}
	code, out, errOut = runPluginCaptured(t, "--json", "doctor")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("degraded doctor exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	doctor = nil
	if err := json.Unmarshal([]byte(out), &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor["status"] != "degraded" {
		t.Fatalf("doctor status = %#v, want degraded", doctor["status"])
	}
	pluginDiagnostics, ok = doctor["plugins"].(map[string]any)
	if !ok || len(pluginDiagnostics["failures"].([]any)) != 1 {
		t.Fatalf("degraded plugin diagnostics = %#v", doctor["plugins"])
	}

	code, _, errOut = runPluginCaptured(t, "--json", "plugin", "trust", "cli-plugin")
	if code != int(anyerr.InvalidUsage) || !strings.Contains(errOut, "requires --yes") {
		t.Fatalf("non-TTY trust exited %d stderr=%q", code, errOut)
	}
	code, out, errOut = runPluginCaptured(t, "--json", "--yes", "plugin", "trust", "cli-plugin")
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("trust exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var trusted map[string]any
	if err := json.Unmarshal([]byte(out), &trusted); err != nil || trusted["previousDigest"] == trusted["digest"] {
		t.Fatalf("trust JSON = %#v err=%v", trusted, err)
	}

	code, out, _ = runPluginCaptured(t, "--json", "plugin", "disable", "cli-plugin")
	var disabled map[string]any
	if err := json.Unmarshal([]byte(out), &disabled); code != int(anyerr.Success) || err != nil ||
		disabled["enabled"] != false {
		t.Fatalf("disable exited %d JSON=%#v err=%v", code, disabled, err)
	}
	code, out, _ = runPluginCaptured(t, "--json", "plugin", "remove", "cli-plugin")
	var removed map[string]any
	if err := json.Unmarshal([]byte(out), &removed); code != int(anyerr.Success) || err != nil ||
		removed["removed"] != true {
		t.Fatalf("remove exited %d JSON=%#v err=%v", code, removed, err)
	}
}

func TestPluginCLIEnableUnknownMatchesConfirmationOrder(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runPluginCaptured(t, "plugin", "enable", "missing-plugin")
	if code != int(anyerr.InvalidUsage) || !strings.Contains(errOut, "requires --yes") {
		t.Fatalf("without --yes exited %d stderr=%q", code, errOut)
	}
	code, _, errOut = runPluginCaptured(t, "--yes", "plugin", "enable", "missing-plugin")
	if code != int(anyerr.NotFound) || !strings.Contains(errOut, "No plugin named") {
		t.Fatalf("with --yes exited %d stderr=%q", code, errOut)
	}

	code, _, errOut = runPluginCaptured(t, "--json", "plugin", "trust", "missing-plugin")
	if code != int(anyerr.InvalidUsage) || !strings.Contains(errOut, "requires --yes") {
		t.Fatalf("trust without --yes exited %d stderr=%q", code, errOut)
	}
	code, _, errOut = runPluginCaptured(t, "--json", "--yes", "plugin", "trust", "missing-plugin")
	if code != int(anyerr.NotFound) || !strings.Contains(errOut, "No plugin named") {
		t.Fatalf("trust with --yes exited %d stderr=%q", code, errOut)
	}
}

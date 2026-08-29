package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/pluginhost"
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

func TestPluginCLIJSONLifecycleAndUntrustedFailure(t *testing.T) {
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
	hostScript, err := filepath.Abs(filepath.Join(cwd, "..", "..", "..", "scripts", "plugin-host.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(pluginhost.EnvNodePath, node)
	t.Setenv(pluginhost.EnvScriptPath, hostScript)
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
	if listed[0]["loaded"] != true || listed[0]["failure"] != nil {
		t.Fatalf("trusted plugin should be activated by the private host: %#v", listed[0])
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
	if !ok || host["state"] != "running" || host["loadedPlugins"] != float64(1) ||
		host["activationCount"] != float64(1) {
		t.Fatalf("doctor plugin host = %#v", pluginDiagnostics["host"])
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

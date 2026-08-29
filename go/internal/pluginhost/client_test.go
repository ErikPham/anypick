package pluginhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/pluginhost"
	"github.com/outof0/anypick/go/internal/plugins"
)

type hostFixture struct {
	host   *pluginhost.Host
	root   string
	events string

	logMu sync.Mutex
	logs  []string
}

func TestHostActivatesAllRegistrationKindsAndInvokesMethods(t *testing.T) {
	fixture := startFixtureHost(t)
	activation := activateFixture(t, fixture.host, fixture.root, "fixture-plugin")

	if activation.Manifest.Name != "fixture-plugin" || activation.Manifest.Version != "1.2.3" {
		t.Fatalf("unexpected manifest: %+v", activation.Manifest)
	}
	if len(activation.Descriptors) != 3 {
		t.Fatalf("descriptor count = %d, want 3", len(activation.Descriptors))
	}
	if len(activation.CompatibleProviders) != 1 {
		t.Fatalf("compatible provider count = %d, want 1", len(activation.CompatibleProviders))
	}
	compatible := activation.CompatibleProviders[0]
	if compatible.ID != "fixture-compatible" || compatible.DefaultProxyPort != 43123 ||
		len(compatible.Credentials) != 1 {
		t.Fatalf("unexpected compatible definition: %+v", compatible)
	}

	descriptors := descriptorsByKind(t, activation)
	providerDescriptor := descriptors[pluginhost.RegistrationProvider]
	providerID, err := providerDescriptor.ID()
	if err != nil || providerID != "fixture-provider" {
		t.Fatalf("provider id = %q, %v", providerID, err)
	}
	for _, method := range []string{
		"crash", "detectLive", "echo", "liveUsage", "login", "loginMethods",
		"sourceAdapter", "voidResult", "wait",
	} {
		if !slices.Contains(providerDescriptor.Methods, method) {
			t.Errorf("provider methods %v do not include %q", providerDescriptor.Methods, method)
		}
	}
	if slices.Contains(providerDescriptor.Methods, "constructor") {
		t.Errorf("constructor leaked into descriptor methods: %v", providerDescriptor.Methods)
	}

	var echo struct {
		Value       string `json:"value"`
		HelperValue string `json:"helperValue"`
		Receiver    string `json:"receiver"`
	}
	if err := fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "echo", []any{"hello"}, &echo,
	); err != nil {
		t.Fatalf("invoke echo: %v", err)
	}
	if echo.Value != "hello" || echo.HelperValue != "from-helper" || echo.Receiver != providerID {
		t.Fatalf("unexpected echo result: %+v", echo)
	}

	var live struct {
		Present  bool   `json:"present"`
		Identity string `json:"identity"`
	}
	if err := fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "detectLive", []any{}, &live,
	); err != nil {
		t.Fatalf("invoke detectLive: %v", err)
	}
	if !live.Present || live.Identity != "fixture@example.test" {
		t.Fatalf("unexpected live status: %+v", live)
	}

	var voidResult any = "not-null"
	if err := fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "voidResult", []any{}, &voidResult,
	); err != nil {
		t.Fatalf("invoke void method: %v", err)
	}
	if voidResult != nil {
		t.Fatalf("void result = %#v, want JSON null", voidResult)
	}

	adapter, err := fixture.host.InvokeObject(
		context.Background(),
		providerDescriptor.Handle,
		"sourceAdapter",
		[]any{map[string]any{"meta": map[string]any{"name": "work"}}},
	)
	if err != nil {
		t.Fatalf("retain source adapter: %v", err)
	}
	if !slices.Contains(adapter.Methods, "transportFor") {
		t.Fatalf("source adapter methods = %v", adapter.Methods)
	}
	var sourceRef struct {
		Kind     string `json:"kind"`
		Provider string `json:"provider"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(adapter.Properties["sourceRef"], &sourceRef); err != nil {
		t.Fatalf("decode sourceRef: %v", err)
	}
	if sourceRef.Kind != "account" || sourceRef.Provider != providerID || sourceRef.Name != "work" {
		t.Fatalf("unexpected sourceRef: %+v", sourceRef)
	}
	var capability string
	if err := fixture.host.Invoke(
		context.Background(), adapter.Handle, "transportFor", []any{"fixture-client"}, &capability,
	); err != nil {
		t.Fatalf("invoke nested transportFor: %v", err)
	}
	if capability != "direct" {
		t.Fatalf("transportFor = %q, want direct", capability)
	}

	err = fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "constructor", []any{}, nil,
	)
	var remote *pluginhost.RemoteError
	if !errors.As(err, &remote) || remote.Code != "UNKNOWN_METHOD" {
		t.Fatalf("constructor invocation error = %v, want UNKNOWN_METHOD", err)
	}

	status, err := fixture.host.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ActivationCount != 1 || status.ObjectCount != 4 {
		t.Fatalf("unexpected active status: %+v", status)
	}
	if err := activation.Dispose(context.Background()); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	status, err = fixture.host.Status(context.Background())
	if err != nil {
		t.Fatalf("status after dispose: %v", err)
	}
	if status.ActivationCount != 0 || status.ObjectCount != 0 {
		t.Fatalf("unexpected disposed status: %+v", status)
	}

	eventually(t, time.Second, func() bool {
		fixture.logMu.Lock()
		defer fixture.logMu.Unlock()
		for _, line := range fixture.logs {
			if strings.Contains(line, "provider stdout stays off the protocol stream") {
				return true
			}
		}
		return false
	})
}

func TestHostRefusesChangedPackageBeforeImport(t *testing.T) {
	root := copyFixture(t, "changed-plugin")
	digest, err := plugins.DigestPluginPackage(root)
	if err != nil {
		t.Fatalf("digest fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "helper.mjs"), []byte("export const helperValue = 'changed';\n"), 0o600); err != nil {
		t.Fatalf("change helper: %v", err)
	}

	fixture := startHost(t, root)
	_, err = fixture.host.Activate(context.Background(), pluginhost.PluginSpec{
		Root: root, TrustedDigest: digest, ExpectedName: "fixture-plugin",
	})
	var remote *pluginhost.RemoteError
	if !errors.As(err, &remote) || remote.Code != "PLUGIN_UNTRUSTED" {
		t.Fatalf("activation error = %v, want PLUGIN_UNTRUSTED", err)
	}
	var details struct {
		Imported bool `json:"imported"`
	}
	if err := json.Unmarshal(remote.Details, &details); err != nil || details.Imported {
		t.Fatalf("unexpected refusal details %s: %v", remote.Details, err)
	}
	if _, err := os.Stat(fixture.events); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin import marker exists before trusted import: %v", err)
	}
	status, err := fixture.host.Status(context.Background())
	if err != nil || status.ActivationCount != 0 || status.ObjectCount != 0 {
		t.Fatalf("status after trust refusal = %+v, %v", status, err)
	}
}

func TestHostRollsBackPartialActivation(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "partial-plugin", `
import { appendFileSync } from 'node:fs';
export default {
  activate(context) {
    context.registerProvider({ id: 'partial-provider', name: 'Partial', ping() { return 'pong'; } });
    throw new Error('activation exploded');
  },
  dispose() {
    appendFileSync(process.env.ANYPICK_PLUGIN_TEST_EVENTS, 'partial-disposed\n', 'utf8');
  },
};
`)
	fixture := startHost(t, root)
	digest, err := plugins.DigestPluginPackage(root)
	if err != nil {
		t.Fatalf("digest partial plugin: %v", err)
	}
	_, err = fixture.host.Activate(context.Background(), pluginhost.PluginSpec{
		Root: root, TrustedDigest: digest, ExpectedName: "partial-plugin",
	})
	var remote *pluginhost.RemoteError
	if !errors.As(err, &remote) || remote.Code != "ACTIVATION_FAILED" {
		t.Fatalf("activation error = %v, want ACTIVATION_FAILED", err)
	}
	status, err := fixture.host.Status(context.Background())
	if err != nil || status.ActivationCount != 0 || status.ObjectCount != 0 {
		t.Fatalf("partial activation leaked state: %+v, %v", status, err)
	}
	data, err := os.ReadFile(fixture.events)
	if err != nil || string(data) != "partial-disposed\n" {
		t.Fatalf("rollback disposal marker = %q, %v", data, err)
	}
}

func TestHostCancellationAndTimeoutKeepTransportUsable(t *testing.T) {
	fixture := startFixtureHost(t)
	activation := activateFixture(t, fixture.host, fixture.root, "fixture-plugin")
	providerDescriptor := descriptorsByKind(t, activation)[pluginhost.RegistrationProvider]

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer timeoutCancel()
	err := fixture.host.Invoke(timeoutCtx, providerDescriptor.Handle, "wait", []any{500}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want deadline exceeded", err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- fixture.host.Invoke(cancelCtx, providerDescriptor.Handle, "wait", []any{500}, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want context canceled", err)
	}

	signalCtx, signalCancel := context.WithCancel(context.Background())
	signalDone := make(chan error, 1)
	go func() {
		signalDone <- fixture.host.InvokeWithSignal(
			signalCtx,
			providerDescriptor.Handle,
			"login",
			[]any{map[string]any{"methodId": "fixture-wait"}},
			0,
			nil,
		)
	}()
	eventually(t, time.Second, func() bool {
		data, _ := os.ReadFile(fixture.events)
		return strings.Contains(string(data), "login:fixture-wait")
	})
	signalCancel()
	if err := <-signalDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("signal-aware cancel error = %v, want context canceled", err)
	}
	eventually(t, time.Second, func() bool {
		data, _ := os.ReadFile(fixture.events)
		return strings.Contains(string(data), "login:aborted")
	})

	status, err := fixture.host.Status(context.Background())
	if err != nil || status.ActivationCount != 1 || status.ObjectCount != 3 {
		t.Fatalf("host unusable after cancel/timeout: %+v, %v", status, err)
	}
}

func TestHostReportsPluginProcessCrash(t *testing.T) {
	fixture := startFixtureHost(t)
	activation := activateFixture(t, fixture.host, fixture.root, "fixture-plugin")
	providerDescriptor := descriptorsByKind(t, activation)[pluginhost.RegistrationProvider]

	err := fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "crash", []any{}, nil,
	)
	if !errors.Is(err, pluginhost.ErrHostCrashed) {
		t.Fatalf("crash error = %v, want ErrHostCrashed", err)
	}
	if err := fixture.host.Close(); !errors.Is(err, pluginhost.ErrHostCrashed) {
		t.Fatalf("close after crash = %v, want ErrHostCrashed", err)
	}
}

func TestHostDisposesActivationsInReverseOrder(t *testing.T) {
	first := copyFixture(t, "first-plugin")
	second := copyFixture(t, "second-plugin")
	rewriteManifestName(t, first, "first-plugin")
	rewriteManifestName(t, second, "second-plugin")
	fixture := startHost(t, first)

	activateFixture(t, fixture.host, first, "first-plugin")
	activateFixture(t, fixture.host, second, "second-plugin")
	if err := fixture.host.Close(); err != nil {
		t.Fatalf("close host: %v", err)
	}
	data, err := os.ReadFile(fixture.events)
	if err != nil {
		t.Fatalf("read disposal events: %v", err)
	}
	lines := strings.Fields(string(data))
	if len(lines) < 4 {
		t.Fatalf("events = %v, want imports and disposals", lines)
	}
	last := lines[len(lines)-2:]
	if !slices.Equal(last, []string{"dispose:second-plugin", "dispose:first-plugin"}) {
		t.Fatalf("disposal order = %v, want second then first", last)
	}
}

func TestOutboundFrameBoundDoesNotPoisonHost(t *testing.T) {
	fixture := startHostWithConfig(t, fixtureRoot(t), pluginhost.Config{MaxFrameBytes: 4096})
	activation := activateFixture(t, fixture.host, fixture.root, "fixture-plugin")
	providerDescriptor := descriptorsByKind(t, activation)[pluginhost.RegistrationProvider]

	err := fixture.host.Invoke(
		context.Background(), providerDescriptor.Handle, "echo", []any{strings.Repeat("x", 4096)}, nil,
	)
	if !errors.Is(err, pluginhost.ErrLocalFrameRejected) {
		t.Fatalf("oversized invoke error = %v, want ErrLocalFrameRejected", err)
	}
	if errors.Is(err, pluginhost.ErrProtocol) {
		t.Fatalf("oversized invoke must not also be ErrProtocol: %v", err)
	}
	if _, err := fixture.host.Status(context.Background()); err != nil {
		t.Fatalf("host was poisoned by a locally rejected frame: %v", err)
	}
}

func TestConfigFromEnvRequiresExplicitAbsoluteAssets(t *testing.T) {
	t.Setenv(pluginhost.EnvNodePath, "")
	t.Setenv(pluginhost.EnvScriptPath, "")
	if _, err := pluginhost.ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted absent runtime assets")
	}
	t.Setenv(pluginhost.EnvNodePath, "node")
	t.Setenv(pluginhost.EnvScriptPath, "scripts/plugin-host.mjs")
	if _, err := pluginhost.ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted PATH-relative runtime assets")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatalf("resolve Node: %v", err)
	}
	t.Setenv(pluginhost.EnvNodePath, node)
	t.Setenv(pluginhost.EnvScriptPath, hostScript(t))
	config, err := pluginhost.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if config.NodePath != node || config.ScriptPath != hostScript(t) {
		t.Fatalf("unexpected environment config: %+v", config)
	}
}

func startFixtureHost(t *testing.T) *hostFixture {
	t.Helper()
	root := fixtureRoot(t)
	return startHost(t, root)
}

func startHost(t *testing.T, root string) *hostFixture {
	t.Helper()
	return startHostWithConfig(t, root, pluginhost.Config{})
}

func startHostWithConfig(t *testing.T, root string, overrides pluginhost.Config) *hostFixture {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatalf("resolve Node: %v", err)
	}
	events := filepath.Join(t.TempDir(), "plugin-events.log")
	fixture := &hostFixture{root: root, events: events}
	config := overrides
	config.NodePath = node
	config.ScriptPath = hostScript(t)
	config.ExtraEnv = append(config.ExtraEnv, "ANYPICK_PLUGIN_TEST_EVENTS="+events)
	if config.CallTimeout == 0 {
		config.CallTimeout = 2 * time.Second
	}
	config.Stderr = func(line string) {
		fixture.logMu.Lock()
		fixture.logs = append(fixture.logs, line)
		fixture.logMu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fixture.host, err = pluginhost.Start(ctx, config)
	if err != nil {
		t.Fatalf("start plugin host: %v", err)
	}
	t.Cleanup(func() { _ = fixture.host.Close() })
	return fixture
}

func activateFixture(
	t *testing.T,
	host *pluginhost.Host,
	root string,
	name string,
) *pluginhost.Activation {
	t.Helper()
	digest, err := plugins.DigestPluginPackage(root)
	if err != nil {
		t.Fatalf("digest plugin: %v", err)
	}
	activation, err := host.Activate(context.Background(), pluginhost.PluginSpec{
		Root: root, TrustedDigest: digest, ExpectedName: name,
	})
	if err != nil {
		t.Fatalf("activate plugin: %v", err)
	}
	return activation
}

func descriptorsByKind(
	t *testing.T,
	activation *pluginhost.Activation,
) map[pluginhost.RegistrationKind]pluginhost.Descriptor {
	t.Helper()
	result := make(map[pluginhost.RegistrationKind]pluginhost.Descriptor)
	for _, descriptor := range activation.Descriptors {
		if _, duplicate := result[descriptor.Kind]; duplicate {
			t.Fatalf("duplicate descriptor kind %q", descriptor.Kind)
		}
		result[descriptor.Kind] = descriptor
	}
	for _, kind := range []pluginhost.RegistrationKind{
		pluginhost.RegistrationProvider,
		pluginhost.RegistrationClient,
		pluginhost.RegistrationCatalogProvider,
	} {
		if _, ok := result[kind]; !ok {
			t.Fatalf("missing descriptor kind %q", kind)
		}
	}
	return result
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate pluginhost test file")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "full-plugin")
}

func hostScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate pluginhost test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "plugin-host.mjs"))
}

func copyFixture(t *testing.T, directoryName string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), directoryName)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("create fixture copy: %v", err)
	}
	entries, err := os.ReadDir(fixtureRoot(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(fixtureRoot(t), entry.Name()))
		if err != nil {
			t.Fatalf("read fixture file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o600); err != nil {
			t.Fatalf("copy fixture file: %v", err)
		}
	}
	return destination
}

func rewriteManifestName(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, "anypick.plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	manifest["name"] = name
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writePlugin(t *testing.T, root, name, source string) {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"name": name, "version": "1.0.0", "apiVersion": 1, "main": "index.mjs",
	})
	if err != nil {
		t.Fatalf("encode plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "anypick.plugin.json"), manifest, 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.mjs"), []byte(source), 0o600); err != nil {
		t.Fatalf("write plugin entry: %v", err)
	}
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

// writeFakeHostScript emits a minimal host that completes the handshake and
// then answers every request according to mode, giving the tests exact control
// over the response frames the real host script would never send.
func writeFakeHostScript(t *testing.T, mode string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-host.mjs")
	body := `
const mode = process.env.FAKE_HOST_MODE ?? '';
const maxFrameBytes = Number(process.env.ANYPICK_PLUGIN_HOST_MAX_FRAME_BYTES);
const capabilities = [
  'activate', 'cancel', 'dispose', 'invoke', 'invoke-abort-signal',
  'retain-object-results', 'reverse-disposal', 'settled-acknowledgement',
];
process.stdout.write(JSON.stringify({
  version: 1, type: 'hello', protocolVersion: 1, pluginApiVersion: 1,
  maxFrameBytes, capabilities,
}) + '\n');
let buffer = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => {
  buffer += chunk;
  let index;
  while ((index = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, index);
    buffer = buffer.slice(index + 1);
    let frame;
    try { frame = JSON.parse(line); } catch { continue; }
    if (frame.type === 'hello') {
      process.stdout.write(JSON.stringify({
        version: 1, type: 'ready', protocolVersion: 1, maxFrameBytes,
      }) + '\n');
      continue;
    }
    if (frame.type !== 'request') continue;
    if (mode === 'omit-settled') {
      process.stdout.write(JSON.stringify({
        version: 1, type: 'response', id: frame.id, result: { ok: true },
      }) + '\n');
    } else if (mode === 'timeout-settled-false') {
      process.stdout.write(JSON.stringify({
        version: 1, type: 'response', id: frame.id,
        error: { code: 'TIMEOUT', message: 'remote deadline' }, settled: false,
      }) + '\n');
    } else if (mode === 'exit-on-request') {
      process.exit(0);
    }
  }
});
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return script
}

func startFakeHost(t *testing.T, mode string) *pluginhost.Host {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatalf("resolve Node: %v", err)
	}
	config := pluginhost.Config{
		NodePath:    node,
		ScriptPath:  writeFakeHostScript(t, mode),
		CallTimeout: 2 * time.Second,
		ExtraEnv:    []string{"FAKE_HOST_MODE=" + mode},
	}
	host, err := pluginhost.Start(context.Background(), config)
	if err != nil {
		t.Fatalf("start fake host: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

// TestHostRejectsResponseMissingSettledFlag pins the protocol boundary: the
// handshake demands settled-acknowledgement, so a response frame without the
// settlement flag is a protocol violation even when it carries a result.
func TestHostRejectsResponseMissingSettledFlag(t *testing.T) {
	host := startFakeHost(t, "omit-settled")

	err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.Is(err, pluginhost.ErrProtocol) {
		t.Fatalf("missing-settled response error = %v, want ErrProtocol", err)
	}
	// The violation poisons the boundary: later calls fail closed instead of
	// accepting further frames from a host that broke the contract.
	if err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil); !errors.Is(err, pluginhost.ErrProtocol) {
		t.Fatalf("post-violation invoke error = %v, want ErrProtocol", err)
	}
	if snapshot := host.HealthSnapshot(); snapshot.Running {
		t.Fatal("host still reports running after a settlement-flag violation")
	}
}

// TestHostDeliversRemoteTimeoutWithSettledFalse proves a VALID settled:false
// frame still flows through: a remote TIMEOUT reaches both call styles with
// settled=false reported to the caller.
func TestHostDeliversRemoteTimeoutWithSettledFalse(t *testing.T) {
	host := startFakeHost(t, "timeout-settled-false")

	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	var remote *pluginhost.RemoteError
	if !errors.As(err, &remote) || remote.Code != "TIMEOUT" {
		t.Fatalf("InvokeSettling error = %v, want TIMEOUT RemoteError", err)
	}
	if settled {
		t.Fatal("InvokeSettling reported settled=true for an explicit settled:false frame")
	}

	err = host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.As(err, &remote) || remote.Code != "TIMEOUT" {
		t.Fatalf("Invoke error = %v, want TIMEOUT RemoteError", err)
	}
}

// TestHostResolvesPendingCallWhenProcessDies proves a pending request cannot
// outlive its host: the synthesized HOST_CLOSED frame carries settled=false
// and the caller gets a crash-class error promptly.
func TestHostResolvesPendingCallWhenProcessDies(t *testing.T) {
	host := startFakeHost(t, "exit-on-request")

	started := time.Now()
	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("pending call took %s to resolve after host death", elapsed)
	}
	if settled {
		t.Fatal("host closure synthesized settled=true for a pending request")
	}
	if !errors.Is(err, pluginhost.ErrHostCrashed) && !errors.Is(err, pluginhost.ErrHostClosed) {
		t.Fatalf("pending-after-exit error = %v, want ErrHostCrashed/ErrHostClosed", err)
	}
}

// TestHostClosedCallIsNotDispatched: a call that never left this process is
// retry-safe. In-flight death (TestHostResolvesPendingCallWhenProcessDies)
// stays ambiguous and MUST NOT carry this sentinel.
func TestHostClosedCallIsNotDispatched(t *testing.T) {
	host := startFakeHost(t, "timeout-settled-false")
	if err := host.Terminate(); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatalf("closed Invoke = %v, want ErrNotDispatched", err)
	}
	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	if !settled {
		t.Fatal("closed InvokeSettling reported settled=false — callers would treat it as an in-flight mutation")
	}
	if !errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatalf("closed InvokeSettling = %v, want ErrNotDispatched", err)
	}
}

// TestHostProcessIdentityDiffersAcrossRestarts: PID reuse cannot collapse two
// supervised runtimes. Generation includes a unique stamp independent of PID.
func TestHostProcessIdentityDiffersAcrossRestarts(t *testing.T) {
	first := startFakeHost(t, "timeout-settled-false")
	genA, pidA := first.ProcessIdentity()
	if genA == "" || pidA <= 0 {
		t.Fatalf("first identity = %q pid=%d", genA, pidA)
	}
	if err := first.Terminate(); err != nil {
		t.Fatalf("terminate first: %v", err)
	}
	// Identity of a reaped generation stays stable so host-loss reconciliation
	// can still name it after Wait.
	genAfter, pidAfter := first.ProcessIdentity()
	if genAfter != genA || pidAfter != pidA {
		t.Fatalf("terminated identity drifted: %q/%d -> %q/%d", genA, pidA, genAfter, pidAfter)
	}
	second := startFakeHost(t, "timeout-settled-false")
	genB, pidB := second.ProcessIdentity()
	if genB == "" || genB == genA {
		t.Fatalf("replacement reused generation %q (pids %d -> %d)", genB, pidA, pidB)
	}
}

type injectedWriter struct {
	n   int // <0 means write the whole buffer
	err error
}

func (w injectedWriter) Write(p []byte) (int, error) {
	n := w.n
	if n < 0 {
		n = len(p)
	}
	if n > len(p) {
		n = len(p)
	}
	return n, w.err
}

func TestHostZeroByteWriteIsNotDispatched(t *testing.T) {
	host := startFakeHost(t, "timeout-settled-false")
	pluginhost.SetTestWriter(host, injectedWriter{n: 0, err: errors.New("injected zero-byte")})

	err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatalf("zero-byte Invoke = %v, want ErrNotDispatched", err)
	}
	if errors.Is(err, pluginhost.ErrWriteInterrupted) {
		t.Fatal("zero-byte write must not be classified interrupted")
	}
	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	if !settled {
		t.Fatal("zero-byte InvokeSettling reported settled=false")
	}
	if !errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatalf("zero-byte InvokeSettling = %v, want ErrNotDispatched", err)
	}
}

func TestHostPartialWriteIsAmbiguous(t *testing.T) {
	host := startFakeHost(t, "timeout-settled-false")
	pluginhost.SetTestWriter(host, injectedWriter{n: 4, err: errors.New("injected partial")})

	err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.Is(err, pluginhost.ErrWriteInterrupted) {
		t.Fatalf("partial Invoke = %v, want ErrWriteInterrupted", err)
	}
	if errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatal("partial write was treated as not-dispatched and would replay")
	}
	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	if settled {
		t.Fatal("partial InvokeSettling reported settled=true for an ambiguous write")
	}
	if !errors.Is(err, pluginhost.ErrWriteInterrupted) {
		t.Fatalf("partial InvokeSettling = %v, want ErrWriteInterrupted", err)
	}
}

func TestHostFullFrameWriteErrorIsAmbiguous(t *testing.T) {
	host := startFakeHost(t, "timeout-settled-false")
	pluginhost.SetTestWriter(host, injectedWriter{n: -1, err: errors.New("injected full-frame")})

	err := host.Invoke(context.Background(), "handle-x", "method", []any{}, nil)
	if !errors.Is(err, pluginhost.ErrWriteInterrupted) {
		t.Fatalf("full-frame Invoke = %v, want ErrWriteInterrupted", err)
	}
	if errors.Is(err, pluginhost.ErrNotDispatched) {
		t.Fatal("full-frame write error was treated as not-dispatched and would replay")
	}
	settled, err := host.InvokeSettling(
		context.Background(), "handle-x", "method", []any{}, 0, nil, time.Second,
	)
	if settled {
		t.Fatal("full-frame InvokeSettling reported settled=true for an ambiguous write")
	}
	if !errors.Is(err, pluginhost.ErrWriteInterrupted) {
		t.Fatalf("full-frame InvokeSettling = %v, want ErrWriteInterrupted", err)
	}
}

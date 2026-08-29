package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/desktop"
)

func TestDesktopServiceCrossProcessHandshake(t *testing.T) {
	isolateEnv(t)
	bin := requireBuiltCLI(t)
	cmd := exec.Command(bin, "desktop-service", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	frames := make(chan string, 8)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), desktop.MaxServiceFrameBytes)
		for scanner.Scan() {
			frames <- scanner.Text()
		}
		scanErr <- scanner.Err()
		close(frames)
	}()

	hello := awaitServiceFrame(t, frames, scanErr, "hello")
	if !strings.HasPrefix(hello, "hello\t") {
		t.Fatalf("protocol stdout was polluted before hello: %q", hello)
	}
	host := desktop.HostHello{
		ProtocolVersion: desktop.ServiceProtocolVersion,
		AppVersion:      "(devel)",
		Platform:        "test",
		Capabilities: []string{
			"cancel-v1", "command-dto-v1", "explicit-refresh",
			"host-effects-v1", "post-mutation-events", "snapshot-dto-v1",
			"usage-events-v1",
		},
	}
	if _, err := stdin.Write([]byte(desktop.FrameHostHello(host) + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = awaitServiceFrame(t, frames, scanErr, "ready")
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot")
	if _, err := stdin.Write([]byte("quit\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("desktop-service exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("desktop-service did not stop after quit\nstderr: %s", stderr.String())
	}
}

func TestDesktopServiceRequiresExplicitStdio(t *testing.T) {
	isolateEnv(t)
	var code int
	_, stderr := captureBoth(t, func() { code = run([]string{"desktop-service"}) })
	if code == 0 {
		t.Fatal("desktop-service without --stdio unexpectedly started")
	}
	if !strings.Contains(stderr, "desktop-service --stdio") {
		t.Fatalf("missing usage guidance: %q", stderr)
	}
}

func TestDesktopServiceCrossProcessCommandKinds(t *testing.T) {
	isolateEnv(t)
	bin := requireBuiltCLI(t)
	cmd := exec.Command(bin, "desktop-service", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	frames := make(chan string, 64)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), desktop.MaxServiceFrameBytes)
		for scanner.Scan() {
			frames <- scanner.Text()
		}
		scanErr <- scanner.Err()
		close(frames)
	}()

	_ = awaitServiceFrame(t, frames, scanErr, "hello")
	host := desktop.HostHello{
		ProtocolVersion: desktop.ServiceProtocolVersion,
		AppVersion:      "(devel)",
		Platform:        "test",
		Capabilities: []string{
			"cancel-v1", "command-dto-v1", "explicit-refresh",
			"host-effects-v1", "post-mutation-events", "snapshot-dto-v1",
			"usage-events-v1",
		},
	}
	writeServiceLine(t, stdin, desktop.FrameHostHello(host))
	_ = awaitServiceFrame(t, frames, scanErr, "ready")
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot")

	// Host-only commands must neither mutate nor pollute protocol stdout. A
	// following refresh proves the same child remains responsive.
	writeServiceLine(t, stdin, "open")
	writeServiceLine(t, stdin, "navigate\taccounts")
	writeServiceLine(t, stdin, "restart")
	writeServiceLine(t, stdin, "refresh")
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot-event")

	writeServicePayload(t, stdin, "logs", map[string]any{
		"version": 1, "requestId": "logs-1", "providerId": "codex",
		"name": "not-saved", "lines": 20,
	})
	logs := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "logs"))
	if logs["requestId"] != "logs-1" || logs["state"] != "not-running" {
		t.Fatalf("logs response = %#v", logs)
	}

	writeServicePayload(t, stdin, "backup-inspect", map[string]any{
		"version": 1, "requestId": "inspect-1", "path": t.TempDir() + "/missing.anypick",
	})
	preview := decodeServicePayload(t,
		awaitServiceFrame(t, frames, scanErr, "backup-preview"))
	if preview["requestId"] != "inspect-1" || preview["status"] != "error" {
		t.Fatalf("backup preview = %#v", preview)
	}

	writeServicePayload(t, stdin, "invoke", map[string]any{
		"version": 1, "requestId": "invoke-1", "revision": 0,
		"actionId": "missing-action",
	})
	invoked := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "result"))
	if invoked["requestId"] != "invoke-1" || invoked["status"] != "error" {
		t.Fatalf("invoke result = %#v", invoked)
	}

	writeServicePayload(t, stdin, "model-roles", map[string]any{
		"version": 1, "requestId": "roles-1", "revision": 0,
		"clientId": "missing-client", "roleActionIds": map[string]string{},
	})
	roles := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "result"))
	if roles["requestId"] != "roles-1" || roles["status"] != "error" {
		t.Fatalf("model roles result = %#v", roles)
	}

	writeServicePayload(t, stdin, "mutate", map[string]any{
		"version": 1, "requestId": "setting-1", "operation": "setting-show-quota",
		"name": "show-quota", "enabled": false,
	})
	setting := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "result"))
	if setting["requestId"] != "setting-1" || setting["status"] != "success" {
		t.Fatalf("setting result = %#v", setting)
	}
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot-event")

	const fixtureSecret = "must-not-cross-desktop-boundary"
	writeServicePayload(t, stdin, "mutate", map[string]any{
		"version": 1, "requestId": "gateway-create-1", "operation": "gateway-create",
		"providerId": "custom", "name": "fixture", "label": "Fixture",
		"endpoint": "https://example.invalid/v1", "apiKey": fixtureSecret,
	})
	created := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "result"))
	if created["requestId"] != "gateway-create-1" || created["status"] != "success" {
		t.Fatalf("gateway create result = %#v", created)
	}
	createdEvent := awaitServiceFrame(t, frames, scanErr, "snapshot-event")
	if strings.Contains(createdEvent, fixtureSecret) {
		t.Fatal("gateway secret crossed the desktop protocol boundary")
	}

	writeServicePayload(t, stdin, "mutate", map[string]any{
		"version": 1, "requestId": "gateway-remove-1", "operation": "gateway-remove",
		"name": "fixture",
	})
	removed := decodeServicePayload(t, awaitServiceFrame(t, frames, scanErr, "result"))
	if removed["requestId"] != "gateway-remove-1" || removed["status"] != "success" {
		t.Fatalf("gateway remove result = %#v", removed)
	}
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot-event")

	// Cancelling an unknown request is deliberately idempotent and silent.
	writeServicePayload(t, stdin, "cancel", map[string]any{
		"version": 1, "requestId": "already-finished",
	})
	writeServiceLine(t, stdin, "refresh")
	_ = awaitServiceFrame(t, frames, scanErr, "snapshot-event")

	writeServiceLine(t, stdin, "quit")
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("desktop-service exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("desktop-service did not stop after command matrix\nstderr: %s", stderr.String())
	}
}

func writeServiceLine(t *testing.T, stdin interface{ Write([]byte) (int, error) }, line string) {
	t.Helper()
	if _, err := stdin.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func writeServicePayload(
	t *testing.T,
	stdin interface{ Write([]byte) (int, error) },
	prefix string,
	payload any,
) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	writeServiceLine(t, stdin, prefix+"\t"+base64.StdEncoding.EncodeToString(raw))
}

func decodeServicePayload(t *testing.T, line string) map[string]any {
	t.Helper()
	_, encoded, ok := strings.Cut(line, "\t")
	if !ok {
		t.Fatalf("unframed service line: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode service frame: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode service JSON: %v", err)
	}
	return value
}

func awaitServiceFrame(t *testing.T, frames <-chan string, scanErr <-chan error, prefix string) string {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-frames:
			if !ok {
				select {
				case err := <-scanErr:
					t.Fatalf("service stdout closed waiting for %s: %v", prefix, err)
				default:
					t.Fatalf("service stdout closed waiting for %s", prefix)
				}
			}
			if strings.HasPrefix(line, prefix+"\t") {
				return line
			}
			t.Fatalf("unexpected protocol frame before %s: %q", prefix, line)
		case <-timer.C:
			t.Fatalf("timeout waiting for service %s frame", prefix)
		}
	}
}

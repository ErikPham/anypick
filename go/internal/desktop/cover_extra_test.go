package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func decodeFramePayload(t *testing.T, line string) []byte {
	t.Helper()
	parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed frame %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		t.Fatalf("base64 %v line %q", err, line)
	}
	return raw
}

func firstFrameLine(s string) string {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

func TestProviderNameOf(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	if got := providerNameOf(srv, "codex"); got == "" {
		t.Fatalf("providerNameOf codex = %q", got)
	}
	if got := providerNameOf(srv, "nope-provider-xyz"); got != "nope-provider-xyz" {
		t.Fatalf("providerNameOf missing = %q want providerID", got)
	}
}

func TestLogErrAndCurrentRevision(t *testing.T) {
	env := newTrayEnv(t)
	var buf bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &buf)
	srv.logErr(os.ErrInvalid)
	if v := srv.currentRevision(); v != 0 {
		t.Fatalf("currentRevision initial = %d want 0", v)
	}
	h, ok := srv.actionHandler("nope")
	if ok || h != nil {
		t.Fatalf("actionHandler missing should be false")
	}
}

func TestHandleSimpleKinds(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	ctx := context.Background()
	for _, kind := range []string{"open", "navigate", "restart", "refresh", "cancel"} {
		cmd := &Command{Kind: kind}
		if kind == "navigate" {
			cmd.Screen = "accounts"
		}
		if err := srv.handle(ctx, cmd); err != nil {
			t.Fatalf("handle %q error %v", kind, err)
		}
	}
	if err := srv.handle(ctx, &Command{Kind: "stop"}); err == nil {
		t.Fatal("stop should return shutdown")
	}
	if err := srv.handle(ctx, &Command{Kind: "quit"}); err == nil {
		t.Fatal("quit should return shutdown")
	}
}

func TestHandleInvokeBranches(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	ctx := context.Background()

	raw, _ := json.Marshal(invokePayload{Version: 1, RequestID: "r-unknown", Revision: 1, ActionID: "nope"})
	_ = srv.handle(ctx, &Command{Kind: "invoke", Payload: raw})
	decoded := decodeFramePayload(t, firstFrameLine(out.String()))
	var res Result
	_ = json.Unmarshal(decoded, &res)
	if !strings.Contains(res.Message, "unknown action") {
		t.Fatalf("want unknown action error, got %#v raw %q", res, string(decoded))
	}
	out.Reset()

	env.writeCodexLive("inv@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "inv-acc", true); err != nil {
		t.Fatal(err)
	}
	snap, err := srv.buildSnapshot(ctx)
	if err != nil || len(snap.Actions) == 0 {
		t.Fatalf("buildSnapshot = %v %v", snap, err)
	}
	id := snap.Actions[0].ID
	raw2, _ := json.Marshal(invokePayload{Version: 1, RequestID: "r-stale", Revision: -10, ActionID: id})
	out.Reset()
	_ = srv.handle(ctx, &Command{Kind: "invoke", Payload: raw2})
	decoded = decodeFramePayload(t, firstFrameLine(out.String()))
	_ = json.Unmarshal(decoded, &res)
	if !strings.Contains(res.Message, "stale action") {
		t.Fatalf("want stale action, got %#v", res)
	}

	roleRaw, _ := json.Marshal(modelRolesPayload{Version: 1, RequestID: "r-roles", Revision: 1, ClientID: "claude"})
	out.Reset()
	_ = srv.handle(ctx, &Command{Kind: "model-roles", Payload: roleRaw})
	decoded = decodeFramePayload(t, firstFrameLine(out.String()))
	_ = json.Unmarshal(decoded, &res)
	if !strings.Contains(res.Message, "no active binding") {
		t.Fatalf("model-roles without binding must be rejected: %#v", res)
	}
	inspectRaw, _ := json.Marshal(backupInspectPayload{Version: 1, RequestID: "r-ins", Path: "/tmp/anypick-missing-backup"})
	out.Reset()
	_ = srv.handle(ctx, &Command{Kind: "backup-inspect", Payload: inspectRaw})
	var preview BackupPreviewResult
	_ = json.Unmarshal(decodeFramePayload(t, firstFrameLine(out.String())), &preview)
	if preview.Status != "error" || preview.Reason == "" || preview.Message == "" {
		t.Fatalf("backup-inspect missing file must fail cleanly: %#v", preview)
	}

	_ = srv.handle(ctx, &Command{Kind: "logs", Payload: json.RawMessage(`{"version":1,"requestId":"r-logs","providerId":"codex","name":"inv-acc","lines":10}`)})
}

func TestHandleLogs(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	raw, _ := json.Marshal(logsPayload{Version: 1, RequestID: "rl1", ProviderID: "codex", Name: "nope", Lines: 5})
	if err := srv.handleLogs(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	decoded := decodeFramePayload(t, firstFrameLine(out.String()))
	var logs ProxyLogsResult
	if json.Unmarshal(decoded, &logs) != nil {
		t.Fatalf("logs json %s", decoded)
	}
	if logs.State != "not-running" {
		t.Fatalf("handleLogs state = %q want not-running decoded %s", logs.State, decoded)
	}
}

func TestHandleMutateOperations(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	ctx := context.Background()
	env.writeCodexLive("mut@example.com")

	cases := []struct {
		name string
		m    Mutate
	}{
		{"account-save-current", Mutate{Version: 1, RequestID: "m1", Operation: "account-save-current", ProviderID: "codex"}},
		{"account-save", Mutate{Version: 1, RequestID: "m2", Operation: "account-save", ProviderID: "codex", Name: "m2-acc"}},
		{"account-remove-missing", Mutate{Version: 1, RequestID: "m3", Operation: "account-remove", ProviderID: "codex", Name: "nope"}},
		{"gateway-create-missing", Mutate{Version: 1, RequestID: "m4", Operation: "gateway-create", ProviderID: "", Name: ""}},
		{"gateway-create-ok", Mutate{Version: 1, RequestID: "m5", Operation: "gateway-create", ProviderID: "custom", Name: "gw1", Endpoint: "https://api.example/v1", APIKey: "sk-123"}},
		{"gateway-edit-ok", Mutate{Version: 1, RequestID: "m6", Operation: "gateway-edit", ProviderID: "custom", Name: "gw1", Endpoint: "https://api2.example/v1", Label: "Edited"}},
		{"gateway-remove-ok", Mutate{Version: 1, RequestID: "m7", Operation: "gateway-remove", Name: "gw1"}},
		{"gateway-remove-missing-name", Mutate{Version: 1, RequestID: "m8", Operation: "gateway-remove", Name: ""}},
		{"gateway-edit-missing", Mutate{Version: 1, RequestID: "m9", Operation: "gateway-edit", Name: "nope-gw", Label: "x"}},
		{"client-reset-ok", Mutate{Version: 1, RequestID: "m10", Operation: "client-reset", ProviderID: "claude"}},
		{"client-reset-bad", Mutate{Version: 1, RequestID: "m11", Operation: "client-reset", ProviderID: "nope"}},
		{"proxy-stop-all", Mutate{Version: 1, RequestID: "m12", Operation: "proxy-stop-all"}},
		{"proxy-restart-all", Mutate{Version: 1, RequestID: "m13", Operation: "proxy-restart-all"}},
		{"account-refresh-missing", Mutate{Version: 1, RequestID: "m14", Operation: "account-refresh", ProviderID: "codex", Name: "nope"}},
		{"unsupported", Mutate{Version: 1, RequestID: "m15", Operation: "nope-op"}},
	}

	for _, c := range cases {
		out.Reset()
		raw, _ := json.Marshal(c.m)
		if err := srv.handleMutate(ctx, raw); err != nil {
			t.Fatalf("%s handleMutate error %v", c.name, err)
		}
		if out.Len() == 0 {
			t.Fatalf("%s no result written", c.name)
		}
		line := firstFrameLine(out.String())
		decoded := decodeFramePayload(t, line)
		var res Result
		if json.Unmarshal(decoded, &res) != nil {
			t.Fatalf("%s json %s", c.name, decoded)
		}
		if res.RequestID != c.m.RequestID {
			t.Fatalf("%s requestId %q want %q", c.name, res.RequestID, c.m.RequestID)
		}
	}

	if _, err := env.Svc.Save(ctx, "codex", "ref-acc", true); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	raw, _ := json.Marshal(Mutate{Version: 1, RequestID: "m-ref-ok", Operation: "account-refresh", ProviderID: "codex", Name: "ref-acc"})
	if err := srv.handleMutate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "result") {
		t.Fatalf("account-refresh success missing result %q", out.String())
	}
}

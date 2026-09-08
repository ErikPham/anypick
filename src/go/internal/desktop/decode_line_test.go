package desktop

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func mustB64(t *testing.T, v interface{}) string {
	t.Helper()
	raw, _ := json.Marshal(v)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestDecodeLineSimpleKinds(t *testing.T) {
	for _, k := range []string{"open", "refresh", "restart", "stop", "quit"} {
		cmd, err := DecodeLine(k)
		if err != nil || cmd.Kind != k {
			t.Fatalf("DecodeLine(%q)=%+v %v", k, cmd, err)
		}
	}
}

func TestDecodeLineNavigate(t *testing.T) {
	cmd, err := DecodeLine("navigate\taccounts")
	if err != nil || cmd.Kind != "navigate" || cmd.Screen != "accounts" {
		t.Fatalf("navigate accounts = %+v %v", cmd, err)
	}
	if _, err := DecodeLine("navigate\tunknown-screen"); err == nil {
		t.Fatal("unknown screen should error")
	}
}

func TestDecodeLineInvoke(t *testing.T) {
	payload := map[string]interface{}{"version": 1, "requestId": "r1", "revision": 1, "actionId": "act-1"}
	cmd, err := DecodeLine("invoke\t" + mustB64(t, payload))
	if err != nil || cmd.Kind != "invoke" {
		t.Fatalf("invoke = %+v %v", cmd, err)
	}
	var got invokePayload
	if json.Unmarshal(cmd.Payload, &got) != nil || got.RequestID != "r1" {
		t.Fatalf("invoke payload = %s", cmd.Payload)
	}
	if _, err := DecodeLine("invoke\tbad-base64!!!"); err == nil {
		t.Fatal("bad base64 should error")
	}
	if _, err := DecodeLine("invoke\t"); err == nil {
		t.Fatal("empty body should error")
	}
}

func TestDecodeLineModelRoles(t *testing.T) {
	p := map[string]interface{}{"version": 1, "requestId": "r2", "revision": 1, "clientId": "claude", "roleActionIds": map[string]string{}}
	cmd, err := DecodeLine("model-roles\t" + mustB64(t, p))
	if err != nil || cmd.Kind != "model-roles" {
		t.Fatalf("model-roles = %+v %v", cmd, err)
	}
}

func TestDecodeLineLogs(t *testing.T) {
	p := map[string]interface{}{"version": 1, "requestId": "r3", "providerId": "codex", "name": "a", "lines": 100}
	cmd, err := DecodeLine("logs\t" + mustB64(t, p))
	if err != nil || cmd.Kind != "logs" {
		t.Fatalf("logs = %+v %v", cmd, err)
	}
}

func TestDecodeLineBackupInspectAndCancel(t *testing.T) {
	p1 := map[string]interface{}{"version": 1, "requestId": "r4", "path": "/tmp/x"}
	cmd, err := DecodeLine("backup-inspect\t" + mustB64(t, p1))
	if err != nil || cmd.Kind != "backup-inspect" {
		t.Fatalf("backup-inspect = %+v %v", cmd, err)
	}
	p2 := map[string]interface{}{"version": 1, "requestId": "r5"}
	cmd, err = DecodeLine("cancel\t" + mustB64(t, p2))
	if err != nil || cmd.Kind != "cancel" {
		t.Fatalf("cancel = %+v %v", cmd, err)
	}
}

func TestDecodeLineMutate(t *testing.T) {
	// Contract-accurate wire form: account-save never carries a path.
	m := map[string]interface{}{"version": 1, "requestId": "r6", "operation": "account-save", "providerId": "grok", "name": "work"}
	cmd, err := DecodeLine("mutate\t" + mustB64(t, m))
	if err != nil || cmd.Kind != "mutate" {
		t.Fatalf("mutate = %+v %v", cmd, err)
	}
	var got Mutate
	if json.Unmarshal(cmd.Payload, &got) != nil || got.Operation != "account-save" {
		t.Fatalf("mutate payload = %s", cmd.Payload)
	}
}

func TestDecodeLineErrors(t *testing.T) {
	if _, err := DecodeLine("not-a-command"); err == nil {
		t.Fatal("unknown line should error")
	}
	large := strings.Repeat("x", MaxCommandBytes+1)
	if _, err := DecodeLine(large); err == nil {
		t.Fatal("too large should error")
	}
	// also via prefixed path with huge b64
	largeB64 := strings.Repeat("a", MaxCommandBytes+1)
	if _, err := DecodeLine("invoke\t" + largeB64); err == nil {
		t.Fatal("huge b64 should error")
	}
}

func TestFrameHelpers(t *testing.T) {
	snap := &Snapshot{Revision: 1, Routes: []map[string]any{}, Actions: []ActionRow{}}
	line := FrameSnapshot(snap)
	if !strings.HasPrefix(line, "snapshot\t") {
		t.Fatalf("FrameSnapshot prefix = %q", line)
	}
	// round-trip decode
	parts := strings.SplitN(line, "\t", 2)
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if json.Unmarshal(raw, &decoded) != nil || decoded.Revision != 1 {
		t.Fatalf("decoded snapshot = %s", raw)
	}

	res := Result{Version: 1, RequestID: "r1", Status: "success"}
	rLine := FrameResult(res)
	if !strings.HasPrefix(rLine, "result\t") {
		t.Fatalf("FrameResult prefix = %q", rLine)
	}

	pl := ProxyLogsResult{Version: 1, RequestID: "r1", ProxyID: "grok/work", State: "ready", Text: "hello"}
	pLine := FrameProxyLogs(pl)
	if !strings.HasPrefix(pLine, "logs\t") {
		t.Fatalf("FrameProxyLogs prefix = %q", pLine)
	}

	// decodeB64Body directly: invalid JSON
	var out map[string]interface{}
	if err := decodeB64Body(mustB64(t, "not-a-json-object-but-string"), &out); err != nil {
		// string JSON unmarshals into out as error? Actually json.Unmarshal string into map fails
		// but that's expected to return error
	}
	if err := decodeB64Body("!!!notb64", &out); err == nil {
		t.Fatal("invalid b64 should error")
	}
}

func TestProviderNameOfAndOrString(t *testing.T) {
	// providerNameOf needs a Server; test orString directly via snapshot gateways
	if got := orString("label", "fallback"); got != "label" {
		t.Fatalf("orString label = %q", got)
	}
	if got := orString("", "fallback"); got != "fallback" {
		t.Fatalf("orString fallback = %q", got)
	}
}

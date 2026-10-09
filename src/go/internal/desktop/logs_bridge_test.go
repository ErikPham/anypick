package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/ownership"
)

func TestHandleLogsProxyHub(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	request := func() ProxyLogsResult {
		t.Helper()
		out.Reset()
		raw, mErr := json.Marshal(logsPayload{Version: 1, RequestID: "r-hub",
			ProviderID: "proxy-hub", Name: "default", Lines: 50})
		if mErr != nil {
			t.Fatal(mErr)
		}
		if hErr := srv.handleLogs(context.Background(), raw); hErr != nil {
			t.Fatal(hErr)
		}
		decoded := decodeFramePayload(t, firstFrameLine(out.String()))
		var res ProxyLogsResult
		if err := json.Unmarshal(decoded, &res); err != nil {
			t.Fatalf("decode %s: %v", decoded, err)
		}
		return res
	}

	// 1. Hub stopped without log file -> res.State == "not-running" (not "error")
	res := request()
	if res.State != "not-running" {
		t.Fatalf("stopped hub without logs: got %q, want not-running", res.State)
	}

	// 2. Write service.log
	hubLog := filepath.Join(env.Deps.Root, "runtime", "hub", "default", "service.log")
	if err := os.MkdirAll(filepath.Dir(hubLog), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "15:00:00  POST /v1/chat/completions → 200 in 150 ms\nauthorization: Bearer sk-ant-api03-abcdef1234567890abcdef1234567890abcdef1234567890-abcdef1234AA\n"
	if err := os.WriteFile(hubLog, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Stopped hub with existing log file -> "ready", historical logs preserved, secrets redacted
	res = request()
	if res.State != "ready" {
		t.Fatalf("stopped hub with logs: got %q, want ready", res.State)
	}
	if !strings.Contains(res.Text, "POST /v1/chat/completions → 200 in 150 ms") {
		t.Fatalf("missing log content: %q", res.Text)
	}
	if strings.Contains(res.Text, "sk-ant-api03") {
		t.Fatalf("unredacted secret in hub log: %q", res.Text)
	}

	// 3. Hub live (running), but log file empty -> res.State == "empty" (not "not-running")
	rec := &ownership.Record{
		RecordVersion: ownership.RecordVersion,
		InstanceID:    "11111111-2222-3333-4444-555555555555",
		PID:           os.Getpid(),
		Command:       []string{os.Args[0]},
		Executable:    os.Args[0],
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	rawRec, _ := json.Marshal(rec)
	pidFile := filepath.Join(env.Deps.Root, "runtime", "hub", "default", "service.pid")
	if err := os.WriteFile(pidFile, rawRec, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(hubLog) // no logs yet
	res = request()
	if res.State != "empty" {
		t.Fatalf("running hub without logs: got %q, want empty", res.State)
	}
}

//go:build !windows

package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/store"
)

func TestSignalPIDErrorsOnBadPIDAndStopsChild(t *testing.T) {
	if err := signalPID(0); err == nil {
		t.Fatal("want error for pid 0")
	}
	if err := signalPID(-5); err == nil {
		t.Fatal("want error for negative pid")
	}

	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	if err := signalPID(cmd.Process.Pid); err != nil {
		t.Fatalf("signalPID: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after SIGTERM")
	}
}

func TestServerErrChannelQuietWhileServing(t *testing.T) {
	srv := &Server{Token: MustNewToken()}
	port, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen("127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	select {
	case err := <-srv.Err():
		t.Fatalf("unexpected serve error: %v", err)
	default:
		// No accept-loop failure yet — expected.
	}
}

func TestCleanupLeasesRemovesOnlyMatchingInstance(t *testing.T) {
	ls := testLeaseStore(t)
	svc := &Service{Jobs: ls}
	mk := func(id, inst string) {
		t.Helper()
		if err := ls.Create(Lease{
			LeaseID: id, Provider: "codex", Account: "work",
			Port: 4000 + len(id), Host: "127.0.0.1",
			OwnerPID: ownPID(), InstanceID: inst,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("l-match", "inst-a")
	mk("l-keep", "inst-b")

	svc.cleanupLeases("codex", "work", "inst-a")

	if got, _ := ls.Get("l-match"); got != nil {
		t.Fatal("matching lease survived cleanup")
	}
	if got, _ := ls.Get("l-keep"); got == nil {
		t.Fatal("unrelated lease was deleted")
	}
}

func TestExecSpawnerLaunchesChildProcess(t *testing.T) {
	spawner := ExecSpawner("/bin/sleep")
	logPath := t.TempDir() + "/proxy.log"
	pid, err := spawner("codex", "work", store.ProxyRuntimeState{}, logPath)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want > 0", pid)
	}
	// The bogus child (sleep with proxy args) exits by itself; make sure the
	// spawner's reaper does not panic on early exit.
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExtractRequestID(t *testing.T) {
	if got := extractRequestID([]byte(`{"request_id":"req_1","x":1}`)); got != "req_1" {
		t.Fatalf("got %q want req_1", got)
	}
	if got := extractRequestID([]byte(`{"other":true}`)); got != "" {
		t.Fatalf("got %q want empty", got)
	}
	if got := extractRequestID([]byte(`not-json`)); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestOpenAIResponsesContextWindowSSEFrame(t *testing.T) {
	sse := string(openAIResponsesContextWindowSSE())
	if !strings.HasPrefix(sse, "event: response.failed\n") {
		t.Fatalf("missing event line: %q", sse)
	}
	if !strings.Contains(sse, `"code":"context_length_exceeded"`) {
		t.Fatalf("missing compaction code: %q", sse)
	}
	if !strings.HasSuffix(sse, "\n\n") {
		t.Fatalf("SSE frames end with a blank line: %q", sse)
	}
}

func TestRewriteContextWindowResponseBranches(t *testing.T) {
	ctxBody := `{"error":{"type":"invalid_request_error","message":"prompt is too long: 200 tokens > 100 maximum"}}`
	plainBody := `{"error":{"type":"authentication_error","message":"bad key"}}`

	mkResp := func(status int, body string) *http.Response {
		resp := &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		return resp
	}
	readBody := func(resp *http.Response) string {
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}

	// 2xx responses pass through untouched.
	okResp := mkResp(200, plainBody)
	reqOK := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := rewriteContextWindowResponse(okResp, reqOK); err != nil {
		t.Fatalf("2xx: %v", err)
	}
	if got := readBody(okResp); got != plainBody {
		t.Fatalf("2xx body changed: %s", got)
	}

	// Non-context errors are restored verbatim.
	plainResp := mkResp(400, plainBody)
	reqPlain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := rewriteContextWindowResponse(plainResp, reqPlain); err != nil {
		t.Fatalf("plain 400: %v", err)
	}
	if got := readBody(plainResp); got != plainBody || plainResp.StatusCode != 400 {
		t.Fatalf("plain 400 mutated: %d %s", plainResp.StatusCode, got)
	}

	// Anthropic path: rewritten to the vendor-shaped overflow body.
	anthResp := mkResp(400, ctxBody)
	anthResp.Header.Set("request-id", "req_9")
	reqAnth := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if err := rewriteContextWindowResponse(anthResp, reqAnth); err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	body := readBody(anthResp)
	if anthResp.StatusCode != 400 ||
		!strings.Contains(anthResp.Header.Get("Content-Type"), "application/json") ||
		!strings.Contains(body, ContextWindowErrorCode) {
		t.Fatalf("anthropic rewrite: %d %q", anthResp.StatusCode, body)
	}
	if got := extractRequestID([]byte(body)); got != "req_9" {
		t.Fatalf("request-id not propagated: %q", got)
	}

	// OpenAI path keeps its own message shape.
	oaiResp := mkResp(413, ctxBody)
	reqOAI := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := rewriteContextWindowResponse(oaiResp, reqOAI); err != nil {
		t.Fatalf("openai: %v", err)
	}
	if oaiResp.StatusCode != 400 || oaiResp.ContentLength <= 0 {
		t.Fatalf("openai rewrite: %d %d", oaiResp.StatusCode, oaiResp.ContentLength)
	}

	// Responses API + streaming client → SSE compaction trigger at 200.
	sseReq := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp := mkResp(400, ctxBody)
	if err := rewriteContextWindowResponse(sseResp, sseReq); err != nil {
		t.Fatalf("sse: %v", err)
	}
	if sseResp.StatusCode != 200 ||
		!strings.Contains(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse rewrite: %d %q", sseResp.StatusCode, sseResp.Header.Get("Content-Type"))
	}
	if raw := readBody(sseResp); !strings.Contains(raw, "response.failed") {
		t.Fatalf("sse body: %q", raw)
	}
}

func TestServeExistingRejectsMissingState(t *testing.T) {
	ts := newTestServices(t)
	err := ts.Service.ServeExisting(context.Background(), "codex", "ghost")
	if err == nil || !strings.Contains(err.Error(), "no proxy state") {
		t.Fatalf("want no-state error, got %v", err)
	}
}

func TestServeExistingServesUntilTerminated(t *testing.T) {
	ts := newTestServices(t)
	seedAccount(t, ts.Service, "serve-child", true)

	port, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	const instID = "inst-child"
	st := store.ProxyRuntimeState{
		AccountName: "serve-child", Endpoint: endpoint,
		PID: 0, InstanceID: instID, StartedAt: nowISO(), Token: "tok-child",
	}
	ctx := context.Background()
	if err := ts.Service.Store.WriteProxyStateContext(ctx, "codex", "serve-child", st); err != nil {
		t.Fatal(err)
	}
	ls := ts.Service.Jobs
	if err := ls.Create(Lease{
		LeaseID: "lease-child", Provider: "codex", Account: "serve-child",
		Port: port, Host: "127.0.0.1", Endpoint: endpoint,
		OwnerPID: ownPID(), InstanceID: instID,
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- ts.Service.ServeExisting(ctx, "codex", "serve-child") }()

	// Once /health echoes our instance id the child is serving and the signal
	// handler is registered — safe to terminate ourselves.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, herr := client.Get(endpoint + "/health")
		if herr == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
			resp.Body.Close()
			if strings.Contains(string(raw), instID) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("ServeExisting never became healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := syscall.Kill(ownPID(), syscall.SIGTERM); err != nil {
		t.Fatalf("self-signal: %v", err)
	}
	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("ServeExisting: %v", serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeExisting did not stop on SIGTERM")
	}

	// Cleanup: liveness and the matching lease are gone. The bearer token
	// must survive — rotating it here is how every configured client 401s
	// until the next activation rewrites settings.
	state, _ := ts.Service.Store.ReadProxyState(ctx, "codex", "serve-child")
	if state == nil || state.Token != "tok-child" {
		t.Fatalf("stable token did not survive child shutdown: %+v", state)
	}
	if state.Endpoint != "" || state.InstanceID != "" || state.PID != 0 {
		t.Fatalf("liveness survived child shutdown: %+v", state)
	}
	if lease, _ := ls.Get("lease-child"); lease != nil {
		t.Fatal("lease survived shutdown")
	}
}

func TestStopDropsStaleCrossProcessRecords(t *testing.T) {
	ts := newTestServices(t)
	seedAccount(t, ts.Service, "stale-stop", true)

	ctx := context.Background()
	// State whose endpoint answers nothing: liveness check fails, so Stop must
	// drop records without signalling anyone.
	st := store.ProxyRuntimeState{
		AccountName: "stale-stop",
		Endpoint:    "http://127.0.0.1:1",
		PID:         999999, InstanceID: "inst-stale", StartedAt: nowISO(), Token: "tok",
	}
	if err := ts.Service.Store.WriteProxyStateContext(ctx, "codex", "stale-stop", st); err != nil {
		t.Fatal(err)
	}
	ls := ts.Service.Jobs
	if err := ls.Create(Lease{
		LeaseID: "lease-stale", Provider: "codex", Account: "stale-stop",
		Port: 1, Host: "127.0.0.1", OwnerPID: 999999, InstanceID: "inst-stale",
	}); err != nil {
		t.Fatal(err)
	}

	if err := ts.Service.Stop(ctx, "codex", "stale-stop"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	state, _ := ts.Service.Store.ReadProxyState(ctx, "codex", "stale-stop")
	if state == nil || state.Endpoint != "" || state.InstanceID != "" {
		t.Fatalf("stale liveness survived: %+v", state)
	}
	if state.Token != "tok" {
		t.Fatalf("stop rotated the stable token: %+v", state)
	}
	if lease, _ := ls.Get("lease-stale"); lease != nil {
		t.Fatal("stale lease survived")
	}

	// Stopping again with nothing recorded stays a no-op.
	if err := ts.Service.Stop(ctx, "codex", "stale-stop"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

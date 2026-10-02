package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// The native app can only show what crosses this boundary. Before Result.Fail
// the engine sent one flattened sentence: no code to branch on, no suggestion
// to act on, and no way to know a failed mutation had already rewritten a
// credential file. These tests pin the three fields that fixed that.

func TestFrameServiceReportsAnUnencodablePayload(t *testing.T) {
	// A frame with an empty body is unparseable to the app, which then cannot
	// tell a broken engine from a broken request. The one shape it always
	// understands is a rejection.
	line := frameService("result", make(chan int))
	if !strings.HasPrefix(line, "protocol-error\t") {
		t.Fatalf("frame = %q, want a protocol-error", line)
	}
	var payload ProtocolError
	if err := json.Unmarshal(decodeFramePayload(t, line), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "FRAME_ENCODE_FAILED" {
		t.Fatalf("Code = %q", payload.Code)
	}
}

func TestCancelIsNotAFailure(t *testing.T) {
	// A user-initiated cancel must not carry an error code or suggestions:
	// the app branches on those to raise its error affordances, and scolding
	// someone for their own cancellation is the bug this prevents.
	result := Result{Version: ProtocolVersion, RequestID: "r9"}.Fail(
		anyerr.Newf("boom", "PROXY_START_FAILED", anyerr.WithSuggestions("hint")))
	result = result.Cancel("Sign-in cancelled.")

	if result.Status != "cancelled" {
		t.Fatalf("Status = %q, want cancelled", result.Status)
	}
	if result.Code != "" || len(result.Suggestions) != 0 || result.Mutated {
		t.Fatalf("a cancel must clear the failure fields: %#v", result)
	}
	if result.Message != "Sign-in cancelled." {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestFailCarriesCodeSuggestionsAndMutationState(t *testing.T) {
	failure := anyerr.Newf("Could not reach the upstream.", "PROXY_START_FAILED",
		anyerr.WithExitCode(anyerr.HealthFailure),
		anyerr.WithMutated(true),
		anyerr.WithSuggestions("Check the port is free.", "Run `anypick doctor`."))

	result := Result{Version: ProtocolVersion, RequestID: "r1", Status: "success"}.Fail(failure)

	if result.Status != "error" {
		t.Fatalf("Status = %q, want error", result.Status)
	}
	if result.Code != "PROXY_START_FAILED" {
		t.Fatalf("Code = %q, want PROXY_START_FAILED", result.Code)
	}
	if !result.Mutated {
		t.Fatal("Mutated must survive: the app uses it to warn that state was partially changed")
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("Suggestions = %v, want both hints", result.Suggestions)
	}
	if !strings.Contains(result.Message, "partially changed") {
		t.Fatalf("a mutated failure must say so, got %q", result.Message)
	}
	if result.RequestID != "r1" {
		t.Fatalf("RequestID = %q, want r1", result.RequestID)
	}
}

func TestFailKeepsCodeFromAWrappedCause(t *testing.T) {
	inner := anyerr.Newf("No account named work.", "ACCOUNT_NOT_FOUND",
		anyerr.WithSuggestions("List accounts with `anypick list accounts`."))
	// The desktop handlers wrap service errors more than once on the way out;
	// errors.As is what keeps the code reachable through that chain.
	wrapped := fmt.Errorf("remove account: %w", inner)

	result := Result{Version: ProtocolVersion, RequestID: "r2"}.Fail(wrapped)

	if result.Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("Code = %q, want ACCOUNT_NOT_FOUND", result.Code)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("Suggestions = %v, want the inner hint", result.Suggestions)
	}
}

func TestFailNilErrorIsNotAFailure(t *testing.T) {
	// `if err != nil { result = result.Fail(err) }` is the only shape callers
	// need, so Fail(nil) must not invent an error.
	start := Result{Version: ProtocolVersion, RequestID: "r3", Status: "success", Message: "fine"}
	got := start.Fail(nil)
	if got.Status != start.Status || got.Message != start.Message || got.Code != start.Code ||
		got.Mutated || len(got.Suggestions) != 0 {
		t.Fatalf("Fail(nil) = %#v, want the result unchanged", got)
	}
}

func TestFailTimeoutStatesWhatIsUnknown(t *testing.T) {
	result := Result{Version: ProtocolVersion, RequestID: "r4"}.Fail(
		fmt.Errorf("run activation: %w", context.DeadlineExceeded))

	if !strings.Contains(result.Message, "run doctor") {
		t.Fatalf("a timeout must route the user to doctor, got %q", result.Message)
	}
	if strings.Contains(result.Message, "rolled back") {
		t.Fatalf("the engine cannot know the rollback happened; message = %q", result.Message)
	}
}

func TestFailNeverRendersACredentialShapedMessage(t *testing.T) {
	token := strings.Repeat("A", 80)
	result := Result{Version: ProtocolVersion, RequestID: "r5"}.Fail(
		anyerr.Newf("upstream said "+token, "PROXY_UPSTREAM_FAILED"))

	if strings.Contains(result.Message, token) {
		t.Fatalf("display-unsafe message reached the frame: %q", result.Message)
	}
	if result.Code != "PROXY_UPSTREAM_FAILED" {
		t.Fatalf("sanitizing the message must not drop the code, got %q", result.Code)
	}
}

func TestFailDropsUnrenderableSuggestions(t *testing.T) {
	result := Result{Version: ProtocolVersion, RequestID: "r6"}.Fail(
		anyerr.Newf("failed", "PROXY_START_FAILED",
			anyerr.WithSuggestions(
				strings.Repeat("B", 120),
				"Check that port 8787 is free.",
				"Second hint.",
				"Third hint.",
				"Fourth hint.")))

	if len(result.Suggestions) > maxResultSuggestions {
		t.Fatalf("Suggestions = %v, want at most %d", result.Suggestions, maxResultSuggestions)
	}
	for _, suggestion := range result.Suggestions {
		if strings.Contains(suggestion, "BBBB") {
			t.Fatalf("token-shaped suggestion rendered: %q", suggestion)
		}
	}
	if len(result.Suggestions) == 0 || result.Suggestions[0] != "Check that port 8787 is free." {
		t.Fatalf("Suggestions = %v, want the renderable hints in order", result.Suggestions)
	}
}

func TestFailAlwaysReportsSomething(t *testing.T) {
	// An empty message would render as a blank alert with a dismiss button and
	// no explanation of what failed.
	if got := (Result{}).Fail(errors.New("")); got.Message == "" {
		t.Fatal("an error with no text must still produce an explanation")
	}
}

func TestPayloadErrIsInvalidUsage(t *testing.T) {
	result := Result{Version: ProtocolVersion, RequestID: "r7"}.Fail(
		payloadErr("account-save", "providerId and name"))

	if result.Message != "account-save needs providerId and name" {
		t.Fatalf("Message = %q", result.Message)
	}
	if result.Code != "INVALID_USAGE" {
		t.Fatalf("Code = %q, want INVALID_USAGE", result.Code)
	}
}

func TestPanicErrAssumesTheWorstAboutState(t *testing.T) {
	// A handler that died mid-flight may have written and failed to record it.
	// Telling the user nothing changed would be a lie they could act on.
	err := panicErr("boom")
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "INTERNAL_PANIC" {
		t.Fatalf("panicErr code = %v", err)
	}
	if !ae.Mutated {
		t.Fatal("a recovered panic must report Mutated")
	}
	if !strings.Contains(strings.Join(ae.Suggestions, " "), "doctor") {
		t.Fatalf("Suggestions = %v, want a doctor handoff", ae.Suggestions)
	}
}

func TestFrameResultOmitsEmptyStructuredFields(t *testing.T) {
	// The new fields are additive: a success frame must stay byte-identical to
	// what the shipped app already decodes, so an older client keeps working.
	raw := decodeFramePayload(t, FrameResult(Result{
		Version: ProtocolVersion, RequestID: "r8", Status: "success", Message: "Done.",
	}))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"code", "suggestions", "mutated"} {
		if _, present := fields[key]; present {
			t.Fatalf("success frame carries %q: %s", key, raw)
		}
	}
}

func TestFrameProtocolErrorSanitizesItsMessage(t *testing.T) {
	token := strings.Repeat("Z", 90)
	line := FrameProtocolError(ProtocolError{
		ProtocolVersion: ServiceProtocolVersion,
		Code:            "MALFORMED_FRAME",
		Message:         "rejected: " + token,
	})
	raw := decodeFramePayload(t, line)
	if strings.Contains(string(raw), token) {
		t.Fatalf("protocol-error rendered a token-shaped message: %s", raw)
	}
	var payload ProtocolError
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "MALFORMED_FRAME" {
		t.Fatalf("Code = %q, want MALFORMED_FRAME", payload.Code)
	}
}

// TestActionPanicStillPublishesATerminalResult is the reason the invoke
// goroutine recovers: the native sheet stays disabled until a frame carries
// its requestId, so an unhandled panic in any provider adapter stranded the
// whole app (or killed the service) instead of reporting one failed action.
func TestActionPanicStillPublishesATerminalResult(t *testing.T) {
	env := newTrayEnv(t)
	out := &syncBuffer{}
	srv := New(env.Deps, strings.NewReader(""), out)

	srv.actionsMu.Lock()
	srv.actions = map[string]actionHandler{
		"action-that-panics": func(context.Context, actionOptions) error {
			panic("provider adapter exploded")
		},
	}
	srv.actionsMu.Unlock()

	got := invokeAndAwait(t, srv, out, 1, "action-that-panics", "req-panic")

	if got.Status != "error" {
		t.Fatalf("Status = %q, want error", got.Status)
	}
	if got.Code != "INTERNAL_PANIC" {
		t.Fatalf("Code = %q, want INTERNAL_PANIC", got.Code)
	}
	if !got.Mutated {
		t.Fatal("a panic mid-mutation must be reported as possibly mutated")
	}
	if got.RequestID != "req-panic" {
		t.Fatalf("RequestID = %q, want req-panic", got.RequestID)
	}
	if strings.Contains(got.Message, "provider adapter exploded") {
		t.Fatalf("panic text is engine-internal and must not reach the app: %q", got.Message)
	}
}

// TestRejectedMutationCarriesStructuredDetailOnTheWire proves the contract
// end-to-end rather than at the struct: the native app reads a frame, and a
// frame that carries only a sentence is what made every app failure look the
// same. It drives the real service over pipes, exactly as the app does.
func TestRejectedMutationCarriesStructuredDetailOnTheWire(t *testing.T) {
	env := newTrayEnv(t)
	inR, inW, outR, outW := servicePipes(t)
	srv := NewService(env.Deps, inR, outW)
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()
	_ = readFrame(t, outR, "hello")
	sendCommand(t, inW, FrameHostHello(validHostHello()))
	_ = readFrame(t, outR, "ready")
	_ = readFrame(t, outR, "snapshot")

	// A gateway-create that reaches the handler without an endpoint (and, for a
	// vendor that needs one, without a key) is refused there rather than by
	// frame validation, so the refusal is what the app must be able to explain.
	sendCommand(t, inW, "mutate\t"+encodePayload(map[string]any{
		"version": 1, "requestId": "req-endpoint", "operation": "gateway-create",
		"providerId": "openrouter", "name": "work",
	}))

	result := decodeJSON(t, readFrame(t, outR, "result")[len("result\t"):])
	if result["status"] != "error" {
		t.Fatalf("status = %v, want error", result["status"])
	}
	if result["code"] != "INVALID_USAGE" {
		t.Fatalf("code = %v, want INVALID_USAGE in %#v", result["code"], result)
	}
	if result["message"] != "gateway-create needs endpoint and apiKey" {
		t.Fatalf("message = %v", result["message"])
	}
	if result["requestId"] != "req-endpoint" {
		t.Fatalf("requestId = %v: the app matches frames by requestId", result["requestId"])
	}

	sendCommand(t, inW, "quit")
	if err := <-done; err != nil {
		t.Fatalf("quit: %v", err)
	}
}

// TestEveryRejectedFrameAnswersTheClient is the no-hang guarantee: the app
// holds the mutation slot open until a terminal frame names its requestId, so
// a frame the engine refuses must still produce one. Before this, a rejected
// command was logged and dropped.
func TestEveryRejectedFrameAnswersTheClient(t *testing.T) {
	env := newTrayEnv(t)
	inR, inW, outR, outW := servicePipes(t)
	srv := NewService(env.Deps, inR, outW)
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()
	_ = readFrame(t, outR, "hello")
	sendCommand(t, inW, FrameHostHello(validHostHello()))
	_ = readFrame(t, outR, "ready")
	_ = readFrame(t, outR, "snapshot")

	for _, frame := range []string{
		"mutate\tnot-canonical-base64",
		"model-roles\tnot-canonical-base64",
		"cancel\tnot-canonical-base64",
		"backup-inspect\tnot-canonical-base64",
		"navigate\tnowhere",
	} {
		sendCommand(t, inW, frame)
		protocolErr := decodeJSON(t, readFrame(t, outR, "protocol-error")[len("protocol-error\t"):])
		if protocolErr["code"] != "MALFORMED_FRAME" {
			t.Fatalf("%s: protocol error = %#v", frame, protocolErr)
		}
	}

	sendCommand(t, inW, "quit")
	if err := <-done; err != nil {
		t.Fatalf("quit: %v", err)
	}
}

// A command that dies before it can answer leaves the app holding its mutation
// slot. The rejection must name the request it belongs to, or the row that sent
// it spins until the host's confirm timeout and reports "did not confirm in
// time" instead of the engine's reason.
func TestFailedCommandNamesItsRequest(t *testing.T) {
	server := &Server{serviceMode: true, Out: &strings.Builder{}}
	server.answerFailedCommand(errors.New("pool storage unavailable"), "req-42")
	line := server.Out.(*strings.Builder).String()
	trimmed := strings.TrimSuffix(line, "\n")
	if !strings.HasPrefix(trimmed, "protocol-error\t") {
		t.Fatalf("frame = %q, want a protocol-error", line)
	}
	var payload ProtocolError
	if err := json.Unmarshal(decodeFramePayload(t, trimmed), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "COMMAND_FAILED" || payload.RequestID != "req-42" {
		t.Fatalf("payload = %+v", payload)
	}

	// A malformed command has no request id to report: an empty id is omitted
	// rather than sent as a value the host would match against nothing.
	server = &Server{serviceMode: true, Out: &strings.Builder{}}
	server.answerFailedCommand(errors.New("bad frame"), "")
	if strings.Contains(server.Out.(*strings.Builder).String(), "requestId") {
		t.Fatalf("empty request id leaked into the frame: %q", server.Out.(*strings.Builder).String())
	}
}

// RequestID reads the id a command carries, so a failure can be attributed to
// the row that sent it. Payloads without one, or without readable JSON, must
// stay empty instead of inventing a target.
func TestCommandRequestIDIsProbedFromThePayload(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"mutate", Command{Kind: "mutate", Payload: json.RawMessage(`{"version":1,"requestId":"r1","operation":"hub-rotation"}`)}, "r1"},
		{"invoke", Command{Kind: "invoke", Payload: json.RawMessage(`{"requestId":"r2"}`)}, "r2"},
		{"no payload", Command{Kind: "refresh"}, ""},
		{"unreadable payload", Command{Kind: "mutate", Payload: json.RawMessage(`{`)}, ""},
		{"no request id", Command{Kind: "logs", Payload: json.RawMessage(`{"providerId":"hub"}`)}, ""},
	}
	for _, tc := range cases {
		if got := tc.cmd.RequestID(); got != tc.want {
			t.Errorf("%s: RequestID() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// lastResultFrame decodes the terminal result a buffer currently holds. Frames
// are base64, so a test cannot look for a code in the raw text.
func lastResultFrame(t *testing.T, out string) Result {
	t.Helper()
	var result Result
	for _, line := range strings.Split(out, "\n") {
		raw, ok := strings.CutPrefix(line, "result\t")
		if !ok {
			continue
		}
		if err := json.Unmarshal(decodeFramePayload(t, "result\t"+raw), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

// A rejected command must still name the request it refused: an unattributed
// rejection leaves the row that sent it spinning to the host's confirm timeout.
func TestRejectedFramesNameTheirRequest(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(
		`{"version":1,"requestId":"req-77","operation":"hub-rotation"}`))
	if got := RequestIDFromLine("mutate\t" + payload); got != "req-77" {
		t.Fatalf("mutate requestId = %q", got)
	}
	if got := RequestIDFromLine("invoke\t" + payload); got != "req-77" {
		t.Fatalf("invoke requestId = %q", got)
	}
	// Output frames too, so a recovery path can tell what it already answered.
	if got := RequestIDFromLine("result\t" + payload); got != "req-77" {
		t.Fatalf("result frame requestId = %q", got)
	}
	for _, line := range []string{
		"refresh",             // no payload at all
		"mutate\tnot-base64",  // unreadable
		"unknown\t" + payload, // a frame that never carries an id
		"mutate\t" + base64.StdEncoding.EncodeToString([]byte(`[]`)), // not an object
	} {
		if got := RequestIDFromLine(line); got != "" {
			t.Errorf("RequestIDFromLine(%q) = %q, want empty", line, got)
		}
	}
}

// An unencodable v1 frame must not become an empty body: a strict host treats
// that as a protocol failure and stops the service.
func TestUnencodableV1FrameIsAnAnswerNotAnEmptyBody(t *testing.T) {
	line := frame("result", make(chan int))
	if !strings.HasPrefix(line, "protocol-error\t") {
		t.Fatalf("frame = %q, want a protocol-error", line)
	}
	var payload ProtocolError
	if err := json.Unmarshal(decodeFramePayload(t, line), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "FRAME_ENCODE_FAILED" {
		t.Fatalf("Code = %q", payload.Code)
	}
}

// The answered set is what keeps a recovery path from answering twice and a
// retried command from running twice. It is bounded, so a long session cannot
// grow it without limit.
func TestAnsweredSetIsBoundedAndIdempotent(t *testing.T) {
	server := &Server{}
	if server.markAnswered("a") {
		t.Fatal("first answer reported as duplicate")
	}
	if !server.markAnswered("a") {
		t.Fatal("second answer not reported as duplicate")
	}
	if server.markAnswered("") {
		t.Fatal("an empty request id must never count as answered")
	}
	for i := 0; i < maxAnsweredRequests+10; i++ {
		server.markAnswered(fmt.Sprintf("req-%d", i))
	}
	if len(server.answered) > maxAnsweredRequests {
		t.Fatalf("answered set grew to %d", len(server.answered))
	}
	if len(server.answeredOrder) > maxAnsweredRequests {
		t.Fatalf("answered order grew to %d", len(server.answeredOrder))
	}
}

// A panic inside a synchronous command is answered for the request it was
// running, and the loop stays alive for the next command.
func TestSynchronousCommandPanicIsAttributed(t *testing.T) {
	out := &strings.Builder{}
	server := &Server{serviceMode: true, Out: out}
	// A composition without a proxy service makes handleLogs dereference nil:
	// exactly the provider-adapter panic this containment exists for.
	payload := json.RawMessage(`{"requestId":"req-panic","providerId":"gemini","name":"work"}`)
	err := server.handleSafely(context.Background(), &Command{Kind: "logs", Payload: payload})
	if err != nil {
		t.Fatalf("handleSafely returned %v; a contained panic is not an error", err)
	}
	line := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(line, "result\t") {
		t.Fatalf("frame = %q, want a result for the panicking request", line)
	}
	var result Result
	if err := json.Unmarshal(decodeFramePayload(t, line), &result); err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "req-panic" || result.Code != "INTERNAL_PANIC" || !result.Mutated {
		t.Fatalf("result = %+v", result)
	}
}

// A handler that never returns must still produce a terminal frame: the host
// holds its mutation slot on that answer, and nothing else will arrive.
func TestRunBoundedAnswersWhenTheHandlerHangs(t *testing.T) {
	out := &syncedBuffer{}
	srv := &Server{serviceMode: true, Out: out, actionTimeout: 50 * time.Millisecond}
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runBounded(context.Background(), "req-hang", "hub-start", true, func(context.Context) error {
			<-release
			return nil
		})
	}()
	<-done

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line := strings.TrimSpace(out.String())
		if strings.HasPrefix(line, "result\t") {
			var result Result
			if err := json.Unmarshal(decodeFramePayload(t, line), &result); err != nil {
				t.Fatal(err)
			}
			if result.RequestID != "req-hang" || result.Code != "ACTION_TIMEOUT" || !result.Mutated {
				t.Fatalf("result = %+v", result)
			}
			close(release)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	t.Fatalf("no terminal frame for a hanging handler; out=%q", out.String())
}

// An unencodable v1 frame must not become an empty body: a strict host treats
// that as a protocol failure and stops the service.

// The answered set is what keeps a recovery path from answering twice and a
// retried command from running twice. It is bounded, so a long session cannot
// grow it without limit.

// A panic inside a synchronous command is answered for the request it was
// running, and the loop stays alive for the next command.

// A handler that never returns must still produce a terminal frame: the host
// holds its mutation slot on that answer, and nothing else will arrive.

// The watchdog stays quiet when the handler answered in time: two terminal
// frames for one request would show an error over a success.
func TestRunBoundedDoesNotAnswerTwice(t *testing.T) {
	out := &syncedBuffer{}
	srv := &Server{serviceMode: true, Out: out, actionTimeout: 50 * time.Millisecond}
	srv.runBounded(context.Background(), "req-fast", "hub-start", true, func(context.Context) error {
		srv.write(FrameResult(Result{Version: ProtocolVersion, RequestID: "req-fast", Status: "success"}))
		return nil
	})
	time.Sleep(200 * time.Millisecond)
	if got := strings.Count(out.String(), "result\t"); got != 1 {
		t.Fatalf("terminal frames = %d, want exactly 1; out=%q", got, out.String())
	}
}

// The dispatch reservation must not look like an answer: a mutate that hangs
// still has to be answered by the watchdog, and a mutate that panics still has
// to be answered by the recovery. Both paths were silent because the duplicate
// guard had already marked the request as answered.
func TestReservedMutateIsStillAnswered(t *testing.T) {
	out := &syncedBuffer{}
	srv := &Server{serviceMode: true, Out: out, actionTimeout: 40 * time.Millisecond}
	payload := json.RawMessage(`{"version":1,"requestId":"req-reserved","operation":"x"}`)

	// A dispatch reserves the id, then the handler panics: one attributed frame.
	srv.reserveRequest("req-reserved")
	if err := srv.handleSafely(context.Background(), &Command{Kind: "logs", Payload: json.RawMessage(
		`{"requestId":"req-reserved","providerId":"gemini","name":"work"}`)}); err != nil {
		t.Fatalf("handleSafely: %v", err)
	}
	if result := lastResultFrame(t, out.String()); result.Code != "INTERNAL_PANIC" ||
		result.RequestID != "req-reserved" {
		t.Fatalf("a panicking reserved command was not answered; result=%+v", result)
	}

	// A second request that hangs is answered by the watchdog.
	out = &syncedBuffer{}
	srv = &Server{serviceMode: true, Out: out, actionTimeout: 40 * time.Millisecond}
	srv.reserveRequest("req-hang")
	release := make(chan struct{})
	go srv.runBounded(context.Background(), "req-hang", "hub-start", true, func(context.Context) error {
		<-release
		return nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && lastResultFrame(t, out.String()).Code != "ACTION_TIMEOUT" {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	if result := lastResultFrame(t, out.String()); result.Code != "ACTION_TIMEOUT" ||
		result.RequestID != "req-hang" {
		t.Fatalf("a hanging reserved command was not answered; result=%+v", result)
	}
	_ = payload
}

// A handler that answers after its watchdog must not add a second terminal
// frame: the host would show a success it has already reported as a timeout.
func TestLateHandlerAnswerIsDropped(t *testing.T) {
	out := &syncedBuffer{}
	srv := &Server{serviceMode: true, Out: out, actionTimeout: 40 * time.Millisecond}
	release := make(chan struct{})
	go srv.runBounded(context.Background(), "req-late", "hub-start", true, func(context.Context) error {
		<-release
		srv.write(FrameResult(Result{
			Version: ProtocolVersion, RequestID: "req-late", Status: "success"}))
		return nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && lastResultFrame(t, out.String()).Code != "ACTION_TIMEOUT" {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	time.Sleep(200 * time.Millisecond)
	if got := strings.Count(out.String(), "result\t"); got != 1 {
		t.Fatalf("terminal frames = %d, want exactly 1; out=%q", got, out.String())
	}
	if result := lastResultFrame(t, out.String()); result.Code != "ACTION_TIMEOUT" {
		t.Fatalf("the watchdog frame is missing; result=%+v", result)
	}
}

// A refused cancel names the request it cancels, not itself: attributing the
// rejection to that id would settle a row whose action is still running.
func TestRefusedCancelIsNotAttributedToItsTarget(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"version":1,"requestId":"target-1"}`))
	if got := RequestIDFromLine("cancel\t" + encoded); got != "" {
		t.Fatalf("a refused cancel was attributed to %q", got)
	}
}

// An oversized output frame is exactly where attribution matters most, so the
// replacement must still name its request.
func TestOversizedFrameKeepsItsRequest(t *testing.T) {
	out := &syncedBuffer{}
	srv := &Server{serviceMode: true, Out: out}
	padding := strings.Repeat("x", MaxServiceFrameBytes+1024)
	srv.write(FrameResult(Result{
		Version: ProtocolVersion, RequestID: "req-big", Status: "success", Payload: json.RawMessage(
			`{"text":"` + padding + `"}`)}))
	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, "protocol-error\t") {
		t.Fatalf("frame = %q, want a protocol-error", line[:60])
	}
	var payload ProtocolError
	if err := json.Unmarshal(decodeFramePayload(t, line), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "FRAME_TOO_LARGE" || payload.RequestID != "req-big" {
		t.Fatalf("payload = %+v", payload)
	}
}

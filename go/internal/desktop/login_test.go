package desktop

// Desktop protocol regressions for account-login: provider-owned browser
// sign-in runs off the main loop, publishes exactly one terminal result,
// honours cancellation frames, and rejects malformed payloads.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncedBuffer lets tests poll protocol output while server goroutines write;
// a bare bytes.Buffer read concurrently with WriteString is a data race.
type syncedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForResult(t *testing.T, out *syncedBuffer) Result {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var res Result
		for _, line := range strings.Split(out.String(), "\n") {
			raw, ok := strings.CutPrefix(line, "result\t")
			if !ok {
				continue
			}
			decoded, derr := base64.StdEncoding.DecodeString(raw)
			if derr != nil {
				continue
			}
			if json.Unmarshal(decoded, &res) == nil && res.RequestID != "" {
				return res
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no terminal result within deadline; out=%q", out.String())
	return Result{}
}

func TestDesktopAccountLoginEndToEnd(t *testing.T) {
	env := newTrayEnv(t)
	authPath := filepath.Join(env.Home, ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", authPath)
	launcher := filepath.Join(t.TempDir(), "grok.sh")
	body := "#!/bin/sh\nmkdir -p \"$(dirname " + authPath + ")\"\n" +
		`printf '%s' '{"sessions":{"s1":{"email":"tray@example.com","access_token":"tok"}}}' > ` + authPath + "\n"
	if err := os.WriteFile(launcher, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_BINARY", launcher)

	var out syncedBuffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	payload, _ := json.Marshal(Mutate{
		Version: 1, RequestID: "login-1", Operation: "account-login",
		ProviderID: "grok", MethodID: "grok-subscription", Name: "from-tray",
	})
	if err := srv.handle(context.Background(), &Command{Kind: "mutate", Payload: payload}); err != nil {
		t.Fatal(err)
	}

	res := waitForResult(t, &out)
	if res.Status != "success" || !strings.Contains(res.Message, "Signed in with Grok subscription") {
		t.Fatalf("result = %+v", res)
	}
	acct, gerr := env.Deps.Store.Get("grok", "from-tray")
	if gerr != nil || acct == nil {
		t.Fatalf("account not saved by desktop login: %v", gerr)
	}
	active, aerr := env.Deps.Store.GetActive("grok")
	if aerr != nil || active != "from-tray" {
		t.Fatalf("active = %q (%v)", active, aerr)
	}
}

func TestDesktopAccountLoginCancelled(t *testing.T) {
	env := newTrayEnv(t)
	authPath := filepath.Join(env.Home, ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", authPath)
	launcher := filepath.Join(t.TempDir(), "grok-slow.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_BINARY", launcher)

	in, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var out syncedBuffer
	srv := New(env.Deps, in, &out)

	go func() { _ = srv.Run(context.Background()) }()
	defer func() { _ = inWrite.Close() }()

	send := func(frame string) {
		t.Helper()
		if _, err := inWrite.WriteString(frame + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	mutatePayload := `{"version":1,"requestId":"login-cancel","operation":"account-login","providerId":"grok","methodId":"grok-subscription"}`
	cancelPayload, _ := json.Marshal(cancelPayload{Version: 1, RequestID: "login-cancel"})
	enc := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	send("mutate\t" + enc(mutatePayload))
	time.Sleep(300 * time.Millisecond)
	send("cancel\t" + enc(string(cancelPayload)))

	res := waitForResult(t, &out)
	if res.Status != "cancelled" {
		t.Fatalf("want cancelled, got %+v", res)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatal("cancelled login must not leave credential material behind")
	}
}

func TestDesktopAccountLoginValidation(t *testing.T) {
	// Extra unknown fields must be rejected before reaching the handler
	// (mirrors the TS isTray*Command key-set guard).
	fields := map[string]json.RawMessage{
		"version":    json.RawMessage(`1`),
		"requestId":  json.RawMessage(`"v1"`),
		"operation":  json.RawMessage(`"account-login"`),
		"providerId": json.RawMessage(`"grok"`),
		"methodId":   json.RawMessage(`"grok-subscription"`),
		"bogus":      json.RawMessage(`true`),
	}
	if err := validateMutate(fields); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

// waitForResultFor waits until a result frame with exactly this requestId
// appears (earlier frames in the buffer are skipped).
func waitForResultFor(t *testing.T, out *syncedBuffer, requestID string) Result {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			raw, ok := strings.CutPrefix(line, "result\t")
			if !ok {
				continue
			}
			decoded, derr := base64.StdEncoding.DecodeString(raw)
			if derr != nil {
				continue
			}
			var res Result
			if json.Unmarshal(decoded, &res) == nil && res.RequestID == requestID {
				return res
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no terminal result %q within deadline; out=%q", requestID, out.String())
	return Result{}
}

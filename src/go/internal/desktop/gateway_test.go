package desktop

// The Add Gateway sheet probes supplied credentials before anything is
// saved, and Account Detail re-probes the stored ones (C7). Only statuses
// cross the boundary — the key never appears in output.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/profile"
)

func gatewayTestServer(t *testing.T, status int, seenAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seenAuth != nil {
			*seenAuth = r.Header.Get("authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
}

func runGatewayTest(t *testing.T, env *trayEnv, m Mutate) Result {
	t.Helper()
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	raw, _ := json.Marshal(m)
	if err := srv.handleMutate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var res Result
	if err := json.Unmarshal(decodeFramePayload(t, firstFrameLine(out.String())), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestGatewayTestProbesSuppliedCredentials(t *testing.T) {
	env := newTrayEnv(t)

	var auth string
	ok := gatewayTestServer(t, http.StatusOK, &auth)
	defer ok.Close()

	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt1", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: ok.URL, APIKey: "sk-live",
	})
	if res.Status != "success" {
		t.Fatalf("probe ok: %+v", res)
	}
	if auth != "Bearer sk-live" {
		t.Fatalf("authorization header = %q", auth)
	}

	// An auth rejection fails the test with the status, never the key (C7).
	denied := gatewayTestServer(t, http.StatusUnauthorized, nil)
	defer denied.Close()
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt2", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: denied.URL, APIKey: "sk-dead",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "401") {
		t.Fatalf("probe denied: %+v", res)
	}
	if strings.Contains(res.Message, "sk-dead") {
		t.Fatalf("key crossed the output boundary: %+v", res)
	}

	// Nothing listening is unreachable, not a silent success.
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt3", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: "http://127.0.0.1:9", APIKey: "sk-live",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "unreachable") {
		t.Fatalf("probe unreachable: %+v", res)
	}
}

// A saved gateway re-probes with its stored endpoint and key (C7): the
// Account Detail Test button uses this instead of a catalog refresh.
func TestGatewayTestUsesStoredProfile(t *testing.T) {
	env := newTrayEnv(t)

	var auth string
	ok := gatewayTestServer(t, http.StatusOK, &auth)
	defer ok.Close()

	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "gw1", Provider: "custom", Endpoint: ok.URL},
		Secrets: profile.Secrets{APIKey: "sk-stored"},
	}); err != nil {
		t.Fatal(err)
	}
	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt4", Operation: "gateway-test",
		Name: "gw1", ProviderID: "custom",
	})
	if res.Status != "success" {
		t.Fatalf("stored probe: %+v", res)
	}
	if auth != "Bearer sk-stored" {
		t.Fatalf("stored authorization header = %q", auth)
	}
}

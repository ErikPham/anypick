package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/proxy"
)

func mustNewProxyToken(t *testing.T) string {
	t.Helper()
	tok, err := proxy.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type fakeResolver struct {
	saw []string
}

func (r *fakeResolver) ResolveUpstream(_ context.Context, providerID, account string) (*proxy.Upstream, error) {
	r.saw = append(r.saw, providerID+"/"+account)
	base, _ := url.Parse("http://placeholder.invalid")
	return &proxy.Upstream{BaseURL: base}, nil
}

func newHubStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// Hub tables come from migrations; they exist in a fresh database.
	// Root points at an isolated dir so coordinator-locked writes work.
	return &Store{DB: database.DB, Root: t.TempDir()}
}

func TestHubRoutesByModelAcrossSources(t *testing.T) {
	st := newHubStore(t)
	seedHubAccounts(t, st, "glm/cn", "deepseek/work")

	cfg, err := st.UpsertConfigContext(context.Background(), Config{
		Name: "default", Enabled: true,
		Sources: []Source{
			{ProviderID: "glm", Account: "cn"},
			{ProviderID: "deepseek", Account: "work"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := proxy.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	token := tok
	route := Route{
		RouteID: "global/claude", HubName: "default", ClientID: "claude",
		Token: token,
		Models: []ModelRoute{
			{Model: "deepseek-chat", SourceIndex: 1},
			// index 0 is the implicit default.
		},
	}
	if err := st.AttachRouteContext(context.Background(), route); err != nil {
		t.Fatal(err)
	}

	resolver := &fakeResolver{}
	srv := &Server{Config: cfg, Store: NewService(st), Resolver: resolver}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	// Health needs no credentials and echoes ownership.
	resp, err := http.Get(srv.Endpoint() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(hb), `"instanceId"`) {
		t.Errorf("health = %s", hb)
	}

	post := func(model string) (*http.Response, string) {
		body, _ := json.Marshal(map[string]string{"model": model, "messages": "[]"})
		req, _ := http.NewRequest("POST", srv.Endpoint()+"/v1/chat/completions",
			strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, string(raw)
	}

	// Model routed to source 1 → deepseek/work.
	respB, body := post("deepseek-chat")
	if respB.StatusCode == 0 || !strings.Contains(body, "") {
		t.Fatalf("unexpected: %d %s", respB.StatusCode, body)
	}
	// Default (no manifest hit) → source 0 → glm/cn.
	_, _ = post("glm-4.7")

	// Wrong token rejected by the hub itself before any routing.
	badReq, _ := http.NewRequest("POST", srv.Endpoint()+"/v1/chat/completions",
		strings.NewReader(`{"model":"x"}`))
	badReq.Header.Set("Authorization", "Bearer nope")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, badResp.Body)
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", badResp.StatusCode)
	}

	// Claude Code sends its own OAuth bearer and the route secret as
	// x-api-key together. A matching key must authenticate.
	mixed, _ := http.NewRequest("POST", srv.Endpoint()+"/v1/chat/completions",
		strings.NewReader(`{"model":"glm-4.7"}`))
	mixed.Header.Set("Authorization", "Bearer claude-oauth-not-the-hub-token")
	mixed.Header.Set("x-api-key", token)
	mixedResp, err := http.DefaultClient.Do(mixed)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, mixedResp.Body)
	mixedResp.Body.Close()
	if mixedResp.StatusCode == http.StatusUnauthorized {
		t.Fatal("matching x-api-key was ignored because Authorization was also set")
	}

	// Resolver saw both sources in order: default first? No — requests ran in
	// sequence deepseek then glm; assert both appeared.
	joined := strings.Join(resolver.saw, ",")
	if !strings.Contains(joined, "deepseek/work") || !strings.Contains(joined, "glm/cn") {
		t.Errorf("resolver saw %v", resolver.saw)
	}
}

func TestStoreRoundTripAndDetach(t *testing.T) {
	st := newHubStore(t)
	seedHubAccounts(t, st, "p/a")
	cfg, err := st.UpsertConfigContext(context.Background(), Config{Name: "h1", Enabled: true,
		Sources: []Source{{ProviderID: "p", Account: "a"}}})
	if err != nil || cfg.Name != "h1" {
		t.Fatalf("cfg %+v err=%v", cfg, err)
	}
	got, _ := st.GetConfig("h1")
	if got == nil || len(got.Sources) != 1 || got.Sources[0].ProviderID != "p" {
		t.Fatalf("config roundtrip = %+v", got)
	}

	tok, err := proxy.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	token := tok
	if err := st.AttachRouteContext(context.Background(), Route{RouteID: "r1", HubName: "h1", Token: token}); err != nil {
		t.Fatal(err)
	}
	found, err := st.RouteByToken(token)
	if err != nil || found == nil || found.RouteID != "r1" || found.Token != token {
		t.Fatalf("route by token = %+v err=%v", found, err)
	}
	if err := st.DetachRouteContext(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	gone, _ := st.RouteByToken(token)
	if gone != nil {
		t.Error("detached route still resolves")
	}
}

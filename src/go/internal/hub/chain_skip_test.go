package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/proxy"
)

// scriptedResolver fails named sources and serves the rest from a mock.
type scriptedResolver struct {
	fail map[string]error
	base *url.URL
	saw  []string
}

func (r *scriptedResolver) ResolveUpstream(_ context.Context, providerID, account string) (*proxy.Upstream, error) {
	r.saw = append(r.saw, providerID+"/"+account)
	if err, ok := r.fail[providerID+"/"+account]; ok {
		return nil, err
	}
	return &proxy.Upstream{BaseURL: r.base, Auth: "tok"}, nil
}

// An unresolvable member must not poison the turn: the chain skips it and a
// healthy candidate serves. Failover only covers quota refusals, so without
// the skip one deleted account fails every request routed through the hub.
func TestServeChainSkipsUnresolvableSource(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)

	st := newHubStore(t)
	if _, err := st.UpsertConfigContext(context.Background(), Config{Name: "h", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	token := mustNewProxyToken(t)
	if err := st.AttachRouteContext(context.Background(), Route{RouteID: "r", HubName: "h", ClientID: "dsh", Token: token}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "h", Enabled: true, Sources: []Source{
		{ProviderID: "workbuddy", Account: "ghost"},
		{ProviderID: "workbuddy", Account: "live"},
	}}
	resolver := &scriptedResolver{fail: map[string]error{"workbuddy/ghost": errors.New("no snapshot for workbuddy/ghost")}, base: base}
	srv := &Server{Config: cfg, Store: NewService(st), Resolver: resolver}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.serveRoute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (want the healthy member to serve)", rec.Code, rec.Body.String())
	}
	if len(resolver.saw) != 2 || resolver.saw[0] != "workbuddy/ghost" || resolver.saw[1] != "workbuddy/live" {
		t.Fatalf("attempt order = %v, want ghost then live", resolver.saw)
	}
}

// When every candidate fails to resolve, the 502 names the dead sources so
// the client (and the operator) sees which hub entries rotted.
func TestServeChainNamesDeadSourcesWhenAllUnresolvable(t *testing.T) {
	st := newHubStore(t)
	if _, err := st.UpsertConfigContext(context.Background(), Config{Name: "h", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	token := mustNewProxyToken(t)
	if err := st.AttachRouteContext(context.Background(), Route{RouteID: "r", HubName: "h", ClientID: "dsh", Token: token}); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://placeholder.invalid")
	cfg := Config{Name: "h", Enabled: true, Sources: []Source{
		{ProviderID: "workbuddy", Account: "ghost"},
	}}
	srv := &Server{Config: cfg, Store: NewService(st),
		Resolver: &scriptedResolver{fail: map[string]error{"workbuddy/ghost": errors.New("no snapshot for workbuddy/ghost")}, base: base}}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.serveRoute(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "workbuddy/ghost") {
		t.Fatalf("body must name the dead source: %s", rec.Body.String())
	}
}

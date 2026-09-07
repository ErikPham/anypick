package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/proxy"
)

func TestParseIndex(t *testing.T) {
	cases := map[string]int{
		"":    -1,
		"#":   -1,
		"#0":  0,
		"#7":  7,
		"#42": 42,
		"a":   -1,
		"#a":  -1,
		"#1x": -1,
	}
	for in, want := range cases {
		if got := parseIndex(in); got != want {
			t.Errorf("parseIndex(%q)=%d want %d", in, got, want)
		}
	}
}

func TestDefaultSourceIndex(t *testing.T) {
	if got := defaultSourceIndex(&Config{}); got != -1 {
		t.Errorf("empty sources = %d want -1", got)
	}
	if got := defaultSourceIndex(&Config{Sources: []Source{{ProviderID: "p", Account: "a"}}}); got != 0 {
		t.Errorf("one source = %d want 0", got)
	}
}

func TestRewriteModel(t *testing.T) {
	route := &Route{Models: []ModelRoute{
		{Model: "alias", SourceIndex: 1, UpstreamModel: "real-model"},
	}}

	got := string(rewriteModel([]byte(`{"model":"alias","messages":[]}`), route, "alias"))
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("rewritten body not json: %v", got)
	}
	if doc["model"] != "real-model" {
		t.Fatalf("model = %v want real-model", doc["model"])
	}

	// No manifest hit → unchanged.
	if same := string(rewriteModel([]byte(`{"model":"other"}`), route, "other")); same != `{"model":"other"}` {
		t.Fatalf("unmatched model changed body: %s", same)
	}
	// Identity mapping → unchanged.
	identity := &Route{Models: []ModelRoute{{Model: "m", SourceIndex: 0, UpstreamModel: "m"}}}
	if same := string(rewriteModel([]byte(`{"model":"m"}`), identity, "m")); same != `{"model":"m"}` {
		t.Fatalf("identity mapping changed body: %s", same)
	}
	// Corrupt body with a mapping → original bytes returned.
	corrupt := `{"model":"alias"`
	if same := string(rewriteModel([]byte(corrupt), route, "alias")); same != corrupt {
		t.Fatalf("corrupt body rewritten: %s", same)
	}
}

func TestRouteModelEntry(t *testing.T) {
	route := &Route{Models: []ModelRoute{
		{Model: "deepseek-chat", SourceIndex: 1},
		{Model: "alias", SourceIndex: 0, UpstreamModel: "real-model"},
	}}
	entry, ok := routeModelEntry(route, "deepseek-chat")
	if !ok || entry.SourceIndex != 1 {
		t.Fatalf("entry=%+v ok=%v want SourceIndex 1", entry, ok)
	}
	if entry, ok := routeModelEntry(route, "alias"); !ok || entry.UpstreamModel != "real-model" {
		t.Fatalf("entry=%+v ok=%v want UpstreamModel real-model", entry, ok)
	}
	if _, ok := routeModelEntry(route, "missing"); ok {
		t.Fatal("want miss")
	}
	// Duplicate pins are a data error, but they must resolve the same way for
	// every caller: first match, both halves.
	dupes := &Route{Models: []ModelRoute{
		{Model: "m", SourceIndex: 0},
		{Model: "m", SourceIndex: 1, UpstreamModel: "other"},
	}}
	if entry, ok := routeModelEntry(dupes, "m"); !ok || entry.SourceIndex != 0 || entry.UpstreamModel != "" {
		t.Fatalf("duplicate pins resolved to %+v, want the first entry", entry)
	}
}

func TestWriteErr(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusConflict, "PROXY_DISABLED", "hub is disabled")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%q", ct)
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if payload.Error.Type != "PROXY_DISABLED" || payload.Error.Message != "hub is disabled" {
		t.Fatalf("payload %+v", payload)
	}
}

func TestPresentedToken(t *testing.T) {
	mk := func(headers map[string]string) *http.Request {
		req := httptest.NewRequest("POST", "/", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req
	}
	if got := presentedToken(mk(map[string]string{"Authorization": "Bearer tok "})); got != "tok" {
		t.Fatalf("bearer = %q", got)
	}
	// Lowercase scheme also matches.
	if got := presentedToken(mk(map[string]string{"Authorization": "bearer tok2"})); got != "tok2" {
		t.Fatalf("lowercase bearer = %q", got)
	}
	if got := presentedToken(mk(map[string]string{"x-api-key": " key3 "})); got != "key3" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := presentedToken(mk(nil)); got != "" {
		t.Fatalf("no headers = %q", got)
	}
}

type errResolver struct{ err error }

func (r *errResolver) ResolveUpstream(context.Context, string, string) (*proxy.Upstream, error) {
	return nil, r.err
}

// seedLegacyRoute inserts a proxy_hub_routes row with an explicit secret,
// bypassing AttachRoute's hashing, to exercise legacy lookup paths.
func seedLegacyRoute(t *testing.T, st *Store, routeID, secret string) {
	t.Helper()
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at) VALUES ('legacy', '{}', 't', 't')
		 ON CONFLICT(name) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Route{RouteID: routeID, HubName: "legacy", ClientID: "claude"})
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hub_routes (route_id, hub_name, manifest_json, token_secret, created_at, updated_at)
		 VALUES (?, 'legacy', ?, ?, 't', 't')`, routeID, string(manifest), secret); err != nil {
		t.Fatal(err)
	}
}

func TestRouteByTokenMigratesLegacySHA256(t *testing.T) {
	st := newHubStore(t)
	token := proxy.MustNewToken()
	seedLegacyRoute(t, st, "r-sha", legacySHA256(token))

	found, err := st.RouteByToken(token)
	if err != nil || found == nil || found.RouteID != "r-sha" || found.Token != token {
		t.Fatalf("found=%+v err=%v", found, err)
	}

	// The row is untouched by the read; migration is an explicit write.
	var stored string
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id='r-sha'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != legacySHA256(token) {
		t.Fatal("read path must not migrate the legacy row")
	}
	if err := st.MigrateRouteTokenContext(context.Background(), "r-sha", token); err != nil {
		t.Fatal(err)
	}
	hashed, herr := st.hashToken(context.Background(), token)
	if herr != nil {
		t.Fatal(herr)
	}
	var migrated string
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id='r-sha'`).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if migrated != hashed {
		t.Fatal("legacy row was not migrated to HMAC secret")
	}

	// Second lookup resolves through the fast HMAC path.
	again, err := st.RouteByToken(token)
	if err != nil || again == nil || again.RouteID != "r-sha" {
		t.Fatalf("hmac relookup=%+v err=%v", again, err)
	}
}

func TestRouteByTokenMigratesPlaintextRow(t *testing.T) {
	st := newHubStore(t)
	token := proxy.MustNewToken()
	seedLegacyRoute(t, st, "r-plain", token)

	found, err := st.RouteByToken(token)
	if err != nil || found == nil || found.RouteID != "r-plain" {
		t.Fatalf("found=%+v err=%v", found, err)
	}
	var stored string
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id='r-plain'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != token {
		t.Fatal("read path must not migrate the plaintext row")
	}
	if err := st.MigrateRouteTokenContext(context.Background(), "r-plain", token); err != nil {
		t.Fatal(err)
	}
	hashed, herr := st.hashToken(context.Background(), token)
	if herr != nil {
		t.Fatal(herr)
	}
	var migrated string
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id='r-plain'`).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if migrated != hashed {
		t.Fatal("plaintext row was not migrated to HMAC secret")
	}
}

func TestRouteByTokenUnknownAndCorruptManifest(t *testing.T) {
	st := newHubStore(t)

	// Unknown token → (nil, nil).
	if r, err := st.RouteByToken(proxy.MustNewToken()); r != nil || err != nil {
		t.Fatalf("unknown token = %+v, %v", r, err)
	}

	// Corrupt manifest behind the HMAC secret surfaces as STATE_CONFLICT.
	token := proxy.MustNewToken()
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at) VALUES ('legacy', '{}', 't', 't')
		 ON CONFLICT(name) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hub_routes (route_id, hub_name, manifest_json, token_secret, created_at, updated_at)
		 VALUES ('r-bad', 'legacy', '{not-json', ?, 't', 't')`, legacySHA256(token)); err != nil {
		t.Fatal(err)
	}
	hashed, herr := st.hashToken(context.Background(), token)
	if herr != nil {
		t.Fatal(herr)
	}
	if _, err := st.DB.Exec(`UPDATE proxy_hub_routes SET token_secret=? WHERE route_id='r-bad'`, hashed); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RouteByToken(token); err == nil ||
		!strings.Contains(err.Error(), "corrupt route manifest") {
		t.Fatalf("want corrupt manifest error, got %v", err)
	}
}

func TestServeRouteDisabledNoSourceUpstreamError(t *testing.T) {
	st := newHubStore(t)
	token := proxy.MustNewToken()
	if _, err := st.UpsertConfigContext(context.Background(), Config{Name: "d", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachRouteContext(context.Background(), Route{RouteID: "global/x", HubName: "d", ClientID: "codex", Token: token}); err != nil {
		t.Fatal(err)
	}

	post := func(cfg Config, model string) *httptest.ResponseRecorder {
		srv := &Server{Config: cfg, Store: st, Resolver: &fakeResolver{}}
		body := `{"model":"` + model + `"}` + "\n"
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.serveRoute(rec, req)
		return rec
	}

	// Disabled hub → 409 before any routing.
	rec := post(Config{Name: "d", Enabled: false, Sources: []Source{{ProviderID: "glm", Account: "cn"}}}, "m")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PROXY_DISABLED") {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body.String())
	}

	// Enabled but zero sources → 404 NO_SOURCE.
	rec = post(Config{Name: "d", Enabled: true}, "m")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NO_SOURCE") {
		t.Fatalf("no sources: %d %s", rec.Code, rec.Body.String())
	}

	// Resolver failure → 502 upstream_error.
	resolverSrv := &Server{
		Config:   Config{Name: "d", Enabled: true, Sources: []Source{{ProviderID: "glm", Account: "cn"}}},
		Store:    st,
		Resolver: &errResolver{err: errors.New("no credential")},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	resolverSrv.serveRoute(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "no credential") {
		t.Fatalf("upstream error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestServeRoutePinsSourceByModelIndex(t *testing.T) {
	st := newHubStore(t)
	cfg, err := st.UpsertConfigContext(context.Background(), Config{
		Name: "pin", Enabled: true,
		Sources: []Source{
			{ProviderID: "glm", Account: "cn"},
			{ProviderID: "deepseek", Account: "work"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := proxy.MustNewToken()
	if err := st.AttachRouteContext(context.Background(), Route{RouteID: "global/c", HubName: "pin", ClientID: "claude", Token: token,
		Models: []ModelRoute{{Model: "deepseek-chat", SourceIndex: 1}}}); err != nil {
		t.Fatal(err)
	}

	resolver := &fakeResolver{}
	srv := &Server{Config: cfg, Store: st, Resolver: resolver}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.serveRoute(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway {
		// The fake upstream is unreachable; what matters is which source was picked.
		t.Logf("upstream status %d", rec.Code)
	}
	if len(resolver.saw) != 1 || resolver.saw[0] != "deepseek/work" {
		t.Fatalf("resolver saw %v, want deepseek/work via model pin", resolver.saw)
	}
}

func TestStartRefusesNonLoopbackHost(t *testing.T) {
	srv := &Server{Config: Config{Name: "bad", Host: "0.0.0.0"}, Store: nil, Resolver: &fakeResolver{}}
	if err := srv.Start(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("want loopback refusal, got %v", err)
	}
}

func TestStopIdempotentWithoutStart(t *testing.T) {
	srv := &Server{}
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop without Start: %v", err)
	}
}

func TestUpsertConfigPreservesCreatedAt(t *testing.T) {
	st := newHubStore(t)
	first, err := st.UpsertConfigContext(context.Background(), Config{Name: "keep", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertConfigContext(context.Background(), Config{Name: "keep", Enabled: false,
		Sources: []Source{{ProviderID: "p", Account: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Fatalf("CreatedAt drifted: %q -> %q", first.CreatedAt, second.CreatedAt)
	}
	if second.Enabled {
		t.Fatal("update did not apply")
	}

	// Missing config reads as nil, not error.
	if got, err := st.GetConfig("nope"); got != nil || err != nil {
		t.Fatalf("missing config = %+v, %v", got, err)
	}
}

func TestGetConfigCorruptJSONIsStateConflict(t *testing.T) {
	st := newHubStore(t)
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at) VALUES ('broken', '{oops', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetConfig("broken"); err == nil ||
		!strings.Contains(err.Error(), "corrupt hub config") {
		t.Fatalf("want corrupt config error, got %v", err)
	}
}

func TestMigrateSkipsCurrentRowsAndRequiresRoot(t *testing.T) {
	st := newHubStore(t)
	token := proxy.MustNewToken()
	seedLegacyRoute(t, st, "r-cur", legacySHA256(token))
	if err := st.MigrateRouteTokenContext(context.Background(), "r-cur", token); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := st.DB.QueryRow(`SELECT updated_at FROM proxy_hub_routes WHERE route_id='r-cur'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	// Second migration must be a read-only no-op: no UPDATE, timestamp kept.
	if err := st.MigrateRouteTokenContext(context.Background(), "r-cur", token); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := st.DB.QueryRow(`SELECT updated_at FROM proxy_hub_routes WHERE route_id='r-cur'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("migration rewrote a current row")
	}

	// Without a data root the write fails closed instead of running
	// uncoordinated (invariant #5).
	rootless := &Store{DB: st.DB}
	if err := rootless.MigrateRouteTokenContext(context.Background(), "r-cur", token); err == nil {
		t.Fatal("rootless migration must fail closed")
	}
}

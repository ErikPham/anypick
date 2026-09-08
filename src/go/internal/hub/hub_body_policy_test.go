package hub

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The hub opens a request body for exactly two reasons: choosing the source by
// model, and remapping the model for the upstream. Both are answers to the
// same question — "what does this route say about this model?" — so both must
// come from one lookup. Reading the pin table twice is two chances to
// disagree, and with duplicate pins the halves did disagree: the source came
// from the first matching entry while the model remap came from the first
// entry that had one, so a request could reach one source carrying another
// source's model id.

// recordingStub is one fake vendor endpoint that keeps the last body it was
// sent, byte for byte.
type recordingStub struct {
	srv *httptest.Server

	mu   sync.Mutex
	hits int
	body string
}

func newRecordingStub(t *testing.T) *recordingStub {
	t.Helper()
	s := &recordingStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits++
		s.body = string(raw)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *recordingStub) lastBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

func (s *recordingStub) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *recordingStub) url(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// startPinHub boots a hub over two sources with the given pin table.
func startPinHub(t *testing.T, models []ModelRoute, token string) (*Server, *recordingStub, *recordingStub) {
	t.Helper()
	st := newHubStore(t)
	a := newRecordingStub(t)
	b := newRecordingStub(t)
	resolver := mapResolver{
		targets: map[string]*url.URL{"p/a": a.url(t), "p/b": b.url(t)},
		creds:   map[string]string{"p/a": "cred-a", "p/b": "cred-b"},
	}
	cfg, err := st.UpsertConfigContext(context.Background(), Config{
		Name: "pins", Enabled: true,
		Sources: []Source{{ProviderID: "p", Account: "a"}, {ProviderID: "p", Account: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	attachLifecycle(t, st, Route{
		RouteID: "pins/r", HubName: "pins", ClientID: "cli", Token: token, Models: models,
	})
	srv := &Server{Config: cfg, Store: NewService(st), Resolver: resolver}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, a, b
}

func postRaw(t *testing.T, endpoint, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// Duplicate pins for one model are a data error, but whichever entry wins has
// to win for both halves of the decision.
func TestHubDoesNotMixTwoPinsForOneModel(t *testing.T) {
	token := mustRouteToken(t)
	srv, a, b := startPinHub(t, []ModelRoute{
		{Model: "m", SourceIndex: 0},
		{Model: "m", SourceIndex: 1, UpstreamModel: "b-only"},
	}, token)

	resp, body := postModel(t, srv.Endpoint(), token, "m")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	if b.hitCount() != 0 {
		t.Fatalf("the second pin's source was contacted %d times; the source must come from the first pin", b.hitCount())
	}
	got := a.lastBody()
	if strings.Contains(got, "b-only") {
		t.Fatalf("source 0 was sent source 1's model id: %s", got)
	}
	if !strings.Contains(got, `"model":"m"`) {
		t.Fatalf("source 0 body = %s, want the requested model untouched", got)
	}
}

// A body the hub was not asked to change must reach the upstream as the bytes
// the client sent. The spacing below is deliberate: any re-encode through
// map[string]interface{} normalises it away.
func TestHubDoesNotRewriteWhenNoMapping(t *testing.T) {
	token := mustRouteToken(t)
	srv, a, _ := startPinHub(t, []ModelRoute{
		{Model: "other", SourceIndex: 1, UpstreamModel: "remapped"},
	}, token)

	const raw = `{"model": "plain", "messages": []}`
	resp, body := postRaw(t, srv.Endpoint(), token, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	if got := a.lastBody(); got != raw {
		t.Fatalf("body was rewritten with no mapping for it:\n got %s\nwant %s", got, raw)
	}
}

// A mapping that applies must still apply — the guard above must not have
// quietly disabled remapping altogether.
func TestHubStillRewritesWhenThePinSaysSo(t *testing.T) {
	token := mustRouteToken(t)
	srv, _, b := startPinHub(t, []ModelRoute{
		{Model: "alias", SourceIndex: 1, UpstreamModel: "real-model"},
	}, token)

	resp, body := postModel(t, srv.Endpoint(), token, "alias")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	got := b.lastBody()
	if !strings.Contains(got, `"model":"real-model"`) {
		t.Fatalf("pin did not remap the model: %s", got)
	}
}

// One lookup, enforced: every reader of the pin table in the request path must
// go through routeModelEntry, and nothing else may walk it.
func TestPinTableIsResolvedOnce(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			funcs[fn.Name.Name] = fn
		}
	}
	calls := func(fn *ast.FuncDecl, name string) bool {
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
					found = true
				}
			}
			return true
		})
		return found
	}
	rangesModels := func(fn *ast.FuncDecl) bool {
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			if rng, ok := n.(*ast.RangeStmt); ok {
				if sel, ok := rng.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Models" {
					found = true
				}
			}
			return true
		})
		return found
	}

	if _, ok := funcs["routeModelEntry"]; !ok {
		t.Fatal("routeModelEntry is gone; the pin lookup must have exactly one home")
	}
	for _, name := range []string{"routeSourceIndex", "rewriteModel"} {
		fn, ok := funcs[name]
		if !ok {
			continue
		}
		if !calls(fn, "routeModelEntry") {
			t.Errorf("%s does not resolve through routeModelEntry — it carries its own pass over the pin table", name)
		}
	}
	for name, fn := range funcs {
		if name == "routeModelEntry" {
			continue
		}
		if rangesModels(fn) {
			t.Errorf("%s walks route.Models itself; the pin lookup must have exactly one home", name)
		}
	}
}

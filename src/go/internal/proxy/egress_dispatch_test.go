package proxy

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The egress dispatch table decides which upstream surface speaks to which
// inbound request. It used to exist twice — once in Serve, once in the pool
// ring — and the two copies had already drifted. These tests pin the surviving
// copy from three directions: the table itself, the behaviour each drift
// broke, and an AST guard that fails if a second copy is ever reintroduced.

func TestEgressKindForCoversEveryUpstreamShape(t *testing.T) {
	messages := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		return r
	}
	chat := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	}
	get := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	}
	responses := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	}
	target := &UpstreamTarget{BaseURL: mustURLT(t, "https://vendor.test/anthropic")}
	base := mustURLT(t, "https://vendor.test/v1")

	cases := []struct {
		name string
		req  *http.Request
		up   *Upstream
		want egressKind
	}{
		{
			name: "gemini wins over everything, including a native target",
			req:  messages(),
			up:   &Upstream{BaseURL: base, TranslateGemini: true, Anthropic: target, TranslateAnthropic: true},
			want: kindGemini,
		},
		{
			name: "gemini also claims the openai surfaces",
			req:  chat(),
			up:   &Upstream{BaseURL: base, TranslateGemini: true, OpenAIPath: "/chat"},
			want: kindGemini,
		},
		{
			name: "native target wins over translation",
			req:  messages(),
			up:   &Upstream{BaseURL: base, Anthropic: target, TranslateAnthropic: true},
			want: kindNativeAnthropic,
		},
		{
			name: "translation when there is no native target",
			req:  messages(),
			up:   &Upstream{BaseURL: base, TranslateAnthropic: true},
			want: kindTranslatedMessages,
		},
		{
			name: "openai path replaces the chat surface",
			req:  chat(),
			up:   &Upstream{BaseURL: base, OpenAIPath: "/openai/chat"},
			want: kindOpenAIPath,
		},
		{
			name: "everything else is passthrough",
			req:  chat(),
			up:   &Upstream{BaseURL: base},
			want: kindPassthrough,
		},
		{
			name: "responses route gets kindOpenAIPath even without openai path",
			req:  responses(),
			up:   &Upstream{BaseURL: base},
			want: kindOpenAIPath,
		},
		{
			name: "gemini claims responses requests when translate gemini is set",
			req:  responses(),
			up:   &Upstream{BaseURL: base, TranslateGemini: true},
			want: kindGemini,
		},
		{
			name: "a non-messages request never reaches the anthropic branches",
			req:  get(),
			up:   &Upstream{BaseURL: base, Anthropic: target, TranslateAnthropic: true},
			want: kindPassthrough,
		},
		{
			name: "nil upstream cannot panic its way into a branch",
			req:  messages(),
			up:   nil,
			want: kindPassthrough,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := egressKindFor(tc.req, tc.up); got != tc.want {
				t.Fatalf("egressKindFor = %d, want %d", got, tc.want)
			}
		})
	}
}

// buildServer is the only place an Upstream becomes a Server, and every
// detached proxy child goes through it. A field it forgets does not fail — it
// just disappears from the egress decision, and the most dangerous one to lose
// is TranslateGemini: without it, kindGemini is never selected and
// /v1/chat/completions is forwarded verbatim to daily-cloudcode-pa, which
// answers an HTML 404. The proxy still starts, /health still passes, and no
// request ever reaches generateContent.
func TestBuildServerCarriesTheGeminiEgressFacts(t *testing.T) {
	base := mustURLT(t, "https://daily-cloudcode-pa.googleapis.com")
	up := &Upstream{
		BaseURL:            base,
		AuthHeader:         "Authorization",
		AuthValuePrefix:    "Bearer ",
		AuthProvider:       func(ctx context.Context) (string, error) { return "tok", nil },
		TranslateGemini:    true,
		Gemini:             &GeminiTarget{IDEType: "ANTIGRAVITY", UserAgent: "antigravity/hub/2.3.1 (aidev_client)"},
		TranslateAnthropic: true,
		Anthropic:          &UpstreamTarget{BaseURL: base},
		OpenAIPath:         "/chat/completions",
	}
	srv := (&Service{}).buildServer("gemini", "acct", mustNewInstanceID(t), "secret", up)
	got := srv.upstreamValue()
	if !got.TranslateGemini {
		t.Fatal("TranslateGemini lost in buildServer: egress would fall through to passthrough")
	}
	if got.Gemini != up.Gemini {
		t.Fatalf("Gemini target lost in buildServer: got %v", got.Gemini)
	}
	if got.Gemini.IDEType != "ANTIGRAVITY" || got.Gemini.UserAgent == "" {
		t.Fatalf("Gemini target lost its client identity: %+v", got.Gemini)
	}
	if srv.UpstreamAuthProvider == nil {
		t.Fatal("AuthProvider lost in buildServer: OAuth tokens would never refresh")
	}
	// The point of all of the above: dispatch must actually pick gemini.
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if kind := egressKindFor(chat, got); kind != kindGemini {
		t.Fatalf("egressKindFor = %d, want kindGemini (%d)", kind, kindGemini)
	}
}

// A pool member that hits the context limit must be converted exactly like a
// single account is: the client compacts on the canonical 400, and a raw
// vendor envelope does not carry that signal.
func TestPoolTranslatedMemberContextWindowIsConverted(t *testing.T) {
	up := newStubUpstream(t, 400)
	up.body = `{"error":{"message":"prompt is too long: 9000 tokens > 4096 maximum"}}`

	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
		mapResolver{"a": {BaseURL: up.url, Auth: "sk-a", TranslateAnthropic: true}},
		filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	resp := poolPost(t, server.Endpoint(), token, "/v1/messages",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"code":"context_length_exceeded"`) {
		t.Fatalf("pool returned the raw vendor envelope, not the compaction signal: %s", body)
	}
	if strings.Contains(string(body), "4096 maximum") && !strings.Contains(string(body), `"type":"error"`) {
		t.Fatalf("pool leaked the vendor shape: %s", body)
	}
}

// A pool member's native Anthropic target must be flattened by the same merge
// the single-account path uses, including the header default. The copy that
// used to live in the pool left the header name empty, which the vendor
// answers 401 — indistinguishable from a dead token.
func TestPoolNativeAnthropicMemberUsesTheSharedMerge(t *testing.T) {
	var gotHeader, gotValue string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = "Authorization"
		gotValue = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
	}))
	defer target.Close()
	targetURL := mustURLT(t, target.URL)

	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
		mapResolver{"a": {
			BaseURL: mustURLT(t, "https://parent.invalid/v1"),
			Auth:    "sk-parent",
			Anthropic: &UpstreamTarget{
				BaseURL: targetURL,
				Auth:    "sk-target",
			},
		}},
		filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	resp := poolPost(t, server.Endpoint(), token, "/v1/messages",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotHeader != "Authorization" {
		t.Fatalf("auth header name = %q, want the defaulted Authorization", gotHeader)
	}
	if gotValue != "Bearer sk-target" {
		t.Fatalf("auth = %q, want the target credential Bearer sk-target", gotValue)
	}
}

func TestFlattenedNativeUpstreamMergeRules(t *testing.T) {
	parentURL := mustURLT(t, "https://parent.invalid/v1")
	targetURL := mustURLT(t, "https://target.invalid/anthropic")
	called := false
	parentProvider := func(_ context.Context) (string, error) {
		called = true
		return "from-parent", nil
	}

	t.Run("target wins", func(t *testing.T) {
		flat := flattenedNativeUpstream(&Upstream{
			BaseURL: parentURL, Auth: "sk-parent", AuthHeader: "x-api-key", AuthValuePrefix: "",
			Anthropic: &UpstreamTarget{
				BaseURL: targetURL, Auth: "sk-target",
				AuthHeader: "anthropic-key", AuthValuePrefix: "tok ",
			},
		})
		if flat.BaseURL != targetURL {
			t.Errorf("BaseURL = %v, want the target's", flat.BaseURL)
		}
		if flat.Auth != "sk-target" {
			t.Errorf("Auth = %q, want the target's", flat.Auth)
		}
		if flat.AuthHeader != "anthropic-key" || flat.AuthValuePrefix != "tok " {
			t.Errorf("header = %q/%q, want the target's", flat.AuthHeader, flat.AuthValuePrefix)
		}
	})

	t.Run("parent fills what the target leaves empty", func(t *testing.T) {
		flat := flattenedNativeUpstream(&Upstream{
			BaseURL: parentURL, Auth: "sk-parent", AuthHeader: "x-api-key",
			AuthProvider: parentProvider,
			ExtraHeaders: []ExtraHeader{{Name: "X-Parent", Value: "1"}},
			Anthropic: &UpstreamTarget{
				BaseURL:      targetURL,
				ExtraHeaders: []ExtraHeader{{Name: "X-Target", Value: "1"}},
			},
		})
		if flat.Auth != "sk-parent" {
			t.Errorf("Auth = %q, want the parent's", flat.Auth)
		}
		if flat.AuthHeader != "x-api-key" {
			t.Errorf("AuthHeader = %q, want the parent's", flat.AuthHeader)
		}
		if flat.AuthProvider == nil {
			t.Fatal("AuthProvider = nil, want the parent's")
		}
		if got, _ := flat.AuthProvider(context.Background()); got != "from-parent" {
			t.Errorf("provider = %q, want the parent's", got)
		}
		if !called {
			t.Error("parent provider was never reachable through the flattened upstream")
		}
		// Target headers first so a conflicting name is resolved the same way
		// serveNativeAnthropic has always applied them.
		if len(flat.ExtraHeaders) != 2 ||
			flat.ExtraHeaders[0].Name != "X-Target" || flat.ExtraHeaders[1].Name != "X-Parent" {
			t.Errorf("ExtraHeaders = %+v, want target then parent", flat.ExtraHeaders)
		}
	})

	t.Run("nil target is not a panic", func(t *testing.T) {
		up := &Upstream{BaseURL: parentURL, Auth: "sk-parent"}
		if got := flattenedNativeUpstream(up); got != up {
			t.Error("flattenedNativeUpstream(nil target) must hand back the parent untouched")
		}
	})
}

// The compaction rewrite belongs on the pool's passthrough leaf too. A member
// that hits the limit used to return the raw vendor error, so the client never
// saw the signal that makes it compact.
func TestPoolPassthroughMemberContextWindowIsRewritten(t *testing.T) {
	up := newStubUpstream(t, 400)
	up.body = `{"error":{"code":"context_length_exceeded"}}`

	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
		mapResolver{"a": {BaseURL: up.url, Auth: "sk-a"}},
		filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	resp := poolPost(t, server.Endpoint(), token, "/v1/messages",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"type":"error"`) ||
		!strings.Contains(string(body), "context_length_exceeded") {
		t.Fatalf("passthrough member was not rewritten for compaction: %d %s", resp.StatusCode, body)
	}
}

// Two copies of a dispatch decision is not duplication, it is a divergence
// waiting to be discovered in production. Both entry points must switch on the
// one function, and the predicates it is built from must not be re-derived
// anywhere else.
func TestOneEgressDispatchTable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	switchesOn := map[string]bool{}
	var rederived []string
	for _, entry := range entries {
		file := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SwitchStmt:
					call, ok := node.Tag.(*ast.CallExpr)
					if !ok {
						return true
					}
					id, ok := call.Fun.(*ast.Ident)
					if ok && id.Name == "egressKindFor" {
						switchesOn[fn.Name.Name] = true
					}
				case *ast.Ident:
					if node.Name != "isGemini" && node.Name != "isMessages" {
						return true
					}
					if fn.Name.Name != "egressKindFor" {
						rederived = append(rederived, fmt.Sprintf("%s:%s", file, fn.Name.Name))
					}
				}
				return true
			})
		}
	}

	for _, want := range []string{"Serve", "serveMember"} {
		if !switchesOn[want] {
			t.Errorf("%s does not switch on egressKindFor — it carries its own copy of the dispatch decision", want)
		}
	}
	if len(rederived) > 0 {
		t.Errorf("egress predicates re-derived outside egressKindFor: %v", rederived)
	}
}

func poolPost(t *testing.T, endpoint, token, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

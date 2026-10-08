package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fetchServer spins an httptest server recording the last request so a test
// can assert on the URL and headers the fetcher actually sent. The request
// crosses to the handler's goroutine, so it is delivered on a channel rather
// than through a shared pointer.
func fetchServer(t *testing.T, status int, body string) (*httptest.Server, <-chan *http.Request) {
	t.Helper()
	seen := make(chan *http.Request, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestParseModelIdsCoversTheThreeWireShapes pins the parser against the three
// list shapes in circulation plus the bare string[] form on self-hosted
// gateways. Ordering is preserved: vendors list newest first and pickers show
// the first match as the default.
func TestParseModelIdsCoversTheThreeWireShapes(t *testing.T) {
	openai := []byte(`{"data":[{"id":"b-model"},{"id":"a-model"},{"id":"b-model"}]}`)
	got := parseModelIds(openai)
	if len(got) != 2 || got[0] != "b-model" || got[1] != "a-model" {
		t.Fatalf("openai shape = %v, want [b-model a-model] in wire order", got)
	}

	anthropic := []byte(`{"data":[{"id":"claude-x"}]}`)
	if got = parseModelIds(anthropic); len(got) != 1 || got[0] != "claude-x" {
		t.Fatalf("anthropic shape = %v", got)
	}

	gemini := []byte(`{"models":[{"name":"models/gemini-x"}]}`)
	if got = parseModelIds(gemini); len(got) != 1 || got[0] != "gemini-x" {
		t.Fatalf("gemini shape = %v, want the models/ prefix stripped", got)
	}

	bare := []byte(`{"models":["bare-one","bare-two"]}`)
	if got = parseModelIds(bare); len(got) != 2 || got[0] != "bare-one" {
		t.Fatalf("bare string[] shape = %v", got)
	}

	if got := parseModelIds([]byte(`not json`)); got != nil {
		t.Fatalf("garbage body = %v, want nil", got)
	}
	if got := parseModelIds([]byte(`{"data":"a string, not an array"}`)); got != nil {
		t.Fatalf("scalar data = %v, want nil", got)
	}
}

// TestFetchOpenAIStyleBuildsTheExpectedRequest checks the URL versioning rule:
// an endpoint already carrying /vN gets /models appended, a bare host gets
// /v1/models. The test server supplies the host; the case only varies the
// path part of the endpoint.
func TestFetchOpenAIStyleBuildsTheExpectedRequest(t *testing.T) {
	cases := []struct {
		suffix, wantPath string
	}{
		{"", "/v1/models"},
		{"/", "/v1/models"},
		{"/v1", "/v1/models"},
		{"/v1beta", "/v1beta/models"},
	}
	for _, c := range cases {
		srv, seen := fetchServer(t, 200, `{"data":[{"id":"m1"}]}`)
		_, err := FetchLiveModels(context.Background(), "openai", &FetchContext{
			Endpoint: srv.URL + c.suffix, APIKey: "sk-key", Client: srv.Client(),
		})
		if err != nil {
			t.Fatalf("%s: %v", c.suffix, err)
		}
		req := <-seen
		if req.URL.Path != c.wantPath {
			t.Errorf("%s hit %s, want %s", c.suffix, req.URL.Path, c.wantPath)
		}
		if got := req.Header.Get("authorization"); got != "Bearer sk-key" {
			t.Errorf("%s auth = %q, want bearer", c.suffix, got)
		}
		srv.Close()
	}
}

// TestFetchAnthropicSkipsWithoutAKey pins the guard: the anthropic wire shape
// has no keyless mode, so a missing key is "no discovery", not an error.
func TestFetchAnthropicSkipsWithoutAKey(t *testing.T) {
	srv, seen := fetchServer(t, 200, `{"data":[{"id":"m1"}]}`)
	ids, err := FetchLiveModels(context.Background(), "anthropic", &FetchContext{
		Endpoint: srv.URL, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil {
		t.Fatalf("keyless anthropic fetch returned %v, want nil", ids)
	}
	select {
	case r := <-seen:
		t.Fatalf("keyless anthropic fetch hit the network: %s", r.URL)
	default:
	}
}

// TestFetchAnthropicSendsTheKeyHeader checks the header split: anthropic puts
// the key in x-api-key with no prefix, not in a bearer authorization.
func TestFetchAnthropicSendsTheKeyHeader(t *testing.T) {
	srv, seen := fetchServer(t, 200, `{"data":[{"id":"m1"}]}`)
	_, err := FetchLiveModels(context.Background(), "anthropic", &FetchContext{
		Endpoint: srv.URL, APIKey: "ak-key", Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := <-seen
	if got := req.Header.Get("x-api-key"); got != "ak-key" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := req.Header.Get("authorization"); got != "" {
		t.Fatalf("authorization = %q, anthropic keys do not ride there", got)
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Fatal("anthropic-version header missing")
	}
}

// TestFetchGeminiPaginatesAndDedupes drives the bounded pagination loop: two
// pages, an overlapping id, and the pageToken that would otherwise spin
// forever.
func TestFetchGeminiPaginatesAndDedupes(t *testing.T) {
	pages := []string{
		`{"models":[{"name":"models/gemini-a"},{"name":"models/gemini-b"}],"nextPageToken":"p2"}`,
		`{"models":[{"name":"models/gemini-b"},{"name":"models/gemini-c"}]}`,
	}
	page := 0
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") == "" {
			t.Errorf("page %d: gemini key missing from the query", page)
		}
		tokens = append(tokens, r.URL.Query().Get("pageToken"))
		if page < len(pages) {
			_, _ = w.Write([]byte(pages[page]))
		}
		page++
	}))
	defer srv.Close()

	ids, err := FetchLiveModels(context.Background(), "gemini", &FetchContext{
		Endpoint: srv.URL, APIKey: "g-key", Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if page != 2 {
		t.Fatalf("fetched %d pages, want 2 (stop when nextPageToken is empty)", page)
	}
	if len(tokens) != 2 || tokens[1] != "p2" {
		t.Fatalf("page tokens = %v, want [ p2]", tokens)
	}
	for _, want := range []string{"gemini-a", "gemini-b", "gemini-c"} {
		if !contains(ids, want) {
			t.Fatalf("ids = %v, missing %q after dedupe", ids, want)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want the overlap deduped to 3", ids)
	}
}

// TestReadModelIDsFallsBackSilentlyOnHTTPError pins the advisory contract: a
// model list is a nice-to-have, a 4xx/5xx is "no discovery", not a failure.
func TestReadModelIDsFallsBackSilentlyOnHTTPError(t *testing.T) {
	for _, status := range []int{401, 404, 500} {
		srv, _ := fetchServer(t, status, `{"error":"no"}`)
		ids, err := FetchLiveModels(context.Background(), "openai", &FetchContext{
			Endpoint: srv.URL, APIKey: "k", Client: srv.Client(),
		})
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if ids != nil {
			t.Fatalf("status %d returned %v, want nil", status, ids)
		}
		srv.Close()
	}
}

// TestFetchLiveModelsRejectsAnUnknownStyle covers the style dispatch default.
func TestFetchLiveModelsRejectsAnUnknownStyle(t *testing.T) {
	if _, err := FetchLiveModels(context.Background(), "ollama", &FetchContext{}); err == nil {
		t.Fatal("unknown style accepted silently")
	} else if !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("error did not name the style: %v", err)
	}
}

// TestFetchContextAuthHeaderOverride checks the per-provider header override:
// AuthHeader set moves the key off the default header, and then no prefix is
// applied unless one is given.
func TestFetchContextAuthHeaderOverride(t *testing.T) {
	srv, seen := fetchServer(t, 200, `{"data":[{"id":"m1"}]}`)
	_, err := FetchLiveModels(context.Background(), "openai", &FetchContext{
		Endpoint: srv.URL, APIKey: "raw-key", AuthHeader: "x-custom-key", Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := <-seen
	if got := req.Header.Get("x-custom-key"); got != "raw-key" {
		t.Fatalf("x-custom-key = %q, want the bare key", got)
	}
	if got := req.Header.Get("authorization"); got != "" {
		t.Fatalf("authorization = %q, the override moves the key off it", got)
	}
}

// TestParseModelIdsJSONIsStable is a canary against accidental field renames
// in the parser's wire struct.
func TestParseModelIdsJSONIsStable(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"data": []map[string]string{{"id": "x"}}})
	if got := parseModelIds(raw); len(got) != 1 || got[0] != "x" {
		t.Fatalf("round-tripped shape parsed as %v", got)
	}
}

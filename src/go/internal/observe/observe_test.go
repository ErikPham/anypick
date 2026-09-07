package observe

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---- classification ----

func TestClassifyBucketsQuotaBeforeStatus(t *testing.T) {
	cases := []struct {
		status     int
		quotaClass bool
		want       Class
	}{
		{200, false, ClassOK},
		{201, false, ClassOK},
		{302, false, ClassOK},
		// A 5xx body the ring already decided means quota must not be
		// reclassified here, or the failover counter and the quota counter
		// disagree about the same request.
		{529, true, ClassQuota},
		{429, false, ClassQuota},
		{402, false, ClassQuota},
		{401, false, ClassClient},
		{403, false, ClassClient},
		{413, false, ClassClient},
		{503, false, ClassClient},
		{500, false, ClassTransient},
		{504, false, ClassTransient},
		// Not a status any upstream should send, but a counter that panics or
		// mislabels is worse than one that says "we broke".
		{418, false, ClassError},
	}
	for _, tc := range cases {
		if got := Classify(tc.status, tc.quotaClass); got != tc.want {
			t.Errorf("Classify(%d, %v) = %q, want %q", tc.status, tc.quotaClass, got, tc.want)
		}
	}
}

// ---- counters ----

func TestEndIsIdempotent(t *testing.T) {
	reg := New()
	req := reg.Begin("t")
	req.End(200, ClassOK)
	req.End(500, ClassError)

	got := reg.Snapshot()["t"]
	if got.Completed != 1 {
		t.Errorf("completed = %d, want 1: a double-counted request inflates every average", got.Completed)
	}
	if got.InFlight != 0 {
		t.Errorf("inFlight = %d, want 0: a gauge that drifts up is worse than no gauge", got.InFlight)
	}
	if got.ByClass[string(ClassError)] != 0 {
		t.Errorf("the second End was honoured: %+v", got.ByClass)
	}
}

func TestNilHandlesAreSafe(t *testing.T) {
	// Every serving path passes a possibly-nil handle around; a metrics call
	// must never be the reason a proxy stops serving.
	var req *Request
	req.Failover()
	req.Stream(time.Second)
	req.End(200, ClassOK)

	var reg *Registry
	_ = reg.Begin("t")
	_ = reg.Snapshot()
}

func TestSnapshotIsACopyNotALiveView(t *testing.T) {
	reg := New()
	req := reg.Begin("t")
	req.End(200, ClassOK)

	snap := reg.Snapshot()
	snap["t"].ByClass[string(ClassOK)] = 99999
	delete(snap["t"].ByStatus, "200")

	again := reg.Snapshot()
	if again["t"].ByClass[string(ClassOK)] == 99999 {
		t.Error("mutating a snapshot changed the registry: /health would lie")
	}
	if _, ok := again["t"].ByStatus["200"]; !ok {
		t.Error("deleting from a snapshot changed the registry")
	}
}

func TestAverageLatencyWithoutCompletionsIsZero(t *testing.T) {
	var s Stats
	if got := s.AverageLatencyMs(); got != 0 {
		t.Errorf("empty average = %d, want 0 (not a divide-by-zero panic)", got)
	}
}

// ---- concurrency ceiling ----

func TestLimiterRefusesPastTheCeiling(t *testing.T) {
	route := t.Name()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	lim := NewLimiter(1)
	h := lim.Handler(route, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
	}))

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never entered the handler")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("past the ceiling = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("refusal carries no Retry-After: a well-behaved client cannot tell backoff from give-up")
	}
	// Queue-free: the refusal must be immediate, not parked until a slot frees.
	if lim.InFlight() != 1 {
		t.Errorf("inFlight = %d, want 1: a refusal must not also consume a slot", lim.InFlight())
	}
	close(release)

	// The refusal is this process's decision, so it is counted as one.
	if got := Default.Snapshot()[route].ByClass[string(ClassClient)]; got != 1 {
		t.Errorf("refusals counted = %d, want 1 — a silent ceiling sends the operator to debug the vendor", got)
	}
}

func TestLimiterReleasesWhenTheRequestCompletes(t *testing.T) {
	lim := NewLimiter(1)
	h := lim.Handler(t.Name(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200: the limiter leaked a slot", i, rec.Code)
		}
	}
	if lim.InFlight() != 0 {
		t.Errorf("inFlight after 5 sequential requests = %d, want 0", lim.InFlight())
	}
}

func TestNilLimiterIsPassThrough(t *testing.T) {
	var lim *Limiter
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := httptest.NewRecorder()
	lim.Handler(t.Name(), next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("nil limiter status = %d, want the next handler's 418", rec.Code)
	}
}

// ---- ADR 0006: a credential cannot reach a counter ----

// TestObserverNeverRecordsToken is the behavioural half of the invariant.
// Every place a credential travels — Authorization, x-api-key, the query
// string, the path, the body — is sent through the exact API a serving path
// uses, and the rendered output is searched for it.
func TestObserverNeverRecordsToken(t *testing.T) {
	const token = "sk-ant-observe-must-never-record-this"
	route := "test.never." + t.Name()

	served := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := Default.Begin(route)
		defer req.End(http.StatusOK, ClassOK)
		_ = r.Header.Get("Authorization")
		_ = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusOK)
	})

	body := `{"model":"claude","token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/"+token+"?token="+token, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-api-key", token)
	served.ServeHTTP(httptest.NewRecorder(), req)

	if raw := HealthCounters(); bytes.Contains(raw, []byte(token)) {
		t.Errorf("a token reached the /health counters: %s", raw)
	}
	snap, err := json.Marshal(Default.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snap, []byte(token)) {
		t.Errorf("a token reached the registry snapshot: %s", snap)
	}
}

// TestNoRequestTypeReachesThisPackage is the structural half: the only way a
// token could ever be recorded is if a request object were handed to this
// package, so no exported function may accept one and no struct may store
// one. Deletion beats detection — a reviewer cannot eyeball this on every
// diff, and a counter that logs a bearer token into a loopback /health body
// is a credential leak that never touches a log file anyone audits.
//
// Limiter.Handler's own closure necessarily names *http.Request; it is a
// FuncLit, not a FuncDecl, and it only forwards the request downstream. It
// is the single deliberate exception and it is visible right here.
func TestNoRequestTypeReachesThisPackage(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "observe.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	bad := func(node ast.Node) bool {
		star, ok := node.(*ast.StarExpr)
		if !ok {
			return false
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		ident, ok := sel.X.(*ast.Ident)
		return ok && ident.Name == "http" && sel.Sel.Name == "Request"
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			for _, field := range d.Type.Params.List {
				if bad(field.Type) {
					t.Errorf("exported func %s takes *http.Request — a token can now reach a counter", d.Name.Name)
				}
			}
			for _, field := range results(d) {
				if bad(field) {
					t.Errorf("exported func %s returns *http.Request", d.Name.Name)
				}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if bad(field.Type) {
						t.Errorf("struct %s stores a *http.Request field", ts.Name.Name)
					}
				}
			}
		}
	}
}

func results(d *ast.FuncDecl) []ast.Expr {
	if d.Type.Results == nil {
		return nil
	}
	var out []ast.Expr
	for _, field := range d.Type.Results.List {
		out = append(out, field.Type)
	}
	return out
}

func TestHealthCountersAreAlwaysValidJSON(t *testing.T) {
	raw := HealthCounters()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("/health counters are not a JSON object: %v (%s)", err, raw)
	}
}

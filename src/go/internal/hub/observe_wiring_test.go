// The hub is the third request path and the only one that delegates: a
// `pool:<provider>` source is served by a credential ring that belongs to
// another package. Both halves must be counted, and they must be counted on
// separate rows — one number for "the hub served it", one for "the ring
// failed over to serve it".
package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/observe"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/upstream"
)

type hubHealth struct {
	OK       bool                     `json:"ok"`
	Instance string                   `json:"instanceId"`
	Counters map[string]observe.Stats `json:"counters"`
}

func fetchHubHealth(t *testing.T, endpoint string) hubHealth {
	t.Helper()
	resp, err := http.Get(endpoint + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body hubHealth
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("/health did not decode: %v", err)
	}
	return body
}

func TestHubCountsItsServedRequests(t *testing.T) {
	st := newHubStore(t)
	cfg, err := st.UpsertConfigContext(context.Background(), Config{
		Name:    "counted",
		Enabled: true,
		Sources: []Source{{ProviderID: "glm", Account: "cn"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Config: cfg, Store: st, Resolver: &fakeResolver{}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	token := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: DefaultRouteID("counted"), HubName: "counted",
		ClientID: "claude", Token: token,
	})

	before := observe.Default.Snapshot()[observe.RouteHub]
	postModel(t, srv.Endpoint(), token, "any-model")
	// A refused request is still a request: without it in the counters an
	// operator cannot tell "nobody is calling" from "everybody is refused".
	postModel(t, srv.Endpoint(), "wrong-token", "any-model")
	after := observe.Default.Snapshot()[observe.RouteHub]

	if delta := after.Started - before.Started; delta != 2 {
		t.Errorf("hub started delta = %d, want 2 (one routed, one refused)", delta)
	}
	if delta := after.ByClass[string(observe.ClassClient)] - before.ByClass[string(observe.ClassClient)]; delta != 1 {
		t.Errorf("client-class delta = %d, want 1: a 401 must not be filed as an upstream failure", delta)
	}
	if after.InFlight < 0 {
		t.Errorf("inFlight = %d, want >= 0", after.InFlight)
	}

	health := fetchHubHealth(t, srv.Endpoint())
	if !health.OK {
		t.Error("/health stopped saying ok")
	}
	if _, ok := health.Counters[observe.RouteHub]; !ok {
		t.Errorf("/health counters = %v, want a %q row", health.Counters, observe.RouteHub)
	}
}

// A pool-backed source is served by the ring, so both rows must move: the
// hub's, because it accepted and routed the request, and the delegated ring's,
// because that is where the failover decision actually happened. Counting only
// the hub would leave the one number that predicts member exhaustion at zero.
func TestHubPoolSourceCountsOnBothRoutes(t *testing.T) {
	st := newHubStore(t)

	memberA := newStubUpstream(t, http.StatusUnauthorized,
		`{"error":{"type":"authentication_error","message":"quota out"}}`)
	memberB := newStubUpstream(t, http.StatusOK, `{"up":"b"}`)
	resolver := mapResolver{
		targets: map[string]*url.URL{
			"deepseek/a": memberA.url(),
			"deepseek/b": memberB.url(),
		},
		creds: map[string]string{
			"deepseek/a": "cred-pool-a",
			"deepseek/b": "cred-pool-b",
		},
	}
	ring := &proxy.PoolServer{
		ProviderID: "deepseek", Members: []string{"a", "b"},
		Resolver: resolver,
		Guard: upstream.NewGuard(upstream.GuardOptions{
			Enabled:      true,
			AccountNames: []string{"a", "b"},
			StatePath:    filepath.Join(t.TempDir(), "guard.json"),
			ProviderID:   "deepseek",
		}),
	}

	cfg, err := st.UpsertConfigContext(context.Background(), Config{
		Name:    "poolhub-counted",
		Enabled: true,
		Sources: []Source{{ProviderID: "deepseek", Account: "pool:deepseek"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Config: cfg, Store: st, Resolver: resolver, PoolRings: staticRing{ring}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	token := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: DefaultRouteID("poolhub-counted"), HubName: "poolhub-counted",
		ClientID: "cli", Token: token,
	})

	before := observe.Default.Snapshot()
	resp, body := postModel(t, srv.Endpoint(), token, "any-model")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pool-backed request = %d %s, want 200", resp.StatusCode, body)
	}
	after := observe.Default.Snapshot()

	if delta := after[observe.RouteHub].Completed - before[observe.RouteHub].Completed; delta != 1 {
		t.Errorf("hub completed delta = %d, want 1", delta)
	}
	if delta := after[observe.RoutePoolHub].Completed - before[observe.RoutePoolHub].Completed; delta != 1 {
		t.Errorf("delegated ring completed delta = %d, want 1", delta)
	}
	if delta := after[observe.RoutePoolHub].Failovers - before[observe.RoutePoolHub].Failovers; delta != 1 {
		t.Errorf("delegated ring failovers delta = %d, want 1", delta)
	}
}

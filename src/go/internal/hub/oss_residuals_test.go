package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
)

func TestListenLoopbackRefusesNonLoopback(t *testing.T) {
	_, err := listenLoopback("0.0.0.0", 1)
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROXY_UNSAFE_HOST" {
		t.Fatalf("listenLoopback(0.0.0.0) = %v, want PROXY_UNSAFE_HOST", err)
	}
	ln, err := listenLoopback("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
}

func TestRootlessMutationsFailClosed(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := &Store{DB: database.DB}
	_, err = st.UpsertConfigContext(context.Background(), Config{Name: "default", Enabled: true})
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "STATE_CONFLICT" {
		t.Fatalf("rootless upsert = %v, want STATE_CONFLICT", err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM proxy_hubs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("proxy_hubs rows = %d, want 0", n)
	}
}

func TestEmptyTokenAttachDoesNotAuthenticateEmpty(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	if _, err := st.UpsertConfigContext(ctx, Config{Name: "default", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"route-a", "route-b"} {
		if err := st.AttachRouteContext(ctx, Route{
			RouteID: id, HubName: "default", ClientID: "claude",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var secretA, secretB string
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id = 'route-a'`).Scan(&secretA); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT token_secret FROM proxy_hub_routes WHERE route_id = 'route-b'`).Scan(&secretB); err != nil {
		t.Fatal(err)
	}
	if secretA == "" || secretA == secretB || !strings.HasPrefix(secretA, "unset:") || !strings.HasPrefix(secretB, "unset:") {
		t.Fatalf("placeholders = %q %q", secretA, secretB)
	}
	emptyHash, err := st.hashToken(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if secretA == emptyHash || secretB == emptyHash {
		t.Fatal("empty attach stored HMAC(\"\")")
	}
	found, err := st.RouteByToken("")
	if err != nil || found != nil {
		t.Fatalf("RouteByToken(\"\") = %+v %v", found, err)
	}
	if found, err := st.RouteByToken("   "); err != nil || found != nil {
		t.Fatalf("whitespace token matched %+v %v", found, err)
	}

	token := mustNewProxyToken(t)
	if err := st.AttachRouteContext(ctx, Route{
		RouteID: "route-a", HubName: "default", ClientID: "claude", Token: token,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachRouteContext(ctx, Route{
		RouteID: "route-a", HubName: "default", ClientID: "claude",
		Models: []ModelRoute{{Model: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	found, err = st.RouteByToken(token)
	if err != nil || found == nil || !found.proven || found.Token != token || found.RouteID != "route-a" {
		t.Fatalf("preserved route = %+v %v", found, err)
	}
	route, err := st.routeByID(ctx, "route-a")
	if err != nil || route == nil || len(route.Models) != 1 || route.Models[0].Model != "m1" {
		t.Fatalf("manifest update = %+v %v", route, err)
	}
}

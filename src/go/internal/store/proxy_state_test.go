package store

import (
	"context"
	"testing"
)

func TestProxyStateRoundTrip(t *testing.T) {
	s := testStore(t)
	state := ProxyRuntimeState{
		AccountName: "work",
		Endpoint:    "http://127.0.0.1:4000",
		PID:         1234,
		Token:       "secret-token",
		StartedAt:   nowISO(),
	}
	if err := s.WriteProxyStateContext(context.Background(), "grok", "work", state); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadProxyState(context.Background(), "grok", "work")
	if err != nil || got == nil {
		t.Fatalf("ReadProxyState: %v %v", got, err)
	}
	if got.Endpoint != state.Endpoint || got.Token != state.Token || got.PID != 1234 {
		t.Fatalf("state = %#v want %#v", got, state)
	}
	// Overwrite.
	state.Endpoint = "http://127.0.0.1:4001"
	if err := s.WriteProxyStateContext(context.Background(), "grok", "work", state); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.ReadProxyState(context.Background(), "grok", "work")
	if got2.Endpoint != "http://127.0.0.1:4001" {
		t.Fatalf("overwrite failed: %#v", got2)
	}
	if err := s.ClearProxyStateContext(context.Background(), "grok", "work"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadProxyState(context.Background(), "grok", "work"); got != nil {
		t.Fatalf("want nil after clear, got %#v", got)
	}
}

func TestProxyStateMissingReturnsNil(t *testing.T) {
	s := testStore(t)
	got, err := s.ReadProxyState(context.Background(), "grok", "nope")
	if err != nil || got != nil {
		t.Fatalf("missing = %#v %v want nil,nil", got, err)
	}
}

func TestProxyStateCorruptReturnsNil(t *testing.T) {
	s := testStore(t)
	// Write valid then corrupt directly via SQL.
	_ = s.WriteProxyStateContext(context.Background(), "grok", "work", ProxyRuntimeState{Endpoint: "http://x", StartedAt: nowISO()})
	if _, err := s.DB.Exec(`UPDATE proxy_state SET state_json='not json' WHERE provider='grok' AND account='work'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadProxyState(context.Background(), "grok", "work")
	if err != nil || got != nil {
		t.Fatalf("corrupt should return nil,nil, got %#v %v", got, err)
	}
}

// Release drops endpoint/pid/instance/leases but keeps the bearer token so
// a later start can reuse it. Deleting the row here is how every restart
// minted a new secret and configured clients 401'd.
func TestReleaseProxyRuntimeKeepsToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.WriteProxyStateContext(ctx, "workbuddy", "work", ProxyRuntimeState{
		AccountName: "work",
		Endpoint:    "http://127.0.0.1:50785",
		PID:         4242,
		InstanceID:  "inst-1",
		StartedAt:   nowISO(),
		Token:       "stable-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseProxyRuntimeContext(ctx, "workbuddy", "work"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadProxyState(context.Background(), "workbuddy", "work")
	if err != nil || got == nil {
		t.Fatalf("ReadProxyState after release: %v %v", got, err)
	}
	if got.Token != "stable-secret" {
		t.Fatalf("token = %q, want stable-secret", got.Token)
	}
	if got.Endpoint != "" || got.InstanceID != "" || got.PID != 0 {
		t.Fatalf("liveness survived release: %#v", got)
	}
}

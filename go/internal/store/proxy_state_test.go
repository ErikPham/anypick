package store

import "testing"

func TestProxyStateRoundTrip(t *testing.T) {
	s := testStore(t)
	state := ProxyRuntimeState{
		AccountName: "work",
		Endpoint:    "http://127.0.0.1:4000",
		PID:         1234,
		Token:       "secret-token",
		StartedAt:   nowISO(),
	}
	if err := s.WriteProxyState("grok", "work", state); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadProxyState("grok", "work")
	if err != nil || got == nil {
		t.Fatalf("ReadProxyState: %v %v", got, err)
	}
	if got.Endpoint != state.Endpoint || got.Token != state.Token || got.PID != 1234 {
		t.Fatalf("state = %#v want %#v", got, state)
	}
	// Overwrite.
	state.Endpoint = "http://127.0.0.1:4001"
	if err := s.WriteProxyState("grok", "work", state); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.ReadProxyState("grok", "work")
	if got2.Endpoint != "http://127.0.0.1:4001" {
		t.Fatalf("overwrite failed: %#v", got2)
	}
	if err := s.ClearProxyState("grok", "work"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadProxyState("grok", "work"); got != nil {
		t.Fatalf("want nil after clear, got %#v", got)
	}
}

func TestProxyStateMissingReturnsNil(t *testing.T) {
	s := testStore(t)
	got, err := s.ReadProxyState("grok", "nope")
	if err != nil || got != nil {
		t.Fatalf("missing = %#v %v want nil,nil", got, err)
	}
}

func TestProxyStateCorruptReturnsNil(t *testing.T) {
	s := testStore(t)
	// Write valid then corrupt directly via SQL.
	_ = s.WriteProxyState("grok", "work", ProxyRuntimeState{Endpoint: "http://x", StartedAt: nowISO()})
	if _, err := s.DB.Exec(`UPDATE proxy_state SET state_json='not json' WHERE provider='grok' AND account='work'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadProxyState("grok", "work")
	if err != nil || got != nil {
		t.Fatalf("corrupt should return nil,nil, got %#v %v", got, err)
	}
}

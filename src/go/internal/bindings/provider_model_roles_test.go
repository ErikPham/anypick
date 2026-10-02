package bindings

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/refs"
)

func TestModelRolesProviderKey(t *testing.T) {
	cases := map[refs.Ref]string{
		refs.AccountRef("claude", "work"):  "claude",
		refs.GatewayRef("Production"):      "gateway/Production",
		refs.ProxyHubRef("hub"):            "proxy-hub/hub",
		refs.PresetRef("@work"):            "",
		refs.Ref{Kind: "bogus", Name: "x"}: "",
	}
	for ref, want := range cases {
		if got := ModelRolesProviderKey(ref); got != want {
			t.Errorf("key(%v) = %q, want %q", ref, got, want)
		}
	}
}

func TestProviderModelRolesRoundTrip(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()

	got, err := s.GetProviderModelRoles(ctx, "claude")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty bucket = %+v, %v", got, err)
	}
	in := map[string]string{"default": "opus", "sonnet": "", "": "x"}
	if err := s.SetProviderModelRoles(ctx, "claude", in); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProviderModelRoles(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["default"] != "opus" {
		t.Fatalf("round trip = %+v", got)
	}

	// Empty write deletes the row: absence and emptiness read identically.
	if err := s.SetProviderModelRoles(ctx, "claude", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProviderModelRoles(ctx, "claude")
	if err != nil || len(got) != 0 {
		t.Fatalf("after clear = %+v, %v", got, err)
	}

	// Buckets are independent.
	if err := s.SetProviderModelRoles(ctx, "openai", map[string]string{"default": "gpt"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProviderModelRoles(ctx, "openai")
	if err != nil || got["default"] != "gpt" {
		t.Fatalf("openai = %+v, %v", got, err)
	}
	if _, err := s.GetProviderModelRoles(ctx, ""); err != nil {
		t.Fatalf("empty provider must read empty, not error: %v", err)
	}
	if err := s.SetProviderModelRoles(ctx, "", map[string]string{"default": "x"}); err != nil {
		t.Fatalf("empty provider must write nothing, not error: %v", err)
	}
}

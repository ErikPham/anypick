package accounts

import (
	"testing"

	"github.com/outof0/anypick/go/internal/provider"
)

func TestNormalizeIdentity(t *testing.T) {
	a := `{"email":"User@Example.COM"}`
	b := `{"email":"user@example.com"}`
	if !identitiesEqual(a, b) {
		t.Fatalf("emails should match case-insensitively")
	}
	if identitiesEqual(a, `{"email":"other@example.com"}`) {
		t.Fatalf("different emails should not match")
	}
	if !identitiesEqual("FOO", "foo") {
		t.Fatalf("plain strings should be case-insensitive")
	}
	if got := normalizeIdentity(`{"email":"A@B"}`); got != "a@b" {
		t.Fatalf("normalizeIdentity email=%q want a@b", got)
	}
	if got := normalizeIdentity("MiXeD"); got != "mixed" {
		t.Fatalf("normalizeIdentity plain=%q want mixed", got)
	}
	if got := normalizeIdentity(`{not json`); got != `{not json` {
		t.Fatalf("normalizeIdentity invalid json=%q", got)
	}
	if got := normalizeIdentity(`{"email":""}`); got != `{"email":""}` {
		t.Fatalf("empty email should fall back to raw, got %q", got)
	}
}

func TestLower(t *testing.T) {
	if got := lower("AbC123"); got != "abc123" {
		t.Fatalf("lower=%q want abc123", got)
	}
	if got := lower(""); got != "" {
		t.Fatalf("lower empty=%q", got)
	}
}

func TestComputeLiveMatchWithoutMatcher(t *testing.T) {
	svc, _ := testService(t)
	pv, _ := svc.Providers.Get("grok")
	live := provider.LiveAuthStatus{Present: true, Identity: "x"}
	m, k, err := computeLiveMatch(pv, t.TempDir(), live)
	if err != nil || k || m {
		t.Fatalf("computeLiveMatch no-matcher = %v %v %v want false false nil", m, k, err)
	}
}

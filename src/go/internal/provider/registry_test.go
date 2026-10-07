package provider

import (
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

type stubProvider struct{ id string }

func (s *stubProvider) ID() string                                        { return s.id }
func (s *stubProvider) Name() string                                      { return s.id }
func (s *stubProvider) Description() string                               { return "" }
func (s *stubProvider) CanProxy() bool                                    { return false }
func (s *stubProvider) AccountAdapter(_ string, _ bool) transport.Adapter { return transport.Adapter{} }
func (s *stubProvider) DetectLive() (LiveAuthStatus, error)               { return LiveAuthStatus{}, nil }
func (s *stubProvider) Backup(_ string) (SnapshotInfo, error)             { return SnapshotInfo{}, nil }
func (s *stubProvider) Restore(_ string) error                            { return nil }
func (s *stubProvider) DescribeSnapshot(_ string) (SnapshotInfo, error) {
	return SnapshotInfo{}, nil
}

func TestRegistryRegisterAndSeal(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubProvider{id: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubProvider{id: "b"}); err != nil {
		t.Fatal(err)
	}
	ids := r.IDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("IDs=%v want [a b] sorted", ids)
	}
	if !r.Has("a") || r.Has("missing") {
		t.Fatalf("Has wrong")
	}
	if _, err := r.Get("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("missing"); err == nil {
		t.Fatal("want error for missing")
	}
	if err := r.Register(&stubProvider{id: "a"}); err != nil {
		t.Fatal(err)
	}
	if len(r.IDs()) != 2 {
		t.Fatalf("duplicate should not add, got %v", r.IDs())
	}
	r.Seal()
	if err := r.Register(&stubProvider{id: "c"}); err == nil {
		t.Fatal("want error after Seal")
	}
}

func TestShortNameOf(t *testing.T) {
	p := &stubWithShort{stubProvider: stubProvider{id: "x"}, short: "XS"}
	if got := ShortNameOf(p); got != "XS" {
		t.Fatalf("ShortNameOf=%q want XS", got)
	}
	p2 := &stubProvider{id: "y"}
	if got := ShortNameOf(p2); got != "y" {
		t.Fatalf("ShortNameOf fallback=%q want y", got)
	}
}

type stubWithShort struct {
	stubProvider
	short string
}

func (s *stubWithShort) ShortName() string { return s.short }

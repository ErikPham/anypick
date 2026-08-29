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

func TestRegistryRecoverSwapReplacesOwnedAndRefusesBuiltin(t *testing.T) {
	r := NewRegistry()
	builtin := &stubProvider{id: "claude"}
	owned := &stubProvider{id: "plugin"}
	if err := r.Register(builtin); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(owned); err != nil {
		t.Fatal(err)
	}
	r.Seal()

	replacement := &stubProvider{id: "plugin"}
	if err := r.RecoverSwap(owned, replacement); err != nil {
		t.Fatalf("owned swap: %v", err)
	}
	got, err := r.Get("plugin")
	if err != nil || got != replacement {
		t.Fatalf("owned id after swap = %#v, %v", got, err)
	}
	if count := len(r.IDs()); count != 2 {
		t.Fatalf("IDs after swap = %v, want no duplicate", r.IDs())
	}

	if err := r.RecoverSwap(owned, &stubProvider{id: "plugin"}); err == nil {
		t.Fatal("stale previous was allowed to overwrite the replacement")
	}
	if err := r.RecoverSwap(&stubProvider{id: "claude"}, &stubProvider{id: "claude"}); err == nil {
		t.Fatal("built-in id was replaced without the committed pointer")
	}
	if got, _ := r.Get("claude"); got != builtin {
		t.Fatal("built-in provider was replaced")
	}
	// Nil previous inserts a brand-new id, never a live built-in.
	fresh := &stubProvider{id: "grown"}
	if err := r.RecoverSwap(nil, fresh); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := r.RecoverUndo(fresh, nil); err != nil {
		t.Fatalf("undo insert: %v", err)
	}
	if r.Has("grown") {
		t.Fatal("undone insert remained in the registry")
	}
	if !r.Has("claude") {
		t.Fatal("built-in vanished during plugin recovery")
	}
}

func TestRegistryRecoverRemoveDeletesOwnedAndRefusesBuiltin(t *testing.T) {
	r := NewRegistry()
	builtin := &stubProvider{id: "claude"}
	owned := &stubProvider{id: "plugin"}
	if err := r.Register(builtin); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(owned); err != nil {
		t.Fatal(err)
	}
	r.Seal()
	if err := r.RecoverRemove(owned); err != nil {
		t.Fatalf("remove owned: %v", err)
	}
	if r.Has("plugin") {
		t.Fatal("owned provider remained after RecoverRemove")
	}
	if err := r.RecoverRemove(&stubProvider{id: "claude"}); err == nil {
		t.Fatal("built-in was removed without the committed pointer")
	}
	if !r.Has("claude") {
		t.Fatal("built-in vanished")
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

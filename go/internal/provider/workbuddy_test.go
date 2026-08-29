package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

func TestWorkBuddyProviderShape(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	if p.ID() != "workbuddy" {
		t.Fatalf("ID = %q, want workbuddy", p.ID())
	}
	if p.Name() != "WorkBuddy AI" {
		t.Fatalf("Name = %q", p.Name())
	}
	if !p.CanProxy() {
		t.Fatal("CanProxy = false, want true")
	}
	if p.ShortName() != "WorkBuddy" {
		t.Fatalf("ShortName = %q", p.ShortName())
	}
}

func TestWorkBuddyAccountAdapterTransport(t *testing.T) {
	a := transport.WorkBuddyAccountAdapter("workbuddy", "main")
	cases := map[string]transport.Capability{
		"workbuddy": transport.Direct,
		"claude":    transport.ManagedBuiltinProxy,
		"codex":     transport.ManagedBuiltinProxy,
		"opencode":  transport.ManagedBuiltinProxy,
		"gemini":    transport.Unsupported,
		"kiro":      transport.Unsupported,
	}
	for client, want := range cases {
		if got := a.TransportFor(client); got != want {
			t.Fatalf("WorkBuddyAccountAdapter.TransportFor(%q) = %v, want %v", client, got, want)
		}
	}
}

func TestWorkBuddyBackupRestoreDetectLive(t *testing.T) {
	home := t.TempDir()
	p := newWorkBuddy(home)
	authPath := filepath.Join(home, ".workbuddy", "auth.json")

	// No live auth yet.
	if st, err := p.DetectLive(); err != nil || st.Present {
		t.Fatalf("DetectLive before login: present=%v err=%v", st.Present, err)
	}
	if _, err := p.Backup(t.TempDir()); err == nil {
		t.Fatal("Backup should fail with no live auth")
	}

	// Write a live auth file.
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"email":"a@b.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil || !st.Present {
		t.Fatalf("DetectLive after login: present=%v err=%v", st.Present, err)
	}

	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Identity != "a@b.com" {
		t.Fatalf("Backup identity = %q, want a@b.com", info.Identity)
	}
	if _, err := os.Stat(filepath.Join(dest, "auth.json")); err != nil {
		t.Fatalf("backup file not written: %v", err)
	}

	// Restore into an empty home.
	home2 := t.TempDir()
	p2 := newWorkBuddy(home2)
	if err := p2.Restore(dest); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(home2, ".workbuddy", "auth.json"))
	if err != nil {
		t.Fatalf("restore read: %v", err)
	}
	if string(restored) != `{"email":"a@b.com"}` {
		t.Fatalf("restored content = %s", restored)
	}

	desc, err := p2.DescribeSnapshot(dest)
	if err != nil {
		t.Fatalf("DescribeSnapshot: %v", err)
	}
	if desc.Identity != "a@b.com" {
		t.Fatalf("DescribeSnapshot identity = %q, want a@b.com", desc.Identity)
	}
}

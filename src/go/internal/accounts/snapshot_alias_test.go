package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/store"
)

// writeGrokLive points the grok provider's live store at one session. The
// bytes are deterministic per email, so rewriting the same email produces the
// same snapshot content.
func writeGrokLive(t *testing.T, email string) {
	t.Helper()
	path := os.Getenv("GROK_AUTH_PATH")
	if path == "" {
		t.Fatal("GROK_AUTH_PATH not set by testService")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sess":{"email":"`+email+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantAnyErrCode(t *testing.T, err error, code string) *anyerr.Error {
	t.Helper()
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
	return ae
}

// One snapshot is one account: saving the same credential content under a
// second name must be refused, --force included. This is the guard that keeps
// pairs like two WorkBuddy accounts holding one snapshot from appearing.
func TestSaveRefusesIdenticalSnapshotUnderSecondName(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	writeGrokLive(t, "same@grok.xai")

	if _, err := svc.Save(ctx, "grok", "first", false); err != nil {
		t.Fatalf("first save: %v", err)
	}

	_, err := svc.Save(ctx, "grok", "second", false)
	ae := wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	if !strings.Contains(ae.Message, "grok/first") {
		t.Fatalf("refusal names the owner: %q", ae.Message)
	}
	if len(ae.Suggestions) == 0 {
		t.Fatal("refusal must suggest the existing account")
	}
	if got, _ := st.Get(ctx, "grok", "second"); got != nil {
		t.Fatal("refused save created an account")
	}

	// --force overwrites the account being written, never mints a new name.
	if _, err := svc.Save(ctx, "grok", "second", true); err == nil {
		t.Fatal("force created a second name for the same snapshot")
	}
}

// Overwriting an account that already holds this snapshot is the refresh path
// and stays allowed, even when a legacy duplicate name exists in the store.
func TestSaveAllowsOverwritingOwnSnapshot(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	writeGrokLive(t, "owner@grok.xai")

	first, err := svc.Save(ctx, "grok", "owner", false)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	again, err := svc.Save(ctx, "grok", "owner", true)
	if err != nil {
		t.Fatalf("force overwrite of the owner: %v", err)
	}
	if again.CreatedAt != first.CreatedAt {
		t.Fatalf("CreatedAt changed %q -> %q", first.CreatedAt, again.CreatedAt)
	}

	// The same-name refusal keeps its own code; the snapshot guard must not
	// mask ACCOUNT_EXISTS.
	_, err = svc.Save(ctx, "grok", "owner", false)
	wantAnyErrCode(t, err, "ACCOUNT_EXISTS")

	// Seed a legacy duplicate row directly, the way a store written before
	// this guard looks. The owner must remain refreshable, a third name not.
	// A snapshot file is required: the store lists only accounts that have one.
	snap, _, err := st.PrepareSnapshot("grok", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"),
		[]byte(`{"sess":{"email":"owner@grok.xai"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMetaContext(ctx, store.Meta{
		Name: "legacy", Provider: "grok", Identity: "owner@grok.xai",
		CreatedAt: first.CreatedAt, UpdatedAt: first.CreatedAt,
	}, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, "grok", "owner", true); err != nil {
		t.Fatalf("refreshing an owner with a legacy duplicate: %v", err)
	}
	_, err = svc.Save(ctx, "grok", "third", false)
	ae := wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	for _, name := range []string{"grok/legacy", "grok/owner"} {
		if !strings.Contains(ae.Message, name) {
			t.Fatalf("refusal lists %s: %q", name, ae.Message)
		}
	}
}

// Distinct credential material stays separately saveable even when the display
// identity repeats: two Codex API keys both report the "API key" label, and
// two Codex workspaces can share one email.
func TestSaveKeepsDistinctSnapshotsSaveable(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	authPath := filepath.Join(t.TempDir(), "codex-auth.json")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_AUTH_PATH", authPath)
	writeAPIKey := func(key string) {
		t.Helper()
		if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"`+key+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeAPIKey("sk-one")
	first, err := svc.Save(ctx, "codex", "key-one", false)
	if err != nil {
		t.Fatalf("save first API key: %v", err)
	}
	if first.Identity != "API key" {
		t.Fatalf("identity = %q, want the placeholder", first.Identity)
	}
	writeAPIKey("sk-two")
	if _, err := svc.Save(ctx, "codex", "key-two", false); err != nil {
		t.Fatalf("a second API-key login must stay saveable: %v", err)
	}

	// The import/overwrite resolver ignores the display-only placeholder too.
	got, err := svc.ResolveIdentityTarget(ctx, "codex", "API key", "key-three", false)
	if err != nil || got != "key-three" {
		t.Fatalf("placeholder identity resolved to %q, %v", got, err)
	}
}

// Refreshing account B from a snapshot identical to A's must not clone A's
// credential into B.
func TestRefreshSnapshotRefusesAnotherAccountsSnapshot(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	writeGrokLive(t, "a@grok.xai")
	if _, err := svc.Save(ctx, "grok", "a", false); err != nil {
		t.Fatal(err)
	}
	writeGrokLive(t, "b@grok.xai")
	if _, err := svc.Save(ctx, "grok", "b", false); err != nil {
		t.Fatal(err)
	}

	writeGrokLive(t, "a@grok.xai")
	_, err := svc.RefreshSnapshot(ctx, "grok", "b")
	ae := wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	if !strings.Contains(ae.Message, "grok/a") {
		t.Fatalf("refusal names the snapshot owner: %q", ae.Message)
	}
}

// The doctor audit reports legacy pairs the save guard can only refuse to
// extend, and recommends keeping the provider's active account.
func TestDuplicateSnapshotGroupsReportsLegacyPairs(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	writeGrokLive(t, "a@grok.xai")
	if _, err := svc.Save(ctx, "grok", "a", false); err != nil {
		t.Fatal(err)
	}
	writeGrokLive(t, "b@grok.xai")
	if _, err := svc.Save(ctx, "grok", "b", false); err != nil {
		t.Fatal(err)
	}
	if groups, err := svc.DuplicateSnapshotGroups(ctx); err != nil || len(groups) != 0 {
		t.Fatalf("distinct snapshots = %+v, %v; want none", groups, err)
	}

	// Seed the pre-guard state: a second name holding a's exact snapshot.
	snap, _, err := st.PrepareSnapshot("grok", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"),
		[]byte(`{"sess":{"email":"a@grok.xai"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMetaContext(ctx, store.Meta{
		Name: "legacy", Provider: "grok", Identity: "a@grok.xai",
	}, snap); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveContext(ctx, "grok", "legacy"); err != nil {
		t.Fatal(err)
	}

	groups, err := svc.DuplicateSnapshotGroups(ctx)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v; want one", groups, err)
	}
	group := groups[0]
	if group.Provider != "grok" || group.Keep != "legacy" {
		t.Fatalf("group = %+v, want provider grok keep legacy", group)
	}
	if len(group.Names) != 2 || group.Names[0] != "a" || group.Names[1] != "legacy" {
		t.Fatalf("names = %v, want [a legacy]", group.Names)
	}
}

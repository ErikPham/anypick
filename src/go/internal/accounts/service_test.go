package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/store"
)

func testService(t *testing.T) (*Service, *store.AccountStore) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	// Wire a compatible vendor for SaveInput path (pure snapshot, no HOME).
	for _, def := range provider.BuiltinDefinitions() {
		if def.ID == "deepseek" {
			p, _ := provider.NewCompatible(def)
			_ = reg.Register(p)
			break
		}
	}
	// Wire a file-snapshot provider for Save path (grok on fake HOME).
	home := t.TempDir()
	t.Setenv("GROK_AUTH_PATH", filepath.Join(home, ".grok", "auth.json"))
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	svc := New(st, reg)
	return svc, st
}

type recordingProxyStopper struct {
	calls []string
	err   error
}

func (p *recordingProxyStopper) Stop(_ context.Context, providerID, accountName string) error {
	p.calls = append(p.calls, providerID+"/"+accountName)
	return p.err
}

func TestSaveInputSuccessAndDuplicate(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	meta, err := svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-test-123", Profile: "global",
	}, false)
	if err != nil {
		t.Fatalf("SaveInput: %v", err)
	}
	if meta.Name != "work" || meta.Provider != "deepseek" {
		t.Fatalf("meta = %#v", meta)
	}
	if got, _ := st.Get("deepseek", "work"); got == nil {
		t.Fatal("account not persisted")
	}

	// Duplicate without force must fail with ACCOUNT_EXISTS.
	_, err = svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-other", Profile: "global",
	}, false)
	if err == nil {
		t.Fatal("want duplicate error, got nil")
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "ACCOUNT_EXISTS" {
		t.Fatalf("want ACCOUNT_EXISTS, got %v", err)
	}

	// With force it overwrites.
	meta2, err := svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-new", Profile: "global",
	}, true)
	if err != nil {
		t.Fatalf("force overwrite: %v", err)
	}
	if meta2.CreatedAt != meta.CreatedAt {
		t.Fatalf("CreatedAt changed %q -> %q", meta.CreatedAt, meta2.CreatedAt)
	}
}

func TestSaveInputValidation(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	// Unknown provider.
	if _, err := svc.SaveInput(ctx, "nope", "a", CredentialInput{Kind: "api-key", Secret: "x", Profile: "global"}, false); err == nil {
		t.Fatal("want unknown provider error")
	} else {
		var ae *anyerr.Error
		if !errors.As(err, &ae) || ae.Code != "PROVIDER_NOT_FOUND" {
			t.Fatalf("want PROVIDER_NOT_FOUND, got %v", err)
		}
	}

	// Provider that does not accept credential inputs (grok).
	if _, err := svc.SaveInput(ctx, "grok", "a", CredentialInput{Kind: "api-key", Secret: "x", Profile: "global"}, false); err == nil {
		t.Fatal("want unsupported credential input")
	}

	// Unknown kind/profile/empty secret are surfaced from Compatible.
	if _, err := svc.SaveInput(ctx, "deepseek", "a2", CredentialInput{Kind: "nope", Secret: "x", Profile: "global"}, false); err == nil {
		t.Fatal("want unsupported kind error")
	}
	if _, err := svc.SaveInput(ctx, "deepseek", "a3", CredentialInput{Kind: "api-key", Secret: "", Profile: "global"}, false); err == nil {
		t.Fatal("want empty secret error")
	}
	if _, err := svc.SaveInput(ctx, "deepseek", "a4", CredentialInput{Kind: "api-key", Secret: "x", Profile: "moon"}, false); err == nil {
		t.Fatal("want unknown profile error")
	}

	// Invalid account name.
	if _, err := svc.SaveInput(ctx, "deepseek", "   ", CredentialInput{Kind: "api-key", Secret: "x", Profile: "global"}, false); err == nil {
		t.Fatal("want invalid name error")
	}
}

func TestSaveNativeAndGet(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	// Seed a fake Grok live auth file.
	authPath := os.Getenv("GROK_AUTH_PATH")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"sess-1":{"email":"dev@grok.xai","access_token":"t1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	meta, err := svc.Save(ctx, "grok", "work", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if meta.Identity != "dev@grok.xai" {
		t.Fatalf("Identity=%q want dev@grok.xai", meta.Identity)
	}
	if got, _ := st.Get("grok", "work"); got == nil {
		t.Fatal("account not persisted after Save")
	}

	// Get normalizes name.
	got, err := svc.Get(ctx, "grok", "work")
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Meta.Name != "work" {
		t.Fatalf("Get name=%q", got.Meta.Name)
	}
}

func TestCurrentReportsActiveAndLive(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	// Save one account, set it active, then Current should report it.
	authPath := os.Getenv("GROK_AUTH_PATH")
	_ = os.MkdirAll(filepath.Dir(authPath), 0o700)
	_ = os.WriteFile(authPath, []byte(`{"sess":{"email":"cur@grok.xai"}}`), 0o600)
	if _, err := svc.Save(ctx, "grok", "cur-acc", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive("grok", "cur-acc"); err != nil {
		t.Fatal(err)
	}
	cur, err := svc.Current(ctx, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Active != "cur-acc" || cur.Account == nil || cur.Account.Meta.Name != "cur-acc" {
		t.Fatalf("Current = %#v", cur)
	}
	if !cur.Live.Present {
		t.Fatalf("Live should be present")
	}
}

func TestRefreshSnapshotPreservesAccountCreation(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	authPath := os.Getenv("GROK_AUTH_PATH")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"first":{"email":"first@grok.xai"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := svc.Save(ctx, "grok", "work", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"second":{"email":"second@grok.xai"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshed, err := svc.RefreshSnapshot(ctx, "grok", "work")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.CreatedAt != original.CreatedAt {
		t.Fatalf("CreatedAt changed during refresh: %q -> %q", original.CreatedAt, refreshed.CreatedAt)
	}
	if refreshed.Identity != "second@grok.xai" {
		t.Fatalf("Identity = %q, want refreshed live identity", refreshed.Identity)
	}
	stored, err := st.Get("grok", "work")
	if err != nil || stored == nil || stored.Meta.Identity != "second@grok.xai" {
		t.Fatalf("stored refresh = %+v, err = %v", stored, err)
	}
	if _, err := svc.RefreshSnapshot(ctx, "grok", "missing"); err == nil {
		t.Fatal("refreshing a missing account must not create it")
	}
}

func TestDeleteStopsProxyAndClearsActiveAccount(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	if _, err := svc.SaveInput(ctx, "deepseek", "Work Account", CredentialInput{
		Kind: "api-key", Secret: "sk-test", Profile: "global",
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive("deepseek", "work-account"); err != nil {
		t.Fatal(err)
	}
	stopper := &recordingProxyStopper{}
	svc.Proxy = stopper

	if err := svc.Delete(ctx, "deepseek", "Work Account"); err != nil {
		t.Fatal(err)
	}
	if len(stopper.calls) != 1 || stopper.calls[0] != "deepseek/work-account" {
		t.Fatalf("proxy stop calls = %v", stopper.calls)
	}
	if account, err := st.Get("deepseek", "work-account"); err != nil || account != nil {
		t.Fatalf("deleted account = %+v, err = %v", account, err)
	}
	if active, err := st.GetActive("deepseek"); err != nil || active != "" {
		t.Fatalf("active = %q, err = %v", active, err)
	}
}

func TestDeleteAbortsWhenProxyCannotStop(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	if _, err := svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-test", Profile: "global",
	}, false); err != nil {
		t.Fatal(err)
	}
	svc.Proxy = &recordingProxyStopper{err: errors.New("stop failed")}

	if err := svc.Delete(ctx, "deepseek", "work"); err == nil {
		t.Fatal("want proxy stop failure")
	}
	if account, err := st.Get("deepseek", "work"); err != nil || account == nil {
		t.Fatalf("account was deleted after stop failure: %+v, err = %v", account, err)
	}
}

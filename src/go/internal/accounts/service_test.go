package accounts

import (
	"context"
	"errors"
	"github.com/outof0/anypick/go/internal/transfer"
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
	if got, _ := st.Get(context.Background(), "deepseek", "work"); got == nil {
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
	if got, _ := st.Get(context.Background(), "grok", "work"); got == nil {
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
	if err := st.SetActiveContext(context.Background(), "grok", "cur-acc"); err != nil {
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
	stored, err := st.Get(context.Background(), "grok", "work")
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
	if err := st.SetActiveContext(context.Background(), "deepseek", "work-account"); err != nil {
		t.Fatal(err)
	}
	stopper := &recordingProxyStopper{}
	svc.Proxy = stopper

	if _, err := svc.Delete(ctx, "deepseek", "Work Account"); err != nil {
		t.Fatal(err)
	}
	if len(stopper.calls) != 1 || stopper.calls[0] != "deepseek/work-account" {
		t.Fatalf("proxy stop calls = %v", stopper.calls)
	}
	if account, err := st.Get(context.Background(), "deepseek", "work-account"); err != nil || account != nil {
		t.Fatalf("deleted account = %+v, err = %v", account, err)
	}
	if active, err := st.GetActive(context.Background(), "deepseek"); err != nil || active != "" {
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

	if _, err := svc.Delete(ctx, "deepseek", "work"); err == nil {
		t.Fatal("want proxy stop failure")
	}
	if account, err := st.Get(context.Background(), "deepseek", "work"); err != nil || account == nil {
		t.Fatalf("account was deleted after stop failure: %+v, err = %v", account, err)
	}
}

// The cascade hook runs before the row is deleted, inside the deletion's own
// lock, and its result is reported back to the caller. Ordering is the whole
// point: a profile removed after its account is already an orphan.
func TestDeleteRunsTheCascadeBeforeRemovingTheRow(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	if _, err := svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-test", Profile: "global",
	}, false); err != nil {
		t.Fatal(err)
	}
	var sawAccountDuringCascade bool
	svc.BeforeDelete = func(c context.Context, providerID, accountName string) ([]string, error) {
		acct, err := st.Get(c, providerID, accountName)
		sawAccountDuringCascade = err == nil && acct != nil
		return []string{providerID + "/" + accountName}, nil
	}

	result, err := svc.Delete(ctx, "deepseek", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !sawAccountDuringCascade {
		t.Fatal("cascade ran after the account row was already gone")
	}
	if len(result.Profiles) != 1 || result.Profiles[0] != "deepseek/work" {
		t.Fatalf("Delete reported %#v, want the cascade result", result.Profiles)
	}
	if account, err := st.Get(context.Background(), "deepseek", "work"); err != nil || account != nil {
		t.Fatalf("account survived: %+v, %v", account, err)
	}
}

// A cascade that cannot finish must abort the deletion: the account surviving
// with its profile is recoverable, the account gone with an orphaned profile
// is the state the cascade exists to prevent.
func TestDeleteAbortsWhenTheCascadeFails(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	if _, err := svc.SaveInput(ctx, "deepseek", "work", CredentialInput{
		Kind: "api-key", Secret: "sk-test", Profile: "global",
	}, false); err != nil {
		t.Fatal(err)
	}
	cascadeErr := errors.New("profile removal failed")
	svc.BeforeDelete = func(context.Context, string, string) ([]string, error) {
		return nil, cascadeErr
	}

	if _, err := svc.Delete(ctx, "deepseek", "work"); !errors.Is(err, cascadeErr) {
		t.Fatalf("Delete = %v, want the cascade error", err)
	}
	if account, err := st.Get(context.Background(), "deepseek", "work"); err != nil || account == nil {
		t.Fatalf("account was deleted after a failed cascade: %+v, %v", account, err)
	}
}

// Use the real Codex snapshot parser: display labels are not account identities.
func saveImportCodexFixture(t *testing.T, svc *Service, name, auth string) {
	t.Helper()
	dir, _, err := svc.Store.PrepareSnapshot("codex", name)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.DiscardPreparedSnapshot(dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	pv, err := svc.Providers.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	info, err := pv.DescribeSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.WriteMetaContext(context.Background(), store.Meta{Provider: "codex", Name: name, Identity: info.Identity, Label: info.Label}, dir); err != nil {
		t.Fatal(err)
	}
}

func assertImportSnapshot(t *testing.T, svc *Service, name, want string) {
	t.Helper()
	acct, err := svc.Store.Get(context.Background(), "codex", name)
	if err != nil || acct == nil {
		t.Fatalf("missing %s: %v", name, err)
	}
	raw, err := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
	if err != nil || string(raw) != want {
		t.Fatalf("snapshot for %s differs", name)
	}
}

func TestImportCodexDistinctAPIKeysRoundTrip(t *testing.T) {
	source, _ := testService(t)
	saveImportCodexFixture(t, source, "first", `{"OPENAI_API_KEY":"sk-fake-first"}`)
	saveImportCodexFixture(t, source, "second", `{"OPENAI_API_KEY":"sk-fake-second"}`)
	ctx := context.Background()
	backup := filepath.Join(t.TempDir(), "backup.json")
	if _, err := transfer.ExportSelectedBackup(source.Store, ctx, []transfer.BackupEntry{{Provider: "codex", Name: "first"}, {Provider: "codex", Name: "second"}}, backup, false); err != nil {
		t.Fatal(err)
	}
	target, _ := testService(t)
	got, err := target.Import(ctx, ImportRequest{Paths: []string{backup}})
	if err != nil || len(got) != 2 {
		t.Fatalf("backup round trip: %v, %v", got, err)
	}
	envelope := filepath.Join(t.TempDir(), "account.json")
	if err := transfer.ExportEnvelope(source.Store, ctx, "codex", "first", envelope); err != nil {
		t.Fatal(err)
	}
	for _, named := range []bool{false, true} {
		dest, _ := testService(t)
		saveImportCodexFixture(t, dest, "second", `{"OPENAI_API_KEY":"sk-fake-second"}`)
		request := ImportRequest{Paths: []string{envelope}, ProviderID: "codex"}
		if named {
			request.Name = "renamed"
		}
		if _, err := dest.Import(ctx, request); err != nil {
			t.Fatalf("named=%v: %v", named, err)
		}
		importedName := "first"
		if named {
			importedName = "renamed"
		}
		assertImportSnapshot(t, dest, importedName, `{"OPENAI_API_KEY":"sk-fake-first"}`)
		assertImportSnapshot(t, dest, "second", `{"OPENAI_API_KEY":"sk-fake-second"}`)
	}
	for _, name := range []string{"first", "second"} {
		acct, err := target.Store.Get(ctx, "codex", name)
		if err != nil || acct == nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
		want := `{"OPENAI_API_KEY":"sk-fake-` + name + `"}`
		if err != nil || string(raw) != want {
			t.Fatalf("%s credentials did not round trip", name)
		}
	}
}

func TestImportCollisionPolicyParity(t *testing.T) {
	ctx := context.Background()
	source, _ := testService(t)
	auth := `{"tokens":{"account_id":"fake-account","access_token":"fake-access"}}`
	saveImportCodexFixture(t, source, "incoming", auth)
	envelope := filepath.Join(t.TempDir(), "account.json")
	backup := filepath.Join(t.TempDir(), "backup.json")
	if err := transfer.ExportEnvelope(source.Store, ctx, "codex", "incoming", envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.ExportSelectedBackup(source.Store, ctx, []transfer.BackupEntry{{Provider: "codex", Name: "incoming"}}, backup, false); err != nil {
		t.Fatal(err)
	}
	for _, existing := range []string{"existing", "incoming"} {
		for _, force := range []bool{false, true} {
			for _, mode := range []string{"automatic", "named", "backup", "reviewed"} {
				t.Run(existing+"/"+mode+"/force="+map[bool]string{false: "false", true: "true"}[force], func(t *testing.T) {
					dest, _ := testService(t)
					oldAuth := `{"tokens":{"account_id":"fake-account","access_token":"fake-old-access"}}`
					saveImportCodexFixture(t, dest, existing, oldAuth)
					request := ImportRequest{Paths: []string{envelope}, Force: force}
					if mode == "named" {
						request.ProviderID = "codex"
						request.Name = "incoming"
					}
					if mode == "backup" {
						request.Paths = []string{backup}
					}
					if mode == "reviewed" {
						request.Selection = []transfer.BackupEntry{{Provider: "codex", Name: "incoming"}}
					}
					got, err := dest.Import(ctx, request)
					if existing == "incoming" && force {
						if err != nil || len(got) != 1 || got[0].Name != "incoming" {
							t.Fatalf("replacement: %v, %v", got, err)
						}
					} else if err == nil {
						t.Fatal("collision succeeded")
					}
					expectedAuth := oldAuth
					if existing == "incoming" && force {
						expectedAuth = auth
					}
					assertImportSnapshot(t, dest, existing, expectedAuth)
					rows, err := dest.Store.ListAccounts(ctx, "codex")
					if err != nil || len(rows) != 1 || rows[0].Meta.Name != existing {
						t.Fatalf("collision changed account names: %v, %v", rows, err)
					}
				})
			}
		}
	}
}

func TestImportErrorExitParity(t *testing.T) {
	for _, mode := range []string{"automatic", "named", "reviewed"} {
		for _, invalid := range []string{"json", "kind", "limit"} {
			t.Run(mode+"/"+invalid, func(t *testing.T) {
				svc, _ := testService(t)
				path := filepath.Join(t.TempDir(), "invalid.json")
				raw := "{"
				if invalid == "kind" {
					raw = `{"kind":"unknown"}`
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				want := anyerr.ImportFormat
				if invalid == "limit" {
					file, err := os.OpenFile(path, os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					err = file.Truncate(transfer.MaxBackupEncodedBytes + 1)
					file.Close()
					if err != nil {
						t.Fatal(err)
					}
					want = anyerr.ImportLimit
				}
				req := ImportRequest{Paths: []string{path}}
				if mode == "named" {
					req.ProviderID = "codex"
					req.Name = "incoming"
				}
				if mode == "reviewed" {
					req.Selection = []transfer.BackupEntry{{Provider: "codex", Name: "incoming"}}
				}
				_, err := svc.Import(context.Background(), req)
				if err == nil || anyerr.ExitCodeOf(err) != want {
					t.Fatalf("error=%v exit=%v want=%v", err, anyerr.ExitCodeOf(err), want)
				}
				rows, err := svc.Store.ListAccounts(context.Background(), "codex")
				if err != nil || len(rows) != 0 {
					t.Fatal("invalid import changed store")
				}
			})
		}
	}
}

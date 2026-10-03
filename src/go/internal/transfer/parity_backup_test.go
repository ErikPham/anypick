package transfer

// Cross-runtime multi-account backup drift tests (native cutover Milestone 1,
// go PARITY M0). The TypeScript-authored fixture is committed at
// testdata/parity/multi-account-backup.json and is restored here on every run:
// any TS-side format or envelope change must fail this test before a Go
// release ships. The reverse-direction fixture (Go-authored, consumed by
// tests/parity-backup.test.ts) is regenerated explicitly with
//
//	ANYPICK_GEN_PARITY_FIXTURES=1 go test ./internal/transfer -run TestParityBackupFixtureGeneration
//
// or via `npx tsx scripts/gen-parity-fixtures.ts`. Ordinary runs never write.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/outof0/anypick/go/internal/store"
)

// repoRoot resolves the repository root from this file's location so fixture
// paths are stable regardless of the go test working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve source path")
	}
	// <repo>/go/internal/transfer/parity_backup_test.go → repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// tsFixturePath is the committed TypeScript-authored package.
func tsFixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "go", "testdata", "parity", "multi-account-backup.json")
}

// goFixturePath is the Go-authored package consumed by tests/parity-backup.test.ts.
func goFixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "tests", "parity", "multi-account-backup-go.json")
}

// parityAuthJSON matches scripts/gen-parity-fixtures.ts codexAuth() byte for
// byte so either runtime can assert exact envelope contents.
func parityAuthJSON(variant string) []byte {
	var idToken string
	switch variant {
	case "work":
		idToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJlbWFpbCI6IndvcmtAZXhhbXBsZS50ZXN0Iiwic3ViIjoiYXV0aDB8cGFyaXR5LXdvcmsifQ.sig"
	case "personal":
		idToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJlbWFpbCI6InBlcnNvbmFsQGV4YW1wbGUudGVzdCIsInN1YiI6ImF1dGgwfHBhcml0eS1wZXJzb25hbCJ9.sig"
	default:
		panic("unknown parity variant: " + variant)
	}
	return []byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"` + idToken +
		`","access_token":"parity-access-` + variant + `","account_id":"parity-acct-` + variant + `"}}`)
}

// saveParityAccount mirrors saveFakeAccount but with the deterministic parity
// credentials shared with the TypeScript fixture generator.
func saveParityAccount(t *testing.T, st *store.AccountStore, name string) {
	t.Helper()
	snapshotDir, _, err := st.PrepareSnapshot("codex", name)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshotDir, "auth.json"), parityAuthJSON(name), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := store.Meta{
		Name: name, Provider: "codex",
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
		Identity: `{"email":"` + name + `@example.test"}`, Label: name,
	}
	if err = st.WriteMetaContext(context.Background(), meta, snapshotDir); err != nil {
		t.Fatal(err)
	}
	_ = st.DiscardPreparedSnapshot(snapshotDir)
}

func TestParityBackupFixtureGeneration(t *testing.T) {
	if os.Getenv("ANYPICK_GEN_PARITY_FIXTURES") != "1" {
		t.Skip("generation-only: set ANYPICK_GEN_PARITY_FIXTURES=1 (scripts/gen-parity-fixtures.ts)")
	}
	st, _ := newTransferEnv(t)
	ctx := context.Background()
	for _, v := range []string{"work", "personal"} {
		saveParityAccount(t, st, v)
	}
	dest := goFixturePath(t)
	count, err := ExportSelectedBackup(st, ctx, []BackupEntry{
		{Provider: "codex", Name: "work"}, {Provider: "codex", Name: "personal"},
	}, dest, true)
	if err != nil || count != 2 {
		t.Fatalf("export failed: count=%d err=%v", count, err)
	}
	t.Logf("fixture: %s", dest)
}

// TestParityExternalExchange is the LIVE producer/consumer drift gate
// (review P1: frozen fixtures cannot detect producer drift). It never runs
// implicitly; scripts/parity-drift-gate.sh drives it:
//
//	PARITY_BACKUP_INPUT=f.json  → restore the CURRENT TypeScript export
//	PARITY_BACKUP_OUTPUT=g.json → export with the CURRENT Go producer
//
// The opposite runtime then consumes each artifact, so any producer-side
// format change fails the opposite suite even if committed fixtures are stale.
func TestParityExternalExchange(t *testing.T) {
	in := os.Getenv("PARITY_BACKUP_INPUT")
	out := os.Getenv("PARITY_BACKUP_OUTPUT")
	if in == "" && out == "" {
		t.Skip("drift-gate only: driven by scripts/parity-drift-gate.sh")
	}
	st, reg := newTransferEnv(t)
	ctx := context.Background()

	if out != "" {
		for _, v := range []string{"work", "personal"} {
			saveParityAccount(t, st, v)
		}
		count, err := ExportSelectedBackup(st, ctx, []BackupEntry{
			{Provider: "codex", Name: "work"}, {Provider: "codex", Name: "personal"},
		}, out, true)
		if err != nil || count != 2 {
			t.Fatalf("produce: count=%d err=%v", count, err)
		}
	}

	if in != "" {
		insp, err := InspectBackup(st, reg, in)
		if err != nil {
			t.Fatalf("current TS producer output rejected by Go inspector: %v", err)
		}
		if len(insp.Entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(insp.Entries))
		}
		metas, err := RestoreSelectedContext(t.Context(), st, reg, in, []BackupEntry{
			{Provider: "codex", Name: "work"}, {Provider: "codex", Name: "personal"},
		}, RestoreOptions{})
		if err != nil || len(metas) != 2 {
			t.Fatalf("restore of current TS export failed: metas=%d err=%v", len(metas), err)
		}
		for _, v := range []string{"work", "personal"} {
			acct, gerr := st.Get(ctx, "codex", v)
			if gerr != nil || acct == nil {
				t.Fatalf("account %s missing after restore", v)
			}
			authRaw, rerr := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
			if rerr != nil {
				t.Fatal(rerr)
			}
			var payload struct {
				Tokens struct {
					AccessToken string `json:"access_token"`
				} `json:"tokens"`
			}
			if jerr := json.Unmarshal(authRaw, &payload); jerr != nil {
				t.Fatalf("%s auth.json unreadable: %v", v, jerr)
			}
			if want := "parity-access-" + v; payload.Tokens.AccessToken != want {
				t.Fatalf("PRODUCER DRIFT: %s access token %q, want %q", v, payload.Tokens.AccessToken, want)
			}
		}
	}
}

func TestParityRestoreFromTypeScriptFixture(t *testing.T) {
	path := tsFixturePath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing TS-authored fixture (regenerate with npx tsx scripts/gen-parity-fixtures.ts): %v", err)
	}
	var pkg struct {
		Version   int    `json:"version"`
		Kind      string `json:"kind"`
		CreatedAt string `json:"createdAt"`
		Accounts  []struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
			Envelope struct {
				Version int               `json:"version"`
				Kind    string            `json:"kind"`
				Meta    json.RawMessage   `json:"meta"`
				Files   map[string]string `json:"files"`
			} `json:"envelope"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if pkg.Version != BackupVersion || pkg.Kind != BackupKind {
		t.Fatalf("version/kind drift: %d/%q", pkg.Version, pkg.Kind)
	}
	if len(pkg.Accounts) != 2 ||
		pkg.Accounts[0].Name != "personal" || pkg.Accounts[1].Name != "work" {
		t.Fatalf("unexpected accounts (selection order must be normalized): %+v", pkg.Accounts)
	}
	for _, a := range pkg.Accounts {
		if a.Envelope.Version != 1 || a.Envelope.Kind != "anypick-account" {
			t.Fatalf("envelope drift in %s: %d/%q", a.Name, a.Envelope.Version, a.Envelope.Kind)
		}
		if a.Envelope.Files["auth.json"] == "" {
			t.Fatalf("account %s lost auth.json", a.Name)
		}
	}

	st, reg := newTransferEnv(t)
	ctx := context.Background()

	insp, err := InspectBackup(st, reg, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(insp.Entries) != 2 {
		t.Fatalf("entries = %+v", insp.Entries)
	}
	for _, e := range insp.Entries {
		if e.Disposition != DispositionCreate {
			t.Fatalf("fresh root should offer create, got %+v", e)
		}
	}

	// Restoring one entry must never expand to the whole package.
	metas, err := RestoreSelectedContext(t.Context(), st, reg, path,
		[]BackupEntry{{Provider: "codex", Name: "work"}}, RestoreOptions{})
	if err != nil || len(metas) != 1 {
		t.Fatalf("single-entry restore: metas=%d err=%v", len(metas), err)
	}
	if acct, gerr := st.Get(ctx, "codex", "personal"); gerr != nil {
		t.Fatalf("lookup failed: %v", gerr)
	} else if acct != nil {
		t.Fatal("selection silently expanded to all credential-bearing accounts")
	}

	// Full restore onto a fresh root lands both accounts with intact envelope
	// bytes. (Re-restoring over the just-restored 'work' would correctly demand
	// explicit replacement approval — covered by TestBackupExportInspectRestoreAtomic.)
	st2, reg2 := newTransferEnv(t)
	metas, err2 := RestoreSelectedContext(t.Context(), st2, reg2, path, []BackupEntry{
		{Provider: "codex", Name: "work"}, {Provider: "codex", Name: "personal"},
	}, RestoreOptions{})
	if err2 != nil || len(metas) != 2 {
		t.Fatalf("full restore: metas=%d err=%v", len(metas), err2)
	}
	for _, v := range []string{"work", "personal"} {
		acct, gerr := st2.Get(ctx, "codex", v)
		if gerr != nil {
			t.Fatalf("account %s missing after restore: %v", v, gerr)
		}
		authRaw, rerr := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		var payload struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
			} `json:"tokens"`
		}
		if jerr := json.Unmarshal(authRaw, &payload); jerr != nil {
			t.Fatalf("%s auth.json unreadable: %v (%s)", v, jerr, authRaw)
		}
		if want := "parity-access-" + v; payload.Tokens.AccessToken != want {
			t.Fatalf("%s access token drifted: got %q want %q", v, payload.Tokens.AccessToken, want)
		}
	}
}

package transfer

// Cross-runtime backup drift (native cutover Milestone 1, go PARITY M0).
//
// The TypeScript engine was removed with ADR 0020, so the TS side of this
// exchange no longer runs. What remains, and why it is still here:
// testdata/parity/multi-account-backup.json is the last artifact the TS
// producer wrote, and TestParityRestoreFromTypeScriptFixture restores it on
// every run — it pins the Go reader against the envelope shape real TS-era
// users have on disk, which is exactly what a format change must not break.
// The Go-authored fixture and the scripts that generated and exchanged it were
// deleted with their consumer; a frozen fixture for a runtime that cannot run
// proves nothing.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

// saveParityAccount mirrors saveFakeAccount but with the deterministic parity
// credentials shared with the TypeScript fixture generator.

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

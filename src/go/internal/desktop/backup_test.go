package desktop

// Regression tests for the desktop backup protocol: selective export honors
// `accounts`, and import resolves a staged inspection by inspectionId with
// entryIds + overwriteConfirmed — never a raw path.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transfer"
)

func writeTestAccount(t *testing.T, env *trayEnv, providerID, name string) {
	t.Helper()
	snapshotDir, _, err := env.Deps.Store.PrepareSnapshot(providerID, name)
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(filepath.Join(snapshotDir, "auth.json"),
		[]byte(`{"tokens":{"access_token":"`+name+`"}}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	meta := accountMetaForTest(providerID, name)
	if werr := env.Deps.Store.WriteMetaContext(context.Background(), meta, snapshotDir); werr != nil {
		t.Fatal(werr)
	}
}

func TestBackupExportHonoursSelectedAccounts(t *testing.T) {
	env := newTrayEnv(t)
	writeTestAccount(t, env, "codex", "keep")
	writeTestAccount(t, env, "claude", "secret-one")

	dest := filepath.Join(t.TempDir(), "pkg.anypick-backup")
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	payload, _ := json.Marshal(Mutate{
		Version: 1, RequestID: "exp1", Operation: "account-backup-export",
		Path: dest,
		Accounts: []BackupAccountRef{
			{Provider: "codex", Name: "keep"},
		},
		Overwrite: true,
	})
	if err := srv.handle(context.Background(), &Command{Kind: "mutate", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("selective export produced no package: %v", err)
	}
	var pkg struct {
		Accounts []struct {
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if json.Unmarshal(raw, &pkg) != nil {
		t.Fatalf("package unreadable: %s", raw)
	}
	if len(pkg.Accounts) != 1 || pkg.Accounts[0].Name != "keep" {
		t.Fatalf("export did not honour the selection: %+v", pkg.Accounts)
	}
}

func TestBackupImportViaInspectionFlow(t *testing.T) {
	env := newTrayEnv(t)
	writeTestAccount(t, env, "codex", "alpha")

	// Build the package outside this root.
	srcStore := env.Deps.Store
	pkgPath := filepath.Join(t.TempDir(), "pkg.anypick-backup")
	_, err := transfer.ExportSelectedBackup(srcStore, context.Background(),
		[]transfer.BackupEntry{{Provider: "codex", Name: "alpha"}}, pkgPath, false)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the local account so restore lands as `create`.
	if _, gerr := srcStore.Get("codex", "alpha"); gerr == nil {
		_ = srcStore.DeleteContext(context.Background(), "codex", "alpha")
	}

	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	ctx := context.Background()

	inspectPayload, _ := json.Marshal(backupInspectPayload{
		Version: 1, RequestID: "ins1", Path: pkgPath,
	})
	if herr := srv.handle(ctx, &Command{Kind: "backup-inspect", Payload: inspectPayload}); herr != nil {
		t.Fatal(herr)
	}
	line := firstFrameLine(out.String())
	if !strings.HasPrefix(line, "backup-preview\t") {
		t.Fatalf("want backup-preview frame, got %q", line)
	}
	var preview BackupPreviewResult
	if jerr := json.Unmarshal(decodeFramePayload(t, line), &preview); jerr != nil {
		t.Fatal(jerr)
	}
	if preview.Status != "ready" || preview.InspectionID == "" || len(preview.Entries) != 1 {
		t.Fatalf("preview wrong: %+v", preview)
	}
	// The native app drops previews whose requestId does not match.
	if preview.RequestID != "ins1" || preview.Version != ProtocolVersion {
		t.Fatalf("preview lost requestId: %+v", preview)
	}
	if preview.Entries[0].Disposition != "create" {
		t.Fatalf("disposition = %q, want create", preview.Entries[0].Disposition)
	}

	out.Reset()
	importPayload, _ := json.Marshal(Mutate{
		Version: 1, RequestID: "imp1", Operation: "account-backup-import",
		InspectionID:       preview.InspectionID,
		EntryIDs:           []string{preview.Entries[0].ID},
		OverwriteConfirmed: true,
	})
	if herr := srv.handle(ctx, &Command{Kind: "mutate", Payload: importPayload}); herr != nil {
		t.Fatal(herr)
	}
	resLine := firstFrameLine(out.String())
	if !strings.HasPrefix(resLine, "result\t") {
		t.Fatalf("want result frame, got %q", resLine)
	}
	var res Result
	if jerr := json.Unmarshal(decodeFramePayload(t, resLine), &res); jerr != nil {
		t.Fatal(jerr)
	}
	if res.Status != "success" {
		t.Fatalf("import failed: %+v", res)
	}
	acct, _ := srcStore.Get("codex", "alpha")
	if acct == nil {
		t.Fatal("restored account missing after import")
	}
}

func accountMetaForTest(providerID, name string) store.Meta {
	return store.Meta{
		Name: name, Provider: providerID,
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
}

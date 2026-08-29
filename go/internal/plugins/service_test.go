package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
)

const testEntry = "export default { activate() {} };\n"

func newServiceForTest(t *testing.T) (*Service, *Store, string) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := &Store{DB: database.DB}
	return NewService(store, root, filepath.Join(root, "bundled")), store, root
}

func writeTestPlugin(t *testing.T, root, dirName, name string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(testEntry), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestServiceLifecycleAndRuntimeInspection(t *testing.T) {
	service, store, root := newServiceForTest(t)
	ctx := context.Background()
	dir := writeTestPlugin(t, root, "acme", "acme-plugin")

	added, err := service.Add(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if added.Enabled || len(added.Digest) != 64 {
		t.Fatalf("new plugin must be disabled with digest: %+v", added)
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || records[0] != added {
		t.Fatalf("stored records = %+v err=%v", records, err)
	}

	enabled, err := service.SetEnabled(ctx, added.Name, true)
	if err != nil || !enabled.Enabled {
		t.Fatalf("enabled = %+v err=%v", enabled, err)
	}
	runtime, err := service.Inspect(ctx)
	if err != nil || len(runtime.Candidates) != 1 || len(runtime.Failures) != 0 {
		t.Fatalf("runtime = %+v err=%v", runtime, err)
	}
	if runtime.Candidates[0].Entry != filepath.Join(dir, "index.mjs") {
		t.Fatalf("candidate entry = %q", runtime.Candidates[0].Entry)
	}

	if err := os.WriteFile(filepath.Join(dir, "helper.mjs"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(
		`{"name":"acme-plugin","version":"2.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err = service.Inspect(ctx)
	if err != nil || len(runtime.Candidates) != 0 || len(runtime.Failures) != 1 ||
		!runtime.Failures[0].Untrusted {
		t.Fatalf("tampered runtime = %+v err=%v", runtime, err)
	}

	trusted, err := service.Trust(ctx, added.Name)
	if err != nil {
		t.Fatal(err)
	}
	if trusted.PreviousDigest != added.Digest || trusted.Record.Digest == added.Digest ||
		!trusted.Record.Enabled || trusted.Record.Version != "2.0.0" {
		t.Fatalf("trust result = %+v", trusted)
	}
	runtime, err = service.Inspect(ctx)
	if err != nil || len(runtime.Candidates) != 1 || len(runtime.Failures) != 0 {
		t.Fatalf("trusted runtime = %+v err=%v", runtime, err)
	}

	if err := service.Remove(ctx, added.Name); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, added.Name); err == nil || anyerr.ExitCodeOf(err) != anyerr.NotFound {
		t.Fatalf("missing remove error = %v", err)
	}
}

func TestServiceConflictReAddAndBundledResolution(t *testing.T) {
	service, _, root := newServiceForTest(t)
	ctx := context.Background()
	dir := writeTestPlugin(t, root, "first", "acme-plugin")
	first, err := service.Add(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(ctx, first.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(testEntry+"// v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readded, err := service.Add(ctx, dir)
	if err != nil || !readded.Enabled || readded.AddedAt != first.AddedAt || readded.Digest == first.Digest {
		t.Fatalf("re-added = %+v err=%v", readded, err)
	}

	impostor := writeTestPlugin(t, root, "second", "acme-plugin")
	if _, err := service.Add(ctx, impostor); err == nil {
		t.Fatal("second directory under same name was accepted")
	} else {
		var structured *anyerr.Error
		if !errors.As(err, &structured) || structured.Code != "PLUGIN_CONFLICT" ||
			structured.Details["installedPath"] != dir {
			t.Fatalf("conflict error = %#v", err)
		}
	}

	bundled := writeTestPlugin(t, filepath.Join(root, "bundled"), "popular-providers", "popular-providers")
	resolved, err := service.ResolvePluginDirectory("bundled:popular-providers")
	if err != nil || resolved != bundled {
		t.Fatalf("bundled path = %q err=%v, want %q", resolved, err, bundled)
	}
	if _, err := service.ResolvePluginDirectory("bundled:../escape"); err == nil ||
		anyerr.ExitCodeOf(err) != anyerr.InvalidUsage {
		t.Fatalf("invalid bundled name error = %v", err)
	}
}

func TestEveryMutationCanReenterCoordinatorScope(t *testing.T) {
	service, _, root := newServiceForTest(t)
	dir := writeTestPlugin(t, root, "acme", "acme-plugin")
	err := lock.WithMutationLock(context.Background(), root, MutationScope, func(held context.Context) error {
		added, err := service.Add(held, dir)
		if err != nil {
			return err
		}
		if _, err = service.SetEnabled(held, added.Name, true); err != nil {
			return err
		}
		if _, err = service.Trust(held, added.Name); err != nil {
			return err
		}
		if _, err = service.SetEnabled(held, added.Name, false); err != nil {
			return err
		}
		return service.Remove(held, added.Name)
	})
	if err != nil {
		t.Fatalf("nested coordinator mutation failed: %v", err)
	}
}

func assertPluginMutationWaitsForCoordinator(
	t *testing.T,
	root string,
	mutation func() error,
) {
	t.Helper()
	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- lock.WithMutationLock(
			context.Background(), root, MutationScope,
			func(context.Context) error {
				close(held)
				<-release
				return nil
			},
		)
	}()
	<-held

	mutationDone := make(chan error, 1)
	go func() { mutationDone <- mutation() }()
	select {
	case err := <-mutationDone:
		close(release)
		<-holderDone
		t.Fatalf("mutation escaped the plugins coordinator scope: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
}

func TestEveryPersistedMutationUsesCoordinatorScope(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		service, _, root := newServiceForTest(t)
		dir := writeTestPlugin(t, root, "add", "add-plugin")
		assertPluginMutationWaitsForCoordinator(t, root, func() error {
			_, err := service.Add(context.Background(), dir)
			return err
		})
	})

	for _, operation := range []string{"enable", "disable", "trust", "remove"} {
		t.Run(operation, func(t *testing.T) {
			service, _, root := newServiceForTest(t)
			dir := writeTestPlugin(t, root, "plugin", "scope-plugin")
			added, err := service.Add(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "disable" {
				if _, err := service.SetEnabled(context.Background(), added.Name, true); err != nil {
					t.Fatal(err)
				}
			}
			assertPluginMutationWaitsForCoordinator(t, root, func() error {
				switch operation {
				case "enable":
					_, err = service.SetEnabled(context.Background(), added.Name, true)
				case "disable":
					_, err = service.SetEnabled(context.Background(), added.Name, false)
				case "trust":
					_, err = service.Trust(context.Background(), added.Name)
				case "remove":
					err = service.Remove(context.Background(), added.Name)
				}
				return err
			})
		})
	}
}

func TestStoreUpsertPreservesOriginalAddedAt(t *testing.T) {
	_, store, _ := newServiceForTest(t)
	ctx := context.Background()
	original := Record{
		Name: "store-plugin", Path: "/first", Version: "1", Digest: "one",
		AddedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	if err := store.Upsert(ctx, original); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Path = "/second"
	updated.Version = "2"
	updated.Enabled = true
	updated.Digest = "two"
	updated.AddedAt = "must-not-replace-original"
	updated.UpdatedAt = "2026-01-02T00:00:00.000Z"
	if err := store.Upsert(ctx, updated); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, original.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.AddedAt != original.AddedAt || got.Path != updated.Path ||
		got.Version != updated.Version || !got.Enabled || got.Digest != updated.Digest ||
		got.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("updated record = %+v", got)
	}
}

func TestInspectCollectsFailuresAndNeverExecutesJavaScript(t *testing.T) {
	service, store, root := newServiceForTest(t)
	ctx := context.Background()
	dir := writeTestPlugin(t, root, "throws", "throws-plugin")
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"),
		[]byte(`throw new Error("must never execute in Go");`), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := service.Add(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(ctx, record.Name, true); err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Inspect(ctx)
	if err != nil || len(runtime.Candidates) != 1 || len(runtime.Failures) != 0 {
		t.Fatalf("inert JS candidate = %+v err=%v", runtime, err)
	}

	broken := Record{
		Name: "broken-plugin", Path: filepath.Join(root, "missing"), Version: "1",
		Enabled: true, Digest: "deadbeef", AddedAt: nowISO(), UpdatedAt: nowISO(),
	}
	if err := store.Upsert(ctx, broken); err != nil {
		t.Fatal(err)
	}
	runtime, err = service.Inspect(ctx)
	if err != nil || len(runtime.Candidates) != 1 || len(runtime.Failures) != 1 ||
		runtime.Failures[0].Name != broken.Name || runtime.Failures[0].Untrusted {
		t.Fatalf("non-fatal failure collection = %+v err=%v", runtime, err)
	}
}

func TestInspectRefusesChangedIdentityAndEscapingEntryWithoutAborting(t *testing.T) {
	service, _, root := newServiceForTest(t)
	ctx := context.Background()

	renameDir := writeTestPlugin(t, root, "renamed", "rename-plugin")
	renameRecord, err := service.Add(ctx, renameDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(ctx, renameRecord.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renameDir, ManifestFile), []byte(
		`{"name":"other-plugin","version":"2","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Trust(ctx, renameRecord.Name); err != nil {
		t.Fatal(err)
	}

	escapeDir := writeTestPlugin(t, root, "escape", "escape-plugin")
	escapeRecord, err := service.Add(ctx, escapeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(ctx, escapeRecord.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(escapeDir, ManifestFile), []byte(
		`{"name":"escape-plugin","version":"2","apiVersion":1,"main":"../../outside.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Trust(ctx, escapeRecord.Name); err != nil {
		t.Fatal(err)
	}

	runtime, err := service.Inspect(ctx)
	if err != nil {
		t.Fatalf("Inspect aborted instead of collecting plugin failures: %v", err)
	}
	if len(runtime.Candidates) != 0 || len(runtime.Failures) != 2 {
		t.Fatalf("runtime = %+v", runtime)
	}
	failures := map[string]LoadFailure{}
	for _, failure := range runtime.Failures {
		failures[failure.Name] = failure
	}
	if failure := failures[renameRecord.Name]; failure.Untrusted ||
		!strings.Contains(failure.Reason, "now calls itself") {
		t.Fatalf("renamed failure = %+v", failure)
	}
	if failure := failures[escapeRecord.Name]; failure.Untrusted ||
		!strings.Contains(failure.Reason, "outside the plugin directory") {
		t.Fatalf("escaping entry failure = %+v", failure)
	}
}

func TestDisabledByEnvExactValues(t *testing.T) {
	for _, value := range []string{"1", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ANYPICK_NO_PLUGINS", value)
			if !DisabledByEnv() {
				t.Fatalf("%q did not disable plugins", value)
			}
		})
	}
	for _, value := range []string{"", "TRUE", "yes", "0"} {
		t.Run("enabled-"+value, func(t *testing.T) {
			t.Setenv("ANYPICK_NO_PLUGINS", value)
			if DisabledByEnv() {
				t.Fatalf("%q unexpectedly disabled plugins", value)
			}
		})
	}
}

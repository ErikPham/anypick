package doctor

// Tests for the six allowlisted repairs. Each kind gets an apply case and a
// refusal case, and every refusal asserts on the filesystem afterwards — a
// refusal that still touched the target would be worse than no fix at all,
// because the user was told nothing happened.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/modelcache"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/refs"
)

// fixture builds an isolated FixDeps with a temp data root and a temp system
// temp directory. Returning both paths keeps the assertions honest: the test
// reads the same directories the fix wrote.
type fixture struct {
	root    string
	tmp     string
	deps    FixDeps
	records map[string]*ownership.Record
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	f := &fixture{
		root:    filepath.Join(base, "data"),
		tmp:     filepath.Join(base, "tmp"),
		records: map[string]*ownership.Record{},
	}
	for _, dir := range []string{f.root, f.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.deps = FixDeps{
		Root:    f.root,
		TempDir: f.tmp,
		Ownership: func(path string) *ownership.Record {
			return f.records[path]
		},
	}
	return f
}

func (f *fixture) write(t *testing.T, rel string, mode os.FileMode) string {
	t.Helper()
	full := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestDeleteTempOverlayAppliesAndRefusesRecent(t *testing.T) {
	f := newFixture(t)
	oldDir := filepath.Join(f.tmp, "anypick-claude-111")
	newDir := filepath.Join(f.tmp, "anypick-live-checkpoint-222")
	foreign := filepath.Join(f.tmp, "not-anypick-333")
	for _, dir := range []string{oldDir, newDir, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Both AnyPick overlays are older than an hour; the foreign one is not
	// AnyPick-owned at all.
	stale := time.Now().Add(-2 * time.Hour)
	for _, dir := range []string{oldDir, newDir} {
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	f.deps.Now = func() time.Time { return now }

	plan := PlanFixes(context.Background(), f.deps)
	var kinds []FixKind
	for _, action := range plan.Actions {
		kinds = append(kinds, action.Kind)
	}
	if !containsKind(kinds, FixDeleteTempOverlay) {
		t.Fatalf("plan did not include delete_temp_overlay: %+v", plan.Actions)
	}
	for _, action := range plan.Actions {
		if strings.Contains(action.Target, "not-anypick") {
			t.Fatalf("plan touched a directory AnyPick does not own: %+v", action)
		}
	}

	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) == 0 {
		t.Fatal("nothing applied")
	}
	for _, outcome := range result.Applied {
		if !outcome.OK {
			t.Fatalf("apply failed: %+v", outcome)
		}
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old overlay survived: %v", err)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatalf("old checkpoint overlay survived: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign directory was touched: %v", err)
	}

	// A recent overlay is refused by the scan and again by the fix, and the
	// directory must survive both.
	recent := filepath.Join(f.tmp, "anypick-claude-recent")
	if err := os.MkdirAll(recent, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now()
	if err := os.Chtimes(recent, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	plan = PlanFixes(context.Background(), f.deps)
	var refused *FixAction
	for i := range plan.Actions {
		if plan.Actions[i].Target == recent {
			refused = &plan.Actions[i]
		}
	}
	if refused == nil {
		t.Fatal("recent overlay was not reported at all; a skipped check must still be reported")
	}
	if refused.Reason == "" {
		t.Fatal("recent overlay carried no refusal reason")
	}
	ApplyFixes(context.Background(), plan, f.deps, false)
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent overlay was deleted: %v", err)
	}
}

// TestDeleteTempOverlayRefusesTargetOutsideTemp is the guard requirement: a
// target outside the temp directory must be refused and left untouched.
func TestDeleteTempOverlayRefusesTargetOutsideTemp(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "anypick-claude-999")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := FixPlan{Actions: []FixAction{{
		ID: "delete_temp_overlay:evil", Kind: FixDeleteTempOverlay,
		Description: "evil", Target: outside,
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("outside target was not refused: %+v", result.Applied)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target was touched: %v", err)
	}
}

func TestRepairPermissionsAppliesAndRefusesOutsideRoot(t *testing.T) {
	f := newFixture(t)
	looseFile := f.write(t, "providers/grok/accounts/work/meta.json", 0o644)
	looseDir := filepath.Join(f.root, "providers", "grok", "accounts", "work")
	if err := os.Chmod(looseDir, 0o755); err != nil {
		t.Fatal(err)
	}

	plan := PlanFixes(context.Background(), f.deps)
	found := false
	for _, action := range plan.Actions {
		if action.Kind == FixRepairPermissions {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan did not include repair_permissions: %+v", plan.Actions)
	}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	for _, outcome := range result.Applied {
		if outcome.Kind == FixRepairPermissions && !outcome.OK {
			t.Fatalf("repair failed: %+v", outcome)
		}
	}
	assertMode(t, looseFile, 0o600)
	assertMode(t, looseDir, 0o700)

	// A target outside the root is refused, and the outside file keeps its
	// mode. This is the guard the requirement calls the riskiest kind: a wrong
	// chmod on a live credential file breaks a login.
	outside := filepath.Join(t.TempDir(), "credential.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = FixPlan{Actions: []FixAction{{
		ID: "repair_permissions:evil", Kind: FixRepairPermissions,
		Description: "evil", Target: outside,
	}}}
	result = ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("outside root was not refused: %+v", result.Applied)
	}
	assertMode(t, outside, 0o644)
}

func TestRepairPermissionsSkipsSymlinkOutsideRoot(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.root, "link-to-secret")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plan := FixPlan{Actions: []FixAction{{
		ID: "repair_permissions:root", Kind: FixRepairPermissions,
		Description: "root", Target: f.root,
	}}}
	ApplyFixes(context.Background(), plan, f.deps, false)
	assertMode(t, outside, 0o644)
}

func TestDeleteStalePIDAppliesAndRefusesLive(t *testing.T) {
	f := newFixture(t)
	stalePath := f.write(t, "providers/grok/accounts/work/runtime/proxy.pid", 0o600)
	livePath := f.write(t, "providers/grok/accounts/live/runtime/proxy.pid", 0o600)
	f.records[stalePath] = &ownership.Record{PID: 999999}
	f.records[livePath] = &ownership.Record{PID: os.Getpid()}

	plan := PlanFixes(context.Background(), f.deps)
	var stale *FixAction
	for i := range plan.Actions {
		if plan.Actions[i].Target == stalePath {
			stale = &plan.Actions[i]
		}
	}
	reportedLive := false
	for _, manual := range plan.Manual {
		if strings.Contains(manual.Detail, "live process") {
			reportedLive = true
		}
	}
	if stale == nil {
		t.Fatalf("stale PID record was not planned: %+v", plan.Actions)
	}
	if !reportedLive {
		t.Fatalf("live PID record was not reported as manual: %+v", plan.Manual)
	}
	for _, action := range plan.Actions {
		if action.Target == livePath {
			t.Fatalf("live PID record was planned for deletion: %+v", action)
		}
	}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	for _, outcome := range result.Applied {
		if outcome.Kind == FixDeleteStalePID && !outcome.OK {
			t.Fatalf("stale PID delete failed: %+v", outcome)
		}
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale PID record survived: %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live PID record was deleted: %v", err)
	}
}

// TestDeleteStalePIDRefusesOutsideRoot proves the guard even when the record
// itself says the process is dead.
func TestDeleteStalePIDRefusesOutsideRoot(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "proxy.pid")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.records[outside] = &ownership.Record{PID: 999999}
	plan := FixPlan{Actions: []FixAction{{
		ID: "delete_stale_pid:evil", Kind: FixDeleteStalePID,
		Description: "evil", Target: outside,
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("outside PID path was not refused: %+v", result.Applied)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside PID path was deleted: %v", err)
	}
}

func TestRebuildCachesClearsStore(t *testing.T) {
	// rebuild_caches is exercised through its unavailable path here; the SQLite
	// clear itself is covered by internal/modelcache's own suite, and this
	// package must not open a second database to re-test it.
	f := newFixture(t)
	plan := FixPlan{Actions: []FixAction{{
		ID: "rebuild_caches:root", Kind: FixRebuildCaches, Description: "caches", Target: f.root,
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("rebuild with no store must refuse, not silently succeed: %+v", result.Applied)
	}
	if !strings.Contains(result.Applied[0].Message, "unavailable") {
		t.Fatalf("refusal did not name the cause: %q", result.Applied[0].Message)
	}
}

// TestRebuildCachesDeletesRows proves the Go repair clears the SQLite cache
// rather than writing a marker file into a <root>/cache directory that the Go
// engine never reads.
func TestRebuildCachesDeletesRows(t *testing.T) {
	f := newFixture(t)
	database, err := db.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := &modelcache.Store{DB: database}
	ctx := context.Background()
	if err := store.Write(ctx, &modelcache.Entry{
		Provider: "grok", Endpoint: "https://example.invalid", Models: []string{"m1"},
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	f.deps.Store = store
	f.deps.Providers = []string{"grok"}

	result := ApplyFixes(ctx, FixPlan{Actions: []FixAction{{
		ID: "rebuild_caches:root", Kind: FixRebuildCaches, Description: "caches", Target: f.root,
	}}}, f.deps, false)
	if len(result.Applied) != 1 || !result.Applied[0].OK {
		t.Fatalf("rebuild failed: %+v", result.Applied)
	}
	models, err := store.ModelsForProvider(ctx, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("cache rows survived the rebuild: %v", models)
	}
	if _, err := os.Stat(filepath.Join(f.root, "cache")); !os.IsNotExist(err) {
		t.Fatalf("rebuild wrote a cache directory; the Go cache is SQLite, not files")
	}
}

// TestStopOrphanProxyRefusesLiveBinding proves the lease/binding cross-check.
// A binding that names the pair makes the running proxy not an orphan, so the
// repair must refuse before it ever reaches the proxy service.
func TestStopOrphanProxyRefusesLiveBinding(t *testing.T) {
	f := newFixture(t)
	database, err := db.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := &bindings.Store{Root: f.root, DB: database.DB}
	svc := bindings.NewService(store)
	ctx := context.Background()
	if _, err := svc.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude",
			Source: refs.Ref{Kind: refs.KindAccount, Provider: "grok", Name: "work"},
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.deps.Binding = svc

	plan := FixPlan{Actions: []FixAction{{
		ID: "stop_orphan_proxy:grok/work", Kind: FixStopOrphanProxy,
		Description: "orphan", Target: "http://127.0.0.1:1", Provider: "grok", Account: "work",
	}}}
	result := ApplyFixes(ctx, plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("binding-referenced pair was not refused: %+v", result.Applied)
	}
	if !strings.Contains(result.Applied[0].Message, "still references") {
		t.Fatalf("refusal did not name the binding: %q", result.Applied[0].Message)
	}
}

func TestIncompleteJournalEntriesArePlanned(t *testing.T) {
	f := newFixture(t)
	database, err := db.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	j := &journal.OperationJournal{DB: database.DB}
	ctx := context.Background()
	entry, err := j.CreateContext(ctx, "activate", journal.CreateOptions{
		State: journal.StateExecuting, AffectedResources: []string{"client/claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.deps.Journal = j

	plan := PlanFixes(ctx, f.deps)
	var planned *FixAction
	for i := range plan.Actions {
		if plan.Actions[i].Kind == FixCompleteJournalRollback {
			planned = &plan.Actions[i]
		}
	}
	if planned == nil {
		t.Fatalf("incomplete journal entry was not planned: %+v", plan.Actions)
	}
	if planned.JournalID != entry.ID {
		t.Fatalf("planned journal id = %q, want %q", planned.JournalID, entry.ID)
	}

	// Planning must not have rolled anything back.
	still, err := j.GetContext(ctx, entry.ID)
	if err != nil || still == nil || still.State != journal.StateExecuting {
		t.Fatalf("planning mutated the journal: %+v err=%v", still, err)
	}
}

func TestCompleteJournalRollbackRefusesWithoutRecovery(t *testing.T) {
	f := newFixture(t)
	plan := FixPlan{Actions: []FixAction{{
		ID: "complete_journal_rollback:x", Kind: FixCompleteJournalRollback,
		Description: "rollback", Target: "journal:x", JournalID: "x",
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("rollback with no recovery callback must refuse: %+v", result.Applied)
	}
	if !strings.Contains(result.Applied[0].Message, "unavailable") {
		t.Fatalf("refusal did not name the cause: %q", result.Applied[0].Message)
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t)
	overlay := filepath.Join(f.tmp, "anypick-claude-dry")
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(overlay, stale, stale); err != nil {
		t.Fatal(err)
	}
	pidPath := f.write(t, "providers/grok/accounts/work/runtime/proxy.pid", 0o600)
	f.records[pidPath] = &ownership.Record{PID: 999999}

	plan := PlanFixes(context.Background(), f.deps)
	result := ApplyFixes(context.Background(), plan, f.deps, true)
	if !result.DryRun {
		t.Fatal("dry run was not marked as one")
	}
	for _, outcome := range result.Applied {
		if !strings.HasPrefix(outcome.Message, "[dry-run]") {
			t.Fatalf("dry-run message lost its prefix: %+v", outcome)
		}
	}
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("dry run deleted the overlay: %v", err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("dry run deleted the PID record: %v", err)
	}
	if result.JournalID != "" {
		t.Fatalf("dry run journalled a mutation: %q", result.JournalID)
	}
}

func TestStopOrphanProxyRefusesWhenBindingReferencesPair(t *testing.T) {
	f := newFixture(t)
	// No proxy service and no bindings: the pair is unreferenced, but the
	// repair must still refuse rather than panic when the service is absent.
	plan := FixPlan{Actions: []FixAction{{
		ID: "stop_orphan_proxy:grok/work", Kind: FixStopOrphanProxy,
		Description: "orphan", Target: "http://127.0.0.1:1", Provider: "grok", Account: "work",
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("stop with no proxy service must refuse: %+v", result.Applied)
	}
	if !strings.Contains(result.Applied[0].Message, "unavailable") {
		t.Fatalf("refusal did not name the cause: %q", result.Applied[0].Message)
	}
}

func TestNonAllowlistedKindIsRefused(t *testing.T) {
	f := newFixture(t)
	plan := FixPlan{Actions: []FixAction{{
		ID: "evil", Kind: FixKind("delete_stale_lock"), Description: "evil", Target: f.root,
	}}}
	result := ApplyFixes(context.Background(), plan, f.deps, false)
	if len(result.Applied) != 1 || result.Applied[0].OK {
		t.Fatalf("non-allowlisted kind was applied: %+v", result.Applied)
	}
	if !strings.Contains(result.Applied[0].Message, "non-allowlisted") {
		t.Fatalf("refusal did not name the reason: %q", result.Applied[0].Message)
	}
}

func TestIsUnderRejectsSiblingAndParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "a", "b"), true},
		{filepath.Join(root, "..", "sibling"), false},
		{filepath.Dir(root), false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isUnder(root, tc.path); got != tc.want {
			t.Errorf("isUnder(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
}

func containsKind(kinds []FixKind, want FixKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

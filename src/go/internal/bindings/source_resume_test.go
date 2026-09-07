package bindings

// Source-resume history regressions: successful source-specific setups are
// recorded for resume, preset pointers are never retained, and listing is
// newest-first with corrupt rows skipped — matching binding-store.ts.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/outof0/anypick/go/internal/refs"
)

func TestRecordAndListSourceResume(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()

	spec := spec() // grok/work via AccountRef
	if err := s.RecordSourceResumeContext(ctx, spec); err != nil {
		t.Fatal(err)
	}
	resumes, err := s.ListSourceResumesContext(ctx, refs.AccountRef("grok", "work"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resumes) != 1 || resumes[0].Client != "claude" {
		t.Fatalf("resumes = %+v", resumes)
	}

	// Upsert on the same (source_ref, client): one row, updated spec.
	spec.TransportPolicy = "proxy-only"
	if err := s.RecordSourceResumeContext(ctx, spec); err != nil {
		t.Fatal(err)
	}
	resumes, _ = s.ListSourceResumesContext(ctx, refs.AccountRef("grok", "work"))
	if len(resumes) != 1 || resumes[0].Spec.TransportPolicy != "proxy-only" {
		t.Fatalf("upsert failed: %+v", resumes)
	}
}

func TestSourceResumeSkipsPresetPointers(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()

	preset := Spec{
		Client:          "codex",
		Source:          refs.Ref{Kind: refs.KindPreset, Name: "team"},
		Model:           OmittedModel(),
		TransportPolicy: "auto",
	}
	if err := s.RecordSourceResumeContext(ctx, preset); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListSourceResumesContext(ctx, refs.Ref{Kind: refs.KindPreset, Name: "team"}); len(got) != 0 {
		t.Fatalf("preset history must never be recorded: %+v", got)
	}
	var count int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM source_resumes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("preset row leaked into history: %d", count)
	}
}

func TestSourceResumeListingOrderAndCorruptRows(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()

	for _, client := range []string{"claude", "codex"} {
		sp := spec()
		sp.Client = client
		if err := s.RecordSourceResumeContext(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
	// Force distinct updated_at ordering: codex newest.
	if _, err := database.DB.Exec(
		`UPDATE source_resumes SET updated_at = '2026-01-01T00:00:00.000Z' WHERE client_id = 'claude'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(
		`UPDATE source_resumes SET updated_at = '2026-02-01T00:00:00.000Z' WHERE client_id = 'codex'`); err != nil {
		t.Fatal(err)
	}

	// Corrupt one row's JSON; it must be skipped, not fatal.
	if _, err := database.DB.Exec(
		`UPDATE source_resumes SET spec_json = '{bogus' WHERE client_id = 'claude'`); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListSourceResumesContext(ctx, refs.AccountRef("grok", "work"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Client != "codex" {
		t.Fatalf("expected only the valid newest row: %+v", got)
	}
}

func TestSourceResumeJSONRoundTripsAgainstTS(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	sp := spec()
	if err := s.RecordSourceResumeContext(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := database.DB.QueryRow(
		`SELECT spec_json FROM source_resumes WHERE client_id = 'claude'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stored), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"client", "source", "model", "transportPolicy"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("spec_json missing %q (TS BindingSpec shape drift): %s", key, stored)
		}
	}
}

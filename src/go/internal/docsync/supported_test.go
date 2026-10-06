package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadmeSupportedMatchesRegistry fails when README.md's summary or table
// lists a different set of apps or sources than the registries ship.
// Regenerate with `make readme-supported` (ANYPICK_UPDATE_README=1).
func TestReadmeSupportedMatchesRegistry(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.SummaryLine()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "..", "README.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Summary.Replace(string(raw), summary)
	if err != nil {
		t.Fatal(err)
	}
	if want, err = Table.Replace(want, s.TableMarkdown()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ANYPICK_UPDATE_README") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want != string(raw) {
		t.Fatalf("README.md supported summary or table is out of date with the registries; run `make readme-supported`.\nsummary:\n%s", summary)
	}
}

func TestReplaceRequiresMarkers(t *testing.T) {
	if _, err := Table.Replace("no markers here", "x"); err == nil {
		t.Fatal("Replace accepted a document without markers")
	}
	// The summary marker must not satisfy the table block, or one block could
	// silently be rewritten into the other.
	if _, err := Table.Replace(Summary.Start()+"\n"+Summary.End(), "x"); err == nil {
		t.Fatal("table block matched the summary markers")
	}
	doc := "a\n" + Table.Start() + "\nold\n" + Table.End() + "\nb\n"
	got, err := Table.Replace(doc, "new\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\nnew\n") || strings.Contains(got, "old") {
		t.Fatalf("Replace did not swap the block: %q", got)
	}
}

func TestSummaryNamesRegisteredApps(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.apps) <= len(featuredApps) || len(s.sources) == 0 {
		t.Fatalf("registries look empty: %d apps, %d sources", len(s.apps), len(s.sources))
	}
	delete(s.byID, featuredApps[0])
	if _, err := s.SummaryLine(); err == nil {
		t.Fatal("summary accepted a featured app that is not registered")
	}
}

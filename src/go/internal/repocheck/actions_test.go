package repocheck

import (
	"strings"
	"testing"
)

const goodWorkflow = "jobs:\n  a:\n    steps:\n" +
	"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n" +
	"      - uses: ./local-action\n" +
	"      - name: x\n        uses: actions/attest-build-provenance/predicate@e8998f949152b193b063cb0ec769d69d929409be # v2.4.0\n"

func TestCheckActionsPassesOnPinnedWorkflows(t *testing.T) {
	f := newFixtureRepo(t)
	f.write(".github/workflows/ci.yml", goodWorkflow)
	sections := CheckActions(f.root)
	for _, finding := range Findings(sections) {
		t.Errorf("clean workflow reported %q", finding.Detail)
	}
	if got := len(sections[0].Checked); got != 2 {
		t.Fatalf("checked %d actions, want 2: %v", got, sections[0].Checked)
	}
}

func TestCheckActionsFindsLoosePins(t *testing.T) {
	cases := []struct{ name, line, expect string }{
		{"a tag", "      - uses: actions/checkout@v4\n", "not pinned to a 40-character commit SHA"},
		// The f60c96f incident: a pin one character short of a real SHA.
		{"a short sha", "      - uses: actions/setup-go@3041bf56c941b39c61721a86cd11f3bb1338122 # v5.2.0\n", "not pinned to a 40-character commit SHA"},
		{"no version note", "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n", "without a `# vX.Y.Z` comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureRepo(t)
			f.write(".github/workflows/ci.yml", "jobs:\n  a:\n    steps:\n"+tc.line)
			findings := Findings(CheckActions(f.root))
			if len(findings) != 1 || !strings.Contains(findings[0].Detail, tc.expect) {
				t.Fatalf("want one finding containing %q, got %+v", tc.expect, findings)
			}
		})
	}
}

func TestCheckActionsRefusesAnEmptyScan(t *testing.T) {
	f := newFixtureRepo(t)
	f.write(".github/workflows/ci.yml", "jobs: {}\n")
	if Passed(CheckActions(f.root)) {
		t.Fatal("a workflow tree with no uses: lines passed")
	}
}

// TestCheckActionsRefusesTheMultilineUsesForm pins the bypass a line-regex
// gate cannot see: YAML allows the ref on the line after `uses:`, and a
// floating pin hidden behind that break passed both the offline gate and the
// CI resolver's grep while every honest pin in the same file passed. The gate
// now refuses the form outright instead of silently skipping it.
func TestCheckActionsRefusesTheMultilineUsesForm(t *testing.T) {
	f := newFixtureRepo(t)
	f.write(".github/workflows/ci.yml",
		"jobs:\n  a:\n    steps:\n"+
			"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"+
			"      - uses:\n          some/evil-action@v1\n")
	sections := CheckActions(f.root)
	findings := Findings(sections)
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding for the multiline uses:, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].Detail, "ref on the next line") {
		t.Fatalf("finding did not name the multiline form: %q", findings[0].Detail)
	}
}

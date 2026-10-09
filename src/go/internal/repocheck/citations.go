package repocheck

import (
	"regexp"
	"strconv"
	"strings"
)

// deadEngineCitation matches a source-tree path naming the TypeScript engine
// this repository deleted. `9702015` removed the ten directories below — each
// with a `registry.ts`-shaped file at its root — and no .ts file under src/ has
// existed since. A path to one of them is a pointer into a tree no clean
// checkout contains, whether it sits in a comment or a string literal.
//
// checkDocumentCitations covers the same defect class for markdown and is the
// reason this check exists rather than a wider glob: its normative document set
// is markdown by design (see the note on markdownCitationPattern), so a Go
// source file citing a deleted path was outside every gate in the repository.
//
// It deliberately matches a string literal too. A test fixture naming a deleted
// path is exactly the case worth surfacing, and narrowing to comment lines would
// make the obvious repair for a false positive (exempt the whole test tree)
// silently drop coverage where fixtures live.
var deadEngineCitation = regexp.MustCompile(`\bsrc/(?:core|providers|types|utils|cli|sources|clients|catalog|tui|tray)/[A-Za-z0-9_-]+(?:\.[A-Za-z0-9]+)*\.tsx?`)

// checkGoSourceCitations reports a Go comment that cites a path under the
// deleted TypeScript engine.
//
// It matches before any filter, so the anti-vacuity guard can tell a dead
// engine from a dead regex. A check that reports "OK" because its pattern
// stopped matching is worse than no check: it converts a real finding into
// silence.
func checkGoSourceCitations(root string) Section {
	section := Section{Name: "Go comments cite no deleted engine path"}

	sources, err := goSources(root)
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot walk "+GoDir+": "+errText(err)))
		return section
	}
	if len(sources) == 0 {
		section.Findings = append(section.Findings, emptyInput(".go file", GoDir))
		return section
	}

	scanned := 0
	citations := 0
	for _, rel := range sources {
		// This package's own test carries the pattern as fixture data, so that a
		// walk which stopped matching stays distinguishable from a clean tree.
		// One file, not the package: exempting internal/repocheck/ wholesale
		// would hide findings across every other file in it.
		if strings.HasSuffix(rel, "internal/repocheck/paths_test.go") {
			continue
		}
		content, unreadable := readFile(root, rel)
		if unreadable != nil {
			section.Findings = append(section.Findings, *unreadable)
			continue
		}
		scanned++
		for i, line := range strings.Split(content, "\n") {
			for _, m := range deadEngineCitation.FindAllString(line, -1) {
				citations++
				section.Findings = append(section.Findings, fail(
					rel+":"+strconv.Itoa(i+1)+" cites `"+m+"`, deleted by 9702015",
					"drop it, or point it at whatever replaced that file —",
					"if the provenance still matters, name the commit that carries it"))
			}
		}
	}

	// The guard is on the walk, not on the match count. A repository whose
	// comments are already clean is the desired state and must report OK. What
	// must never pass is a walk that saw nothing, because that is
	// indistinguishable from a regex that quietly stopped matching.
	if scanned == 0 {
		section.Findings = append(section.Findings, emptyInput("scanned Go file", GoDir))
		return section
	}
	section.Checked = append(section.Checked,
		strconv.Itoa(scanned)+" Go files scanned, "+
			strconv.Itoa(citations)+" deleted engine citations")
	return section
}

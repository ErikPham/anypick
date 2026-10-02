package repocheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Target references in documentation, CI and hooks are the same class of
// problem as the path references above: nothing compiles them, so a renamed
// target leaves instructions that fail for whoever follows them. That is how
// this gate came to exist — a rename pass updated CI and the hooks and missed
// six documents, which a human grep for the old name cannot find reliably once
// the old name is also an ordinary English word.

// makeReferenceInFence matches a `make <target>` line inside a fenced code
// block, with or without the conventional shell prompt.
var makeReferenceInFence = regexp.MustCompile(`^\s*(?:\$ )?make ([a-z][a-z0-9-]*)`)

// makeReferenceInSpan matches a `make <target>` mention inside an inline code
// span. Prose is deliberately not scanned: "make sure" and "make the app" are
// not target references, and a gate that flags English fails its own test.
var makeReferenceInSpan = regexp.MustCompile("`make ([a-z][a-z0-9-]*)[` ]")

// referenceSources are the live files whose instructions must name real
// targets. docs/history is excluded on purpose: it is provenance, and rewriting
// a superseded record to match today's target names would falsify it.
var referenceSources = []string{
	"AGENTS.md",
	"CONTRIBUTING.md",
	"CODE_REVIEW.md",
	"SECURITY.md",
	"README.md",
	"docs/AGENTS.md",
	"packaging/README.md",
	"npm/anypick/README.md",
	"src/go/README.md",
	"src/macos/README.md",
	"src/tauri/README.md",
}

// referenceGlobs are the directories scanned for the same reason.
var referenceGlobs = []string{
	".github",
	".githooks",
	"docs/src/content",
}

// checkMakeReferences reports instructions that name a target the Makefile does
// not define.
func checkMakeReferences(root string) Section {
	section := Section{Name: "documented make targets exist"}

	makefile, missing := readFile(root, "Makefile")
	if missing != nil {
		section.Findings = append(section.Findings, *missing)
		return section
	}
	targets := map[string]bool{}
	for _, match := range makeTargetPattern.FindAllStringSubmatch(makefile, -1) {
		targets[match[1]] = true
	}
	if len(targets) == 0 {
		section.Findings = append(section.Findings, emptyInput("Makefile target", "Makefile"))
		return section
	}

	files, err := referenceFiles(root)
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot list reference sources: "+errText(err)))
		return section
	}
	if len(files) == 0 {
		section.Findings = append(section.Findings, emptyInput("reference source", strings.Join(referenceGlobs, ", ")))
		return section
	}

	references := 0
	for _, rel := range files {
		content, unreadable := readFile(root, rel)
		if unreadable != nil {
			section.Findings = append(section.Findings, *unreadable)
			continue
		}
		for lineNo, name := range makeReferences(rel, content) {
			references++
			if targets[name] {
				continue
			}
			section.Findings = append(section.Findings, fail(
				rel+":"+strconv.Itoa(lineNo)+" tells the reader to run `make "+name+"`, which is not a target",
				"either restore the target, or update the instruction —",
				"`make help` lists every target this repository defines"))
		}
	}
	if references == 0 {
		section.Findings = append(section.Findings, emptyInput("make reference", strings.Join(files, ", ")))
		return section
	}
	section.Checked = append(section.Checked,
		strconv.Itoa(references)+" make references across "+strconv.Itoa(len(files))+" files")
	return section
}

// makeReferences extracts target names from the code contexts of one file,
// keyed by line number.
//
// Markdown contributes inline code spans and fenced blocks; workflows and
// hooks contribute commands, because a stale target there fails a build rather
// than a reader. Prose is never scanned: "make sure" is not a target reference,
// and a gate that flags English fails its own test.
func makeReferences(rel, content string) map[int]string {
	out := map[int]string{}
	switch {
	case filepath.Ext(rel) == ".yml" || filepath.Ext(rel) == ".yaml":
		return yamlReferences(content)
	case filepath.Ext(rel) == ".sh" || filepath.Ext(rel) == "":
		// Hooks are shell scripts with no extension by convention
		// (.githooks/pre-push), so an empty extension means "command file".
		return commandReferences(content)
	}
	inFence := false
	for i, line := range strings.Split(content, "\n") {
		lineNo := i + 1
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			if m := makeReferenceInFence.FindStringSubmatch(line); m != nil {
				out[lineNo] = m[1]
			}
			continue
		}
		for _, m := range makeReferenceInSpan.FindAllStringSubmatch(line, -1) {
			out[lineNo] = m[1]
		}
	}
	return out
}

// yamlReferences reads `run:` values, inline and as block scalars. Nothing else
// in a workflow file is a command.
func yamlReferences(content string) map[int]string {
	out := map[int]string{}
	lines := strings.Split(content, "\n")
	blockIndent := -1
	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if blockIndent >= 0 {
			if trimmed == "" || indent >= blockIndent {
				collectCommands(line, lineNo, out)
				continue
			}
			blockIndent = -1
		}
		value, ok := strings.CutPrefix(trimmed, "run:")
		if !ok {
			// `- run: ...` is the same key with a list marker.
			if value, ok = strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), "run:"); !ok {
				continue
			}
		}
		value = strings.TrimSpace(value)
		if value == "|" || value == ">" || value == "|-" || value == ">-" {
			blockIndent = indent + 1
			continue
		}
		collectCommands(value, lineNo, out)
	}
	return out
}

// commandReferences reads `make <target>` from shell command lines only, so a
// comment that explains history does not have to stay true forever.
func commandReferences(content string) map[int]string {
	out := map[int]string{}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		collectCommands(line, i+1, out)
	}
	return out
}

// collectCommands pulls every `make <target>` out of one command string,
// including the `$(…)` substitutions a script may wrap it in.
func collectCommands(command string, lineNo int, out map[int]string) {
	for _, m := range makeCommandPattern.FindAllStringSubmatch(command, -1) {
		out[lineNo] = m[1]
	}
}

// makeCommandPattern matches `make <target>` in a command position: start of
// the string, after a separator, or after a shell substitution.
var makeCommandPattern = regexp.MustCompile(`(?:^|[;&|]\s*|\$\(|` + "`" + `)\s*make ([a-z][a-z0-9-]*)`)

// referenceFiles lists the live files whose instructions are checked: the
// named documents plus every markdown, shell and workflow file under the
// reference globs. Build output, vendored dependencies and provenance records
// are skipped.
func referenceFiles(root string) ([]string, error) {
	var out []string
	for _, rel := range referenceSources {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			out = append(out, rel)
		}
	}
	for _, glob := range referenceGlobs {
		base := filepath.Join(root, glob)
		if _, statErr := os.Stat(base); statErr != nil {
			// A reference source that vanished is a finding, not a silent skip:
			// the instructions inside it stop being checked, and a check that
			// quietly covers less is the failure this package exists to prevent.
			return nil, fmt.Errorf("%s cannot be read: %w", glob, statErr)
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "node_modules", "dist", "history":
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(entry.Name()) {
			case ".md", ".mdx", ".yml", ".yaml", ".sh", "":
			default:
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return uniqueSorted(out), nil
}

// normativeDocuments are the documents that state what is true now, as opposed
// to narrating what happened. READMEs are deliberately absent: a README has to
// be able to say "these trackers were retired" and name them, which is exactly
// what src/go/README.md does — and flagging that would be a false positive on
// the correct handling of a removal.
var normativeDocuments = []string{
	"AGENTS.md",
	"CONTRIBUTING.md",
	"CODE_REVIEW.md",
	"SECURITY.md",
}

// markdownCitationPattern matches a `path.md` inside an inline code span.
//
// Source comments are deliberately not scanned, for the reason the `make`
// check gives for not scanning prose: a bare markdown path there can name
// another project's documentation. src/go/internal/client/gemini.go cites
// `docs/reference/configuration.md` for a vendor CLI release, and no file of
// that name has ever existed here. A gate that flags that fails its own test.
var markdownCitationPattern = regexp.MustCompile("`([^`\\s]+\\.md)`")

// citationBases are the roots a citation is resolved against. A document writes
// `0004-atomic-snapshot-save.md` meaning adr/, and `src/macos/README.md`
// meaning the repository root, and both are correct as written.
var citationBases = []string{"", "docs/", "adr/", "src/go/", "docs/history/"}

// checkDocumentCitations reports an authority document that cites a markdown
// file this repository does not have.
//
// It is the other half of checkMakeReferences and the same class of defect:
// nothing compiles a citation, so a consolidation that retires six documents
// leaves every pointer at the old name, and the reader who follows one cannot
// tell the document is stale. That is what happened when
// SYSTEM_ARCHITECTURE_2026-09-07.md and BACKEND_ARCHITECTURE_2026-09-07.md were
// consolidated into docs/history: two ADRs, CODE_REVIEW.md and three source
// comments still cited the deleted originals, and CODE_REVIEW.md §8 calls this
// a P1 while asserting that `make check` catches it. It did not.
func checkDocumentCitations(root string) Section {
	section := Section{Name: "authority documents cite files that exist"}
	docs, err := normativeDocumentFiles(root)
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot list the authority documents: "+errText(err),
			"this check reads the documents that state what is true; without",
			"them it proved nothing"))
		return section
	}

	citations := 0
	for _, rel := range docs {
		content, unreadable := readFile(root, rel)
		if unreadable != nil {
			section.Findings = append(section.Findings, *unreadable)
			continue
		}
		for _, cite := range markdownCitations(content) {
			// A glob describes a class of files, not one of them. §8 of this
			// very standard writes `*.md` when it states the rule this check
			// enforces, and resolving that literally fails the gate on its own
			// documentation — which is how this line came to exist.
			if strings.ContainsAny(cite.token, "*?[]") {
				continue
			}
			citations++
			if citationResolves(root, cite.token) {
				continue
			}
			section.Findings = append(section.Findings, fail(
				rel+":"+strconv.Itoa(cite.line)+" cites `"+cite.token+"`, which does not exist",
				"point it at whatever replaced that file, or drop it —",
				"a citation of a path that is gone is docs drift, and §8 rates it P1"))
		}
	}
	if citations == 0 {
		section.Findings = append(section.Findings, emptyInput("markdown citation", strings.Join(docs, ", ")))
		return section
	}
	section.Checked = append(section.Checked,
		strconv.Itoa(citations)+" citations across "+strconv.Itoa(len(docs))+" authority documents")
	return section
}

// normativeDocumentFiles lists the documents the citation check reads.
func normativeDocumentFiles(root string) ([]string, error) {
	var out []string
	for _, rel := range normativeDocuments {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			out = append(out, rel)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "adr"))
	if err != nil {
		// A missing adr/ is a finding, not a silent skip: the decisions stop
		// being checked, and a check that quietly covers less is the failure
		// this package exists to prevent.
		return nil, fmt.Errorf("adr cannot be read: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		out = append(out, filepath.Join("adr", entry.Name()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no authority document could be read")
	}
	return uniqueSorted(out), nil
}

// markdownCitation is one citation and the line it sits on.
type markdownCitation struct {
	line  int
	token string
}

func markdownCitations(content string) []markdownCitation {
	var out []markdownCitation
	for i, line := range strings.Split(content, "\n") {
		for _, m := range markdownCitationPattern.FindAllStringSubmatch(line, -1) {
			out = append(out, markdownCitation{line: i + 1, token: m[1]})
		}
	}
	return out
}

// citationResolves reports whether a cited markdown path exists under any base
// a document might reasonably mean.
func citationResolves(root, token string) bool {
	if strings.Contains(token, "://") {
		return true
	}
	for _, base := range citationBases {
		if exists(root, "", base+token) {
			return true
		}
	}
	return false
}

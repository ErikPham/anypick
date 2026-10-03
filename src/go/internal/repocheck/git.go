package repocheck

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The git-backed checks. They inspect tracked content rather than the working
// tree: a file that is tracked but deleted on disk, or ignored but present, is
// invisible to `git status`, which is how the internal/refs package once
// stopped being committed while every local build kept working.
//
// Each git failure is reported as a failure of the check, never as "nothing
// found". A lookup that did not run must not read as a pass.

// checkGoSourcesNotIgnored reports Go source git would ignore. The .gitignore
// that swallowed src/go/internal/refs was unanchored, so the pattern matched at
// any depth: main did not compile, and git said nothing.
func checkGoSourcesNotIgnored(root string) Section {
	section := Section{Name: "no Go source is git-ignored"}
	files, err := goSources(root)
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot walk "+GoDir+": "+errText(err)))
		return section
	}
	if len(files) == 0 {
		section.Findings = append(section.Findings, emptyInput(".go file", GoDir))
		return section
	}
	stdout, exit, gitErr := git(root, strings.Join(files, "\x00"),
		"check-ignore", "--no-index", "-z", "--stdin")
	if gitErr != nil {
		section.Findings = append(section.Findings, fail(gitErr.Error(),
			"the ignore lookup did not run, so this check proved nothing"))
		return section
	}
	// check-ignore exits 1 when nothing matched, which is the pass.
	if exit != 0 && exit != 1 {
		section.Findings = append(section.Findings, fail(
			"git check-ignore exited "+strconv.Itoa(exit),
			"the ignore lookup did not run, so this check proved nothing"))
		return section
	}
	ignored := uniqueSorted(strings.Split(strings.TrimRight(strings.ReplaceAll(stdout, "\x00", "\n"), "\n"), "\n"))
	if len(ignored) > 0 {
		for _, path := range ignored {
			section.Findings = append(section.Findings, fail(path+" is git-ignored",
				"git-ignored source can never be committed, and git status stays",
				"completely silent about it (see .gitignore)"))
		}
		return section
	}
	section.Checked = append(section.Checked, "every .go file under "+GoDir+" is visible to git")
	return section
}

// checkNoTrackedArtifacts reports build output committed under src/. Naming the
// offenders is how a 16.9 MB Mach-O ended up in the repository: the ignore
// pattern was written before commit 44e5046 moved everything under src/, and
// once a file is tracked, .gitignore no longer applies to it.
//
// Only Xcode image assets are excused, and only when their magic bytes agree,
// so a renamed binary cannot slip through on its extension.
func checkNoTrackedArtifacts(root string) Section {
	section := Section{Name: "no build artifact is tracked"}
	run := newIndexRun(root)

	tracked, err := run.tracked("src/")
	if err != nil {
		section.Findings = append(section.Findings, fail(err.Error(),
			"this check lists tracked files; without that list it proved nothing"))
		return section
	}
	if len(tracked) == 0 {
		section.Findings = append(section.Findings, fail("git ls-files src/ listed nothing",
			"a check over zero entries proves nothing"))
		return section
	}
	text, err := run.text("src/")
	if err != nil {
		section.Findings = append(section.Findings, fail(err.Error(),
			"no usable text-file list, so this check did NOT run"))
		return section
	}

	section.Checked = append(section.Checked, strconv.Itoa(len(tracked))+" tracked files under src/")
	section.Findings = append(section.Findings, artifactFindings(tracked, text, run.blob)...)
	if len(section.Findings) == 0 {
		section.Checked = append(section.Checked,
			"every tracked file under src/ is text, empty, or a magic-verified image asset")
	}
	return section
}

// checkReferenceDocsTracked reports the named documents the reference checks
// read that a clean checkout would not have. A document that is untracked (or
// deleted and committed) silently shrinks checkMakeReferences and
// checkDocumentCitations: fewer files, fewer references, every gate still
// green. referenceSources is the list those checks consume, so iterating it
// here keeps the required set and the checked set from drifting apart.
func checkReferenceDocsTracked(root string) Section {
	section := Section{Name: "the reference documents are tracked"}
	for _, doc := range referenceSources {
		_, exit, err := git(root, "", "ls-files", "--error-unmatch", "--", doc)
		switch {
		case err != nil:
			section.Findings = append(section.Findings, fail("cannot ask git about "+doc+": "+err.Error()))
		case exit != 0:
			section.Findings = append(section.Findings, fail(doc+" is not tracked by git",
				"a clean checkout does not have it, and the reference checks read it"))
		default:
			section.Checked = append(section.Checked, doc)
		}
	}
	return section
}

// checkHooksTracked reports hook scripts that exist locally but are not
// committed. An untracked hook is a gate that only one working copy runs — the
// same silence that once swallowed internal/refs.
func checkHooksTracked(root string) Section {
	section := Section{Name: "the local review gates are tracked"}
	entries, err := os.ReadDir(filepath.Join(root, ".githooks"))
	if err != nil {
		section.Findings = append(section.Findings, fail(".githooks cannot be read: "+errText(err),
			"the tracked hooks are the local half of CODE_REVIEW.md; restore the directory"))
		return section
	}
	var hooks []string
	for _, entry := range entries {
		if !entry.IsDir() {
			hooks = append(hooks, filepath.Join(".githooks", entry.Name()))
		}
	}
	if len(hooks) == 0 {
		section.Findings = append(section.Findings, emptyInput("hook", ".githooks"))
		return section
	}
	for _, hook := range hooks {
		_, exit, err := git(root, "", "ls-files", "--error-unmatch", "--", hook)
		switch {
		case err != nil:
			section.Findings = append(section.Findings, fail("cannot ask git about "+hook+": "+err.Error()))
		case exit != 0:
			section.Findings = append(section.Findings, fail(hook+" is not tracked",
				"an untracked hook is a gate only this working copy runs"))
		default:
			section.Checked = append(section.Checked, hook)
		}
	}
	return section
}

// checkHooksInstalled reports a clone where the tracked gates exist but git
// will not run them.
//
// checkHooksTracked above proves the hooks are in the repository. This proves
// they are wired into this checkout, which is a different fact and the one that
// actually decides whether a commit is gated: a tracked hook that
// core.hooksPath does not point at has caught nothing, and reads exactly like a
// passing one.
//
// The gap is not hypothetical. The hooks sat committed, executable and green
// for two days while core.hooksPath was unset (CODE_REVIEW.md §12), and this
// repository was later found with core.hooksPath explicitly set to .git/hooks —
// the default directory, which contains nothing but git's samples. Both states
// are invisible to `make check`, which is why the rule needs a gate instead of
// another paragraph of documentation (§5).
//
// It is deliberately silent under CI. "Installed" is a property of a
// developer's checkout, not of the repository: a CI clone never commits, so it
// has nothing for a hook to intercept, and failing there would make the gate
// red for a condition CI cannot fix. The skip is recorded in Checked, so a
// skipped check is never mistaken for one that ran.
func checkHooksInstalled(root string) Section {
	section := Section{Name: "the local review gates are installed"}
	if os.Getenv("CI") != "" {
		section.Checked = append(section.Checked, "not checked under CI: a fresh clone never commits")
		return section
	}
	stdout, exit, err := git(root, "", "config", "--get", "core.hooksPath")
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot read core.hooksPath: "+err.Error(),
			"this check asks git which hooks it will run; without an answer it proved nothing"))
		return section
	}
	configured := strings.TrimSpace(stdout)
	if exit != 0 || configured == "" {
		section.Findings = append(section.Findings, fail(
			"core.hooksPath is unset, so git runs .git/hooks and not the tracked .githooks",
			"run `make hooks` once per clone: an uninstalled gate has caught nothing,",
			"and reads exactly like a passing one"))
		return section
	}
	// `make hooks` writes the relative path, but an absolute one pointing at
	// the same directory is equally installed — compare what it resolves to,
	// not how it was spelled.
	resolved := configured
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	if filepath.Clean(resolved) != filepath.Join(root, ".githooks") {
		section.Findings = append(section.Findings, fail(
			"core.hooksPath is "+configured+", which is not the tracked .githooks directory",
			"run `make hooks`: it points core.hooksPath at .githooks, so commit-msg",
			"and pre-push actually fire"))
		return section
	}
	section.Checked = append(section.Checked, "core.hooksPath → .githooks")
	return section
}

// goSources lists every .go file under GoDir, excluding the build output
// directory.
func goSources(root string) ([]string, error) {
	base := filepath.Join(root, GoDir)
	var out []string
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// indexRun reads tracked content from the git index. It exists so the artifact
// and ignore checks share one runner and one failure convention.
type indexRun struct {
	root string
}

func newIndexRun(root string) *indexRun { return &indexRun{root: root} }

func (r *indexRun) tracked(prefix string) ([]string, error) {
	stdout, _, err := git(r.root, "", "ls-files", "-z", "--", prefix)
	if err != nil {
		return nil, err
	}
	return splitZ(stdout), nil
}

// text lists tracked files under prefix whose content git considers text. The
// index is searched (`--cached`), not the working tree: this check is about
// tracked content, and a file deleted on disk but still tracked was invisible
// to a working-tree grep, which made the gate blame a markdown file for being a
// build artifact.
func (r *indexRun) text(prefix string) ([]string, error) {
	stdout, _, err := git(r.root, "", "grep", "--cached", "-Il", "-z", ".", "--", prefix)
	if err != nil {
		return nil, err
	}
	return splitZ(stdout), nil
}

// blob returns a tracked blob's size and its first 8 bytes, straight from the
// index. Both come from one call site because the image exemption must be
// decided by magic bytes, never by extension alone.
func (r *indexRun) blob(spec string) (int64, []byte) {
	size, _, err := git(r.root, "", "cat-file", "-s", spec)
	if err != nil {
		return -1, nil
	}
	head, _, err := git(r.root, "", "cat-file", "blob", spec)
	if err != nil {
		return parseSize(size), nil
	}
	if len(head) > 8 {
		head = head[:8]
	}
	return parseSize(size), []byte(head)
}

// parseSize reads the byte count git reports, or -1 when it said something
// unparseable — an unreadable size must not read as "empty file, therefore
// harmless".
func parseSize(raw string) int64 {
	size, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return -1
	}
	return size
}

// splitZ splits NUL-terminated git output.
func splitZ(raw string) []string {
	raw = strings.TrimRight(raw, "\x00")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}

// checkGoSourcesTracked reports Go source under src/go that exists locally but
// was never added to the index.
//
// This is the other half of the git-ignored check, and the same incident: a
// package that is present on disk, compiles for whoever has it, and is absent
// from a clean clone. An untracked .go file under src/go is not a scratch file
// — the module compiles it — so leaving it out ships a tree that does not
// build. It runs locally (pre-push) because CI always starts from a clone,
// where the check is vacuously true.
func checkGoSourcesTracked(root string) Section {
	section := Section{Name: "every Go source under src/go is in the index"}
	tracked, err := goSources(root)
	if err != nil {
		section.Findings = append(section.Findings, fail("cannot walk "+GoDir+": "+errText(err)))
		return section
	}
	if len(tracked) == 0 {
		section.Findings = append(section.Findings, emptyInput(".go file", GoDir))
		return section
	}
	// --exclude-standard leaves git-ignored files to the check above, so one
	// file is never reported twice.
	stdout, _, err := git(root, "", "ls-files", "--others", "--exclude-standard", "-z", "--", GoDir)
	if err != nil {
		section.Findings = append(section.Findings, fail(err.Error(),
			"the untracked listing did not run, so this check proved nothing"))
		return section
	}
	var untracked []string
	for _, path := range splitZ(stdout) {
		if strings.HasSuffix(path, ".go") {
			untracked = append(untracked, path)
		}
	}
	section.Checked = append(section.Checked,
		strconv.Itoa(len(tracked))+" tracked Go files under "+GoDir)
	for _, path := range uniqueSorted(untracked) {
		section.Findings = append(section.Findings, fail(path+" is not in the index",
			"git add "+path,
			"a clean clone would not have it, and the module compiles it"))
	}
	return section
}

// checkRootEntries reports untracked, unignored entries at the repository root.
//
// The root is curated: every entry there is either tracked or deliberately
// ignored, because it is the one directory a reader browses and the one place a
// stray file is most likely to be something that should never be in a checkout
// — a downloaded app bundle, an exported credential, another product's
// manifest. Nothing else looks there: the artifact check reads the index, the
// Go-source check reads src/go, and `git status` is read by humans who have
// already stopped reading it. A 107 KB JavaScript bundle and a foreign
// package.json sat in this repository's root, invisible to every gate, until
// someone asked why they were there.
//
// --directory collapses a wholly untracked directory into one entry, so the
// finding names `npm/` rather than each file inside it.
func checkRootEntries(root string) Section {
	section := Section{Name: "no stray untracked entry at the repository root"}
	stdout, _, err := git(root, "", "ls-files", "--others", "--exclude-standard", "--directory", "-z")
	if err != nil {
		section.Findings = append(section.Findings, fail(err.Error(),
			"the untracked listing did not run, so this check proved nothing"))
		return section
	}
	// Only depth 1: anything nested belongs to the check that owns its tree.
	var stray []string
	for _, path := range splitZ(stdout) {
		if strings.Contains(strings.TrimSuffix(path, "/"), "/") {
			continue
		}
		stray = append(stray, path)
	}
	section.Checked = append(section.Checked, "every root entry is tracked or ignored")
	for _, path := range uniqueSorted(stray) {
		section.Findings = append(section.Findings, fail(path+" is untracked and not ignored",
			"decide which it is: git add "+path+" if it belongs to this repository,",
			"add it to .gitignore if a tool generates it here, or delete it"))
	}
	return section
}

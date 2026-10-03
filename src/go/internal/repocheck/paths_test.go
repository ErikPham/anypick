package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo writes the smallest tree every check can parse, and stages it so
// the git-backed sections have an index to read. Nothing is committed: every
// check here reads the index, and a commit would need an identity the test
// machine may not have.
//
// The fixture exists so each check can be exercised in both directions. The
// previous shell implementation could only be run against the live repository,
// which is why nothing noticed when a comment disabled three of its checks.
type fixtureRepo struct {
	root string
	t    *testing.T
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	root := t.TempDir()
	fixture := &fixtureRepo{root: root, t: t}
	fixture.write("VERSION", "1.0.0\n")
	fixture.write("AGENTS.md",
		"# Agent instructions\n\nBuild it with `make build`, then `make test`.\n\n"+
			"```bash\nmake build\n```\n")
	fixture.write("CODE_REVIEW.md", "# Review standard\n")
	fixture.write("CONTRIBUTING.md", "# Contributing\n")
	// One decision, citing a document that exists: the citation check needs a
	// resolvable citation to prove it ran, and a check over zero entries proves
	// nothing.
	fixture.write("adr/0001-example.md", "# ADR-0001: an example\n\n- Task: see `CODE_REVIEW.md`\n")
	fixture.write("src/go/go.mod", "module example.test/engine\n\ngo 1.26\n")
	fixture.write("src/go/main.go", "package main\n\nfunc main() {}\n")
	fixture.write("src/go/internal/buildinfo/buildinfo.go",
		"package buildinfo\n\n// ProtocolVersion is the stdio protocol the app negotiates.\nconst ProtocolVersion = 2\n")
	fixture.write("src/macos/README.md", "# macOS app\n\nRun `make test-macos`.\n")
	fixture.write("docs/src/content/docs/index.mdx", "---\ntitle: Docs\n---\n\nUse `make build`.\n")
	fixture.write("src/macos/AppServiceClient.swift",
		"actor AppServiceClient {\n  static let protocolVersion = 2\n}\n")
	fixture.write("src/macos/Store.swift",
		"final class TrayStore {\n  func consume(_ line: String) {\n    guard protocolVersion == 2 else { return }\n  }\n}\n")
	fixture.write("src/macos/scripts/bundle-go-helper.sh",
		"#!/bin/bash\nscript_dir=\"$(cd \"$(dirname \"${BASH_SOURCE[0]}\")\" && pwd)\"\nrepo_root=\"$(cd \"${script_dir}/../../..\" && pwd)\"\ngo_root=\"${repo_root}/src/go\"\n")
	fixture.write("src/macos/AnyPick.xcodeproj/project.pbxproj",
		"buildSettings = { SRCPATH = \"$(SRCROOT)/Resources/AppIcon.icns\"; };\n"+
			"files = ( \"$(SRCROOT)/scripts/bundle-go-helper.sh\", \"$(SRCROOT)/../go/go.mod\" );\n"+
			"assets = ( { path = ../../assets/icon-1024.png; } );\n")
	fixture.write("src/tauri/app/tauri.conf.json",
		"{\n  \"bundle\": {\n    \"icon\": [\n      \"../../../assets/icon-32.png\"\n    ]\n  }\n}\n")
	fixture.write("src/tauri/app/build.rs",
		"fn main() {\n    println!(\"cargo:rerun-if-changed=../../go\");\n}\n")
	fixture.write("src/tauri/app/src/protocol.rs",
		"pub const PROTOCOL_VERSION: u32 = 2;\n\npub fn check(value: u32) -> bool { value == PROTOCOL_VERSION }\n")
	fixture.write("src/tauri/ui/lib/wire.ts",
		"export const PROTOCOL_VERSION = 2;\n")
	fixture.write(".github/workflows/ci.yml",
		"jobs:\n  go:\n    steps:\n      - run: make test\n        working-directory: src/go\n")
	fixture.write(".githooks/pre-push", "#!/bin/sh\nexit 0\n")
	fixture.write("Makefile",
		".PHONY: build test test-macos\n\n"+
			"build: ## Build\n\tgo build ./...\n\n"+
			"test: ## Test\n\tgo test ./...\n\n"+
			"test-macos: ## Test the app\n\txcodebuild test\n")
	// Assets: one verified PNG and one verified ICNS, byte-exact magic included.
	fixture.writeBinary("assets/icon-32.png", pngMagic, []byte{0x00, 0x01})
	fixture.writeBinary("assets/icon-1024.png", pngMagic, []byte{0x00})
	fixture.writeBinary("src/macos/Resources/AppIcon.icns", icnsMagic, []byte{0x00})
	// Every remaining reference source, so the tracked-documents check sees a
	// clean tree. Stubs stay free of `make` targets and file citations: a
	// fixture that invented either would add findings to unrelated cases.
	for _, rel := range referenceSources {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			continue
		}
		fixture.write(rel, "# "+filepath.Base(rel)+"\n")
	}
	fixture.gitInit()
	return fixture
}

func (f *fixtureRepo) write(rel, content string) {
	f.t.Helper()
	f.writeBinary(rel, nil, []byte(content))
}

func (f *fixtureRepo) writeBinary(rel string, magic []byte, tail []byte) {
	f.t.Helper()
	path := filepath.Join(f.root, filepath.Clean(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	data := append(append([]byte{}, magic...), tail...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixtureRepo) gitInit() {
	f.t.Helper()
	f.git("init", "--quiet")
	f.git("add", "-A")
	// A correctly bootstrapped clone has run `make hooks`. Without this the
	// fixture stands for a checkout nobody gated, which checkHooksInstalled
	// reports -- correctly, and not what the tests below are about.
	f.git("config", "core.hooksPath", ".githooks")
}

func (f *fixtureRepo) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestCheckPathsPassesOnACleanFixture is the floor: the gate must be quiet on a
// tree that satisfies every rule, or the real repository cannot be its only
// passing case.
func TestCheckPathsPassesOnACleanFixture(t *testing.T) {
	fixture := newFixtureRepo(t)
	sections := CheckPaths(fixture.root)
	for _, finding := range Findings(sections) {
		t.Errorf("clean fixture reported %q", finding.Detail)
	}
}

// TestEveryPathCheckInspectsSomething is the property the shell gate could not
// express: a check that parsed its input and found nothing to verify used to
// report success. Each section must name at least one thing it inspected.
func TestEveryPathCheckInspectsSomething(t *testing.T) {
	fixture := newFixtureRepo(t)
	sections := CheckPaths(fixture.root)
	if len(sections) == 0 {
		t.Fatal("CheckPaths ran no checks")
	}
	for _, s := range sections {
		if len(s.Checked) == 0 && len(s.Findings) == 0 {
			t.Errorf("section %q neither inspected anything nor reported a problem", s.Name)
		}
		if len(s.Checked) == 0 {
			t.Errorf("section %q inspected nothing (findings: %v)", s.Name, s.Findings)
		}
	}
}

// TestCheckPathsFindsBrokenReferences drives each file-based check with a
// deliberate break, so a check that stops working fails here instead of going
// quiet in the repository.
func TestCheckPathsFindsBrokenReferences(t *testing.T) {
	cases := []struct {
		name   string
		breaks func(*fixtureRepo)
		expect string
		// advice is a fragment the failure must carry, because a finding
		// without a next step makes the reader reconstruct the fix.
		advice string
	}{
		{
			name:   "moved directory",
			breaks: func(f *fixtureRepo) { f.git("rm", "-q", "-f", "-r", "src/tauri") },
			expect: "src/tauri is missing",
		},
		{
			name: "helper resolves the wrong depth",
			breaks: func(f *fixtureRepo) {
				f.write("src/macos/scripts/bundle-go-helper.sh",
					"script_dir=\"x\"\nrepo_root=\"$(cd \"${script_dir}/../..\" && pwd)\"\ngo_root=\"${repo_root}/src/go\"\n")
				f.git("add", "-A")
			},
			expect: "rather than the repository root",
			advice: "fix the relative depth",
		},
		{
			name: "helper points at the pre-src layout",
			breaks: func(f *fixtureRepo) {
				f.write("src/macos/scripts/bundle-go-helper.sh",
					"script_dir=\"x\"\nrepo_root=\"$(cd \"${script_dir}/../../..\" && pwd)\"\ngo_root=\"${repo_root}/go\"\n")
				f.git("add", "-A")
			},
			expect: "which does not exist",
			advice: "module path has drifted",
		},
		{
			name: "xcode srcroot reference is gone",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "-f", "src/macos/Resources/AppIcon.icns")
			},
			expect: "SRCROOT/Resources/AppIcon.icns does not exist",
		},
		{
			name: "xcode asset reference is gone",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "-f", "assets/icon-1024.png")
			},
			expect: "../../assets/icon-1024.png does not exist",
		},
		{
			name: "tauri icon is gone",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "-f", "assets/icon-32.png")
			},
			expect: "../../../assets/icon-32.png does not exist",
		},
		{
			name: "sidecar path is wrong",
			breaks: func(f *fixtureRepo) {
				f.write("src/tauri/app/build.rs",
					"fn main() {\n    println!(\"cargo:rerun-if-changed=../../missing\");\n}\n")
				f.git("add", "-A")
			},
			expect: "watches ../../missing, which does not exist",
		},
		{
			name: "workflow working-directory is wrong",
			breaks: func(f *fixtureRepo) {
				f.write(".github/workflows/ci.yml",
					"jobs:\n  go:\n    steps:\n      - run: go test ./...\n        working-directory: src/engine\n")
				f.git("add", "-A")
			},
			expect: "working-directory src/engine does not exist",
		},
		{
			name: "go source is git-ignored",
			breaks: func(f *fixtureRepo) {
				f.write(".gitignore", "/src/go/internal/\n")
				f.git("rm", "-q", "--cached", "-r", "src/go/internal")
			},
			expect: "is git-ignored",
			advice: "can never be committed",
		},
		{
			name: "a binary is tracked",
			breaks: func(f *fixtureRepo) {
				// The NUL is load-bearing. Git decides "binary" by looking for
				// one in the first 8000 bytes, and this check defines a
				// candidate as "tracked but not text". A Mach-O header without
				// the padding NUL is a *text* file to git, so `git grep -I`
				// lists it, artifactFindings never sees a candidate, and this
				// case passes while testing nothing -- which is exactly how it
				// was found failing.
				f.writeBinary("src/go/blob.bin",
					[]byte{0xcf, 0xfa, 0xed, 0xfe, 0x00}, []byte("macho"))
				f.git("add", "-A")
			},
			expect: "is not text and not a verified image asset",
			advice: "git rm --cached",
		},
		{
			name: "an authority document is untracked",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "--cached", "CODE_REVIEW.md")
			},
			expect: "CODE_REVIEW.md is not tracked by git",
		},
		{
			// SECURITY.md is a reference source but not an authority document:
			// untracked, it shrank both reference checks while every gate stayed
			// green. The check now iterates referenceSources, so it is caught.
			name: "a reference document that is not an authority document is untracked",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "--cached", "SECURITY.md")
			},
			expect: "SECURITY.md is not tracked by git",
		},
		{
			name: "a stray file sits at the repository root",
			breaks: func(f *fixtureRepo) {
				// Written, not staged, not ignored: a bundle or an exported
				// credential lands here and no other check looks at the root.
				f.write("auth.js", "// a downloaded bundle\n")
			},
			expect: "auth.js is untracked and not ignored",
			advice: "git add auth.js",
		},
		{
			name: "a Go source was never added to the index",
			breaks: func(f *fixtureRepo) {
				// Written, not staged: the module compiles it, a clean clone
				// would not have it.
				f.write("src/go/internal/late/late.go", "package late\n")
			},
			expect: "src/go/internal/late/late.go is not in the index",
			advice: "git add src/go/internal/late/late.go",
		},
		{
			name: "a document tells the reader to run a target that does not exist",
			breaks: func(f *fixtureRepo) {
				f.write("AGENTS.md", "# Agent instructions\n\nRun `make deploy`.\n")
			},
			expect: "AGENTS.md:3 tells the reader to run `make deploy`, which is not a target",
			advice: "`make help` lists every target",
		},
		{
			name: "a workflow runs a target that does not exist",
			breaks: func(f *fixtureRepo) {
				f.write(".github/workflows/ci.yml",
					"jobs:\n  go:\n    steps:\n      - run: make deploy\n")
			},
			expect: ".github/workflows/ci.yml:4 tells the reader to run `make deploy`",
		},
		{
			name: "a fenced block runs a target that does not exist",
			breaks: func(f *fixtureRepo) {
				f.write("CONTRIBUTING.md", "# Contributing\n\n```bash\nmake deploy\n```\n")
			},
			expect: "CONTRIBUTING.md:4 tells the reader to run `make deploy`",
		},
		{
			name: "a Makefile target is missing from .PHONY",
			breaks: func(f *fixtureRepo) {
				f.write("Makefile", ".PHONY: build\n\nbuild: ## Build\n\tgo build ./...\n\ntest: ## Test\n\tgo test ./...\n\ntest-macos: ## Test the app\n\txcodebuild test\n")
			},
			expect: "target test is not in .PHONY",
			advice: "a target that matches a file on disk runs",
		},
		{
			name: "a .PHONY entry has no target",
			breaks: func(f *fixtureRepo) {
				f.write("Makefile", ".PHONY: build test test-macos gone\n\nbuild: ## Build\n\tgo build ./...\n\ntest: ## Test\n\tgo test ./...\n\ntest-macos: ## Test the app\n\txcodebuild test\n")
			},
			expect: ".PHONY lists gone, which is not a target",
		},
		{
			name: "a hook is untracked",
			breaks: func(f *fixtureRepo) {
				f.git("rm", "-q", "--cached", ".githooks/pre-push")
			},
			expect: ".githooks/pre-push is not tracked",
		},
		{
			name: "an authority document cites a file that is gone",
			breaks: func(f *fixtureRepo) {
				// The shape this check exists for: the cited document was
				// consolidated away and the pointer was left behind.
				f.write("adr/0001-example.md",
					"# ADR-0001: an example\n\n- Task: see `CONSOLIDATED_PLAN_2026-01-01.md`\n")
				f.git("add", "-A")
			},
			expect: "cites `CONSOLIDATED_PLAN_2026-01-01.md`, which does not exist",
			advice: "point it at whatever replaced that file",
		},
		{
			name: "the tracked gates are not installed in this clone",
			breaks: func(f *fixtureRepo) {
				// The state this repository was actually found in: the hooks
				// are committed, and core.hooksPath points at the default
				// directory git would have used anyway.
				f.git("config", "core.hooksPath", ".git/hooks")
			},
			expect: "which is not the tracked .githooks directory",
			advice: "run `make hooks`",
		},
		{
			name: "the hooks were never installed at all",
			breaks: func(f *fixtureRepo) {
				// Not set to the wrong place -- never set. Git then runs
				// .git/hooks, which holds nothing but its own samples.
				f.git("config", "--unset", "core.hooksPath")
			},
			expect: "core.hooksPath is unset",
			advice: "run `make hooks`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// checkHooksInstalled is silent under CI by design, so pin the
			// environment: without this the table proves less on a runner than
			// on a laptop, and the two "not installed" cases would pass
			// vacuously wherever CI happens to be set.
			t.Setenv("CI", "")
			fixture := newFixtureRepo(t)
			tc.breaks(fixture)
			found := Findings(CheckPaths(fixture.root))
			if len(found) == 0 {
				t.Fatalf("no finding for %s", tc.name)
			}
			var joined strings.Builder
			for _, finding := range found {
				joined.WriteString(finding.Detail)
				joined.WriteString("\n")
				joined.WriteString(strings.Join(finding.Advice, " "))
				joined.WriteString("\n")
			}
			report := joined.String()
			if !strings.Contains(report, tc.expect) {
				t.Fatalf("findings do not mention %q:\n%s", tc.expect, report)
			}
			if tc.advice != "" && !strings.Contains(report, tc.advice) {
				t.Fatalf("findings carry no advice %q:\n%s", tc.advice, report)
			}
		})
	}
}

// TestCheckPathsReportsAnEmptyExtraction covers the failure mode that made the
// shell gate unusable: an extraction that matched nothing looked identical to a
// clean tree.
func TestCheckPathsReportsAnEmptyExtraction(t *testing.T) {
	fixture := newFixtureRepo(t)
	fixture.write("src/macos/AnyPick.xcodeproj/project.pbxproj", "// no references at all\n")
	fixture.git("add", "-A")

	found := Findings(CheckPaths(fixture.root))
	var joined strings.Builder
	for _, finding := range found {
		joined.WriteString(finding.Detail)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "found nothing in") {
		t.Fatalf("an empty extraction must fail loudly, findings:\n%s", joined.String())
	}
}

// TestRootEntriesIgnoresDeliberatelyIgnoredFiles is the anti-false-positive
// case: the rule is "tracked or ignored", not "tracked".
func TestRootEntriesIgnoresDeliberatelyIgnoredFiles(t *testing.T) {
	fixture := newFixtureRepo(t)
	fixture.write(".gitignore", "scratch/\ngenerated.txt\n")
	fixture.write("generated.txt", "tool output\n")
	fixture.write("scratch/notes.md", "notes\n")
	fixture.git("add", "-A")

	found := Findings([]Section{checkRootEntries(fixture.root)})
	if len(found) != 0 {
		t.Fatalf("ignored entries must not be reported: %v", details(found))
	}
}

// TestRootEntriesReportsADirectoryOnce keeps the finding readable when a whole
// tree is untracked, which is the shape this check saw in practice.
func TestRootEntriesReportsADirectoryOnce(t *testing.T) {
	fixture := newFixtureRepo(t)
	fixture.write("launcher/index.js", "module.exports = {}\n")
	fixture.write("launcher/package.json", "{}\n")

	found := Findings([]Section{checkRootEntries(fixture.root)})
	if len(found) != 1 || found[0].Detail != "launcher/ is untracked and not ignored" {
		t.Fatalf("findings = %v, want one entry for the directory", details(found))
	}
}

// TestDocumentCitationsIgnoreGlobs pins the rule that a glob is not a citation:
// `*.md` names a class of files, and resolving it as a path fails the gate on a
// document that is describing the gate itself. That is not hypothetical —
// CODE_REVIEW.md §8 states this check's rule in exactly those terms, and the
// gate failed on its own standard the first time it ran afterwards.
func TestDocumentCitationsIgnoreGlobs(t *testing.T) {
	fixture := newFixtureRepo(t)
	fixture.write("AGENTS.md",
		"# Agent instructions\n\nA rule about every `*.md` and each `docs/*.md`.\n")
	fixture.git("add", "-A")

	found := Findings([]Section{checkDocumentCitations(fixture.root)})
	if len(found) != 0 {
		t.Fatalf("a glob is not a citation: %v", details(found))
	}
}

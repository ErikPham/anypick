package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// versionSource is one place a release version is declared.
//
// The macOS app ships MARKETING_VERSION, the Tauri app ships Cargo.toml and
// tauri.conf.json, and the npm adapter selects the matching CLI archive. All
// five are the same release; a workflow that synced only some of them would
// silently ship clients labelled with different versions.
type versionSource struct {
	// label appears in check failures.
	label string
	// rel is the path relative to the repository root.
	rel  string
	read func(root string) (string, error)
	// write is nil for the canonical VERSION file, which writeVersion owns.
	write func(root, version string) error
}

// cargoVersionPattern matches the package version only. It is confined to the
// [package] block below, so `rust-version` and dependency versions are never
// touched.
var cargoVersionPattern = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]*)"`)
var cargoPackageBlock = regexp.MustCompile(`(?ms)^\[package\]\n(.*?)(?:^\[|\z)`)

// tauriVersionPattern matches the top-level "version" key at two-space indent.
// tauri.conf.json has exactly one, but the indent keeps the match honest if a
// nested bundle version is ever added.
var tauriVersionPattern = regexp.MustCompile(`(?m)^  "version"\s*:\s*"([^"]*)"`)

// npmVersionPattern matches the package's top-level version key. The package
// is intentionally kept hand-readable, so this avoids reserializing its JSON
// every time a release version is propagated.
var npmVersionPattern = regexp.MustCompile(`(?m)^  "version"\s*:\s*"([^"]*)"`)

func readCargoVersion(root string) (string, error) {
	content, err := readFile(root, cargoToml)
	if err != nil {
		return "", err
	}
	block := cargoPackageBlock.FindStringSubmatch(string(content))
	if block == nil {
		return "", fmt.Errorf("%s has no [package] block", cargoToml)
	}
	m := cargoVersionPattern.FindStringSubmatch(block[1])
	if m == nil {
		return "", fmt.Errorf("%s [package] has no version", cargoToml)
	}
	return strings.TrimSpace(m[1]), nil
}

func writeCargoVersion(root, version string) error {
	content, err := readFile(root, cargoToml)
	if err != nil {
		return err
	}
	block := cargoPackageBlock.FindStringSubmatch(string(content))
	if block == nil {
		return fmt.Errorf("%s has no [package] block", cargoToml)
	}
	updatedBlock := cargoVersionPattern.ReplaceAllString(block[1], `version = "`+version+`"`)
	updated := strings.Replace(string(content), block[1], updatedBlock, 1)
	return writeFile(root, cargoToml, updated)
}

// cargoLockVersionPattern matches the root package version only: the
// [[package]] stanza whose name is anypick-desktop. Dependency stanzas are
// never touched.
var cargoLockVersionPattern = regexp.MustCompile("(?m)^\\[\\[package\\]\\]\\nname = \"anypick-desktop\"\\nversion = \"([^\"]*)\"")

func readCargoLockVersion(root string) (string, error) {
	content, err := readFile(root, cargoLock)
	if err != nil {
		return "", err
	}
	m := cargoLockVersionPattern.FindStringSubmatch(string(content))
	if m == nil {
		return "", fmt.Errorf("%s has no anypick-desktop package stanza", cargoLock)
	}
	return strings.TrimSpace(m[1]), nil
}

func writeCargoLockVersion(root, version string) error {
	content, err := readFile(root, cargoLock)
	if err != nil {
		return err
	}
	if !cargoLockVersionPattern.Match(content) {
		return fmt.Errorf("%s has no anypick-desktop package stanza", cargoLock)
	}
	updated := cargoLockVersionPattern.ReplaceAllString(string(content),
		"[[package]]\nname = \"anypick-desktop\"\nversion = \""+version+"\"")
	return writeFile(root, cargoLock, updated)
}

func readTauriVersion(root string) (string, error) {
	content, err := readFile(root, tauriConf)
	if err != nil {
		return "", err
	}
	m := tauriVersionPattern.FindStringSubmatch(string(content))
	if m == nil {
		return "", fmt.Errorf("%s has no top-level \"version\"", tauriConf)
	}
	return strings.TrimSpace(m[1]), nil
}

func writeTauriVersion(root, version string) error {
	content, err := readFile(root, tauriConf)
	if err != nil {
		return err
	}
	updated := tauriVersionPattern.ReplaceAllString(string(content), `  "version": "`+version+`"`)
	return writeFile(root, tauriConf, updated)
}

func readNpmVersion(root string) (string, error) {
	content, err := readFile(root, npmPackage)
	if err != nil {
		return "", err
	}
	m := npmVersionPattern.FindStringSubmatch(string(content))
	if m == nil {
		return "", fmt.Errorf("%s has no top-level \"version\"", npmPackage)
	}
	return strings.TrimSpace(m[1]), nil
}

func writeNpmVersion(root, version string) error {
	content, err := readFile(root, npmPackage)
	if err != nil {
		return err
	}
	if !npmVersionPattern.Match(content) {
		return fmt.Errorf("%s has no top-level \"version\"", npmPackage)
	}
	updated := npmVersionPattern.ReplaceAllString(string(content), `  "version": "`+version+`"`)
	return writeFile(root, npmPackage, updated)
}

// secondarySources are the declarations that must agree with VERSION.
// VERSION itself is handled separately because it is the canonical value.
var secondarySources = []versionSource{
	{label: "Xcode MARKETING_VERSION", rel: xcodeProject, read: readXcodeMarketingVersion, write: writeXcodeMarketingVersion},
	{label: "Cargo.toml", rel: cargoToml, read: readCargoVersion, write: writeCargoVersion},
	{label: "Cargo.lock", rel: cargoLock, read: readCargoLockVersion, write: writeCargoLockVersion},
	{label: "tauri.conf.json", rel: tauriConf, read: readTauriVersion, write: writeTauriVersion},
	{label: "npm package.json", rel: npmPackage, read: readNpmVersion, write: writeNpmVersion},
}

// bumpVersion applies a semver bump to x.y.z. Extracted from cmdBump so the
// arithmetic is testable without touching the filesystem.
func bumpVersion(current, part string) (string, error) {
	fields := strings.Split(current, ".")
	if len(fields) != 3 {
		return "", fmt.Errorf("%q does not look like x.y.z", current)
	}
	nums := make([]int, 3)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return "", fmt.Errorf("%q is not numeric", current)
		}
		nums[i] = n
	}
	switch part {
	case "major":
		nums[0]++
		nums[1], nums[2] = 0, 0
	case "minor":
		nums[1]++
		nums[2] = 0
	case "patch":
		nums[2]++
	default:
		return "", fmt.Errorf("bump part must be major, minor or patch, got %q", part)
	}
	return fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2]), nil
}

// commitSubject is the Conventional Commits header: `type(scope)!: summary`.
var commitSubject = regexp.MustCompile(`^([a-zA-Z]+)(\([^)]*\))?(!)?:`)

// breakingFooter matches the two forms Conventional Commits allows for a
// breaking change in the body.
var breakingFooter = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE:`)

// planBump decides the next release from a set of commit messages.
//
// Only feat, fix and perf are release-worthy, per CONTRIBUTING.md. chore, docs,
// test, refactor and build are real work that a release reader does not need
// to see, so a run containing only those stops before changing anything.
//
// It returns "", "major", "minor" or "patch": "" means there is nothing to
// release, and the caller should stop before changing anything. A breaking
// change outranks a feature, which outranks a fix, because the highest impact
// must survive — a release that bumps patch while carrying a breaking change
// would lie to everyone downstream.
func planBump(messages []string) string {
	rank := map[string]int{"": 0, "patch": 1, "minor": 2, "major": 3}
	bump := ""
	note := func(candidate string) {
		if rank[candidate] > rank[bump] {
			bump = candidate
		}
	}
	for _, message := range messages {
		if breakingFooter.MatchString(message) {
			note("major")
			continue
		}
		subject := message
		if idx := strings.Index(subject, "\n"); idx >= 0 {
			subject = subject[:idx]
		}
		m := commitSubject.FindStringSubmatch(strings.TrimSpace(subject))
		if m == nil {
			continue
		}
		if m[3] == "!" {
			note("major")
			continue
		}
		switch strings.ToLower(m[1]) {
		case "feat":
			note("minor")
		case "fix", "perf":
			note("patch")
		}
	}
	return bump
}

func readFile(root, rel string) ([]byte, error) {
	content, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", rel, err)
	}
	return content, nil
}

func writeFile(root, rel, content string) error {
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", rel, err)
	}
	return nil
}

// runGit shells out. This tool only ever runs in a clean checkout inside CI or
// on a developer's machine, and git is the one dependency a release already
// has.
func runGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// cmdPlan decides whether there is anything to release and prints the answer as
// KEY=VALUE lines for the workflow to consume.
//
// It replaces scripts/release-plan.mjs, which was deleted with the JavaScript
// toolchain. The version arithmetic and the Conventional Commits reading live
// in planBump and bumpVersion so they can be tested without a repository.
func cmdPlan(root string) int {
	current, err := readVersionFile(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !stableVersion.MatchString(current) {
		fmt.Fprintf(os.Stderr, "error: %s must be a stable x.y.z version, got %q\n", versionFile, current)
		return 1
	}
	tag := "v" + current

	tagged := gitTagExists(root, tag)
	if tagged && gitTagPointsAtHead(root, tag) {
		// A rerun after a partial external failure: the tag is already the
		// release, so reuse it rather than inventing a second version.
		emit("resume", "current", current, tag)
		return 0
	}

	revRange := "HEAD"
	if tagged {
		revRange = tag + "..HEAD"
	}
	messages, err := gitLogMessages(root, revRange)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	bump := planBump(messages)
	if bump == "" {
		emit("none", "none", current, tag)
		return 0
	}
	next, berr := bumpVersion(current, bump)
	if berr != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", versionFile, berr)
		return 1
	}
	emit("release", bump, next, "v"+next)
	return 0
}

func emit(mode, bump, version, tag string) {
	fmt.Printf("mode=%s\n", mode)
	fmt.Printf("bump=%s\n", bump)
	fmt.Printf("version=%s\n", version)
	fmt.Printf("tag=%s\n", tag)
}

func gitTagExists(root, tag string) bool {
	out, err := runGit(root, "tag", "--list", tag)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == tag
}

func gitTagPointsAtHead(root, tag string) bool {
	tagged, err := runGit(root, "rev-list", "-n", "1", tag)
	if err != nil {
		return false
	}
	head, err := runGit(root, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(tagged) == strings.TrimSpace(head)
}

// gitLogMessages returns the full commit messages in a revision range, newest
// first. %B is the raw body, which is where the BREAKING CHANGE footer lives.
func gitLogMessages(root, revRange string) ([]string, error) {
	out, err := runGit(root, "log", "--format=%x00%B", revRange)
	if err != nil {
		return nil, err
	}
	var messages []string
	for _, part := range strings.Split(out, "\x00") {
		part = strings.TrimSpace(part)
		if part != "" {
			messages = append(messages, part)
		}
	}
	return messages, nil
}

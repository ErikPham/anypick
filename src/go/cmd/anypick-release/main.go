// Command anypick-release keeps release versions synchronized.
//
// It replaces scripts/release-plan.mjs. With npm distribution gone, the two
// version sources that matter are the VERSION file at the repository root
// (canonical) and MARKETING_VERSION in the Xcode project (what the macOS app
// ships). Everything the old script checked — package.json, five platform
// package manifests, Cargo.toml, tauri.conf.json — belonged to distribution
// paths that no longer exist.
//
// Usage:
//
//	anypick-release check            verify every source agrees with VERSION
//	anypick-release set <x.y.z>      write VERSION and propagate it
//	anypick-release bump <major|minor|patch>
//
// Exit codes: 0 success, 1 out of sync or invalid, 2 usage error.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// versionFile is canonical. It is deliberately a plain file: a release
// version should be readable and diffable without a toolchain.
const versionFile = "VERSION"

const xcodeProject = "macos/AnyPick.xcodeproj/project.pbxproj"

var stableVersion = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	switch args[0] {
	case "check":
		return cmdCheck(root)
	case "set":
		if len(args) != 2 {
			usage()
			return 2
		}
		return cmdSet(root, args[1])
	case "bump":
		if len(args) != 2 {
			usage()
			return 2
		}
		return cmdBump(root, args[1])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: anypick-release check | set <x.y.z> | bump <major|minor|patch>")
}

func cmdCheck(root string) int {
	version, err := readVersionFile(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !stableVersion.MatchString(version) {
		fmt.Fprintf(os.Stderr, "error: %s must be a stable x.y.z version, got %q\n", versionFile, version)
		return 1
	}
	xcode, err := readXcodeMarketingVersion(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if xcode != version {
		fmt.Fprintf(os.Stderr,
			"error: version sources disagree: %s = %s, Xcode MARKETING_VERSION = %s\n",
			versionFile, version, xcode)
		return 1
	}
	fmt.Printf("Release versions are synchronized at %s.\n", version)
	return 0
}

func cmdSet(root, next string) int {
	if !stableVersion.MatchString(next) {
		fmt.Fprintf(os.Stderr, "error: %q is not a stable x.y.z version\n", next)
		return 2
	}
	if code := writeVersion(root, next); code != 0 {
		return code
	}
	fmt.Printf("Release version set to %s.\n", next)
	return 0
}

func cmdBump(root, part string) int {
	current, err := readVersionFile(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fields := strings.Split(current, ".")
	if len(fields) != 3 {
		fmt.Fprintf(os.Stderr, "error: %s does not look like x.y.z: %q\n", versionFile, current)
		return 1
	}
	nums := make([]int, 3)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s is not numeric: %q\n", versionFile, current)
			return 1
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
		fmt.Fprintf(os.Stderr, "error: bump part must be major, minor or patch, got %q\n", part)
		return 2
	}
	next := fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2])
	return cmdSet(root, next)
}

func writeVersion(root, next string) int {
	versionPath := filepath.Join(root, versionFile)
	if err := os.WriteFile(versionPath, []byte(next+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeXcodeMarketingVersion(root, next); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func readVersionFile(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, versionFile))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", versionFile, err)
	}
	return strings.TrimSpace(string(content)), nil
}

// marketingVersionPattern matches the Xcode build setting. Every target in the
// project carries one; they must all agree, which is the check below.
var marketingVersionPattern = regexp.MustCompile(`(?m)^\s*MARKETING_VERSION\s*=\s*([^;]+);\s*$`)

func readXcodeMarketingVersion(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, xcodeProject))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", xcodeProject, err)
	}
	matches := marketingVersionPattern.FindAllStringSubmatch(string(content), -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("%s does not contain MARKETING_VERSION", xcodeProject)
	}
	first := strings.TrimSpace(matches[0][1])
	for _, m := range matches[1:] {
		if strings.TrimSpace(m[1]) != first {
			return "", fmt.Errorf("Xcode MARKETING_VERSION values disagree: %s vs %s",
				first, strings.TrimSpace(m[1]))
		}
	}
	return first, nil
}

func writeXcodeMarketingVersion(root, version string) error {
	path := filepath.Join(root, xcodeProject)
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	count := 0
	updated := marketingVersionPattern.ReplaceAllStringFunc(string(content), func(match string) string {
		count++
		inner := marketingVersionPattern.FindStringSubmatch(match)
		return strings.Replace(match, inner[1], version, 1)
	})
	if count == 0 {
		return fmt.Errorf("%s does not contain MARKETING_VERSION", xcodeProject)
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// repoRoot walks up from the working directory to find the VERSION file, so
// the command works from the repository root or any subdirectory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, versionFile)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find %s in any parent directory", versionFile)
		}
		dir = parent
	}
}

// Package artifact holds the release-artifact gates.
//
// ADR 0020 §8: a native download must be independently inspectable as evidence
// that it contains no Node runtime. This package is that inspection, in Go, so
// the gate no longer depends on a JavaScript toolchain to verify the absence of
// one.
package artifact

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxTextBytes is the content-scan bound shared with
// src/macos/scripts/check-node-free-app.sh. Files larger than this are exempt from
// the developer-path scan (they are still subject to the name, extension,
// symlink and shebang rules). Anything at or under the bound is scanned in
// full, not a prefix.
const MaxTextBytes = 512 * 1024

var forbiddenNames = map[string]bool{
	"node": true, "node.exe": true,
	"npm": true, "npm.cmd": true, "npm.ps1": true,
	"npx": true, "npx.cmd": true,
	"pnpm": true, "pnpm.cjs": true, "pnpm.cmd": true, "pnpm.exe": true,
	"tsx": true, "tsx.cmd": true,
	"plugin-host.mjs": true, "plugin-host.js": true,
}

var forbiddenExtensions = []string{".mjs", ".cjs"}

// devPathPattern matches absolute developer paths and the Go environment
// variables that would pin an artifact to one machine. The leading alternation
// anchors on a boundary character so a path fragment inside a longer token
// (a bundle id, a URL host) is not a false positive.
// The Homebrew prefix matches the shell gate as a prefix: node, node@22, and
// node@22.14.0 all fail that script. Requiring a slash or an integer-only
// version let those paths through this scanner.
var devPathPattern = regexp.MustCompile(
	`(?:^|[\s"'=])(?:/Users/|/home/|/opt/homebrew/opt/node|GOROOT=|GOPATH=)`,
)

// nodeShebangPattern matches an interpreter line that invokes Node.
// Case-insensitive, and nodejs / node_modules are not interpreters: the shell
// gate requires a non-alphanumeric boundary after the name.
var nodeShebangPattern = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])node(?:exe)?(?:[^[:alnum:]_]|$)`)

// Violation is one reason an artifact failed the gate.
type Violation struct {
	Path    string
	Message string
}

func (v Violation) String() string { return v.Message }

// InspectNodeFree walks root and returns every way it violates the ADR 0020
// artifact contract. An empty slice means the artifact is clean.
//
// root is resolved to its real path first, so a symlinked artifact root cannot
// hide an escape behind a relative target.
func InspectNodeFree(root string) []Violation {
	artifactRoot, err := filepath.Abs(root)
	if err != nil {
		return []Violation{{Path: root, Message: fmt.Sprintf("%s is not a directory", root)}}
	}
	realRoot, err := filepath.EvalSymlinks(artifactRoot)
	if err != nil {
		return []Violation{{Path: artifactRoot, Message: fmt.Sprintf("%s is not a directory", artifactRoot)}}
	}
	info, err := os.Lstat(realRoot)
	if err != nil || !info.IsDir() {
		return []Violation{{Path: artifactRoot, Message: fmt.Sprintf("%s is not a directory", artifactRoot)}}
	}

	var out []Violation
	walk(realRoot, realRoot, &out)
	return out
}

// IsNodeFree reports whether root passes the gate.
func IsNodeFree(root string) bool { return len(InspectNodeFree(root)) == 0 }

func walk(entry, realRoot string, out *[]Violation) {
	name := filepath.Base(entry)
	lower := strings.ToLower(name)
	rel := displayPath(entry, realRoot)

	if forbiddenNames[lower] {
		*out = append(*out, Violation{rel, "forbidden name " + rel})
	}
	for _, ext := range forbiddenExtensions {
		if strings.HasSuffix(lower, ext) {
			*out = append(*out, Violation{rel, "forbidden Node module " + rel})
			break
		}
	}
	if lower == "plugin-host" {
		*out = append(*out, Violation{rel, "forbidden plugin-host path " + rel})
	}

	stat, err := os.Lstat(entry)
	if err != nil {
		*out = append(*out, Violation{rel, fmt.Sprintf("unreadable %s: %v", rel, err)})
		return
	}

	switch {
	case stat.Mode()&os.ModeSymlink != 0:
		target, err := filepath.EvalSymlinks(entry)
		if err != nil {
			*out = append(*out, Violation{rel, "dangling symlink " + rel})
			return
		}
		if !isInside(target, realRoot) {
			*out = append(*out, Violation{rel, "symlink " + rel + " escapes the artifact"})
		}
		return
	case stat.IsDir():
		children, err := os.ReadDir(entry)
		if err != nil {
			*out = append(*out, Violation{rel, fmt.Sprintf("unreadable %s: %v", rel, err)})
			return
		}
		for _, child := range children {
			walk(filepath.Join(entry, child.Name()), realRoot, out)
		}
		return
	case !stat.Mode().IsRegular() || stat.Size() == 0:
		return
	}

	content, err := os.ReadFile(entry)
	if err != nil {
		*out = append(*out, Violation{rel, fmt.Sprintf("unreadable %s: %v", rel, err)})
		return
	}
	if hasNodeShebang(content) {
		*out = append(*out, Violation{rel, "Node shebang " + rel})
	}
	// The bound is a performance guard, not a correctness one: a leaked path
	// past it would only appear in a file large enough to be a binary anyway.
	if len(content) > MaxTextBytes {
		return
	}
	if !bytes.Contains(content, []byte{0}) && devPathPattern.Match(content) {
		*out = append(*out, Violation{rel, "developer path leaked in " + rel})
	}
}

// hasNodeShebang reports whether the file starts with a shebang that invokes
// Node. Only the first line is considered, matching the JS scanner.
func hasNodeShebang(content []byte) bool {
	if len(content) < 3 || content[0] != '#' || content[1] != '!' {
		return false
	}
	line := content
	if i := bytes.IndexByte(content, '\n'); i >= 0 {
		line = content[:i]
	}
	return nodeShebangPattern.Match(line)
}

func isInside(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "" || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func displayPath(entry, realRoot string) string {
	rel, err := filepath.Rel(realRoot, entry)
	if err != nil {
		return filepath.ToSlash(entry)
	}
	if rel == "" {
		return filepath.Base(realRoot)
	}
	return filepath.ToSlash(rel)
}

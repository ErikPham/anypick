package repocheck

import (
	"regexp"
	"sort"
)

// The desktop stdio protocol version is declared once per language (Go, Rust,
// Swift, and the Tauri UI's wire.ts). Nothing links the declarations, so
// bumping one and missing another produces an app that refuses its own
// handshake at run time while every test stays green: Go only ever agrees
// with itself.
//
// The Go authority is internal/buildinfo, not internal/desktop. desktop also
// declares a ProtocolVersion, but that is the older tray protocol (1) and is
// deliberately not the number the native app negotiates.

// goProtocolPattern reads the authority constant.
var goProtocolPattern = regexp.MustCompile(`(?m)^const ProtocolVersion = ([0-9]+)`)

// rustProtocolPattern reads the Tauri front end's constant.
var rustProtocolPattern = regexp.MustCompile(`(?m)^pub const PROTOCOL_VERSION: u32 = ([0-9]+);`)

// wireProtocolPattern reads the Tauri UI TypeScript constant.
var wireProtocolPattern = regexp.MustCompile(`(?m)^export const PROTOCOL_VERSION = ([0-9]+);`)

// swiftProtocolPattern reads the number the macOS client sends in its hello.
var swiftProtocolPattern = regexp.MustCompile(`static let protocolVersion = ([0-9]+)`)

// swiftGatePattern reads every version guard the macOS client applies to an
// inbound frame's protocolVersion. A guard left behind at the old number
// silently rejects the frames it was written to protect.
var swiftGatePattern = regexp.MustCompile(`protocolVersion == ([0-9]+)`)

// protocolSource is one place a protocol version is declared or asserted.
type protocolSource struct {
	// label names the declaration in failures.
	label string
	// rel is the file, relative to the repository root.
	rel string
	// pattern extracts the number.
	pattern *regexp.Regexp
	// every collects all matches; by default only the first counts.
	every bool
}

var protocolSources = []protocolSource{
	{"go (authority)", "src/go/internal/buildinfo/buildinfo.go", goProtocolPattern, false},
	{"rust", "src/tauri/app/src/protocol.rs", rustProtocolPattern, false},
	{"swift", "src/macos/AppServiceClient.swift", swiftProtocolPattern, false},
	{"wire.ts", "src/tauri/ui/lib/wire.ts", wireProtocolPattern, false},
}

// CheckProtocol verifies every front end declares the same protocol version the
// Go authority does, including the version guards in the macOS client.
func CheckProtocol(root string) []Section {
	goRel := protocolSources[0].rel
	section := Section{Name: "the front ends agree on the stdio protocol version"}

	authority, finding := protocolVersion(root, protocolSources[0], goRel)
	if finding != nil {
		section.Findings = append(section.Findings, *finding)
		return []Section{section}
	}
	section.Checked = append(section.Checked, "go="+authority)

	for _, source := range protocolSources[1:] {
		version, sourceFinding := protocolVersion(root, source, source.rel)
		if sourceFinding != nil {
			section.Findings = append(section.Findings, *sourceFinding)
			continue
		}
		if version != authority {
			section.Findings = append(section.Findings, fail(
				source.label+" declares protocol "+version+", go declares "+authority,
				"the app and its service negotiate this number at handshake time;",
				"a mismatch refuses every connection"))
			continue
		}
		section.Checked = append(section.Checked, source.label+"="+version)
	}

	// The macOS client also guards individual frames. Those are the declarations
	// a version bump forgets, and the failure they cause (a rejected frame) does
	// not name the number.
	storeRel := "src/macos/Store.swift"
	content, missing := readFile(root, storeRel)
	if missing != nil {
		section.Findings = append(section.Findings, *missing)
		return []Section{section}
	}
	guards := uniqueSorted(captures(swiftGatePattern, content))
	if len(guards) == 0 {
		section.Findings = append(section.Findings, emptyInput("protocolVersion guard", storeRel))
		return []Section{section}
	}
	for _, guard := range guards {
		if guard != authority {
			section.Findings = append(section.Findings, fail(
				storeRel+" guards protocolVersion == "+guard+", go declares "+authority,
				"this client rejects frames whose version does not match"))
			continue
		}
		section.Checked = append(section.Checked, storeRel+" guard "+guard)
	}
	return []Section{section}
}

// protocolVersion reads one declaration. A missing declaration is a finding:
// the check cannot pass by not finding the line it was written to compare.
func protocolVersion(root string, source protocolSource, rel string) (string, *Finding) {
	content, missing := readFile(root, rel)
	if missing != nil {
		return "", missing
	}
	if source.every {
		values := uniqueSorted(captures(source.pattern, content))
		if len(values) == 1 {
			return values[0], nil
		}
		finding := fail(rel+" declares conflicting protocol versions: "+join(values),
			"every declaration in this file is compared; they must agree")
		return "", &finding
	}
	version, ok := capture(source.pattern, content)
	if !ok {
		finding := fail(rel+" no longer declares "+source.label+" protocol version",
			"this check compares it against the others; if the declaration moved,",
			"update the pattern rather than dropping the comparison")
		return "", &finding
	}
	return version, nil
}

func join(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	out := ""
	for i, v := range sorted {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

package repocheck

import (
	"strings"
	"testing"
)

// TestCheckProtocolPassesOnTheFixture is the floor: four declarations that
// agree, plus the client's frame guard, must come back clean.
func TestCheckProtocolPassesOnTheFixture(t *testing.T) {
	fixture := newFixtureRepo(t)
	sections := CheckProtocol(fixture.root)
	for _, finding := range Findings(sections) {
		t.Errorf("clean fixture reported %q", finding.Detail)
	}
	if len(sections) != 1 || len(sections[0].Checked) == 0 {
		t.Fatalf("protocol check inspected nothing: %+v", sections)
	}
}

// TestCheckProtocolFindsDisagreement drives each declaration: a bump in one
// language and not the others is the failure this check exists for, and it
// never fails a build — only the handshake, at run time.
func TestCheckProtocolFindsDisagreement(t *testing.T) {
	cases := []struct {
		name   string
		breaks func(*fixtureRepo)
		expect string
	}{
		{
			name: "rust is left behind",
			breaks: func(f *fixtureRepo) {
				f.write("src/tauri/app/src/protocol.rs", "pub const PROTOCOL_VERSION: u32 = 3;\n")
			},
			expect: "rust declares protocol 3, go declares 2",
		},
		{
			name: "swift is left behind",
			breaks: func(f *fixtureRepo) {
				f.write("src/macos/AppServiceClient.swift",
					"actor AppServiceClient {\n  static let protocolVersion = 1\n}\n")
			},
			expect: "swift declares protocol 1, go declares 2",
		},
		{
			name: "wire.ts is left behind",
			breaks: func(f *fixtureRepo) {
				f.write("src/tauri/ui/lib/wire.ts", "export const PROTOCOL_VERSION = 9;\n")
			},
			expect: "wire.ts declares protocol 9, go declares 2",
		},
		{
			name: "a frame guard is left behind",
			breaks: func(f *fixtureRepo) {
				f.write("src/macos/Store.swift",
					"final class TrayStore {\n  func consume(_ line: String) {\n    guard protocolVersion == 1 else { return }\n  }\n}\n")
			},
			expect: "guards protocolVersion == 1, go declares 2",
		},
		{
			name: "the authority declaration moved",
			breaks: func(f *fixtureRepo) {
				f.write("src/go/internal/buildinfo/buildinfo.go",
					"package buildinfo\n\nconst ServiceProtocolVersion = 2\n")
			},
			expect: "no longer declares go (authority) protocol version",
		},
		{
			name: "the guard disappeared",
			breaks: func(f *fixtureRepo) {
				f.write("src/macos/Store.swift", "final class TrayStore {}\n")
			},
			expect: "found nothing in src/macos/Store.swift",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixtureRepo(t)
			tc.breaks(fixture)
			found := Findings(CheckProtocol(fixture.root))
			if len(found) == 0 {
				t.Fatalf("no finding for %s", tc.name)
			}
			report := strings.Join(details(found), "\n")
			if !strings.Contains(report, tc.expect) {
				t.Fatalf("findings do not mention %q:\n%s", tc.expect, report)
			}
		})
	}
}

func details(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, finding.Detail)
	}
	return out
}

// TestCheckProtocolReadsTheAuthorityNotTheTrayConstant pins the distinction the
// shell gate also had to make: internal/desktop declares ProtocolVersion = 1
// for the older tray protocol, and treating that as the negotiated number would
// fail every run.
func TestCheckProtocolReadsTheAuthorityNotTheTrayConstant(t *testing.T) {
	fixture := newFixtureRepo(t)
	fixture.write("src/go/internal/desktop/protocol.go", "package desktop\n\nconst ProtocolVersion = 1\n")
	sections := CheckProtocol(fixture.root)
	if !Passed(sections) {
		t.Fatalf("the tray constant must not be compared: %v", details(Findings(sections)))
	}
	if strings.Contains(strings.Join(sections[0].Checked, " "), "=1") {
		t.Fatalf("the tray constant leaked into the comparison: %v", sections[0].Checked)
	}
}

package desktop

import (
	"os"
	"regexp"
	"testing"
)

// rustHostCapabilities reads the capability list the Tauri host actually
// declares. Reading the file is the point: a hand-copied list in this test
// would go stale the moment someone edits protocol.rs, and the bug this guards
// is precisely the two sides of the handshake drifting apart. check-protocol
// compares the protocol *version* across the three hosts; nothing compared the
// capability *set*, so a host could satisfy the number and still be rejected
// at runtime.
func rustHostCapabilities(t *testing.T) []string {
	t.Helper()
	const rel = "../../../tauri/app/src/protocol.rs"
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	block := regexp.MustCompile(`(?s)HOST_CAPABILITIES:[^=]*=\s*&\[(.*?)\]`).FindSubmatch(raw)
	if block == nil {
		t.Fatalf("%s no longer declares HOST_CAPABILITIES", rel)
	}
	found := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(string(block[1]), -1)
	if len(found) == 0 {
		// repocheck's rule applies here too: an extraction that matched nothing
		// reads as a pass and proves nothing.
		t.Fatalf("%s declares HOST_CAPABILITIES but the extraction matched nothing", rel)
	}
	caps := make([]string, 0, len(found))
	for _, m := range found {
		caps = append(caps, m[1])
	}
	return caps
}

// TestTauriHostCapabilitiesSatisfyTheEngineHandshake drives the engine's own
// first-frame decoder with the Tauri host's own declared capabilities. The
// handshake is the only thing that decides whether the Tauri app runs at all,
// and its failure mode is a rejection the user sees as an app that never
// connects.
func TestTauriHostCapabilitiesSatisfyTheEngineHandshake(t *testing.T) {
	host := validHostHello()
	host.Capabilities = rustHostCapabilities(t)
	if _, err := decodeHostHello(FrameHostHello(host)); err != nil {
		t.Fatalf("the Tauri host's own capability list is rejected by the engine: %v", err)
	}
}

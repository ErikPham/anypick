package activation

import (
	"encoding/json"
	"strings"
	"testing"
)

// Invariant #2 — proxy secrets never cross an output boundary — is the
// highest-precedence rule in AGENTS.md, and this is the one place it rests on a
// struct tag instead of on a check: ManagedProxyRef.Token carries `json:"-"`.
//
// Every other boundary has something behind it. `Status` has
// TestStatusNeverExposesToken, which marshals the value and looks for the
// secret. The desktop gateway secret has two tests. The CLI, the TUI and the
// desktop protocol never name the field at all — the TUI drops it with a
// comment, and cmd/ and internal/desktop contain no reference to it whatsoever.
//
// Here, the only thing between the token and a `--json` plan is a tag. A tag is
// structural, mechanical and silently violable: deleting it compiles, fails no
// test, and prints the proxy token. CODE_REVIEW.md §5 puts that shape on rung 3
// — a test that fails the build — rather than rung 1, a comment that claims the
// invariant is satisfied.
//
// The plan is not marshalled today. That is precisely why the tag is worth
// pinning: it is the thing that will still be true on the day someone adds
// `--json` output for a plan, and nothing else would notice if it stopped
// being true.
func TestManagedProxyTokenIsExcludedFromJSON(t *testing.T) {
	const secret = "proxy-token-must-never-be-marshalled"

	resolved := ResolvedTransport{
		Endpoint: "http://127.0.0.1:8787",
		ManagedProxy: &ManagedProxyRef{
			Provider: "codex",
			Account:  "work",
			Port:     8787,
			LeaseID:  "lease-1",
			Token:    secret,
		},
	}

	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(encoded)

	if strings.Contains(got, secret) {
		t.Fatalf("the proxy token reached a JSON boundary: %s", got)
	}
	// Positive controls. Without them this test would also pass if the struct
	// marshalled to nothing, which is the failure mode the rest of this
	// repository calls "a check over zero entries proves nothing".
	if !strings.Contains(got, `"port":8787`) {
		t.Fatalf("the managed proxy was not marshalled at all, so the check above proved nothing: %s", got)
	}
	if !strings.Contains(got, `"leaseId":"lease-1"`) {
		t.Fatalf("the managed proxy lease was not marshalled, so the token check is not specific: %s", got)
	}
	if !strings.Contains(got, `"endpoint":"http://127.0.0.1:8787"`) {
		t.Fatalf("the endpoint was not marshalled, so the transport is not on the boundary: %s", got)
	}
}

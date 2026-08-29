package transfer

// Cross-runtime contract fixtures (go/testdata/parity): a TypeScript-produced
// envelope must decode identically in Go, and a rejected envelope must be
// rejected for the same reason. If the TS codec changes, these tests force an
// explicit corresponding Go decision.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const fixtureEnvelope = `{
  "version": 1,
  "kind": "anypick-account",
  "meta": {
    "name": "work",
    "provider": "codex",
    "createdAt": "2026-01-01T00:00:00.000Z",
    "updatedAt": "2026-01-02T00:00:00.000Z",
    "label": "Work login",
    "identity": "{\"email\":\"user@example.com\"}",
    "credentialKind": "native"
  },
  "proxy": {
    "enabled": true,
    "port": 9100,
    "host": "127.0.0.1",
    "options": { "upstreamUrl": "https://upstream.invalid" }
  },
  "files": {
    "snapshot/auth.json": "eyJ0b2tlbnMiOnsiYWNjZXNzX3Rva2VuIjoiYWJjIn19",
    "snapshot/config.toml": "bW9kZWwgPSAiZ3B0LTUuNi1zb2wiCg=="
  }
}
`

func TestFixtureEnvelopeFromTypeScriptDecodes(t *testing.T) {
	// Keep a checked-in copy in sync with this literal.
	path := filepath.Join("..", "..", "testdata", "parity", "account-envelope.json")
	if raw, err := os.ReadFile(path); err == nil {
		var want, got interface{}
		if json.Unmarshal(raw, &want) != nil || json.Unmarshal([]byte(fixtureEnvelope), &got) != nil {
			t.Fatal("fixture files are not valid JSON")
		}
		wj, _ := json.Marshal(want)
		gj, _ := json.Marshal(got)
		if string(wj) != string(gj) {
			t.Fatal("testdata/parity/account-envelope.json drifted from the fixture contract; update both together")
		}
	} else {
		t.Fatalf("missing parity fixture %s: %v", path, err)
	}

	env, err := DecodeEnvelope([]byte(fixtureEnvelope), "codex")
	if err != nil {
		t.Fatalf("TS-shaped envelope rejected by Go codec: %v", err)
	}
	if env.Meta.Name != "work" || env.Meta.Label != "Work login" {
		t.Fatalf("meta decoded wrong: %+v", env.Meta)
	}
	// Proxy options and activation state never survive the boundary.
	if env.Proxy == nil || env.Proxy.Enabled || env.Proxy.Options != nil {
		t.Fatalf("imported proxy config must be disabled without options: %+v", env.Proxy)
	}
	if len(env.Files) != 2 {
		t.Fatalf("files lost: %d", len(env.Files))
	}
	data, err := stdBase64.DecodeString(env.Files["snapshot/auth.json"])
	if err != nil || string(data) != `{"tokens":{"access_token":"abc"}}` {
		t.Fatalf("file payload decoded wrong: %q (%v)", data, err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/refs"
)

// seedNamedCodexProfile writes the native files one activation would have
// written for a named Codex profile and stores its route, so the CLI test
// exercises the same state the app produces.
func seedNamedCodexProfile(t *testing.T, name string, source refs.Ref) string {
	t.Helper()
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	root := a.Root
	if _, err := a.CodexProfileSvc.UpsertContext(context.Background(), "codex", name, bindings.Spec{
		Client: "codex", Source: source, Model: bindings.ExplicitModel("gpt-5.6"),
		TransportPolicy: "auto",
	}); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	home := client.CodexProfileHomePath(root, name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client.CodexProfileAuthPath(root, name),
		[]byte(`{"OPENAI_API_KEY":"sk-`+name+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client.CodexProfileConfigPathForRoot(root, name),
		[]byte("# BEGIN anypick-managed\n# END anypick-managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeLiveCodexAuth(t *testing.T, home, token string) {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"auth_mode":"chatgpt","tokens":{"access_token":"` + token +
		`","account_id":"acct-1"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The user-visible half of the cascade: `account remove` says which named
// profiles went with the account, in both the human and the JSON surface.
func TestAccountRemoveReportsAndRemovesNamedProfiles(t *testing.T) {
	base := isolateEnv(t)
	defer resetGlobals()()
	writeLiveCodexAuth(t, filepath.Join(base, "home"), "token-gone")

	var code int
	out, errOut := captureBoth(t, func() {
		code = run([]string{"add", "account", "codex", "--name", "gone", "--current"})
	})
	if code != 0 {
		t.Fatalf("add account exited %d (stdout=%q stderr=%q)", code, out, errOut)
	}
	root := seedNamedCodexProfile(t, "gone", refs.AccountRef("codex", "gone"))

	out, errOut = captureBoth(t, func() {
		code = run([]string{"account", "remove", "codex", "gone", "--yes"})
	})
	if code != 0 {
		t.Fatalf("account remove exited %d (stdout=%q stderr=%q)", code, out, errOut)
	}
	if !strings.Contains(out, "codex/gone") {
		t.Fatalf("removal did not report the named profile cleanup: %q", out)
	}
	for _, path := range []string{
		client.CodexProfileHomePath(root, "gone"),
		client.CodexProfileAuthPath(root, "gone"),
		client.CodexProfileConfigPathForRoot(root, "gone"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("account remove left %s behind (stat err = %v)", path, err)
		}
	}
}

func TestAccountRemoveJSONListsCleanedProfiles(t *testing.T) {
	base := isolateEnv(t)
	defer resetGlobals()()
	writeLiveCodexAuth(t, filepath.Join(base, "home"), "token-gone")

	var code int
	captureBoth(t, func() {
		code = run([]string{"add", "account", "codex", "--name", "gone", "--current"})
	})
	if code != 0 {
		t.Fatalf("add account exited %d", code)
	}
	seedNamedCodexProfile(t, "gone", refs.AccountRef("codex", "gone"))

	out, errOut := captureBoth(t, func() {
		code = run([]string{"account", "remove", "codex", "gone", "--yes", "--json"})
	})
	if code != 0 {
		t.Fatalf("account remove --json exited %d (stdout=%q stderr=%q)", code, out, errOut)
	}
	var payload struct {
		Profiles []string `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("--json output is not machine-readable: %v (%q)", err, out)
	}
	if len(payload.Profiles) != 1 || payload.Profiles[0] != "codex/gone" {
		t.Fatalf("profiles field = %#v, want [codex/gone]", payload.Profiles)
	}
}

// An account with no named profile must keep the old output shape: the field
// is present and empty rather than absent or null, so a script reading it does
// not have to special-case the common path.
func TestAccountRemoveWithoutProfilesKeepsEmptyList(t *testing.T) {
	base := isolateEnv(t)
	defer resetGlobals()()
	writeLiveCodexAuth(t, filepath.Join(base, "home"), "token-lonely")

	var code int
	captureBoth(t, func() {
		code = run([]string{"add", "account", "codex", "--name", "lonely", "--current"})
	})
	if code != 0 {
		t.Fatalf("add account exited %d", code)
	}
	out, errOut := captureBoth(t, func() {
		code = run([]string{"account", "remove", "codex", "lonely", "--yes", "--json"})
	})
	if code != 0 {
		t.Fatalf("account remove --json exited %d (stdout=%q stderr=%q)", code, out, errOut)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	raw, ok := payload["profiles"]
	if !ok {
		t.Fatalf("profiles field missing from %q", out)
	}
	if string(raw) != "[]" {
		t.Fatalf("profiles = %s, want an empty list", raw)
	}
}

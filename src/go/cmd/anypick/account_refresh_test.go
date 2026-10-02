package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
)

// TestAccountRefreshProviderFilter pins the documented `[<provider> [<name>]]`
// grammar: naming one provider must refresh only that provider's accounts. The
// old implementation treated a lone provider as "no filter" and silently
// refreshed everything.
func TestAccountRefreshProviderFilter(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, seed := range []struct{ provider, name string }{
		{"deepseek", "one"},
		{"cerebras", "two"},
	} {
		if _, err := a.Accounts.SaveInput(ctx, seed.provider, seed.name, accounts.CredentialInput{
			Kind: "api-key", Secret: "sk-" + seed.name, Profile: "global",
		}, true); err != nil {
			closeFn()
			t.Fatalf("seed %s/%s: %v", seed.provider, seed.name, err)
		}
	}
	closeFn()

	refresh := func(args ...string) []struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Status   string `json:"status"`
	} {
		t.Helper()
		var code int
		captureBoth(t, func() { code = run(append(args, "--json")) })
		if code != int(anyerr.Success) {
			t.Fatalf("refresh exit = %d (stderr=%q)", code, lastStderr)
		}
		var rows []struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal([]byte(lastStdout), &rows); err != nil {
			t.Fatalf("refresh output is not JSON: %v (%q)", err, lastStdout)
		}
		return rows
	}

	rows := refresh("account", "refresh", "deepseek")
	if len(rows) == 0 {
		t.Fatal("provider filter refreshed nothing")
	}
	for _, row := range rows {
		if row.Provider != "deepseek" {
			t.Fatalf("deepseek filter refreshed %s/%s", row.Provider, row.Name)
		}
	}

	rows = refresh("account", "refresh")
	if len(rows) < 2 {
		t.Fatalf("unfiltered refresh returned %d rows, want both providers", len(rows))
	}
}

// A provider-wide refresh must not copy the live login onto accounts that hold
// a different one: that is how six codex accounts collapsed into one snapshot
// (their updated_at all landed in the same 20ms on 2026-09-15). The account
// that owns the live credential refreshes; the others refuse and keep theirs.
func TestAccountRefreshDoesNotCollapseDistinctAccounts(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	authPath := filepath.Join(os.Getenv("HOME"), ".grok", "auth.json")
	writeLive := func(email string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, []byte(`{"sess":{"email":"`+email+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeLive("one@grok.xai")
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Accounts.Save(ctx, "grok", "one", false); err != nil {
		closeFn()
		t.Fatal(err)
	}
	writeLive("two@grok.xai")
	if _, err := a.Accounts.Save(ctx, "grok", "two", false); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	// The live login belongs to grok/one again; refreshing the provider must
	// leave grok/two's credential alone.
	writeLive("one@grok.xai")
	var code int
	captureBoth(t, func() { code = run([]string{"account", "refresh", "grok", "--json"}) })
	if code != int(anyerr.Success) {
		t.Fatalf("refresh exit = %d (stderr=%q)", code, lastStderr)
	}
	var rows []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(lastStdout), &rows); err != nil {
		t.Fatalf("refresh output is not JSON: %v (%q)", err, lastStdout)
	}
	status := map[string]string{}
	for _, row := range rows {
		status[row.Name] = row.Status
	}
	if !strings.Contains(status["one"], "refreshed") {
		t.Fatalf("grok/one status = %q, want refreshed", status["one"])
	}
	if !strings.Contains(status["two"], "grok/one") {
		t.Fatalf("grok/two status = %q, want the duplicate refusal naming grok/one", status["two"])
	}

	a2, closeFn2, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn2()
	two, err := a2.Accounts.Get(ctx, "grok", "two")
	if err != nil || two == nil {
		t.Fatalf("grok/two = %+v, %v", two, err)
	}
	raw, err := os.ReadFile(filepath.Join(two.SnapshotDir, "auth.json"))
	if err != nil || !strings.Contains(string(raw), "two@grok.xai") {
		t.Fatalf("grok/two credential was overwritten: %s %v", raw, err)
	}
}

// A named refresh that hits the snapshot alias guard must keep the error text
// and the suggestions in the JSON row. Dropping either leaves a script with a
// status and no next step, or a next step and no status.
func TestAccountRefreshJSONKeepsSuggestions(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	authPath := filepath.Join(os.Getenv("HOME"), ".grok", "auth.json")
	writeLive := func(email string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, []byte(`{"sess":{"email":"`+email+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLive("one@grok.xai")
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Accounts.Save(ctx, "grok", "one", false); err != nil {
		closeFn()
		t.Fatal(err)
	}
	writeLive("two@grok.xai")
	if _, err := a.Accounts.Save(ctx, "grok", "two", false); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	writeLive("one@grok.xai")
	var code int
	captureBoth(t, func() { code = run([]string{"account", "refresh", "grok", "two", "--json"}) })
	if code != int(anyerr.Operational) {
		t.Fatalf("named refresh exit = %d, want operational (stderr=%q stdout=%q)", code, lastStderr, lastStdout)
	}
	var rows []struct {
		Name        string   `json:"name"`
		Status      string   `json:"status"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(lastStdout), &rows); err != nil {
		t.Fatalf("refresh output is not JSON: %v (%q)", err, lastStdout)
	}
	if len(rows) != 1 || rows[0].Name != "two" {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rows[0].Status, "grok/one") {
		t.Fatalf("status dropped the alias: %q", rows[0].Status)
	}
	joined := strings.Join(rows[0].Suggestions, "\n")
	if !strings.Contains(joined, "Use grok/one; AnyPick keeps one account per login.") ||
		!strings.Contains(joined, "anypick account remove grok/one") {
		t.Fatalf("suggestions = %#v", rows[0].Suggestions)
	}
	if strings.Contains(lastStdout, "one@grok.xai") || strings.Contains(lastStdout, `"sess"`) {
		t.Fatal("refresh JSON included the live credential")
	}
}

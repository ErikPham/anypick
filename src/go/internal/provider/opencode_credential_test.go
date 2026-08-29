package provider

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
)

// openWritableCredentialDB creates dir/opencode.db as an empty SQLite file and
// opens it for fixture inserts.
func openWritableCredentialDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, "opencode.db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.OpenForeign(path, false)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// seedLiveOpenCodeDB creates a beta-CLI-shaped credential store in dir and
// points OPENCODE_DATA_DIR at it. The returned cleanup restores the env.
func seedLiveOpenCodeDB(t *testing.T, rows map[string]string) {
	t.Helper()
	dir := t.TempDir()
	d := openWritableCredentialDB(t, dir)
	if _, err := d.Exec(`CREATE TABLE credential (
		id text PRIMARY KEY, integration_id text, label text NOT NULL,
		value text NOT NULL, connector_id text, method_id text,
		active integer, time_created integer NOT NULL, time_updated integer NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	i := 0
	for service, key := range rows {
		value, _ := json.Marshal(map[string]string{"type": "key", "key": key})
		if _, err := d.Exec(
			"INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) VALUES (?, ?, 'default', ?, 0, 0)",
			"cred_"+string(rune('a'+i)), service, string(value)); err != nil {
			t.Fatal(err)
		}
		i++
	}
	d.Close()
	t.Setenv("OPENCODE_DATA_DIR", dir)
}

func opencodeGoProviderForTest(t *testing.T) *Compatible {
	t.Helper()
	for _, def := range BuiltinDefinitions() {
		if def.ID == "opencode-go" {
			pv, err := NewCompatible(def)
			if err != nil {
				t.Fatal(err)
			}
			return pv
		}
	}
	t.Fatal("builtin opencode-go definition missing")
	return nil
}

// The live CLI login outranks a saved mirror: a snapshot saved before a
// re-login must not keep serving the previous account's key.
func TestResolveUpstreamPrefersLiveOpenCodeKey(t *testing.T) {
	pv := opencodeGoProviderForTest(t)
	snap := t.TempDir()
	if _, err := pv.BackupInput("subscription-key", "sk-stale-saved", "global", snap); err != nil {
		t.Fatal(err)
	}
	seedLiveOpenCodeDB(t, map[string]string{"opencode-go": "sk-live-key"})

	up, err := pv.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	if up.Auth != "sk-live-key" {
		t.Errorf("Auth = %q, want live db key", up.Auth)
	}
	if !strings.Contains(up.BaseURL.String(), "/zen/go/") {
		t.Errorf("BaseURL = %s, want go gateway", up.BaseURL)
	}
}

// Without a usable live store (older CLI, other machine) the saved secret is
// still authoritative.
func TestResolveUpstreamKeepsStoredSecretWithoutLiveDB(t *testing.T) {
	pv := opencodeGoProviderForTest(t)
	snap := t.TempDir()
	if _, err := pv.BackupInput("subscription-key", "sk-stored-secret", "global", snap); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_DATA_DIR", filepath.Join(t.TempDir(), "missing"))

	up, err := pv.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	if up.Auth != "sk-stored-secret" {
		t.Errorf("Auth = %q, want stored secret", up.Auth)
	}
}

// Non-platform integrations and oauth-shaped rows never satisfy a Zen/Go
// gateway.
func TestLiveOpenCodeKeyIgnoresNonPlatformRows(t *testing.T) {
	dir := t.TempDir()
	d := openWritableCredentialDB(t, dir)
	if _, err := d.Exec(`CREATE TABLE credential (
		id text PRIMARY KEY, integration_id text, label text NOT NULL,
		value text NOT NULL, connector_id text, method_id text,
		active integer, time_created integer NOT NULL, time_updated integer NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	oauth, _ := json.Marshal(map[string]any{
		"type": "oauth", "access": "a", "refresh": "r", "expires": 1,
	})
	inserts := [][3]string{
		{"c1", "deepseek", `{"type":"key","key":"sk-other"}`},
		{"c2", "openai", string(oauth)},
	}
	for _, ins := range inserts {
		if _, err := d.Exec(
			"INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) VALUES (?, ?, 'default', ?, 0, 0)",
			ins[0], ins[1], ins[2]); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	t.Setenv("OPENCODE_DATA_DIR", dir)
	if got := preferredLiveOpenCodeKey("opencode-go"); got != "" {
		t.Errorf("key = %q, want empty for non-platform rows", got)
	}
}

// A db-only login counts as present for the native provider.
func TestOpenCodeDetectLiveFromDBOnly(t *testing.T) {
	dir := t.TempDir()
	d := openWritableCredentialDB(t, dir)
	if _, err := d.Exec(`CREATE TABLE credential (
		id text PRIMARY KEY, integration_id text, label text NOT NULL,
		value text NOT NULL, connector_id text, method_id text,
		active integer, time_created integer NOT NULL, time_updated integer NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		"INSERT INTO credential VALUES ('c1', 'opencode-go', 'default', '{\"type\":\"key\",\"key\":\"sk-live\"}', NULL, NULL, 1, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	t.Setenv("OPENCODE_DATA_DIR", dir)
	p := newOpenCode(filepath.Join(dir, "home"))
	status, err := p.DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present || status.Identity != "opencode-go" {
		t.Errorf("status = %+v, want present opencode-go", status)
	}
}

package hub

// Cross-runtime contract fixtures (go/testdata/parity): a TypeScript-authored
// proxy_hubs row must decode in Go with the ref→Source mapping and enabled
// flags preserved, a corrupt row must fail closed instead of zero-decoding
// into an empty config, and a Go-written row must carry every key the TS
// decoder (src/core/codec/proxy-hub.ts) requires. If the TS codec changes,
// these tests force an explicit corresponding Go decision.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tsHubConfigFixture = `{
  "name": "default",
  "enabled": true,
  "host": "127.0.0.1",
  "port": 4680,
  "revision": 3,
  "sources": [
    { "ref": { "kind": "account", "provider": "glm", "name": "cn" }, "enabled": true },
    { "ref": { "kind": "account-pool", "provider": "deepseek" }, "enabled": false },
    { "ref": { "kind": "account", "provider": "codex", "name": "ws" } }
  ],
  "modelOwners": [
    { "model": "glm-4.7", "source": { "kind": "account", "provider": "glm", "name": "cn" } }
  ],
  "createdAt": "2026-01-01T00:00:00.000Z",
  "updatedAt": "2026-01-02T00:00:00.000Z"
}
`

func insertHubRow(t *testing.T, st *Store, name, configJSON string) {
	t.Helper()
	_, err := st.DB.Exec(
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name, configJSON, "2026-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
}

func TestHubConfigRowFromTypeScriptDecodes(t *testing.T) {
	// Keep a checked-in copy in sync with this literal.
	path := filepath.Join("..", "..", "testdata", "parity", "hub-config.json")
	if raw, err := os.ReadFile(path); err == nil {
		var want, got interface{}
		if json.Unmarshal(raw, &want) != nil || json.Unmarshal([]byte(tsHubConfigFixture), &got) != nil {
			t.Fatal("fixture files are not valid JSON")
		}
		wj, _ := json.Marshal(want)
		gj, _ := json.Marshal(got)
		if string(wj) != string(gj) {
			t.Fatal("testdata/parity/hub-config.json drifted from the fixture contract; update both together")
		}
	} else {
		t.Fatalf("missing parity fixture %s: %v", path, err)
	}

	st := newHubStore(t)
	insertHubRow(t, st, "default", tsHubConfigFixture)

	cfg, err := st.GetConfig("default")
	if err != nil {
		t.Fatalf("TS-authored hub row rejected by Go store: %v", err)
	}
	if cfg == nil || len(cfg.Sources) != 3 {
		t.Fatalf("decoded sources = %#v, want 3 mapped entries", cfg)
	}
	if cfg.Sources[0].ProviderID != "glm" || cfg.Sources[0].Account != "cn" {
		t.Fatalf("account source = %s/%s, want glm/cn", cfg.Sources[0].ProviderID, cfg.Sources[0].Account)
	}
	if !cfg.Sources[0].IsEnabled() {
		t.Fatal("account source enabled flag lost")
	}
	// account-pool ref → the pool:<provider> serialization.
	if cfg.Sources[1].ProviderID != "deepseek" || cfg.Sources[1].Account != "pool:deepseek" {
		t.Fatalf("pool source = %s/%s, want deepseek/pool:deepseek",
			cfg.Sources[1].ProviderID, cfg.Sources[1].Account)
	}
	if cfg.Sources[1].IsEnabled() {
		t.Fatal("disabled pool source decoded as enabled")
	}
	// TS treats absent enabled as on.
	if cfg.Sources[2].ProviderID != "codex" || cfg.Sources[2].Account != "ws" || !cfg.Sources[2].IsEnabled() {
		t.Fatalf("absent-enabled source = %#v, want codex/ws enabled", cfg.Sources[2])
	}
	if cfg.Revision != 3 {
		t.Fatalf("revision = %d, want 3 preserved", cfg.Revision)
	}
	if len(cfg.modelOwners) == 0 || !strings.Contains(string(cfg.modelOwners), "glm-4.7") {
		t.Fatalf("modelOwners not carried verbatim: %s", string(cfg.modelOwners))
	}
}

func TestHubConfigCorruptRowFailsClosed(t *testing.T) {
	st := newHubStore(t)
	for name, row := range map[string]string{
		"garbage":     `{not json`,
		"wrong-shape": `{"name":"x","sources":[{"nope":1}]}`,
		"bad-ref":     `{"name":"x","sources":[{"ref":{"kind":"gateway","name":"g"}}]}`,
		"null-row":    `null`,
	} {
		insertHubRow(t, st, name, row)
		cfg, err := st.GetConfig(name)
		if err == nil {
			t.Fatalf("%s: corrupt row zero-decoded into %#v, want error", name, cfg)
		}
		if cfg != nil {
			t.Fatalf("%s: corrupt row returned a config alongside an error", name)
		}
		if !strings.Contains(err.Error(), "corrupt hub config") && !strings.Contains(err.Error(), "unrecognized source shape") &&
			!strings.Contains(err.Error(), "unsupported ref kind") {
			t.Fatalf("%s: unexpected error class: %v", name, err)
		}
	}
}

func TestHubWrittenShapeSatisfiesTypeScriptReader(t *testing.T) {
	st := newHubStore(t)
	off := false
	if _, err := st.UpsertConfig(Config{
		Name: "default", Enabled: true, Port: 4919,
		Sources: []Source{
			{ProviderID: "glm", Account: "cn"},
			{ProviderID: "deepseek", Account: "pool:deepseek"},
			{ProviderID: "off", Account: "acct", Enabled: &off},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.DB.QueryRow(`SELECT config_json FROM proxy_hubs WHERE name = 'default'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}

	// Every key decodeProxyHubConfig requires must exist with a valid value.
	var doc struct {
		Name      string `json:"name"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		Revision  int    `json:"revision"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		Sources   []struct {
			Ref *struct {
				Kind     string `json:"kind"`
				Provider string `json:"provider"`
				Name     string `json:"name"`
			} `json:"ref"`
			Enabled bool `json:"enabled"`
		} `json:"sources"`
		ModelOwners []interface{} `json:"modelOwners"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("Go-written row is not valid JSON: %v\n%s", err, raw)
	}
	if doc.Name == "" || doc.Host == "" || doc.Port < 1 || doc.Port > 65535 ||
		doc.Revision < 1 || doc.CreatedAt == "" || doc.UpdatedAt == "" ||
		doc.Sources == nil || doc.ModelOwners == nil {
		t.Fatalf("Go-written row misses keys the TS decoder requires: %s", raw)
	}
	for i, s := range doc.Sources {
		if s.Ref == nil || (s.Ref.Kind != "account" && s.Ref.Kind != "account-pool") || s.Ref.Provider == "" {
			t.Fatalf("source[%d] ref unusable by TS decoder: %s", i, raw)
		}
		if s.Ref.Kind == "account" && s.Ref.Name == "" {
			t.Fatalf("source[%d] account ref missing name: %s", i, raw)
		}
	}
	if doc.Sources[0].Ref.Kind != "account" || doc.Sources[1].Ref.Kind != "account-pool" {
		t.Fatalf("pool convention not round-tripped as account-pool ref: %s", raw)
	}

	// And Go itself re-reads its own write losslessly, including the disabled
	// third source and the preserved TS-only modelOwners field.
	back, err := st.GetConfig("default")
	if err != nil || back == nil || len(back.Sources) != 3 {
		t.Fatalf("Go re-read of own write failed: %v %#v", err, back)
	}
	if back.Sources[1].Account != "pool:deepseek" || back.Sources[2].IsEnabled() {
		t.Fatalf("round-trip lost source identity/enabled: %#v", back.Sources)
	}
	if doc.Sources[2].Enabled {
		t.Fatalf("disabled source written as enabled: %s", raw)
	}
}

// A kernel-assigned hub port is persisted as absent: inventing a concrete
// port collides with the managed builtin proxy's default listener (observed
// as e2e 401s from the wrong listener), so this is the ONE deliberate
// deviation from the TS decoder contract — see tsHubConfig in store.go.
func TestHubKernelAssignedPortStaysAbsentNotInvented(t *testing.T) {
	st := newHubStore(t)
	if _, err := st.UpsertConfig(Config{Name: "auto", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.DB.QueryRow(`SELECT config_json FROM proxy_hubs WHERE name = 'auto'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc["port"]; present {
		t.Fatalf("kernel-assigned port must not be invented, got %s", raw)
	}
}

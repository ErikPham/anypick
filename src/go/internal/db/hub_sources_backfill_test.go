package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// The relational migration must convert a ghost-laden hub without losing
// healthy members or mis-pointing pins: ghosts drop from sources, pins to
// them drop from manifests, surviving pins become value owners.
func TestBackfillHubSourcesCleansGhosts(t *testing.T) {
	root := t.TempDir()
	database, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	mustExec(t, database, `INSERT INTO accounts (provider, name, meta_json, created_at, updated_at)
		VALUES ('workbuddy', 'live', '{}', 't', 't'), ('gemini', 'g', '{}', 't', 't')`)
	config := `{"name":"default","enabled":true,"host":"127.0.0.1","sources":[
		{"ref":{"kind":"account","provider":"workbuddy","name":"ghost"}},
		{"ref":{"kind":"account","provider":"gemini","name":"g"}},
		{"ref":{"kind":"account-pool","provider":"codex"}},
		{"providerId":"workbuddy","account":"live"}],
		"modelOwners":[],"revision":1,"createdAt":"t","updatedAt":"t"}`
	mustExec(t, database, `INSERT INTO proxy_hubs (name, config_json, created_at, updated_at)
		VALUES ('default', ?, 't', 't')`, config)
	manifest := `{"routeId":"default-route","hubName":"default","clientId":"cli","models":[
		{"model":"dead","sourceIndex":0},{"model":"kept","sourceIndex":3},
		{"model":"oob","sourceIndex":9},{"model":"pool","sourceIndex":2}]}`
	mustExec(t, database, `INSERT INTO proxy_hub_routes (route_id, hub_name, manifest_json, token_secret, created_at, updated_at)
		VALUES ('default-route', 'default', ?, 'sec', 't', 't')`, manifest)

	// Simulate the pre-backfill state: rows absent while the document names
	// sources (exactly what the migration itself sees).
	mustExec(t, database, `DELETE FROM proxy_hub_sources`)
	if err := backfillHubSources(database); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	type row struct {
		pos            int
		kind, provider string
		account, pool  string
		enabled        int
	}
	rows, err := database.Query(`SELECT position, kind, provider, account, pool, enabled
		FROM proxy_hub_sources WHERE hub_name = 'default' ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		var provider, account, pool any
		if err := rows.Scan(&r.pos, &r.kind, &provider, &account, &pool, &r.enabled); err != nil {
			t.Fatal(err)
		}
		r.provider, _ = provider.(string)
		r.account, _ = account.(string)
		r.pool, _ = pool.(string)
		got = append(got, r)
	}
	rows.Close()
	want := []row{
		{0, "account", "gemini", "g", "", 1},
		{1, "pool", "codex", "", "codex", 1},
		{2, "account", "workbuddy", "live", "", 1},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	var doc string
	if err := database.QueryRow(`SELECT config_json FROM proxy_hubs WHERE name = 'default'`).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "ghost") {
		t.Fatalf("rewritten config still names the ghost: %s", doc)
	}

	var out string
	if err := database.QueryRow(`SELECT manifest_json FROM proxy_hub_routes WHERE route_id = 'default-route'`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	var manifestDoc struct {
		Models []struct {
			Model    string `json:"model"`
			Owner    string `json:"owner"`
			Upstream string `json:"upstreamModel"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &manifestDoc); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, m := range manifestDoc.Models {
		owners[m.Model] = m.Owner
	}
	if owners["kept"] != "workbuddy/live" || owners["pool"] != "codex/pool:codex" {
		t.Fatalf("owners = %v", owners)
	}
	if _, ok := owners["dead"]; ok {
		t.Fatalf("ghost pin survived: %v", owners)
	}
	if _, ok := owners["oob"]; ok {
		t.Fatalf("out-of-range pin survived: %v", owners)
	}

	// Re-running is a no-op (guard on existing rows).
	if err := backfillHubSources(database); err != nil {
		t.Fatalf("rerun: %v", err)
	}
}

func mustExec(t *testing.T, database *DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

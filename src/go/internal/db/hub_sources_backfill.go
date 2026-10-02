// One-time backfill for the "relational hub sources" migration (ADR-0029).
//
// The JSON shapes below are FROZEN point-in-time copies of the hub wire
// format — they must never be updated to track hub package changes, or a
// future shape would misread a past database. Membership moves to
// proxy_hub_sources (foreign-keyed: ghosts cannot be inserted), route pins
// move from positional SourceIndex to value Owner keys, and ghosts are
// dropped from both. Anything unparseable fails the migration instead of
// inventing state.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// hubBackfillSource is one config_json source entry in either known shape.
type hubBackfillSource struct {
	provider string
	account  string
	pool     string
	// poolProvider preserves the entry's own provider for pool refs so the
	// round-trip is exact (hub stores it alongside; the accounts foreign key
	// skips the NULL account).
	poolProvider string
	enabled      bool
	raw          json.RawMessage
}

func parseHubBackfillSources(data []byte, hub string) ([]hubBackfillSource, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("hub %s config is not JSON", hub)
	}
	rawSources, ok := doc["sources"]
	if !ok {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(rawSources, &entries); err != nil {
		return nil, fmt.Errorf("hub %s sources is not an array", hub)
	}
	out := make([]hubBackfillSource, 0, len(entries))
	for i, entry := range entries {
		where := fmt.Sprintf("sources[%d] of %s", i, hub)
		var probe struct {
			Ref *struct {
				Kind     string `json:"kind"`
				Provider string `json:"provider"`
				Name     string `json:"name"`
			} `json:"ref"`
			Enabled    *bool  `json:"enabled"`
			ProviderID string `json:"providerId"`
			Account    string `json:"account"`
		}
		if err := json.Unmarshal(entry, &probe); err != nil {
			return nil, fmt.Errorf("%s: unreadable entry", where)
		}
		src := hubBackfillSource{enabled: true, raw: entry}
		if probe.Enabled != nil {
			src.enabled = *probe.Enabled
		}
		switch {
		case probe.Ref != nil && probe.Ref.Kind == "account-pool" && probe.Ref.Provider != "":
			src.pool = probe.Ref.Provider
			src.poolProvider = probe.Ref.Provider
		case probe.Ref != nil && probe.Ref.Kind == "account" && probe.Ref.Provider != "" && probe.Ref.Name != "":
			src.provider, src.account = probe.Ref.Provider, probe.Ref.Name
		case probe.ProviderID != "" && strings.HasPrefix(probe.Account, "pool:"):
			src.pool = strings.TrimPrefix(probe.Account, "pool:")
			src.poolProvider = probe.ProviderID
		case probe.ProviderID != "" && probe.Account != "":
			src.provider, src.account = probe.ProviderID, probe.Account
		default:
			return nil, fmt.Errorf("%s: unrecognized source shape", where)
		}
		out = append(out, src)
	}
	return out, nil
}

// hubOwnerKey is the frozen value-pin format: "provider/account", pool refs
// as "provider/pool:provider". It mirrors hub.sourceOwnerKey, duplicated
// here so the migration never drifts with it.
func hubOwnerKey(provider, account string) string { return provider + "/" + account }

func backfillHubSources(d execer) error {
	var count int
	if err := queryRowInt(d, `SELECT COUNT(*) FROM proxy_hub_sources`, &count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hubs, err := queryHubConfigs(d)
	if err != nil {
		return err
	}
	for _, h := range hubs {
		if err := backfillOneHub(d, h.name, h.config); err != nil {
			return err
		}
	}
	return nil
}

type hubConfigRow struct {
	name   string
	config []byte
}

func queryHubConfigs(d execer) ([]hubConfigRow, error) {
	rows, err := d.Query(`SELECT name, config_json FROM proxy_hubs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hubConfigRow
	for rows.Next() {
		var r hubConfigRow
		var cfg string
		if err := rows.Scan(&r.name, &cfg); err != nil {
			return nil, err
		}
		r.config = []byte(cfg)
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryRowInt(d execer, query string, dest *int) error {
	rows, err := d.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return sql.ErrNoRows
	}
	return rows.Scan(dest)
}

func accountExists(d execer, provider, account string) (bool, error) {
	rows, err := d.Query(`SELECT 1 FROM accounts WHERE provider = ? AND name = ?`, provider, account)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), nil
}

func backfillOneHub(d execer, hub string, config []byte) error {
	sources, err := parseHubBackfillSources(config, hub)
	if err != nil {
		return err
	}
	// ownerKeys[i] is "" for dropped ghosts.
	ownerKeys := make([]string, len(sources))
	keptEntries := make([]json.RawMessage, 0, len(sources))
	position := 0
	for i, src := range sources {
		// Mirror hub.insertSourceRow mapping exactly: pool rows keep the
		// entry provider with a NULL account (FK-skipped); the owner key is
		// provider + "/" + account in both worlds.
		kind, provider, account, pool := "account", src.provider, src.account, ""
		if src.pool != "" {
			kind, provider, account, pool = "pool", src.poolProvider, "", src.pool
		}
		owner := hubOwnerKey(provider, "pool:"+pool)
		if kind == "account" {
			owner = hubOwnerKey(provider, account)
			ok, err := accountExists(d, provider, account)
			if err != nil {
				return err
			}
			if !ok {
				continue // ghost: no account row, so no relational row; pins drop below
			}
		}
		ownerKeys[i] = owner
		if err := insertHubSourceRow(d, hub, position, kind, provider, account, pool, src.enabled); err != nil {
			return err
		}
		position++
		keptEntries = append(keptEntries, src.raw)
	}
	if err := rewriteHubConfigSources(d, hub, config, keptEntries); err != nil {
		return err
	}
	return rewriteHubManifestPins(d, hub, ownerKeys)
}

func insertHubSourceRow(d execer, hub string, position int, kind, provider, account, pool string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	var providerArg, accountArg, poolArg any
	if provider != "" {
		providerArg = provider
	}
	if account != "" {
		accountArg = account
	}
	if pool != "" {
		poolArg = pool
	}
	_, err := d.Exec(
		`INSERT INTO proxy_hub_sources (hub_name, position, kind, provider, account, pool, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hub, position, kind, providerArg, accountArg, poolArg, enabledInt)
	return err
}

func rewriteHubConfigSources(d execer, hub string, config []byte, kept []json.RawMessage) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(config, &doc); err != nil {
		return err
	}
	if _, ok := doc["sources"]; !ok {
		return nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	doc["sources"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE proxy_hubs SET config_json = ?, updated_at = ? WHERE name = ?`,
		string(out), time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), hub)
	return err
}

// rewriteHubManifestPins converts positional pins to value owners using the
// pre-removal source list. Pins to dropped ghosts or out-of-range indices
// are corrupt by definition (serving them 502s today) and are removed.
func rewriteHubManifestPins(d execer, hub string, ownerKeys []string) error {
	rows, err := d.Query(`SELECT route_id, manifest_json FROM proxy_hub_routes WHERE hub_name = ?`, hub)
	if err != nil {
		return err
	}
	type row struct{ id, manifest string }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.manifest); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		var manifest map[string]json.RawMessage
		if json.Unmarshal([]byte(r.manifest), &manifest) != nil {
			continue
		}
		rawModels, ok := manifest["models"]
		if !ok {
			continue
		}
		var models []map[string]json.RawMessage
		if json.Unmarshal(rawModels, &models) != nil {
			continue
		}
		kept := make([]map[string]json.RawMessage, 0, len(models))
		changed := false
		for _, m := range models {
			var idx *int
			if v, ok := m["sourceIndex"]; ok {
				var n int
				if json.Unmarshal(v, &n) == nil {
					idx = &n
				}
			}
			if idx == nil || *idx < 0 || *idx >= len(ownerKeys) || ownerKeys[*idx] == "" {
				changed = true // ghost or corrupt pin: drop
				continue
			}
			m["owner"] = json.RawMessage(`"` + ownerKeys[*idx] + `"`)
			delete(m, "sourceIndex")
			changed = true
			kept = append(kept, m)
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		manifest["models"] = encoded
		out, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		if _, err := d.Exec(`UPDATE proxy_hub_routes SET manifest_json = ?, updated_at = ? WHERE route_id = ?`,
			string(out), time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), r.id); err != nil {
			return err
		}
	}
	return nil
}

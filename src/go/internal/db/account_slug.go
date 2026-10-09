package db

import (
	"encoding/json"

	"github.com/outof0/anypick/go/internal/slug"
)

// renameUnsluggedAccountKeys moves an account whose SQL key is not its slug
// onto that slug. Reads slug before they query, so a row stored as "Personal"
// is listed as "personal" and then missed. The slug row is left untouched when
// it already exists: renaming onto it would destroy one of the two logins.
func renameUnsluggedAccountKeys(d execer) error {
	rows, err := d.Query(`SELECT provider, name, meta_json FROM accounts`)
	if err != nil {
		return err
	}
	type legacy struct {
		provider string
		name     string
		meta     string
		slug     string
	}
	var pending []legacy
	for rows.Next() {
		var item legacy
		if err := rows.Scan(&item.provider, &item.name, &item.meta); err != nil {
			_ = rows.Close()
			return err
		}
		slugName, nerr := slug.AccountName(item.name)
		if nerr != nil || slugName == item.name {
			continue
		}
		item.slug = slugName
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		taken, err := countQuery(d,
			`SELECT COUNT(*) FROM accounts WHERE provider = ? AND name = ?`,
			item.provider, item.slug)
		if err != nil {
			return err
		}
		if taken > 0 {
			continue
		}
		if err := renameOneAccountKey(d, item.provider, item.name, item.slug, item.meta); err != nil {
			return err
		}
	}
	return nil
}

func countQuery(d execer, query string, args ...any) (int, error) {
	rows, err := d.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

func renameOneAccountKey(d execer, provider, oldName, slugName, metaJSON string) error {
	metaJSON = rewriteJSONStringField(metaJSON, "name", oldName, slugName)
	if _, err := d.Exec(
		`INSERT INTO accounts (provider, name, meta_json, proxy_json, created_at, updated_at)
		 SELECT provider, ?, ?, proxy_json, created_at, updated_at
		 FROM accounts WHERE provider = ? AND name = ?`,
		slugName, metaJSON, provider, oldName); err != nil {
		return err
	}
	if _, err := d.Exec(
		`INSERT INTO account_snapshot_files (provider, account, path, content)
		 SELECT provider, ?, path, content FROM account_snapshot_files
		 WHERE provider = ? AND account = ?`,
		slugName, provider, oldName); err != nil {
		return err
	}
	if _, err := d.Exec(
		`UPDATE provider_active SET account_name = ? WHERE provider = ? AND account_name = ?`,
		slugName, provider, oldName); err != nil {
		return err
	}
	if err := renameProxyState(d, provider, oldName, slugName); err != nil {
		return err
	}
	if _, err := d.Exec(
		`UPDATE proxy_leases SET account = ? WHERE provider = ? AND account = ?`,
		slugName, provider, oldName); err != nil {
		return err
	}
	if _, err := d.Exec(
		`UPDATE proxy_hub_sources SET account = ? WHERE provider = ? AND account = ?`,
		slugName, provider, oldName); err != nil {
		return err
	}
	if err := renamePoolMembers(d, provider, oldName, slugName); err != nil {
		return err
	}
	if _, err := d.Exec(
		`DELETE FROM accounts WHERE provider = ? AND name = ?`,
		provider, oldName); err != nil {
		return err
	}
	return nil
}

func renameProxyState(d execer, provider, oldName, slugName string) error {
	rows, err := d.Query(
		`SELECT state_json FROM proxy_state WHERE provider = ? AND account = ?`,
		provider, oldName)
	if err != nil {
		return err
	}
	var states []string
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return err
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(states) == 0 {
		return nil
	}
	state := rewriteJSONStringField(states[0], "accountName", oldName, slugName)
	_, err = d.Exec(
		`UPDATE proxy_state SET account = ?, state_json = ? WHERE provider = ? AND account = ?`,
		slugName, state, provider, oldName)
	return err
}

func renamePoolMembers(d execer, provider, oldName, slugName string) error {
	rows, err := d.Query(`SELECT members_json FROM pools WHERE provider = ?`, provider)
	if err != nil {
		return err
	}
	var raw string
	found := false
	if rows.Next() {
		found = true
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !found {
		return nil
	}
	var members []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil
	}
	changed := false
	for i := range members {
		account, ok := members[i]["account"]
		if !ok {
			continue
		}
		var name string
		if json.Unmarshal(account, &name) != nil || name != oldName {
			continue
		}
		encoded, err := json.Marshal(slugName)
		if err != nil {
			return err
		}
		members[i]["account"] = encoded
		changed = true
	}
	if !changed {
		return nil
	}
	next, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE pools SET members_json = ? WHERE provider = ?`, string(next), provider)
	return err
}

func rewriteJSONStringField(raw, key, from, to string) string {
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return raw
	}
	cur, ok := doc[key]
	if !ok {
		return raw
	}
	var value string
	if json.Unmarshal(cur, &value) != nil || value != from {
		return raw
	}
	encoded, err := json.Marshal(to)
	if err != nil {
		return raw
	}
	doc[key] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return string(out)
}

// Package profile ports src/core/profile-store.ts: gateway runtime profiles
// (endpoint + API key material) stored in the profiles table.
package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// Meta mirrors RuntimeProfileMeta (subset the core layer consumes).
type Meta struct {
	Name            string                 `json:"name"`
	Provider        string                 `json:"provider"`
	CreatedAt       string                 `json:"createdAt"`
	UpdatedAt       string                 `json:"updatedAt"`
	Label           string                 `json:"label,omitempty"`
	Notes           string                 `json:"notes,omitempty"`
	Endpoint        string                 `json:"endpoint,omitempty"`
	HeaderNames     []string               `json:"headerNames,omitempty"`
	Models          map[string]string      `json:"models,omitempty"`
	DefaultModel    string                 `json:"defaultModel,omitempty"`
	SonnetModel     string                 `json:"sonnetModel,omitempty"`
	OpusModel       string                 `json:"opusModel,omitempty"`
	HaikuModel      string                 `json:"haikuModel,omitempty"`
	ClientOverrides map[string]interface{} `json:"clientOverrides,omitempty"`
}

// Secrets mirrors RuntimeProfileSecrets. Never surfaced in status output.
type Secrets struct {
	APIKey          string                 `json:"apiKey,omitempty"`
	Headers         map[string]string      `json:"headers,omitempty"`
	ClientOverrides map[string]interface{} `json:"clientOverrides,omitempty"`
}

// Runtime mirrors RuntimeProfile.
type Runtime struct {
	Meta    Meta
	Secrets Secrets
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Store owns the profiles table.
type Store struct{ DB *sql.DB }

func (s *Store) Get(name string) (*Runtime, error) {
	return s.GetContext(context.Background(), name)
}

func (s *Store) GetContext(ctx context.Context, name string) (*Runtime, error) {
	var metaJSON, secretsJSON string
	err := s.DB.QueryRowContext(ctx,
		`SELECT meta_json, secrets_json FROM profiles WHERE name = ?`, name).
		Scan(&metaJSON, &secretsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &Runtime{}
	if err := json.Unmarshal([]byte(metaJSON), &r.Meta); err != nil {
		return nil, anyerr.Wrapf(err, "decode profile meta", "STATE_CONFLICT")
	}
	if secretsJSON == "" {
		secretsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(secretsJSON), &r.Secrets); err != nil {
		return nil, anyerr.Wrapf(err, "decode profile secrets", "STATE_CONFLICT")
	}
	return r, nil
}

func (s *Store) UpsertContext(ctx context.Context, r Runtime) (Runtime, error) {
	now := nowISO()
	existing, err := s.GetContext(ctx, r.Meta.Name)
	if err != nil {
		return Runtime{}, err
	}
	createdAt := now
	if existing != nil {
		createdAt = existing.Meta.CreatedAt
	}
	r.Meta.CreatedAt = createdAt
	r.Meta.UpdatedAt = now
	metaJSON, err := json.Marshal(r.Meta)
	if err != nil {
		return Runtime{}, err
	}
	secretsJSON, err := json.Marshal(r.Secrets)
	if err != nil {
		return Runtime{}, err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO profiles (name, meta_json, secrets_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   meta_json = excluded.meta_json,
		   secrets_json = excluded.secrets_json,
		   updated_at = excluded.updated_at`,
		r.Meta.Name, string(metaJSON), string(secretsJSON), createdAt, now)
	if err != nil {
		return Runtime{}, err
	}
	return r, nil
}

func (s *Store) DeleteContext(ctx context.Context, name string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM profiles WHERE name = ?`, name)
	return err
}

// List returns stored profile names, sorted.
func (s *Store) List() ([]string, error) {
	return s.ListContext(context.Background())
}

func (s *Store) ListContext(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

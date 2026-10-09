// Package profile stores gateway runtime profiles
// (endpoint + API key material) in the profiles table.
package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// Meta is the subset of runtime profile metadata the core layer consumes.
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

// Secrets holds API key material. Never surfaced in status output.
type Secrets struct {
	APIKey          string                 `json:"apiKey,omitempty"`
	Headers         map[string]string      `json:"headers,omitempty"`
	ClientOverrides map[string]interface{} `json:"clientOverrides,omitempty"`
}

type Runtime struct {
	Meta    Meta
	Secrets Secrets
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Store owns the profiles table.
//
// Root is the AnyPick data directory: mutating methods take the coordinator
// lock under it themselves (invariant #5), so callers must not depend on an
// ambient lock. A Store without Root cannot take the lock and its mutating
// methods fail closed instead of writing uncoordinated.
type Store struct {
	DB   *sql.DB
	Root string
}

func (s *Store) mutate(ctx context.Context, scope string, fn func(context.Context) error) error {
	if s.Root == "" {
		return anyerr.Newf("profile store has no data root; refusing uncoordinated write", "STATE_CONFLICT")
	}
	return lock.WithMutationLock(ctx, s.Root, scope, fn)
}

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
	var out Runtime
	err := s.mutate(ctx, refs.MutationScopeForRef(refs.GatewayRef(r.Meta.Name)), func(c context.Context) error {
		got, err := s.upsertLocked(c, r)
		out = got
		return err
	})
	return out, err
}

func (s *Store) upsertLocked(ctx context.Context, r Runtime) (Runtime, error) {
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
	return s.mutate(ctx, refs.MutationScopeForRef(refs.GatewayRef(name)), func(c context.Context) error {
		_, err := s.DB.ExecContext(c, `DELETE FROM profiles WHERE name = ?`, name)
		return err
	})
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

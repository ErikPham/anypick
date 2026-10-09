// Package presets stores reusable binding templates a user can apply to a
// client by name.
package presets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// Saved mirrors SavedPreset. A preset spec's model is limited to
// explicit|omitted (unknown never persists).
type Saved struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Revision   int           `json:"revision"`
	Spec       bindings.Spec `json:"spec"`
	CreatedAt  string        `json:"createdAt"`
	UpdatedAt  string        `json:"updatedAt"`
	LastUsedAt string        `json:"lastUsedAt,omitempty"`
	UseCount   int           `json:"useCount"`
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Store owns the presets table.
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
		return anyerr.Newf("presets store has no data root; refusing uncoordinated write", "STATE_CONFLICT")
	}
	return lock.WithMutationLock(ctx, s.Root, scope, fn)
}

const columns = `id, name, revision, spec_json, created_at, updated_at, last_used_at, use_count`

// List returns every saved preset ordered by name (TS listPresets).
func (s *Store) List() ([]Saved, error) {
	rows, err := s.DB.Query(`SELECT ` + columns + ` FROM presets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Saved
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) ListContext(ctx context.Context) ([]Saved, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM presets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Saved
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scan(row rowScanner) (*Saved, error) {
	var id, name, specJSON, createdAt, updatedAt string
	var revision, useCount int
	var lastUsed sql.NullString
	if err := row.Scan(&id, &name, &revision, &specJSON, &createdAt, &updatedAt, &lastUsed, &useCount); err != nil {
		return nil, err
	}
	p := &Saved{
		ID: id, Name: name, Revision: revision,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
		LastUsedAt: lastUsed.String, UseCount: useCount,
	}
	if err := json.Unmarshal([]byte(specJSON), &p.Spec); err != nil {
		return nil, anyerr.Wrapf(err, "decode preset spec", "STATE_CONFLICT")
	}
	return p, nil
}

func (s *Store) GetByName(name string) (*Saved, error) {
	return s.GetByNameContext(context.Background(), name)
}

func (s *Store) GetByNameContext(ctx context.Context, name string) (*Saved, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM presets WHERE name = ?`, name)
	p, err := scan(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (s *Store) Exists(name string) (bool, error) {
	return s.ExistsContext(context.Background(), name)
}

func (s *Store) ExistsContext(ctx context.Context, name string) (bool, error) {
	p, err := s.GetByNameContext(ctx, name)
	return p != nil, err
}

// Upsert creates or bumps the revision of a named preset.
func (s *Store) UpsertContext(ctx context.Context, name string, spec bindings.Spec) (Saved, error) {
	var out Saved
	err := s.mutate(ctx, refs.MutationScopeForRef(refs.PresetRef(name)), func(c context.Context) error {
		got, err := s.upsertLocked(c, name, spec)
		out = got
		return err
	})
	return out, err
}

func (s *Store) upsertLocked(ctx context.Context, name string, spec bindings.Spec) (Saved, error) {
	now := nowISO()
	existing, err := s.GetByNameContext(ctx, name)
	if err != nil {
		return Saved{}, err
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return Saved{}, err
	}
	if existing == nil {
		id, err := newPresetID()
		if err != nil {
			return Saved{}, anyerr.Wrapf(err, "generate preset id", "DB_WRITE")
		}
		_, err = s.DB.ExecContext(ctx,
			`INSERT INTO presets (id, name, revision, spec_json, created_at, updated_at, use_count)
			 VALUES (?, ?, 1, ?, ?, ?, 0)`,
			id, name, string(specJSON), now, now)
		if err != nil {
			return Saved{}, err
		}
		return Saved{ID: id, Name: name, Revision: 1, Spec: spec, CreatedAt: now, UpdatedAt: now}, nil
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE presets SET spec_json = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`,
		string(specJSON), now, existing.ID)
	if err != nil {
		return Saved{}, err
	}
	existing.Spec = spec
	existing.Revision++
	existing.UpdatedAt = now
	return *existing, nil
}

// MarkUsed records a successful activation of a preset.
func (s *Store) MarkUsedContext(ctx context.Context, id string) error {
	var name string
	err := s.DB.QueryRowContext(ctx, `SELECT name FROM presets WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return s.mutate(ctx, refs.MutationScopeForRef(refs.PresetRef(name)), func(c context.Context) error {
		return s.markUsedLocked(c, id)
	})
}

func (s *Store) markUsedLocked(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE presets SET last_used_at = ?, use_count = use_count + 1 WHERE id = ?`,
		nowISO(), id)
	return err
}

func (s *Store) DeleteByNameContext(ctx context.Context, name string) error {
	return s.mutate(ctx, refs.MutationScopeForRef(refs.PresetRef(name)), func(c context.Context) error {
		_, err := s.DB.ExecContext(c, `DELETE FROM presets WHERE name = ?`, name)
		return err
	})
}

func SourceRefOf(p Saved) refs.Ref { return p.Spec.Source }

type rowScanner interface{ Scan(dest ...any) error }

func newPresetID() (string, error) {
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out), nil
}

func randRead(b []byte) (int, error) { return rand.Read(b) }

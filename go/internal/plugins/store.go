package plugins

import (
	"context"
	"database/sql"
)

// Record is one installed plugin registry row. Presence is deliberately
// separate from Enabled because install is not permission to execute code.
type Record struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Enabled   bool   `json:"enabled"`
	Digest    string `json:"digest"`
	AddedAt   string `json:"addedAt"`
	UpdatedAt string `json:"updatedAt"`
}

const recordColumns = `name, path, version, enabled, digest, added_at, updated_at`

// Store owns all SQL for the plugins table.
type Store struct{ DB *sql.DB }

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (*Record, error) {
	var record Record
	var enabled int64
	if err := row.Scan(
		&record.Name, &record.Path, &record.Version, &enabled, &record.Digest,
		&record.AddedAt, &record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	record.Enabled = enabled == 1
	return &record, nil
}

func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+recordColumns+` FROM plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, name string) (*Record, error) {
	record, err := scanRecord(s.DB.QueryRowContext(
		ctx, `SELECT `+recordColumns+` FROM plugins WHERE name = ?`, name,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}

func (s *Store) Upsert(ctx context.Context, record Record) error {
	enabled := 0
	if record.Enabled {
		enabled = 1
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO plugins (`+recordColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
		  path = excluded.path,
		  version = excluded.version,
		  enabled = excluded.enabled,
		  digest = excluded.digest,
		  updated_at = excluded.updated_at`,
		record.Name, record.Path, record.Version, enabled, record.Digest,
		record.AddedAt, record.UpdatedAt,
	)
	return err
}

func (s *Store) Remove(ctx context.Context, name string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM plugins WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

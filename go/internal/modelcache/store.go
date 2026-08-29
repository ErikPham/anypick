// Package modelcache stores cached vendor model lists. Advisory data only:
// a miss costs one HTTP request, never correctness, so a corrupt or absent
// row reads as "no cache". The store owns its SQL and takes no locks — the
// discovery service coordinates them (ADR 0009).
package modelcache

import (
	"context"
	"encoding/json"

	"github.com/outof0/anypick/go/internal/db"
)

// Entry mirrors CachedModelList.
type Entry struct {
	Provider  string
	Endpoint  string
	Models    []string
	FetchedAt string
}

// Store is the SQLite-backed cache keyed by provider AND endpoint (the same
// provider id can point at different hosts serving different lists).
type Store struct{ DB *db.DB }

// Get returns the cached list, or nil on miss/corruption.
func (s *Store) Get(ctx context.Context, provider, endpoint string) (*Entry, error) {
	var modelsJSON, fetchedAt string
	err := s.DB.QueryRowContext(ctx,
		`SELECT models_json, fetched_at FROM model_catalog_cache WHERE provider = ? AND endpoint = ?`,
		provider, endpoint).Scan(&modelsJSON, &fetchedAt)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, nil // advisory data: treat read failures as a miss
	}
	var models []string
	if json.Unmarshal([]byte(modelsJSON), &models) != nil {
		return nil, nil
	}
	return &Entry{Provider: provider, Endpoint: endpoint, Models: models, FetchedAt: fetchedAt}, nil
}

// Write upserts one cached list.
func (s *Store) Write(ctx context.Context, e *Entry) error {
	raw, err := json.Marshal(e.Models)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO model_catalog_cache (provider, endpoint, models_json, fetched_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(provider, endpoint) DO UPDATE SET
		   models_json = excluded.models_json,
		   fetched_at = excluded.fetched_at`,
		e.Provider, e.Endpoint, string(raw), e.FetchedAt)
	return err
}

// Clear drops cached lists, optionally scoped to one provider.
func (s *Store) Clear(ctx context.Context, provider string) error {
	if provider == "" {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM model_catalog_cache`)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM model_catalog_cache WHERE provider = ?`, provider)
	return err
}

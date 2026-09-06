// Package pools ports src/core/pool-store.ts: per-provider multi-account
// pools served through that provider's proxy.
package pools

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Member mirrors PoolMember.
type Member struct {
	Account string `json:"account"`
	Enabled bool   `json:"enabled"`
}

// Pool mirrors ProviderProxyPool.
type Pool struct {
	Provider  string   `json:"provider"`
	Mode      string   `json:"mode"` // 'single' | 'multi'
	Enabled   bool     `json:"enabled"`
	Strategy  string   `json:"strategy"` // 'failover' | 'round-robin'
	Host      string   `json:"host,omitempty"`
	Port      int      `json:"port,omitempty"`
	Members   []Member `json:"members"`
	UpdatedAt string   `json:"updatedAt"`
}

// Store owns the pools table.
type Store struct{ DB *sql.DB }

func (s *Store) Get(providerID string) (*Pool, error) {
	return s.GetContext(context.Background(), providerID)
}

func (s *Store) GetContext(ctx context.Context, providerID string) (*Pool, error) {
	var mode, strategy string
	var enabled bool
	var host sql.NullString
	var port sql.NullInt64
	var membersJSON string
	var updatedAt string
	err := s.DB.QueryRowContext(ctx,
		`SELECT mode, enabled, strategy, host, port, members_json, updated_at
		 FROM pools WHERE provider = ?`, providerID).
		Scan(&mode, &enabled, &strategy, &host, &port, &membersJSON, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pool := &Pool{
		Provider:  providerID,
		Mode:      mode,
		Enabled:   enabled,
		Strategy:  strategy,
		Host:      host.String,
		UpdatedAt: updatedAt,
	}
	if port.Valid {
		pool.Port = int(port.Int64)
	}
	if err := json.Unmarshal([]byte(membersJSON), &pool.Members); err != nil {
		pool.Members = []Member{}
	}
	return pool, nil
}

// SetMulti enables multi-account pooling for a provider.
func (s *Store) SetMultiContext(ctx context.Context, providerID string, members []Member, host string, port int) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if members == nil {
		members = []Member{}
	}
	membersJSON, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO pools (provider, mode, enabled, strategy, host, port, members_json, updated_at)
		 VALUES (?, 'multi', 1, 'failover', ?, ?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET
		   mode = 'multi', enabled = 1, strategy = 'failover',
		   host = excluded.host, port = excluded.port,
		   members_json = excluded.members_json,
		   updated_at = excluded.updated_at`,
		providerID, nullIfEmpty(host), nullIfZero(port), string(membersJSON), now)
	return err
}

// Set stores a full pool row (mode/strategy/enabled included). Used by the
// CLI so `enable` can persist non-default modes and strategies instead of
// silently dropping them.
func (s *Store) Set(ctx context.Context, pool Pool) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	members := pool.Members
	if members == nil {
		members = []Member{}
	}
	membersJSON, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO pools (provider, mode, enabled, strategy, host, port, members_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET
		   mode = excluded.mode, enabled = excluded.enabled,
		   strategy = excluded.strategy, host = excluded.host,
		   port = excluded.port, members_json = excluded.members_json,
		   updated_at = excluded.updated_at`,
		pool.Provider, nullIfEmpty(pool.Mode), boolInt(pool.Enabled), nullIfEmpty(pool.Strategy),
		nullIfEmpty(pool.Host), nullIfZero(pool.Port), string(membersJSON), now)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) DisableContext(ctx context.Context, providerID string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO pools (provider, mode, enabled, strategy, members_json, updated_at)
		 VALUES (?, 'single', 0, 'failover', '[]', ?)
		 ON CONFLICT(provider) DO UPDATE SET enabled = 0, updated_at = excluded.updated_at`,
		providerID, time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	return err
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// GetProxyConfig reads one account's proxy config; missing rows decode to the
// default (disabled).
func (s *AccountStore) GetProxyConfig(ctx context.Context, providerID, name string) (ProxyConfig, error) {
	var raw sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT proxy_json FROM accounts WHERE provider = ? AND name = ?`,
		providerID, name).Scan(&raw)
	if err == sql.ErrNoRows {
		return DefaultProxyConfig(), nil
	}
	if err != nil {
		return DefaultProxyConfig(), err
	}
	if !raw.Valid || raw.String == "" {
		return DefaultProxyConfig(), nil
	}
	out := DefaultProxyConfig()
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		// Corrupt config degrades to the safe default, never crashes a read.
		return DefaultProxyConfig(), nil
	}
	return out, nil
}

func (s *AccountStore) SetProxyConfigContext(ctx context.Context, providerID, name string, cfg ProxyConfig) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.setProxyConfigContext(ctx, providerID, name, cfg)
	})
}

func (s *AccountStore) setProxyConfigContext(ctx context.Context, providerID, name string, cfg ProxyConfig) error {
	account, err := s.Get(ctx, providerID, name)
	if err != nil {
		return err
	}
	if account == nil {
		return anyerr.Newf(`No account "`+name+`" for provider "`+providerID+`".`,
			"ACCOUNT_NOT_FOUND")
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE accounts SET proxy_json = ?, updated_at = ? WHERE provider = ? AND name = ?`,
		marshalJSON(cfg), nowISO(), providerID, name)
	return err
}

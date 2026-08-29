package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// GetProxyConfig mirrors AccountStore.getProxyConfig: missing rows decode to
// DEFAULT_PROXY_CONFIG.
func (s *AccountStore) GetProxyConfig(providerID, name string) (ProxyConfig, error) {
	return s.GetProxyConfigContext(context.Background(), providerID, name)
}

func (s *AccountStore) GetProxyConfigContext(ctx context.Context, providerID, name string) (ProxyConfig, error) {
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

// SetProxyConfig persists per-account proxy configuration. Requires the
// account to exist — enabling a proxy for a phantom account is a caller bug.
func (s *AccountStore) SetProxyConfig(providerID, name string, cfg ProxyConfig) error {
	return s.SetProxyConfigContext(context.Background(), providerID, name, cfg)
}

func (s *AccountStore) SetProxyConfigContext(ctx context.Context, providerID, name string, cfg ProxyConfig) error {
	account, err := s.GetContext(ctx, providerID, name)
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

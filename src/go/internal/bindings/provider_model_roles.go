package bindings

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/outof0/anypick/go/internal/refs"
)

// ModelRolesProviderKey maps a route source onto its provider-defaults
// bucket. Accounts key by provider; gateways and hubs key by their own
// namespaced identity so "Production" (a gateway) can never collide with a
// provider, and two hubs never share defaults.
func ModelRolesProviderKey(source refs.Ref) string {
	switch source.Kind {
	case refs.KindAccount, refs.KindAccountPool:
		return source.Provider
	case refs.KindGateway:
		return "gateway/" + source.Name
	case refs.KindProxyHub:
		return "proxy-hub/" + source.Name
	default:
		return ""
	}
}

// GetProviderModelRoles reads the stored defaults for one provider bucket.
// Empty (nil map, nil error) means no defaults: callers fall back to the
// engine default. Like every other method on Store it is ctx-first; there
// is no ctx-less form (see UpsertGlobalContext).
func (s *Store) GetProviderModelRoles(ctx context.Context, providerID string) (map[string]string, error) {
	out := map[string]string{}
	if providerID == "" {
		return out, nil
	}
	var raw string
	err := s.DB.QueryRowContext(ctx,
		`SELECT roles_json FROM provider_model_roles WHERE provider_id = ?`, providerID).Scan(&raw)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	decoded := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	for role, model := range decoded {
		if role != "" && model != "" {
			out[role] = model
		}
	}
	return out, nil
}

// SetProviderModelRoles replaces the defaults for one provider bucket. An
// empty map deletes the row: "no defaults" and "empty defaults" are the same
// state, and a leftover empty row would read as a configured default. Defaults
// apply only to connections carrying no explicit pin, so an explicit
// per-connection choice always wins.
func (s *Store) SetProviderModelRoles(ctx context.Context, providerID string, roles map[string]string) error {
	if providerID == "" {
		return nil
	}
	return s.mutate(ctx, refs.ProviderScope(providerID), func(c context.Context) error {
		clean := map[string]string{}
		for role, model := range roles {
			if role != "" && model != "" {
				clean[role] = model
			}
		}
		if len(clean) == 0 {
			_, err := s.DB.ExecContext(c,
				`DELETE FROM provider_model_roles WHERE provider_id = ?`, providerID)
			return err
		}
		encoded, err := json.Marshal(clean)
		if err != nil {
			return err
		}
		now := nowISO()
		_, err = s.DB.ExecContext(c,
			`INSERT INTO provider_model_roles (provider_id, roles_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(provider_id) DO UPDATE SET
			   roles_json = excluded.roles_json,
			   updated_at = excluded.updated_at`,
			providerID, string(encoded), now, now)
		return err
	})
}

package pools

import "context"

// Service is the boundary outer layers talk to for pool state. It exists for
// the same reason hub.Service, accounts.Service, proxy.Service, bindings.Service
// and presets.Service do: the table belongs to internal/pools, and a CLI, TUI
// or desktop caller should ask for a pool rather than hold a handle on the
// table-owning store.
//
// The methods are deliberately ctx-first and carry no ctx-less form, matching
// the rest of the engine.
type Service struct {
	Store *Store
}

func NewService(s *Store) *Service { return &Service{Store: s} }

// GetContext returns the pool for a provider, nil when none is configured.
func (s *Service) GetContext(ctx context.Context, providerID string) (*Pool, error) {
	return s.Store.GetContext(ctx, providerID)
}

// Set persists a full pool row (mode/strategy/enabled included).
func (s *Service) Set(ctx context.Context, pool Pool) error {
	return s.Store.Set(ctx, pool)
}

// DisableContext drops a provider back to single-account mode.
func (s *Service) DisableContext(ctx context.Context, providerID string) error {
	return s.Store.DisableContext(ctx, providerID)
}

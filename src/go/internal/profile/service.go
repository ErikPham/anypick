package profile

import "context"

// Service is the boundary outer layers talk to for gateway runtime profiles.
// It exists for the same reason hub.Service, accounts.Service, proxy.Service,
// bindings.Service, presets.Service and pools.Service do: the table belongs to
// internal/profile, and a CLI, TUI or desktop caller should ask for a profile
// rather than hold a handle on the table-owning store.
//
// The methods are deliberately ctx-first and carry no ctx-less form, matching
// the rest of the engine.
type Service struct {
	Store *Store
}

func NewService(s *Store) *Service { return &Service{Store: s} }

func (s *Service) ListContext(ctx context.Context) ([]string, error) {
	return s.Store.ListContext(ctx)
}

// GetContext returns the named profile, nil when none.
func (s *Service) GetContext(ctx context.Context, name string) (*Runtime, error) {
	return s.Store.GetContext(ctx, name)
}

// UpsertContext creates or bumps the revision of a named profile.
func (s *Service) UpsertContext(ctx context.Context, r Runtime) (Runtime, error) {
	return s.Store.UpsertContext(ctx, r)
}

// DeleteContext removes the named profile.
func (s *Service) DeleteContext(ctx context.Context, name string) error {
	return s.Store.DeleteContext(ctx, name)
}

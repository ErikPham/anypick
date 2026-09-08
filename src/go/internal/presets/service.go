package presets

import (
	"context"

	"github.com/outof0/anypick/go/internal/bindings"
)

// Service is the boundary outer layers talk to for presets state. It exists
// for the same reason hub.Service, accounts.Service, proxy.Service and
// bindings.Service do: the table belongs to internal/presets, and a CLI, TUI
// or desktop caller should ask for a preset rather than hold a handle on the
// table-owning store.
//
// The methods are deliberately ctx-first and carry no ctx-less form, matching
// the rest of the engine.
type Service struct {
	Store *Store
}

func NewService(s *Store) *Service { return &Service{Store: s} }

// ListContext returns every saved preset ordered by name.
func (s *Service) ListContext(ctx context.Context) ([]Saved, error) {
	return s.Store.ListContext(ctx)
}

// GetByNameContext returns the named preset, nil when none.
func (s *Service) GetByNameContext(ctx context.Context, name string) (*Saved, error) {
	return s.Store.GetByNameContext(ctx, name)
}

// ExistsContext reports whether a preset with the given name is saved.
func (s *Service) ExistsContext(ctx context.Context, name string) (bool, error) {
	return s.Store.ExistsContext(ctx, name)
}

// UpsertContext creates or bumps the revision of a named preset.
func (s *Service) UpsertContext(ctx context.Context, name string, spec bindings.Spec) (Saved, error) {
	return s.Store.UpsertContext(ctx, name, spec)
}

// MarkUsedContext records a successful activation of a preset.
func (s *Service) MarkUsedContext(ctx context.Context, id string) error {
	return s.Store.MarkUsedContext(ctx, id)
}

// DeleteByNameContext removes the named preset.
func (s *Service) DeleteByNameContext(ctx context.Context, name string) error {
	return s.Store.DeleteByNameContext(ctx, name)
}

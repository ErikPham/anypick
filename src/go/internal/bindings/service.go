package bindings

import (
	"context"

	"github.com/outof0/anypick/go/internal/refs"
)

// Service is the boundary outer layers talk to for bindings state. It exists
// for the same reason hub.Service, accounts.Service and proxy.Service do: the
// tables belong to internal/bindings, and a CLI, TUI or desktop caller should
// ask for a binding rather than hold a handle on the table-owning store.
//
// The methods are deliberately ctx-first and carry no ctx-less form, matching
// the rest of the engine — every mutator routes through Store.mutate, which
// takes the coordinator lock (ADR 0009).
type Service struct {
	Store *Store
}

func NewService(s *Store) *Service { return &Service{Store: s} }

// GetGlobalContext returns the global binding for a client, nil when none.
func (s *Service) GetGlobalContext(ctx context.Context, clientID string) (*Global, error) {
	return s.Store.GetGlobalContext(ctx, clientID)
}

// UpsertGlobalContext inserts or replaces the global binding for a client.
func (s *Service) UpsertGlobalContext(ctx context.Context, g Global) (Global, error) {
	return s.Store.UpsertGlobalContext(ctx, g)
}

// GetProjectContext returns one project binding, nil when none.
func (s *Service) GetProjectContext(ctx context.Context, root, clientID string) (*Project, error) {
	return s.Store.GetProjectContext(ctx, root, clientID)
}

// UpsertProjectContext inserts or replaces one project binding.
func (s *Service) UpsertProjectContext(ctx context.Context, p Project) (Project, error) {
	return s.Store.UpsertProjectContext(ctx, p)
}

// DeleteGlobalContext removes the global binding for a client.
func (s *Service) DeleteGlobalContext(ctx context.Context, clientID string) error {
	return s.Store.DeleteGlobalContext(ctx, clientID)
}

// DeleteProjectContext removes one project binding.
func (s *Service) DeleteProjectContext(ctx context.Context, root, clientID string) error {
	return s.Store.DeleteProjectContext(ctx, root, clientID)
}

// CountProjectsContext reports how many project bindings exist under a root.
func (s *Service) CountProjectsContext(ctx context.Context, root string) (int64, error) {
	return s.Store.CountProjectsContext(ctx, root)
}

// DeleteAllProjectsContext removes every project binding under a root and
// reports how many were removed.
func (s *Service) DeleteAllProjectsContext(ctx context.Context, root string) (int64, error) {
	return s.Store.DeleteAllProjectsContext(ctx, root)
}

// RecordSourceResumeContext caches a successful, source-specific setup so
// clients can resume recent compatible setups.
func (s *Service) RecordSourceResumeContext(ctx context.Context, spec Spec) error {
	return s.Store.RecordSourceResumeContext(ctx, spec)
}

// ListSourceResumesContext returns the most recently successful compatible
// setups for a concrete source (newest first).
func (s *Service) ListSourceResumesContext(ctx context.Context, source refs.Ref) ([]SourceResume, error) {
	return s.Store.ListSourceResumesContext(ctx, source)
}

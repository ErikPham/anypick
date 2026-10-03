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

// GetProviderModelRoles returns the stored model defaults for one provider
// bucket, empty when none were saved.
func (s *Service) GetProviderModelRoles(ctx context.Context, providerID string) (map[string]string, error) {
	return s.Store.GetProviderModelRoles(ctx, providerID)
}

// SetProviderModelRoles replaces the model defaults for one provider bucket.
// An empty map deletes the row.
func (s *Service) SetProviderModelRoles(ctx context.Context, providerID string, roles map[string]string) error {
	return s.Store.SetProviderModelRoles(ctx, providerID, roles)
}

func (s *Service) UpsertGlobalContext(ctx context.Context, g Global) (Global, error) {
	return s.Store.UpsertGlobalContext(ctx, g)
}

// RealignNativeClientBinding mirrors the account-service post-switch hook in
// the TypeScript runtime. A provider account switch changes the live account
// pointer, but a homonymous native client can have a separate global binding
// that still names the old account. Only that exact provider/client binding is
// rewritten; explicit cross-provider routes remain pinned by design.
func (s *Service) RealignNativeClientBinding(ctx context.Context, providerID, accountName string) error {
	binding, err := s.GetGlobalContext(ctx, providerID)
	if err != nil || binding == nil {
		return err
	}
	source := binding.Spec.Source
	if source.Kind != refs.KindAccount || source.Provider != providerID || source.Name == accountName {
		return nil
	}
	binding.Spec.Source.Name = accountName
	_, err = s.UpsertGlobalContext(ctx, *binding)
	return err
}

func (s *Service) GetProjectContext(ctx context.Context, root, clientID string) (*Project, error) {
	return s.Store.GetProjectContext(ctx, root, clientID)
}

func (s *Service) UpsertProjectContext(ctx context.Context, p Project) (Project, error) {
	return s.Store.UpsertProjectContext(ctx, p)
}

func (s *Service) DeleteGlobalContext(ctx context.Context, clientID string) error {
	return s.Store.DeleteGlobalContext(ctx, clientID)
}

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

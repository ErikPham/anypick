package hub

import "context"

// Service is the boundary outer layers talk to for Proxy Hub state. It exists
// for the same reason accounts.Service and proxy.Service do: the tables belong
// to internal/hub, and a CLI or desktop caller should ask for a hub rather than
// hold a handle on the table-owning store.
//
// The methods are deliberately ctx-first and carry no ctx-less form, matching
// the rest of the engine — GetConfig in particular replaces the store's
// ctx-less GetConfig, which was the last one of those left reachable from the
// CLI.
type Service struct {
	Store *Store
}

func NewService(s *Store) *Service { return &Service{Store: s} }

// GetConfig returns one saved hub config, nil when there is none.
func (s *Service) GetConfig(ctx context.Context, name string) (*Config, error) {
	return s.Store.GetConfigContext(ctx, name)
}

// UpsertConfig creates or replaces a hub config and returns what was stored.
func (s *Service) UpsertConfig(ctx context.Context, cfg Config) (Config, error) {
	return s.Store.UpsertConfigContext(ctx, cfg)
}

// SetModelOwner pins one published model to one source owner.
func (s *Service) SetModelOwner(ctx context.Context, hubName, model, owner string) error {
	return s.Store.SetModelOwnerContext(ctx, hubName, model, owner)
}

// PreviewManifest renders the manifest a client would fetch, without serving it.
func (s *Service) PreviewManifest(ctx context.Context, hubName string) (*ManifestPreview, error) {
	return s.Store.PreviewManifest(ctx, hubName)
}

// ListConfigsContext returns every saved hub config.
func (s *Service) ListConfigsContext(ctx context.Context) ([]Config, error) {
	return s.Store.ListConfigsContext(ctx)
}

// RouteModelsContext returns the published models a hub routes for hubName.
func (s *Service) RouteModelsContext(ctx context.Context, hubName string) ([]ModelRoute, error) {
	return s.Store.RouteModelsContext(ctx, hubName)
}

// GetConfigContext returns one saved hub config, nil when there is none.
func (s *Service) GetConfigContext(ctx context.Context, name string) (*Config, error) {
	return s.Store.GetConfigContext(ctx, name)
}

// UpsertConfigContext creates or replaces a hub config and returns what was stored.
func (s *Service) UpsertConfigContext(ctx context.Context, cfg Config) (Config, error) {
	return s.Store.UpsertConfigContext(ctx, cfg)
}

// AttachRouteContext pins one published route to a source owner.
func (s *Service) AttachRouteContext(ctx context.Context, r Route) error {
	return s.Store.AttachRouteContext(ctx, r)
}

// RouteByToken resolves a serving token to its route (ctx-less: the hub server
// resolves before it has a request context).
func (s *Service) RouteByToken(token string) (*Route, error) {
	return s.Store.RouteByToken(token)
}

// MigrateRouteTokenContext re-binds a route's serving token.
func (s *Service) MigrateRouteTokenContext(ctx context.Context, routeID, token string) error {
	return s.Store.MigrateRouteTokenContext(ctx, routeID, token)
}

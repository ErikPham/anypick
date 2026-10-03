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

func (s *Service) UpsertConfig(ctx context.Context, cfg Config) (Config, error) {
	return s.Store.UpsertConfigContext(ctx, cfg)
}

func (s *Service) SetModelOwner(ctx context.Context, hubName, model, owner string) error {
	return s.Store.SetModelOwnerContext(ctx, hubName, model, owner)
}

// RemoveSource detaches one source and repairs positional pins (see
// Store.RemoveSourceContext). It returns whether anything was removed and
// which model pins were dropped.
func (s *Service) RemoveSource(ctx context.Context, hubName, providerID, account string) (bool, []string, error) {
	return s.Store.RemoveSourceContext(ctx, hubName, providerID, account)
}

// PruneAccount removes every hub reference to one deleted account across all
// hubs. Callers run it after the account row is gone; it only needs the
// provider/name pair, never the snapshot.
func (s *Service) PruneAccount(ctx context.Context, providerID, account string) ([]string, error) {
	return s.Store.PruneAccountContext(ctx, providerID, account)
}

func (s *Service) PreviewManifest(ctx context.Context, hubName string) (*ManifestPreview, error) {
	return s.Store.PreviewManifest(ctx, hubName)
}

func (s *Service) ListConfigsContext(ctx context.Context) ([]Config, error) {
	return s.Store.ListConfigsContext(ctx)
}

func (s *Service) RouteModelsContext(ctx context.Context, hubName string) ([]ModelRoute, error) {
	return s.Store.RouteModelsContext(ctx, hubName)
}

func (s *Service) GetConfigContext(ctx context.Context, name string) (*Config, error) {
	return s.Store.GetConfigContext(ctx, name)
}

func (s *Service) UpsertConfigContext(ctx context.Context, cfg Config) (Config, error) {
	return s.Store.UpsertConfigContext(ctx, cfg)
}

func (s *Service) AttachRouteContext(ctx context.Context, r Route) error {
	return s.Store.AttachRouteContext(ctx, r)
}

func (s *Service) DetachRouteContext(ctx context.Context, routeID string) error {
	return s.Store.DetachRouteContext(ctx, routeID)
}

// RouteByToken resolves a serving token to its route (ctx-less: the hub server
// resolves before it has a request context).
func (s *Service) RouteByToken(token string) (*Route, error) {
	return s.Store.RouteByToken(token)
}

func (s *Service) MigrateRouteTokenContext(ctx context.Context, routeID, token string) error {
	return s.Store.MigrateRouteTokenContext(ctx, routeID, token)
}

func (s *Service) EnsureHubTokenContext(ctx context.Context, hubName string) (string, error) {
	return s.Store.EnsureHubTokenContext(ctx, hubName)
}

func (s *Service) RotateHubTokenContext(ctx context.Context, hubName string) (string, error) {
	return s.Store.RotateHubTokenContext(ctx, hubName)
}

func (s *Service) SnapshotRouteContext(ctx context.Context, routeID string) (*RouteRowSnapshot, error) {
	return s.Store.SnapshotRouteContext(ctx, routeID)
}

func (s *Service) RestoreRouteRowContext(ctx context.Context, snap *RouteRowSnapshot) error {
	return s.Store.RestoreRouteRowContext(ctx, snap)
}

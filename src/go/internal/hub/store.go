// Package hub ports the Proxy Hub essence (src/core/proxy-hub-*): one local
// endpoint that serves many sources, with token-scoped routes and model-based
// source selection.
//
// Security: loopback-only bind, per-route secrets (constant-time compare,
// fail-closed), tokens never appear in status output.
package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
)

// Source names one upstream account. A TS-authored row persists sources as
// {ref:{kind:'account'|'account-pool', provider, name?}, enabled}; Go keeps
// the flat essential form and maps on the store boundary (decodeHubConfig).
// Account "pool:<provider>" is the serialization of an account-pool ref.
type Source struct {
	ProviderID string `json:"providerId"`
	Account    string `json:"account"`
	// Enabled mirrors ProxyHubSource.enabled (src/types/proxy-hub.ts). The TS
	// codec reads absent as enabled, so nil — the zero value — must mean on;
	// a plain bool would flip every existing constructor to disabled.
	Enabled *bool `json:"-"`
}

// IsEnabled reports whether the source participates in routing.
func (s Source) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

const defaultHubHost = "127.0.0.1"

// Config mirrors the essential fields of ProxyHubConfig.
type Config struct {
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	Host      string   `json:"host,omitempty"` // default 127.0.0.1
	Port      int      `json:"port,omitempty"` // 0 → kernel-assigned
	Sources   []Source `json:"sources"`
	Revision  int      `json:"revision"` // positive integer per the TS decoder
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	// modelOwners carries the TS-only field verbatim across read-modify-write
	// cycles so a Go save never drops TS data it does not model.
	modelOwners json.RawMessage
}

// ModelRoute pins a model id to a source (ProxyHubRouteTarget essence).
type ModelRoute struct {
	Model         string `json:"model"`
	SourceIndex   int    `json:"sourceIndex"`
	UpstreamModel string `json:"upstreamModel,omitempty"`
}

// Route is one client-facing attachment: its secret authenticates requests;
// its manifest routes requested models onto sources.
type Route struct {
	RouteID  string       `json:"routeId"`
	HubName  string       `json:"hubName"`
	ClientID string       `json:"clientId"`
	Protocol string       `json:"protocol,omitempty"`
	Models   []ModelRoute `json:"models,omitempty"`
	Token    string       `json:"-"` // never serialized to output boundaries
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Store owns the proxy_hubs / proxy_hub_routes tables. token_secret is
// owner-only material and is never decoded into status types.
//
// Root is the AnyPick data directory: mutating methods take the coordinator
// lock under it themselves (invariant #5), so callers must not depend on an
// ambient lock. A Store without Root cannot take the lock and its mutating
// methods fail closed instead of writing uncoordinated.
type Store struct {
	DB   *sql.DB
	Root string
}

// UpsertConfig inserts or updates the hub configuration row.
func (s *Store) UpsertConfigContext(ctx context.Context, cfg Config) (Config, error) {
	now := nowISO()
	existing, err := s.GetConfigContext(ctx, cfg.Name)
	if err != nil {
		return Config{}, err
	}
	created := now
	if existing != nil {
		created = existing.CreatedAt
	}
	cfg.CreatedAt = created
	cfg.UpdatedAt = now
	cfg.Revision = 1
	owners := cfg.modelOwners
	if existing != nil {
		created = existing.CreatedAt
		cfg.CreatedAt = created
		// Mirror ProxyHubStore.save(): every write bumps revision.
		if existing.Revision > 0 {
			cfg.Revision = existing.Revision + 1
		}
		if owners == nil {
			owners = existing.modelOwners
		}
	}
	data, err := marshalTSConfig(cfg, owners)
	if err != nil {
		return Config{}, err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   config_json = excluded.config_json,
		   updated_at = excluded.updated_at`,
		cfg.Name, string(data), created, now)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (s *Store) GetConfig(name string) (*Config, error) {
	return s.GetConfigContext(context.Background(), name)
}

// ListConfigsContext returns every hub configuration by name. A corrupt row
// fails the whole list: like decodeHubConfig, a half-loaded hub set would
// serve requests against the wrong sources.
func (s *Store) ListConfigsContext(ctx context.Context) ([]Config, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT name, config_json FROM proxy_hubs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Config{}
	for rows.Next() {
		var name, data string
		if err := rows.Scan(&name, &data); err != nil {
			return nil, err
		}
		cfg, err := decodeHubConfig([]byte(data), name)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

// RouteModelsContext returns the pinned models of a hub's default route
// (model → owning source index). Nil route (never attached) yields no pins,
// not an error: an unconfigured hub simply publishes nothing yet.
func (s *Store) RouteModelsContext(ctx context.Context, hubName string) ([]ModelRoute, error) {
	route, err := s.routeByID(ctx, DefaultRouteID(hubName))
	if err != nil {
		return nil, err
	}
	if route == nil {
		return nil, nil
	}
	return route.Models, nil
}

func (s *Store) GetConfigContext(ctx context.Context, name string) (*Config, error) {
	var data string
	err := s.DB.QueryRowContext(ctx,
		`SELECT config_json FROM proxy_hubs WHERE name = ?`, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeHubConfig([]byte(data), name)
}

// ---- cross-runtime config_json boundary ----

// wireRef / wireSource mirror src/core/codec/proxy-hub.ts exactly. wireSource
// also accepts the legacy Go-written flat shape so pre-cutover rows still
// load; anything else fails closed instead of zero-decoding into an empty
// config that would 502 every request.
type wireRef struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type wireSource struct {
	Ref     *wireRef `json:"ref"`
	Enabled *bool    `json:"enabled"`
	// Legacy Go-only shape (pre-cutover rows written by this package).
	ProviderID string `json:"providerId"`
	Account    string `json:"account"`
}

func (w wireSource) toSource(where string) (Source, error) {
	if w.Ref != nil {
		switch w.Ref.Kind {
		case "account":
			if w.Ref.Provider == "" || w.Ref.Name == "" {
				return Source{}, anyerr.Newf(where+": account ref needs provider and name", "STATE_CONFLICT")
			}
			return Source{ProviderID: w.Ref.Provider, Account: w.Ref.Name, Enabled: w.Enabled}, nil
		case "account-pool":
			if w.Ref.Provider == "" {
				return Source{}, anyerr.Newf(where+": account-pool ref needs provider", "STATE_CONFLICT")
			}
			return Source{ProviderID: w.Ref.Provider, Account: "pool:" + w.Ref.Provider, Enabled: w.Enabled}, nil
		default:
			return Source{}, anyerr.Newf(where+": unsupported ref kind \""+w.Ref.Kind+"\"", "STATE_CONFLICT")
		}
	}
	if w.ProviderID != "" && w.Account != "" {
		return Source{ProviderID: w.ProviderID, Account: w.Account, Enabled: w.Enabled}, nil
	}
	return Source{}, anyerr.Newf(where+": unrecognized source shape", "STATE_CONFLICT")
}

// decodeHubConfig parses one proxy_hubs.config_json row. Unparseable or
// wrong-shaped rows return STATE_CONFLICT — never a silent empty config.
func decodeHubConfig(data []byte, name string) (*Config, error) {
	fail := func(why string) (*Config, error) {
		return nil, anyerr.Newf("corrupt hub config for "+name+": "+why, "STATE_CONFLICT")
	}
	var probe interface{}
	if err := json.Unmarshal(data, &probe); err != nil || probe == nil {
		return fail("not JSON")
	}
	if _, ok := probe.(map[string]interface{}); !ok {
		return fail("expected object")
	}
	var w struct {
		Name        string          `json:"name"`
		Enabled     bool            `json:"enabled"`
		Host        string          `json:"host"`
		Port        int             `json:"port"`
		Sources     []wireSource    `json:"sources"`
		ModelOwners json.RawMessage `json:"modelOwners"`
		Revision    int             `json:"revision"`
		CreatedAt   string          `json:"createdAt"`
		UpdatedAt   string          `json:"updatedAt"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return fail(err.Error())
	}
	cfg := &Config{
		Name: w.Name, Enabled: w.Enabled, Host: w.Host, Port: w.Port,
		Sources: make([]Source, 0, len(w.Sources)), Revision: w.Revision,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	if len(w.ModelOwners) > 0 {
		cfg.modelOwners = json.RawMessage(string(w.ModelOwners))
	}
	for i, ws := range w.Sources {
		src, err := ws.toSource(fmt.Sprintf("sources[%d] of %s", i, name))
		if err != nil {
			return nil, err
		}
		cfg.Sources = append(cfg.Sources, src)
	}
	return cfg, nil
}

// tsSource/tsHubConfig are the write-side twins of the TS decoder contract:
// every key decodeProxyHubConfig requires is present, so TypeScript reading a
// Go-written row decodes it natively (round-trip without protocol drift).
//
// Known one-field gap: a kernel-assigned port (0) is persisted as absent,
// which the TS decoder rejects — deliberately, because inventing a concrete
// port here collides with the managed builtin proxy's default listener. A
// TS reader treats such a row as unconfigured (fail closed); once a hub is
// given an explicit port the row is fully TS-readable.
type tsSource struct {
	Ref     wireRef `json:"ref"`
	Enabled bool    `json:"enabled"`
}

type tsHubConfig struct {
	Name        string          `json:"name"`
	Enabled     bool            `json:"enabled"`
	Host        string          `json:"host"`
	Port        int             `json:"port,omitempty"` // absent = kernel-assigned (gap note above)
	Sources     []tsSource      `json:"sources"`
	ModelOwners json.RawMessage `json:"modelOwners"`
	Revision    int             `json:"revision"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
}

func marshalTSConfig(cfg Config, modelOwners json.RawMessage) ([]byte, error) {
	host := cfg.Host
	if host == "" {
		host = defaultHubHost
	}
	owners := modelOwners
	if len(owners) == 0 {
		owners = json.RawMessage("[]")
	}
	wire := tsHubConfig{
		Name: cfg.Name, Enabled: cfg.Enabled,
		Host: host, Port: cfg.Port,
		Sources:     make([]tsSource, 0, len(cfg.Sources)),
		ModelOwners: owners, Revision: cfg.Revision,
		CreatedAt: cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}
	for _, s := range cfg.Sources {
		ref := wireRef{Kind: "account", Provider: s.ProviderID, Name: s.Account}
		if pool, ok := poolSourceProvider(s); ok {
			ref = wireRef{Kind: "account-pool", Provider: pool}
		}
		wire.Sources = append(wire.Sources, tsSource{Ref: ref, Enabled: s.IsEnabled()})
	}
	return json.Marshal(wire)
}

// AttachRoute stores a route with its generated token secret. The secret is
// stored as HMAC-SHA256 hex derived from a per-install key so a DB dump does
// not expose bearer tokens (P1). The key lives in config.hub_token_hmac_key
// and is generated lazily on first use; deleting that row rotates the key
// and invalidates all existing HMAC tokens (re-attach required).
func (s *Store) AttachRouteContext(ctx context.Context, r Route) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	hashed, err := s.hashToken(ctx, r.Token)
	if err != nil {
		return err
	}
	now := nowISO()
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO proxy_hub_routes
		   (route_id, hub_name, manifest_json, token_secret, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(route_id) DO UPDATE SET
		   manifest_json = excluded.manifest_json,
		   token_secret = excluded.token_secret,
		   updated_at = excluded.updated_at`,
		r.RouteID, r.HubName, string(data), hashed, now, now)
	return err
}

const hubHMACKeyName = "hub_token_hmac_key"

func (s *Store) hmacKeyExisting(ctx context.Context) ([]byte, bool, error) {
	var hexKey string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, hubHMACKeyName).Scan(&hexKey)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b, derr := hex.DecodeString(hexKey)
	if derr != nil {
		return nil, false, anyerr.Newf("corrupt hub hmac key", "STATE_CONFLICT")
	}
	return b, true, nil
}

func (s *Store) ensureHMACKey(ctx context.Context) ([]byte, error) {
	if b, ok, err := s.hmacKeyExisting(ctx); err != nil {
		return nil, err
	} else if ok {
		return b, nil
	}
	raw := make([]byte, 32)
	if _, rerr := rand.Read(raw); rerr != nil {
		return nil, rerr
	}
	hexKey := hex.EncodeToString(raw)
	if _, ierr := s.DB.ExecContext(ctx,
		`INSERT INTO config (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		hubHMACKeyName, hexKey); ierr != nil {
		return nil, ierr
	}
	// Re-read the winner under race.
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, hubHMACKeyName).Scan(&hexKey); err != nil {
		return nil, err
	}
	b, derr := hex.DecodeString(hexKey)
	if derr != nil {
		return nil, anyerr.Newf("corrupt hub hmac key", "STATE_CONFLICT")
	}
	return b, nil
}

func (s *Store) hashToken(ctx context.Context, token string) (string, error) {
	key, err := s.ensureHMACKey(ctx)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write([]byte(token)); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func legacySHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DetachRoute removes an attached route.
// defaultRouteID is the CLI-facing attachment used by `proxy hub owner`.
const defaultRouteIDSuffix = "/default"

// DefaultRouteID returns the implicit route id for a hub name.
func DefaultRouteID(hubName string) string { return hubName + defaultRouteIDSuffix }

// routeByRow mirrors RouteByTokenContext but keyed by id.
// RouteRowSnapshot captures one persisted route row verbatim (manifest,
// token hash, timestamps) so activation compensation can restore EXACTLY what
// existed before an AttachRoute overwrite.
type RouteRowSnapshot struct {
	RouteID      string
	ManifestJSON string
	TokenSecret  string
	CreatedAt    string
	UpdatedAt    string
}

// SnapshotRouteContext reads the raw stored row for routeID; returns nil when
// the route does not exist.
func (s *Store) SnapshotRouteContext(ctx context.Context, routeID string) (*RouteRowSnapshot, error) {
	var snap RouteRowSnapshot
	err := s.DB.QueryRowContext(ctx,
		`SELECT route_id, manifest_json, token_secret, created_at, updated_at
		   FROM proxy_hub_routes WHERE route_id = ?`, routeID).
		Scan(&snap.RouteID, &snap.ManifestJSON, &snap.TokenSecret, &snap.CreatedAt, &snap.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// RestoreRouteRowContext reinstates a snapshotted row byte-for-byte.
func (s *Store) RestoreRouteRowContext(ctx context.Context, snap *RouteRowSnapshot) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO proxy_hub_routes
		   (route_id, hub_name, manifest_json, token_secret, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(route_id) DO UPDATE SET
		   hub_name = excluded.hub_name,
		   manifest_json = excluded.manifest_json,
		   token_secret = excluded.token_secret,
		   created_at = excluded.created_at,
		   updated_at = excluded.updated_at`,
		snap.RouteID, routeHubName(snap.ManifestJSON), snap.ManifestJSON, snap.TokenSecret,
		snap.CreatedAt, snap.UpdatedAt)
	return err
}

// routeHubName extracts hub_name from a stored route manifest.
func routeHubName(manifestJSON string) string {
	var r Route
	if json.Unmarshal([]byte(manifestJSON), &r) != nil {
		return ""
	}
	return r.HubName
}

func (s *Store) routeByID(ctx context.Context, routeID string) (*Route, error) {
	var manifest string
	err := s.DB.QueryRowContext(ctx,
		`SELECT manifest_json FROM proxy_hub_routes WHERE route_id = ?`, routeID).Scan(&manifest)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Route
	if json.Unmarshal([]byte(manifest), &r) != nil {
		return nil, anyerr.Newf("corrupt hub route "+routeID, "STATE_CONFLICT")
	}
	return &r, nil
}

// SetModelOwnerContext pins a model id to the source whose provider/account
// matches owner on the hub's default route (explicit model owner).
func (s *Store) SetModelOwnerContext(ctx context.Context, hubName, model, owner string) error {
	cfg, err := s.GetConfigContext(ctx, hubName)
	if err != nil {
		return err
	}
	if cfg == nil {
		return anyerr.Newf("Proxy Hub not found: "+hubName, "HUB_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	ownerIndex := -1
	for i, src := range cfg.Sources {
		if src.ProviderID+"/"+src.Account == owner || src.Account == owner || src.ProviderID == owner {
			ownerIndex = i
			break
		}
	}
	if ownerIndex < 0 {
		return anyerr.Newf("source not attached to this hub: "+owner, "HUB_SOURCE_MISSING")
	}
	routeID := DefaultRouteID(hubName)
	route, err := s.routeByID(ctx, routeID)
	if err != nil {
		return err
	}
	if route == nil {
		route = &Route{RouteID: routeID, HubName: hubName, ClientID: "cli"}
	}
	replaced := false
	for i := range route.Models {
		if route.Models[i].Model == model {
			route.Models[i].SourceIndex = ownerIndex
			replaced = true
		}
	}
	if !replaced {
		route.Models = append(route.Models, ModelRoute{Model: model, SourceIndex: ownerIndex})
	}
	return s.AttachRouteContext(ctx, *route)
}

// PreviewManifest returns the deterministic route lines a client would see:
// one line per source plus explicit model-owner pins.
func (s *Store) PreviewManifest(ctx context.Context, hubName string) (*ManifestPreview, error) {
	cfg, err := s.GetConfigContext(ctx, hubName)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, anyerr.Newf("Proxy Hub not found: "+hubName, "HUB_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	out := &ManifestPreview{Name: hubName}
	for _, src := range cfg.Sources {
		out.Lines = append(out.Lines,
			fmt.Sprintf("source %d: %s/%s", out.SourceCount, src.ProviderID, src.Account))
		out.SourceCount++
	}
	route, err := s.routeByID(ctx, DefaultRouteID(hubName))
	if err != nil {
		return nil, err
	}
	if route != nil {
		for _, m := range route.Models {
			target := "default"
			if m.SourceIndex >= 0 && m.SourceIndex < len(cfg.Sources) {
				s := cfg.Sources[m.SourceIndex]
				target = s.ProviderID + "/" + s.Account
			}
			line := fmt.Sprintf("model %s → %s", m.Model, target)
			if m.UpstreamModel != "" && m.UpstreamModel != m.Model {
				line += fmt.Sprintf(" (%s)", m.UpstreamModel)
			}
			out.Lines = append(out.Lines, line)
		}
	}
	return out, nil
}

// ManifestPreview is what `proxy hub preview` prints; never includes secrets.
type ManifestPreview struct {
	Name        string   `json:"name"`
	SourceCount int      `json:"-"`
	Lines       []string `json:"lines"`
}

func (s *Store) DetachRouteContext(ctx context.Context, routeID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM proxy_hub_routes WHERE route_id = ?`, routeID)
	return err
}

// RouteByToken resolves the route owning a presented credential. Token secrets
// are stored as HMAC-SHA256 hex (AttachRoute hashes them with a per-install
// key); lookup tries HMAC, then SHA-256, then plaintext for legacy rows. The
// returned Route echoes the presented token on match so callers can do
// constant-time comparison without exposing the stored hash.
//
// This is a pure read: it never migrates rows and never provisions the HMAC
// key. Legacy-row migration is an explicit write via MigrateRouteTokenContext,
// invoked by the request path only after authentication has succeeded.
func (s *Store) RouteByToken(token string) (*Route, error) {
	return s.RouteByTokenContext(context.Background(), token)
}

func (s *Store) RouteByTokenContext(ctx context.Context, token string) (*Route, error) {
	// HMAC path — only if a key has been provisioned (RouteByToken must not
	// auto-create the key; AttachRoute is the writer that provisions it).
	if key, ok, err := s.hmacKeyExisting(ctx); err != nil {
		return nil, err
	} else if ok {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(token))
		hashed := hex.EncodeToString(mac.Sum(nil))
		var data, secret string
		err := s.DB.QueryRowContext(ctx,
			`SELECT manifest_json, token_secret FROM proxy_hub_routes WHERE token_secret = ?`, hashed).
			Scan(&data, &secret)
		if err == nil {
			var r Route
			if json.Unmarshal([]byte(data), &r) != nil {
				return nil, anyerr.Newf("corrupt route manifest", "STATE_CONFLICT")
			}
			r.Token = token
			return &r, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	// Legacy SHA-256 rows (pre-HMAC) — returned as-is; the caller migrates
	// explicitly via MigrateRouteTokenContext after authenticating.
	{
		sha := legacySHA256(token)
		var data, secret string
		err := s.DB.QueryRowContext(ctx,
			`SELECT manifest_json, token_secret FROM proxy_hub_routes WHERE token_secret = ?`, sha).
			Scan(&data, &secret)
		if err == nil {
			var r Route
			if json.Unmarshal([]byte(data), &r) != nil {
				return nil, anyerr.Newf("corrupt route manifest", "STATE_CONFLICT")
			}
			r.Token = token
			return &r, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	// Earliest plaintext rows — returned as-is; see above.
	var data, secret string
	err := s.DB.QueryRowContext(ctx,
		`SELECT manifest_json, token_secret FROM proxy_hub_routes WHERE token_secret = ?`, token).
		Scan(&data, &secret)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Route
	if json.Unmarshal([]byte(data), &r) != nil {
		return nil, anyerr.Newf("corrupt route manifest", "STATE_CONFLICT")
	}
	r.Token = token
	return &r, nil
}

// MigrateRouteTokenContext moves one route's stored secret to the current HMAC
// form. It is the only writer on the legacy-migration path: RouteByTokenContext
// is a pure read, so the UPDATE cannot hide inside per-request authentication.
// Call it after a successful authentication; the legacy row keeps
// authenticating until a migration succeeds, so callers log (never swallow)
// a failure. Already-migrated rows short-circuit on a read: the UPDATE runs
// only when the stored secret differs, keeping the per-request cost to one
// indexed SELECT plus an HMAC.
//
// The write takes the coordinator lock itself (invariant #5) under the hub
// routes scope, re-entrant when the caller already holds it.
func (s *Store) MigrateRouteTokenContext(ctx context.Context, routeID, token string) error {
	if s.Root == "" {
		return anyerr.Newf("hub store has no data root; refusing uncoordinated write", "STATE_CONFLICT")
	}
	// Fast path without the lock: an already-migrated row short-circuits on
	// one indexed SELECT plus an HMAC, keeping the per-request cost off the
	// coordinator. Only a differing row takes the lock and re-checks inside
	// it, so two racing migrations converge on the same idempotent UPDATE.
	var current string
	if err := s.DB.QueryRowContext(ctx,
		`SELECT token_secret FROM proxy_hub_routes WHERE route_id = ?`, routeID).Scan(&current); err != nil {
		return err
	}
	migrated, merr := s.hashToken(ctx, token)
	if merr != nil {
		return merr
	}
	if subtle.ConstantTimeCompare([]byte(current), []byte(migrated)) == 1 {
		return nil
	}
	return lock.WithMutationLock(ctx, s.Root, "hub/routes", func(c context.Context) error {
		var rechecked string
		if err := s.DB.QueryRowContext(c,
			`SELECT token_secret FROM proxy_hub_routes WHERE route_id = ?`, routeID).Scan(&rechecked); err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(rechecked), []byte(migrated)) == 1 {
			return nil
		}
		_, err := s.DB.ExecContext(c,
			`UPDATE proxy_hub_routes SET token_secret = ?, updated_at = ? WHERE route_id = ?`,
			migrated, nowISO(), routeID)
		return err
	})
}

// NewRouteToken generates a fresh per-route bearer secret (32 random bytes,
// hex). The plaintext lives only long enough to be handed to the client that
// will present it; the store persists the HMAC only.
func NewRouteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

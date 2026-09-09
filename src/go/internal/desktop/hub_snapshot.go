package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
)

// This file ports the Hub section of the tray supervisor: hub sources,
// conflicts, published models, the hub proxy row and its lifecycle actions.
// Previously the snapshot left all of these empty, so Proxy Hub was dead in
// production (empty state, disabled Start, zero models) — H1/H2/H3.
//
// Wire contract (both sides owned here):
//   HubSource row: id "hub/<hub>/<provider>/<account>", name = account,
//     hub = hub name. setHubSource sends Name=hub, ProviderID, SourceID=
//     account, Enabled. The toggle honors Enabled explicitly (no presence
//     guessing) and creates the hub config on first enable.
//   Hub proxy row: id "proxy-hub/<hub>", providerId "proxy-hub". Toggle =
//     start/stop, restart = stop-then-start with real errors (no swallowing).
//   HubConflicts stays []: without per-account model catalogs any conflict
//     would be fabricated. Published models come from the hub's pinned
//     routes, which is ground truth.

func hubDir(root, name string) string { return filepath.Join(root, "runtime", "hub", name) }

func hubPIDPath(root, name string) string { return filepath.Join(hubDir(root, name), "service.pid") }

func hubLogPath(root, name string) string { return filepath.Join(hubDir(root, name), "service.log") }

// hubLive reports whether the hub child is ours and answering.
func (s *Server) hubLive(hubName string, port int, host string) (running bool, address string) {
	if host == "" {
		host = "127.0.0.1"
	}
	if port > 0 {
		address = fmt.Sprintf("http://%s:%d", host, port)
	}
	rec := ownership.Load(hubPIDPath(s.Deps.Root, hubName))
	if rec == nil || !rec.VerifyLive() {
		return false, address
	}
	if port > 0 && !hubHealthEcho(address+"/health", rec.InstanceID) {
		return false, address
	}
	return true, address
}

func hubHealthEcho(healthURL, wantInstanceID string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var payload struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload) != nil {
		return false
	}
	return payload.OK && (wantInstanceID == "" || payload.InstanceID == wantInstanceID)
}

// hubConfigs returns all hub configs, or nil when the hub store is unwired
// (legacy tests). A corrupt row fails closed with an error: serving half a
// hub set is worse than showing none.
func (s *Server) hubConfigs(ctx context.Context) ([]hub.Config, error) {
	if s.Deps.Hub == nil {
		return nil, nil
	}
	return s.Deps.Hub.ListConfigsContext(ctx)
}

// buildHubSections fills HubSources, HubConflicts, HubPublishedModels,
// LogSources (hub entries) and appends one proxy row per hub with lifecycle
// actions registered in the caller's registry.
func (s *Server) buildHubSections(ctx context.Context, snap *Snapshot,
	registry map[string]func(context.Context) error, actionID func() string) error {
	configs, err := s.hubConfigs(ctx)
	if err != nil {
		return err
	}
	byHub := map[string]*hub.Config{}
	for i := range configs {
		byHub[configs[i].Name] = &configs[i]
	}
	// The UI always offers the "default" hub, creating it on first use, so
	// an empty install still shows sources to enable and a Start button.
	names := []string{}
	seen := map[string]bool{}
	for _, c := range configs {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	if !seen["default"] {
		names = append(names, "default")
	}
	sort.Strings(names)

	pinned := s.hubPinnedCatalogs(ctx, names)

	for _, providerID := range s.Deps.Providers.IDs() {
		_, err := s.Deps.Providers.Get(providerID)
		if err != nil || s.Deps.Accounts == nil {
			continue
		}
		list, err := s.Deps.Accounts.List(ctx, providerID)
		if err != nil {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Meta.Name < list[j].Meta.Name })
		for _, acc := range list {
			name := acc.Meta.Name
			label := labelOr(acc.Meta.Label, name)
			detail := acc.Meta.Identity
			if detail == "" {
				detail = providerID + "/" + name
			}
			for _, hubName := range names {
				enabled := false
				if cfg := byHub[hubName]; cfg != nil {
					for _, src := range cfg.Sources {
						if src.ProviderID == providerID && src.Account == name && src.IsEnabled() {
							enabled = true
							break
						}
					}
				}
				snap.HubSources = append(snap.HubSources, map[string]any{
					"id":         "hub/" + hubName + "/" + providerID + "/" + name,
					"providerId": providerID,
					"name":       name,
					"label":      label,
					"detail":     detail,
					"enabled":    enabled,
					"hub":        hubName,
				})
			}
			snap.HubCatalogs = append(snap.HubCatalogs, s.hubCatalogRows(ctx, names, pinned, providerID, name, acc.SnapshotDir)...)
		}
	}

	for _, hubName := range names {
		var cfg *hub.Config
		if c := byHub[hubName]; c != nil {
			cp := *c
			cfg = &cp
		}
		port, host := 0, ""
		cfgEnabled := false
		if cfg != nil {
			port, host, cfgEnabled = cfg.Port, cfg.Host, cfg.Enabled
		}
		running, address := s.hubLive(hubName, port, host)
		toggleID, restartID := actionID(), actionID()
		label := "Proxy Hub"
		if hubName != "default" {
			label += " " + hubName
		}
		detail := "stopped"
		if running {
			detail = fmt.Sprintf("running on port %d", port)
		}
		snap.Proxies = append(snap.Proxies, ProxyRow{
			ID: rowIDHub(hubName), ProviderID: "proxy-hub",
			Label: label, Detail: detail, Address: address,
			Running: running, Enabled: cfgEnabled,
			LogsAvailable:   true,
			ToggleActionID:  toggleID,
			RestartActionID: restartID,
		})
		registry[toggleID] = s.hubToggleActivator(hubName)
		registry[restartID] = s.hubRestartActivator(hubName)
		snap.LogSources = append(snap.LogSources, LogSourceRow{
			ID: rowIDHub(hubName), ProviderID: "proxy-hub",
			Name: hubName, Label: label, Detail: detail,
		})
		if err := s.appendHubPublished(ctx, snap, hubName); err != nil {
			s.logErr(err)
		}
	}
	// No fabricated conflicts: without per-account model catalogs any
	// conflict row would be invented. The slice stays as initialized.
	return nil
}

func rowIDHub(hubName string) string { return "proxy-hub/" + hubName }

// appendHubPublished maps the hub's pinned route models to published-model
// rows. Only pinned models appear: unpinned catalog models need per-account
// catalogs the snapshot does not (and must not, per-tick) fetch.
func (s *Server) appendHubPublished(ctx context.Context, snap *Snapshot, hubName string) error {
	if s.Deps.Hub == nil {
		return nil
	}
	models, err := s.Deps.Hub.RouteModelsContext(ctx, hubName)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return nil
	}
	cfg, err := s.Deps.Hub.GetConfigContext(ctx, hubName)
	if err != nil {
		return err
	}
	for _, m := range models {
		ownerLabel, ownerProvider, ownerRef := "", "", ""
		if cfg != nil && m.SourceIndex >= 0 && m.SourceIndex < len(cfg.Sources) {
			src := cfg.Sources[m.SourceIndex]
			ownerProvider = src.ProviderID
			ownerRef = src.ProviderID + "/" + src.Account
			ownerLabel = src.Account
			if acc, aerr := s.Deps.Accounts.Get(ctx, src.ProviderID, src.Account); aerr == nil && acc != nil && acc.Meta.Label != "" {
				ownerLabel = acc.Meta.Label
			}
		}
		row := map[string]any{
			"id": m.Model, "ownerLabel": ownerLabel,
			"ownerProviderId": ownerProvider, "needsChoice": false,
		}
		if ownerRef != "" {
			row["ownerAccountId"] = ownerRef
		}
		snap.HubPublishedModels = append(snap.HubPublishedModels, row)
	}
	return nil
}

// tailHubLog tails a hub child's service.log. Hub output carries no
// account secrets, but the generic credential patterns are still screened
// before framing.
func tailHubLog(root, hubName string, lines int) (string, error) {
	raw, err := os.ReadFile(hubLogPath(root, hubName))
	if err != nil || len(raw) == 0 {
		return "", err
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return proxy.RedactSecrets(strings.Join(all, "\n"), nil), nil
}

// hubToggleActivator flips the hub: running → verified stop (+Enabled=false
// so it stays stopped, matching account proxies after PX1); stopped →
// enable the config (creating "default") and spawn the child.
func (s *Server) hubToggleActivator(hubName string) func(context.Context) error {
	return func(c context.Context) error {
		cfg, err := s.hubConfigOrNil(c, hubName)
		if err != nil {
			return err
		}
		port, host := 0, ""
		if cfg != nil {
			port, host = cfg.Port, cfg.Host
		}
		if running, _ := s.hubLive(hubName, port, host); running {
			return s.stopHub(c, hubName)
		}
		return s.startHub(c, hubName)
	}
}

func (s *Server) hubRestartActivator(hubName string) func(context.Context) error {
	return func(c context.Context) error {
		if err := s.stopHub(c, hubName); err != nil {
			return err
		}
		return s.startHub(c, hubName)
	}
}

func (s *Server) hubConfigOrNil(ctx context.Context, hubName string) (*hub.Config, error) {
	if s.Deps.Hub == nil {
		return nil, anyerr.Newf("hub store unavailable", "HUB_UNAVAILABLE")
	}
	return s.Deps.Hub.GetConfigContext(ctx, hubName)
}

// hubPinnedCatalogs maps hub source ids to their pinned route models, so
// catalog rows carry everything publishable (discovery catalogs plus pins)
// and Swift compiles conflicts and published models from a single input.
// Best-effort per hub: a route or config read failure drops that hub's pins,
// never the snapshot.
func (s *Server) hubPinnedCatalogs(ctx context.Context, hubs []string) map[string][]string {
	out := map[string][]string{}
	if s.Deps.Hub == nil {
		return out
	}
	for _, hubName := range hubs {
		models, err := s.Deps.Hub.RouteModelsContext(ctx, hubName)
		if err != nil || len(models) == 0 {
			continue
		}
		cfg, err := s.Deps.Hub.GetConfigContext(ctx, hubName)
		if err != nil || cfg == nil {
			continue
		}
		for _, m := range models {
			if m.SourceIndex < 0 || m.SourceIndex >= len(cfg.Sources) {
				continue
			}
			src := cfg.Sources[m.SourceIndex]
			id := "hub/" + hubName + "/" + src.ProviderID + "/" + src.Account
			out[id] = append(out[id], m.Model)
		}
	}
	return out
}

// hubCatalogRows returns one HubCatalog row per hub for a saved account:
// {accountID: hub source id, catalog: catalog provider id, models: [...]}.
// Only model ids cross the boundary — endpoints and keys never do.
// Resolution is cache-or-static (discovery NoLive): a cold cache never
// triggers per-tick egress. Live models warm the cache explicitly on hub
// start. Accounts whose provider maps to no catalog, and empty results,
// emit no row: absence reads as "no catalog", never as an error.
func (s *Server) hubCatalogRows(ctx context.Context, hubs []string, pinned map[string][]string, providerID, name, snapshotDir string) []map[string]any {
	catalogID := catalog.CatalogProviderFor(providerID)
	if catalogID == "" {
		return nil
	}
	models := map[string]bool{}
	if providerID == "opencode" {
		for _, id := range provider.OpenCodeSnapshotCatalog(snapshotDir) {
			models[id] = true
		}
		for _, service := range provider.OpenCodeSnapshotServices(snapshotDir) {
			endpoint := provider.OpenCodeServiceEndpoint(service)
			if endpoint == "" {
				continue
			}
			for _, id := range s.catalogList(ctx, catalogID, endpoint) {
				models[id] = true
			}
		}
	} else {
		for _, id := range s.catalogList(ctx, catalogID, "") {
			models[id] = true
		}
	}
	if len(models) == 0 && len(pinned) == 0 {
		return nil
	}
	list := make([]string, 0, len(models))
	for id := range models {
		list = append(list, id)
	}
	rows := make([]map[string]any, 0, len(hubs))
	for _, hubName := range hubs {
		sourceID := "hub/" + hubName + "/" + providerID + "/" + name
		set := map[string]bool{}
		for _, id := range list {
			set[id] = true
		}
		for _, id := range pinned[sourceID] {
			set[id] = true
		}
		if len(set) == 0 {
			continue
		}
		union := make([]string, 0, len(set))
		for id := range set {
			union = append(union, id)
		}
		sort.Strings(union)
		rows = append(rows, map[string]any{
			"accountID": sourceID,
			"catalog":   catalogID,
			"models":    union,
		})
	}
	return rows
}

// catalogList resolves one provider/endpoint pair without egress: fresh or
// stale cache, else the static policy list. A legacy composition without the
// injected discovery service falls back to static only.
func (s *Server) catalogList(ctx context.Context, catalogID, endpoint string) []string {
	if s.Deps.Discovery == nil {
		return discovery.StaticModels(catalogID)
	}
	return s.Deps.Discovery.List(ctx, discovery.Request{ProviderID: catalogID, Endpoint: endpoint, NoLive: true}).Models
}

// openCodeWarmTimeout bounds one catalog fetch during hub start.
const openCodeWarmTimeout = 6 * time.Second

// warmOpenCodeCatalogs refreshes the opencode model cache from saved-mirror
// platform keys. Explicit user action (hub start) only — snapshot ticks stay
// cache-only, so a cold cache never triggers per-tick egress. Keys stay in
// Go: only model ids reach the cache. Advisory throughout: failures log and
// the start proceeds; an unwarmable catalog simply reads empty on the next
// ticks until a later start warms it.
func (s *Server) warmOpenCodeCatalogs(ctx context.Context, fetch func(ctx context.Context, endpoint, key string) ([]string, error)) {
	if s.Deps.Accounts == nil || s.Deps.Discovery == nil {
		return
	}
	accs, err := s.Deps.Accounts.List(ctx, "opencode")
	if err != nil || len(accs) == 0 {
		return
	}
	type job struct{ endpoint, key string }
	seen := map[string]bool{}
	jobs := []job{}
	for _, acc := range accs {
		for service, key := range provider.OpenCodeMirrorKeys(acc.SnapshotDir) {
			endpoint := provider.OpenCodeServiceEndpoint(service)
			if endpoint == "" || key == "" || seen[endpoint] {
				continue
			}
			seen[endpoint] = true
			jobs = append(jobs, job{endpoint, key})
		}
	}
	if len(jobs) == 0 {
		return
	}
	for _, j := range jobs {
		fetchCtx, cancel := context.WithTimeout(ctx, openCodeWarmTimeout)
		models, err := fetch(fetchCtx, j.endpoint, j.key)
		cancel()
		if err != nil || len(models) == 0 {
			s.logErr(catalogEmptyError(j.endpoint, err))
			continue
		}
		if werr := s.Deps.Discovery.StoreLive(ctx, "opencode", j.endpoint, models); werr != nil {
			s.logErr(werr)
		}
	}
}

func catalogEmptyError(endpoint string, err error) error {
	if err != nil {
		return err
	}
	return anyerr.Newf("opencode catalog at "+endpoint+" returned no models", "CATALOG_EMPTY")
}

func (s *Server) startHub(ctx context.Context, hubName string) error {
	if s.Deps.Activation == nil || s.Deps.Activation.SpawnHubChild == nil {
		return anyerr.Newf("hub spawn unavailable in this composition",
			"ACTIVATION_UNAVAILABLE")
	}
	var spawnErr error
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.ProxyHubRef(hubName)), func(c context.Context) error {
		cfg, gerr := s.hubConfigOrNil(c, hubName)
		if gerr != nil {
			return gerr
		}
		if cfg == nil {
			cfg = &hub.Config{Name: hubName, Sources: []hub.Source{}}
		}
		cfg.Enabled = true
		if _, uerr := s.Deps.Hub.UpsertConfigContext(c, *cfg); uerr != nil {
			return uerr
		}
		if _, serr := s.Deps.Activation.SpawnHubChild(hubName); serr != nil {
			spawnErr = serr
			return serr
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Warm opencode catalogs after spawn: explicit start is the one user
	// action allowed live egress, so the next snapshot ticks read warmed
	// cache instead of cold emptiness. Advisory — never fails the start.
	s.warmOpenCodeCatalogs(ctx, func(c context.Context, endpoint, key string) ([]string, error) {
		return catalog.FetchLiveModels(c, "openai", &catalog.FetchContext{Endpoint: endpoint, APIKey: key})
	})
	return spawnErr
}

func (s *Server) stopHub(ctx context.Context, hubName string) error {
	var stopped bool
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.ProxyHubRef(hubName)), func(c context.Context) error {
		cfg, gerr := s.hubConfigOrNil(c, hubName)
		if gerr != nil {
			return gerr
		}
		port, host := 0, ""
		if cfg != nil {
			port, host = cfg.Port, cfg.Host
		}
		addr := ""
		if port > 0 {
			if host == "" {
				host = "127.0.0.1"
			}
			addr = fmt.Sprintf("http://%s:%d/health", host, port)
		}
		stopped = ownership.Stop(hubPIDPath(s.Deps.Root, hubName), ownership.StopOptions{
			Health: func(r ownership.Record) bool {
				if addr == "" {
					return true
				}
				return hubHealthEcho(addr, r.InstanceID)
			},
		})
		if cfg != nil && cfg.Enabled {
			cfg.Enabled = false
			if _, uerr := s.Deps.Hub.UpsertConfigContext(c, *cfg); uerr != nil {
				return uerr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !stopped {
		return anyerr.Newf("hub "+hubName+" is not running", "HUB_NOT_RUNNING")
	}
	return nil
}

// handleHubSourceToggle honors the explicit Enabled flag: true attaches the
// source (creating the hub config on first use), false detaches it. The old
// presence-toggle ignored Enabled and misread Name, inverting the switch.
func (s *Server) handleHubSourceToggle(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID}
	name := orString(strings.TrimSpace(m.Name), "default")
	account := strings.TrimSpace(orString(m.SourceID, m.Label))
	if m.ProviderID == "" || account == "" {
		return errorResult(m.RequestID, anyerr.Newf(
			"hub-source-toggle needs providerId and sourceId", "INVALID_USAGE"))
	}
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.ProxyHubRef(name)), func(c context.Context) error {
		cfg, gerr := s.hubConfigOrNil(c, name)
		if gerr != nil {
			return gerr
		}
		if cfg == nil {
			if !m.Enabled {
				return nil
			}
			cfg = &hub.Config{Name: name, Sources: []hub.Source{}}
		}
		kept := make([]hub.Source, 0, len(cfg.Sources))
		for _, src := range cfg.Sources {
			if src.ProviderID == m.ProviderID && src.Account == account {
				continue
			}
			kept = append(kept, src)
		}
		if m.Enabled {
			enabled := true
			kept = append(kept, hub.Source{ProviderID: m.ProviderID, Account: account, Enabled: &enabled})
		}
		cfg.Sources = kept
		_, uerr := s.Deps.Hub.UpsertConfigContext(c, *cfg)
		return uerr
	})
	if err != nil {
		result.Status, result.Message = "error", err.Error()
		return result
	}
	if m.Enabled {
		result.Status, result.Message = "success", "Added to the hub."
	} else {
		result.Status, result.Message = "success", "Removed from the hub."
	}
	return result
}

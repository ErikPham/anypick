package desktop

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

// Snapshot mirrors TraySnapshot field-for-field. Sections the Go engine does
// not populate yet are emitted as empty arrays so UI typing keeps working.
type Snapshot struct {
	ProxyCount         int                  `json:"proxyCount"`
	Revision           int                  `json:"revision"`
	Routes             []map[string]any     `json:"routes"`
	Actions            []ActionRow          `json:"actions"`
	ClientModelConfigs []map[string]any     `json:"clientModelConfigs"`
	Usage              []UsageSnapshotRow   `json:"usage"`
	Proxies            []ProxyRow           `json:"proxies"`
	Accounts           []AccountRow         `json:"accounts"`
	HubSources         []map[string]any     `json:"hubSources"`
	HubConflicts       []map[string]any     `json:"hubConflicts"`
	LogSources         []LogSourceRow       `json:"logSources"`
	Gateways           []GatewayRow         `json:"gateways"`
	AccountProviders   []AccountProviderRow `json:"accountProviders"`
	GatewayProviders   []GatewayProviderRow `json:"gatewayProviders"`
	Settings           Preferences          `json:"settings"`
	Activity           []map[string]any     `json:"activity"`
	DegradedConditions []DegradedCondition  `json:"degradedConditions"`
}

// DegradedCondition is a redacted, user-actionable engine condition. It is
// separate from Activity because it represents current state, not history.
type DegradedCondition struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// ActionRow mirrors TrayActionSnapshot (subset emitted today).
type ActionRow struct {
	ID             string `json:"id"`
	ClientID       string `json:"clientId"`
	SourceID       string `json:"sourceId"`
	Client         string `json:"client"`
	Label          string `json:"label"`
	Detail         string `json:"detail,omitempty"`
	Kind           string `json:"kind"` // 'native' | 'gateway'
	Presentation   string `json:"presentation"`
	Selected       bool   `json:"selected"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabledReason,omitempty"`
	RouteKind      string `json:"routeKind,omitempty"` // 'direct-account'
}

// ProxyRow mirrors TrayProxySnapshot.
type ProxyRow struct {
	ID              string `json:"id"`
	ProviderID      string `json:"providerId"`
	Label           string `json:"label"`
	Detail          string `json:"detail"`
	Address         string `json:"address,omitempty"`
	Running         bool   `json:"running"`
	Enabled         bool   `json:"enabled"`
	LogsAvailable   bool   `json:"logsAvailable,omitempty"`
	ToggleActionID  string `json:"toggleActionId"`
	RestartActionID string `json:"restartActionId"`
}

// AccountRow mirrors TrayManagedAccountSnapshot.
type AccountRow struct {
	ID             string `json:"id"`
	ProviderID     string `json:"providerId"`
	SourceID       string `json:"sourceId,omitempty"`
	Name           string `json:"name"`
	Label          string `json:"label"`
	Detail         string `json:"detail"`
	Active         bool   `json:"active"`
	CanRefresh     bool   `json:"canRefresh"`
	SwitchActionID string `json:"switchActionId"`
}

// LogSourceRow mirrors TrayLogSourceSnapshot.
type LogSourceRow struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Detail     string `json:"detail"`
}

// GatewayRow mirrors TrayGatewaySnapshot (display subset; gateways arrive
// with the profile store port).
type GatewayRow = map[string]any

// AccountProviderRow mirrors TrayAccountProviderSnapshot.
type AccountProviderRow struct {
	ID           string           `json:"id"`
	ProviderID   string           `json:"providerId"`
	ClientID     string           `json:"clientId,omitempty"`
	SourceID     string           `json:"sourceId,omitempty"`
	Label        string           `json:"label"`
	Detail       string           `json:"detail"`
	Installed    bool             `json:"installed"`
	LivePresent  bool             `json:"livePresent"`
	LiveIdentity string           `json:"liveIdentity,omitempty"`
	LoginMethods []LoginMethodRow `json:"loginMethods"`
	CanClear     bool             `json:"canClear,omitempty"`
}

// LoginMethodRow is the non-secret provider-owned browser-login capability
// exposed to the native host. Launcher availability is recomputed per
// snapshot, so installing an official client takes effect without restart.
type LoginMethodRow struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Detail            string `json:"detail,omitempty"`
	Provenance        string `json:"provenance"`
	Preferred         bool   `json:"preferred"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

// GatewayProviderRow mirrors TrayGatewayProviderSnapshot.
type GatewayProviderRow struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Detail              string   `json:"detail"`
	ProvidesEndpoint    bool     `json:"providesEndpoint,omitempty"`
	FirstPartyClientIDs []string `json:"firstPartyClientIds,omitempty"`
	Kind                string   `json:"kind,omitempty"`
}

// Preferences mirrors TrayPreferences.
type Preferences struct {
	LaunchAtLogin       bool `json:"launchAtLogin"`
	StartEnabledProxies bool `json:"startEnabledProxies"`
	ShowQuota           bool `json:"showQuota"`
	QuotaGuardEnabled   bool `json:"quotaGuardEnabled"`
}

// currentSettings reads tray preferences from the config service when wired,
// falling back to TS defaults (showQuota/startEnabledProxies true).
func (s *Server) currentSettings(ctx context.Context) Preferences {
	prefs := Preferences{StartEnabledProxies: true, ShowQuota: true}
	// Login-item state is platform truth: only a wired host-state provider
	// may report it (ADR 0020 §7). Absence reports disabled.
	if enabled, reported := s.reportedLaunchAtLogin(); reported {
		prefs.LaunchAtLogin = enabled
	} else if s.Deps.HostState != nil {
		prefs.LaunchAtLogin = s.Deps.HostState.LaunchAtLoginEnabled()
	}
	if s.Deps.Config != nil {
		if cfg, err := s.Deps.Config.Read(ctx); err == nil {
			p := config.TrayPreferencesOf(cfg, prefs.LaunchAtLogin)
			prefs.LaunchAtLogin = p.LaunchAtLogin
			prefs.StartEnabledProxies = p.StartEnabledProxies
			prefs.ShowQuota = p.ShowQuota
			prefs.QuotaGuardEnabled = p.QuotaGuardEnabled
		}
	}
	return prefs
}

// buildSnapshot assembles the current state and registers opaque action
// handlers for this revision. Field names match TraySnapshot exactly so the
// existing native app can consume it unchanged.
func (s *Server) buildSnapshot(ctx context.Context) (*Snapshot, error) {
	s.revision++
	rev := s.revision
	snap := &Snapshot{
		Revision:           rev,
		Routes:             []map[string]any{},
		Actions:            []ActionRow{},
		ClientModelConfigs: []map[string]any{},
		Usage:              []UsageSnapshotRow{},
		Proxies:            []ProxyRow{},
		Accounts:           []AccountRow{},
		HubSources:         []map[string]any{},
		HubConflicts:       []map[string]any{},
		LogSources:         []LogSourceRow{},
		Gateways:           []GatewayRow{},
		AccountProviders:   []AccountProviderRow{},
		GatewayProviders:   []GatewayProviderRow{},
		Settings:           Preferences{ShowQuota: true},
		Activity:           []map[string]any{},
		DegradedConditions: []DegradedCondition{},
	}
	registry := map[string]func(context.Context) error{}
	actionSeq := 0
	actionID := func() string { actionSeq++; return fmt.Sprintf("act-%d-%03d", rev, actionSeq) }

	// Settings reflect the persisted global config + login-item state.
	snap.Settings = s.currentSettings(ctx)
	if s.Deps.PluginDiagnostics != nil {
		diagnostics := s.Deps.PluginDiagnostics(ctx)
		if diagnostics.Host.State == "degraded" {
			snap.DegradedConditions = append(snap.DegradedConditions, DegradedCondition{
				ID: "plugin-host", Kind: "plugin-host", Title: "Plugin host unavailable",
				Detail: diagnostics.Host.Error,
			})
		}
		for _, failure := range diagnostics.Failures {
			title := "Plugin couldn’t load"
			if failure.Untrusted {
				title = "Plugin changed after trust"
			}
			snap.DegradedConditions = append(snap.DegradedConditions, DegradedCondition{
				ID: "plugin:" + failure.Name, Kind: "plugin", Title: title,
				Detail: failure.Name + ": " + failure.Reason,
			})
		}
	}

	for _, providerID := range s.Deps.Providers.IDs() {
		pv, err := s.Deps.Providers.Get(providerID)
		if err != nil {
			continue
		}
		list, err := s.Deps.Store.ListAccountsContext(ctx, providerID)
		if err != nil {
			continue
		}
		active, _ := s.Deps.Store.GetActiveContext(ctx, providerID)

		sort.Slice(list, func(i, j int) bool { return list[i].Meta.Name < list[j].Meta.Name })
		for _, acc := range list {
			sourceID := refs.SerializeRef(refs.AccountRef(providerID, acc.Meta.Name))
			rowID := providerID + "/" + acc.Meta.Name

			// Two distinct registrations per saved account, mirroring TS:
			// the accounts-panel row switches the provider's active account
			// (account-switch operation), while the Switch-menu chip binds the
			// client through the persistent planner (invokeTrayAction →
			// bindingService.use). One shared id cannot serve both.
			switchID := actionID()
			routeID := actionID()
			name := acc.Meta.Name
			_, canRefresh := pv.(interface{ RefreshAuth(string) error })
			snap.Accounts = append(snap.Accounts, AccountRow{
				ID: rowID, ProviderID: providerID, SourceID: sourceID,
				Name: name, Label: labelOr(acc.Meta.Label, name),
				Active: active == name, CanRefresh: canRefresh,
				SwitchActionID: switchID,
			})
			registry[switchID] = func(c context.Context) error {
				_, uerr := s.Deps.Accounts.Use(c, providerID, name, accounts.UseOptions{NoProxy: false})
				return uerr
			}
			registry[routeID] = s.routeActivator(providerID, refs.AccountRef(providerID, acc.Meta.Name))
			selected := active == name
			short := shortNameOf(s, pv)
			snap.Actions = append(snap.Actions, ActionRow{
				ID:           routeID,
				ClientID:     providerID,
				SourceID:     sourceID,
				Client:       short,
				Label:        fmt.Sprintf("%s · %s", short, acc.Meta.Name),
				Kind:         "native",
				Presentation: "native-account",
				Selected:     selected,
				Enabled:      true,
				RouteKind:    "direct-account",
			})

			cfg, _ := s.Deps.Store.GetProxyConfigContext(ctx, providerID, name)
			if !cfg.Enabled {
				continue
			}
			st, statusErr := s.Deps.Proxy.Status(providerID, name)
			toggleID := actionID()
			restartID := actionID()
			running, address := proxySnapshotState(st, statusErr)
			snap.Proxies = append(snap.Proxies, ProxyRow{
				ID: rowID, ProviderID: providerID,
				Label:           fmt.Sprintf("%s/%s proxy", providerID, name),
				Detail:          detailFor(running, cfg.Port),
				Address:         address,
				Running:         running,
				Enabled:         true,
				ToggleActionID:  toggleID,
				RestartActionID: restartID,
			})
			registry[toggleID] = func(c context.Context) error {
				if running {
					return s.Deps.Proxy.Stop(c, providerID, name)
				}
				_, serr := s.Deps.Proxy.StartProxy(c, providerID, name)
				return serr
			}
			registry[restartID] = func(c context.Context) error {
				_ = s.Deps.Proxy.Stop(c, providerID, name)
				_, serr := s.Deps.Proxy.StartProxy(c, providerID, name)
				return serr
			}
			snap.ProxyCount = len(snap.Proxies)
		}

		// accountProviders rows for file-snapshot providers.
		live, lerr := pv.DetectLive()
		methods, merr := s.Deps.Accounts.LoginMethods(ctx, providerID, "")
		loginMethods := make([]LoginMethodRow, 0, len(methods))
		installed := lerr == nil && live.Present
		if merr == nil {
			for _, method := range methods {
				loginMethods = append(loginMethods, LoginMethodRow{
					ID: method.ID, Label: method.Label, Detail: method.Description,
					Provenance: method.Provenance, Preferred: method.Preferred,
					Available: method.Available, UnavailableReason: method.UnavailableReason,
				})
				installed = installed || method.Available
			}
		}
		row := AccountProviderRow{
			ID: providerID, ProviderID: providerID,
			Label: pv.Name(), Detail: pv.Description(),
			Installed: installed, LoginMethods: loginMethods,
		}
		if lerr == nil {
			row.LivePresent = live.Present
			row.LiveIdentity = live.Identity
			if live.Identity != "" {
				row.Detail = live.Identity
			}
		}
		if clearer, ok := pv.(provider.LiveClearer); ok && clearer != nil {
			row.CanClear = true
		}
		snap.AccountProviders = append(snap.AccountProviders, row)

		snap.LogSources = append(snap.LogSources, LogSourceRow{
			ID: providerID + "/__provider__", ProviderID: providerID,
			Name: "__provider__", Label: pv.Name(),
		})
	}

	// Compatible vendors surface as gateway providers.
	for _, providerID := range s.Deps.Providers.IDs() {
		pv, err := s.Deps.Providers.Get(providerID)
		if err != nil {
			continue
		}
		if cp, ok := pv.(interface {
			Definition() *provider.CompatibleDefinition
		}); ok {
			def := cp.Definition()
			snap.GatewayProviders = append(snap.GatewayProviders, GatewayProviderRow{
				ID: def.ID, Label: def.Name, Detail: def.Description,
				ProvidesEndpoint: len(def.Credentials) > 0 &&
					len(def.Credentials[0].Profiles) > 0 &&
					strings.Contains(def.Credentials[0].Profiles[0].OpenAIBase, "http"),
			})
		}
	}

	// Apps view rows: every client id that appears anywhere — the five known
	// apps plus any provider holding saved accounts (managedAppClientIDs).
	appIDs := s.managedAppClientIDs()

	// Cross-provider actions: each client sees every compatible account as a
	// switching chip. Uses the transport adapter to determine pairing.
	for _, clientID := range s.Deps.Clients.IDs() {
		cad, cerr := s.Deps.Clients.Get(clientID)
		if cerr != nil {
			continue
		}
		short := clientID
		if sn, ok := cad.(provider.ShortNamer); ok && sn.ShortName() != "" {
			short = sn.ShortName()
		} else if fields := strings.Fields(cad.Name()); len(fields) > 0 {
			short = fields[0]
		}
		for _, otherProviderID := range s.Deps.Providers.IDs() {
			if otherProviderID == clientID {
				continue // own accounts already emitted above
			}
			pv2, perr := s.Deps.Providers.Get(otherProviderID)
			if perr != nil || !pv2.CanProxy() {
				continue
			}
			list, lerr := s.Deps.Store.ListAccountsContext(ctx, otherProviderID)
			if lerr != nil || len(list) == 0 {
				continue
			}
			sample := list[0]
			adapter := pv2.AccountAdapter(sample.Meta.Name, sample.Proxy.Enabled)
			if cap := adapter.TransportFor(clientID); cap != transport.ManagedBuiltinProxy {
				continue
			}
			sn := provider.ShortNameOf(pv2)
			for _, acc := range list {
				id := fmt.Sprintf("xprov-%d-%s-%s-%s", rev, clientID, otherProviderID, acc.Meta.Name)
				row := ActionRow{
					ID:           id,
					ClientID:     clientID,
					SourceID:     refs.SerializeRef(refs.AccountRef(otherProviderID, acc.Meta.Name)),
					Client:       short,
					Label:        fmt.Sprintf("%s · %s", sn, acc.Meta.Name),
					Kind:         "native",
					Presentation: "app-route",
					Selected:     false,
					Enabled:      true,
					RouteKind:    "account",
				}
				snap.Actions = append(snap.Actions, row)
				// The chip binds THIS client to the other provider's account;
				// switching that provider's own active account would be a
				// wrong-scope mutation (TS target: {clientId, source}).
				registry[id] = s.routeActivator(clientID,
					refs.AccountRef(otherProviderID, acc.Meta.Name))
			}
		}
	}

	// Gateway actions: each client sees every ready gateway as a chip.
	if names, lerr := s.Deps.Profiles.ListContext(ctx); lerr == nil {
		sort.Strings(names)
		for _, gwName := range names {
			pr, gerr := s.Deps.Profiles.GetContext(ctx, gwName)
			if gerr != nil || pr == nil || pr.Meta.Endpoint == "" {
				continue
			}
			for _, clientID := range s.Deps.Clients.IDs() {
				cad, cerr := s.Deps.Clients.Get(clientID)
				if cerr != nil {
					continue
				}
				short := clientID
				if sn, ok := cad.(provider.ShortNamer); ok && sn.ShortName() != "" {
					short = sn.ShortName()
				} else if fields := strings.Fields(cad.Name()); len(fields) > 0 {
					short = fields[0]
				}
				id := fmt.Sprintf("gw-%d-%s-%s", rev, clientID, gwName)
				row := ActionRow{
					ID:           id,
					ClientID:     clientID,
					SourceID:     refs.SerializeRef(refs.GatewayRef(gwName)),
					Client:       short,
					Label:        gwName,
					Kind:         "gateway",
					Presentation: "app-route",
					Selected:     false,
					Enabled:      true,
					RouteKind:    "gateway",
				}
				snap.Actions = append(snap.Actions, row)
				// Gateway chips activate like any other source (bindingService.use).
				// A nil handler reported success without mutating anything.
				registry[id] = s.routeActivator(clientID, refs.GatewayRef(gwName))
			}
		}
	}

	// Current-route action per client — the Apps view subtitle reads the
	// selected action's label ("Uses …"), so it must exist even when there is
	// no binding (native login case).
	for _, clientID := range appIDs {
		short := designAppName(clientID)
		binding, berr := s.Deps.Bindings.GetGlobalContext(ctx, clientID)
		if berr != nil {
			return nil, berr
		}
		cur, _ := s.Deps.Accounts.Current(ctx, clientID)

		var row *ActionRow
		switch {
		case binding != nil:
			srcRef := binding.Spec.Source
			label := refs.DisplayRef(srcRef)
			kind := "gateway"
			routeKind := "gateway"
			if srcRef.Kind == refs.KindAccount {
				kind = "native"
				routeKind = "direct-account"
				if accRow, aerr := s.Deps.Store.GetContext(ctx, srcRef.Provider, srcRef.Name); aerr == nil && accRow != nil {
					pv2, _ := s.Deps.Providers.Get(srcRef.Provider)
					sn := srcRef.Provider
					if pv2 != nil {
						sn = provider.ShortNameOf(pv2)
					}
					label = fmt.Sprintf("%s · %s", sn, accRow.Meta.Name)
				}
			}
			id := actionID()
			row = &ActionRow{
				ID: id, ClientID: clientID,
				SourceID:     refs.SerializeRef(srcRef),
				Client:       short,
				Label:        label,
				Kind:         kind,
				Presentation: "app-route",
				Selected:     true,
				Enabled:      true,
				RouteKind:    routeKind,
			}
			// Re-invoking the selected route re-applies its source through the
			// planner regardless of kind; never a silent no-op.
			registry[id] = s.routeActivator(clientID, srcRef)
		case cur != nil && cur.Live.Present && cur.Active != "":
			pv2, perr := s.Deps.Providers.Get(clientID)
			sn := clientID
			if perr == nil && pv2 != nil {
				sn = provider.ShortNameOf(pv2)
			}
			id := actionID()
			active := cur.Active
			row = &ActionRow{
				ID: id, ClientID: clientID,
				SourceID:     clientID,
				Client:       short,
				Label:        fmt.Sprintf("%s · %s", sn, active),
				Kind:         "native",
				Presentation: "app-route",
				Selected:     true,
				Enabled:      true,
				RouteKind:    "direct-account",
			}
			registry[id] = s.routeActivator(clientID, refs.AccountRef(clientID, active))
		}
		if row != nil {
			snap.Actions = append(snap.Actions, *row)
		}
	}

	// Routes: one row per app — what Apps view renders.
	for _, clientID := range appIDs {
		var binding *bindings.Global
		if s.Deps.Bindings != nil {
			var berr error
			binding, berr = s.Deps.Bindings.GetGlobalContext(ctx, clientID)
			if berr != nil {
				return nil, berr
			}
		}
		nativePresent := false
		if _, perr := s.Deps.Providers.Get(clientID); perr == nil {
			if cur, cerr2 := s.Deps.Accounts.Current(ctx, clientID); cerr2 == nil && cur != nil {
				nativePresent = cur.Live.Present
			}
		}
		route := map[string]any{
			"clientId": clientID,
			"client":   designAppName(clientID),
			"status":   "unbound",
		}
		switch {
		case binding != nil:
			route["source"] = refs.DisplayRef(binding.Spec.Source)
			if model := primaryModelOf(binding.Spec); model != "" {
				route["model"] = model
			}
			route["status"] = "ready"
		case nativePresent:
			route["source"] = "native login"
			route["status"] = "native"
		}
		snap.Routes = append(snap.Routes, route)
	}

	// Gateways: saved vendor profiles.
	if names, lerr := s.Deps.Profiles.ListContext(ctx); lerr == nil {
		for _, name := range names {
			pr, gerr := s.Deps.Profiles.GetContext(ctx, name)
			if gerr != nil || pr == nil {
				continue
			}
			defaultModel := pr.Meta.DefaultModel
			snap.Gateways = append(snap.Gateways, GatewayRow{
				"id":           name,
				"providerId":   pr.Meta.Provider,
				"name":         name,
				"detail":       orString(pr.Meta.Label, pr.Meta.Provider),
				"ready":        pr.Meta.Endpoint != "",
				"defaultModel": defaultModel,
			})
		}
	}

	// Usage: quota/rate-limit for providers with a live login. Kept in a
	// separate builder so app-service mode can refresh this bounded section
	// without rebuilding and polling the full snapshot.
	snap.Usage = s.buildUsage(ctx)

	s.actionsMu.Lock()
	s.prevActions = s.actions
	s.actions = registry
	s.actionRows = snapActionsIndex(snap)
	s.actionOrder = snapActionsOrder(snap)
	s.actionsMu.Unlock()
	return snap, nil
}

func (s *Server) buildUsage(ctx context.Context) []UsageSnapshotRow {
	rows := make([]UsageSnapshotRow, 0)
	for _, providerID := range s.Deps.Providers.IDs() {
		pv, err := s.Deps.Providers.Get(providerID)
		if err != nil {
			continue
		}
		lup, ok := pv.(provider.LiveUsageProvider)
		if !ok {
			continue
		}
		usage, uerr := lup.LiveUsage(ctx)
		if uerr != nil || usage == nil || len(usage.Windows) == 0 {
			continue
		}
		short := provider.ShortNameOf(pv)
		account := ""
		if active, _ := s.Deps.Store.GetActive(providerID); active != "" {
			account = active
		}
		var windows []map[string]interface{}
		for _, w := range usage.Windows {
			label := strings.TrimSpace(w.Label)
			if len(label) > 24 {
				label = label[:24]
			}
			entry := map[string]interface{}{
				"label":            label,
				"remainingPercent": w.RemainingPercent,
			}
			if w.ResetsAtMs != nil {
				entry["resetsAtMs"] = *w.ResetsAtMs
			}
			windows = append(windows, entry)
		}
		rows = append(rows, UsageSnapshotRow{
			Client:  short,
			Account: orString(account, providerID),
			Windows: windows,
		})
	}
	return rows
}

// primaryModelOf mirrors primaryModel: explicit selection wins, then the
// default model role from client options.
func primaryModelOf(spec bindings.Spec) string {
	if spec.Model.Mode == "explicit" {
		return spec.Model.ID
	}
	if roles, ok := spec.ClientOptions["modelRoles"].(map[string]interface{}); ok {
		if def, ok := roles["default"].(string); ok && def != "" {
			return def
		}
	}
	return ""
}

func providerNameOf(s *Server, providerID string) string {
	if pv, err := s.Deps.Providers.Get(providerID); err == nil {
		return pv.Name()
	}
	return providerID
}

func labelOr(label, fallback string) string {
	if label != "" {
		return label
	}
	return fallback
}

func detailFor(running bool, port int) string {
	if running {
		return fmt.Sprintf("running on port %d", port)
	}
	return "stopped"
}

// proxySnapshotState keeps Status failures and absent status records on the
// stopped path. Go evaluates function arguments eagerly, so selecting
// st.Endpoint through a generic ternary helper would still dereference nil.
func proxySnapshotState(st *proxy.Status, err error) (bool, string) {
	if err != nil || st == nil || !st.Running {
		return false, ""
	}
	return true, st.Endpoint
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// shortNameOf returns the compact display name for a provider row.
func shortNameOf(s *Server, pv interface{ Name() string }) string {
	if p, ok := pv.(provider.Provider); ok {
		return provider.ShortNameOf(p)
	}
	return pv.Name()
}

// designAppName mirrors designAppName in ProductionModel.swift.
func designAppName(client string) string {
	switch strings.ToLower(client) {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini"
	case "kiro":
		return "Kiro"
	case "grok":
		return "Grok"
	case "opencode":
		return "OpenCode"
	default:
		return strings.ToUpper(client[:1]) + client[1:]
	}
}

// managedAppClientIDs mirrors Store.managedAppClientIDs: every clientId that
// appears on any action, first-occurrence order, plus the five known apps so
// unbound/absent apps still render a row.
func (s *Server) managedAppClientIDs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range s.actionOrder {
		if a, ok := s.actionRows[id]; ok {
			add(a.ClientID)
		}
	}
	for _, id := range []string{"claude", "codex", "gemini", "kiro", "opencode"} {
		add(id)
	}
	return out
}

func snapActionsIndex(snap *Snapshot) map[string]ActionRow {
	out := make(map[string]ActionRow, len(snap.Actions))
	for _, a := range snap.Actions {
		out[a.ID] = a
	}
	return out
}

func snapActionsOrder(snap *Snapshot) []string {
	out := make([]string, len(snap.Actions))
	for i, a := range snap.Actions {
		out[i] = a.ID
	}
	return out
}

// UsageSnapshotRow mirrors TrayUsageSnapshot.
type UsageSnapshotRow struct {
	Client  string                   `json:"client"`
	Account string                   `json:"account"`
	Windows []map[string]interface{} `json:"windows"`
}

package desktop

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/journal"
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
	HubPublishedModels []map[string]any     `json:"hubPublishedModels"`
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
	// CredentialKinds lists the explicit credential products this provider
	// accepts through account-save-input (API keys / plan keys for compatible
	// vendors). Native-only providers leave it empty: their accounts come
	// from a live login or a browser sign-in, never from typed input.
	// Definitions only — no secrets cross this boundary.
	CredentialKinds []CredentialKindRow `json:"credentialKinds,omitempty"`
}

// CredentialKindRow is one BackupInput credential product: its kind id, the
// label the UI shows, and the endpoint profiles the user can attach it to.
type CredentialKindRow struct {
	Kind     string                 `json:"kind"`
	Label    string                 `json:"label"`
	Profiles []CredentialProfileRow `json:"profiles"`
}

// CredentialProfileRow is one endpoint profile within a credential kind.
type CredentialProfileRow struct {
	ID    string `json:"id"`
	Label string `json:"label"`
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
	RequiresApiKey      bool     `json:"requiresApiKey"`
	DefaultEndpoint     string   `json:"defaultEndpoint,omitempty"`
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
		HubPublishedModels: []map[string]any{},
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
			// Emit a row for every saved account behind a proxy-capable
			// provider, not just enabled ones: the Proxies pane is the only
			// place that offers the first Start, and without a row the
			// toggle action is never registered so enabling stays silent.
			if !pv.CanProxy() {
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
				Enabled:         cfg.Enabled,
				LogsAvailable:   true,
				ToggleActionID:  toggleID,
				RestartActionID: restartID,
			})
			registry[toggleID] = func(c context.Context) error {
				// Re-check liveness inside the handler: `running` was captured
				// at snapshot build and the child may have died (or started)
				// since, which used to run the wrong branch with a false
				// success (PX2).
				if st, serr := s.Deps.Proxy.StatusContext(c, providerID, name); serr == nil && st != nil && st.Running {
					return s.Deps.Proxy.Stop(c, providerID, name)
				}
				if _, serr := s.Deps.Proxy.StartProxy(c, providerID, name); serr != nil {
					return serr
				}
				return s.reapplyClientsBoundTo(c, refs.AccountRef(providerID, name))
			}
			registry[restartID] = func(c context.Context) error {
				if err := s.Deps.Proxy.Stop(c, providerID, name); err != nil {
					return err
				}
				if _, serr := s.Deps.Proxy.StartProxy(c, providerID, name); serr != nil {
					return serr
				}
				return s.reapplyClientsBoundTo(c, refs.AccountRef(providerID, name))
			}
			if cfg.Enabled {
				snap.ProxyCount++
			}
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
			// ClientID lets the native client resolve this provider to its
			// managed app row (Store.managedAppClientIDs). Without it the
			// Apps view can only see providers that already own actions, so
			// a provider with zero saved accounts (e.g. fresh WorkBuddy)
			// never appears.
			ClientID: providerID,
			Label:    pv.Name(), Detail: pv.Description(),
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
		// Compatible vendors (and any future BackupInputer) accept typed
		// credentials even though they have no live login and no browser
		// flow. Publishing the kinds/profiles is what lets the native Add
		// Account sheet offer "API key" input instead of a disabled row.
		if def, ok := pv.(interface {
			Definition() *provider.CompatibleDefinition
		}); ok {
			row.CredentialKinds = credentialKindRows(def.Definition())
		}
		snap.AccountProviders = append(snap.AccountProviders, row)
	}

	// Gateway providers: compatible vendors (fixed endpoint) plus first-party
	// providers that can serve an upstream from a profile. gemini implements
	// ResolveUpstream(snapshotDir) and is bridged from the saved profile key,
	// so it belongs in the Gateway/Proxy picker even though it is not a
	// CompatibleDefinition vendor. grok also implements ResolveUpstream and is
	// bridged from the live OIDC session (~/.grok/auth.json) it shares with the
	// account flow, so it belongs in the picker too (requiresApiKey=false).
	// opencode/workbuddy also implement ResolveUpstream but need credentials the
	// picker cannot supply (Zen/Go key in a specific auth doc, subscription
	// login), so they are intentionally excluded (see the eligibility gate
	// below). Compatible providers win on ID so there is no duplicate row.
	addedGateway := make(map[string]bool)
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
				// Compatible vendors are bring-your-own-key gateways.
				RequiresApiKey: true,
			})
			addedGateway[def.ID] = true
			continue
		}
		_, isUpstream := pv.(interface {
			ResolveUpstream(snapshotDir string) (*proxy.Upstream, error)
		})
		if !isUpstream || addedGateway[providerID] {
			continue
		}
		// Only providers the gateway can actually serve belong in the
		// Gateway/Proxy picker. gemini is bridged from the saved profile key
		// (see snapshotUpstreamResolver). grok is bridged from the live OIDC
		// session it shares with the account flow (requiresApiKey=false).
		// opencode needs a Zen/Go key in a specific auth doc and workbuddy a
		// subscription login — neither of which the picker's inputs can supply,
		// so listing them would create dead gateways that fail at first request.
		if providerID != "gemini" && providerID != "grok" {
			continue
		}
		row := GatewayProviderRow{
			ID:     providerID,
			Label:  pv.Name(),
			Detail: pv.Description(),
			Kind:   "hosted",
		}
		switch providerID {
		case "gemini":
			// gemini-proxy serves the Google Gemini API (api-key or
			// OAuth/Code Assist) translated from OpenAI/Anthropic/Responses.
			// The api-key upstream base is fixed, so the engine supplies it.
			row.ProvidesEndpoint = true
			row.DefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta"
			row.FirstPartyClientIDs = []string{"gemini"}
			row.RequiresApiKey = true
		case "grok":
			// grok-proxy serves cli-chat-proxy.grok.com using the live x.ai
			// OIDC session (grok login) shared with the account flow. No API
			// key is collected; the upstream auth is refreshed per request.
			row.ProvidesEndpoint = true
			row.DefaultEndpoint = "https://cli-chat-proxy.grok.com"
			row.RequiresApiKey = false
		}
		snap.GatewayProviders = append(snap.GatewayProviders, row)
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
				// A single client binding must not blank the whole snapshot
				// (Proxies pane included). Treat it as unbound.
				binding = nil
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

	// Activity: mutation history from the operation journal (the single source
	// of truth — never re-derived). A nil journal or a read failure yields an
	// honest empty list, never a fabricated event.
	snap.Activity = s.buildActivity(ctx)

	// Log sources mirror the proxies that can actually serve ProxyLogs
	// (providerID/accountName). The old per-provider "__provider__" placeholder
	// never matched a real proxy.log file, so every log request returned empty
	// and the Live Logs pane stayed blank.
	snap.LogSources = logSourcesForProxies(snap.Proxies)

	// Proxy Hub sections (H1/H2/H3): sources for every saved account, one
	// lifecycle-managed proxy row per hub, pinned published models. Hub
	// entries append to Proxies/LogSources; conflicts stay empty (no
	// fabricated rows without model catalogs).
	if err := s.buildHubSections(ctx, snap, registry, actionID); err != nil {
		// Account proxy rows are already filled. A hub-store read must not
		// drop the snapshot and leave Proxies empty.
		s.logErr(err)
	}

	s.actionsMu.Lock()
	s.prevActions = s.actions
	s.actions = registry
	s.actionRows = snapActionsIndex(snap)
	s.actionOrder = snapActionsOrder(snap)
	s.actionsMu.Unlock()
	return snap, nil
}

// buildActivity maps the operation journal to the Activity snapshot section.
// The journal is the single source of truth for mutation history (invariant:
// ActivationPlan.steps drives execution; the snapshot never re-derives it).
// Only display-safe fields cross the boundary: ids, timestamps, affected
// resource identities and the terminal state. Entry params may carry
// credential-adjacent metadata and are never read here.
func (s *Server) buildActivity(ctx context.Context) []map[string]any {
	out := []map[string]any{}
	if s.Deps.Journal == nil {
		return out
	}
	entries, err := s.Deps.Journal.ListRecentContext(ctx, 50)
	if err != nil {
		return out
	}
	for _, e := range entries {
		createdAt := e.StartedAt
		if createdAt == "" {
			createdAt = e.UpdatedAt
		}
		row := map[string]any{
			"id":        e.ID,
			"createdAt": createdAt,
			"message":   activityMessage(e),
			"isError":   activityIsError(e),
			"kind":      activityKind(e),
		}
		// Absent means unbranded: the client renders no provider mark rather
		// than a fallback glyph for an empty identity.
		if provider := activityProvider(e); provider != "" {
			row["provider"] = provider
		}
		out = append(out, row)
	}
	return out
}

// activityKind classifies a journal entry for the native client's subtitle and
// symbol mapping. Activation entries fall through to the client's default
// ("App routing updated.") — they bind a client to a source rather than
// changing a saved login, so the account/gateway/proxy/quota/settings kinds
// would all mistitle them.
func activityKind(e journal.Entry) string {
	t := e.Type
	switch {
	case strings.HasPrefix(t, "activate:"):
		return "activation"
	case strings.Contains(t, "gateway"):
		return "gateway"
	case strings.Contains(t, "proxy") || strings.Contains(t, "pool"):
		return "proxy"
	case strings.Contains(t, "quota"):
		return "quota"
	case strings.Contains(t, "setting") || strings.Contains(t, "config"):
		return "settings"
	default:
		return "activation"
	}
}

// activityProvider derives the brand identity for an event row from affected
// resources (L9): account refs carry "account/<provider>/<name>". Gateway,
// hub, and pool refs carry no provider, so those rows stay unbranded rather
// than guessing.
func activityProvider(e journal.Entry) string {
	for _, r := range e.AffectedResources {
		if rest, ok := strings.CutPrefix(r, "account/"); ok {
			if provider, _, found := strings.Cut(rest, "/"); found && provider != "" {
				return provider
			}
		}
	}
	for _, r := range e.AffectedResources {
		if provider, ok := strings.CutPrefix(r, "provider/"); ok && provider != "" {
			return provider
		}
	}
	return ""
}

func activityIsError(e journal.Entry) bool {
	return e.State == journal.StateFailed || e.State == journal.StateRolledBack
}

// activityMessage renders a human title from affected resources and terminal
// state. Affected entries use SerializeRef identities ("account/grok/a",
// "gateway/gw1") plus the "client/<id>" scope the executor records — plain
// display names, never secrets.
func activityMessage(e journal.Entry) string {
	client, source := splitAffected(e.AffectedResources)
	clientLabel := ""
	if client != "" {
		clientLabel = designAppName(client)
	}
	switch e.State {
	case journal.StateCommitted:
		if clientLabel != "" && source != "" {
			return clientLabel + " now uses " + source + "."
		}
		if clientLabel != "" {
			return clientLabel + " updated."
		}
		return "Operation completed."
	case journal.StateFailed:
		if clientLabel != "" {
			return clientLabel + " change failed."
		}
		return "Operation failed."
	case journal.StateRolledBack:
		if clientLabel != "" {
			return clientLabel + " change was rolled back."
		}
		return "Operation was rolled back."
	case journal.StateRollingBack:
		if clientLabel != "" {
			return clientLabel + " change is rolling back."
		}
		return "Operation is rolling back."
	default:
		if clientLabel != "" {
			return clientLabel + " change in progress."
		}
		return "Operation in progress."
	}
}

func splitAffected(affected []string) (client, source string) {
	for _, r := range affected {
		if c, ok := strings.CutPrefix(r, "client/"); ok {
			if client == "" {
				client = c
			}
			continue
		}
		if source == "" {
			source = displayAffected(r)
		}
	}
	return client, source
}

func displayAffected(r string) string {
	if name, ok := strings.CutPrefix(r, "account/"); ok {
		return name
	}
	if name, ok := strings.CutPrefix(r, "gateway/"); ok {
		return name
	}
	if name, ok := strings.CutPrefix(r, "hub/"); ok {
		return "hub:" + name
	}
	if name, ok := strings.CutPrefix(r, "pool/"); ok {
		return "pool:" + name
	}
	if name, ok := strings.CutPrefix(r, "preset/"); ok {
		return "@" + name
	}
	return r
}

// credentialKindRows maps a compatible definition's credential products to
// snapshot rows. Profiles with an empty label fall back to their id so the
// picker never shows a blank option.
func credentialKindRows(def *provider.CompatibleDefinition) []CredentialKindRow {
	if def == nil {
		return nil
	}
	out := make([]CredentialKindRow, 0, len(def.Credentials))
	for _, c := range def.Credentials {
		profiles := make([]CredentialProfileRow, 0, len(c.Profiles))
		for _, ep := range c.Profiles {
			label := ep.Label
			if label == "" {
				label = ep.ID
			}
			profiles = append(profiles, CredentialProfileRow{ID: ep.ID, Label: label})
		}
		label := c.Label
		if label == "" {
			label = c.Kind
		}
		out = append(out, CredentialKindRow{Kind: c.Kind, Label: label, Profiles: profiles})
	}
	return out
}

// logSourcesForProxies mirrors the proxies that can actually serve ProxyLogs.
// Each source id is the proxy id (providerID/accountName) so the logs command
// resolves to the same proxy.log file the engine tails.
func logSourcesForProxies(proxies []ProxyRow) []LogSourceRow {
	out := []LogSourceRow{}
	for _, p := range proxies {
		name := p.ID
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			continue
		}
		out = append(out, LogSourceRow{
			ID:         p.ID,
			ProviderID: p.ProviderID,
			Name:       name,
			Label:      p.Label,
			Detail:     p.Detail,
		})
	}
	return out
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
			if w.LimitWindowSeconds != nil {
				entry["limitWindowSeconds"] = *w.LimitWindowSeconds
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
	case "zcode":
		return "ZCode"
	case "dsh":
		return "DeepSeek Harness"
	default:
		return strings.ToUpper(client[:1]) + client[1:]
	}
}

// managedAppClientIDs mirrors Store.managedAppClientIDs: every clientId that
// appears on any action, first-occurrence order, plus the known apps so
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
	for _, id := range []string{"claude", "codex", "gemini", "kiro", "opencode", "workbuddy", "zcode"} {
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

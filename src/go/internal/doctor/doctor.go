// Package doctor ports src/core/doctor*.ts to the Go engine: read-only health
// checks over a single routing client (what the TUI's "d diagnose" runs for a
// highlighted app on the Apps board). It is a leaf package — it depends only on
// the service registries, never on app or tui, so cmd/anypick can reuse it too.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/presets"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

const (
	StatusOK        = "ok"
	StatusDegraded  = "degraded"
	StatusUnhealthy = "unhealthy"
	StatusNotSet    = "not-set"
)

// Finding is one check result. The shape mirrors cmd/anypick's doctor finding
// (check / status / detail) so a single renderer serves both.
type Finding struct {
	Check  string `json:"check"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail,omitempty"`
}

// EnvironmentReport contains only the redacted proxy environment state that
// is safe to display in a diagnostic report.
type EnvironmentReport struct {
	Proxy ProxyEnvironment `json:"proxy"`
}

type ProxyEnvironment struct {
	Present     []string `json:"present,omitempty"`
	HTTPProxy   string   `json:"httpProxy,omitempty"`
	HTTPSProxy  string   `json:"httpsProxy,omitempty"`
	AllProxy    string   `json:"allProxy,omitempty"`
	NoProxySet  bool     `json:"noProxySet"`
	NoProxySize int      `json:"noProxyEntries,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Report is the per-client diagnosis shown by the TUI.
type Report struct {
	ClientID string    `json:"clientId"`
	Status   string    `json:"status"`
	Findings []Finding `json:"findings"`
}

// Deps is the minimal slice of the app the doctor needs. The TUI builds it from
// *app.App; cmd/anypick can build its own.
type Deps struct {
	Clients   *client.Registry
	Providers *provider.Registry
	Bindings  *bindings.Service
	Store     *store.AccountStore
	Profiles  *profile.Service
	Presets   *presets.Service
	Accounts  *accounts.Service
	Proxy     *proxy.Service
}

// DiagnoseClient answers "why is this app's route not healthy?" for one routing
// client. It never mutates anything (read-only, like the CLI doctor).
func DiagnoseClient(ctx context.Context, deps Deps, clientID string) (*Report, error) {
	rep := &Report{ClientID: clientID, Status: StatusOK}
	add := func(check, status, detail string) {
		rep.Findings = append(rep.Findings, Finding{Check: check, Status: status, Detail: detail})
	}

	if cad, err := deps.Clients.Get(clientID); err != nil {
		add("client", "fail", "unknown client "+clientID)
	} else if insp, ierr := cad.Inspect(); ierr == nil && !insp.Present {
		detail := cad.Name() + " not installed"
		if len(insp.Issues) > 0 {
			detail = cad.Name() + ": " + joinIssues(insp.Issues)
		}
		add("client", "warn", detail)
	} else {
		add("client", "ok", cad.Name()+" installed")
	}

	routeSet := false
	b, berr := deps.Bindings.GetGlobalContext(ctx, clientID)
	switch {
	case berr != nil:
		add("route", "fail", "binding lookup failed: "+berr.Error())
	case b == nil || b.Spec.Source.Kind == "":
		add("route", "warn", "no route set — press enter to connect")
	default:
		routeSet = true
		ref := b.Spec.Source
		if ok, missing := sourceExists(ctx, deps, ref); !ok {
			add("route", "fail", "route source missing: "+missing)
		} else {
			add("route", "ok", "route "+refs.DisplayRef(ref))
			checkProxy(ctx, deps, add, ref)
		}
	}

	// Roll the per-finding statuses up to one report status.
	status := StatusOK
	for _, f := range rep.Findings {
		if f.Status == "fail" {
			status = StatusUnhealthy
		} else if f.Status == "warn" && status == StatusOK {
			status = StatusDegraded
		}
	}
	if !routeSet && status != StatusUnhealthy {
		// No route means there is nothing further to diagnose, distinct from a
		// degraded-but-working route.
		status = StatusNotSet
	}
	rep.Status = status
	return rep, nil
}

func checkProxy(ctx context.Context, deps Deps, add func(string, string, string), ref refs.Ref) {
	var prov, name string
	switch ref.Kind {
	case refs.KindGateway:
		if pr, err := deps.Profiles.GetContext(ctx, ref.Name); err == nil && pr != nil {
			prov, name = pr.Meta.Provider, ref.Name
		} else {
			return
		}
	case refs.KindAccount, refs.KindAccountPool:
		prov, name = ref.Provider, ref.Name
	default:
		return
	}
	if prov == "" {
		return
	}
	st, err := deps.Proxy.StatusContext(ctx, prov, name)
	if err != nil {
		return
	}
	switch {
	case st.Running:
		add("proxy", "ok", prov+"/"+name+" running")
	case st.Enabled:
		add("proxy", "warn", prov+"/"+name+" enabled but not running")
	}
}

func sourceExists(ctx context.Context, deps Deps, ref refs.Ref) (bool, string) {
	switch ref.Kind {
	case refs.KindGateway:
		if _, err := deps.Profiles.GetContext(ctx, ref.Name); err != nil {
			return false, "gateway " + ref.Name
		}
		return true, ""
	case refs.KindAccount, refs.KindAccountPool:
		if _, err := deps.Store.Get(ctx, ref.Provider, ref.Name); err != nil {
			return false, ref.Provider + "/" + ref.Name
		}
		return true, ""
	case refs.KindPreset:
		saved, err := deps.Presets.ListContext(ctx)
		if err != nil {
			return false, "preset " + ref.Name
		}
		for _, p := range saved {
			if p.Name == ref.Name {
				return true, ""
			}
		}
		return false, "preset " + ref.Name
	}
	return true, ""
}

func joinIssues(issues []string) string {
	out := ""
	for i, s := range issues {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// InstallationReport is a read-only health report for the local AnyPick data
// root. The CLI and desktop settings pane share this implementation.
type InstallationReport struct {
	Status      string              `json:"status"`
	Findings    []Finding           `json:"findings"`
	Plugins     plugins.Diagnostics `json:"plugins"`
	Environment EnvironmentReport   `json:"environment"`
}

// InstallationDeps is the service slice required to diagnose one data root.
type InstallationDeps struct {
	Root              string
	Providers         *provider.Registry
	Clients           *client.Registry
	Accounts          *accounts.Service
	Bindings          *bindings.Service
	Proxy             *proxy.Service
	Environment       EnvironmentReport
	PluginDiagnostics func(context.Context) plugins.Diagnostics
}

// DiagnoseInstallation checks persistent references and local service state.
// It never writes or repairs anything; callers can present specific repair
// actions once the user has reviewed the findings.
func DiagnoseInstallation(ctx context.Context, deps InstallationDeps) InstallationReport {
	report := InstallationReport{Findings: []Finding{}}
	if deps.PluginDiagnostics != nil {
		report.Plugins = deps.PluginDiagnostics(ctx)
	}
	report.Environment = deps.Environment
	environmentStatus := StatusOK
	if len(report.Environment.Proxy.Warnings) > 0 {
		environmentStatus = "warn"
	}
	add := func(check, status, detail string) {
		report.Findings = append(report.Findings, Finding{Check: check, Status: status, Detail: detail})
	}
	add("proxy-environment", environmentStatus, proxyEnvironmentDetail(report.Environment))

	if _, err := os.Stat(filepath.Join(deps.Root, "anypick.db")); err != nil {
		add("database", "fail", "anypick.db missing under data root")
	} else {
		add("database", StatusOK, "")
	}

	if deps.Providers != nil && deps.Accounts != nil {
		// Active pointers must reference existing accounts.
		for _, providerID := range deps.Providers.IDs() {
			active, _ := deps.Accounts.Active(ctx, providerID)
			if active == "" {
				continue
			}
			acct, err := deps.Accounts.Get(ctx, providerID, active)
			if err != nil || acct == nil {
				add("active-binding", "warn", fmt.Sprintf("%s points at missing account %q", providerID, active))
			} else {
				add("active-binding", StatusOK, providerID+"/"+active)
			}
		}

		// Enabled proxies should have persisted state or be restartable.
		if deps.Proxy != nil {
			for _, providerID := range deps.Providers.IDs() {
				list, err := deps.Accounts.List(ctx, providerID)
				if err != nil {
					continue
				}
				for _, acct := range list {
					cfg, _ := deps.Proxy.ProxyConfig(ctx, providerID, acct.Meta.Name)
					if !cfg.Enabled {
						continue
					}
					st, err := deps.Proxy.StatusContext(ctx, providerID, acct.Meta.Name)
					if err == nil && st.Running {
						add("proxy", StatusOK, fmt.Sprintf("%s/%s running", providerID, acct.Meta.Name))
					} else {
						add("proxy", "warn", fmt.Sprintf("%s/%s enabled but not running", providerID, acct.Meta.Name))
					}
				}
			}
		}
	}

	if deps.Clients != nil && deps.Bindings != nil {
		boundClients := map[string][]string{}
		for _, clientID := range deps.Clients.IDs() {
			binding, err := deps.Bindings.GetGlobalContext(ctx, clientID)
			if err != nil || binding == nil || binding.Spec.Source.Kind != refs.KindAccount {
				continue
			}
			ref := binding.Spec.Source.Provider + "/" + binding.Spec.Source.Name
			boundClients[ref] = append(boundClients[ref], clientID)
		}
		if deps.Accounts != nil {
			groups, err := deps.Accounts.DuplicateSnapshotGroups(ctx)
			for _, group := range groups {
				keepRef := group.Provider + "/" + group.Keep
				refsInGroup := make([]string, 0, len(group.Names))
				inUse := make([]string, 0, len(group.Names))
				removable := make([]string, 0, len(group.Names))
				for _, name := range group.Names {
					ref := group.Provider + "/" + name
					refsInGroup = append(refsInGroup, ref)
					clients := boundClients[ref]
					switch {
					case ref == keepRef:
						inUse = append(inUse, ref)
					case len(clients) > 0:
						inUse = append(inUse, ref+" (bound to "+strings.Join(clients, ", ")+")")
					default:
						removable = append(removable, ref)
					}
				}
				detail := fmt.Sprintf("%s share one snapshot", strings.Join(refsInGroup, ", "))
				if len(inUse) > 1 {
					detail += fmt.Sprintf(" — in use: %s; re-bind clients to one account before removing the rest", strings.Join(inUse, ", "))
				} else {
					detail += " — keep " + keepRef
					for _, ref := range removable {
						detail += "; anypick account remove " + ref
					}
				}
				add("duplicate-snapshot", "warn", detail)
			}
			if err != nil {
				add("duplicate-snapshot", "warn", "snapshot audit incomplete: "+err.Error())
			}
		}
	}

	if report.Plugins.Host.LoadedPlugins > 0 || report.Plugins.Host.State == "degraded" {
		pluginStatus := StatusOK
		if report.Plugins.Host.State != "running" {
			pluginStatus = "warn"
		}
		detail := fmt.Sprintf("%d loaded; host %s", report.Plugins.Host.LoadedPlugins, report.Plugins.Host.State)
		if report.Plugins.Host.Error != "" {
			detail += ": " + report.Plugins.Host.Error
		}
		add("plugin-host", pluginStatus, detail)
	}
	for _, failure := range report.Plugins.Failures {
		add("plugin:"+failure.Name, "warn", failure.Reason)
	}

	report.Status = StatusOK
	for _, finding := range report.Findings {
		if finding.Status == "warn" && report.Status == StatusOK {
			report.Status = StatusDegraded
		}
		if finding.Status == "fail" {
			report.Status = StatusUnhealthy
		}
	}
	return report
}

func proxyEnvironmentDetail(report EnvironmentReport) string {
	proxy := report.Proxy
	if len(proxy.Present) == 0 {
		return "no proxy environment variables configured"
	}
	parts := make([]string, 0, 5)
	if proxy.HTTPProxy != "" {
		parts = append(parts, "HTTP_PROXY="+proxy.HTTPProxy)
	}
	if proxy.HTTPSProxy != "" {
		parts = append(parts, "HTTPS_PROXY="+proxy.HTTPSProxy)
	}
	if proxy.AllProxy != "" {
		parts = append(parts, "ALL_PROXY="+proxy.AllProxy)
	}
	if proxy.NoProxySet {
		parts = append(parts, fmt.Sprintf("NO_PROXY=%d entries", proxy.NoProxySize))
	}
	if len(parts) == 0 {
		parts = append(parts, "proxy variables are empty")
	}
	if len(proxy.Warnings) > 0 {
		parts = append(parts, "warnings: "+strings.Join(proxy.Warnings, "; "))
	}
	return strings.Join(parts, ", ")
}

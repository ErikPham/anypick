// Package doctor ports src/core/doctor*.ts to the Go engine: read-only health
// checks over a single routing client (what the TUI's "d diagnose" runs for a
// highlighted app on the Apps board). It is a leaf package — it depends only on
// the service registries, never on app or tui, so cmd/anypick can reuse it too.
package doctor

import (
	"context"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
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
	Bindings  *bindings.Store
	Store     *store.AccountStore
	Profiles  *profile.Store
	Presets   *presets.Store
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

	// 1. The client app must actually be installed.
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

	// 2. The route (global binding) and whether its source still resolves.
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

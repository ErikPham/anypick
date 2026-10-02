// Package agentbridge exposes the small, read-only bridge surface that is
// useful to local coordinators. AnyPick remains the owner of credentials and
// proxy lifecycles; this package only describes what the current installation
// can serve and whether a saved route is healthy.
package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

// ManifestVersion is the version of the redacted list-agents contract. It is
// deliberately independent from the desktop protocol version: a coordinator
// can consume this endpoint without speaking the native app protocol.
const ManifestVersion = 1

// Manifest is the safe, read-only worker inventory a local coordinator needs.
// It contains no account credentials, proxy tokens, gateway endpoints, or
// filesystem paths.
type Manifest struct {
	Version int     `json:"version"`
	Agents  []Agent `json:"agents"`
}

// Agent describes one installed coding client. A client is the worker from a
// coordinator's point of view; the route below is the AnyPick-owned way to
// reach it.
type Agent struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"` // ready | degraded | unconfigured | unavailable
	Detail       string            `json:"detail,omitempty"`
	Capabilities AgentCapabilities `json:"capabilities"`
	Route        *Route            `json:"route,omitempty"`
}

// AgentCapabilities contains only non-secret adapter metadata.
type AgentCapabilities struct {
	Protocols          []string `json:"protocols,omitempty"`
	EnvironmentOverlay bool     `json:"environmentOverlay"`
	IsolatedHome       bool     `json:"isolatedHome"`
	PersistentConfig   bool     `json:"persistentConfig"`
}

// Route is the resolved, redacted binding for an agent. Capability and
// protocol are calculated by activation.PlanActivation so this endpoint does
// not grow a second provider/client compatibility matrix.
type Route struct {
	Source     string       `json:"source"`
	SourceKind string       `json:"sourceKind"`
	Model      string       `json:"model,omitempty"`
	Capability string       `json:"capability"`
	Protocol   string       `json:"protocol,omitempty"`
	Ready      bool         `json:"ready"`
	Error      string       `json:"error,omitempty"`
	Proxy      *ProxyHealth `json:"proxy,omitempty"`
}

// ProxyHealth is intentionally state-only. Endpoint and token material are
// not useful to a coordinator until AnyPick has activated the client and are
// therefore kept behind the native activation boundary.
type ProxyHealth struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
}

// Deps is the narrow read-only bridge composition. Activation remains the
// authority for resolving source/provider/client compatibility.
type Deps struct {
	Activation *activation.Deps
	Clients    *client.Registry
	Bindings   *bindings.Service
	Proxy      *proxy.Service
}

// Build returns a deterministic manifest for the current installation.
// Missing or malformed bindings degrade one agent rather than hiding all
// other workers from a coordinator.
func Build(ctx context.Context, deps Deps) (Manifest, error) {
	if deps.Clients == nil {
		return Manifest{}, fmt.Errorf("agent manifest requires a client registry")
	}

	ids := deps.Clients.IDs()
	sort.Strings(ids)
	out := Manifest{Version: ManifestVersion, Agents: make([]Agent, 0, len(ids))}
	for _, id := range ids {
		adapter, err := deps.Clients.Get(id)
		if err != nil {
			continue
		}
		caps := adapter.Capabilities()
		agent := Agent{
			ID:     id,
			Name:   adapter.Name(),
			Status: "unavailable",
			Capabilities: AgentCapabilities{
				Protocols:          protocolStrings(caps),
				EnvironmentOverlay: caps != nil && caps.SupportsEnvironmentOverlay,
				IsolatedHome:       caps != nil && caps.SupportsIsolatedHome,
				PersistentConfig:   caps != nil && caps.SupportsPersistentConfig,
			},
		}

		if inspection, inspectErr := adapter.Inspect(); inspectErr == nil && inspection != nil {
			if inspection.Present {
				agent.Status = "unconfigured"
			} else if len(inspection.Issues) > 0 {
				// Adapter issue text can contain a native config path. Keep the
				// bridge contract path-free and leave detailed inspection to the
				// native app/doctor surfaces.
				agent.Detail = "client inspection reported a problem"
			}
		} else if inspectErr != nil {
			agent.Detail = "inspection failed"
		}

		route, routeErr := buildRoute(ctx, deps, id)
		if routeErr != nil {
			agent.Status = "degraded"
			agent.Detail = routeErr.Error()
		} else if route == nil {
			if agent.Detail == "" {
				agent.Detail = "no route configured"
			}
		} else {
			agent.Route = route
			if route.Error != "" {
				agent.Detail = route.Error
			}
			switch {
			case !route.Ready:
				agent.Status = "degraded"
			case route.Proxy != nil && !route.Proxy.Running && route.Capability != string(transport.Direct):
				agent.Status = "degraded"
				agent.Detail = "route is configured but its proxy is not running"
			default:
				agent.Status = "ready"
			}
		}
		out.Agents = append(out.Agents, agent)
	}
	return out, nil
}

func protocolStrings(caps *client.Capabilities) []string {
	if caps == nil {
		return nil
	}
	out := make([]string, 0, len(caps.AcceptedProtocols))
	for _, protocol := range caps.AcceptedProtocols {
		out = append(out, string(protocol))
	}
	sort.Strings(out)
	return out
}

func buildRoute(ctx context.Context, deps Deps, clientID string) (*Route, error) {
	if deps.Bindings == nil {
		return nil, nil
	}
	binding, err := deps.Bindings.GetGlobalContext(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("binding lookup failed")
	}
	if binding == nil || binding.Spec.Source.Kind == "" {
		return nil, nil
	}

	route := &Route{
		Source:     refs.DisplayRef(binding.Spec.Source),
		SourceKind: string(binding.Spec.Source.Kind),
	}
	if binding.Spec.Model.Mode == "explicit" {
		route.Model = binding.Spec.Model.ID
	}
	if deps.Activation == nil {
		route.Error = "activation resolver unavailable"
		return route, nil
	}

	plan, err := activation.PlanActivation(ctx, activation.Request{
		Mode:            activation.ModePersistent,
		Client:          clientID,
		Source:          &binding.Spec.Source,
		Model:           route.Model,
		TransportPolicy: transport.Policy(binding.Spec.TransportPolicy),
	}, deps.Activation, activation.Options{})
	if err != nil {
		route.Error = diagnosticError(err)
		return route, nil
	}
	route.Capability = string(plan.Transport.Capability)
	route.Protocol = string(plan.Transport.Protocol)
	route.Ready = plan.Transport.Capability != transport.Unsupported

	if binding.Spec.Source.Kind == refs.KindAccount && deps.Proxy != nil {
		status, statusErr := deps.Proxy.StatusContext(ctx, binding.Spec.Source.Provider, binding.Spec.Source.Name)
		if statusErr == nil && status != nil && requiresManagedProxy(plan.Transport.Capability) {
			route.Proxy = &ProxyHealth{Enabled: status.Enabled, Running: status.Running}
		}
	}
	return route, nil
}

func diagnosticError(err error) string {
	var anyPickErr *anyerr.Error
	if errors.As(err, &anyPickErr) && anyPickErr.Code != "" {
		return anyPickErr.Code
	}
	return "route resolution failed"
}

func requiresManagedProxy(capability transport.Capability) bool {
	return capability == transport.ManagedBuiltinProxy || capability == transport.ManagedExternalProxy
}

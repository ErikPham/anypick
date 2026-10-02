package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
)

// snapshotUpstreamResolver resolves forwarding targets from account
// snapshots through provider-owned capabilities. Gateway profiles created via
// gateway-create carry no account snapshot, so the provider is handed the
// profile credential in memory instead.
type snapshotUpstreamResolver struct {
	store     *store.AccountStore
	providers *provider.Registry
	profiles  *profile.Store
}

// ResolveUpstream finds the egress target for one saved account.
func (r *snapshotUpstreamResolver) ResolveUpstream(ctx context.Context, providerID, accountName string) (*proxy.Upstream, error) {
	pv, err := r.providers.Get(providerID)
	if err != nil {
		return nil, err
	}
	acct, err := r.store.Get(context.Background(), providerID, accountName)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		// Gateway profiles (gateway-create) hold the credential in the profile
		// store, not an account snapshot. A provider that can serve a gateway
		// from a profile key implements GatewayUpstreamWithKey and receives it
		// in memory.
		//
		// The key is deliberately never written to a snapshot .env: this runs
		// once per request, so a cleartext write here races a concurrent
		// `anypick backup` with a half-written file, and a stale copy keeps
		// resolving after the key is rotated or deleted (invariants #2 and #4).
		if r.profiles != nil {
			if pr, perr := r.profiles.GetContext(ctx, accountName); perr == nil &&
				pr != nil && pr.Meta.Provider == providerID && pr.Secrets.APIKey != "" {
				// Providers with a per-account endpoint (Cloudflare AI
				// Gateway) resolve from the stored endpoint plus the profile
				// headers. The header map stays generic here; the provider
				// maps its own keys onto wire headers.
				if gw, ok := pv.(interface {
					GatewayUpstreamWithEndpoint(endpoint, apiKey string, headers map[string]string) (*proxy.Upstream, error)
				}); ok && pr.Meta.Endpoint != "" {
					if up, uerr := gw.GatewayUpstreamWithEndpoint(pr.Meta.Endpoint, pr.Secrets.APIKey, pr.Secrets.Headers); uerr == nil {
						return withCatalogScope(up, providerID, accountName), nil
					}
				}
				if gw, ok := pv.(interface {
					GatewayUpstreamWithKey(apiKey string) (*proxy.Upstream, error)
				}); ok {
					if up, uerr := gw.GatewayUpstreamWithKey(pr.Secrets.APIKey); uerr == nil {
						return withCatalogScope(up, providerID, accountName), nil
					}
				}
			}
		}
		// Providers with a live-session gateway own how that session becomes
		// an upstream. The resolver only dispatches the optional capability.
		if gp, ok := pv.(interface {
			GatewayUpstream() (*proxy.Upstream, error)
		}); ok {
			if up, uerr := gp.GatewayUpstream(); uerr == nil {
				return withCatalogScope(up, providerID, accountName), nil
			}
		}
		return nil, fmt.Errorf("no snapshot for %s/%s", providerID, accountName)
	}

	if compat, ok := pv.(interface {
		ResolveUpstream(snapshotDir string) (*proxy.Upstream, error)
	}); ok {
		up, uerr := compat.ResolveUpstream(acct.SnapshotDir)
		if uerr != nil {
			return nil, uerr
		}
		return withCatalogScope(up, providerID, accountName), nil
	}

	return nil, fmt.Errorf("provider %q does not expose a snapshot upstream", providerID)
}

func withCatalogScope(up *proxy.Upstream, providerID, accountName string) *proxy.Upstream {
	if up == nil || strings.TrimSpace(up.CatalogScope) != "" {
		return up
	}
	up.CatalogScope = proxy.AccountCatalogScope(providerID, accountName)
	return up
}

func catalogEntriesFrom(entries map[string]catalog.Provider) map[string]activation.CatalogEntry {
	out := make(map[string]activation.CatalogEntry, 8)
	for id, p := range entries {
		out[id] = activation.CatalogEntry{Name: p.Name, APIStyle: string(p.APIStyle), Protocols: p.Protocols}
	}
	return out
}

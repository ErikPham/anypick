package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
)

// snapshotUpstreamResolver resolves forwarding targets from account
// snapshots: compatible vendors use their saved endpoint profile + scheme;
// codex uses the ChatGPT backend with the snapshot's OAuth token. Gateway
// profiles created via gateway-create carry no account snapshot, so the
// provider is handed the profile credential in memory instead.
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
		if gw, ok := pv.(interface {
			GatewayUpstreamWithKey(apiKey string) (*proxy.Upstream, error)
		}); ok && r.profiles != nil {
			if pr, perr := r.profiles.GetContext(ctx, accountName); perr == nil &&
				pr != nil && pr.Meta.Provider == providerID && pr.Secrets.APIKey != "" {
				if up, uerr := gw.GatewayUpstreamWithKey(pr.Secrets.APIKey); uerr == nil {
					return up, nil
				}
			}
		}
		// grok gateways reuse the live x.ai OIDC session (~/.grok/auth.json)
		// shared with the account flow; no API key, no account snapshot.
		if providerID == "grok" {
			if gp, ok := pv.(interface {
				GatewayUpstream() (*proxy.Upstream, error)
			}); ok {
				if up, uerr := gp.GatewayUpstream(); uerr == nil {
					return up, nil
				}
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
		return up, nil
	}

	if providerID == "codex" {
		data, rerr := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
		if rerr != nil {
			return nil, fmt.Errorf("read snapshot auth.json: %w", rerr)
		}
		var auth struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
			} `json:"tokens"`
		}
		if json.Unmarshal(data, &auth) != nil || auth.Tokens.AccessToken == "" {
			return nil, fmt.Errorf("snapshot has no OAuth access token")
		}
		base, _ := url.Parse("https://chatgpt.com/backend-api")
		return &proxy.Upstream{BaseURL: base, Auth: auth.Tokens.AccessToken}, nil
	}
	return nil, fmt.Errorf("no builtin upstream for provider %q", providerID)
}

func catalogEntriesFrom(entries map[string]catalog.Provider) map[string]activation.CatalogEntry {
	out := make(map[string]activation.CatalogEntry, 8)
	for id, p := range entries {
		out[id] = activation.CatalogEntry{Name: p.Name, APIStyle: string(p.APIStyle), Protocols: p.Protocols}
	}
	return out
}

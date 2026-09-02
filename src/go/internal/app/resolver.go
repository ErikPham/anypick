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
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
)

// snapshotUpstreamResolver resolves forwarding targets from account
// snapshots: compatible vendors use their saved endpoint profile + scheme;
// codex uses the ChatGPT backend with the snapshot's OAuth token. Gateway
// profiles created via gateway-create carry no account snapshot, so for
// ResolveUpstream providers (gemini) it seeds one from the saved profile key.
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
	acct, err := r.store.GetContext(context.Background(), providerID, accountName)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		// Gateway profiles (gateway-create) hold the credential in the profile
		// store, not an account snapshot. For gemini — which serves the Google
		// Gemini API through the engine's OpenAI-compatible translation — seed a
		// snapshot .env from the profile key so ResolveUpstream resolves it.
		if providerID == "gemini" && r.profiles != nil {
			if pr, perr := r.profiles.GetContext(ctx, accountName); perr == nil &&
				pr != nil && pr.Meta.Provider == "gemini" && pr.Secrets.APIKey != "" {
				snapDir := paths.AccountSnapshotDir(r.store.Root, "gemini", accountName)
				if mkErr := os.MkdirAll(snapDir, 0o700); mkErr == nil {
					if wErr := os.WriteFile(filepath.Join(snapDir, ".env"),
						[]byte("GEMINI_API_KEY="+pr.Secrets.APIKey+"\n"), 0o600); wErr == nil {
						if up, uerr := pv.(interface {
							ResolveUpstream(snapshotDir string) (*proxy.Upstream, error)
						}).ResolveUpstream(snapDir); uerr == nil {
							return up, nil
						}
					}
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

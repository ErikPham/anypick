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
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
)

// snapshotUpstreamResolver resolves forwarding targets from account
// snapshots: compatible vendors use their saved endpoint profile + scheme;
// codex uses the ChatGPT backend with the snapshot's OAuth token.
type snapshotUpstreamResolver struct {
	store     *store.AccountStore
	providers *provider.Registry
}

// ResolveUpstream finds the egress target for one saved account.
func (r *snapshotUpstreamResolver) ResolveUpstream(_ context.Context, providerID, accountName string) (*proxy.Upstream, error) {
	pv, err := r.providers.Get(providerID)
	if err != nil {
		return nil, err
	}
	acct, err := r.store.GetContext(context.Background(), providerID, accountName)
	if err != nil {
		return nil, err
	}
	if acct == nil {
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

// catalogEntries maps the built-in catalog into the activation layer's
// classification input.
func catalogEntries() map[string]activation.CatalogEntry {
	return catalogEntriesFrom(catalog.Builtin())
}

func catalogEntriesFrom(entries map[string]catalog.Provider) map[string]activation.CatalogEntry {
	out := make(map[string]activation.CatalogEntry, 8)
	for id, p := range entries {
		out[id] = activation.CatalogEntry{Name: p.Name, APIStyle: string(p.APIStyle), Protocols: p.Protocols}
	}
	return out
}

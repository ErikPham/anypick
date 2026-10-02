package provider

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/proxy"
)

// ResolveUpstream keeps Codex's snapshot-specific upstream policy with the
// provider. The app resolver should orchestrate capabilities, not know the
// shape of a provider's auth.json or its upstream endpoint.
func (p *codex) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	data, err := os.ReadFile(filepath.Join(snapshotDir, "auth.json"))
	if err != nil {
		return nil, fmt.Errorf("read snapshot auth.json: %w", err)
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

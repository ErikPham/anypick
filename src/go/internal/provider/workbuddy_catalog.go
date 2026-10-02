package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const workbuddyProductCatalogFile = "acc-product-config-v3.json"

const workbuddyCustomLocalModelPrefix = "custom-local:"

// workbuddyModelDiscovery reads the product catalogue maintained by the
// WorkBuddy/CodeBuddy desktop app. The file is deliberately read at discovery
// time: model entitlements and preview ids change without an AnyPick release.
// It contains product metadata only, never session credentials.
func workbuddyModelDiscovery(home string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		root := expandHome(home)
		if root == "" {
			root, _ = os.UserHomeDir()
		}
		var lastErr error
		for _, path := range workbuddyCatalogPaths(root) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			models, err := readWorkBuddyProductCatalog(path)
			if err == nil && len(models) > 0 {
				return models, nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				lastErr = err
			}
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("WorkBuddy product model catalog not found")
	}
}

func workbuddyRouterModelID(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "default-model", "fast-model", "balanced-model", "primary-model", "deep-model", "auto-chat":
		return true
	}
	return false
}

func workbuddyCatalogPaths(home string) []string {
	dirs := []string{".workbuddy-ai", ".workbuddy", ".codebuddy"}
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(home, dir, "cache", workbuddyProductCatalogFile))
	}
	return paths
}

// workbuddyProductEndpoint reads the chat endpoint the desktop app resolved
// for this installation (ACC_PRODUCT_CONFIG_V3's `endpoint`). The app writes
// the file for its own use; reading it keeps the proxy on the same edge the
// app itself calls — WorkBuddy's app calls www.workbuddy.ai even when the
// device-flow token was issued by www.codebuddy.ai, and the two edges do not
// behave identically under load.
func workbuddyProductEndpoint(home string) string {
	root := expandHome(home)
	if root == "" {
		root, _ = os.UserHomeDir()
	}
	for _, path := range workbuddyCatalogPaths(root) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc struct {
			Endpoint string `json:"endpoint"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		endpoint := strings.TrimSuffix(strings.TrimSpace(doc.Endpoint), "/")
		if u, uerr := url.Parse(endpoint); uerr == nil && u.Scheme == "https" && u.Hostname() != "" {
			return endpoint
		}
	}
	return ""
}

// workbuddyChinaOrigin reports whether a credential origin belongs to the China
// deployment. Its tokens are issued for copilot.tencent.com / codebuddy.cn and
// are rejected by the international product endpoint, so those accounts keep
// their own host.
func workbuddyChinaOrigin(origin string) bool {
	host := strings.ToLower(strings.TrimSpace(origin))
	if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
		host = strings.ToLower(u.Hostname())
	}
	return strings.Contains(host, "copilot.tencent.com") || strings.HasSuffix(host, "codebuddy.cn")
}

func readWorkBuddyProductCatalog(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode WorkBuddy product model catalog: %w", err)
	}
	models := make([]string, 0, len(doc.Models))
	seen := make(map[string]bool, len(doc.Models))
	for _, model := range doc.Models {
		id := strings.TrimSpace(model.ID)
		// WorkBuddy persists the merged custom-model catalog in this cache and
		// namespaces custom entries as custom-local:<id>. Those entries are
		// downstream aliases created by the client, not models the subscription
		// account serves. Feeding them back into AnyPick would make the next
		// activation publish every model twice.
		//
		// The UI tier aliases (fast-model, default-model, …) are thinking-only
		// routers, not slugs a borrowing client can call. Publishing them as
		// custom-local models is what collapses the WorkBuddy agent into a
		// "Let me write." / "OK." loop.
		if id != "" && !strings.HasPrefix(id, workbuddyCustomLocalModelPrefix) &&
			!workbuddyRouterModelID(id) && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	return models, nil
}

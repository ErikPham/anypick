package provider

import (
	"sort"
	"strings"
)

// Saved-mirror model catalogs for Zen/Go platform keys.
//
// An opencode account snapshots the whole auth.json, so one account can hold
// several platform entries (a Zen key and a Go subscription side by side).
// Catalogs are therefore computed per mirror, not per provider id: the
// static union below is safe on every snapshot tick (local read, verified
// definition data, no egress, no secrets), while live models warm the cache
// explicitly on hub start.

// OpenCodeSnapshotServices lists platform services with api entries in a
// saved auth.json mirror, in deterministic order. Best-effort: a missing or
// unreadable mirror simply contributes nothing.
func OpenCodeSnapshotServices(snapshotDir string) []string {
	keys, _ := openCodeSnapshotKeys(snapshotDir)
	services := make([]string, 0, len(keys))
	for _, k := range keys {
		if OpenCodeServiceEndpoint(k.Service) != "" {
			services = append(services, k.Service)
		}
	}
	return services
}

// OpenCodeSnapshotCatalog returns the static model union for a saved
// mirror's platform entries, sourced from the matching opencode-zen /
// opencode-go Compatible definitions (verified in-repo data, never
// invented). Sorted and deduplicated for stable snapshots.
func OpenCodeSnapshotCatalog(snapshotDir string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, service := range OpenCodeSnapshotServices(snapshotDir) {
		for _, id := range openCodeDefinitionModels(service) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

// OpenCodeMirrorKeys maps service→key for api entries in a saved mirror.
// Server-side only: keys feed live model fetches and cache writes during
// explicit warming; only model ids ever leave Go.
func OpenCodeMirrorKeys(snapshotDir string) map[string]string {
	keys, _ := openCodeSnapshotKeys(snapshotDir)
	out := map[string]string{}
	for _, k := range keys {
		if OpenCodeServiceEndpoint(k.Service) == "" || k.Key == "" {
			continue
		}
		if _, dup := out[k.Service]; !dup {
			out[k.Service] = k.Key
		}
	}
	return out
}

// openCodeDefinitionModels returns the verified static model list for a
// platform service from its Compatible definition. Unknown services yield
// nothing: no endpoint, no list, no fabrication.
func openCodeDefinitionModels(service string) []string {
	id := "opencode-zen"
	if strings.EqualFold(service, "opencode-go") {
		id = "opencode-go"
	}
	for _, def := range BuiltinDefinitions() {
		if def.ID == id {
			return append([]string{}, def.Models...)
		}
	}
	return nil
}

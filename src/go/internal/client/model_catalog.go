package client

import "strings"

// sourceModelIDs is the adapter-facing model list for one exact source. The
// catalog is authoritative and selections are appended only when they are no
// longer present, so a stale binding remains selectable without inventing a
// provider-wide fallback.
func sourceModelIDs(ctx *ApplyContext) []string {
	if ctx == nil {
		return nil
	}
	ids := make([]string, 0, len(ctx.ModelCatalog)+len(ctx.ModelRoles)+1)
	seen := map[string]bool{}
	push := func(raw string) {
		id := trimModelID(raw)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range ctx.ModelCatalog {
		push(id)
	}
	push(selectedDefaultModel(ctx))
	for _, role := range sortedKeys(ctx.ModelRoles) {
		if role != "default" {
			push(ctx.ModelRoles[role])
		}
	}
	return ids
}

func selectedDefaultModel(ctx *ApplyContext) string {
	if ctx == nil {
		return ""
	}
	return trimModelID(firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel))
}

func trimModelID(raw string) string {
	return strings.TrimSpace(raw)
}

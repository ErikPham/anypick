package desktop

import (
	"context"
	"sort"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/refs"
)

// maxModelOptions caps one connection's published model list. The desktop
// picker is a human list, not a catalog dump; the configured models are
// re-added after the cap so a pinned model never becomes unselectable.
const maxModelOptions = 60

// modelRoleTarget is the registry entry behind one published model option.
//
// The native host only ever sees the opaque action id: source refs and model
// ids stay engine-side, and resolution happens at apply time against the
// snapshot that published them (the Go counterpart of
// src/tray/model-role-actions.ts).
type modelRoleTarget struct {
	ClientID string
	Source   refs.Ref
	ModelID  string
}

// clientModelConfigs fills Snapshot.ClientModelConfigs — one row per app:
// the roles it can configure, the roles it currently has, and the model ids
// this connection can serve. It mirrors
// src/tray/snapshot-client-routes.ts (the `modelConfig` block).
//
// Rows are published for every app, including unbound ones: the macOS Models
// section reads `roles` to decide whether to render at all, and an unbound
// app still owes the user an explanation (`unavailableReason`).
func (b *snapshotBuilder) clientModelConfigs(ctx context.Context, appIDs []string) {
	for _, clientID := range appIDs {
		roles := client.RolesFor(clientID)
		roleRows := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			roleRows = append(roleRows, map[string]any{"id": role.ID, "label": role.Label})
		}
		row := map[string]any{
			"clientId":   clientID,
			"client":     designAppName(clientID),
			"roles":      roleRows,
			"modelRoles": map[string]string{},
			"options":    []map[string]any{},
			"editable":   false,
		}

		global, err := b.bindingFor(ctx, clientID)
		if err != nil || global == nil {
			row["unavailableReason"] = "Choose a proxy or gateway route first."
			b.snap.ClientModelConfigs = append(b.snap.ClientModelConfigs, row)
			continue
		}

		spec := global.Spec
		source := spec.Source
		row["sourceLabel"] = refs.DisplayRef(source)

		// Stored roles win, filtered to roles this client declares: a role
		// left over from another client must never be re-applied.
		allowed := client.AllowedRoleIDs(clientID)
		current := map[string]string{}
		for id, model := range modelRolesFromSpec(spec) {
			if allowed[id] && model != "" {
				current[id] = model
			}
		}
		if primary := primaryModelOf(spec); primary != "" {
			row["defaultModel"] = primary
			if current["default"] == "" {
				current["default"] = primary
			}
		}
		if len(current) > 0 {
			row["modelRoles"] = current
		}

		models := b.modelOptionsFor(ctx, source, roleModelValues(current))
		for _, model := range models {
			id := b.actionID()
			b.modelRoles[id] = modelRoleTarget{ClientID: clientID, Source: source, ModelID: model}
			row["options"] = append(row["options"].([]map[string]any), map[string]any{
				"actionId":    id,
				"modelId":     model,
				"sourceLabel": refs.DisplayRef(source),
			})
		}
		options := row["options"].([]map[string]any)
		row["editable"] = len(options) > 0
		switch {
		case len(options) == 0:
			row["unavailableReason"] = "No models were discovered for this connection."
		case source.Kind == refs.KindAccount && source.Provider == clientID:
			// Same-family account: the client keeps its own login and its own
			// model picker, so AnyPick must not offer a second one.
			row["editable"] = false
			row["unavailableReason"] = "This connection uses the app’s own model picker."
		}
		b.snap.ClientModelConfigs = append(b.snap.ClientModelConfigs, row)
	}
}

// bindingFor reads one client's global binding, tolerating a nil Bindings
// dependency (legacy compositions) the same way routes() does.
func (b *snapshotBuilder) bindingFor(ctx context.Context, clientID string) (*bindings.Global, error) {
	if b.srv.Deps.Bindings == nil {
		return nil, nil
	}
	return b.srv.Deps.Bindings.GetGlobalContext(ctx, clientID)
}

// modelOptionsFor resolves the model ids a connection can serve: configured
// models first, then the hub's published catalog or the source provider's
// cached/static catalog.
//
// NoLive is always set: a snapshot tick may read a warm cache or the static
// catalog but must never trigger vendor egress (see discovery.Request).
func (b *snapshotBuilder) modelOptionsFor(ctx context.Context, source refs.Ref, current []string) []string {
	seen := map[string]bool{}
	out := []string{}
	push := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if source.Kind == refs.KindProxyHub {
		for _, row := range b.snap.HubPublishedModels {
			if needs, ok := row["needsChoice"].(bool); ok && needs {
				continue // a model without a resolved owner is not selectable
			}
			if id, _ := row["id"].(string); id != "" {
				push(id)
			}
		}
	} else {
		catalogID, endpoint := b.sourceCatalog(ctx, source)
		for _, model := range b.srv.catalogList(ctx, catalogID, endpoint) {
			push(model)
		}
	}
	sort.Strings(out)
	return capModelOptions(out, current)
}

// capModelOptions trims a catalog to the picker's cap while keeping every
// pinned model selectable — a connection must never be unable to re-select
// the model it is already running.
func capModelOptions(models, pinned []string) []string {
	if len(models) > maxModelOptions {
		models = models[:maxModelOptions]
	}
	kept := make(map[string]bool, len(models))
	for _, model := range models {
		kept[model] = true
	}
	for i := len(pinned) - 1; i >= 0; i-- {
		if model := pinned[i]; model != "" && !kept[model] {
			models = append([]string{model}, models...)
			kept[model] = true
		}
	}
	return models
}

// sourceCatalog maps a bound source onto the (catalog id, endpoint) pair the
// model catalog is keyed by. The catalog id is the *model* provider, not the
// account provider: a saved codex account serves the OpenAI catalog
// (catalog.CatalogProviderFor), same as the hub catalogs do.
//
// Endpoint is best-effort: a stopped proxy or an unknown provider simply
// resolves to the static list.
func (b *snapshotBuilder) sourceCatalog(ctx context.Context, source refs.Ref) (string, string) {
	switch source.Kind {
	case refs.KindAccount, refs.KindAccountPool:
		endpoint := ""
		if st, err := b.srv.Deps.Proxy.StatusContext(ctx, source.Provider, source.Name); err == nil && st != nil && st.Running {
			endpoint = st.Endpoint
		}
		return catalog.CatalogProviderFor(source.Provider), endpoint
	case refs.KindGateway:
		if pr, err := b.srv.Deps.Profiles.GetContext(ctx, source.Name); err == nil && pr != nil {
			return catalog.CatalogProviderFor(pr.Meta.Provider), pr.Meta.Endpoint
		}
	}
	return "", ""
}

// modelRolesFromSpec reads the persisted clientOptions.modelRoles map. It is
// the single reader of that shape — primaryModelOf and this file share it so
// the two can never disagree about what "current" means.
func modelRolesFromSpec(spec bindings.Spec) map[string]string {
	out := map[string]string{}
	raw, ok := spec.ClientOptions["modelRoles"].(map[string]interface{})
	if !ok {
		return out
	}
	for id, value := range raw {
		if model, ok := value.(string); ok && model != "" {
			out[id] = model
		}
	}
	return out
}

func roleModelValues(roles map[string]string) []string {
	out := make([]string, 0, len(roles))
	for _, model := range roles {
		if model != "" {
			out = append(out, model)
		}
	}
	sort.Strings(out)
	return out
}

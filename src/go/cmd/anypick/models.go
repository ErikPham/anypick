package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/refs"
)

// cmdModels answers the question the MODEL_NOT_PINNED warning raises but never
// answered: "Re-run with --model <id>" — which ids?
//
// It lists the ids the bound source can serve, resolved the same way the
// desktop snapshot resolves its picker options (catalog.CatalogProviderFor for
// account/gateway sources, the hub's routed models for hub sources), so the
// CLI and the app cannot disagree about what is pinnable.
//
// Go-only command: the TypeScript tree has no equivalent (its Configure Models
// picker lives in the tray), which is declared in
// testdata/parity/cli-tree.json's omitted list.
func cmdModels(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	with := fs.String("with", "", "source reference (defaults to the client's current binding)")
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: anypick models <client> [--with <source>]")
		return int(anyerr.InvalidUsage)
	}
	clientID := fs.Arg(0)
	if _, err := a.Clients.Get(clientID); err != nil {
		return reportErr(err)
	}
	source, err := modelsSource(ctx, a, clientID, *with)
	if err != nil {
		return reportErr(err)
	}
	catalogID, models, err := modelsForSource(ctx, a, source)
	if err != nil {
		return reportErr(err)
	}
	// Whatever the client already pins stays listed: a connection must never
	// be unable to re-select the model it is running.
	models = mergeModelIDs(models, pinnedModelIDs(ctx, a, clientID))
	if len(models) == 0 {
		return reportErr(anyerr.Newf(
			"no model catalog is known for "+refs.DisplayRef(source)+
				"; run `anypick proxy hub preview` for hub routes, or check the provider id",
			"MODEL_CATALOG_UNKNOWN"))
	}

	if globals.JSON {
		jsonOut(map[string]any{
			"client":  clientID,
			"source":  refs.DisplayRef(source),
			"catalog": catalogID,
			"models":  models,
		})
		return int(anyerr.Success)
	}
	// Context goes to stderr so stdout stays a plain id-per-line stream that
	// `--quiet` (and any pipe) can consume untouched.
	if !globals.Quiet {
		context := fmt.Sprintf("%s ← %s", clientID, refs.DisplayRef(source))
		if catalogID != "" {
			context += " · " + catalogID + " catalog"
		}
		fmt.Fprintln(os.Stderr, context)
	}
	for _, model := range models {
		fmt.Println(model)
	}
	return int(anyerr.Success)
}

// modelsSource resolves --with, falling back to the client's persisted
// binding. Unknown or missing sources are errors, never an empty list: "no
// models" and "no route" are different answers.
func modelsSource(ctx context.Context, a *app.App, clientID, with string) (refs.Ref, error) {
	if with != "" {
		providerSet := map[string]bool{}
		for _, id := range a.Providers.IDs() {
			providerSet[id] = true
		}
		return refs.ParseRef(with, &refs.ParseContext{AccountProviders: providerSet})
	}
	binding, err := a.BindingSvc.GetGlobalContext(ctx, clientID)
	if err != nil {
		return refs.Ref{}, err
	}
	if binding == nil {
		return refs.Ref{}, anyerr.Newf(
			"client "+clientID+" has no binding to list models for; pass --with <source>",
			"BINDING_NOT_FOUND")
	}
	return binding.Spec.Source, nil
}

// modelsForSource returns the catalog id (empty for hub routes) and the model
// ids that connection publishes.
func modelsForSource(ctx context.Context, a *app.App, source refs.Ref) (string, []string, error) {
	switch source.Kind {
	case refs.KindProxyHub:
		routes, err := a.ProxyHub.RouteModelsContext(ctx, source.Name)
		if err != nil {
			return "", nil, err
		}
		out := make([]string, 0, len(routes))
		for _, route := range routes {
			out = append(out, route.Model)
		}
		return "", out, nil
	case refs.KindAccount, refs.KindAccountPool:
		return catalog.CatalogProviderFor(source.Provider), discovery.StaticModels(catalog.CatalogProviderFor(source.Provider)), nil
	case refs.KindGateway:
		profile, err := a.Runtime.ProfileSvc.GetContext(ctx, source.Name)
		if err != nil {
			return "", nil, err
		}
		if profile == nil {
			return "", nil, anyerr.Newf("gateway "+source.Name+" does not exist", "GATEWAY_NOT_FOUND")
		}
		catalogID := catalog.CatalogProviderFor(profile.Meta.Provider)
		return catalogID, discovery.StaticModels(catalogID), nil
	default:
		return "", nil, anyerr.Newf(
			"models are not listed for "+string(source.Kind)+" sources", "MODEL_CATALOG_UNKNOWN")
	}
}

// pinnedModelIDs collects every model the client's binding currently stores —
// the explicit selection plus each role override.
func pinnedModelIDs(ctx context.Context, a *app.App, clientID string) []string {
	binding, err := a.BindingSvc.GetGlobalContext(ctx, clientID)
	if err != nil || binding == nil {
		return nil
	}
	var out []string
	if binding.Spec.Model.Mode == "explicit" {
		out = append(out, binding.Spec.Model.ID)
	}
	roles, _ := binding.Spec.ClientOptions["modelRoles"].(map[string]interface{})
	for _, value := range roles {
		if model, ok := value.(string); ok {
			out = append(out, model)
		}
	}
	return out
}

func mergeModelIDs(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, id := range list {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

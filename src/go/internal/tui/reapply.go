package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
)

// reapplyClientsBoundTo re-runs activation for every persistent client whose
// global binding names ref — the TUI counterpart of the desktop switch
// handler. The post-switch hook only realigns the binding row, so without
// this, proxy-backed clients keep serving the previous account.
//
// Only exact matches move; cross-provider routes stay pinned by design. The
// pinned model is preserved while the catalog still publishes it.
func reapplyClientsBoundTo(ctx context.Context, a *app.App, ref refs.Ref) (int, error) {
	if a == nil || a.Clients == nil || a.BindingSvc == nil || a.Deps == nil {
		return 0, nil
	}
	want := refs.SerializeRef(ref)
	type target struct {
		clientID string
		binding  *bindings.Global
	}
	var targets []target
	var errs []error
	for _, clientID := range a.Clients.IDs() {
		b, err := a.BindingSvc.GetGlobalContext(ctx, clientID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", clientID, err))
			continue
		}
		if b == nil || refs.SerializeRef(b.Spec.Source) != want {
			continue
		}
		targets = append(targets, target{clientID: clientID, binding: b})
	}
	if len(targets) == 0 {
		// No client is bound to this source, so nothing needs the catalog and
		// the switch must not perform live vendor egress.
		return 0, errors.Join(errs...)
	}
	var catalog []string
	if ref.Kind == refs.KindAccount && a.Proxy != nil {
		if models, derr := a.Proxy.DiscoverModels(ctx, ref.Provider, ref.Name); derr == nil {
			catalog = models
		}
	}
	reapplied := 0
	canUndo := a.Deps.Runtime != nil
	var recovery [][]string
	for _, t := range targets {
		b := t.binding
		model := pinnedBindingModel(b)
		specCopy := b.Spec
		if model != "" && len(catalog) > 0 && !containsModelID(catalog, model) {
			// The entitlement changed under this binding; let the planner
			// bootstrap from the current catalog instead of pinning stale.
			specCopy.Model = bindings.OmittedModel()
		}
		var records []string
		if canUndo {
			var rerr error
			records, rerr = a.Deps.Runtime.PrepareClientConfigRecovery(
				t.clientID, fmt.Sprintf("reapply-%s-%d", t.clientID, time.Now().UnixNano()))
			if rerr != nil {
				return reapplied, finishTUIReapply(recovery, fmt.Errorf("%s: %w", t.clientID, rerr))
			}
		}
		// Re-apply the stored spec rather than planning a fresh source-only
		// request: model roles and the transport policy travel with the
		// binding, and a fresh spec would silently reset them.
		plan, err := activation.PlanActivation(ctx, activation.Request{
			Mode:         activation.ModePersistent,
			Client:       t.clientID,
			ModelCatalog: catalog,
		}, a.Deps, activation.Options{
			ReapplySpec: &specCopy,
			ReapplyProv: &bindings.Provenance{
				Kind:                   "global_binding_snapshot",
				GlobalBindingUpdatedAt: b.UpdatedAt,
			},
			SkipNativeAuthWrite: true,
		})
		if err != nil {
			cause := errors.Join(append(errs, fmt.Errorf("%s: %w", t.clientID, err))...)
			if !canUndo && len(recovery) > 0 {
				return reapplied, accounts.IncompleteClientRestore(cause)
			}
			return reapplied, finishTUIReapply(recovery, cause)
		}
		if _, err := activation.Execute(ctx, plan, a.Deps, activation.ExecuteOptions{}); err != nil {
			cause := errors.Join(append(errs, fmt.Errorf("%s: %w", t.clientID, err))...)
			if !canUndo && len(recovery) > 0 {
				return reapplied, accounts.IncompleteClientRestore(cause)
			}
			return reapplied, finishTUIReapply(append(recovery, records), cause)
		}
		recovery = append(recovery, records)
		reapplied++
	}
	return reapplied, errors.Join(errs...)
}

// finishTUIReapply restores client configs captured before this reapply.
// A restore failure is marked incomplete so the switch does not claim the
// clients were returned to the previous account.
func finishTUIReapply(recovery [][]string, cause error) error {
	var restoreErr error
	for i := len(recovery) - 1; i >= 0; i-- {
		if err := runtime.RestoreClientConfigRecovery(recovery[i]); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	if restoreErr != nil {
		return accounts.IncompleteClientRestore(errors.Join(cause, restoreErr))
	}
	return cause
}

// pinnedBindingModel returns the model id a binding already runs, if any:
// the explicit selection first, then the default model role.
func pinnedBindingModel(b *bindings.Global) string {
	if b == nil {
		return ""
	}
	if b.Spec.Model.Mode == "explicit" && strings.TrimSpace(b.Spec.Model.ID) != "" {
		return strings.TrimSpace(b.Spec.Model.ID)
	}
	if roles, ok := b.Spec.ClientOptions["modelRoles"].(map[string]interface{}); ok {
		if def, ok := roles["default"].(string); ok {
			return strings.TrimSpace(def)
		}
	}
	return ""
}

func containsModelID(models []string, want string) bool {
	for _, model := range models {
		if model == want {
			return true
		}
	}
	return false
}

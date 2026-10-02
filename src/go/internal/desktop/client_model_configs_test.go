package desktop

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/refs"
)

// bindClaudeToCodexAccount saves a codex account and binds claude to it:
// a cross-provider (proxy) route, which is the case the Models UI exists for.
func bindClaudeToCodexAccount(t *testing.T, env *trayEnv, srv *Server) {
	t.Helper()
	ctx := context.Background()
	if env.Deps.Discovery == nil {
		env.Deps.Discovery = &discovery.Service{Root: env.Root, Cache: env.Deps.ModelCache}
	}
	// Some callers construct the server before seeding the account. Keep the
	// copied dependency graph pointed at the same discovery service.
	srv.Deps.Discovery = env.Deps.Discovery
	env.writeCodexLive("models@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "models-acc", true); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := env.Deps.Discovery.StoreLive(ctx, "openai", "account/codex/models-acc",
		[]string{"gpt-5.6-sol", "gpt-5.6-terra"}); err != nil {
		t.Fatalf("seed account catalog: %v", err)
	}
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude",
			Source: refs.AccountRef("codex", "models-acc"),
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
}

func modelConfigFor(snap *Snapshot, clientID string) map[string]any {
	for _, row := range snap.ClientModelConfigs {
		if id, _ := row["clientId"].(string); id == clientID {
			return row
		}
	}
	return nil
}

func TestSnapshotPublishesModelConfigForBoundClient(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	bindClaudeToCodexAccount(t, env, srv)

	snap, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "claude")
	if row == nil {
		t.Fatalf("no clientModelConfigs row for claude: %#v", snap.ClientModelConfigs)
	}
	roles, _ := row["roles"].([]map[string]any)
	if len(roles) != 4 {
		t.Fatalf("roles = %#v want the four claude slots", roles)
	}
	options, _ := row["options"].([]map[string]any)
	if len(options) == 0 {
		t.Fatalf("options empty for a bound client: %#v", row)
	}
	if editable, _ := row["editable"].(bool); !editable {
		t.Fatalf("editable = false with %d options", len(options))
	}
	for _, option := range options {
		if id, _ := option["actionId"].(string); id == "" {
			t.Fatalf("option without an actionId: %#v", option)
		}
		if model, _ := option["modelId"].(string); model == "" {
			t.Fatalf("option without a modelId: %#v", option)
		}
		if provider, _ := option["providerId"].(string); provider != "codex" {
			t.Fatalf("option providerId = %q, want codex: %#v", provider, option)
		}
	}
	// Every published option id must resolve back to this client + source.
	first, _ := options[0]["actionId"].(string)
	target, ok := srv.snapshotModelRoleTargets()[first]
	if !ok {
		t.Fatalf("action %q was published but is not in the model registry", first)
	}
	if target.ClientID != "claude" || target.Source != refs.AccountRef("codex", "models-acc") {
		t.Fatalf("target = %#v want claude bound to the codex account", target)
	}
}

func TestSnapshotPublishesAccountScopedLiveCatalog(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Discovery = &discovery.Service{Root: env.Root, Cache: env.Deps.ModelCache}
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	bindClaudeToCodexAccount(t, env, srv)
	futureModel := "model-added-after-release"
	if err := env.Deps.Discovery.StoreLive(
		context.Background(), "openai", "account/codex/models-acc", []string{futureModel}); err != nil {
		t.Fatal(err)
	}
	if cached, _ := env.Deps.ModelCache.Get(context.Background(), "openai", "account/codex/models-acc"); cached == nil {
		t.Fatal("test catalog was not stored")
	}
	if got := srv.catalogList(context.Background(), "openai", "", "account/codex/models-acc"); len(got) != 1 || got[0] != futureModel {
		t.Fatalf("catalogList = %v", got)
	}

	snap, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "claude")
	if row == nil {
		t.Fatal("no client model config")
	}
	for _, option := range row["options"].([]map[string]any) {
		if option["modelId"] == futureModel {
			return
		}
	}
	t.Fatalf("account-scoped live model %q was not published: %#v", futureModel, row)
}

func TestSnapshotPublishesModelsForAHubBoundClientBeforeAnyPin(t *testing.T) {
	env := hubTestEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	ctx := context.Background()
	env.writeCodexLive("hub-models@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "hub-models", true); err != nil {
		t.Fatalf("save account: %v", err)
	}
	enabled := true
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{
		Name: "default", Enabled: true,
		Sources: []hub.Source{{ProviderID: "codex", Account: "hub-models", Enabled: &enabled}},
	}); err != nil {
		t.Fatalf("configure hub: %v", err)
	}
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude",
			Source: refs.ProxyHubRef("default"),
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatalf("bind claude to hub: %v", err)
	}

	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "claude")
	if row == nil {
		t.Fatal("no model config for claude")
	}
	options, _ := row["options"].([]map[string]any)
	if len(options) == 0 {
		t.Fatalf("hub-bound app has no model options before a route pin: %#v", row)
	}
	if editable, _ := row["editable"].(bool); !editable {
		t.Fatalf("hub-bound app is not editable: %#v", row)
	}
	for _, option := range options {
		id, _ := option["actionId"].(string)
		target, ok := srv.snapshotModelRoleTargets()[id]
		if !ok || target.Source != refs.ProxyHubRef("default") {
			t.Fatalf("hub option %q target = %#v, exists = %v", id, target, ok)
		}
	}
}

func TestSnapshotKeepsAPinnedModelSelectable(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	ctx := context.Background()
	bindClaudeToCodexAccount(t, env, srv)
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude",
			Source: refs.AccountRef("codex", "models-acc"),
			Model:  bindings.OmittedModel(),
			ClientOptions: map[string]interface{}{
				"modelRoles": map[string]interface{}{"default": "pinned-model-xyz"},
			},
		},
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "claude")
	if row == nil {
		t.Fatal("no claude config")
	}
	if stored, _ := row["modelRoles"].(map[string]string); stored["default"] != "pinned-model-xyz" {
		t.Fatalf("modelRoles = %#v want the stored default", stored)
	}
	found := false
	for _, option := range row["options"].([]map[string]any) {
		if option["modelId"] == "pinned-model-xyz" {
			found = true
		}
	}
	if !found {
		t.Fatal("a pinned model must stay selectable")
	}
}

func TestUnboundClientExplainsWhyModelsAreUnavailable(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	snap, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "claude")
	if row == nil {
		t.Fatal("an unbound app still owes the UI a row")
	}
	if editable, _ := row["editable"].(bool); editable {
		t.Fatalf("editable = true with no binding: %#v", row)
	}
	reason, _ := row["unavailableReason"].(string)
	if reason == "" {
		t.Fatalf("unavailableReason missing: %#v", row)
	}
	roles, _ := row["roles"].([]map[string]any)
	if len(roles) == 0 {
		t.Fatal("roles must be published so the UI can render the section")
	}
}

func TestApplyModelRolesRejectsForeignRoles(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	srv.modelRoleTargets = map[string]modelRoleTarget{
		"a1": {ClientID: "codex", Source: refs.AccountRef("codex", "x"), ModelID: "m1"},
	}
	err := srv.applyModelRoles(context.Background(), "codex",
		map[string]string{"default": "a1", "haiku": "a1"}, "")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("a claude-only role must be rejected, got %v", err)
	}
}

func TestApplyModelRolesRejectsMixedConnections(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	srv.modelRoleTargets = map[string]modelRoleTarget{
		"a1": {ClientID: "claude", Source: refs.AccountRef("codex", "x"), ModelID: "m1"},
		"a2": {ClientID: "claude", Source: refs.GatewayRef("gw"), ModelID: "m2"},
	}
	err := srv.applyModelRoles(context.Background(), "claude",
		map[string]string{"default": "a1", "sonnet": "a2"}, "")
	if err == nil || !strings.Contains(err.Error(), "one connection") {
		t.Fatalf("roles from two sources must be rejected, got %v", err)
	}
}

func TestApplyModelRolesRejectsAnotherClientsOption(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	srv.modelRoleTargets = map[string]modelRoleTarget{
		"a1": {ClientID: "codex", Source: refs.AccountRef("codex", "x"), ModelID: "m1"},
	}
	err := srv.applyModelRoles(context.Background(), "claude", map[string]string{"default": "a1"}, "")
	if err == nil || !strings.Contains(err.Error(), "model list changed") {
		t.Fatalf("an option published for another app must be rejected, got %v", err)
	}
}

func TestApplyModelRolesResetNeedsABinding(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	// Empty roles with no binding must fail closed, never nil-deref the
	// missing source.
	err := srv.applyModelRoles(context.Background(), "codex", map[string]string{}, "")
	if err == nil || !strings.Contains(err.Error(), "no connection") {
		t.Fatalf("reset without binding = %v", err)
	}
}

func TestCapModelOptionsKeepsAPinnedModel(t *testing.T) {
	models := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		models = append(models, fmt.Sprintf("m%02d", i))
	}
	sort.Strings(models)
	// Sorts last, so the plain cap would drop it.
	capped := capModelOptions(models, []string{"zz-pinned"})
	if len(capped) > maxModelOptions+1 {
		t.Fatalf("cap ignored: %d options", len(capped))
	}
	found := false
	for _, model := range capped {
		if model == "zz-pinned" {
			found = true
		}
	}
	if !found {
		t.Fatal("a pinned model must survive the option cap")
	}
}

func TestSameFamilyAccountDefersToTheAppsOwnPicker(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	ctx := context.Background()
	env.writeCodexLive("self@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "self", true); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "codex",
		Spec: bindings.Spec{
			Client: "codex",
			Source: refs.AccountRef("codex", "self"),
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	row := modelConfigFor(snap, "codex")
	if row == nil {
		t.Fatal("no codex config")
	}
	if editable, _ := row["editable"].(bool); editable {
		t.Fatalf("a same-provider account must not offer a second picker: %#v", row)
	}
	if reason, _ := row["unavailableReason"].(string); reason == "" {
		t.Fatalf("unavailableReason missing: %#v", row)
	}
}

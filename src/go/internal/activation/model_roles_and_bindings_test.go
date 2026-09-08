package activation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

func TestProtocolForClientMatrix(t *testing.T) {
	openai := transport.OpenAI
	anthropic := transport.Protocol("anthropic")

	// Nothing known anywhere → OpenAI default.
	if got := protocolForClient(nil, nil); got != transport.OpenAI {
		t.Fatalf("nil caps: %q", got)
	}
	// Source protocols only → first one.
	if got := protocolForClient(nil, []transport.Protocol{anthropic}); got != anthropic {
		t.Fatalf("source only: %q", got)
	}
	// Preference wins when the source supports it.
	pref := protocolForClient(&client.Capabilities{
		ProtocolPreference: anthropic,
	}, []transport.Protocol{openai, anthropic})
	if pref != anthropic {
		t.Fatalf("preference ignored: %q", pref)
	}
	// Preference unsupported → first source protocol.
	fallback := protocolForClient(&client.Capabilities{
		ProtocolPreference: anthropic,
	}, []transport.Protocol{openai})
	if fallback != openai {
		t.Fatalf("fallback: %q", fallback)
	}
	// First accepted protocol when no explicit preference.
	accepted := protocolForClient(&client.Capabilities{
		AcceptedProtocols: []transport.Protocol{anthropic, openai},
	}, []transport.Protocol{openai, anthropic})
	if accepted != anthropic {
		t.Fatalf("accepted preference: %q", accepted)
	}
	// No source protocols at all → OpenAI default.
	if got := protocolForClient(&client.Capabilities{}, nil); got != transport.OpenAI {
		t.Fatalf("default: %q", got)
	}
}

func TestMergeModelRolesIntoClientOptions(t *testing.T) {
	base := map[string]interface{}{"temperature": 0.2, "modelRoles": map[string]interface{}{
		"plan": "model-a", "empty": "",
	}}
	got := mergeModelRolesIntoClientOptions(base,
		map[string]string{"plan": "model-b", "review": " model-c "},
		bindings.OmittedModel())

	if got["temperature"] != 0.2 {
		t.Fatalf("base keys not copied: %+v", got)
	}
	roleOf := func(key string) (string, bool) {
		m, ok := got["modelRoles"].(map[string]interface{})
		if !ok {
			return "", false
		}
		s, ok := m[key].(string)
		return s, ok
	}
	if v, ok := roleOf("plan"); !ok || v != "model-b" {
		t.Fatalf("new roles must override existing: %+v", got["modelRoles"])
	}
	// New role values are taken verbatim (trimming belongs to rolesFromSpec).
	if v, ok := roleOf("review"); !ok || v != " model-c " {
		t.Fatalf("verbatim new role expected: %q", v)
	}
	if _, has := roleOf("empty"); has {
		t.Fatalf("blank existing role kept: %+v", got["modelRoles"])
	}

	// Explicit model seeds default only when absent.
	withDefault := mergeModelRolesIntoClientOptions(nil,
		map[string]string{"default": "kept"}, bindings.ExplicitModel("explicit-id"))
	if withDefault["modelRoles"].(map[string]interface{})["default"] != "kept" {
		t.Fatalf("existing default overwritten: %+v", withDefault)
	}
	seeded := mergeModelRolesIntoClientOptions(nil, nil, bindings.ExplicitModel("explicit-id"))
	if seeded["modelRoles"].(map[string]interface{})["default"] != "explicit-id" {
		t.Fatalf("default not seeded: %+v", seeded)
	}
	// Nothing to merge → base copy without modelRoles key.
	plain := mergeModelRolesIntoClientOptions(map[string]interface{}{"k": "v"}, nil, bindings.OmittedModel())
	if _, has := plain["modelRoles"]; has || plain["k"] != "v" {
		t.Fatalf("plain merge wrong: %+v", plain)
	}
}

func TestModelFromRolesOrSelectionAndRequest(t *testing.T) {
	spec := bindings.Spec{ClientOptions: map[string]interface{}{
		"modelRoles": map[string]interface{}{"default": "role-default"},
	}}
	got := modelFromRolesOrSelection(spec, bindings.OmittedModel())
	if got.ID != "role-default" || got.Mode != "explicit" {
		t.Fatalf("roles default not promoted: %+v", got)
	}
	explicit := bindings.ExplicitModel("keep-me")
	if same := modelFromRolesOrSelection(spec, explicit); same != explicit {
		t.Fatalf("explicit selection overridden: %+v", same)
	}
	noRoles := bindings.Spec{}
	if same := modelFromRolesOrSelection(noRoles, bindings.OmittedModel()); same.Mode != "omitted" {
		t.Fatalf("missing roles changed selection: %+v", same)
	}
	blankDefault := bindings.Spec{ClientOptions: map[string]interface{}{
		"modelRoles": map[string]interface{}{"default": ""},
	}}
	if same := modelFromRolesOrSelection(blankDefault, bindings.OmittedModel()); same.Mode != "omitted" {
		t.Fatalf("blank default promoted: %+v", same)
	}

	if got := modelFromRequest("req-model", bindings.OmittedModel()); got.ID != "req-model" {
		t.Fatalf("request model ignored: %+v", got)
	}
	if got := modelFromRequest("", explicit); got != explicit {
		t.Fatalf("fallback ignored: %+v", got)
	}
	if got := explicitModelID(explicit); got != "keep-me" {
		t.Fatalf("explicitModelID: %q", got)
	}
	if got := explicitModelID(bindings.OmittedModel()); got != "" {
		t.Fatalf("omitted should be empty: %q", got)
	}
}

func TestRolesFromSpec(t *testing.T) {
	if got := rolesFromSpec(nil); got != nil {
		t.Fatalf("nil spec: %v", got)
	}
	noKey := bindings.Spec{}
	if got := rolesFromSpec(&noKey); got != nil {
		t.Fatalf("no key: %v", got)
	}
	spec := bindings.Spec{ClientOptions: map[string]interface{}{
		"modelRoles": map[string]interface{}{
			"plan":  " model-a ",
			"num":   42, // non-string skipped
			"blank": "   ",
		},
	}}
	got := rolesFromSpec(&spec)
	if len(got) != 1 || got["plan"] != "model-a" {
		t.Fatalf("rolesFromSpec: %v", got)
	}
	allBlank := bindings.Spec{ClientOptions: map[string]interface{}{
		"modelRoles": map[string]interface{}{"x": "  "},
	}}
	if got := rolesFromSpec(&allBlank); got != nil {
		t.Fatalf("all blank should be nil: %v", got)
	}
	wrongShape := bindings.Spec{ClientOptions: map[string]interface{}{"modelRoles": "nope"}}
	if got := rolesFromSpec(&wrongShape); got != nil {
		t.Fatalf("wrong shape should be nil: %v", got)
	}
}

func TestErrorsAsHelper(t *testing.T) {
	direct := anyerr.Newf("boom", "BOOM")
	var target *anyerr.Error
	if !errorsAs(direct, &target) || target.Code != "BOOM" {
		t.Fatal("direct anyerr not matched")
	}
	wrapped := fmt.Errorf("outer: %w", direct)
	target = nil
	if !errorsAs(wrapped, &target) || target.Code != "BOOM" {
		t.Fatal("wrapped anyerr not matched")
	}
	if errorsAs(errors.New("plain"), &target) {
		t.Fatal("plain error must not match")
	}
	if errorsAs(nil, &target) {
		t.Fatal("nil must not match")
	}
}

func TestCommitProjectBindingBranches(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	// Wrong mode → internal plan error.
	actx := &activationContext{}
	err := commitProjectBinding(ctx, PlanStep{},
		&Plan{Mode: ModePersistent}, deps, ExecuteOptions{}, actx)
	if err == nil || !strings.Contains(err.Error(), "invalid project binding") {
		t.Fatalf("wrong mode: %v", err)
	}

	// Project mode but no root anywhere → INVALID_USAGE.
	spec := &bindings.Spec{Client: "codex"}
	prov := &bindings.Provenance{Kind: "direct"}
	var ae *anyerr.Error
	err = commitProjectBinding(ctx, PlanStep{},
		&Plan{Mode: ModeProject, Client: "codex", BindingSpec: spec, Provenance: prov},
		deps, ExecuteOptions{}, actx)
	if !errorsAs(err, &ae) || ae.Code != "INVALID_USAGE" {
		t.Fatalf("want INVALID_USAGE, got %v", err)
	}

	root := t.TempDir()
	opts := ExecuteOptions{ProjectRoot: root}

	// Success commits the project binding.
	actx = &activationContext{}
	step := PlanStep{Params: map[string]interface{}{}}
	if err := commitProjectBinding(ctx, step,
		&Plan{Mode: ModeProject, Client: "codex", BindingSpec: spec, Provenance: prov},
		deps, opts, actx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !actx.mutated || actx.projectBind == nil {
		t.Fatalf("activation context not updated: %+v", actx)
	}
	stored, err := deps.Bindings.GetProjectContext(ctx, root, "codex")
	if err != nil || stored == nil {
		t.Fatalf("binding not stored: %v %v", stored, err)
	}

	// Rollback (previous == nil) deletes it again.
	if runRollback(actx) != true {
		t.Fatal("rollback reported failure")
	}
	if gone, _ := deps.Bindings.GetProjectContext(ctx, root, "codex"); gone != nil {
		t.Fatalf("rollback did not delete: %+v", gone)
	}

	// Previous binding is restored verbatim on rollback.
	old := bindings.Project{
		ProjectRoot: root, Client: "codex",
		Spec:       bindings.Spec{Client: "codex", TransportPolicy: "proxy"},
		Provenance: bindings.Provenance{Kind: "legacy_migration"},
	}
	if _, err := deps.Bindings.UpsertProjectContext(ctx, old); err != nil {
		t.Fatal(err)
	}
	actx = &activationContext{}
	newSpec := &bindings.Spec{Client: "codex", TransportPolicy: "direct"}
	if err := commitProjectBinding(ctx, PlanStep{Params: map[string]interface{}{"projectRoot": root}},
		&Plan{Mode: ModeProject, Client: "codex", BindingSpec: newSpec, Provenance: prov},
		deps, ExecuteOptions{}, actx); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if !runRollback(actx) {
		t.Fatal("rollback failed")
	}
	restored, _ := deps.Bindings.GetProjectContext(ctx, root, "codex")
	if restored == nil || restored.Spec.TransportPolicy != "proxy" ||
		restored.Provenance.Kind != "legacy_migration" {
		t.Fatalf("previous binding not restored: %+v", restored)
	}
}

func TestRecoverRestoresExactFileBackups(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	// src is the LIVE client config; dstFile is the backup
	// runtime.PrepareClientConfigRecovery took before activation mutated it.
	// Recovery reads the backup and writes the live file.
	src := filepath.Join(t.TempDir(), "config.toml")
	dstFile := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := os.MkdirAll(filepath.Dir(dstFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Activation got as far as rewriting the live file, then died.
	if err := os.WriteFile(src, []byte("post-activation"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The journal transition is part of the recovery contract: success is
	// only counted when the entry exists and the state lands.
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-file", State: journal.StateExecuting,
	}); err != nil {
		t.Fatal(err)
	}
	result := &RecoveryResult{}
	entry := &journal.Entry{
		ID:          "j-file",
		State:       journal.StateExecuting,
		Params:      map[string]interface{}{},
		BackupPaths: []string{src + " => " + dstFile},
	}
	recover(ctx, deps, entry, result)

	data, rerr := os.ReadFile(src)
	if rerr != nil || string(data) != "original" {
		t.Fatalf("live file not restored from backup: %q %v", data, rerr)
	}
	// The backup must survive recovery untouched: it is the only copy
	// (ADR 0004). Reading the record the other way round clobbers it.
	if kept, err := os.ReadFile(dstFile); err != nil || string(kept) != "original" {
		t.Fatalf("backup clobbered by recovery: %q %v", kept, err)
	}
	if result.Recovered != 1 || len(result.Failed) != 0 {
		t.Fatalf("result %+v", result)
	}
}

// TestRecoverUsesRealProducerRecords drives recover() with compensation records
// from the real runtime.PrepareClientConfigRecovery rather than hand-built
// strings, because a hand-built record can encode the wrong direction and still
// pass — which is exactly how the inverted rollback survived (ADR 0004).
func TestRecoverUsesRealProducerRecords(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	live := filepath.Join(env.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	records, err := deps.Runtime.PrepareClientConfigRecovery("claude", "journal-abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 compensation record, got %d (%v)", len(records), records)
	}
	parts := strings.SplitN(records[0], "=>", 2)
	if len(parts) != 2 {
		t.Fatalf("record is not src=>dest: %q", records[0])
	}
	backup := strings.TrimSpace(parts[1])

	// Activation rewrote the live file and then died mid-flight.
	if err := os.WriteFile(live, []byte("post-activation"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "journal-abc", State: journal.StateExecuting,
	}); err != nil {
		t.Fatal(err)
	}
	result := &RecoveryResult{}
	entry := &journal.Entry{
		ID:          "journal-abc",
		State:       journal.StateExecuting,
		Params:      map[string]interface{}{},
		BackupPaths: records,
	}
	recover(ctx, deps, entry, result)

	if data, err := os.ReadFile(live); err != nil || string(data) != "original" {
		t.Fatalf("live client config not restored: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "original" {
		t.Fatalf("backup destroyed by recovery: %q err=%v", data, err)
	}
	if result.Recovered != 1 || len(result.Failed) != 0 {
		t.Fatalf("result %+v", result)
	}
}

func TestRecoverDeleteMarkerRemovesCreatedFiles(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps

	created := filepath.Join(t.TempDir(), "created.json")
	if err := os.WriteFile(created, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Journal.CreateContext(context.Background(), "activation", journal.CreateOptions{
		ID: "j-del", State: journal.StateExecuting,
	}); err != nil {
		t.Fatal(err)
	}
	result := &RecoveryResult{}
	recover(context.Background(), deps, &journal.Entry{
		ID: "j-del", State: journal.StateExecuting,
		Params:      map[string]interface{}{},
		BackupPaths: []string{"delete => " + created},
	}, result)

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("delete marker did not remove the created file")
	}
	if result.Recovered != 1 {
		t.Fatalf("result %+v", result)
	}
}

func TestRecoverFailedBackupsMarkEntryFailed(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		backups []string
	}{
		{"missing src", []string{filepath.Join(t.TempDir(), "gone") + " => " + filepath.Join(t.TempDir(), "x")}},
		{"empty part", []string{" => " + filepath.Join(t.TempDir(), "x")}},
		{"bare missing path", []string{filepath.Join(t.TempDir(), "vanished")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entryID := "j-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
				ID: entryID, State: journal.StateExecuting,
			}); err != nil {
				t.Fatal(err)
			}
			result := &RecoveryResult{}
			entry := &journal.Entry{
				ID: entryID, State: journal.StateExecuting,
				Params: map[string]interface{}{}, BackupPaths: tc.backups,
			}
			recover(ctx, deps, entry, result)
			if len(result.Failed) != 1 || result.Recovered != 0 {
				t.Fatalf("result %+v", result)
			}
			updated, err := deps.Journal.Get(entryID)
			if err != nil || updated == nil || updated.State != journal.StateFailed {
				t.Fatalf("journal not marked failed: %+v %v", updated, err)
			}
		})
	}
}

func TestRecoverPlannedRefusedAndUnknownSources(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	// Planned entries never started: nothing to compensate.
	planned := &RecoveryResult{}
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-planned", State: journal.StatePlanned,
	}); err != nil {
		t.Fatal(err)
	}
	recover(ctx, deps, &journal.Entry{ID: "j-planned", State: journal.StatePlanned}, planned)
	if planned.Recovered != 1 || len(planned.Refused) != 0 {
		t.Fatalf("planned: %+v", planned)
	}

	// Preset sources are never forward-executed from journals → refused.
	refused := &RecoveryResult{}
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-preset", State: journal.StateVerifying,
	}); err != nil {
		t.Fatal(err)
	}
	recover(ctx, deps, &journal.Entry{
		ID: "j-preset", State: journal.StateVerifying,
		Params: map[string]interface{}{"source": "preset/demo"},
	}, refused)
	if len(refused.Refused) != 1 || len(refused.Failed) != 1 {
		t.Fatalf("preset refusal: %+v", refused)
	}

	// Account sources that no longer re-resolve exactly → refused.
	unknown := &RecoveryResult{}
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-unknown", State: journal.StateRollingBack,
	}); err != nil {
		t.Fatal(err)
	}
	recover(ctx, deps, &journal.Entry{
		ID: "j-unknown", State: journal.StateRollingBack,
		Params: map[string]interface{}{"source": refs.SerializeRef(refs.AccountRef("codex", "ghost"))},
	}, unknown)
	if len(unknown.Refused) != 1 || len(unknown.Failed) != 1 {
		t.Fatalf("unknown source: %+v", unknown)
	}

	// Executing state with neither backups nor a source ref cannot continue.
	stuck := &RecoveryResult{}
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-stuck", State: journal.StateExecuting,
	}); err != nil {
		t.Fatal(err)
	}
	recover(ctx, deps, &journal.Entry{
		ID: "j-stuck", State: journal.StateExecuting,
		Params: map[string]interface{}{},
	}, stuck)
	if len(stuck.Failed) != 1 || stuck.Recovered != 0 {
		t.Fatalf("stuck entry: %+v", stuck)
	}
}

func TestValidateCredentialKinds(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	planFor := func(ref refs.Ref) *Plan {
		return &Plan{ResolvedSource: &ResolvedSource{Ref: ref}}
	}

	// Missing account snapshot → AUTH_REQUIRED with an add hint.
	err := validateCredential(ctx, planFor(refs.AccountRef("codex", "ghost")), deps)
	var ae *anyerr.Error
	if !errorsAs(err, &ae) || ae.Code != "AUTH_REQUIRED" || ae.ExitCode != anyerr.AuthRequired {
		t.Fatalf("account missing: %v", err)
	}
	if len(ae.Suggestions) == 0 {
		t.Fatal("AUTH_REQUIRED should carry suggestions")
	}

	// Existing account passes.
	env.writeCodexLive(t, "user@test.dev", "tok-ok")
	if _, err := env.Svc.Save(ctx, "codex", "ok", true); err != nil {
		t.Fatal(err)
	}
	if err := validateCredential(ctx, planFor(refs.AccountRef("codex", "ok")), deps); err != nil {
		t.Fatalf("existing account rejected: %v", err)
	}

	// Missing gateway profile → GATEWAY_NOT_FOUND / NotFound.
	gw := refs.Ref{Kind: refs.KindGateway, Name: "nope"}
	err = validateCredential(ctx, planFor(gw), deps)
	if !errorsAs(err, &ae) || ae.Code != "GATEWAY_NOT_FOUND" || ae.ExitCode != anyerr.NotFound {
		t.Fatalf("gateway: %v", err)
	}

	// Pool without enabled members → AUTH_REQUIRED.
	pool := refs.Ref{Kind: refs.KindAccountPool, Provider: "glm"}
	err = validateCredential(ctx, planFor(pool), deps)
	if !errorsAs(err, &ae) || ae.Code != "AUTH_REQUIRED" {
		t.Fatalf("pool: %v", err)
	}

	// Other kinds are accepted without checks.
	if err := validateCredential(ctx, planFor(refs.Ref{Kind: refs.KindPreset}), deps); err != nil {
		t.Fatalf("other kind: %v", err)
	}
}

// A failed compensation must not skip the independent compensations behind
// it: every item is attempted, failures aggregate, and the entry is Failed
// even though one item restored fine.
func TestRecoverAttemptsCompensationsAfterAFailure(t *testing.T) {
	env := newTestEnv(t)
	deps := env.Deps
	ctx := context.Background()

	goodSrc := filepath.Join(t.TempDir(), "config.toml")
	goodDst := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := os.MkdirAll(filepath.Dir(goodDst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goodDst, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goodSrc, []byte("post-activation"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "gone") + " => " + filepath.Join(t.TempDir(), "x")
	if _, err := deps.Journal.CreateContext(ctx, "activation", journal.CreateOptions{
		ID: "j-partial", State: journal.StateExecuting,
	}); err != nil {
		t.Fatal(err)
	}
	result := &RecoveryResult{}
	recover(ctx, deps, &journal.Entry{
		ID: "j-partial", State: journal.StateExecuting,
		Params:      map[string]interface{}{},
		BackupPaths: []string{bad, goodSrc + " => " + goodDst},
	}, result)

	// The good item behind the failure was still attempted and applied.
	if data, err := os.ReadFile(goodSrc); err != nil || string(data) != "original" {
		t.Fatalf("compensation behind the failure was skipped: %q %v", data, err)
	}
	if len(result.Failed) != 1 || result.Recovered != 0 {
		t.Fatalf("partial recovery must report failure, got %+v", result)
	}
	updated, err := deps.Journal.Get("j-partial")
	if err != nil || updated == nil || updated.State != journal.StateFailed {
		t.Fatalf("journal not marked failed: %+v %v", updated, err)
	}
}

// An entry the journal does not know must never count as recovered: the
// terminal transition is part of the recovery contract, not a formality.
func TestRecoverMissingJournalEntryNeverCountsRecovered(t *testing.T) {
	env := newTestEnv(t)
	result := &RecoveryResult{}
	recover(context.Background(), env.Deps, &journal.Entry{
		ID: "j-ghost", State: journal.StateExecuting,
		Params:      map[string]interface{}{},
		BackupPaths: []string{},
	}, result)
	if result.Recovered != 0 || len(result.Failed) != 1 {
		t.Fatalf("ghost entry must fail, got %+v", result)
	}
}

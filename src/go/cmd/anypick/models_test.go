package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/modelcache"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
)

func TestGatewayModelCatalogUsesTheGatewayLiveModels(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("model discovery path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-secret" {
			t.Fatalf("model discovery authorization = %q, want the gateway key", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gateway-model-a"},{"id":"gateway-model-b"}]}`))
	}))
	defer server.Close()

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := a.Runtime.ProfileSvc.UpsertContext(context.Background(), profile.Runtime{
		Meta: profile.Meta{
			Name:         "dynamic-gateway",
			Provider:     "custom",
			Endpoint:     server.URL + "/v1",
			DefaultModel: "gateway-model-a",
		},
		Secrets: profile.Secrets{APIKey: "gateway-secret"},
	}); err != nil {
		t.Fatal(err)
	}

	catalogID, models, err := modelsForSource(context.Background(), a, refs.GatewayRef("dynamic-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	if catalogID != "custom" {
		t.Fatalf("gateway catalog id = %q, want custom", catalogID)
	}
	for _, want := range []string{"gateway-model-a", "gateway-model-b"} {
		if !containsString(models, want) {
			t.Fatalf("gateway models = %v, missing live model %q", models, want)
		}
	}
	if got, _ := activationModelCatalog(context.Background(), a, refs.GatewayRef("dynamic-gateway")); !containsString(got, "gateway-model-b") {
		t.Fatalf("activation catalog = %v, want the same live gateway catalog", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Planning must keep discovery order: the list head becomes the default
// model, so sorting it would flip the default every time another surface
// (desktop cache order, CLI) plans the same source.
func TestMergeModelIDsOrderedKeepsDiscoveryOrder(t *testing.T) {
	got := mergeModelIDsOrdered(
		[]string{"default-model", "fast-model", "default-model", "  ", "balanced-model"},
		[]string{"fast-model", "extra"},
	)
	want := []string{"default-model", "fast-model", "balanced-model", "extra"}
	if len(got) != len(want) {
		t.Fatalf("ordered = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered = %v, want %v", got, want)
		}
	}
}

// TestModelsListsCatalogIDsForAGatewaySource covers the question the
// MODEL_NOT_PINNED warning asks the user to answer by hand.
func TestModelsListsCatalogIDsForAGatewaySource(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d (stdout=%q stderr=%q)", exit, lastStdout, lastStderr)
	}
	ids := nonEmptyLines(lastStdout)
	if len(ids) == 0 {
		t.Fatal("no model ids listed for a seeded gateway")
	}
	found := false
	for _, id := range ids {
		if id == "openai/gpt-5.6-sol" {
			found = true
		}
		if strings.ContainsAny(id, " \t") {
			t.Fatalf("stdout must be one bare id per line, got %q", id)
		}
	}
	if !found {
		t.Fatalf("openrouter catalog id missing from %v", ids)
	}
	// Context is chatter: stderr by default, so stdout stays pipeable.
	// DisplayRef names the gateway itself ("or"), not the "gateway/or" ref.
	for _, want := range []string{"claude", "or", "openrouter catalog"} {
		if !strings.Contains(lastStderr, want) {
			t.Fatalf("context line %q lacks %q", lastStderr, want)
		}
	}
}

// TestModelsDefaultsToTheCurrentBinding: without --with the client's persisted
// binding is the source, which is how the warning's user would call it.
func TestModelsDefaultsToTheCurrentBinding(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"use", "claude", "--with", "gateway/or"}) })
	if exit != 0 {
		t.Fatalf("use exited %d (%q)", exit, lastStderr)
	}
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d (%q)", exit, lastStderr)
	}
	fromBinding := nonEmptyLines(lastStdout)

	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	fromFlag := nonEmptyLines(lastStdout)

	if strings.Join(fromBinding, ",") != strings.Join(fromFlag, ",") {
		t.Fatalf("binding source and --with disagree:\n%v\n%v", fromBinding, fromFlag)
	}
}

// TestModelsQuietKeepsOnlyIDs: --quiet silences chatter, never data.
func TestModelsQuietKeepsOnlyIDs(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	globals.Quiet = true
	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("quiet models exited %d", exit)
	}
	if strings.TrimSpace(lastStderr) != "" {
		t.Fatalf("--quiet must silence the context line, got %q", lastStderr)
	}
	if len(nonEmptyLines(lastStdout)) == 0 {
		t.Fatal("--quiet must still print the ids")
	}
}

// A missing route is an error, not an empty list: "nothing to pin" and "no
// connection" are different answers.
func TestModelsWithoutRouteFails(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit == int(anyerr.Success) {
		t.Fatalf("models without a binding must fail, stdout=%q", lastStdout)
	}
	if !strings.Contains(lastStderr, "--with") {
		t.Fatalf("failure must say how to supply a source, got %q", lastStderr)
	}
}

func TestModelsUnknownClientFails(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "nope-client", "--with", "gateway/or"}) })
	if exit == int(anyerr.Success) {
		t.Fatal("an unknown client must fail")
	}
}

// The pinned model stays listed even when the catalog does not know it: the
// user must be able to re-select what the connection is already running.
//
// The id is deliberately absent from every catalog — a pinnable-but-unknown id
// is the only input that can tell "merged the binding" apart from "printed the
// catalog".
func TestModelsKeepsAPinnedModel(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	const pinned = "zz-pinned-not-in-any-catalog"
	var exit int
	captureBoth(t, func() {
		exit = run([]string{"use", "claude", "--with", "gateway/or", "--model", pinned})
	})
	if exit != 0 {
		t.Fatalf("use exited %d (%q)", exit, lastStderr)
	}
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d", exit)
	}
	found := false
	for _, id := range nonEmptyLines(lastStdout) {
		if id == pinned {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned model %q missing from %q", pinned, lastStdout)
	}
}

// A dry run must still plan a client whose native config needs a default model
// (dsh, zcode, …). The preview reads the last successful account discovery
// from the model cache instead of spending the saved credential on egress, so
// `use dsh --with <account>` plans instead of failing MODEL_REQUIRED.
func TestUseDryRunBootstrapsModelFromTheCachedAccountCatalog(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	// gemini is one of the providers dsh can ride through the managed proxy,
	// and an API-key account is creatable without a browser login.
	var exit int
	captureBoth(t, func() {
		exit = run([]string{
			"add", "account", "gemini", "--name", "api",
			"--credential", "api-key", "--secret", "gemini-api-key",
		})
	})
	if exit != 0 {
		t.Fatalf("seed gemini account exited %d (stdout=%q stderr=%q)",
			exit, lastStdout, lastStderr)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cache := &discovery.Service{Root: a.Root, Cache: &modelcache.Store{DB: a.DB}}
	if err := cache.StoreLive(context.Background(),
		catalog.CatalogIdentityFor("gemini"),
		catalog.AccountCatalogKey("gemini", "api"),
		[]string{"gemini-3.8-flash", "gemini-3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	closeFn()

	globals.DryRun = true
	globals.JSON = true
	captureBoth(t, func() { exit = run([]string{"use", "dsh", "--with", "gemini/api"}) })
	if exit != 0 {
		t.Fatalf("dry-run use exited %d (stdout=%q stderr=%q)",
			exit, lastStdout, lastStderr)
	}
	if !strings.Contains(lastStdout, "gemini-3.8-flash") {
		t.Fatalf("dry run did not bootstrap the cached catalog model: %q", lastStdout)
	}

	// Dry runs plan; they never persist.
	binding, err := openAppBindingForTest(t, "dsh")
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil {
		t.Fatalf("dry run persisted a dsh binding: %+v", binding)
	}
}

func TestModelsAndUseBootstrapsProxyHubModels(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() {
		exit = run([]string{
			"add", "account", "gemini", "--name", "api",
			"--credential", "api-key", "--secret", "gemini-api-key",
		})
	})
	if exit != 0 {
		t.Fatalf("seed gemini account exited %d: %q", exit, lastStderr)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cache := &discovery.Service{Root: a.Root, Cache: &modelcache.Store{DB: a.DB}}
	if err := cache.StoreLive(context.Background(),
		catalog.CatalogIdentityFor("gemini"),
		catalog.AccountCatalogKey("gemini", "api"),
		[]string{"gemini-2.5-flash", "gemini-2.5-pro"}); err != nil {
		t.Fatal(err)
	}

	// Create a Proxy Hub named "default" with gemini/api as its enabled source.
	if _, err := a.ProxyHub.UpsertConfig(context.Background(), hub.Config{
		Name:    "default",
		Enabled: true,
		Sources: []hub.Source{
			{ProviderID: "gemini", Account: "api"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	closeFn()

	// Verify `anypick models dsh --with hub:default` lists the models.
	captureBoth(t, func() { exit = run([]string{"models", "dsh", "--with", "hub:default"}) })
	if exit != 0 {
		t.Fatalf("models dsh --with hub:default exited %d: %q", exit, lastStderr)
	}
	if !strings.Contains(lastStdout, "gemini-2.5-flash") || !strings.Contains(lastStdout, "gemini-2.5-pro") {
		t.Fatalf("models output missing expected hub models: %q", lastStdout)
	}

	// Verify `anypick use dsh --with hub:default` successfully bootstraps a default model.
	globals.DryRun = true
	globals.JSON = true
	captureBoth(t, func() { exit = run([]string{"use", "dsh", "--with", "hub:default"}) })
	if exit != 0 {
		t.Fatalf("use dsh --with hub:default failed with %d (stdout=%q stderr=%q)",
			exit, lastStdout, lastStderr)
	}
	if !strings.Contains(lastStdout, "gemini-2.5-flash") {
		t.Fatalf("dry run did not bootstrap expected model for hub: %q", lastStdout)
	}
}

// TestModelsUnknownAccountFailsLikeUnknownGateway pins the account branch's
// validation. The gateway branch refused an unknown gateway; the account
// branch printed the provider's static catalog and exited 0, so a typo'd
// account produced a confident wrong answer instead of an error, on the very
// command the MODEL_NOT_PINNED warning sends users to.
func TestModelsUnknownAccountFailsLikeUnknownGateway(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	exit := 0
	_, stderr := captureBoth(t, func() {
		exit = run([]string{"models", "claude", "--with", "claude/DOES-NOT-EXIST"})
	})
	if exit != int(anyerr.NotFound) {
		t.Fatalf("models with a nonexistent account exited %d, want %d (not found); stderr=%q",
			exit, int(anyerr.NotFound), stderr)
	}
	if !strings.Contains(stderr, "ACCOUNT_NOT_FOUND") && !strings.Contains(stderr, "DOES-NOT-EXIST") {
		t.Fatalf("error did not name the missing account: %q", stderr)
	}
}

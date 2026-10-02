package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCloudflareAccount = "f4b3c2a1e5d647a8b9c0d1e2f3a4b5c6"
	testCloudflareGateway = "default"
	testCloudflareProfile = testCloudflareAccount + "/" + testCloudflareGateway
)

func TestCloudflareProfileParsing(t *testing.T) {
	p := newCloudflareAIGateway()
	snap := func() string { return t.TempDir() }

	if _, err := p.BackupInput("nope", "tok", testCloudflareProfile, snap()); err == nil {
		t.Fatal("unknown kind must fail")
	}
	if _, err := p.BackupInput("api-token", "", testCloudflareProfile, snap()); err == nil {
		t.Fatal("empty secret must fail")
	}
	if _, err := p.BackupInput("api-token", "tok", "", snap()); err == nil {
		t.Fatal("missing profile must fail")
	}
	if _, err := p.BackupInput("api-token", "tok", "global", snap()); err == nil {
		t.Fatal("CLI-default profile must fail with a routing hint")
	} else if !strings.Contains(err.Error(), "<account_id>/<gateway_id>") {
		t.Fatalf("err = %v, want routing hint", err)
	}
	if _, err := p.BackupInput("api-token", "tok", "not-an-account/default", snap()); err == nil {
		t.Fatal("malformed account id must fail")
	}

	// Bare account id defaults the gateway.
	dir := t.TempDir()
	if _, err := p.BackupInput("api-token", "tok", testCloudflareAccount, dir); err != nil {
		t.Fatalf("bare account: %v", err)
	}
	cred, err := readCloudflareCredential(dir)
	if err != nil || cred == nil {
		t.Fatalf("read = %+v, %v", cred, err)
	}
	if cred.GatewayID != "default" {
		t.Fatalf("gateway = %q, want default", cred.GatewayID)
	}
}

func TestCloudflareBackupInputRoundTrip(t *testing.T) {
	p := newCloudflareAIGateway()
	snap := t.TempDir()
	info, err := p.BackupInput("api-token", "cf-token-123", testCloudflareProfile, snap)
	if err != nil {
		t.Fatal(err)
	}
	if info.CredentialKind != "proxy-only" || info.CredentialClass != "provider_api_key" {
		t.Fatalf("info = %+v", info)
	}
	if info.Identity != testCloudflareGateway {
		t.Fatalf("identity = %q", info.Identity)
	}
	raw, _ := os.ReadFile(filepath.Join(snap, cloudflareCredentialFile))
	if strings.Contains(string(raw), testCloudflareProfile) {
		t.Fatalf("stored file should split routing pair, got %s", raw)
	}

	desc, err := p.DescribeSnapshot(snap)
	if err != nil || desc.CredentialKind != "proxy-only" {
		t.Fatalf("describe = %+v, %v", desc, err)
	}

	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := "https://api.cloudflare.com/client/v4/accounts/" + testCloudflareAccount + "/ai/v1"
	if up.BaseURL.String() != wantBase {
		t.Fatalf("base = %s, want %s", up.BaseURL, wantBase)
	}
	if up.Auth != "cf-token-123" || up.AuthHeader != "Authorization" || up.AuthValuePrefix != "Bearer " {
		t.Fatalf("auth = %+v", up)
	}
	found := false
	for _, h := range up.ExtraHeaders {
		if h.Name == "cf-aig-gateway-id" && h.Value == testCloudflareGateway {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra headers = %+v, want cf-aig-gateway-id", up.ExtraHeaders)
	}
	// Native target joins with /v1/messages: no trailing /v1 (see
	// TestCloudflareAnthropicBaseHasNoVersionSegment).
	wantAnthropicBase := "https://api.cloudflare.com/client/v4/accounts/" + testCloudflareAccount + "/ai"
	if up.Anthropic == nil || up.Anthropic.BaseURL.String() != wantAnthropicBase {
		t.Fatalf("anthropic = %+v", up.Anthropic)
	}
	if up.TranslateAnthropic {
		t.Fatal("native Anthropic target must disable translation")
	}
	if up.ModelDiscovery == nil {
		t.Fatal("want live model discovery")
	}

	if _, err := p.ResolveUpstream(t.TempDir()); err == nil {
		t.Fatal("missing snapshot must fail")
	}
}

func TestCloudflareAnthropicBaseHasNoVersionSegment(t *testing.T) {
	// The proxy joins this base with the inbound /v1/messages path: a
	// trailing /v1 would double to .../ai/v1/v1/messages for Claude Code.
	// The OpenAI base keeps its /v1 (joined with /chat/completions).
	if got := cloudflareAnthropicBaseURL(testCloudflareAccount); got !=
		"https://api.cloudflare.com/client/v4/accounts/"+testCloudflareAccount+"/ai" {
		t.Errorf("anthropic base = %q", got)
	}
	if got := cloudflareBaseURL(testCloudflareAccount); got !=
		"https://api.cloudflare.com/client/v4/accounts/"+testCloudflareAccount+"/ai/v1" {
		t.Errorf("openai base = %q", got)
	}
}

func TestCloudflareResolveUpstreamAnthropicBase(t *testing.T) {
	p := newCloudflareAIGateway()
	dir := t.TempDir()
	if _, err := p.BackupInput("api-token", "tok-123", testCloudflareProfile, dir); err != nil {
		t.Fatalf("BackupInput: %v", err)
	}
	up, err := p.ResolveUpstream(dir)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Anthropic == nil || up.Anthropic.BaseURL == nil {
		t.Fatal("missing native Anthropic target")
	}
	if got := up.Anthropic.BaseURL.String(); got !=
		"https://api.cloudflare.com/client/v4/accounts/"+testCloudflareAccount+"/ai" {
		t.Errorf("anthropic base = %q, want no trailing /v1", got)
	}
	if got := up.BaseURL.String(); got !=
		"https://api.cloudflare.com/client/v4/accounts/"+testCloudflareAccount+"/ai/v1" {
		t.Errorf("openai base = %q", got)
	}
}

func TestCloudflareAccountAdapterPairing(t *testing.T) {
	p := newCloudflareAIGateway()
	a := p.AccountAdapter("work", false)
	for _, client := range []string{"claude", "codex", "pi", "omp"} {
		if got := a.TransportFor(client); got != "managed_builtin_proxy" {
			t.Errorf("%s = %s", client, got)
		}
	}
	for _, client := range []string{"opencode", "zcode", "dsh", "commandcode", "gemini", "kiro", "cline", "workbuddy"} {
		if got := a.TransportFor(client); got != "unsupported" {
			t.Errorf("%s = %s, want unsupported", client, got)
		}
	}
	if live, _ := p.DetectLive(); live.Present {
		t.Fatal("no native login expected")
	}
	if _, err := p.Backup(t.TempDir()); err == nil {
		t.Fatal("native Backup must fail")
	}
	if err := p.Restore(t.TempDir()); err != nil {
		t.Fatalf("restore no-op: %v", err)
	}
}

func TestCloudflareGatewayInfoListsInGatewayPicker(t *testing.T) {
	p := newCloudflareAIGateway()
	info := p.GatewayInfo()
	if !info.ProvidesEndpoint {
		t.Error("endpoint is derived per-account; the picker must not collect a URL")
	}
	if !info.RequiresAPIKey || !info.RequiresGatewayID || !info.RequiresAccountID {
		t.Error("gateway needs an API token, an account id, and a gateway id")
	}
	if info.Kind != "hosted" {
		t.Errorf("Kind = %q, want hosted", info.Kind)
	}
}

func TestCloudflareNormalizeGatewayID(t *testing.T) {
	p := newCloudflareAIGateway()
	if got, err := p.NormalizeGatewayID(""); err != nil || got != "default" {
		t.Errorf("empty = %q, %v; want default, nil", got, err)
	}
	if got, err := p.NormalizeGatewayID("  prod-gw_1 "); err != nil || got != "prod-gw_1" {
		t.Errorf("padded = %q, %v; want prod-gw_1, nil", got, err)
	}
	for _, bad := range []string{"has space", "way-too-long-" + strings.Repeat("x", 64), "semi;colon"} {
		if _, err := p.NormalizeGatewayID(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestCloudflareAccountIDFromEndpoint(t *testing.T) {
	if got, err := cloudflareAccountIDFromEndpoint(
		"https://api.cloudflare.com/client/v4/accounts/" + testCloudflareAccount + "/ai/v1",
	); err != nil || got != testCloudflareAccount {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := cloudflareAccountIDFromEndpoint("https://example.com/v1"); err == nil {
		t.Error("non-Cloudflare endpoint must fail")
	}
	if _, err := cloudflareAccountIDFromEndpoint(
		"https://api.cloudflare.com/client/v4/accounts/nothex/ai/v1",
	); err == nil {
		t.Error("malformed account id must fail")
	}
}

func TestCloudflareGatewayUpstreamWithEndpoint(t *testing.T) {
	p := newCloudflareAIGateway()
	ep := "https://api.cloudflare.com/client/v4/accounts/" + testCloudflareAccount + "/ai/v1"
	up, err := p.GatewayUpstreamWithEndpoint(ep, "tok-123", map[string]string{"gatewayId": "prod"})
	if err != nil {
		t.Fatalf("GatewayUpstreamWithEndpoint: %v", err)
	}
	if got := up.BaseURL.String(); got != ep {
		t.Errorf("base = %q, want endpoint", got)
	}
	if up.Auth != "tok-123" || up.AuthHeader != "Authorization" || up.AuthValuePrefix != "Bearer " {
		t.Errorf("auth = %q/%q/%q", up.AuthHeader, up.AuthValuePrefix, up.Auth)
	}
	found := false
	for _, h := range up.ExtraHeaders {
		if h.Name == "cf-aig-gateway-id" && h.Value == "prod" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing cf-aig-gateway-id header: %+v", up.ExtraHeaders)
	}
	if up.Anthropic == nil {
		t.Fatal("missing native Anthropic target for Claude Code")
	}
	if got := up.Anthropic.BaseURL.String(); got !=
		"https://api.cloudflare.com/client/v4/accounts/"+testCloudflareAccount+"/ai" {
		t.Errorf("anthropic base = %q, want no trailing /v1", got)
	}
	// Missing gateway id falls back to the provider default, never an error.
	up2, err := p.GatewayUpstreamWithEndpoint(ep, "tok-123", nil)
	if err != nil {
		t.Fatalf("nil headers: %v", err)
	}
	for _, h := range up2.ExtraHeaders {
		if h.Name == "cf-aig-gateway-id" && h.Value == "default" {
			found = true
		}
	}
	if _, err := p.GatewayUpstreamWithEndpoint("https://example.com/v1", "tok", nil); err == nil {
		t.Error("non-Cloudflare endpoint must fail closed, never guess an account")
	}
}

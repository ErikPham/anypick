package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexDetectLiveVariants(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_AUTH_PATH", "")

	// empty home -> absent
	env := newProviderEnv(t, nil)
	pv := mustProvider(t, env.Registry, "codex")
	live, err := pv.DetectLive()
	if err != nil || live.Present {
		t.Fatalf("empty should be absent: %+v %v", live, err)
	}

	// API key counts as present
	env.write(t, filepath.Join(env.Home, ".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-123"}`)
	live, err = pv.DetectLive()
	if err != nil || !live.Present || live.Identity != "API key" {
		t.Fatalf("api key detect = %+v %v", live, err)
	}

	// OAuth tokens with email JWT
	idToken := fakeJWT(map[string]string{"email": "codex@openai"})
	env.write(t, filepath.Join(env.Home, ".codex", "auth.json"),
		`{"tokens":{"access_token":"`+idToken+`","id_token":"`+idToken+`","account_id":"abcd1234efghijkl"}}`)
	live, err = pv.DetectLive()
	if err != nil || !live.Present || live.Identity != "codex@openai" {
		t.Fatalf("oauth detect = %+v %v", live, err)
	}
	if live.AccountID != "abcd1234efghijkl" {
		t.Fatalf("AccountID=%q", live.AccountID)
	}

	// Unreadable JSON still counts as present (best-effort)
	env.write(t, filepath.Join(env.Home, ".codex", "auth.json"), `{not json`)
	live, err = pv.DetectLive()
	if err != nil || !live.Present {
		t.Fatalf("unreadable JSON should be present: %+v %v", live, err)
	}
}

func TestCodexBackupRestoreDescribeAndEnvOverride(t *testing.T) {
	// Test CODEX_AUTH_PATH override
	tmpAuth := filepath.Join(t.TempDir(), "my-auth.json")
	t.Setenv("CODEX_AUTH_PATH", tmpAuth)
	t.Setenv("CODEX_HOME", "")
	env := newProviderEnv(t, nil)
	pv := mustProvider(t, env.Registry, "codex")

	// No live -> Backup fails with NO_LIVE_AUTH
	snap := t.TempDir()
	if _, err := pv.Backup(snap); err == nil {
		t.Fatal("Backup without live should fail")
	}

	// Create live via env path
	if err := os.MkdirAll(filepath.Dir(tmpAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpAuth, []byte(`{"OPENAI_API_KEY":"sk-env"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := pv.Backup(snap)
	if err != nil || info.Identity != "API key" {
		t.Fatalf("Backup via CODEX_AUTH_PATH = %+v %v", info, err)
	}
	// Restore via same env path
	if err := os.Remove(tmpAuth); err != nil {
		t.Fatal(err)
	}
	if err := pv.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmpAuth); err != nil {
		t.Fatalf("restored env auth not found: %v", err)
	}
	// DescribeSnapshot
	desc, err := pv.DescribeSnapshot(snap)
	if err != nil || desc.Identity != "API key" {
		t.Fatalf("DescribeSnapshot = %+v %v", desc, err)
	}
	// Missing snapshot -> empty
	empty, err := pv.DescribeSnapshot(t.TempDir())
	if err != nil || empty.Identity != "" {
		t.Fatalf("missing snapshot should be empty: %+v %v", empty, err)
	}
	// AccountAdapter proxy semantics
	adapter := pv.AccountAdapter("work", false)
	if got := adapter.TransportFor("codex"); got != "direct" {
		t.Errorf("codex own = %s", got)
	}
}

func TestClaudeFileRoundTrip(t *testing.T) {
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	credPath := filepath.Join(t.TempDir(), "creds.json")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", credPath)
	env := newProviderEnv(t, nil)
	pv := mustProvider(t, env.Registry, "claude")

	// No live
	live, err := pv.DetectLive()
	if err != nil || live.Present {
		t.Fatalf("claude empty should be absent: %+v %v", live, err)
	}

	// Backup without live -> NO_LIVE_AUTH
	if _, err := pv.Backup(t.TempDir()); err == nil {
		t.Fatal("claude Backup without live should fail")
	}

	// Seed a credential file: {"claudeAiOauth":{"email":"claude@example.com"}}
	raw := `{"claudeAiOauth":{"email":"claude@example.com","accessToken":"at","refreshToken":"rt"}}`
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte(raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err = pv.DetectLive()
	if err != nil || !live.Present || live.Identity != "claude@example.com" {
		t.Fatalf("claude detect = %+v %v", live, err)
	}

	// Backup copies into snapshot
	snap := t.TempDir()
	info, err := pv.Backup(snap)
	if err != nil || info.Identity != "claude@example.com" {
		t.Fatalf("claude Backup = %+v %v", info, err)
	}
	snapData, _ := os.ReadFile(filepath.Join(snap, "credentials.json"))
	var snapDoc map[string]interface{}
	if json.Unmarshal(snapData, &snapDoc) != nil || snapDoc["credential"] == nil {
		t.Fatalf("snapshot missing credential: %s", snapData)
	}

	// Clear live -> absent
	if clearer, ok := pv.(LiveClearer); ok {
		if err := clearer.ClearLive(); err != nil {
			t.Fatal(err)
		}
	}
	live, _ = pv.DetectLive()
	if live.Present {
		t.Fatalf("after ClearLive should be absent: %+v", live)
	}

	// Restore brings it back
	if err := pv.Restore(snap); err != nil {
		t.Fatal(err)
	}
	live, _ = pv.DetectLive()
	if !live.Present || live.Identity != "claude@example.com" {
		t.Fatalf("after Restore = %+v", live)
	}

	// DescribeSnapshot
	desc, err := pv.DescribeSnapshot(snap)
	if err != nil || desc.Identity != "claude@example.com" {
		t.Fatalf("DescribeSnapshot = %+v %v", desc, err)
	}

	// SnapshotMatchesLive: same credential -> true
	match, known, err := pv.(LiveMatcher).SnapshotMatchesLive(snap)
	if err != nil || !known || !match {
		t.Fatalf("SnapshotMatchesLive same = %v %v %v", match, known, err)
	}

	// Change live credential -> mismatch
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"email":"other@example.com"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	match, known, err = pv.(LiveMatcher).SnapshotMatchesLive(snap)
	if err != nil || !known || match {
		t.Fatalf("SnapshotMatchesLive different should be false: %v %v %v", match, known, err)
	}

	// Fingerprint strips refreshToken: rotation should still match
	// Reset live to original with different refreshToken
	if err := os.WriteFile(credPath, []byte(raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Manually check fingerprint helper
	fp1 := claudeFingerprint(`{"claudeAiOauth":{"email":"a@b","accessToken":"x","refreshToken":"old"}}`)
	fp2 := claudeFingerprint(`{"claudeAiOauth":{"email":"a@b","accessToken":"x","refreshToken":"new"}}`)
	if fp1 != fp2 {
		t.Fatalf("fingerprint should strip refreshToken: %q vs %q", fp1, fp2)
	}
	fp3 := claudeFingerprint(`{"claudeAiOauth":{"email":"a@b","accessToken":"y"}}`)
	if fp1 == fp3 {
		t.Fatalf("different accessToken should differ")
	}

	// Trivial provider getters
	if pv.Name() == "" || pv.Description() == "" || pv.CanProxy() {
		t.Fatalf("claude getters wrong: Name=%q Desc=%q CanProxy=%v", pv.Name(), pv.Description(), pv.CanProxy())
	}
	if pv.(interface{ ShortName() string }).ShortName() != "Claude" {
		t.Fatalf("ShortName wrong")
	}
	_ = env // keep env alive
}

func TestParseCodexUsage(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":70,"label":"Primary","resets_at_ms":123},"secondary_window":{"used_percent":20}}}`)
	usage, err := parseCodexUsage(body)
	if err != nil || len(usage.Windows) != 2 {
		t.Fatalf("parseCodexUsage = %+v %v", usage, err)
	}
	// 100-70=30 remaining, 100-20=80
	if usage.Windows[0].RemainingPercent != 30 || usage.Windows[0].Label != "Primary" {
		t.Fatalf("primary window = %+v", usage.Windows[0])
	}
	if usage.Windows[1].RemainingPercent != 80 {
		t.Fatalf("secondary window = %+v", usage.Windows[1])
	}
	if usage.Windows[0].ResetsAtMs == nil || *usage.Windows[0].ResetsAtMs != 123 {
		t.Fatalf("resets_at_ms missing")
	}

	// Vendor-native shapes: reset_at (unix seconds), reset_after_seconds
	// (relative), limit_window_seconds (cadence).
	body4 := []byte(`{"rate_limit":{"primary_window":{"used_percent":40,"label":"Primary","reset_at":1700000000,"limit_window_seconds":18000},"secondary_window":{"used_percent":10,"reset_after_seconds":3600,"limit_window_seconds":604800}}}`)
	usage4, err := parseCodexUsage(body4)
	if err != nil || len(usage4.Windows) != 2 {
		t.Fatalf("native shapes failed: %+v %v", usage4, err)
	}
	if usage4.Windows[0].ResetsAtMs == nil || *usage4.Windows[0].ResetsAtMs != 1700000000*1000 {
		t.Fatalf("reset_at seconds not converted: %+v", usage4.Windows[0])
	}
	if usage4.Windows[0].LimitWindowSeconds == nil || *usage4.Windows[0].LimitWindowSeconds != 18000 {
		t.Fatalf("limit_window_seconds missing: %+v", usage4.Windows[0])
	}
	after := usage4.Windows[1].ResetsAtMs
	nowMs := float64(nowUnixMilli())
	if after == nil || *after < nowMs+3599*1000 || *after > nowMs+3601*1000 {
		t.Fatalf("reset_after_seconds not anchored to now: %+v", usage4.Windows[1])
	}
	if usage4.Windows[1].LimitWindowSeconds == nil || *usage4.Windows[1].LimitWindowSeconds != 604800 {
		t.Fatalf("weekly cadence missing: %+v", usage4.Windows[1])
	}

	// Alt shape: rate_limits key
	body2 := []byte(`{"rate_limits":{"primary_window":{"used_percent":0}}}`)
	usage2, err := parseCodexUsage(body2)
	if err != nil || usage2.Windows[0].RemainingPercent != 100 {
		t.Fatalf("alt shape failed: %+v %v", usage2, err)
	}

	// Fallback: root is rate_limit
	body3 := []byte(`{"primary_window":{"used_percent":50}}`)
	usage3, err := parseCodexUsage(body3)
	if err != nil || usage3.Windows[0].RemainingPercent != 50 {
		t.Fatalf("root fallback failed: %+v %v", usage3, err)
	}

	// No windows -> error
	if _, err := parseCodexUsage([]byte(`{}`)); err == nil {
		t.Fatal("empty should error")
	}
	if _, err := parseCodexUsage([]byte(`not json`)); err == nil {
		t.Fatal("invalid JSON should error")
	}

	// helpers
	if got := usageLabel("fallback", map[string]interface{}{"label": "  custom "}); got != "custom" {
		t.Fatalf("usageLabel=%q", got)
	}
	if got := usageLabel("fallback", map[string]interface{}{}); got != "fallback" {
		t.Fatalf("usageLabel fallback=%q", got)
	}
	if clampPercent(150) != 100 || clampPercent(-5) != 0 || clampPercent(33.6) != 34 {
		t.Fatalf("clampPercent wrong")
	}
}

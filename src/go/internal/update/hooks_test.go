package update

import "testing"

func TestHookGateIgnoresWithoutFlag(t *testing.T) {
	env := MapEnv{
		EnvFeedURL:        "http://127.0.0.1:9",
		EnvChannel:        "brew",
		EnvClock:          "2026-10-12T00:00:00Z",
		EnvClockOffset:    "25h",
		EnvFault:          "checksum-mismatch",
		EnvCurrentVersion: "9.9.9",
		// EnvTesting deliberately unset
	}
	if TestingEnabled(env) {
		t.Fatal("testing should be disabled")
	}
	if HookValue(env, EnvFeedURL) != "" {
		t.Fatal("FEED_URL leaked without gate")
	}
	if HookValue(env, EnvChannel) != "" {
		t.Fatal("CHANNEL leaked without gate")
	}
	if HookValue(env, EnvClock) != "" {
		t.Fatal("CLOCK leaked without gate")
	}
	if HookValue(env, EnvClockOffset) != "" {
		t.Fatal("CLOCK_OFFSET leaked without gate")
	}
	if HookValue(env, EnvFault) != "" {
		t.Fatal("FAULT leaked without gate")
	}
	if HookValue(env, EnvCurrentVersion) != "" {
		t.Fatal("CURRENT_VERSION leaked without gate")
	}
	if _, ok := HookChannel(env); ok {
		t.Fatal("HookChannel ok without gate")
	}
	if HookCurrentVersion(env) != "" {
		t.Fatal("HookCurrentVersion without gate")
	}
	if HookFeedURL(env) != "" {
		t.Fatal("HookFeedURL without gate")
	}
	if HookFault(env) != "" {
		t.Fatal("HookFault without gate")
	}
}

func TestHookGateOpen(t *testing.T) {
	env := MapEnv{
		EnvTesting:        "1",
		EnvFeedURL:        "http://127.0.0.1:9/",
		EnvChannel:        "brew",
		EnvCurrentVersion: "1.2.3",
		EnvFault:          "swap-fail",
	}
	if !TestingEnabled(env) {
		t.Fatal("want enabled")
	}
	if got := HookFeedURL(env); got != "http://127.0.0.1:9" {
		t.Fatalf("feed %q", got)
	}
	ch, ok := HookChannel(env)
	if !ok || ch != ChannelBrew {
		t.Fatalf("channel %q ok=%v", ch, ok)
	}
	if HookCurrentVersion(env) != "1.2.3" {
		t.Fatal("version")
	}
	if HookFault(env) != "swap-fail" {
		t.Fatal("fault")
	}
}

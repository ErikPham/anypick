package proxy

import (
	"fmt"
	"testing"
	"time"
)

// TestCodeAssistProjectCacheIsBounded pins the leak fix: the cache is keyed by
// a credential that OAuth refresh replaces, so without a bound it grows once
// per refresh for the lifetime of a long-running proxy process.
func TestCodeAssistProjectCacheIsBounded(t *testing.T) {
	c := &codeAssistProjectCache{byToken: map[string]string{}}
	for i := 0; i < maxCodeAssistProjectEntries*4; i++ {
		c.set(fmt.Sprintf("token-%d", i), "project")
	}
	if got := len(c.byToken); got > maxCodeAssistProjectEntries {
		t.Fatalf("cache grew to %d entries, bound is %d", got, maxCodeAssistProjectEntries)
	}
	if _, ok := c.get("token-0"); ok {
		t.Fatal("an evicted entry survived the reset")
	}
}

// TestGeminiCooldownSweepsExpiredEntries pins the second leak: expired entries
// used to be removed only when that exact key was queried again, so a key set
// once and never asked for stayed in the map forever.
func TestGeminiCooldownSweepsExpiredEntries(t *testing.T) {
	c := &geminiCooldown{until: map[string]time.Time{}}
	for i := 0; i < maxGeminiCooldownEntries+10; i++ {
		c.set(fmt.Sprintf("route-%d", i), 1)
	}
	time.Sleep(5 * time.Millisecond)

	// Crossing the bound again must sweep what has already expired.
	c.set("final", 60_000)
	if got := len(c.until); got > maxGeminiCooldownEntries {
		t.Fatalf("cooldown map kept %d entries after a sweep, bound is %d",
			got, maxGeminiCooldownEntries)
	}
	if c.remainingMs("final") <= 0 {
		t.Fatal("the live cooldown was swept away with the expired ones")
	}
}

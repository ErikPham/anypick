package desktop

import (
	"strings"
	"testing"
)

func TestDisplaySafeIdentityKeepsHumanIdentifiers(t *testing.T) {
	for _, value := range []string{
		"person@example.com",
		"antigravity:abcdef123456",
		"Claude Max",
		"user-42",
	} {
		if got := displaySafeIdentity(value); got != value {
			t.Fatalf("displaySafeIdentity(%q) = %q", value, got)
		}
	}
}

func TestDisplaySafeIdentityRejectsOpaqueCredentials(t *testing.T) {
	opaque := strings.Repeat("A", 256)
	jwt := strings.Repeat("a", 24) + "." + strings.Repeat("b", 32) + "." + strings.Repeat("c", 24)
	for _, value := range []string{opaque, jwt, "identity\nwith a line break"} {
		if got := displaySafeIdentity(value); got != "" {
			t.Fatalf("opaque display identity was kept with length %d", len(value))
		}
	}
}

func TestDisplaySafeMessageDoesNotExposeOpaqueCredential(t *testing.T) {
	opaque := strings.Repeat("A", 256)
	raw := "Detected " + opaque + " in Gemini."
	got := displaySafeMessage(raw)
	if got == raw || strings.Contains(got, opaque) {
		t.Fatal("opaque credential crossed the display-message boundary")
	}
}

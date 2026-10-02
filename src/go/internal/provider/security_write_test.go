package provider

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestDarwinAddGenericPasswordUsesStdinPasswordWhenItFits(t *testing.T) {
	stdin, args := darwinAddGenericInvocation("Claude Code-credentials", "erik", "short-secret\n")
	if args != nil {
		t.Fatalf("short secret took argv: %v", args)
	}
	if len(stdin) > securityStdinLimit {
		t.Fatalf("stdin is %d bytes, limit %d", len(stdin), securityStdinLimit)
	}
	if strings.Contains(stdin, " -X ") {
		t.Fatalf("stdin used -X: %s", stdin)
	}
	if !strings.Contains(stdin, "-s 'Claude Code-credentials'") {
		t.Fatalf("service with a space was not quoted: %s", stdin)
	}
	if !strings.Contains(stdin, " -w ") {
		t.Fatalf("stdin is not a -w write: %s", stdin)
	}
}

func TestDarwinAddGenericPasswordUsesArgvWhenStdinWouldTruncate(t *testing.T) {
	value := strings.Repeat("A", securityStdinLimit)
	stdin, args := darwinAddGenericInvocation("Claude Code-credentials", "erik", value)
	if stdin != "" {
		t.Fatalf("oversized value stayed on stdin (%d bytes)", len(stdin))
	}
	if len(args) < 2 || args[len(args)-2] != "-X" {
		t.Fatalf("argv = %v, want a trailing -X", args)
	}
	decoded, err := hex.DecodeString(args[len(args)-1])
	if err != nil || string(decoded) != value {
		t.Fatalf("argv hex did not round-trip the full secret: %v len=%d", err, len(decoded))
	}
	for _, arg := range args[:len(args)-1] {
		if strings.Contains(arg, value[:24]) {
			t.Fatal("plaintext secret leaked into argv")
		}
	}
}

//go:build linux

package ownership

import (
	"os"
	"runtime"
	"testing"
)

// A Go binary's argv[0] is not guaranteed to appear in /proc/<pid>/cmdline.
// It is absent on linux/amd64 and present on linux/arm64, from the same
// toolchain on the same kernel, so a record that carries it verifies on a
// developer Mac and fails on an x86 CI runner.
//
// ownership.Spawn records argv[1:] and so is unaffected, but any test or caller
// that builds a Record from os.Args[0] is. This pins the platform behavior
// where it is discovered, so the next reader is not misled by a green run on
// arm64.
func TestLiveArgvDoesNotPromiseArgvZero(t *testing.T) {
	tokens, err := liveArgv(os.Getpid())
	if err != nil {
		t.Fatalf("liveArgv: %v", err)
	}

	for _, token := range tokens {
		if token == os.Args[0] {
			t.Logf("argv[0] is visible in cmdline on this platform (%s)", runtime.GOARCH)
			return
		}
	}
	t.Logf("argv[0] is NOT in cmdline on this platform (%s); a record carrying "+
		"os.Args[0] cannot verify here", runtime.GOARCH)
}

// The verification a real record relies on: its argv[1:] tokens are present.
func TestLiveArgvCarriesArgvTail(t *testing.T) {
	if len(os.Args) < 2 {
		t.Skip("no argv[1:] to check")
	}
	tokens, err := liveArgv(os.Getpid())
	if err != nil {
		t.Fatalf("liveArgv: %v", err)
	}
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		set[token] = true
	}
	for _, want := range os.Args[1:] {
		if !set[want] {
			t.Fatalf("argv[1:] token %q missing from cmdline %v", want, tokens)
		}
	}
}

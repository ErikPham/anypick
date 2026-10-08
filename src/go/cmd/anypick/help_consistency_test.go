package main

import (
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// TestHelpIsConsistentAcrossTheCommandTree pins the --help contract: every
// command answers --help with its usage on stdout and exit 0, without running
// the action. The census that found the gaps: tray and app printed a bare
// "Usage:" to stderr and exited 2, list/current/models/stop-proxy/completion
// did the same, and update --help performed the real network check. A command
// whose help is unusable makes the rest of the tree unlearnable.
func TestHelpIsConsistentAcrossTheCommandTree(t *testing.T) {
	// One command per dispatch shape: bare subcommand group (tray), alias
	// (app), inventory (list), diagnostic (current), catalog (models), server
	// lifecycle (serve), proxy subcommand (stop-proxy), shell integration
	// (completion), and the no-service reporters. start/status/stop and the
	// tray family already had the right shape and are covered elsewhere.
	commands := []string{
		"list", "current", "models", "serve", "stop-proxy", "completion",
		"app", "providers", "clients",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			restore := resetGlobals()
			defer restore()
			isolateEnv(t)

			var stdout, stderr string
			exit := 0
			stdout, stderr = captureBoth(t, func() {
				exit = run([]string{cmd, "--help"})
			})

			if exit != int(anyerr.Success) {
				t.Fatalf("%s --help exited %d, want 0 (stderr=%q)", cmd, exit, stderr)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Fatalf("%s --help printed nothing on stdout", cmd)
			}
			if strings.Contains(stderr, "Usage:") && !strings.Contains(stdout, "usage") {
				t.Fatalf("%s --help printed usage to stderr instead of stdout", cmd)
			}
		})
	}
}

// TestUpdateHelpDoesNotPerformTheCheck pins the surprising half: --help on
// update used to run the real GitHub release query, a network call hiding
// behind a help flag.
func TestUpdateHelpDoesNotPerformTheCheck(t *testing.T) {
	restore := resetGlobals()
	defer restore()
	isolateEnv(t)

	var stdout string
	exit := 0
	stdout, _ = captureBoth(t, func() {
		exit = run([]string{"update", "--help"})
	})

	if exit != int(anyerr.Success) {
		t.Fatalf("update --help exited %d, want 0", exit)
	}
	// The real check prints the current version or a release comparison; help
	// prints its usage instead.
	if strings.Contains(stdout, "latest") && !strings.Contains(stdout, "usage") {
		t.Fatalf("update --help performed the release check: %q", stdout)
	}
}

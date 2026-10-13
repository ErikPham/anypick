// Package cli holds small flag helpers used by cmd/anypick. Extracted from
// main.go so the reorder logic is unit-testable without spawning the CLI.
package cli

import (
	"flag"
	"os"
	"strings"
)

func isBoolFlag(name string) bool {
	switch name {
	case "all", "allow-foreign-config", "current", "detach", "dry-run",
		"follow", "force", "fix", "new", "no-input", "quiet", "reveal",
		"rotate", "start", "stdio", "stop", "trace", "tray", "verbose", "overwrite",
		"yes", "y", "json", "q", "v":
		return true
	default:
		return false
	}
}

// FsFor creates a FlagSet that prints to Stderr and returns ContinueOnError.
func FsFor() *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// Reorder moves flag arguments ahead of positionals so the stdlib flag
// package (which stops at the first non-flag) accepts `add <provider> --name x`.
func Reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			pos = append(pos, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if eq := strings.Index(name, "="); eq >= 0 {
			flags = append(flags, arg)
			continue
		}
		if isBoolFlag(name) || i+1 >= len(args) ||
			(strings.HasPrefix(args[i+1], "-") && args[i+1] != "-") {
			flags = append(flags, arg)
			continue
		}
		flags = append(flags, arg, args[i+1])
		i++
	}
	return append(flags, pos...)
}

// WantsHelp reports whether args ask for help without running the command.
func WantsHelp(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

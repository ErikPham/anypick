// Command anypick-repocheck runs the repository-structure gates.
//
// It replaces two shell programs that lived inside Makefile recipes: the
// repository-path check and the cross-language protocol-version check. Escaped
// shell in a recipe cannot be unit tested, cannot run on Windows, and — as the
// rewritten `paths` gate proved — can be silently disabled by a comment.
//
// Usage:
//
//	anypick-repocheck paths [root]
//	anypick-repocheck protocol [root]
//	anypick-repocheck actions [root]
//
// root defaults to the working directory; both subcommands walk up to the
// repository root from there.
//
// Exit codes: 0 clean, 1 violations found, 2 usage error.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/outof0/anypick/go/internal/repocheck"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || len(args) > 2 {
		usage(errOut)
		return 2
	}
	start := "."
	if len(args) == 2 {
		start = args[1]
	}
	var check func(string) []repocheck.Section
	switch args[0] {
	case "paths":
		check = repocheck.CheckPaths
	case "protocol":
		check = repocheck.CheckProtocol
	case "actions":
		check = repocheck.CheckActions
	default:
		usage(errOut)
		return 2
	}

	root, err := repocheck.Root(start)
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	sections := check(root)
	for _, section := range sections {
		fmt.Fprintf(out, "checking %s\n", section.Name)
		for _, item := range section.Checked {
			fmt.Fprintf(out, "  OK   %s\n", item)
		}
		for _, finding := range section.Findings {
			fmt.Fprintf(out, "  FAIL %s\n", finding.Detail)
			for _, line := range finding.Advice {
				fmt.Fprintf(out, "       %s\n", line)
			}
		}
	}
	if !repocheck.Passed(sections) {
		fmt.Fprintf(errOut, "%s check FAILED (%d finding(s))\n", args[0], len(repocheck.Findings(sections)))
		return 1
	}
	fmt.Fprintf(out, "%s check OK\n", args[0])
	return 0
}

func usage(errOut io.Writer) {
	fmt.Fprintln(errOut, "usage: anypick-repocheck {paths|protocol|actions} [root]")
}

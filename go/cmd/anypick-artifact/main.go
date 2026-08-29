// Command anypick-artifact runs the release-artifact gates.
//
// It exists so ADR 0020 §8 can be enforced without a JavaScript toolchain:
// until this command existed, proving a native artifact contained no Node
// runtime required Node to run the scanner.
//
// Usage:
//
//	anypick-artifact node-free <artifact-root>
//
// Exit codes: 0 clean, 1 violations found, 2 usage error.
package main

import (
	"fmt"
	"os"

	"github.com/outof0/anypick/go/internal/artifact"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) != 2 || args[0] != "node-free" {
		fmt.Fprintln(os.Stderr, "usage: anypick-artifact node-free <artifact-root>")
		return 2
	}
	root := args[1]
	violations := artifact.InspectNodeFree(root)
	if len(violations) == 0 {
		fmt.Printf("Node-free artifact OK: %s\n", root)
		return 0
	}
	fmt.Fprintf(os.Stderr, "ADR 0020: %s is not a Node-free native artifact:\n", root)
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  - %s\n", v.Message)
	}
	return 1
}

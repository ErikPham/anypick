// Dry-run breadth (CLI parity part 2, slice A) plus the confirmDelete port.
// Every mutating command prints its plan and exits BEFORE taking any
// mutation lock or touching services (invariant #3): no DB writes, no file
// writes, no journal rows, no spawned processes.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// kv is one ordered JSON object member. TS builds dry-run objects by
// insertion order and JSON.stringify drops undefined members, so plain-nil
// values are omitted while jsonNull emits an explicit null.
type kv struct {
	Key   string
	Value any
}

// jsonNull distinguishes "present but null" from "omitted".
type jsonNull struct{}

// orderedJSON renders fields in insertion order with TS JSON.stringify
// semantics; map[string]any would randomize key order across runs.
func orderedJSON(fields []kv) string {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, f := range fields {
		if f.Value == nil {
			continue // undefined members vanish, exactly like JSON.stringify
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		if _, isNull := f.Value.(jsonNull); isNull {
			b.WriteString("null")
			continue
		}
		v, err := json.Marshal(f.Value)
		if err != nil {
			v = []byte("null")
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.String()
}

// printDryRunFields ports printDryRunCommand/printDryRun: --json prints
// exactly one {"dryRun":true,"action":…,…} value on stdout; human mode prints
// "[dry-run] <detail>". stdout only — stderr stays free for diagnostics.
func printDryRunFields(action, detail string, fields ...kv) {
	if globals.JSON {
		fmt.Println(orderedJSON(append([]kv{{Key: "dryRun", Value: true}, {Key: "action", Value: action}}, fields...)))
		return
	}
	fmt.Printf("[dry-run] %s\n", detail)
}

// strOrNil maps an unset flag to an omitted member (TS undefined).
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// intOrNil maps an unset numeric flag to an omitted member.
func intOrNil(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// strOrJSONNull maps "" to an explicit null member (TS `client ?? null`).
func strOrJSONNull(s string) any {
	if s == "" {
		return jsonNull{}
	}
	return s
}

// emitResetDryRun renders bindingService.reset's dry-run result
// ({client, removedGlobal, dryRun:true}): --json prints the whole object;
// gateway reset prefixes [dry-run], plain reset describes the global binding.
func emitResetDryRun(clientID string, removedGlobal, gatewayStyle bool) {
	if globals.JSON {
		fmt.Println(orderedJSON([]kv{
			{Key: "client", Value: clientID},
			{Key: "removedGlobal", Value: removedGlobal},
			{Key: "dryRun", Value: true},
		}))
		return
	}
	if gatewayStyle {
		fmt.Printf("[dry-run] reset %s\n", clientID)
		return
	}
	if removedGlobal {
		fmt.Printf("Would remove global binding for %s\n", clientID)
	} else {
		fmt.Printf("No global binding for %s\n", clientID)
	}
}

// errDeclined marks an interactive "n" answer: nothing happened, exit 0.
var errDeclined = errors.New("confirmation declined")

// confirmDelete ports commands.ts confirmDelete: --yes short-circuits;
// otherwise a non-TTY stdin/stdout refuses with CONFIRMATION_REQUIRED and the
// TS exit code 2 WITHOUT consuming stdin, and a real TTY gets a y/N prompt.
func confirmDelete(label string, yes bool) error {
	if yes {
		return nil
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		return anyerr.Newf(
			fmt.Sprintf("Deleting %s requires --yes in non-interactive mode.", label),
			"CONFIRMATION_REQUIRED",
			anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithMutated(false))
	}
	fmt.Printf("Delete %s? [y/N] ", label)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("Cancelled.")
		return errDeclined
	}
	return nil
}

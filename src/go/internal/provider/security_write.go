package provider

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// quoteSecurityCommand quotes one token for a `security -i` line. Claude's
// service name contains spaces. An unquoted space splits the command, and
// the password then lands in the wrong argument.
func quoteSecurityCommand(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\r\n'\"\\$`!#&|;<>(){}[]*?") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// darwinAddGenericInvocation chooses how to pass value to
// add-generic-password. stdin is the go-keyring -w command when it fits in
// security's 4096-byte line. Past that, args is the argv form. Hex -X on
// stdin truncates the item and security still replaces it, so a rollback
// that uses the same channel corrupts the previous login too.
func darwinAddGenericInvocation(service, account, value string) (stdin string, args []string) {
	command := fmt.Sprintf(
		"add-generic-password -U -s %s -a %s -w %s\n",
		quoteSecurityCommand(service),
		quoteSecurityCommand(account),
		quoteSecurityCommand(value))
	if len(command) <= securityStdinLimit {
		return command, nil
	}
	return "", []string{
		"add-generic-password", "-U",
		"-s", service,
		"-a", account,
		"-X", hex.EncodeToString([]byte(value)),
	}
}

func writeGenericPassword(service, account, value string) error {
	stdin, args := darwinAddGenericInvocation(service, account, value)
	if stdin != "" {
		return runSecurityStdin(stdin)
	}
	return runSecurityArgs(args)
}

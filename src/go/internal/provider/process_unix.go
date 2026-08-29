package provider

import (
	"context"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"
)

// runSecurity shells out to /usr/bin/security (macOS keychain). The credential
// travels via stdin exactly like the TS store — never in process argv.
func runSecurity(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(script + "\n")
	out, err := cmd.Output()
	return string(out), err
}

func quoteSecurity(arg string) string {
	if !strings.ContainsAny(arg, "'\\") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// keychainAvailable reports whether the macOS keychain store can be used.
// Other platforms always take the file-backed store.
func keychainAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if _, err := exec.LookPath("/usr/bin/security"); err != nil {
		return false
	}
	return true
}

// currentUsername mirrors userInfo().username for keychain account fields.
func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "anypick"
}

// Secret screening for proxy log output.
//
// Every proxy log surface the CLI and the desktop protocol read passes
// through here (ADR-0006 token boundary): the exact persisted runtime token
// plus generic credential patterns are redacted before anything crosses an
// output boundary, and screened text stays bounded.
package proxy

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/paths"
)

// maxRedactedOutput bounds any secret-screened text that crosses an output
// boundary (invariant #2): provider-controlled strings stay bounded frames.
const maxRedactedOutput = 128 << 10

// credentialPattern screens bearer/API-key/token assignments even when the
// exact engine secret is not among the known values.
var credentialPattern = regexp.MustCompile(
	`(?i)(bearer\s+|x-api-key\s*[:=]\s*|authorization\s*[:=]\s*|api[-_]?key\s*[:=]\s*|token\s*[:=]\s*)([a-z0-9._~+/=-]{16,})`)

// RedactSecrets removes the given literal secrets plus generic credential
// assignments from provider-controlled text and bounds its length.
func RedactSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = credentialPattern.ReplaceAllString(text, "${1}[redacted]")
	if len(text) > maxRedactedOutput {
		text = text[:maxRedactedOutput] + "…[truncated]"
	}
	return text
}

// redactErr returns err with its message screened; structured codes and the
// error chain survive so exit codes and errors.As keep working. Unchanged
// messages pass the original error through untouched, preserving sentinels
// like context.Canceled for errors.Is.
func redactErr(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	screened := RedactSecrets(err.Error(), secrets)
	if screened == err.Error() {
		return err
	}
	var structured *anyerr.Error
	if errors.As(err, &structured) {
		clone := *structured
		clone.Message = screened
		return &clone
	}
	return errors.New(screened)
}

// accountSecrets lists every engine-owned secret that could leak through this
// provider/account's output: the persisted runtime token plus in-flight ones.
func (s *Service) accountSecrets(providerID, accountName string, extra ...string) []string {
	secrets := append([]string(nil), extra...)
	if state, err := s.Store.ReadProxyState(context.Background(), providerID, accountName); err == nil &&
		state != nil && state.Token != "" {
		secrets = append(secrets, state.Token)
	}
	return secrets
}

// ScreenProxyLog applies the same secret screening ProxyLogs uses so
// `--follow` cannot bypass redaction by reading the raw file.
func (s *Service) ScreenProxyLog(providerID, accountName, text string) string {
	return RedactSecrets(text, s.accountSecrets(providerID, accountName))
}

// ProxyLogs returns recent, secret-screened proxy log text from the engine's
// own log file. Provider text is untrusted: the exact persisted runtime token
// plus generic credential patterns are redacted before anything crosses an
// output boundary.
func (s *Service) ProxyLogs(ctx context.Context, providerID, accountName string, lines int) (string, error) {
	if _, err := s.Store.GetProxyConfig(ctx, providerID, accountName); err != nil {
		return "", err
	}
	secrets := s.accountSecrets(providerID, accountName)
	return RedactSecrets(tailLogFile(paths.ProxyLogPath(s.Store.Root, providerID, accountName), lines), secrets), nil
}

// tailLogFile returns empty output when no log exists yet, otherwise the
// trailing `lines` entries.
func tailLogFile(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

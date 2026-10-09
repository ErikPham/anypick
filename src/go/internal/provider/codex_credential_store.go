// Read Codex's credential-store choice without interpreting the rest of its
// TOML configuration. `file` is Codex's default when no setting is present.
package provider

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// CodexCredentialStore is where the Codex CLI keeps its persisted login.
type CodexCredentialStore string

const (
	CodexStoreFile    CodexCredentialStore = "file"
	CodexStoreKeyring CodexCredentialStore = "keyring"
	CodexStoreAuto    CodexCredentialStore = "auto"
	CodexStoreUnknown CodexCredentialStore = "unknown"
)

type CodexCredentialStoreStatus struct {
	Mode CodexCredentialStore
	// SupportsSnapshotSwitch is true only when AnyPick can atomically snapshot
	// and restore the live credential.
	SupportsSnapshotSwitch bool
	ConfigPath             string
}

// InspectCodexCredentialStore reads the credential-store choice. `auto` is
// deliberately not treated as file: on machines with a usable OS credential
// store it can resolve to the keyring, where writing auth.json would leave the
// actual CLI login unchanged.
func InspectCodexCredentialStore(codexHome string) CodexCredentialStoreStatus {
	configPath := filepath.Join(codexHome, "config.toml")
	mode := CodexStoreFile

	raw, err := os.ReadFile(configPath)
	if err == nil {
		if configured := readTopLevelTOMLString(string(raw), "cli_auth_credentials_store"); configured != "" {
			switch CodexCredentialStore(configured) {
			case CodexStoreFile, CodexStoreKeyring, CodexStoreAuto:
				mode = CodexCredentialStore(configured)
			default:
				// An unrecognized value is not proof that auth.json is
				// authoritative; prefer a recoverable refusal over a false switch.
				mode = CodexStoreUnknown
			}
		}
	} else if !os.IsNotExist(err) {
		// Existing but unreadable configuration is not proof that auth.json is
		// authoritative either.
		mode = CodexStoreUnknown
	}

	return CodexCredentialStoreStatus{
		Mode:                   mode,
		SupportsSnapshotSwitch: mode == CodexStoreFile,
		ConfigPath:             configPath,
	}
}

// CodexCredentialStoreGuidance is human-safe remediation that never contains
// credential material.
func CodexCredentialStoreGuidance(status CodexCredentialStoreStatus) string {
	setting := "cli_auth_credentials_store = \"file\""
	switch status.Mode {
	case CodexStoreAuto:
		return "Codex is configured to use auto credential storage, which may select the OS keyring. Set " +
			setting + " in " + status.ConfigPath + ", then sign in again before using account switching."
	case CodexStoreKeyring:
		return "Codex is configured to use the OS keyring. Set " +
			setting + " in " + status.ConfigPath + ", then sign in again before using account switching."
	default:
		return "Codex credential storage is not a supported file store. Set " +
			setting + " in " + status.ConfigPath + ", then sign in again before using account switching."
	}
}

// AssertFileCredentialStore refuses browser login / switching when auth.json
// is not the authoritative credential store.
func AssertFileCredentialStore(codexHome string) error {
	status := InspectCodexCredentialStore(codexHome)
	if status.SupportsSnapshotSwitch {
		return nil
	}
	return anyerr.Newf(
		CodexCredentialStoreGuidance(status),
		"CODEX_CREDENTIAL_STORE_UNSUPPORTED",
		anyerr.WithSuggestions(
			"Set cli_auth_credentials_store = \"file\" in "+status.ConfigPath+".",
			"Run codex login again so auth.json becomes the authoritative credential.",
		))
}

// readTopLevelTOMLString reads one root scalar assignment. The setting is
// root-only in Codex's config; accepting an assignment after a table header
// would accidentally read an unrelated plugin/provider value.
func readTopLevelTOMLString(content, key string) string {
	inRoot := true
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isTOMLTableHeader(trimmed) {
			inRoot = false
			continue
		}
		if !inRoot {
			continue
		}
		value, ok := matchTOMLAssignment(line, key)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if idx := tomlCommentIndex(value); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			unquoted := value[1 : len(value)-1]
			return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(unquoted)
		}
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

func isTOMLTableHeader(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	depth := 0
	for _, r := range trimmed {
		if r == '[' {
			depth++
		} else if r == ']' {
			depth--
		} else if depth == 0 {
			return false
		}
	}
	return true
}

func matchTOMLAssignment(line, key string) (string, bool) {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimRight(line[:idx], " \t"))
	// Strip an inline comment that starts directly at the name token boundary
	// is not possible here; only an exact name match counts.
	if name != key {
		return "", false
	}
	return line[idx+1:], true
}

func tomlCommentIndex(value string) int {
	inQuote := byte(0)
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case inQuote == 0 && c == '#':
			return i
		case inQuote == 0 && c == '"':
			inQuote = '"'
		case inQuote == 0 && c == '\'':
			inQuote = '\''
		case inQuote == '"' && c == '\\':
			i++
		case inQuote != 0 && c == inQuote:
			inQuote = 0
		}
	}
	return -1
}

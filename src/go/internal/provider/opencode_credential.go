package provider

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/db"
)

// The beta OpenCode CLI records logins in its sqlite `credential` table and
// stopped rewriting auth.json on login, so a saved mirror can silently hold a
// previous account's key. These helpers read the live store best-effort: any
// failure (missing file, older schema, foreign row shape) means "no live key",
// never an error.

type openCodePlatformKey struct {
	Service string
	Key     string
}

var openCodePlatformServices = map[string]bool{
	"opencode-go":  true,
	"opencode":     true,
	"opencode-zen": true,
	"zen":          true,
}

func openCodeLiveDataDir(home string) string {
	if env := os.Getenv("OPENCODE_DATA_DIR"); env != "" {
		return expandHome(env)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && expandHome(home) == homeDirOf() {
		return filepath.Join(expandHome(xdg), "opencode")
	}
	return filepath.Join(expandHome(home), ".local", "share", "opencode")
}

// liveOpenCodePlatformKeys lists platform API keys from the live CLI install's
// credential database.
func liveOpenCodePlatformKeys() []openCodePlatformKey {
	path := filepath.Join(openCodeLiveDataDir(homeDirOf()), "opencode.db")
	d, err := db.OpenForeign(path, true)
	if err != nil {
		return nil
	}
	defer d.Close()
	rows, err := d.Query("SELECT integration_id, value FROM credential")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []openCodePlatformKey
	for rows.Next() {
		var integrationID, value string
		if rows.Scan(&integrationID, &value) != nil {
			continue
		}
		if !openCodePlatformServices[integrationID] || value == "" {
			continue
		}
		// Api keys are stored as {"type":"key"|"api","key":"sk-…"}; oauth rows
		// (refresh tokens etc.) are not usable for the Zen/Go endpoints.
		var parsed struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal([]byte(value), &parsed) != nil {
			continue
		}
		if (parsed.Type == "key" || parsed.Type == "api") && parsed.Key != "" {
			out = append(out, openCodePlatformKey{Service: integrationID, Key: parsed.Key})
		}
	}
	return out
}

// preferredLiveOpenCodeKey picks the live key that backs a compatible gateway:
// the Go subscription id for opencode-go, any platform id for Zen. Empty when
// the live store has nothing usable.
func preferredLiveOpenCodeKey(definitionID string) string {
	keys := liveOpenCodePlatformKeys()
	if definitionID == "opencode-go" {
		for _, k := range keys {
			if k.Service == "opencode-go" {
				return k.Key
			}
		}
		return ""
	}
	for _, k := range keys {
		switch k.Service {
		case "opencode", "zen", "opencode-zen":
			return k.Key
		}
	}
	return ""
}

// liveOpenCodeAuthStatus reports a db-only login: present with the platform
// service as identity, or absent when the live store has no usable key.
func liveOpenCodeAuthStatus() (LiveAuthStatus, bool) {
	keys := liveOpenCodePlatformKeys()
	if len(keys) == 0 {
		return LiveAuthStatus{}, false
	}
	service := keys[0].Service
	for _, k := range keys {
		if k.Service == "opencode-go" {
			service = k.Service
			break
		}
	}
	return LiveAuthStatus{Present: true, Identity: service}, true
}

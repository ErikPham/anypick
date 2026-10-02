package provider

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestBuiltinNativeProviderAccountContract keeps the release matrix honest:
// every file-backed provider must be detectable, savable, clearable and
// restorable from an isolated fixture. Provider-specific tests still own the
// edge cases; this table catches the common add-account seam whenever a new
// provider is registered or a path/env variable changes.
func TestBuiltinNativeProviderAccountContract(t *testing.T) {
	fixtures := []struct {
		id    string
		build func(*testing.T) Provider
	}{
		{
			id: "codex",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				codexHome := filepath.Join(home, ".codex")
				t.Setenv("CODEX_HOME", codexHome)
				writeTestFile(t, filepath.Join(codexHome, "auth.json"), `{"OPENAI_API_KEY":"sk-contract"}`)
				return newCodex(home)
			},
		},
		{
			id: "claude",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
				path := filepath.Join(home, ".claude", ".credentials.json")
				t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", path)
				writeTestFile(t, path, `{"email":"claude-contract@example.com","token":"secret"}`)
				return newClaude(home)
			},
		},
		{
			id: "gemini",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				config := filepath.Join(home, ".gemini")
				t.Setenv("GEMINI_CONFIG_DIR", config)
				t.Setenv("GEMINI_CLI_HOME", "")
				writeTestFile(t, filepath.Join(config, ".env"), "GEMINI_API_KEY=gemini-contract\n")
				return newGemini(home)
			},
		},
		{
			id: "grok",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				path := filepath.Join(home, ".grok", "auth.json")
				t.Setenv("GROK_AUTH_PATH", path)
				writeTestFile(t, path, `{"session":{"email":"grok-contract@example.com","access_token":"token"}}`)
				return newGrok(home)
			},
		},
		{
			id: "opencode",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				path := filepath.Join(home, ".local", "share", "opencode", "auth.json")
				t.Setenv("OPENCODE_AUTH_PATH", path)
				t.Setenv("OPENCODE_DATA_DIR", filepath.Join(home, "missing-data"))
				writeTestFile(t, path, `{"openai":{"type":"oauth","access":"token"}}`)
				return newOpenCode(home)
			},
		},
		{
			id: "kiro",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				secretDB := filepath.Join(t.TempDir(), "kiro.sqlite3")
				cache := t.TempDir()
				t.Setenv("ANYPICK_KIRO_NO_KEYCHAIN", "1")
				t.Setenv("ANYPICK_KIRO_SECRET_DB", secretDB)
				t.Setenv("KIRO_SSO_CACHE_DIR", cache)
				db, err := sql.Open("sqlite", "file:"+secretDB)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
					db.Close()
					t.Fatal(err)
				}
				token, _ := json.Marshal(map[string]string{
					"accessToken": "at-contract", "refreshToken": "rt-contract",
					"profileArn": "arn:aws:sso::1:profile/contract", "provider": "BuilderId",
				})
				if _, err := db.Exec("INSERT INTO auth_kv (key, value) VALUES (?, ?)",
					"kirocli:social:token", string(token)); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return newKiro(home)
			},
		},
		{
			id: "workbuddy",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				path := filepath.Join(home, ".workbuddy", "auth.json")
				t.Setenv("WORKBUDDY_AUTH_PATH", path)
				// Restore/ClearLive also reach the desktop app's session, so
				// the contract round-trip must not touch the real home.
				t.Setenv("WORKBUDDY_NATIVE_AUTH_PATH", filepath.Join(t.TempDir(), "workbuddy-desktop-ai.info"))
				if err := writeWorkBuddyAuthDocument(path, workbuddyAuthDocument{
					Version: 1, Kind: workbuddyKindSub, MethodID: "workbuddy-international",
					Base: "https://www.codebuddy.ai", Domain: "www.codebuddy.ai",
					AccessToken: "at-contract", RefreshToken: "rt-contract", UserID: "contract-user",
				}); err != nil {
					t.Fatal(err)
				}
				return newWorkBuddy(home)
			},
		},
		{
			id: "zai", build: func(t *testing.T) Provider {
				home := t.TempDir()
				livePath := filepath.Join(home, "zai-live.json")
				t.Setenv(zaiLiveEnv, livePath)
				if err := writeZAICredential(livePath, zaiCredential{
					Version:          1,
					Source:           zaiSourceID,
					OAuthAccessToken: "oauth-contract",
					Identity:         "zai-contract@example.com",
				}); err != nil {
					t.Fatal(err)
				}
				p, err := newZAI(home)
				if err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			id: "cline",
			build: func(t *testing.T) Provider {
				home := t.TempDir()
				path := filepath.Join(home, ".cline-anypick", "auth.json")
				t.Setenv("CLINE_AUTH_PATH", path)
				t.Setenv("CLINE_PROVIDERS_PATH", filepath.Join(home, "missing-providers.json"))
				if err := writeClineAuthDocument(path, clineAuthDocument{
					Version: 1, Kind: clineKindSub, MethodID: clineLoginMethodID,
					AccessToken: "at-contract", RefreshToken: "rt-contract",
					ExpiresAtMs: 9999999999999, Email: "cline-contract@example.com",
				}); err != nil {
					t.Fatal(err)
				}
				return newCline(home)
			},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.id, func(t *testing.T) {
			p := fixture.build(t)
			live, err := p.DetectLive()
			if err != nil || !live.Present {
				t.Fatalf("DetectLive = %+v, err=%v", live, err)
			}
			snapshot := t.TempDir()
			info, err := p.Backup(snapshot)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if info.CredentialKind == "proxy-only" && !p.CanProxy() {
				t.Fatalf("non-proxy provider returned proxy-only snapshot: %+v", info)
			}
			if _, err := p.DescribeSnapshot(snapshot); err != nil {
				t.Fatalf("DescribeSnapshot: %v", err)
			}
			clearer, ok := p.(LiveClearer)
			if !ok {
				t.Fatal("registered native provider has no LiveClearer")
			}
			if err := clearer.ClearLive(); err != nil {
				t.Fatalf("ClearLive: %v", err)
			}
			cleared, err := p.DetectLive()
			if err != nil || cleared.Present {
				t.Fatalf("DetectLive after clear = %+v, err=%v", cleared, err)
			}
			if err := p.Restore(snapshot); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			restored, err := p.DetectLive()
			if err != nil || !restored.Present {
				t.Fatalf("DetectLive after restore = %+v, err=%v", restored, err)
			}
		})
	}
}

// TestEveryBuiltinCredentialProductIsAddable exercises every declared
// credential kind/profile pair, including the long tail of compatible
// providers. This is the UI contract too: if this fails, either the account
// sheet or the provider definition would offer an input that cannot resolve.
func TestEveryBuiltinCredentialProductIsAddable(t *testing.T) {
	t.Setenv("OPENCODE_DATA_DIR", filepath.Join(t.TempDir(), "missing-opencode-data"))
	for _, def := range BuiltinDefinitions() {
		def := def
		t.Run(def.ID, func(t *testing.T) {
			pv, err := NewCompatible(def)
			if err != nil {
				t.Fatal(err)
			}
			published := pv.CredentialDefinitions()
			if len(published) != len(def.Credentials) {
				t.Fatalf("published %d credential kinds, definition has %d", len(published), len(def.Credentials))
			}
			for _, credential := range def.Credentials {
				for _, profile := range credential.Profiles {
					snapshot := t.TempDir()
					info, err := pv.BackupInput(credential.Kind,
						"contract-"+def.ID, profile.ID, snapshot)
					if err != nil {
						t.Fatalf("BackupInput %s/%s: %v", credential.Kind, profile.ID, err)
					}
					if info.CredentialKind != "proxy-only" {
						t.Fatalf("%s/%s info = %+v", credential.Kind, profile.ID, info)
					}
					if _, err := pv.ResolveUpstream(snapshot); err != nil {
						t.Fatalf("ResolveUpstream %s/%s: %v", credential.Kind, profile.ID, err)
					}
				}
			}
		})
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

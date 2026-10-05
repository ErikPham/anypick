package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/client"
)

// A failed activation must be able to roll back these clients: every live
// file their adapters write has to be snapshotted (or marked for deletion)
// before the adapter runs.
func TestPrepareRecoveryCoversProxyBackedClients(t *testing.T) {
	// Path resolution must stay hermetic: product-dir overrides would aim the
	// backup at the developer's real config instead of the test home.
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	seeded := map[string]string{
		"gemini":    filepath.Join(".gemini", "settings.json"),
		"opencode":  filepath.Join(".config", "opencode", "opencode.json"),
		"dsh":       filepath.Join(".dsh", ".env"),
		"workbuddy": filepath.Join(".workbuddy-ai", "models.json"),
	}
	for clientID, rel := range seeded {
		t.Run(clientID, func(t *testing.T) {
			s := testService(t)
			full := filepath.Join(s.Home, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			const orig = `{"anypick-test":true}`
			if err := os.WriteFile(full, []byte(orig), 0o600); err != nil {
				t.Fatal(err)
			}
			records, err := s.PrepareClientConfigRecovery(clientID, "journal-123")
			if err != nil {
				t.Fatal(err)
			}
			if len(records) == 0 {
				t.Fatalf("no recovery records for %s; rollback would leave its files mutated", clientID)
			}
			for _, r := range records {
				if !strings.Contains(r, "=>") {
					t.Fatalf("malformed recovery record: %q", r)
				}
			}
			// Mutate, then restore: the seeded content must come back.
			if err := os.WriteFile(full, []byte(`{"anypick-test":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := RestoreClientConfigRecovery(records); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != orig {
				t.Fatalf("restored %s = %q, want %q", rel, got, orig)
			}
		})
	}
}

// TestPrepareRecoveryRecordsStayHermetic ensures every record points inside
// the service home/root, never at the developer's real config.
func TestPrepareRecoveryRecordsStayHermetic(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	s := testService(t)
	for _, clientID := range []string{"gemini", "opencode", "dsh", "claude", "codex", "zcode", "commandcode", "workbuddy"} {
		records, err := s.PrepareClientConfigRecovery(clientID, "journal-hermetic")
		if err != nil {
			t.Fatalf("%s: %v", clientID, err)
		}
		for _, r := range records {
			for _, path := range strings.Split(r, "=>") {
				if path == "" || path == "src" || path == "delete" {
					continue
				}
				if !strings.HasPrefix(path, s.Home) && !strings.HasPrefix(path, s.Root) {
					t.Fatalf("%s record escapes test dirs: %q", clientID, r)
				}
			}
		}
	}
}

// kiro persistent writes only AnyPick-root env files and touches no live
// client file, so there is nothing to snapshot.
func TestPrepareRecoveryKiroHasNoLiveFiles(t *testing.T) {
	s := testService(t)
	records, err := s.PrepareClientConfigRecovery("kiro", "journal-kiro")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("kiro records = %v, want none (no live files written)", records)
	}
}

// unmappedClientAdapter is a registered client with no recovery path in
// PrepareClientConfigRecovery. The registry lookup must succeed so the test
// reaches the switch rather than failing earlier on an unknown id.
type unmappedClientAdapter struct{}

func (unmappedClientAdapter) ID() string                         { return "unmapped-test-client" }
func (unmappedClientAdapter) Name() string                       { return "Unmapped Test Client" }
func (unmappedClientAdapter) Capabilities() *client.Capabilities { return &client.Capabilities{} }
func (unmappedClientAdapter) Apply(*client.ApplyContext) (*client.ApplyResult, error) {
	return nil, nil
}
func (unmappedClientAdapter) Reset() error { return nil }
func (unmappedClientAdapter) Inspect() (*client.InspectResult, error) {
	return nil, nil
}

// A registered client with no recovery path must fail loudly. Returning
// (nil, nil) here gave the caller no compensation record and no error, so the
// config write it was about to perform would have been unrecoverable.
func TestPrepareRecoveryFailsClosedForUnmappedClient(t *testing.T) {
	s := testService(t)
	const orphan = "unmapped-test-client"
	if err := s.Clients.Register(unmappedClientAdapter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clients.Get(orphan); err != nil {
		t.Fatalf("stub client must resolve through the registry, got %v", err)
	}
	records, err := s.PrepareClientConfigRecovery(orphan, "journal-orphan")
	if err == nil {
		t.Fatalf("unmapped client returned no error and records %v; want CLIENT_CONFIG_RECOVERY_UNSUPPORTED", records)
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error type = %T, want *anyerr.Error", err)
	}
	if ae.Code != "CLIENT_CONFIG_RECOVERY_UNSUPPORTED" {
		t.Fatalf("error code = %q, want CLIENT_CONFIG_RECOVERY_UNSUPPORTED", ae.Code)
	}
	if !strings.Contains(ae.Message, orphan) {
		t.Fatalf("error message %q must name the client %q so the fix is obvious", ae.Message, orphan)
	}
}

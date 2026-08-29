package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// providerEnv isolates tests from the real machine: fake HOME, no keychain.
type providerEnv struct {
	t        *testing.T
	Home     string
	Registry *Registry
	saved    map[string]string
}

func newProviderEnv(t *testing.T, extraEnv map[string]string) *providerEnv {
	t.Helper()
	env := &providerEnv{t: t, Home: t.TempDir(), saved: map[string]string{}}
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	for k, v := range extraEnv {
		if v == "" {
			continue
		}
		t.Setenv(k, v)
	}
	reg := NewRegistry()
	if err := RegisterBuiltin(reg, env.Home); err != nil {
		t.Fatal(err)
	}
	env.Registry = reg
	return env
}

func (e *providerEnv) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mustPath resolves a HOME-relative path for assertions.
func (e *providerEnv) mustPath(rel string) string {
	return filepath.Join(e.Home, rel)
}

func mustProvider(t *testing.T, reg *Registry, id string) Provider {
	t.Helper()
	pv, err := reg.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return pv
}

func fakeJWT(claims map[string]string) string {
	payload, _ := json.Marshal(claims)
	header := `{"alg":"none"}`
	return base64.RawURLEncoding.EncodeToString([]byte(header)) +
		"." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func errorsAsAnyProvider(err error, target **anyerr.Error) bool {
	for err != nil {
		if e, ok := err.(*anyerr.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

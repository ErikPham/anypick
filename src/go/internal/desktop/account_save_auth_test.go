package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountSaveAuthSnapshotBit(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	snap, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !findAccountProvider(t, snap, "codex").AcceptsAuthPaste {
		t.Fatal("codex does not accept auth paste")
	}
	if !findAccountProvider(t, snap, "claude").AcceptsAuthPaste {
		t.Fatal("claude does not accept auth paste")
	}
	for _, id := range []string{"deepseek", "gemini", "grok"} {
		if findAccountProvider(t, snap, id).AcceptsAuthPaste {
			t.Fatalf("%s accepts auth paste", id)
		}
	}
}

func TestAccountSaveAuthHandler(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	token := strings.Repeat("a", 14000)
	body := []byte(`{"tokens":{"id_token":"` + token + `"}}`)
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{
		"version": 1, "requestId": "paste-1", "operation": "account-save-auth",
		"providerId": "codex", "name": "wide", "path": path,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	line := "mutate\t" + base64.StdEncoding.EncodeToString(raw)
	if len(line) > 16*1024 {
		t.Fatalf("command is %d bytes", len(line))
	}
	if _, err := DecodeLine(line); err != nil {
		t.Fatal(err)
	}
	res := runCorrectnessMutation(t, srv, &out, payload)
	if res.Status != "success" || strings.Contains(res.Message, "aaaa") {
		t.Fatalf("result = %+v", res)
	}
	acct, err := env.Deps.Store.Get(context.Background(), "codex", "wide")
	if err != nil || acct == nil {
		t.Fatalf("saved = %+v %v", acct, err)
	}
	saved, err := os.ReadFile(filepath.Join(acct.SnapshotDir, "auth.json"))
	if err != nil || !bytes.Contains(saved, []byte(token)) {
		t.Fatalf("snapshot missing the pasted token: %v", err)
	}
	framed, err := json.Marshal(res)
	if err != nil || bytes.Contains(framed, []byte("aaaa")) {
		t.Fatalf("result frame contains the document: %s", framed)
	}

	secret := "paste-secret-token"
	rejected := map[string]interface{}{
		"version": 1, "requestId": "paste-key", "operation": "account-save-auth",
		"providerId": "codex", "name": "wide", "path": path, "apiKey": secret,
	}
	if _, err := DecodeLine("mutate\t" + b64(t, rejected)); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("apiKey payload err = %v", err)
	}

	versioned := map[string]interface{}{
		"version": 2, "requestId": "paste-v", "operation": "account-save-auth",
		"providerId": "codex", "path": path,
	}
	if _, err := DecodeLine("mutate\t" + b64(t, versioned)); err == nil || !strings.Contains(err.Error(), "unsupported version 2") {
		t.Fatalf("version err = %v", err)
	}

	target := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linked := map[string]interface{}{
		"version": 1, "requestId": "paste-link", "operation": "account-save-auth",
		"providerId": "codex", "name": "link", "path": link,
	}
	res = runCorrectnessMutation(t, srv, &out, linked)
	if res.Status != "error" {
		t.Fatalf("symlink = %+v", res)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("symlink target changed: %q %v", got, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	}
}

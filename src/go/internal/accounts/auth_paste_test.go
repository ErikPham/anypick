package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

func unsignedEmailJWT(email string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return header + "." + payload + ".sig"
}

func codexPaste(email, accountID, access string) string {
	tok := unsignedEmailJWT(email)
	if accountID == "" {
		return `{"tokens":{"access_token":"` + access + `","id_token":"` + tok + `"}}`
	}
	return `{"tokens":{"access_token":"` + access + `","id_token":"` + tok + `","account_id":"` + accountID + `"}}`
}

func pasteAccounts(t *testing.T, st *store.AccountStore, providerID string) []store.Account {
	t.Helper()
	list, err := st.ListAccounts(context.Background(), providerID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func readSnap(t *testing.T, acct *store.Account, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(acct.SnapshotDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSavePastedAuthCodex(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	live := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("CODEX_AUTH_PATH", live)
	if err := os.WriteFile(live, []byte("LIVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "super-secret-access-token"
	paste := codexPaste("work@openai.com", "", token)

	metas, err := svc.SavePastedAuth(ctx, "codex", "work", []byte(paste), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "work" || metas[0].Provider != "codex" || metas[0].Identity != "work@openai.com" {
		t.Fatalf("meta = %+v", metas)
	}
	if metas[0].CredentialClass != "" {
		t.Fatalf("credential class = %q", metas[0].CredentialClass)
	}
	acct, err := st.Get(ctx, "codex", "work")
	if err != nil || acct == nil {
		t.Fatalf("saved = %+v %v", acct, err)
	}
	if readSnap(t, acct, "auth.json") != paste {
		t.Fatalf("auth.json = %q", readSnap(t, acct, "auth.json"))
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "LIVE" {
		t.Fatalf("live auth = %q %v", got, err)
	}
	if active, err := st.GetActive(ctx, "codex"); err != nil || active != "" {
		t.Fatalf("active = %q %v", active, err)
	}

	_, err = svc.SavePastedAuth(ctx, "codex", "work", []byte(paste), false)
	ae := wantAnyErrCode(t, err, "ACCOUNT_EXISTS")
	if strings.Contains(ae.Error(), token) {
		t.Fatalf("exists error leaks the token: %s", ae.Error())
	}
	if len(pasteAccounts(t, st, "codex")) != 1 {
		t.Fatal("duplicate save wrote a second row")
	}

	_, err = svc.SavePastedAuth(ctx, "codex", "other", []byte(paste), false)
	ae = wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	if !strings.Contains(ae.Error(), "codex/work") {
		t.Fatalf("alias error = %s", ae.Error())
	}
	if len(pasteAccounts(t, st, "codex")) != 1 {
		t.Fatal("alias save wrote a second row")
	}

	if err := st.SetProxyConfigContext(ctx, "codex", "work", store.ProxyConfig{Enabled: true, Port: 4242}); err != nil {
		t.Fatal(err)
	}
	const next = "replacement-access-token"
	replaced := codexPaste("work@openai.com", "", next)
	if _, err := svc.SavePastedAuth(ctx, "codex", "work", []byte(replaced), true); err != nil {
		t.Fatal(err)
	}
	if len(pasteAccounts(t, st, "codex")) != 1 {
		t.Fatal("force replace wrote a second row")
	}
	acct, err = st.Get(ctx, "codex", "work")
	if err != nil || acct == nil {
		t.Fatal(err)
	}
	body := readSnap(t, acct, "auth.json")
	if !strings.Contains(body, next) || strings.Contains(body, token) {
		t.Fatalf("replaced auth.json = %q", body)
	}
	if !acct.Proxy.Enabled || acct.Proxy.Port != 4242 {
		t.Fatalf("proxy = %+v", acct.Proxy)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "LIVE" {
		t.Fatalf("live auth after replace = %q %v", got, err)
	}
}

func TestSavePastedAuthCodexList(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	a := codexPaste("work@openai.com", "acct-a", "tok-a")
	b := codexPaste("work@openai.com", "acct-b", "tok-b")
	raw := []byte("[" + a + "," + b + "]")

	metas, err := svc.SavePastedAuth(ctx, "codex", "", raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("metas = %+v", metas)
	}
	byName := map[string]store.Meta{}
	for _, meta := range metas {
		byName[meta.Name] = meta
		if meta.Identity != "work@openai.com" {
			t.Fatalf("identity = %q", meta.Identity)
		}
	}
	if _, ok := byName["acct-a"]; !ok {
		t.Fatalf("names = %+v", byName)
	}
	if _, ok := byName["acct-b"]; !ok {
		t.Fatalf("names = %+v", byName)
	}
	if active, err := st.GetActive(ctx, "codex"); err != nil || active != "" {
		t.Fatalf("active = %q %v", active, err)
	}

	_, err = svc.SavePastedAuth(ctx, "codex", "nope", raw, false)
	wantAnyErrCode(t, err, "INVALID_USAGE")
	if len(pasteAccounts(t, st, "codex")) != 2 {
		t.Fatal("named list wrote or deleted rows")
	}

	svc2, st2 := testService(t)
	_, err = svc2.SavePastedAuth(ctx, "codex", "", []byte("["+a+","+a+"]"), false)
	ae := wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	if !strings.Contains(ae.Error(), "same login") {
		t.Fatalf("duplicate paste = %s", ae.Error())
	}
	if len(pasteAccounts(t, st2, "codex")) != 0 {
		t.Fatal("duplicate paste wrote a row")
	}
}

func TestSavePastedAuthRejectsForeignShapes(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	const secret = "secret-token"
	cases := []string{
		`{"accessToken":"` + secret + `"}`,
		`{"access_token":"` + secret + `"}`,
		`{"accounts":[{"tokens":{"access_token":"` + secret + `"}}]}`,
	}
	for _, raw := range cases {
		_, err := svc.SavePastedAuth(ctx, "codex", "work", []byte(raw), false)
		ae := wantAnyErrCode(t, err, "AUTH_DOCUMENT_INVALID")
		if strings.Contains(ae.Error(), secret) {
			t.Fatalf("error leaks the document: %s", ae.Error())
		}
	}
	if len(pasteAccounts(t, st, "codex")) != 0 {
		t.Fatal("invalid paste wrote a row")
	}
}

func TestSavePastedAuthCodexConnectionRow(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	live := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("CODEX_AUTH_PATH", live)
	if err := os.WriteFile(live, []byte("LIVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
	  "accessToken": "at-conn",
	  "refreshToken": "rt-conn",
	  "idToken": "not-a-jwt",
	  "name": "ada@example.com",
	  "expiresAt": "2026-10-10T07:41:46.127Z",
	  "expiresIn": 864000,
	  "providerSpecificData": {
	    "chatgptAccountId": "acct-conn-1",
	    "chatgptPlanType": "plus"
	  }
	}`)
	metas, err := svc.SavePastedAuth(ctx, "codex", "", raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "ada-example.com" || metas[0].Identity != "ada@example.com" {
		t.Fatalf("meta = %+v", metas)
	}
	acct, err := st.Get(ctx, "codex", "ada-example.com")
	if err != nil || acct == nil {
		t.Fatal(err)
	}
	var saved struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh string `json:"last_refresh"`
	}
	if err := json.Unmarshal([]byte(readSnap(t, acct, "auth.json")), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.AuthMode != "chatgpt" || saved.Tokens.AccessToken != "at-conn" ||
		saved.Tokens.RefreshToken != "rt-conn" || saved.Tokens.IDToken != "not-a-jwt" ||
		saved.Tokens.AccountID != "acct-conn-1" ||
		saved.LastRefresh != "2026-09-30T07:41:46.127Z" {
		t.Fatalf("auth.json = %+v", saved)
	}
	if strings.Contains(readSnap(t, acct, "auth.json"), "accessToken") {
		t.Fatal("stored the connection row instead of auth.json")
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "LIVE" {
		t.Fatalf("live codex = %q %v", got, err)
	}

	same := []byte(`{"refreshToken":"rt-conn","expiresIn":864000,"accessToken":"at-conn","idToken":"not-a-jwt","expiresAt":"2026-10-10T07:41:46.127Z","name":"ada@example.com","providerSpecificData":{"chatgptPlanType":"plus","chatgptAccountId":"acct-conn-1"}}`)
	_, err = svc.SavePastedAuth(ctx, "codex", "other", same, false)
	wantAnyErrCode(t, err, "ACCOUNT_SNAPSHOT_EXISTS")
	if len(pasteAccounts(t, st, "codex")) != 1 {
		t.Fatal("same connection wrote a second row")
	}
}

func TestSavePastedAuthCodexAPIKeyNeedsName(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	raw := []byte(`{"OPENAI_API_KEY":"sk-test"}`)
	metas, err := svc.SavePastedAuth(ctx, "codex", "key", raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Identity != "API key" || metas[0].Name != "key" {
		t.Fatalf("meta = %+v", metas)
	}
	acct, err := st.Get(ctx, "codex", "key")
	if err != nil || acct == nil || readSnap(t, acct, "auth.json") != string(raw) {
		t.Fatalf("snapshot = %+v %v", acct, err)
	}
	_, err = svc.SavePastedAuth(ctx, "codex", "", raw, false)
	wantAnyErrCode(t, err, "AUTH_PASTE_NAME_REQUIRED")
}

func TestSavePastedAuthClaude(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	live := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", live)
	if err := os.WriteFile(live, []byte("LIVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"claudeAiOauth":{"email":"c@anthropic.com","accessToken":"ca-tok","refreshToken":"cr-tok"}}`
	metas, err := svc.SavePastedAuth(ctx, "claude", "mine", []byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "mine" || metas[0].Identity != "c@anthropic.com" {
		t.Fatalf("meta = %+v", metas)
	}
	acct, err := st.Get(ctx, "claude", "mine")
	if err != nil || acct == nil {
		t.Fatal(err)
	}
	var snap struct {
		Version    int    `json:"version"`
		Account    string `json:"account"`
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(readSnap(t, acct, "credentials.json")), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Version != 1 || snap.Account != "c@anthropic.com" || snap.Credential != raw {
		t.Fatalf("wrapper = %+v", snap)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "LIVE" {
		t.Fatalf("live claude = %q %v", got, err)
	}

	wrapper := `{"version":1,"account":"c@anthropic.com","credential":"{\"claudeAiOauth\":{\"email\":\"c@anthropic.com\",\"accessToken\":\"ca-tok\"}}"}`
	if _, err := svc.SavePastedAuth(ctx, "claude", "wrap", []byte(wrapper), false); err != nil {
		t.Fatal(err)
	}
	acct, err = st.Get(ctx, "claude", "wrap")
	if err != nil || acct == nil {
		t.Fatal(err)
	}
	if readSnap(t, acct, "credentials.json") != wrapper {
		t.Fatalf("wrapper bytes = %q", readSnap(t, acct, "credentials.json"))
	}
}

func TestSavePastedAuthClaudeConnectionRow(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	live := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", live)
	if err := os.WriteFile(live, []byte("LIVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"accessToken":"ca-flat","refreshToken":"cr-flat","name":"c@anthropic.com","expiresAt":"2026-10-10T00:00:00Z"}`
	metas, err := svc.SavePastedAuth(ctx, "claude", "", []byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "c-anthropic.com" || metas[0].Identity != "c@anthropic.com" {
		t.Fatalf("meta = %+v", metas)
	}
	acct, err := st.Get(ctx, "claude", "c-anthropic.com")
	if err != nil || acct == nil {
		t.Fatal(err)
	}
	var snap struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(readSnap(t, acct, "credentials.json")), &snap); err != nil {
		t.Fatal(err)
	}
	var cred struct {
		OAuth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			Email        string `json:"email"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(snap.Credential), &cred); err != nil {
		t.Fatal(err)
	}
	if cred.OAuth.AccessToken != "ca-flat" || cred.OAuth.RefreshToken != "cr-flat" ||
		cred.OAuth.Email != "c@anthropic.com" || cred.OAuth.ExpiresAt != "2026-10-10T00:00:00Z" {
		t.Fatalf("credential = %+v", cred.OAuth)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "LIVE" {
		t.Fatalf("live claude = %q %v", got, err)
	}

	chatgpt := `{"accessToken":"at-conn","idToken":"not-a-jwt","name":"ada@example.com","providerSpecificData":{"chatgptAccountId":"acct-conn-1"}}`
	_, err = svc.SavePastedAuth(ctx, "claude", "nope", []byte(chatgpt), false)
	ae := wantAnyErrCode(t, err, "AUTH_DOCUMENT_INVALID")
	if strings.Contains(ae.Error(), "at-conn") {
		t.Fatalf("error leaks the document: %s", ae.Error())
	}
	if len(pasteAccounts(t, st, "claude")) != 1 {
		t.Fatal("chatgpt row wrote a claude account")
	}
}

func TestSavePastedAuthUnsupportedProviders(t *testing.T) {
	svc, _ := testService(t)
	if err := provider.RegisterBuiltinCompatibles(svc.Providers, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := []struct{ id, name string }{
		{"deepseek", "DeepSeek"},
		{"gemini", "Google Gemini"},
		{"grok", "xAI Grok"},
		{"opencode", "OpenCode"},
		{"kiro", "Kiro"},
		{"cline", "Cline"},
		{"workbuddy", "WorkBuddy AI"},
		{"zai", "Z.ai (GLM)"},
		{"cloudflare-aigateway", "Cloudflare AI Gateway"},
	}
	for _, tc := range cases {
		_, err := svc.SavePastedAuth(ctx, tc.id, "n", []byte(`{"accessToken":"x"}`), false)
		ae := wantAnyErrCode(t, err, "UNSUPPORTED_AUTH_PASTE")
		if !strings.HasPrefix(ae.Error(), tc.name+" ") {
			t.Fatalf("%s message = %q", tc.id, ae.Error())
		}
	}
}

func TestSavePastedAuthBounds(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	_, err := svc.SavePastedAuth(ctx, "codex", "big", make([]byte, provider.MaxAuthPasteBytes+1), false)
	wantAnyErrCode(t, err, "AUTH_PASTE_TOO_LARGE")
	_, err = svc.SavePastedAuth(ctx, "codex", "bad", []byte("{"), false)
	ae := wantAnyErrCode(t, err, "AUTH_DOCUMENT_INVALID")
	if ae.Error() != "Auth JSON is not valid." {
		t.Fatalf("bad json = %q", ae.Error())
	}
}

func TestSavePastedAuthReentersProviderLock(t *testing.T) {
	svc, st := testService(t)
	paste := []byte(codexPaste("work@openai.com", "", "tok-lock"))
	errCh := make(chan error, 1)
	go func() {
		errCh <- lock.WithMutationLock(context.Background(), st.Root, refs.ProviderScope("codex"), func(c context.Context) error {
			metas, err := svc.SavePastedAuth(c, "codex", "work", paste, false)
			if err != nil {
				return err
			}
			if len(metas) != 1 || metas[0].Name != "work" {
				return errors.New("paste under the provider lock did not save work")
			}
			return nil
		})
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SavePastedAuth deadlocked under the provider lock")
	}
}

package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/store"
)

// fakeLauncher writes an executable script and returns its absolute path
// (absolute names short-circuit PATH resolution in browserlogin).
func loginLauncher(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-grok-login.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantCode(t *testing.T, err error) string {
	t.Helper()
	var ae *anyerr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want AnyPickError, got %v", err)
	}
	return ae.Code
}

func newLoginService(t *testing.T) (*Service, *store.AccountStore, string) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	home := t.TempDir()
	authPath := filepath.Join(home, ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", authPath)
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	return New(st, reg), st, authPath
}

func grokAuthBody(email string) string {
	return `{"sessions":{"s1":{"email":"` + email + `","access_token":"tok"}}}`
}

func TestLoginMethodsForProviderWithoutCapability(t *testing.T) {
	svc, _, _ := newLoginService(t)
	methods, err := svc.LoginMethods(context.Background(), "gemini", "")
	if err != nil {
		t.Fatalf("LoginMethods: %v", err)
	}
	if len(methods) != 0 {
		t.Fatalf("gemini must not advertise browser sign-in: %+v", methods)
	}
}

func TestLoginMethodsAvailabilityFollowsLauncher(t *testing.T) {
	// Scrub PATH so host-installed launchers cannot flip availability.
	emptyPath := t.TempDir()
	svc, _, _ := newLoginService(t)

	// No launcher on PATH and no extra path present → unavailable with reason.
	t.Setenv("PATH", emptyPath)
	methods, err := svc.LoginMethods(context.Background(), "grok", "")
	if err != nil {
		t.Fatalf("LoginMethods: %v", err)
	}
	if len(methods) != 1 || methods[0].Available {
		t.Fatalf("expected single unavailable method, got %+v", methods)
	}
	if methods[0].UnavailableReason == "" {
		t.Fatal("missing unavailableReason")
	}

	// With the launcher present the same method becomes available.
	svc2, _, _ := newLoginService(t)
	t.Setenv("GROK_BINARY", loginLauncher(t, "exit 0"))
	methods, err = svc2.LoginMethods(context.Background(), "grok", "")
	if err != nil {
		t.Fatalf("LoginMethods: %v", err)
	}
	if len(methods) != 1 || !methods[0].Available || methods[0].ID != "grok-subscription" {
		t.Fatalf("available method mismatch: %+v", methods)
	}
	if methods[0].Provenance != "official" || !methods[0].Preferred {
		t.Fatalf("provenance/preferred lost: %+v", methods[0])
	}
}

func TestLoginEndToEndCreatesAccountAndActivates(t *testing.T) {
	svc, st, authPath := newLoginService(t)
	script := loginLauncher(t, "mkdir -p \"$(dirname \""+authPath+"\")\" && printf '%s' '"+grokAuthBody("user@example.com")+"' > "+authPath)
	t.Setenv("GROK_BINARY", script)

	res, err := svc.Login(context.Background(), "grok", LoginOptions{Name: "work"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Provider != "grok" || res.Account.Name == "" {
		t.Fatalf("result = %#v", res)
	}
	if res.Method.ID != "grok-subscription" {
		t.Fatalf("method = %+v", res.Method)
	}
	if res.BackedUpTo != nil {
		t.Fatalf("no stash expected without prior live login: %v", *res.BackedUpTo)
	}
	acct, gerr := st.Get(context.Background(), "grok", res.Account.Name)
	if gerr != nil || acct == nil {
		t.Fatalf("account not persisted: %v", gerr)
	}
	active, aerr := st.GetActive(context.Background(), "grok")
	if aerr != nil || active != res.Account.Name {
		t.Fatalf("active = %q, want %q", active, res.Account.Name)
	}
}

func TestLoginReusesIdentitySnapshotName(t *testing.T) {
	svc, st, authPath := newLoginService(t)
	writeAuth := loginLauncher(t, "mkdir -p \"$(dirname \""+authPath+"\")\" && printf '%s' '"+grokAuthBody("same@example.com")+"' > "+authPath)

	// First login creates an identity-named account.
	t.Setenv("GROK_BINARY", writeAuth)
	first, err := svc.Login(context.Background(), "grok", LoginOptions{Name: "first-run"})
	if err != nil {
		t.Fatalf("first Login: %v", err)
	}

	// A second login for the same identity must refresh that account, not alias.
	second, err := svc.Login(context.Background(), "grok", LoginOptions{})
	if err != nil {
		t.Fatalf("second Login: %v", err)
	}
	if second.Account.Name != first.Account.Name {
		t.Fatalf("identity reuse failed: %q vs %q", first.Account.Name, second.Account.Name)
	}
	list, _ := st.ListAccounts(context.Background(), "grok")
	if len(list) != 1 {
		t.Fatalf("expected one account, got %d", len(list))
	}
}

func TestLoginStashesPriorLiveLoginAndSetsBackedUpTo(t *testing.T) {
	svc, _, authPath := newLoginService(t)
	// Pre-existing live login (different identity).
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := grokAuthBody("old@example.com")
	if err := os.WriteFile(authPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	newAuth := grokAuthBody("new@example.com")
	script := loginLauncher(t, "printf '%s' '"+newAuth+"' > "+authPath)
	t.Setenv("GROK_BINARY", script)

	res, err := svc.Login(context.Background(), "grok", LoginOptions{Name: "brand-new"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.BackedUpTo == nil || *res.BackedUpTo == "" {
		t.Fatal("prior live login was not stashed")
	}
	// The new login is now live and active.
	live, derr := svc.Providers.Get("grok")
	if derr != nil {
		t.Fatal(derr)
	}
	status, derr := live.DetectLive()
	if derr != nil || !status.Present {
		t.Fatalf("live after login = %+v (%v)", status, derr)
	}
}

func TestLoginFailureRollsBackLiveAuth(t *testing.T) {
	svc, _, authPath := newLoginService(t)
	old := grokAuthBody("keep@example.com")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	failing := loginLauncher(t, "exit 7")
	t.Setenv("GROK_BINARY", failing)

	before, _ := svc.Store.ListAccounts(context.Background(), "grok")
	_, err := svc.Login(context.Background(), "grok", LoginOptions{})
	if err == nil || wantCode(t, err) != "PROVIDER_LOGIN_FAILED" {
		t.Fatalf("want PROVIDER_LOGIN_FAILED, got %v", err)
	}
	data, rerr := os.ReadFile(authPath)
	if rerr != nil || string(data) != old {
		t.Fatalf("live auth not restored: %v %q", rerr, data)
	}
	after, _ := svc.Store.ListAccounts(context.Background(), "grok")
	if len(after) < len(before) {
		t.Fatal("stash snapshot was deleted on rollback")
	}
}

func TestLoginUnknownMethodRejected(t *testing.T) {
	svc, _, _ := newLoginService(t)
	t.Setenv("GROK_BINARY", loginLauncher(t, "exit 0"))
	_, err := svc.Login(context.Background(), "grok", LoginOptions{MethodID: "nope"})
	if err == nil || wantCode(t, err) != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("unknown method: %v", err)
	}
}

func TestSelectLoginMethodPreferenceOrdering(t *testing.T) {
	officialPreferred := provider.LoginMethod{ID: "a", Label: "B", Provenance: "community", Available: true}
	m := []provider.LoginMethod{
		officialPreferred,
		{ID: "b", Label: "A", Provenance: "official", Preferred: true, Available: true},
	}
	got, err := selectLoginMethod(m, "")
	if err != nil || got.ID != "b" {
		t.Fatalf("official preferred must win: %v %v", got, err)
	}

	ambiguous := []provider.LoginMethod{
		{ID: "x", Provenance: "official", Available: true},
		{ID: "y", Provenance: "official", Available: true},
	}
	if _, err := selectLoginMethod(ambiguous, ""); err == nil ||
		wantCode(t, err) != "PROVIDER_LOGIN_METHOD_REQUIRED" {
		t.Fatalf("ambiguity must be explicit: %v", err)
	}

	requested := []provider.LoginMethod{{ID: "only"}}
	if got, err := selectLoginMethod(requested, "only"); err != nil || got.ID != "only" {
		t.Fatalf("requested method: %v %v", got, err)
	}
	_, err = selectLoginMethod(requested, "other")
	if err == nil || wantCode(t, err) != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("unknown id must fail: %v", err)
	}
}

func TestLoginCancelInCommitWindowRollsBack(t *testing.T) {
	svc, st, authPath := newLoginService(t)

	// Wrap the real grok provider so that when its launcher finishes, the
	// request context is already cancelled: the launcher writes auth.json and
	// THEN we cancel — exactly the late-cancel race.
	launcher := loginLauncher(t,
		"mkdir -p \"$(dirname "+authPath+")\" && printf '%s' '"+grokAuthBody("race@example.com")+"' > "+authPath+
			" && sleep 0.2")
	t.Setenv("GROK_BINARY", launcher)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the launcher child exits but before DetectLive commits:
	// poll for the credential file from a goroutine.
	go func() {
		for {
			if _, err := os.Stat(authPath); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	_, err := svc.Login(ctx, "grok", LoginOptions{Name: "racy"})
	if err == nil {
		t.Fatal("cancelled login must not succeed")
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_CANCELLED" {
		t.Fatalf("want PROVIDER_LOGIN_CANCELLED, got %v", err)
	}
	// No account may be committed by the abandoned request.
	list, lerr := st.ListAccounts(context.Background(), "grok")
	if lerr != nil {
		t.Fatal(lerr)
	}
	for _, acct := range list {
		if acct.Meta.Identity != "" && strings.Contains(acct.Meta.Identity, "race@example.com") {
			t.Fatalf("cancelled login committed state: %+v", acct.Meta)
		}
	}
	if active, _ := st.GetActive(context.Background(), "grok"); active != "" {
		t.Fatalf("active pointer set despite cancellation: %q", active)
	}
}

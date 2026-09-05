package desktop

// account-save-input: the native Add Account sheet's typed-credential path
// for compatible vendors (API keys / plan keys). These tests pin the
// contract: snapshot advertises kinds+profiles, the handler saves through
// Accounts.SaveInput, and malformed payloads are rejected at the boundary.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func findAccountProvider(t *testing.T, snap *Snapshot, id string) AccountProviderRow {
	t.Helper()
	for _, row := range snap.AccountProviders {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no accountProviders row for %q", id)
	return AccountProviderRow{}
}

func TestAccountSaveInputSnapshotExposesCredentialKinds(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	snap, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	deepseek := findAccountProvider(t, snap, "deepseek")
	if len(deepseek.CredentialKinds) != 1 {
		t.Fatalf("deepseek kinds = %+v, want one api-key kind", deepseek.CredentialKinds)
	}
	kind := deepseek.CredentialKinds[0]
	if kind.Kind != "api-key" || len(kind.Profiles) == 0 || kind.Profiles[0].ID == "" {
		t.Fatalf("deepseek kind = %+v, want api-key with profiles", kind)
	}

	mimo := findAccountProvider(t, snap, "mimo")
	if len(mimo.CredentialKinds) != 2 {
		t.Fatalf("mimo kinds = %+v, want api-key + subscription-key", mimo.CredentialKinds)
	}

	// Native-only providers publish no credential input: their accounts come
	// from a live login or a browser sign-in.
	claude := findAccountProvider(t, snap, "claude")
	if len(claude.CredentialKinds) != 0 {
		t.Fatalf("claude kinds = %+v, want none", claude.CredentialKinds)
	}
}

func TestAccountSaveInputHandler(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	save := func(requestID string, payload map[string]interface{}) Result {
		t.Helper()
		payload["version"] = 1
		payload["requestId"] = requestID
		payload["operation"] = "account-save-input"
		return runCorrectnessMutation(t, srv, &out, payload)
	}

	res := save("save-input-1", map[string]interface{}{
		"providerId": "deepseek", "name": "work",
		"kind": "api-key", "profile": "global", "apiKey": "sk-test",
	})
	if res.Status != "success" {
		t.Fatalf("save-input = %+v", res)
	}
	acct, err := env.Deps.Store.Get("deepseek", "work")
	if err != nil || acct == nil {
		t.Fatalf("saved account = %+v, %v", acct, err)
	}

	// An omitted profile selects the kind's first endpoint profile.
	res = save("save-input-2", map[string]interface{}{
		"providerId": "deepseek", "name": "second",
		"kind": "api-key", "apiKey": "sk-test",
	})
	if res.Status != "success" {
		t.Fatalf("save-input without profile = %+v", res)
	}

	// Duplicates fail without overwrite, exactly like account-save.
	res = save("save-input-3", map[string]interface{}{
		"providerId": "deepseek", "name": "work",
		"kind": "api-key", "profile": "global", "apiKey": "sk-other",
	})
	if res.Status != "error" {
		t.Fatalf("duplicate save-input = %+v, want error", res)
	}

	// Unknown kinds fail with the provider's coded error, not a silent save.
	res = save("save-input-4", map[string]interface{}{
		"providerId": "deepseek", "name": "nope",
		"kind": "oauth", "profile": "global", "apiKey": "sk-test",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "does not accept credential kind") {
		t.Fatalf("unknown kind = %+v, want UNSUPPORTED_CREDENTIAL_INPUT", res)
	}

	// Native-only providers reject credential input.
	res = save("save-input-5", map[string]interface{}{
		"providerId": "claude", "name": "work",
		"kind": "api-key", "profile": "global", "apiKey": "sk-test",
	})
	if res.Status != "error" {
		t.Fatalf("native save-input = %+v, want error", res)
	}
}

func TestAccountSaveInputContract(t *testing.T) {
	valid := map[string]interface{}{
		"version": 1, "requestId": "r", "operation": "account-save-input",
		"providerId": "deepseek", "name": "work",
		"kind": "api-key", "profile": "global", "apiKey": "sk-test",
	}
	if _, err := DecodeLine("mutate\t" + b64(t, valid)); err != nil {
		t.Fatalf("valid save-input rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(m map[string]interface{})
	}{
		{"missing kind", func(m map[string]interface{}) { delete(m, "kind") }},
		{"empty kind", func(m map[string]interface{}) { m["kind"] = "" }},
		{"missing apiKey", func(m map[string]interface{}) { delete(m, "apiKey") }},
		{"empty apiKey", func(m map[string]interface{}) { m["apiKey"] = "" }},
		{"missing name", func(m map[string]interface{}) { delete(m, "name") }},
		{"missing providerId", func(m map[string]interface{}) { delete(m, "providerId") }},
		{"unknown field", func(m map[string]interface{}) { m["region"] = "us" }},
		{"label not allowed", func(m map[string]interface{}) { m["label"] = "Work" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]interface{}{}
			for k, v := range valid {
				m[k] = v
			}
			tc.mutate(m)
			if _, err := DecodeLine("mutate\t" + b64(t, m)); err == nil {
				t.Fatalf("%s: payload accepted", tc.name)
			}
		})
	}
}

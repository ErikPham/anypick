package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// Doctor must surface legacy duplicate snapshots with an actionable removal
// command: the save guard only stops new pairs, it cannot clean old ones.
func TestDoctorReportsDuplicateSnapshots(t *testing.T) {
	isolateEnv(t)
	home := os.Getenv("HOME")
	authPath := filepath.Join(home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	live := []byte(`{"sess":{"email":"dup@grok.xai"}}`)
	if err := os.WriteFile(authPath, live, 0o600); err != nil {
		t.Fatal(err)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Accounts.Save(ctx, "grok", "first", false); err != nil {
		closeFn()
		t.Fatalf("save first: %v", err)
	}
	// Seed the pre-guard state the service now refuses to create on its own.
	snap, _, err := a.Store.PrepareSnapshot("grok", "second")
	if err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), live, 0o600); err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := a.Store.WriteMetaContext(ctx, store.Meta{
		Name: "second", Provider: "grok",
	}, snap); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	clearGlobalsForParity()
	code := -1
	out, errOut := captureBoth(t, func() { code = run([]string{"--json", "doctor"}) })
	clearGlobalsForParity()
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("doctor exited %d stdout=%q stderr=%q", code, out, errOut)
	}

	var report struct {
		Status   string `json:"status"`
		Findings []struct {
			Check  string `json:"check"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor JSON = %v: %s", err, out)
	}
	if report.Status != "degraded" {
		t.Fatalf("doctor status = %q, want degraded", report.Status)
	}
	for _, f := range report.Findings {
		if f.Check != "duplicate-snapshot" {
			continue
		}
		if f.Status != "warn" {
			t.Fatalf("duplicate finding status = %q, want warn", f.Status)
		}
		for _, want := range []string{
			"grok/first", "grok/second", "keep grok/first", "anypick account remove grok/second",
		} {
			if !strings.Contains(f.Detail, want) {
				t.Fatalf("detail %q lacks %q", f.Detail, want)
			}
		}
		return
	}
	t.Fatalf("no duplicate-snapshot finding in %s", out)
}

// Doctor must not suggest removing an account a global binding still points at:
// the two live names need a re-bind first.
func TestDoctorKeepsBoundDuplicateAccounts(t *testing.T) {
	isolateEnv(t)
	home := os.Getenv("HOME")
	authPath := filepath.Join(home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	live := []byte(`{"sess":{"email":"bound@grok.xai"}}`)
	if err := os.WriteFile(authPath, live, 0o600); err != nil {
		t.Fatal(err)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Accounts.Save(ctx, "grok", "first", false); err != nil {
		closeFn()
		t.Fatalf("save first: %v", err)
	}
	snap, _, err := a.Store.PrepareSnapshot("grok", "second")
	if err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), live, 0o600); err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := a.Store.WriteMetaContext(ctx, store.Meta{
		Name: "second", Provider: "grok",
	}, snap); err != nil {
		closeFn()
		t.Fatal(err)
	}
	// second is active while claude still routes through first.
	if err := a.Store.SetActiveContext(ctx, "grok", "second"); err != nil {
		closeFn()
		t.Fatal(err)
	}
	if _, err := a.BindingSvc.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec:   bindings.Spec{Client: "claude", Source: refs.AccountRef("grok", "first")},
	}); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	clearGlobalsForParity()
	code := -1
	out, errOut := captureBoth(t, func() { code = run([]string{"--json", "doctor"}) })
	clearGlobalsForParity()
	if code != int(anyerr.Success) || errOut != "" {
		t.Fatalf("doctor exited %d stdout=%q stderr=%q", code, out, errOut)
	}
	var report struct {
		Findings []struct {
			Check  string `json:"check"`
			Detail string `json:"detail"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor JSON = %v: %s", err, out)
	}
	for _, f := range report.Findings {
		if f.Check != "duplicate-snapshot" {
			continue
		}
		if !strings.Contains(f.Detail, "bound to claude") {
			t.Fatalf("detail %q does not name the binding", f.Detail)
		}
		if strings.Contains(f.Detail, "anypick account remove") {
			t.Fatalf("doctor suggested removing a bound account: %q", f.Detail)
		}
		return
	}
	t.Fatalf("no duplicate-snapshot finding in %s", out)
}

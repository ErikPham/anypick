package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/pools"
)

func TestPoolMemberCommandPreservesPoolConfiguration(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		if _, err := a.Accounts.SaveInput(ctx, "deepseek", name, accounts.CredentialInput{
			Kind: "api-key", Secret: "sk-" + name, Profile: "global",
		}, true); err != nil {
			closeFn()
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	wantMembers := []pools.Member{
		{Account: "alpha", Enabled: true},
		{Account: "beta", Enabled: true},
	}
	if err := a.PoolSvc.Set(ctx, pools.Pool{
		Provider: "deepseek", Mode: "single", Enabled: false,
		Strategy: "round-robin", Host: "127.0.0.1", Port: 43123,
		Members: wantMembers,
	}); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	var code int
	captureBoth(t, func() {
		code = run([]string{"proxy", "pool", "member", "deepseek", "alpha", "off"})
	})
	if code != int(anyerr.Success) {
		t.Fatalf("pool member exited %d", code)
	}

	got, err := openPoolForTest(t)
	if err != nil || got == nil {
		t.Fatalf("pool after member toggle = %#v, %v", got, err)
	}
	if got.Mode != "single" || got.Enabled || got.Strategy != "round-robin" ||
		got.Host != "127.0.0.1" || got.Port != 43123 {
		t.Fatalf("member toggle changed pool configuration: %#v", got)
	}
	wantMembers[0].Enabled = false
	if !reflect.DeepEqual(got.Members, wantMembers) {
		t.Fatalf("members = %#v, want %#v", got.Members, wantMembers)
	}
}

func TestPoolMemberCommandRequiresExplicitValidState(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	for _, args := range [][]string{
		{"proxy", "pool", "member", "deepseek", "alpha"},
		{"proxy", "pool", "member", "deepseek", "alpha", "maybe"},
		{"proxy", "pool", "member", "deepseek", "alpha", "on", "extra"},
	} {
		var code int
		captureBoth(t, func() { code = run(args) })
		if code != int(anyerr.InvalidUsage) {
			t.Errorf("%v exited %d, want %d", args, code, anyerr.InvalidUsage)
		}
	}
}

func TestPoolMemberStateMatchesTypeScriptAliases(t *testing.T) {
	for _, tc := range []struct {
		state   string
		enabled bool
		valid   bool
	}{
		{"on", true, true}, {"enable", true, true}, {"1", true, true},
		{"off", false, true}, {"disable", false, true}, {"0", false, true},
		{"true", false, false}, {"", false, false},
	} {
		enabled, valid := poolMemberState(tc.state)
		if enabled != tc.enabled || valid != tc.valid {
			t.Errorf("poolMemberState(%q) = %v,%v; want %v,%v",
				tc.state, enabled, valid, tc.enabled, tc.valid)
		}
	}
}

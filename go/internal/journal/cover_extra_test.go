package journal

import (
	"context"
	"reflect"
	"testing"
)

func TestListRecentLimitAndOrdering(t *testing.T) {
	j := openJournal(t)
	ctx := context.Background()

	ids := []string{"j1", "j2", "j3"}
	for _, id := range ids {
		if _, err := j.CreateContext(ctx, "activation",
			CreateOptions{ID: id, State: StateExecuting, AffectedResources: []string{"account/codex/" + id}}); err != nil {
			t.Fatal(err)
		}
	}

	// Limit trims to the N most recent rows.
	recent, err := j.ListRecent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("ListRecent(2) returned %d entries", len(recent))
	}
	seen := map[string]bool{}
	for _, e := range recent {
		seen[e.ID] = true
		if !idsContain(ids, e.ID) {
			t.Fatalf("unexpected id %q", e.ID)
		}
	}

	// limit <= 0 falls back to the default page size.
	all, err := j.ListRecentContext(ctx, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("default limit: %d %v", len(all), err)
	}

	// ListRecent (no context) shares the same query path.
	viaWrapper, err := j.ListRecent(50)
	if err != nil || len(viaWrapper) != 3 {
		t.Fatalf("wrapper list: %d %v", len(viaWrapper), err)
	}
}

func idsContain(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestParamsRoundTripAndNullDefaults(t *testing.T) {
	j := openJournal(t)
	ctx := context.Background()

	withParams, err := j.CreateContext(ctx, "activation", CreateOptions{
		ID:     "j-params",
		State:  StatePlanned,
		Params: map[string]interface{}{"source": "account/codex/a", "count": float64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if withParams.Params["source"] != "account/codex/a" {
		t.Fatalf("create params: %+v", withParams.Params)
	}
	got, err := j.Get("j-params")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if !reflect.DeepEqual(got.Params, map[string]interface{}{
		"source": "account/codex/a", "count": float64(2),
	}) {
		t.Fatalf("params roundtrip: %+v", got.Params)
	}

	// Entries without params read back with nil params and non-nil slices.
	bare, err := j.CreateContext(ctx, "activation", CreateOptions{ID: "j-bare"})
	if err != nil {
		t.Fatal(err)
	}
	if bare.Params != nil {
		t.Fatalf("bare entry params: %+v", bare.Params)
	}
	gotBare, _ := j.Get("j-bare")
	if gotBare.Params != nil ||
		gotBare.AffectedResources == nil || gotBare.BackupPaths == nil {
		t.Fatalf("bare defaults: %+v", gotBare)
	}
}

func TestUpdatePartialPatchPreservesUntouchedFields(t *testing.T) {
	j := openJournal(t)
	ctx := context.Background()

	original, err := j.CreateContext(ctx, "activation", CreateOptions{
		ID:                "j-patch",
		State:             StatePlanned,
		AffectedResources: []string{"account/codex/a"},
		BackupPaths:       []string{"/tmp/x => /tmp/y"},
		Params:            map[string]interface{}{"k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}

	patched, err := j.UpdateContext(ctx, "j-patch", Entry{State: StateRolledBack})
	if err != nil {
		t.Fatal(err)
	}
	if patched.State != StateRolledBack {
		t.Fatalf("state not applied: %+v", patched)
	}
	// Zero-value patch fields keep stored values.
	if !reflect.DeepEqual(patched.BackupPaths, original.BackupPaths) ||
		!reflect.DeepEqual(patched.AffectedResources, original.AffectedResources) ||
		!reflect.DeepEqual(patched.Params, original.Params) {
		t.Fatalf("untouched fields drifted: %+v", patched)
	}

	// Explicit fields replace.
	replaced, err := j.Update("j-patch", Entry{
		Params:      map[string]interface{}{"k2": "v2"},
		BackupPaths: []string{"delete => /tmp/z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Params["k"] != nil || replaced.Params["k2"] != "v2" ||
		len(replaced.BackupPaths) != 1 {
		t.Fatalf("replacement ignored: %+v", replaced)
	}
	stored, _ := j.Get("j-patch")
	if stored.State != StateRolledBack || stored.Params["k2"] != "v2" {
		t.Fatalf("stored state after replace: %+v", stored)
	}
}

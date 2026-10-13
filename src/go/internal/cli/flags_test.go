package cli

import (
	"reflect"
	"testing"
)

func TestReorder(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"positional first then flag", []string{"myprov", "--name", "x"}, []string{"--name", "x", "myprov"}},
		{"already flag first", []string{"--name", "x", "myprov"}, []string{"--name", "x", "myprov"}},
		{"bool flag without value", []string{"myprov", "--force"}, []string{"--force", "myprov"}},
		{"two positionals", []string{"a", "b"}, []string{"a", "b"}},
		{"flag with equals", []string{"a", "--name=x"}, []string{"--name=x", "a"}},
		{"boolean flag before positional", []string{"--all", "codex"}, []string{"--all", "codex"}},
		{"boolean flag between positionals", []string{"codex", "--all", "work"}, []string{"--all", "codex", "work"}},
		{"current before provider", []string{"--current", "codex"}, []string{"--current", "codex"}},
		{"stdin sentinel is a value", []string{"codex", "--auth-file", "-"}, []string{"--auth-file", "-", "codex"}},
		{"empty", nil, nil},
	}
	for _, tc := range tests {
		got := Reorder(tc.in)
		// nil vs empty: treat both as empty.
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Reorder(%v)=%v want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestWantsHelp(t *testing.T) {
	if !WantsHelp([]string{"--help"}) || !WantsHelp([]string{"-h"}) || !WantsHelp([]string{"help"}) {
		t.Fatal("expected help args")
	}
	if WantsHelp(nil) || WantsHelp([]string{"--json"}) {
		t.Fatal("unexpected help")
	}
}

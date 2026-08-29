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

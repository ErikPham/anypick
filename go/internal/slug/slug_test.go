package slug

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"Kiro Key - 1k", "kiro-key-1k", false},
		{"café", "cafe", false},
		{"  hello world  ", "hello-world", false},
		{"a", "a", false},
		{"", "", true},
		{"   ", "", true},
		{"---", "", true},
	}
	for _, tc := range tests {
		got, err := Normalize(tc.raw, "Account name")
		if tc.wantErr {
			if err == nil {
				t.Errorf("Normalize(%q) want error, got %q", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Normalize(%q)=%q want %q", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizePathSafety(t *testing.T) {
	cases := []string{"../etc/passwd", "a/b\\c", "a..b", "..", "a/../b"}
	for _, raw := range cases {
		got, err := Normalize(raw, "Account name")
		if err != nil {
			continue // rejected is safe
		}
		if strings.Contains(got, "/") || strings.Contains(got, "\\") {
			t.Errorf("Normalize(%q)=%q contains path separator", raw, got)
		}
		if got == ".." || strings.Contains(got, "..") && strings.Contains(raw, "..") {
			// collapsed dots should not leave parent refs; single dots ok but ".." must not survive as segment
			if got == ".." {
				t.Errorf("Normalize(%q)=%q is parent ref", raw, got)
			}
		}
	}
}

func TestNormalizeMaxLen(t *testing.T) {
	long := strings.Repeat("a", MaxSlugLen+20)
	got, err := Normalize(long, "Account name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > MaxSlugLen {
		t.Fatalf("len %d > MaxSlugLen %d", len(got), MaxSlugLen)
	}
	if !isAlnum(got[0]) {
		t.Fatalf("must start with alnum: %q", got)
	}
}

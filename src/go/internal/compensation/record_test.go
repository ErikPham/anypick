package compensation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ── unit tests ────────────────────────────────────────────────────────

func TestParseRecord(t *testing.T) {
	cases := []struct {
		in       string
		src      string
		dest     string
		ok       bool
		isDelete bool
	}{
		{in: "a=>b", src: "a", dest: "b", ok: true},
		{in: " delete => /tmp/x ", src: "delete", dest: "/tmp/x", ok: true, isDelete: true},
		{in: " /live/settings.json => /root/backup.bak ", src: "/live/settings.json", dest: "/root/backup.bak", ok: true},
		{in: "nope", ok: false},
		{in: "=>b", ok: false},
		{in: "a=>", ok: false},
		{in: "", ok: false},
	}
	for _, tc := range cases {
		rec, ok := Parse(tc.in)
		if ok != tc.ok {
			t.Fatalf("Parse(%q) ok = %v, want %v", tc.in, ok, tc.ok)
		}
		if !tc.ok {
			continue
		}
		if rec.Src != tc.src || rec.Dest != tc.dest {
			t.Errorf("Parse(%q) = {%q %q}, want {%q %q}", tc.in, rec.Src, rec.Dest, tc.src, tc.dest)
		}
		if rec.IsDelete() != tc.isDelete {
			t.Errorf("Parse(%q).IsDelete() = %v, want %v", tc.in, rec.IsDelete(), tc.isDelete)
		}
	}
}

// TestApplyReadsBackupWritesLive pins the direction, which is the whole reason
// this package exists. Read it together with TestNoSecondRecordParser below.
func TestApplyReadsBackupWritesLive(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "nested", "settings.json")
	backup := filepath.Join(dir, "backup.bak")

	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Activation got as far as rewriting the live file, then died.
	if err := os.WriteFile(live, []byte("post-activation"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec, ok := Parse(live + " => " + backup)
	if !ok {
		t.Fatal("Parse failed")
	}
	if err := rec.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if data, err := os.ReadFile(live); err != nil || string(data) != "original" {
		t.Fatalf("live file = %q err=%v, want the backup's content", data, err)
	}
	// The backup is the only copy; replaying must never consume it.
	if data, err := os.ReadFile(backup); err != nil || string(data) != "original" {
		t.Fatalf("backup = %q err=%v, want untouched", data, err)
	}
}

func TestApplyDeleteMarker(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(created, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, ok := Parse("delete => " + created)
	if !ok || !rec.IsDelete() {
		t.Fatalf("Parse = %+v ok=%v, want a delete record", rec, ok)
	}
	if err := rec.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(created); err == nil {
		t.Fatal("delete record did not remove the file activation created")
	}
}

// A record whose backup is missing must fail loudly rather than silently
// leave the live file pointing at a dead proxy.
func TestApplyFailsWhenBackupMissing(t *testing.T) {
	dir := t.TempDir()
	rec, ok := Parse(filepath.Join(dir, "live") + " => " + filepath.Join(dir, "absent.bak"))
	if !ok {
		t.Fatal("Parse failed")
	}
	if err := rec.Apply(); err == nil {
		t.Fatal("Apply succeeded with no backup on disk")
	}
}

// ── the gate ──────────────────────────────────────────────────────────

// recordSplitters are the string operations that would indicate a second,
// independent reader of the wire format.
//
// Note what is deliberately absent: the producer builds records with plain
// concatenation (`src+"=>"+dest`), which is not a parse and is not flagged.
// Only splitting on the separator is.
var recordSplitters = map[string]bool{
	"SplitN": true, "Split": true, "Index": true,
	"Contains": true, "HasPrefix": true, "LastIndex": true,
}

// TestNoSecondRecordParser is the reason this package exists.
//
// There are two consumers of the "src=>dest" format: the in-process undo and
// the crash-recovery pass. They used to parse it independently and disagreed
// about which field was the backup, so recovery copied the live file over its
// only backup and destroyed it (ADR 0004). One parser is the only shape where
// that cannot recur, so this fails the build if another one appears anywhere in
// the module — including in a provider ported next quarter.
//
// CODE_REVIEW.md §5: a rule that is mechanical, structural and silently
// violable belongs at rung 3.
func TestNoSecondRecordParser(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	fset := token.NewFileSet()
	var violations []string

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "bin" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			// Test fixtures legitimately spell the format to build records and
			// to assert on messages; only production code can diverge.
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		// This package owns the format.
		if filepath.Dir(rel) == "internal/compensation" {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			if !recordSplitters[sel.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, unqErr := strconv.Unquote(lit.Value)
				if unqErr != nil || !strings.Contains(value, "=>") {
					continue
				}
				pos := fset.Position(lit.Pos())
				violations = append(violations,
					pos.Filename+":"+strconv.Itoa(pos.Line)+
						": strings."+sel.Sel.Name+" on "+lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("the compensation record format is parsed in more than one place; "+
			"use compensation.Parse instead:\n  %s", strings.Join(violations, "\n  "))
	}
}

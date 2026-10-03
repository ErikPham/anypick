package testgate

import (
	"os"
	"path/filepath"
	"testing"
)

// This file is what makes the shared gate falsifiable. Six packages call
// CtxlessMutations, so a defect in it makes six gates go quiet at once -- and
// until now nothing had ever run it against a known-bad input.
//
// It also pins a real defect. The check used to parse entry.Name(), the bare
// file name, against the process working directory instead of against dir.
// Every caller passes ".", where the two coincide, so it was right in this
// repository and wrong for any other caller -- and no test could have said so
// while detection was welded to *testing.T.

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const receiver = "package store\n\ntype S struct{}\n"

func TestCtxlessMutations(t *testing.T) {
	cases := []struct {
		name   string
		source string
		allow  map[string]string
		want   []string
	}{
		{
			name:   "a ctx-less mutator is reported",
			source: receiver + "\nfunc (s *S) WriteThing(name string) error { return nil }\n",
			want:   []string{"S.WriteThing"},
		},
		{
			name:   "a ctx-first mutator is clean",
			source: receiver + "\nfunc (s *S) WriteThing(ctx context.Context, name string) error { return nil }\n",
		},
		{
			name:   "a read keeps its ctx-less form",
			source: receiver + "\nfunc (s *S) GetThing(name string) (string, error) { return name, nil }\n",
		},
		{
			name:   "an unexported mutator is out of scope",
			source: receiver + "\nfunc (s *S) writeThing(name string) error { return nil }\n",
		},
		{
			name:   "a free function is not a method",
			source: receiver + "\nfunc WriteThing(name string) error { return nil }\n",
		},
		{
			name:   "ctx in a later position does not satisfy the rule",
			source: receiver + "\nfunc (s *S) WriteThing(name string, ctx context.Context) error { return nil }\n",
			want:   []string{"S.WriteThing"},
		},
		{
			name:   "an allowlisted mutator with a reason is clean",
			source: receiver + "\nfunc (s *S) PrepareThing() error { return nil }\n",
			allow:  map[string]string{"S.PrepareThing": "creates a fresh staging dir; takes no lock"},
		},
		{
			name:   "an allowlisted mutator with a blank reason is reported",
			source: receiver + "\nfunc (s *S) PrepareThing() error { return nil }\n",
			allow:  map[string]string{"S.PrepareThing": "   "},
			want:   []string{"S.PrepareThing"},
		},
		{
			// The regression pin for the Type.Method key. Keyed on the method
			// name alone, an http.ResponseWriter exemption extended to every
			// other type in the package, so a persisted mutator on a second type
			// passed a gate that looked like it was covering it.
			name:   "an exemption for one type does not cover a second type",
			source: receiver + "\ntype T struct{}\nfunc (t *T) WriteThing(name string) error { return nil }\n",
			allow:  map[string]string{"S.WriteThing": "http.ResponseWriter implementation"},
			want:   []string{"T.WriteThing"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePackage(t, map[string]string{"store.go": tc.source})
			findings, err := CtxlessMutations(dir, tc.allow)
			if err != nil {
				t.Fatalf("CtxlessMutations: %v", err)
			}
			var got []string
			for _, finding := range findings {
				got = append(got, finding.Method)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("findings = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestDirIsHonoured is the regression pin for the path defect. The file lives
// in a temp directory; the process working directory is this package's, where
// no store.go exists. Resolve the name against the working directory again and
// this fails with a parse error instead of reporting the mutator.
func TestDirIsHonoured(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"store.go": receiver + "\nfunc (s *S) DeleteThing(name string) error { return nil }\n",
	})
	findings, err := CtxlessMutations(dir, nil)
	if err != nil {
		t.Fatalf("the gate did not read the directory it was given: %v", err)
	}
	if len(findings) != 1 || findings[0].Method != "S.DeleteThing" {
		t.Fatalf("findings = %v, want the mutator from %s", findings, dir)
	}
}

// TestTestFilesAreSkipped keeps the gate off test helpers, which are allowed to
// be ctx-less.
func TestTestFilesAreSkipped(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"store.go":       receiver,
		"helper_test.go": "package store\n\ntype H struct{}\n\nfunc (h *H) WriteHelper() error { return nil }\n",
	})
	findings, err := CtxlessMutations(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none: _test.go files are out of scope", findings)
	}
}

// TestMissingDirIsAnErrorNotSilence: a gate that could not run must never read
// as a gate that passed.
func TestMissingDirIsAnErrorNotSilence(t *testing.T) {
	if _, err := CtxlessMutations(filepath.Join(t.TempDir(), "absent"), nil); err == nil {
		t.Fatal("a directory that cannot be read must be an error, not an empty pass")
	}
}

// TestMutationVerbsKeepsItsCoverage is a ratchet, not a mirror. The verb list is
// the detection's entire signal, and a silently shortened list makes all six
// dependent gates pass while looking green -- the exact failure this package was
// consolidated to prevent. Adding a verb stays free; removing one now has to be
// deliberate.
func TestMutationVerbsKeepsItsCoverage(t *testing.T) {
	for _, verb := range []string{
		"WriteThing", "SetThing", "ClearThing", "DeleteThing", "PatchThing",
		"UpdateThing", "CreateThing", "RemoveThing", "SaveThing", "UpsertThing",
		"InsertThing", "DiscardThing", "PrepareThing", "IngestThing", "ResetThing",
		"RotateThing", "RevokeThing", "MarkThing", "MoveThing", "ReplaceThing",
		"ApplyThing", "AttachThing", "DetachThing", "MigrateThing", "RestoreThing",
		// ReleaseProxyRuntimeContext and CopySnapshotFiles are persisted
		// mutations; a gate blind to these names cannot see them lose a lock.
		"ReleaseThing", "CopyThing", "HeartbeatThing", "ReapThing",
	} {
		if !MutationVerbs(verb) {
			t.Errorf("mutationVerbs(%q) = false: a verb left the list and six gates went quiet", verb)
		}
	}
	for _, read := range []string{"GetThing", "ListThings", "CountThings", "DescribeThing"} {
		if MutationVerbs(read) {
			t.Errorf("mutationVerbs(%q) = true: reads take no lock and must not be reported", read)
		}
	}
}

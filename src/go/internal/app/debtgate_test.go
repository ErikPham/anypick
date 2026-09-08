package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// longFunctionBudget is how many functions over longFunctionLines the tree is
// allowed to have. A ratchet: it stops the number growing, it does not fix any
// of them. Length matters where it meets churn — the worst offenders here
// (buildSnapshot at 532 lines, handleMutate, PlanActivation) sit in the files
// that change most often, and that combination is where regressions come from.
// Lower the number in the same commit that shortens a function.
const (
	longFunctionBudget = 51
	longFunctionLines  = 80
)

// openDebtBudget is the number of unchecked items allowed under §10 of
// CODE_REVIEW.md. It is a ratchet: adding an open item fails the build,
// closing one does not, and when the count drops the number below must drop
// with it so it cannot creep back up.
//
// It is currently 1 because three of the four items were found, on
// 2026-09-08, to have been finished in code while this list still called them
// open: the store ctx-less reads, the bindings coordinator lock, and
// CODEOWNERS. A debt list nobody re-checks is how the list starts lying.
const openDebtBudget = 1

// Both patterns are assembled from wordTODO so that this file never contains a
// literal marker of its own. A gate that matches on a marker must not be able
// to match itself, and the way to guarantee that is at the source, not with a
// "skip this file" exemption — an exempted gate is a gate that rots.
const wordTODO = "TODO"

// todoPattern matches the only marker shapes we accept. A marker without a
// reference is anonymous debt: nobody can tell whose decision unblocks it, so
// it survives every refactor. Either an ADR number or a gate name is required.
var todoPattern = regexp.MustCompile(wordTODO + `\((adr-[0-9]{4}|gate:[a-z0-9-]+)\)`)

// markerTodo matches a marker in need of a reference, and deliberately not
// prose that merely mentions the word: only the two shapes that open a marker
// count, since the bare word inside a sentence is not debt and flagging it
// would make this gate report on its own commentary.
var markerTodo = regexp.MustCompile(wordTODO + `(?:\(|:\s)`)

func TestEveryTODOInGoCarriesAReference(t *testing.T) {
	goRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	var problems []string
	err = filepath.Walk(goRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(goRoot, path)
		for i, line := range strings.Split(string(body), "\n") {
			if !markerTodo.MatchString(line) {
				continue
			}
			if todoPattern.MatchString(line) {
				continue
			}
			problems = append(problems, rel+":"+itoa(i+1)+" TODO without a reference: "+
				strings.TrimSpace(line)+" — use TODO(adr-NNNN) or TODO(gate:name)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", goRoot, err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestLongFunctionCountDoesNotGrow(t *testing.T) {
	goRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	type longFunc struct {
		where string
		lines int
	}
	var found []longFunc
	fset := token.NewFileSet()

	err = filepath.Walk(goRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			lines := fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
			if lines <= longFunctionLines {
				continue
			}
			rel, _ := filepath.Rel(goRoot, path)
			found = append(found, longFunc{
				where: rel + ":" + itoa(fset.Position(fn.Pos()).Line) + " " + fn.Name.Name,
				lines: lines,
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", goRoot, err)
	}

	if len(found) == 0 {
		t.Fatalf("no function over %d lines found under %s: either the tree changed "+
			"shape or the walk failed, so this check proved nothing", longFunctionLines, goRoot)
	}
	if len(found) > longFunctionBudget {
		sort.Slice(found, func(i, j int) bool { return found[i].lines > found[j].lines })
		t.Errorf("%d functions exceed %d lines, budget is %d — shorten one, or raise the "+
			"budget in the same commit with the reason in the message. Longest:",
			len(found), longFunctionLines, longFunctionBudget)
		for _, f := range found[:min(len(found), 5)] {
			t.Errorf("  %d lines  %s", f.lines, f.where)
		}
	}
	if len(found) < longFunctionBudget {
		t.Logf("down to %d functions over %d lines — lower longFunctionBudget to %d to lock it in",
			len(found), longFunctionLines, len(found))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestOpenDebtListDoesNotGrow(t *testing.T) {
	goRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}
	doc := filepath.Join(goRoot, "..", "..", "CODE_REVIEW.md")
	body, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v — without it this check proves nothing", doc, err)
	}

	var open, done int
	inSection := false
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.HasPrefix(line, "## 10.")
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(line, "- [ ]") {
			open++
		}
		if strings.HasPrefix(line, "- [x]") {
			done++
		}
	}

	if open+done == 0 {
		t.Fatalf("no - [ ] or - [x] item found under §10 of %s: the section moved, "+
			"so this check proved nothing", doc)
	}
	if open > openDebtBudget {
		t.Errorf("§10 has %d open items, budget is %d — close one before adding another, "+
			"or raise the budget in the same commit with the reason in the message",
			open, openDebtBudget)
	}
	if open < openDebtBudget {
		t.Logf("§10 shrank to %d open item(s) — lower openDebtBudget to %d to lock it in",
			open, open)
	}
}

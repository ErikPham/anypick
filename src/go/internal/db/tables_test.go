package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sqlKeywords are identifiers the table scanner would otherwise mistake for
// table names, because SQL puts them in the same position.
var sqlKeywords = map[string]bool{
	"if": true, "set": true, "values": true, "where": true, "select": true,
	"and": true, "or": true, "not": true, "null": true, "exists": true,
	"conflict": true, "excluded": true, "order": true, "by": true, "group": true,
	"limit": true, "offset": true, "as": true, "on": true, "in": true, "is": true,
	"default": true, "primary": true, "key": true, "unique": true, "references": true,
	"constraint": true, "index": true, "each": true, "returning": true, "case": true,
	"when": true, "then": true, "else": true, "end": true, "distinct": true,
}

// tableRef finds table names inside SQL string literals. It is deliberately
// narrow: it only reports identifiers that appear where a table name must
// appear, so a new table still gets caught but prose in a comment does not.
// UPDATE needs its SET: "update check failed" and "Update the plugin" are
// error messages that open with a SQL verb but are not statements.
var tableRef = regexp.MustCompile(`(?i)(?:\bFROM|\bINTO|\bTABLE|\bJOIN)\s+([a-zA-Z_][a-zA-Z0-9_]*)` + `|(?i)\bUPDATE\s+([a-zA-Z_][a-zA-Z0-9_]*)\s+SET`)

// sqlVerb guards the scan. Plenty of ordinary strings contain "from" or "into"
// as English, and some even contain a SQL verb mid-sentence — "Update the
// plugin, or use a AnyPick release that supports API %s." is a user-facing
// message, not a statement. Real SQL begins with its verb, so that is what we
// require.
var sqlVerb = regexp.MustCompile(`(?i)^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|REPLACE|WITH)\b`)

// TestTableOwnershipIsDeclaredAndRespected enforces the persistence half of
// AGENTS.md invariant #5: every table has exactly one owning package.
//
// The check is syntactic because that is where the risk lives. Nothing stops a
// feature from reaching into another package's table — it compiles, it passes
// review, and the two owners then disagree about the row's meaning. This turns
// that into a build failure with the offending file and line.
func TestTableOwnershipIsDeclaredAndRespected(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	fset := token.NewFileSet()
	violations := map[string][]string{}

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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		pkg := packageOf(root, path)
		if pkg == "" {
			return nil
		}
		// internal/db owns the schema and the migrations, so it may name any
		// table. Tests are allowed to set up whatever state they need.
		if pkg == "internal/db" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			raw := strings.Trim(lit.Value, "`\"")
			if !strings.Contains(raw, " ") || !sqlVerb.MatchString(raw) {
				return true // not SQL: paths, ids, and prose all live here too
			}
			for _, m := range tableRef.FindAllStringSubmatch(raw, -1) {
				table := strings.ToLower(m[1])
				if table == "" {
					table = strings.ToLower(m[2])
				}
				if sqlKeywords[table] {
					continue
				}
				owner, declared := TableOwners[table]
				if !declared {
					violations[table] = append(violations[table],
						"undeclared table referenced by "+pkg+" at "+fset.Position(lit.Pos()).String())
					continue
				}
				if owner.External {
					continue
				}
				if pkg == owner.Package {
					continue
				}
				if reason, ok := owner.SharedWith[pkg]; ok && strings.TrimSpace(reason) != "" {
					continue
				}
				violations[table] = append(violations[table],
					"owned by "+owner.Package+" but written by "+pkg+" at "+
						fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}

	if len(violations) > 0 {
		var b strings.Builder
		b.WriteString("table ownership violated (AGENTS.md invariant #5):\n")
		for table, list := range violations {
			for _, v := range list {
				b.WriteString("  " + table + ": " + v + "\n")
			}
		}
		b.WriteString("\nDeclare the table in internal/db/tables.go, or add the package to\n" +
			"SharedWith with a written reason. Two packages writing one table is\n" +
			"how invariants start leaking.")
		t.Error(b.String())
	}
}

// packageOf returns the package import path for a file, relative to the Go
// module root, or "" when the file is outside any package directory.
func packageOf(root, path string) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

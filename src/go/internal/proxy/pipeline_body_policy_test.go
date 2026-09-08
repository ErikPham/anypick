package proxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestPipelineBodyPolicy pins the last G0 acceptance criterion: the four inbound
// doors (single-account Serve, PoolServer.servePoolRequest, the hub door that
// reuses Serve, and the pool's capture leaves) all enforce the SAME request-body
// cap through the one helper, ReadCappedBody.
//
// The regression this guards against is the one the hub path already hit: a raw
// io.ReadAll(r.Body) / io.LimitReader(r.Body, ...) that silently truncates an
// oversized upload instead of rejecting it with 413, then forwards broken JSON
// upstream. There must be exactly one place the cap lives, and no door may read
// the request body outside it.
func TestPipelineBodyPolicy(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	// Doors that must actively enforce the cap by calling ReadCappedBody on the
	// inbound request. ReverseProxy-streaming paths (serveNativeAnthropic, the
	// generic Serve passthrough) legitimately never read the body, so they are
	// intentionally not listed here.
	cappedDoors := map[string]bool{
		"servePoolRequest":            false,
		"geminiWithCapture":           false,
		"translatedWithCapture":       false,
		"serveGemini":                 false,
		"serveTranslatedMessagesWith": false,
		// The reverse door: both the committing single-account paths (Serve /
		// serveNativeAnthropic / serveOpenAIPath) and the pool ring's capture leaf
		// (reverseWithCapture) now funnel through reverseEgress, which is the one
		// place the reverse body-cap lives.
		"reverseEgress": false,
	}
	violations := map[string]string{}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Params == nil {
				continue
			}
			// The helper's own body reads r.Body through io.LimitReader; that is
			// the implementation of the cap, not a bypass. Never flag it.
			if fn.Name.Name == "ReadCappedBody" {
				continue
			}
			if !takesRequest(fn.Type.Params.List) {
				continue
			}
			reqIdent := requestParamName(fn.Type.Params.List)

			var readsBodyRaw, usesCapped bool
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if callee, ok := call.Fun.(*ast.SelectorExpr); ok {
					if callee.Sel.Name == "ReadCappedBody" {
						usesCapped = true
					}
				} else if callee, ok := call.Fun.(*ast.Ident); ok {
					if callee.Name == "ReadCappedBody" {
						usesCapped = true
					}
				}
				if isRawRequestBodyRead(call, reqIdent) {
					readsBodyRaw = true
				}
				return true
			})

			if readsBodyRaw {
				violations[name+":"+fn.Name.Name] =
					"reads the request body outside ReadCappedBody (cap bypass)"
			}
			if _, ok := cappedDoors[fn.Name.Name]; ok {
				if !usesCapped {
					violations[name+":"+fn.Name.Name] =
						"inbound door does not enforce the body cap via ReadCappedBody"
				}
				cappedDoors[fn.Name.Name] = usesCapped
			}
		}
	}

	if len(violations) > 0 {
		msgs := make([]string, 0, len(violations))
		for k, v := range violations {
			msgs = append(msgs, k+": "+v)
		}
		t.Fatalf("body-policy unification broken:\n%s", strings.Join(msgs, "\n"))
	}
	if !cappedDoors["servePoolRequest"] {
		t.Fatal("servePoolRequest no longer enforces the body cap — the pool door regressed")
	}
}

func takesRequest(params []*ast.Field) bool {
	for _, p := range params {
		if isPtrNamed(p.Type, "Request") {
			return true
		}
	}
	return false
}

func isPtrNamed(e ast.Expr, name string) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch t := star.X.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == name
	case *ast.Ident:
		return t.Name == name
	}
	return false
}

func requestParamName(params []*ast.Field) string {
	for _, p := range params {
		if isPtrNamed(p.Type, "Request") {
			if len(p.Names) > 0 {
				return p.Names[0].Name
			}
			return "r"
		}
	}
	return ""
}

// isRawRequestBodyRead detects io.ReadAll(X.Body) / io.LimitReader(X.Body, ...)
// where X is the inbound request parameter. Response-body reads (resp.Body) do
// not match because their base identifier differs from the request parameter.
func isRawRequestBodyRead(call *ast.CallExpr, reqIdent string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "ReadAll" && sel.Sel.Name != "LimitReader" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	bodySel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || bodySel.Sel.Name != "Body" {
		return false
	}
	id, ok := bodySel.X.(*ast.Ident)
	return ok && id.Name == reqIdent
}

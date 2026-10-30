package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONTrustBoundaryOwners(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	for _, tc := range []struct {
		path      string
		selector  string // "pkg.Func" selector call to require, or "" for a bare identifier call
		identCall string
	}{
		{path: "internal/stdhttp/admin/keepwarm/handler.go", selector: "jsonbody.Decode"},
		{path: "internal/stdhttp/admin/tokenaccounting/handler.go", selector: "jsonbody.Decode"},
		{path: "connectors/opencode/internal/upstream/anthropic.go", identCall: "readNonStreamResponse"},
		{path: "connectors/opencode/internal/upstream/gemini.go", identCall: "readNonStreamResponse"},
	} {
		file := parseArchFile(t, filepath.Join(root, filepath.FromSlash(tc.path)))
		if tc.selector != "" {
			receiver, name, ok := strings.Cut(tc.selector, ".")
			if !ok || !hasSelectorCall(file, receiver, name) {
				t.Fatalf("%s must call %s", tc.path, tc.selector)
			}
			continue
		}
		if !hasIdentCall(file, tc.identCall) {
			t.Fatalf("%s must call %s", tc.path, tc.identCall)
		}
	}

	openResponses := parseArchFile(t, filepath.Join(root, "internal", "plugins", "protocols", "openresponses", "strict_json.go"))
	if hasPkgCall(openResponses, "json", "NewDecoder") || hasSelectorCall(openResponses, "dec", "Token") {
		t.Fatal("OpenResponses generic strict validation must remain delegated to jsonshape")
	}
	if !hasSelectorCall(openResponses, "jsonshape", "Preflight") {
		t.Fatal("OpenResponses strict validation must call jsonshape.Preflight")
	}
}

func parseArchFile(t *testing.T, path string) *ast.File {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	_, f, err := ParseGoSource(path, src)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

// hasIdentCall reports an unqualified call to name (e.g. readNonStreamResponse(...)).
func hasIdentCall(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// hasPkgCall reports a selector call pkg.Name.
func hasPkgCall(file *ast.File, pkg, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel == nil {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == pkg && selector.Sel.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// hasSelectorCall reports a selector call ending in name regardless of receiver.
func hasSelectorCall(file *ast.File, receiver, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel == nil || selector.Sel.Name != name {
			return true
		}
		if strings.TrimSpace(exprText(selector.X)) == receiver {
			found = true
			return false
		}
		return true
	})
	return found
}

func exprText(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}

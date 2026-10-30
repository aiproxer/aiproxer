package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const refclientOpenResponsesTree = "internal/refclient/openresponses"

// refclientOpenResponsesForbiddenTargets are production OpenResponses codec,
// adapter, emulator, and matrix packages the independent reference client must
// never import (design §Independent Protocol Emulators).
var refclientOpenResponsesForbiddenTargets = []string{
	"/internal/plugins/protocols/openresponses",
	"/internal/plugins/frontends/openresponses",
	"/internal/plugins/backends/openresponsescompat",
	"/internal/refbackend",
	"/internal/testkit/conformance",
	"/internal/testkit/openresponses",
}

// TestOpenResponsesRefClientBoundary is the Task 8.1 architecture gate. It proves
// the independent reference client emulator exists, depends only on stdlib plus
// gorilla/websocket, never imports production OpenResponses codec/adapters or other
// emulator/matrix packages, and never appears in production dependency graphs.
func TestOpenResponsesRefClientBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(refclientOpenResponsesTree))

	// 1. The package must exist with non-test Go source (never empty).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("refclient/openresponses package missing: %v", err)
	}
	nonTestCount := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			nonTestCount++
		}
	}
	if nonTestCount == 0 {
		t.Fatal("refclient/openresponses has no non-test Go files")
	}

	// 2. Every Go file (including tests) under the tree must not import forbidden
	// targets and must use only stdlib plus the approved gorilla/websocket dependency.
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Errorf("walk %s: %v", path, err)
			return nil
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			return nil
		}
		_, f, err := ParseGoSource(path, src)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, imp := range importsOf(f) {
			for _, forb := range refclientOpenResponsesForbiddenTargets {
				if matchImportTarget(imp, forb) {
					t.Errorf("%s imports forbidden target %s (%s)", SlashPath(relOf(root, path)), imp, forb)
				}
			}
			if !refclientAllowedImport(imp) {
				t.Errorf("%s imports disallowed dependency %q (stdlib + gorilla/websocket only)", SlashPath(relOf(root, path)), imp)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}

	// 3. Production must never import the reference client emulator.
	findings, err := ScanForbiddenImports(root)
	if err != nil {
		t.Fatalf("ScanForbiddenImports: %v", err)
	}
	for _, f := range findings {
		if matchImportTarget(f.Detail, "/internal/refclient/openresponses") {
			t.Errorf("production imports reference client emulator: %s", f.String())
		}
	}

	// 4. The permanent deny-list must register the refclient independence edges.
	requiredRules := []struct{ source, target string }{
		{refclientOpenResponsesTree, "/internal/plugins/protocols/openresponses"},
		{refclientOpenResponsesTree, "/internal/plugins/frontends/openresponses"},
		{refclientOpenResponsesTree, "/internal/plugins/backends/openresponsescompat"},
		{refclientOpenResponsesTree, "/internal/refbackend"},
		{refclientOpenResponsesTree, "/internal/testkit/conformance"},
	}
	for _, req := range requiredRules {
		found := false
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == req.source && rule.TargetPattern == req.target {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ForbiddenImports missing rule source %q target %q", req.source, req.target)
		}
	}
}

// TestOpenResponsesRefClientBoundary_Matcher flags representative illegal imports.
func TestOpenResponsesRefClientBoundary_Matcher(t *testing.T) {
	t.Parallel()
	representative := []struct {
		pkg    string
		imp    string
		target string
	}{
		{refclientOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses", "/internal/plugins/protocols/openresponses"},
		{refclientOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses/codec", "/internal/plugins/frontends/openresponses"},
		{refclientOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat/client", "/internal/plugins/backends/openresponsescompat"},
		{refclientOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/refbackend/openresponses", "/internal/refbackend"},
		{refclientOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/testkit/conformance/matrix", "/internal/testkit/conformance"},
	}
	for _, tc := range representative {
		matched := false
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == tc.pkg && matchImportTarget(tc.imp, rule.TargetPattern) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("matcher failed to flag illegal import %q for %s", tc.imp, tc.pkg)
		}
	}
}

func importsOf(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

func relOf(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// refclientAllowedImport reports whether an import is stdlib or gorilla/websocket.
func refclientAllowedImport(imp string) bool {
	if isStdlibImport(imp) {
		return true
	}
	return imp == "github.com/gorilla/websocket"
}

func isStdlibImport(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const refbackendOpenResponsesTree = "internal/refbackend/openresponses"

// refbackendOpenResponsesForbiddenTargets are production OpenResponses codec,
// adapter, reference-client, and matrix packages the independent reference
// backend emulator must never import in non-test code (design §Independent
// Protocol Emulators).
var refbackendOpenResponsesForbiddenTargets = []string{
	"/internal/plugins/protocols/openresponses",
	"/internal/plugins/frontends/openresponses",
	"/internal/plugins/backends/openresponsescompat",
	"/internal/refclient",
	"/internal/testkit/conformance",
	"/internal/testkit/openresponses",
}

// refbackendAllowedTestImports are the extra dependencies permitted only in
// *_test.go files (the direct wire suite and leak verification).
func refbackendAllowedTestImport(imp string) bool {
	return imp == "github.com/aiproxer/aiproxer/internal/refclient/openresponses" ||
		imp == "go.uber.org/goleak"
}

// TestOpenResponsesRefBackendBoundary is the Task 8.2 architecture gate. It proves
// the independent reference backend emulator exists with non-test source, depends
// only on stdlib plus gorilla/websocket in non-test code, never imports production
// OpenResponses codec/adapters or reference-client/testkit wire types, never
// appears in production dependency graphs, and the permanent deny-list registers
// the independence edges.
func TestOpenResponsesRefBackendBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(refbackendOpenResponsesTree))

	// 1. The package must exist with non-test Go source (never empty).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("refbackend/openresponses package missing: %v", err)
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
		t.Fatal("refbackend/openresponses has no non-test Go files")
	}

	// 2. Non-test files: only stdlib + gorilla/websocket; no forbidden targets.
	//    Test files: additionally the refclient (direct wire) and goleak, but
	//    still never production codec/adapters or matrix wire packages.
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
		isTest := strings.HasSuffix(path, "_test.go")
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
			for _, forb := range refbackendOpenResponsesForbiddenTargets {
				// The direct wire suite legitimately imports the reference client
				// from *_test.go; non-test wire code must never share client types.
				if isTest && forb == "/internal/refclient" {
					continue
				}
				if matchImportTarget(imp, forb) {
					t.Errorf("%s imports forbidden target %s (%s)", SlashPath(relOf(root, path)), imp, forb)
				}
			}
			allowed := isStdlibImport(imp) || imp == "github.com/gorilla/websocket"
			if isTest {
				allowed = allowed || refbackendAllowedTestImport(imp)
			}
			if !allowed {
				t.Errorf("%s imports disallowed dependency %q", SlashPath(relOf(root, path)), imp)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}

	// 3. Production must never import the reference backend emulator.
	findings, err := ScanForbiddenImports(root)
	if err != nil {
		t.Fatalf("ScanForbiddenImports: %v", err)
	}
	for _, f := range findings {
		if matchImportTarget(f.Detail, "/internal/refbackend/openresponses") {
			t.Errorf("production imports reference backend emulator: %s", f.String())
		}
	}

	// 4. The permanent deny-list must register the refbackend independence edges.
	requiredRules := []struct{ source, target string }{
		{refbackendOpenResponsesTree, "/internal/plugins/protocols/openresponses"},
		{refbackendOpenResponsesTree, "/internal/plugins/frontends/openresponses"},
		{refbackendOpenResponsesTree, "/internal/plugins/backends/openresponsescompat"},
		{refbackendOpenResponsesTree, "/internal/refclient"},
		{refbackendOpenResponsesTree, "/internal/testkit/conformance"},
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

// TestOpenResponsesRefBackendBoundary_Matcher flags representative illegal imports.
func TestOpenResponsesRefBackendBoundary_Matcher(t *testing.T) {
	t.Parallel()
	representative := []struct {
		pkg    string
		imp    string
		target string
	}{
		{refbackendOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses", "/internal/plugins/protocols/openresponses"},
		{refbackendOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses/codec", "/internal/plugins/frontends/openresponses"},
		{refbackendOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat/client", "/internal/plugins/backends/openresponsescompat"},
		{refbackendOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/refclient/openresponses", "/internal/refclient"},
		{refbackendOpenResponsesTree, "github.com/aiproxer/aiproxer/internal/testkit/conformance/matrix", "/internal/testkit/conformance"},
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

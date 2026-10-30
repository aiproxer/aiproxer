package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestExecutionCompositionBoundaries verifies that core routing and runtime
// do not import concrete plugins or connector packages.
func TestExecutionCompositionBoundaries(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/routing", "./internal/core/runtime"}, []forbiddenDep{
		{Substr: "/internal/plugins/backends", ErrMsg: "core routing/runtime must not depend on concrete backend plugins"},
		{Substr: "/connectors", ErrMsg: "core routing/runtime must not depend on connectors"},
		{Substr: "/internal/pluginreg", ErrMsg: "core routing/runtime must not depend on pluginreg directly"},
	})
}

// TestStandardPluginsClassifyAllBuiltins verifies that all standard plugin
// backend registrations include an explicit, non-empty execution class.
func TestStandardPluginsClassifyAllBuiltins(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}
	for _, id := range reg.BackendFactoryIDs() {
		prof, ok := reg.BackendExecutionProfile(id)
		if !ok {
			t.Errorf("backend factory %q missing execution profile in standard bundle", id)
			continue
		}
		if prof.Class != aipsdk.BackendExecutionInference && prof.Class != aipsdk.BackendExecutionAgentRuntime {
			t.Errorf("backend factory %q has invalid/unspecified execution class %q", id, prof.Class)
		}
	}
}

// TestAipAPIHasNoExecutionClass ensures BackendExecutionClass stays in pkg/aipsdk
// and does not leak into canonical pkg/aipapi.
func TestAipAPIHasNoExecutionClass(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgDir := filepath.Join("..", "..", "pkg", "aipapi")
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir(pkg/aipapi): %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(pkgDir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					if strings.Contains(ts.Name.Name, "ExecutionClass") || strings.Contains(ts.Name.Name, "ExecutionProfile") {
						t.Errorf("pkg/aipapi must not declare execution class types: found %s in %s", ts.Name.Name, path)
					}
				}
			}
		}
	}
}

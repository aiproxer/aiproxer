package qa

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	nativePluginReason = "native Go plugin loading is forbidden; optional backends are executable connectors (ADR 0008)"
	diContainerReason  = "dependency-injection containers are forbidden; wire dependencies explicitly in constructors and composition roots"
)

// compositionForbiddenRoots are the DI container module roots this repository
// must never depend on. A root and every package under it are forbidden.
var compositionForbiddenRoots = []string{
	"go.uber.org/fx",
	"go.uber.org/dig",
	"github.com/google/wire",
	"github.com/samber/do",
}

// compositionImportReason reports why an import path is forbidden, or "" when
// it is allowed. The match is the whole import path: a container subpackage
// (go.uber.org/fx/fxevent) is as forbidden as its root, while a package that
// merely contains the word (internal/standardplugins/pluginreg,
// github.com/foo/plugin) is not.
func compositionImportReason(importPath string) string {
	if importPath == "plugin" {
		return nativePluginReason
	}
	for _, root := range compositionForbiddenRoots {
		if importPath == root || strings.HasPrefix(importPath, root+"/") {
			return diContainerReason
		}
	}
	return ""
}

// compositionImportsInSource returns the forbidden import paths declared in
// one Go source. The parse is imports-only: the ban is about what the file
// depends on, not how it uses it.
func compositionImportsInSource(src []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if compositionImportReason(importPath) != "" {
			out = append(out, importPath)
		}
	}
	return out, nil
}

// mayImportCompositionPackage is the cheap pre-filter for the repository
// scan: every forbidden import path contains one of these markers.
func mayImportCompositionPackage(src []byte) bool {
	if bytes.Contains(src, []byte("plugin")) {
		return true
	}
	for _, root := range compositionForbiddenRoots {
		if bytes.Contains(src, []byte(root)) {
			return true
		}
	}
	return false
}

// TestCompositionImports_NoNativePluginOrDIContainer rejects native plugin
// loading and the four DI container roots in every tracked Go file of every
// module, test files included: a test that imports a container still makes it
// a module dependency. The archtest import scan covers only the production
// roots, so this is the repository-wide ban.
func TestCompositionImports_NoNativePluginOrDIContainer(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var findings []string
	for _, rel := range trackedGoFiles(t, root) {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", rel, err)
		}
		if !mayImportCompositionPackage(src) {
			continue
		}
		imports, err := compositionImportsInSource(src)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, importPath := range imports {
			findings = append(findings, rel+" imports "+importPath)
		}
	}
	if len(findings) > 0 {
		t.Fatalf("forbidden native plugin or DI-container imports (%d):\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// TestCompositionImports_MatchesWholeImportPath proves the fixture cases that
// motivated a repository-wide check: the container subpackage that the exact
// archtest rule missed is caught, while packages that merely contain the word
// stay allowed.
func TestCompositionImports_MatchesWholeImportPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		importPath string
		want       bool
	}{
		{name: "native plugin", importPath: "plugin", want: true},
		{name: "fx root", importPath: "go.uber.org/fx", want: true},
		{name: "fx subpackage", importPath: "go.uber.org/fx/fxevent", want: true},
		{name: "dig subpackage", importPath: "go.uber.org/dig/digtest", want: true},
		{name: "wire root", importPath: "github.com/google/wire", want: true},
		{name: "versioned do", importPath: "github.com/samber/do/v2", want: true},
		{name: "internal plugin registry", importPath: "github.com/aiproxer/aiproxer/internal/standardplugins/pluginreg", want: false},
		{name: "third-party plugin package", importPath: "github.com/foo/plugin", want: false},
		{name: "plugin suffix package", importPath: "pluginreg", want: false},
		{name: "container name as a prefix of another module", importPath: "github.com/samber/doctrine", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := compositionImportReason(tc.importPath) != ""; got != tc.want {
				t.Fatalf("compositionImportReason(%q) forbidden = %v, want %v", tc.importPath, got, tc.want)
			}
		})
	}

	src := []byte("package fixture\n\nimport (\n\t_ \"plugin\"\n\t_ \"go.uber.org/fx/fxevent\"\n\t_ \"github.com/aiproxer/aiproxer/internal/standardplugins/pluginreg\"\n\t_ \"github.com/foo/plugin\"\n)\n")
	got, err := compositionImportsInSource(src)
	if err != nil {
		t.Fatalf("scan fixture source: %v", err)
	}
	want := []string{"plugin", "go.uber.org/fx/fxevent"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fixture imports = %v, want %v", got, want)
	}
}

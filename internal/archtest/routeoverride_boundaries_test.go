package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/continuity"
)

func TestPkgAipapiCallHasNoRouteOverrideFields(t *testing.T) {
	t.Parallel()
	assertNoOverrideField(t, reflect.TypeFor[aipapi.Call]())
	assertNoOverrideField(t, reflect.TypeFor[aipapi.RouteIntent]())
	assertNoOverrideField(t, reflect.TypeFor[aipapi.SessionRef]())
}

func assertNoOverrideField(t *testing.T, typ reflect.Type) {
	t.Helper()
	for field := range typ.Fields() {
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "override") || strings.Contains(name, "adminrevision") {
			t.Fatalf("%s must not gain route-override field %s", typ.String(), field.Name)
		}
	}
}

func TestPublicContinuityStoreHasNoRouteOverrideMethods(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[continuity.Store]()
	for method := range typ.Methods() {
		switch method.Name {
		case "Snapshot", "Replace", "Clear":
			t.Fatalf("pkg/aipsdk/continuity.Store must not gain route-override method %s", method.Name)
		}
	}
}

func TestFrontendsBackendsConnectorsDoNotImportRouteOverride(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	forbidden := "internal/core/routeoverride"
	roots := []string{
		filepath.Join(root, "internal", "plugins", "frontends"),
		filepath.Join(root, "internal", "plugins", "backends"),
		filepath.Join(root, "connectors"),
	}
	fset := token.NewFileSet()
	for _, dir := range roots {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "testdata" || info.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				if strings.Contains(strings.Trim(imp.Path.Value, `"`), forbidden) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s imports routeoverride", filepath.ToSlash(rel))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminRouteOverrideDoesNotImportFrontendsOrBackends(t *testing.T) {
	t.Parallel()
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/routeoverride", "/internal/plugins/frontends",
		"route-override admin adapter must not import frontend plugins")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/routeoverride", "/internal/plugins/backends",
		"route-override admin adapter must not import backend plugins")
	assertDirectImportsExclude(t, "./internal/stdhttp/admin/routeoverride", "/connectors/",
		"route-override admin adapter must not import connectors")
	assertDirectImportsExclude(t, "./internal/core/routeoverride", "/internal/stdhttp",
		"routeoverride core must not import stdhttp")
	assertDirectImportsExclude(t, "./internal/core/routeoverride", "/internal/plugins/",
		"routeoverride core must not import plugins")
}

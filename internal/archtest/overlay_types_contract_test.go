package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestNativeArchBuildContextUsesBinaryCGOSetting(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("test binary has no build information")
	}
	wantCGO := ""
	wantTrimpath := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "CGO_ENABLED":
			wantCGO = setting.Value
		case "-trimpath":
			wantTrimpath = setting.Value == "true"
		}
	}
	if wantCGO != "0" && wantCGO != "1" {
		t.Fatalf("test binary CGO_ENABLED setting=%q, want 0 or 1", wantCGO)
	}

	for _, runtimeCGO := range []string{"0", "1"} {
		t.Run("runtime CGO_ENABLED="+runtimeCGO, func(t *testing.T) {
			t.Setenv("CGO_ENABLED", runtimeCGO)
			ambientTrimpath := "-trimpath=true"
			wantFlag := "-trimpath=false"
			if wantTrimpath {
				ambientTrimpath, wantFlag = "-trimpath=false", "-trimpath=true"
			}
			t.Setenv("GOFLAGS", ambientTrimpath)
			contexts := nativeArchBuildContexts()
			if len(contexts) != 1 {
				t.Fatalf("native contexts=%d, want 1", len(contexts))
			}
			context := contexts[0]
			if context.Trimpath != wantTrimpath || context.trimpathFlag() != wantFlag {
				t.Fatalf("native trimpath=%t flag=%s; want binary setting %t and flag %s", context.Trimpath, context.trimpathFlag(), wantTrimpath, wantFlag)
			}
			for _, canonical := range archSupportedBuildContexts {
				if canonical.Trimpath || canonical.trimpathFlag() != "-trimpath=false" {
					t.Fatalf("canonical context should retain untrimmed paths: %+v", canonical)
				}
			}
			if context.CGOEnabled != (wantCGO == "1") {
				t.Fatalf("native CGOEnabled=%t with runtime CGO_ENABLED=%s; binary setting is %s", context.CGOEnabled, runtimeCGO, wantCGO)
			}
			wantEnv := "CGO_ENABLED=" + wantCGO
			for _, entry := range context.env() {
				if strings.HasPrefix(entry, "CGO_ENABLED=") {
					if entry != wantEnv {
						t.Fatalf("native context environment has %q, want %q", entry, wantEnv)
					}
					return
				}
			}
			t.Fatalf("native context environment is missing %q", wantEnv)
		})
	}
}

func TestArchOverlayTypes(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.test/overlay\n\ngo 1.26\n")
	dep := write("dep/dep.go", "package dep\nfunc Value() int { return 1 }\n")
	target := write("target/target.go", "package target\nimport \"example.test/overlay/dep\"\nfunc Identity() int { return 1 }\nfunc bindHost() int { return dep.Value() }\n")
	phantom := filepath.Join(root, "target", "phantom.go")

	baseConfig := &packages.Config{
		Dir: root, Env: append(packagesLoadEnv("linux", "amd64"), "GOWORK=off"),
	}
	basePackages, err := loadArchOverlay(baseConfig, "./target")
	if err != nil {
		t.Fatalf("load unmodified package before overlay cases: %v", err)
	}
	depMetadata := basePackages[0].Imports["example.test/overlay/dep"]
	if depMetadata == nil || depMetadata.ExportFile == "" {
		t.Fatal("base load did not provide dependency export metadata")
	}

	// The import map key is the source alias, while PkgPath is the resolved
	// package identity used by the export archive and go/types.
	const importAlias = "example.test/overlay/vendor/shortdep"
	importer := &archExportImporter{
		fset: token.NewFileSet(),
		packages: map[string]*packages.Package{
			importAlias: depMetadata,
		},
		types: make(map[string]*types.Package),
	}
	aliasedPackage, err := importer.Import(importAlias)
	if err != nil {
		t.Fatalf("import aliased dependency export: %v", err)
	}
	if aliasedPackage.Path() != depMetadata.PkgPath {
		t.Fatalf("aliased import path=%q, want resolved package path %q", aliasedPackage.Path(), depMetadata.PkgPath)
	}
	if again, err := importer.Import(importAlias); err != nil || again != aliasedPackage {
		t.Fatalf("repeat aliased import = %p, %v; want cached package %p", again, err, aliasedPackage)
	}

	cases := []struct {
		name    string
		overlay map[string][]byte
		wantErr bool
	}{
		{
			name: "new file and shadowed caller",
			overlay: map[string][]byte{phantom: []byte(`package target
func rogue() int { return bindHost() }
func shadow() int { bindHost := func() int { return 2 }; return bindHost() }
`)},
		},
		{
			name: "dependency overlay exports",
			overlay: map[string][]byte{
				dep:    []byte("package dep\nfunc Value() string { return \"updated\" }\n"),
				target: []byte("package target\nimport \"example.test/overlay/dep\"\nfunc bindHost() string { return dep.Value() }\n"),
			},
		},
		{
			name:    "dependency change invalidates root",
			overlay: map[string][]byte{dep: []byte("package dep\nfunc Value() string { return \"updated\" }\n")},
			wantErr: true,
		},
		{
			name:    "dependency fails type checking",
			overlay: map[string][]byte{dep: []byte("package dep\nfunc Value() int { return missing }\n")},
			wantErr: true,
		},
		{
			name: "dependency creates import cycle",
			overlay: map[string][]byte{dep: []byte(`package dep
import "example.test/overlay/target"
func Value() int { return target.Identity() }
`)},
			wantErr: true,
		},
		{
			name:    "invalid new source",
			overlay: map[string][]byte{phantom: []byte("package target\nfunc rogue() { missing() }\n")},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &packages.Config{
				Dir: root, Env: append(packagesLoadEnv("linux", "amd64"), "GOWORK=off"),
				Overlay: tc.overlay,
			}
			pkgs, err := loadArchOverlay(cfg, "./target")
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadArchOverlay error=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			pkg := pkgs[0]
			object := pkg.Types.Scope().Lookup("bindHost")
			fn, ok := object.(*types.Func)
			if !ok {
				t.Fatalf("bindHost object=%T, want *types.Func", object)
			}
			signature, ok := fn.Type().(*types.Signature)
			if !ok {
				t.Fatalf("bindHost type=%T, want *types.Signature", fn.Type())
			}
			wantResult := "int"
			if tc.name == "dependency overlay exports" {
				wantResult = "string"
			}
			if got := signature.Results().At(0).Type().String(); got != wantResult {
				t.Fatalf("bindHost result=%s, want %s", got, wantResult)
			}
			if tc.name == "new file and shadowed caller" {
				var actualCalls int
				for _, file := range pkg.Syntax {
					ast.Inspect(file, func(node ast.Node) bool {
						if call, ok := node.(*ast.CallExpr); ok && calledObject(pkg.TypesInfo, call.Fun) == fn {
							actualCalls++
						}
						return true
					})
				}
				if actualCalls != 1 {
					t.Fatalf("typed bindHost calls=%d, want 1 (shadowed call excluded)", actualCalls)
				}
			}
		})
	}
}

func TestArchExportImporterFailsClosedOnInvalidExports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	corrupt := filepath.Join(root, "corrupt.a")
	if err := os.WriteFile(corrupt, []byte("not a Go export archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}

	const importPath = "example.test/dep"
	for _, tc := range []struct {
		name       string
		metadata   bool
		exportFile string
	}{
		{name: "missing metadata"},
		{name: "missing export metadata", metadata: true},
		{name: "missing export file", metadata: true, exportFile: filepath.Join(root, "missing.a")},
		{name: "unreadable export", metadata: true, exportFile: unreadable},
		{name: "corrupt export", metadata: true, exportFile: corrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packagesByImport := map[string]*packages.Package{}
			if tc.metadata {
				packagesByImport[importPath] = &packages.Package{PkgPath: importPath, ExportFile: tc.exportFile}
			}
			imp := &archExportImporter{
				fset:     token.NewFileSet(),
				packages: packagesByImport,
				types:    make(map[string]*types.Package),
			}
			pkg, err := imp.Import(importPath)
			if err == nil || pkg != nil {
				t.Fatalf("Import() = %v, %v; want fail-closed error", pkg, err)
			}
		})
	}
}

package archtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestCoreExcludesBackendPluginHostAndWire(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{Substr: "/internal/plugins/backends/", ErrMsg: "internal/core must not import concrete backends"},
		{Substr: "/internal/infra/backendplugins", ErrMsg: "internal/core must not import backendplugins host/discovery"},
		{Substr: "/api/backendplugin/", ErrMsg: "internal/core must not import generated backend plugin RPC"},
		{Substr: "google.golang.org/grpc", ErrMsg: "internal/core must not import grpc"},
		{Substr: "google.golang.org/protobuf", ErrMsg: "internal/core must not import protobuf"},
	})
}

func TestPublicBackendPluginABI_NoInternalOrProviderSDKs(t *testing.T) {
	t.Parallel()
	modInternal := "github.com/aiproxer/aiproxer/internal/"
	assertDepsExcludeForbidden(t, []string{
		"./pkg/aipsdk/backendplugin/...",
		"./api/backendplugin/v1",
	}, []forbiddenDep{
		{Substr: modInternal, ErrMsg: "public backendplugin ABI must not import internal"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "public ABI must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "public ABI must not import Anthropic SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "public ABI must not import AWS SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "public ABI must not import Gemini SDK"},
	})
}

func TestRootGoMod_NoConnectorModules(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	modPath := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "connectors/") || strings.Contains(lower, "connector-support/") {
			t.Fatalf("root go.mod must not require/replace connector modules: %s", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestEssentialBackendBundle_ExactAllowlist(t *testing.T) {
	t.Parallel()
	b := standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{})
	got := make([]string, 0, len(b.Backends))
	for _, e := range b.Backends {
		got = append(got, e.ID)
		if !standardplugins.IsEssentialBackendKind(e.ID) {
			t.Fatalf("non-essential kind %q in EssentialBackendBundle", e.ID)
		}
	}
	want := append([]string(nil), standardplugins.EssentialBackendKinds()...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("essential kinds=%v want=%v", got, want)
	}
}

func TestEssentialAllowlistFixture_DetectsViolation(t *testing.T) {
	t.Parallel()
	fixture := filepath.Join(repoRoot(t), "internal", "archtest", "testdata", "backend_plugin_arch", "forbidden_essential_kind.txt")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	kind := strings.TrimSpace(string(raw))
	if kind == "" {
		t.Fatal("empty fixture")
	}
	// Detector under test: essential allowlist membership used by EssentialBackendBundle gates.
	if !detectEssentialAllowlistViolation(kind) {
		t.Fatalf("detector failed to reject fixture kind %q", kind)
	}
	for _, id := range standardplugins.EssentialBackendKinds() {
		if detectEssentialAllowlistViolation(id) {
			t.Fatalf("detector falsely rejected essential kind %q", id)
		}
	}
}

func detectEssentialAllowlistViolation(kind string) bool {
	return !standardplugins.IsEssentialBackendKind(kind)
}

func TestGenericBackendFactoryDeps_NoProviderSpecificNames(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "pluginreg", "generic_host_deps.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"Codex", "OpenCode", "ACP", "Cursor", "Ollama", "OpenRouter"}
	var typeNode *ast.TypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "GenericBackendFactoryDeps" {
			return true
		}
		typeNode = ts
		return false
	})
	if typeNode == nil {
		t.Fatal("GenericBackendFactoryDeps not found")
	}
	st, ok := typeNode.Type.(*ast.StructType)
	if !ok {
		t.Fatal("expected struct")
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			for _, bad := range forbidden {
				if strings.Contains(name.Name, bad) {
					t.Fatalf("GenericBackendFactoryDeps field %s names provider-specific collaborator %q", name.Name, bad)
				}
			}
		}
		if id, ok := field.Type.(*ast.Ident); ok {
			for _, bad := range forbidden {
				if strings.Contains(id.Name, bad) {
					t.Fatalf("GenericBackendFactoryDeps type %s is provider-specific", id.Name)
				}
			}
		}
	}
}

func TestPublicABIFixture_DetectsInternalImport(t *testing.T) {
	t.Parallel()
	fixture := filepath.Join(repoRoot(t), "internal", "archtest", "testdata", "backend_plugin_arch", "forbidden_public_import.go.txt")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	imports := parseGoImports(t, string(raw))
	forbidden := "github.com/aiproxer/aiproxer/internal/"
	if !detectForbiddenImportSubstr(imports, forbidden) {
		t.Fatalf("detector must flag fixture imports %v for %q", imports, forbidden)
	}
	// Live packages must still pass the same gate.
	assertDepsExcludeForbidden(t, []string{"./pkg/aipsdk/backendplugin/..."}, []forbiddenDep{
		{Substr: forbidden, ErrMsg: "fixture-backed public ABI gate"},
	})
}

func parseGoImports(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(file.Imports))
	for _, imp := range file.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

func detectForbiddenImportSubstr(imports []string, substr string) bool {
	for _, imp := range imports {
		if strings.Contains(imp, substr) {
			return true
		}
	}
	return false
}

func TestDynamic_BackendFactoryDepsIsGenericAlias(t *testing.T) {
	t.Parallel()
	// Frozen generic host surface: BackendFactoryDeps aliases GenericBackendFactoryDeps.
	// Credentials is the provider-neutral per-candidate numbered-credential
	// snapshot (api-key-backend-credential-contract task 2); it names no
	// connector and carries only ordered slot values keyed by env root.
	allow := map[string]struct{}{
		"Identity":    {},
		"Credentials": {},
	}
	path := filepath.Join(repoRoot(t), "internal", "pluginreg", "generic_host_deps.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "GenericBackendFactoryDeps" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				fields = append(fields, name.Name)
				if _, ok := allow[name.Name]; !ok {
					t.Fatalf("GenericBackendFactoryDeps field %q not on allowlist", name.Name)
				}
				for _, bad := range []string{"OpenCode", "ACP", "Cursor", "Ollama", "OpenRouter", "Codex"} {
					if strings.Contains(name.Name, bad) {
						t.Fatalf("GenericBackendFactoryDeps field %q introduces connector-specific name %q", name.Name, bad)
					}
				}
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("GenericBackendFactoryDeps not found")
	}
	if len(fields) != len(allow) {
		t.Fatalf("GenericBackendFactoryDeps fields=%v allowlist size=%d", fields, len(allow))
	}
	assertBackendFactoryDepsIsGenericAlias(t)
}

func assertBackendFactoryDepsIsGenericAlias(t *testing.T) {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "pluginreg", "reg.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "BackendFactoryDeps" {
			return true
		}
		found = true
		if ts.Assign == token.NoPos {
			t.Fatal("BackendFactoryDeps must be a type alias to GenericBackendFactoryDeps")
		}
		id, ok := ts.Type.(*ast.Ident)
		if !ok || id.Name != "GenericBackendFactoryDeps" {
			t.Fatalf("BackendFactoryDeps alias target=%v want GenericBackendFactoryDeps", ts.Type)
		}
		return false
	})
	if !found {
		t.Fatal("BackendFactoryDeps not found in reg.go")
	}
}

func TestFactoryDeps_ProviderSpecific_DiscoveredRegistrationNoConnectorImports(t *testing.T) {
	t.Parallel()
	// Task 4.2 InstallDiscovered / generic discovered registration must not import connector families.
	// Until that symbol lands, this gate is prepared and no-op-pass; once present it enforces.
	root := repoRoot(t)
	pkgs := packagesDeclaringInstallDiscovered(t, root)
	if len(pkgs) == 0 {
		t.Log("InstallDiscovered not present yet; ProviderSpecific discovered-import gate prepared")
		return
	}
	patterns := append([]string(nil), pkgs...)
	assertDepsExcludeForbidden(t, patterns, []forbiddenDep{
		{Substr: "/internal/plugins/backends/", ErrMsg: "discovered registration must not import concrete backend connectors"},
		{Substr: "/connectors/", ErrMsg: "discovered registration must not import connector modules"},
	})
}

func packagesDeclaringInstallDiscovered(t *testing.T, root string) []string {
	t.Helper()
	candidates := []string{
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "infra", "backendplugins"),
	}
	seen := map[string]struct{}{}
	var out []string
	for _, dir := range candidates {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				base := d.Name()
				if base == "testdata" || strings.HasPrefix(base, ".") {
					if path != dir {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil
			}
			has := false
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					return true
				}
				if fn.Name.Name == "InstallDiscovered" {
					has = true
					return false
				}
				return true
			})
			if !has {
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			pattern := "./" + filepath.ToSlash(rel)
			if _, ok := seen[pattern]; !ok {
				seen[pattern] = struct{}{}
				out = append(out, pattern)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestGOListJSON_CorePackagesDecodable(t *testing.T) {
	t.Parallel()
	out, err := cachedGoList(t, "-test=false", "-json", "./internal/core/...")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	n := 0
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("expected core packages")
	}
}

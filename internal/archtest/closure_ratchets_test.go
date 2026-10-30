package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestClosureForbiddenImports_RulesPresent proves the ownership-closure import
// deny-list covers every Task 11.2 edge with zero whitelist exceptions.
func TestClosureForbiddenImports_RulesPresent(t *testing.T) {
	t.Parallel()

	want := []ForbiddenImportRule{
		{SourcePattern: "pkg/aipruntime", TargetPattern: "/internal/plugins/features/"},
		{SourcePattern: "internal/plugins/features", TargetPattern: "/internal/core"},
		{SourcePattern: "internal/plugins/features", TargetPattern: "/internal/infra/runtimebundle"},
		{SourcePattern: "internal/plugins/features", TargetPattern: "/internal/standardplugins/featurehost"},
	}
	for _, w := range want {
		var matched []ForbiddenImportRule
		for _, rule := range ClosureForbiddenImports {
			if rule.SourcePattern == w.SourcePattern && rule.TargetPattern == w.TargetPattern {
				matched = append(matched, rule)
			}
		}
		if len(matched) != 1 {
			t.Fatalf("ClosureForbiddenImports must contain exactly 1 rule for %q -> %q, got %d",
				w.SourcePattern, w.TargetPattern, len(matched))
		}
		if len(matched[0].ExceptPrefix) != 0 {
			t.Fatalf("closure rule %q -> %q must have zero whitelist exceptions, got %v",
				w.SourcePattern, w.TargetPattern, matched[0].ExceptPrefix)
		}
	}
}

// TestClosureForbiddenImports_NegativeFixtures proves renamed/nested bypasses
// cannot escape the closure deny-list while legitimate composition stays allowed.
func TestClosureForbiddenImports_NegativeFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{name: "aipruntime imports concrete feature", relPath: "pkg/aipruntime/options.go", importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm", wantForbid: true},
		{name: "aipruntime nested imports feature subpackage", relPath: "pkg/aipruntime/nested/deep.go", importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine", wantForbid: true},
		{name: "reasoningpreservation feature imports core", relPath: "internal/plugins/features/reasoningpreservation/renamed.go", importPath: "github.com/aiproxer/aiproxer/internal/core/runtime", wantForbid: true},
		{name: "agentloopguard feature imports runtimebundle", relPath: "internal/plugins/features/agentloopguard/nested/bypass.go", importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle", wantForbid: true},
		{name: "partsnoop feature imports featurehost", relPath: "internal/plugins/features/partsnoop/policy.go", importPath: "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost", wantForbid: true},
		{name: "keepwarm nested imports featurehost child", relPath: "internal/plugins/features/keepwarm/nested/deep.go", importPath: "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy", wantForbid: true},
		{name: "feature imports own subpackage allowed", relPath: "internal/plugins/features/reasoningpreservation/bundle.go", importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation/replay", wantForbid: false},
		{name: "runtimebundle imports featurehost facade allowed", relPath: "internal/infra/runtimebundle/process_builder.go", importPath: "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost", wantForbid: false},
		{name: "featurehost imports concrete feature allowed", relPath: "internal/standardplugins/featurehost/keepwarm.go", importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm", wantForbid: false},
		{name: "aipruntime imports sdk registration allowed", relPath: "pkg/aipruntime/options.go", importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost", wantForbid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package test\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports(%q): %v", tc.relPath, err)
			}
			if got := len(findings) > 0; got != tc.wantForbid {
				t.Fatalf("ScanFileForbiddenImports(%q, %q): got forbidden=%v, want %v (findings: %v)",
					tc.relPath, tc.importPath, got, tc.wantForbid, findings)
			}
		})
	}
}

// TestProductionClosureEdgesHold scans the live production tree for every
// Task 11.2 edge: aipruntime and all feature trees stay free of forbidden
// concrete/kernel imports.
func TestProductionClosureEdgesHold(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		isAipruntime := MatchPathPrefix(pkg, "pkg/aipruntime")
		isFeature := MatchPathPrefix(pkg, "internal/plugins/features")
		if !isAipruntime && !isFeature {
			return nil
		}
		findings, err := ScanFileForbiddenImports(rel, abs, src)
		if err != nil {
			return err
		}
		for _, f := range findings {
			violations = append(violations, f.String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production closure edges violated (%d):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// TestFeatureHost_NoResolverMethodGrowth forbids service-locator shaped
// methods on the process facade beyond the existing startup/generation surface.
func TestFeatureHost_NoResolverMethodGrowth(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "standardplugins", "featurehost")
	forbidden := []string{"Resolver", "Registry", "Binder", "Register", "Bind", "Resolve", "Lookup"}
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name == nil {
				continue
			}
			if !fn.Name.IsExported() {
				continue
			}
			for _, f := range forbidden {
				if strings.EqualFold(fn.Name.Name, f) {
					bad = append(bad, SlashPath(rel)+": featurehost must not expose resolver/registry method "+fn.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("featurehost facade exposes service-locator methods (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// TestFeatureHost_NoBindingMapStorage forbids arbitrary string-keyed binding
// or registration maps in featurehost production code. Typed startup structs
// and per-feature option maps remain allowed.
func TestFeatureHost_NoBindingMapStorage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "standardplugins", "featurehost")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			mt, ok := n.(*ast.MapType)
			if !ok {
				return true
			}
			key, ok := mt.Key.(*ast.Ident)
			if !ok || key.Name != "string" {
				return true
			}
			if mapValueIsBindingLike(mt.Value) {
				rel, _ := filepath.Rel(root, path)
				bad = append(bad, SlashPath(rel)+": string-keyed binding/registration map storage")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("featurehost must not store arbitrary binding maps (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

func mapValueIsBindingLike(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "any" || strings.Contains(v.Name, "Registration") || strings.Contains(v.Name, "Binding")
	case *ast.InterfaceType:
		return true
	case *ast.SelectorExpr:
		return v.Sel != nil && (strings.Contains(v.Sel.Name, "Registration") || strings.Contains(v.Sel.Name, "Binding"))
	case *ast.StarExpr:
		return mapValueIsBindingLike(v.X)
	case *ast.IndexExpr:
		return mapValueIsBindingLike(v.X)
	case *ast.IndexListExpr:
		return mapValueIsBindingLike(v.X)
	default:
		return false
	}
}

// TestFeatureHost_ReflectOnlyTypedNilGuards permits reflection in featurehost
// production code solely for startup typed-nil capability guards. Any broader
// reflective construction or method dispatch fails this ratchet.
func TestFeatureHost_ReflectOnlyTypedNilGuards(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "standardplugins", "featurehost")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		findings, err := scanFeaturehostReflectViolations(rel, src)
		if err != nil {
			return err
		}
		bad = append(bad, findings...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("featurehost reflection must stay inside typed-nil guards (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// TestClosureReflectNegativeFixtures proves the typed-nil-guard ratchet
// cannot be bypassed: aliased reflect imports, package-level reflective
// initializers, and dynamic dispatch hiding inside nil-guard-named functions
// are all violations, while canonical typed-nil guards stay silent.
func TestClosureReflectNegativeFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		src         string
		wantFinding bool
	}{
		{
			name: "aliased import dynamic dispatch",
			src: `package featurehost

import r "reflect"

func ResolveCapability(v any, name string) any {
	return r.ValueOf(v).MethodByName(name).Call(nil)[0].Interface()
}
`,
			wantFinding: true,
		},
		{
			name: "package-level reflective initializer",
			src: `package featurehost

import "reflect"

var cachedType = reflect.TypeOf(0)
`,
			wantFinding: true,
		},
		{
			name: "MethodByName inside isNil-named func",
			src: `package featurehost

import "reflect"

func isNilDynamic(v any, method string) bool {
	rv := reflect.ValueOf(v)
	out := rv.MethodByName(method).Call(nil)
	return len(out) == 0
}
`,
			wantFinding: true,
		},
		{
			name: "canonical typed-nil guard stays silent",
			src: `package featurehost

import "reflect"

func isNilEgressPolicy(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}
`,
			wantFinding: false,
		},
		{
			name: "exported IsNil guard via alias stays silent",
			src: `package featurehost

import r "reflect"

func IsNilCapability(v any) bool {
	if v == nil {
		return true
	}
	rv := r.ValueOf(v)
	switch rv.Kind() {
	case r.Chan, r.Func, r.Interface, r.Map, r.Pointer, r.Slice, r.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}
`,
			wantFinding: false,
		},
		{
			name: "plain code without reflect stays silent",
			src: `package featurehost

func Compose(a, b string) string { return a + b }
`,
			wantFinding: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings, err := scanFeaturehostReflectViolations("internal/standardplugins/featurehost/fixture.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("scanFeaturehostReflectViolations: %v", err)
			}
			if got := len(findings) > 0; got != tc.wantFinding {
				t.Fatalf("scanFeaturehostReflectViolations(%q): got violation=%v, want %v (findings: %v)",
					tc.name, got, tc.wantFinding, findings)
			}
		})
	}
}

// TestClosureReflectCopiedValueAlias proves ValueOf-derived identifiers cannot
// escape the typed-nil-guard ratchet through identifier copies: `copied := rv`,
// multi-assign, and var-spec copies all propagate the reflect.Value taint, so
// dynamic dispatch on the copy is a violation, while guard-only Kind/IsNil use
// of a copy stays silent.
func TestClosureReflectCopiedValueAlias(t *testing.T) {
	t.Parallel()

	const head = "package featurehost\nimport \"reflect\"\n"
	cases := []struct {
		name        string
		src         string
		wantFinding bool
	}{
		{name: "copied value dispatch", src: head + "func isNilDynamic(v any, m string) bool {\nrv := reflect.ValueOf(v)\ncopied := rv\nout := copied.MethodByName(m).Call(nil)\nreturn len(out) == 0\n}\n", wantFinding: true},
		{name: "multi-assign copy dispatch", src: head + "func isNilDynamic(v any, m string, o any) bool {\nrv := reflect.ValueOf(v)\na, b := rv, o\n_ = b\nout := a.MethodByName(m).Call(nil)\nreturn len(out) == 0\n}\n", wantFinding: true},
		{name: "var-spec copy dispatch", src: head + "func isNilDynamic(v any, m string) bool {\nrv := reflect.ValueOf(v)\nvar c = rv\nout := c.MethodByName(m).Call(nil)\nreturn len(out) == 0\n}\n", wantFinding: true},
		{name: "copied value guard-only stays silent", src: head + "func isNilEgressPolicy(v any) bool {\nif v == nil {\nreturn true\n}\nrv := reflect.ValueOf(v)\ncopied := rv\nswitch copied.Kind() {\ncase reflect.Chan, reflect.Pointer:\nreturn copied.IsNil()\ndefault:\nreturn false\n}\n}\n", wantFinding: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings, err := scanFeaturehostReflectViolations("internal/standardplugins/featurehost/fixture.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("scanFeaturehostReflectViolations: %v", err)
			}
			if got := len(findings) > 0; got != tc.wantFinding {
				t.Fatalf("scanFeaturehostReflectViolations(%q): got violation=%v, want %v (findings: %v)",
					tc.name, got, tc.wantFinding, findings)
			}
		})
	}
}

// TestRetiredCorePackageAbsenceCoversAllManifestDirs locks the redundant
// absence test to the full manifest: every internal/core/* manifest entry for
// a retired package must also appear in retired_core_packages_test.go so list
// drift cannot silently narrow the resurrection gate.
func TestRetiredCorePackageAbsenceCoversAllManifestDirs(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "archtest", "retired_core_packages_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	var missing []string
	for _, dir := range RetiredPackageDirs {
		if !strings.HasPrefix(dir, "internal/core/") {
			continue
		}
		if !strings.Contains(text, `"`+dir+`"`) {
			missing = append(missing, dir)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("retired_core_packages_test.go omits retired core dirs (%d):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestCriticalFileBudgetsCoverFeaturehostFacade locks the Task 11.3 facade
// caps: every featurehost composition-surface file must keep an explicit
// critical-file ceiling so feature growth cannot turn the facade into a god
// package (Req 12.5).
func TestCriticalFileBudgetsCoverFeaturehostFacade(t *testing.T) {
	t.Parallel()

	facade := []string{
		"internal/standardplugins/featurehost/runtime.go",
		"internal/standardplugins/featurehost/process.go",
		"internal/standardplugins/featurehost/generation.go",
		"internal/standardplugins/featurehost/inputs.go",
		"internal/standardplugins/featurehost/bindings.go",
	}
	capped := map[string]int{}
	for _, b := range CriticalFileBudgets {
		capped[b.Path] = b.Max
	}
	for _, path := range facade {
		max, ok := capped[path]
		if !ok {
			t.Fatalf("featurehost facade file %q has no critical-file budget entry", path)
		}
		if max <= 0 {
			t.Fatalf("featurehost facade file %q has non-positive budget %d", path, max)
		}
	}
}

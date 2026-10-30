package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestInternalCoreProductionClosureExcludesCompositionHelpers enforces dependency
// direction from introduce-hexagonal-architecture: the policy core must not import
// composition or assembly packages (bounded exceptions flow inward, not outward).
func TestInternalCoreProductionClosureExcludesCompositionHelpers(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/pluginreg",
			ErrMsg: "internal/core must not depend on pluginreg (composition root; invert inward only)",
		},
		{
			Substr: "/internal/infra/runtimebundle",
			ErrMsg: "internal/core must not depend on runtimebundle (assembly outside core)",
		},
	})
}

// TestInternalCoreProductionExcludesInfra enforces that no internal/core/...
// package imports internal/infra/... at the production-code level (test-only
// imports are acceptable). This catches boundary leaks like the former
// core/continuity -> infra/db and core/extensions -> infra/extensiontrace edges.
func TestInternalCoreProductionExcludesInfra(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/infra/",
			ErrMsg: "internal/core must not depend on internal/infra (hex: core imports point inward only)",
		},
	})
}

// TestStdHTTPProductionExcludesConcreteFrontends enforces that
// internal/stdhttp does not directly import any internal/plugins/frontends/...
// package at the production-code level. Concrete plugins are wired through
// pluginreg; test-only imports for alignment checks are acceptable.
// Uses direct imports (not transitive deps) because stdhttp legitimately
// depends on pluginreg which transitively reaches frontend packages.
func TestStdHTTPProductionExcludesConcreteFrontends(t *testing.T) {
	t.Parallel()
	assertDirectImportsExclude(t, "./internal/stdhttp", "/internal/plugins/frontends/",
		"internal/stdhttp must not directly import concrete frontend plugins (use pluginreg or local constants)")
}

// assertDirectImportsExclude checks that the package at pattern has no direct
// production imports (Imports field, not Deps) containing substr.
func assertDirectImportsExclude(t *testing.T, pattern, substr, errMsg string) {
	t.Helper()
	out, err := cachedGoList(t, "-test=false", "-json", pattern)
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, imp := range pkg.Imports {
			if strings.Contains(imp, substr) {
				t.Fatalf("%s: %s directly imports %s", errMsg, pkg.ImportPath, imp)
			}
		}
	}
}

// assertDirectImportsExcludeExcept is like assertDirectImportsExclude but skips
// packages whose import path contains exemptSubstr (legitimate consumers), so a
// recursive pattern can cover a whole tree while exempting the intended consumer.
func assertDirectImportsExcludeExcept(t *testing.T, pattern, substr, exemptSubstr, errMsg string) {
	t.Helper()
	out, err := cachedGoList(t, "-test=false", "-json", pattern)
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.Contains(pkg.ImportPath, exemptSubstr) {
			continue
		}
		for _, imp := range pkg.Imports {
			if strings.Contains(imp, substr) {
				t.Fatalf("%s: %s directly imports %s", errMsg, pkg.ImportPath, imp)
			}
		}
	}
}

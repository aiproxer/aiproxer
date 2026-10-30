package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestPhase6_scopePackageImportsStayMinimalAndSafe proves the public scope contract package does
// not depend on enforcement, billing, rate-limiting, redaction, policy-decision, admin, auth,
// runtime, or provider/backend packages: the feature is attribution-only foundation work
// (requirements 8.1, 8.2, 8.3, 8.4, 7.4). Scope may only depend on stdlib and execview.
func TestPhase6_scopePackageImportsStayMinimalAndSafe(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"/internal/plugins/backends",
		"/internal/plugins/frontends",
		"/internal/plugins/features",
		"/internal/core/securesession",
		"/internal/core/runtime",
		"/internal/core/auth",
		"/internal/core/config",
		"/internal/core/execctx",
		"/pkg/aipsdk/usage",
		"/pkg/aipsdk/traffic",
		"/pkg/aipsdk/auth",
		"/pkg/aipsdk/transport",
		"billing",
		"ratelimit",
		"redact",
		"oauth",
		"saml",
	}
	for _, imp := range listDirectImports(t, "./pkg/aipsdk/scope") {
		low := strings.ToLower(imp)
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Fatalf("pkg/aipsdk/scope imports forbidden dependency %q (attribution-only boundary): %s", bad, imp)
			}
		}
	}
}

// TestPhase6_backendsDoNotDirectlyImportScope proves backend provider adapters do not directly
// import the control-plane scope contract, so attribution is not forwarded to backend providers by
// default (requirement 7.4). Transitively reachable via shared SDK types is acceptable; a direct
// import would indicate a new provider-facing forwarding surface.
func TestPhase6_backendsDoNotDirectlyImportScope(t *testing.T) {
	t.Parallel()
	for _, pkg := range listPackages(t, "./internal/plugins/backends/...") {
		for _, imp := range pkg.Imports {
			if strings.HasSuffix(imp, "/pkg/aipsdk/scope") {
				t.Fatalf("backend adapter %s directly imports %s (no provider scope forwarding)", pkg.ImportPath, imp)
			}
		}
	}
}

// listDirectImports returns the direct (non-transitive) imports of a single package pattern.
func listDirectImports(t *testing.T, pattern string) []string {
	t.Helper()
	pkgs := listPackages(t, pattern)
	if len(pkgs) == 0 {
		t.Fatalf("no packages matched %s", pattern)
	}
	return pkgs[0].Imports
}

func listPackages(t *testing.T, pattern string) []goListPackage {
	t.Helper()
	out, err := cachedGoList(t, "-test=false", "-json", pattern)
	if err != nil {
		t.Fatalf("go list %s: %v", pattern, err)
	}
	var pkgs []goListPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

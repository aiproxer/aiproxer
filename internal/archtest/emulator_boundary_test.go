package archtest

import (
	"testing"
)

// TestEmulatorBoundary verifies that internal/refclient/openresponses and
// internal/refbackend/openresponses are test-only packages that do not import production
// OpenResponses codecs/adapters, and that production packages never import refclient or refbackend.
func TestEmulatorBoundary(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// Scan forbidden imports in production code
	findings, err := ScanForbiddenImports(root)
	if err != nil {
		t.Fatalf("ScanForbiddenImports failed: %v", err)
	}

	for _, f := range findings {
		if (MatchPathPrefix(f.Path, "internal/refclient/openresponses") ||
			MatchPathPrefix(f.Path, "internal/refbackend/openresponses") ||
			MatchPathPrefix(f.Path, "internal/testkit/openresponses")) &&
			(matchImportTarget(f.Detail, "/internal/plugins/protocols/openresponses") ||
				matchImportTarget(f.Detail, "/internal/plugins/frontends/openresponses") ||
				matchImportTarget(f.Detail, "/internal/plugins/backends/openresponsescompat")) {
			t.Errorf("emulator boundary violation: %s", f.String())
		}
	}
}

// TestNoTautology verifies anti-tautology architecture gates:
// 1. Production code never imports reference client/backend emulators.
// 2. Reference emulators never import production wire codecs, parsers, or state machines.
// 3. Testkit contracts never import production OpenResponses codecs.
// 4. Prohibited cross-domain import edges are registered in ForbiddenImports rules.
func TestNoTautology(t *testing.T) {
	t.Parallel()

	requiredRules := []struct {
		source string
		target string
		reason string
	}{
		{
			source: "internal/refclient/openresponses",
			target: "/internal/plugins/protocols/openresponses",
			reason: "reference client emulator must not import production OpenResponses codecs",
		},
		{
			source: "internal/refbackend/openresponses",
			target: "/internal/plugins/protocols/openresponses",
			reason: "reference backend emulator must not import production OpenResponses codecs",
		},
		{
			source: "internal/testkit/openresponses",
			target: "/internal/plugins/protocols/openresponses",
			reason: "testkit contracts must not import production OpenResponses codecs",
		},
		{
			source: "internal/plugins/protocols/openresponses",
			target: "/internal/refclient",
			reason: "production protocol wire code must not import reference clients",
		},
		{
			source: "internal/plugins/protocols/openresponses",
			target: "/internal/refbackend",
			reason: "production protocol wire code must not import reference backends",
		},
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
			t.Errorf("ForbiddenImports missing anti-tautology rule for source %q target %q", req.source, req.target)
		}
	}
}

package archtest

import (
	"testing"
)

// TestProductionStandardWiringDoesNotDependOnReferenceSupport ensures composition roots and
// runtimebundle (standard wiring) never take a non-test dependency on test-only reference packages.
//
// Complements internal/core/runtime/boundaries_test.go (core production code must not import
// internal/plugins); together they keep emulator-only surfaces out of release binaries while still
// allowing *_test.go imports for integration coverage.
func TestProductionStandardWiringDoesNotDependOnReferenceSupport(t *testing.T) {
	t.Parallel()
	const refBackend = "github.com/aiproxer/aiproxer/internal/refbackend"
	const refClient = "github.com/aiproxer/aiproxer/internal/refclient"
	rules := []forbiddenDep{
		{Substr: refBackend, ErrMsg: "production wiring must not depend on internal/refbackend"},
		{Substr: refClient, ErrMsg: "production wiring must not depend on internal/refclient"},
	}
	patterns := []string{
		"./cmd/aipstd",
		"./internal/pluginreg/...",
		"./internal/infra/runtimebundle/...",
	}
	assertDepsExcludeForbidden(t, patterns, rules)
}

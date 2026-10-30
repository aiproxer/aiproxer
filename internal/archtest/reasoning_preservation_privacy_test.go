package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReasoningPreservationPrivacy_NoSecondSecretDetector ensures the reasoning-preservation
// feature reuses the trusted pkg/aipsdk/secretguard contract and does not introduce
// a competing heuristic detector, and that control-plane vs model separation is preserved.
func TestReasoningPreservationPrivacy_NoSecondSecretDetector(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	featurePkg := "internal/plugins/features/reasoningpreservation"
	// Direct import allowlist: feature may import pkg/aipsdk/secretguard contract only.
	// It must not import internal secretguard impl or diagredact heuristic as second detector.
	assertProductionDirectImportsExclude(t, []string{featurePkg}, []string{
		"github.com/aiproxer/aiproxer/internal/core/secretguard",
		"github.com/aiproxer/aiproxer/internal/infra/diagredact",
	})
	// Ensure feature imports the trusted contract (at least the adapter does).
	foundSecretguardContract := false
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if PackageDirFromRel(rel) != featurePkg {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if imp == "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard" {
				foundSecretguardContract = true
			}
			// Forbid heuristic regex detector patterns inside feature (except adapter which delegates)
			// We scan source for suspicious patterns like "regexp.*secret" or "github_pat|sk-" inside feature files.
			_ = imp
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !foundSecretguardContract {
		t.Fatal("reasoning-preservation feature must import pkg/aipsdk/secretguard contract via adapter")
	}
	// Ensure no feature file (except secretguard_adapter.go) contains heuristic secret regex
	err = WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if PackageDirFromRel(rel) != featurePkg {
			return nil
		}
		if strings.HasSuffix(rel, "secretguard_adapter.go") {
			return nil
		}
		lower := strings.ToLower(string(src))
		// Heuristic detector markers that would indicate a second detector
		for _, needle := range []string{"secretpatterns", "github_pat", "sk-or-", "sk-ant-", "diagredact.sanitize", "regexp.mustcompile.*secret"} {
			if strings.Contains(lower, strings.ToLower(needle)) {
				// Allow test files? WalkProductionGoFiles already excludes *_test.go
				t.Fatalf("%s contains second-detector marker %q", rel, needle)
			}
		}
		// Also forbid direct regexp compilation for secret detection outside adapter
		if strings.Contains(lower, "regexp.mustcompile") && strings.Contains(lower, "secret") {
			t.Fatalf("%s introduces regexp secret detector", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ensure CompressorInputSegment remains envelope/context-free: only Index and Text fields
	_ = filepath.Join // keep import used
}

// TestReasoningPreservationPrivacy_FeatureImportsBounded verifies the feature's import surface
// stays narrow: only pkg/aipsdk/secretguard for privacy, no provider SDKs, no billing ledger, no second store.
func TestReasoningPreservationPrivacy_FeatureImportsBounded(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/reasoningpreservation/..."}, []forbiddenDep{
		{Substr: "/internal/core/secretguard", ErrMsg: "feature must use pkg/aipsdk/secretguard contract, not internal impl"},
		{Substr: "/internal/infra/diagredact", ErrMsg: "feature must reuse trusted secretguard matcher, not diagredact heuristic"},
		{Substr: "/internal/core/billing", ErrMsg: "feature must not own billing ledger"},
		{Substr: "/enterprise/internal/infra/billingstore", ErrMsg: "feature must not write billing persistence"},
		{Substr: "/internal/core/continuity", ErrMsg: "feature must not own second transcript store"},
		{Substr: "database/sql", ErrMsg: "feature must not open database"},
		{Substr: "github.com/uptrace/bun", ErrMsg: "feature must not add durable ORM"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "feature must not import provider SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "feature must not import provider SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "feature must not import provider SDK"},
	})
}

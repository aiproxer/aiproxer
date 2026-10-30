package archtest

import (
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// TestCoreHasZeroMemoSteeringPolicy forbids interleaved memo steering policy
// from living in generic core production code (Req 4.2/5.2/13.3). Memo
// rendering, overlay identity, placement/fallback selection, and memo-specific
// filtering/deactivation are feature-owned
// (internal/plugins/features/interleavedthinking) and reach core only through
// the runtime.InterleavedProcessor port adapted by
// internal/standardplugins/featurehost.
func TestCoreHasZeroMemoSteeringPolicy(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	forbiddenLiterals := []string{
		"[Session Steering Guidance]",
		"interleaved-thinking-memo",
		"interleaved_thinking_memo",
	}
	forbiddenIdents := map[string]struct{}{
		"SessionSteeringGuidanceHeader": {},
		"interleavedMemoOverlayID":      {},
		"interleavedMemoSteeringReason": {},
		"memoSteeringPayload":           {},
		"stripMemoSteeringOverlay":      {},
		"StablePrefixFallback":          {},
	}

	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/core") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind.String() != "STRING" {
					return true
				}
				for _, lit := range forbiddenLiterals {
					if strings.Contains(v.Value, lit) {
						violations = append(violations, rel+": memo literal "+lit)
					}
				}
			case *ast.Ident:
				if _, ok := forbiddenIdents[v.Name]; ok {
					violations = append(violations, rel+": memo symbol "+v.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("internal/core production holds memo steering policy (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

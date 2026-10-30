package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPhase5UsageAuthorityProductionIsQuantityOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	roots := []string{
		// The usage-authority infrastructure tree moved to the Enterprise tree
		// with task 6.1 Slice A; its quantity-only guard moved with it into
		// enterprise/internal/archtest. The usage-authority core tree moved
		// with task 6.1 Slice B; its quantity-only guard moved with it too.
		"internal/core/runtime/accounting_authority.go",
		"internal/core/runtime/authority_coord_adapter.go",
		"internal/core/runtime/authority_lifecycle.go",
		"internal/core/runtime/authority_lifecycle_release.go",
		"internal/core/runtime/authority_lifecycle_settle.go",
		"internal/infra/runtimebundle/authority_coord.go",
	}
	forbidden := regexp.MustCompile(`\b(AmountUnitMoneyNano|RuleKindBudget|RuleKindSpendCap|Spend|FinalCost|EstimatedCost|AuthoritativeCostPresent|CostAuthority)\b`)
	for _, rel := range roots {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		check := func(file string) error {
			if strings.HasSuffix(file, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if match := forbidden.FindString(string(raw)); match != "" {
				return &quantityOnlyViolation{file: file, symbol: match}
			}
			return nil
		}
		if info.IsDir() {
			err = filepath.Walk(path, func(file string, info os.FileInfo, walkErr error) error {
				if walkErr != nil || info.IsDir() || filepath.Ext(file) != ".go" {
					return walkErr
				}
				return check(file)
			})
		} else {
			err = check(path)
		}
		if err != nil {
			t.Fatalf("quantity-only guard: %v", err)
		}
	}
}

type quantityOnlyViolation struct {
	file   string
	symbol string
}

func (e *quantityOnlyViolation) Error() string {
	return "retired monetary UsageAuthority symbol " + e.symbol + " in production file " + e.file
}

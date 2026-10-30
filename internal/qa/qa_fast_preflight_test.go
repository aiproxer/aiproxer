//go:build precommit

package qa

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/archtest"
	"github.com/aiproxer/aiproxer/internal/qa/kirospec"
)

func TestQAFastPreflight_KiroLifecycle(t *testing.T) {
	t.Parallel()

	errs := kirospec.Validate(repositoryFile(t))
	if len(errs) != 0 {
		t.Fatalf("invalid Kiro spec state:\n%s", strings.Join(errs, "\n"))
	}
}

func TestQAFastPreflight_ArchitectureBudgets(t *testing.T) {
	t.Parallel()
	root := repositoryFile(t)

	for _, budget := range archtest.CriticalFileBudgets {
		lines, err := archtest.CountFileLines(filepath.Join(root, filepath.FromSlash(budget.Path)))
		if err != nil {
			t.Errorf("%s: %v", budget.Path, err)
			continue
		}
		if lines > budget.Max {
			t.Errorf("%s: measured %d exceeds budget ceiling %d", budget.Path, lines, budget.Max)
		}
	}
	for _, budget := range archtest.LineBudgets {
		lines, err := archtest.CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Dir)))
		if err != nil {
			t.Errorf("%s: %v", budget.Dir, err)
			continue
		}
		if lines > budget.Max {
			t.Errorf("%s: measured %d exceeds budget ceiling %d", budget.Dir, lines, budget.Max)
		}
	}
}

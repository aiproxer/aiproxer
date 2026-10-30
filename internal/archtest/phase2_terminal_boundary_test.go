package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase2RuntimeDoesNotWireRetiredCentralAppendLayer(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/core/runtime",
		"internal/infra/runtimebundle",
	} {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			text, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			assertNoRetiredAppendSymbols(t, filepath.ToSlash(path), string(text))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", rel, err)
		}
	}
	// The billing port definition site moved with the billing domain (task
	// 6.5); enterprise/internal/archtest guards it.
}

func assertNoRetiredAppendSymbols(t *testing.T, path, text string) {
	t.Helper()
	for _, forbidden := range []string{
		"CallUsageAppender",
		"CallLegUsageAppender",
		"CallUsageAppenderFunc",
		"CallLegUsageAppenderFunc",
		"CentralSink",
		"AppendCallUsage",
		"AppendCallLegUsage",
		"UsageAppendWorker",
		"EnqueueCallUsageAppend",
		"EnqueueCallLegUsageAppend",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s wires retired central append symbol %q", path, forbidden)
		}
	}
}

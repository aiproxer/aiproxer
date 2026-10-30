package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Task 2.3 consumer surfaces that must not select transitional internal
// contract vocabulary (ReloadTrigger/ReloadResult/ReloadStatus, constants).
func TestReloadContract_PublicHTTPCmdConsumersForbidInternalVocabulary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	targets := []string{
		filepath.Join(root, "pkg", "aipruntime"),
		filepath.Join(root, "internal", "stdhttp", "admin", "configreload"),
		filepath.Join(root, "cmd", "aipstd"),
	}
	var findings []string
	for _, dir := range targets {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			bad, err := scanInternalReloadContractSelectors(rel, string(src))
			if err != nil {
				return err
			}
			findings = append(findings, bad...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("public/HTTP/cmd production files must not select reload contract vocabulary from internal/core/configreload (use pkg/aipsdk/configreload); policy/algorithm selectors remain allowed only in the policy owner:\n%s",
			strings.Join(findings, "\n"))
	}
}

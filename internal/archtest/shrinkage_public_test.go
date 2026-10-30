package archtest

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

func skipMissingPublicBaseline(t *testing.T, root, sha string) {
	t.Helper()
	if !opensource.IsOpenSourceTree(root) {
		return
	}
	if _, err := runCmd(root, "git", "cat-file", "-e", sha+"^{commit}"); err != nil {
		t.Skipf("public export has fresh history without private baseline commit %s", sha)
	}
}

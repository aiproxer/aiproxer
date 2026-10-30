package opensource

import (
	"os"
	"path/filepath"
	"testing"
)

// The public export omits enterprise; all other stat outcomes must fail closed.
func TestIsOpenSourceTree_RequiresAbsentEnterprise(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if !IsOpenSourceTree(root) {
		t.Fatal("absent enterprise must identify the public tree")
	}
	enterprise := filepath.Join(root, "enterprise")
	if err := os.Mkdir(enterprise, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsOpenSourceTree(root) {
		t.Fatal("present enterprise must keep private checks enabled")
	}
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if IsOpenSourceTree(file) {
		t.Fatal("stat errors other than absence must keep private checks enabled")
	}
}

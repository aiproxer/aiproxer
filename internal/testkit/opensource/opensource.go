// Package opensource identifies the public export without relying on Git history.
package opensource

import (
	"os"
	"path/filepath"
)

// IsOpenSourceTree reports whether root lacks the Enterprise tree. Other stat
// errors retain the private repository's fail-closed checks.
func IsOpenSourceTree(root string) bool {
	_, err := os.Stat(filepath.Join(root, "enterprise"))
	return os.IsNotExist(err)
}

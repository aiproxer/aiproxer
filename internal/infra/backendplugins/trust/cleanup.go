package trust

import "os"

// CleanupStaged removes a staged artifact path after failed upgrade/rollback.
func CleanupStaged(path string) error {
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

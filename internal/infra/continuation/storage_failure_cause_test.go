package continuation_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	infra "github.com/aiproxer/aiproxer/internal/infra/continuation"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// TestFileStoreStorageFailureRetainsCause proves a storage-failure wrap keeps the
// underlying cause reachable: the sentinel names the boundary, the cause names
// why, and error text is not a classifier.
func TestFileStoreStorageFailureRetainsCause(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "continuation.json")
	if err := os.WriteFile(path, []byte(`{"records":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := infra.NewFileStore(path, aipcont.DefaultStorageLimits())
	if !errors.Is(err, aipcont.ErrStorageFailure) {
		t.Fatalf("corrupt constructor error=%v want ErrStorageFailure", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("storage failure dropped its decode cause: %v", err)
	}
}

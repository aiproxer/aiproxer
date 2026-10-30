package discovery_test

import (
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
)

// wrappedSymlinkFS delegates Lstat/ReadDir to the OS but reports every Open
// as a wrapped ErrSymlinkRejected, modelling instrumentation that adds
// context while preserving the cause chain.
type wrappedSymlinkFS struct{}

func (wrappedSymlinkFS) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(name)
}

func (wrappedSymlinkFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (wrappedSymlinkFS) Open(name string) (*os.File, error) {
	return nil, fmt.Errorf("probe open %s: %w", name, discovery.ErrSymlinkRejected)
}

func TestDiscovery_WrappedSymlinkRejectionKeepsReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeManifest(t, root, "w.backendplugin.json", manifestBody("io.wrapped", "kw", runtime.GOOS))
	res, err := discovery.Discover(discovery.Config{
		ExplicitPaths: []string{root},
		Development:   true,
		FS:            wrappedSymlinkFS{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 1 {
		t.Fatalf("%+v", res.Descriptors)
	}
	got := res.Descriptors[0]
	if got.Status != discovery.StatusSkipped || got.Reason != "symlink_rejected" {
		t.Fatalf("want skipped/symlink_rejected, got %+v", got)
	}
}

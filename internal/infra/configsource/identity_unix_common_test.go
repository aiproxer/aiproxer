//go:build unix

package configsource

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIdentityConstructors_TagProvenance locks the scheme tag on each Unix
// constructor so a future refactor cannot silently emit untagged identities
// that would compare equal across differing metadata.
func TestIdentityConstructors_TagProvenance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := identityFromFileInfo(fi)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Scheme != identitySchemeDeviceInode {
		t.Fatalf("fallback scheme=%q want %q", fallback.Scheme, identitySchemeDeviceInode)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fromFile, err := identityFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	fromPath, err := identityFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both handle- and path-derived identities for one stable file must agree,
	// including provenance, or ReadStable's self-consistency check would fail.
	if fromFile != fromPath {
		t.Fatalf("handle identity %+v != path identity %+v", fromFile, fromPath)
	}
	if fromFile.Scheme == "" {
		t.Fatal("live identity must carry a provenance scheme")
	}
}

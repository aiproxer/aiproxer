//go:build !windows

package wrapperinstall

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const restrictiveUmaskHelperEnv = "WRAPPERINSTALL_RESTRICTIVE_UMASK_HELPER"

// TestEnsurePublishesExecutableUnderRestrictiveUmask proves the final mode does
// not depend on the process umask. The extraction file is created with O_CREATE
// and 0755, which the umask filters; without an explicit chmod the connector
// would resolve a binary it cannot launch. The umask is process-global, so the
// assertion runs in an isolated subprocess instead of disturbing siblings.
func TestEnsurePublishesExecutableUnderRestrictiveUmask(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestrictiveUmaskHelper$", "-test.v")
	cmd.Env = append(os.Environ(), restrictiveUmaskHelperEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restrictive-umask helper failed: %v\n%s", err, out)
	}
}

// TestRestrictiveUmaskHelper is the subprocess half of
// TestEnsurePublishesExecutableUnderRestrictiveUmask. It is skipped unless the
// parent selected it, so a normal run does not change the test process umask.
func TestRestrictiveUmaskHelper(t *testing.T) {
	if os.Getenv(restrictiveUmaskHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	syscall.Umask(0o077)
	server, _ := releaseFixture(t)
	defer server.Close()

	cache := t.TempDir()
	path, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if err != nil {
		t.Fatalf("Ensure under umask 077: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("published mode = %#o, want 0755 despite the restrictive umask", got)
	}
}

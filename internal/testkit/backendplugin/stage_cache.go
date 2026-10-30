package backendplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type cachedConnectorBinary struct {
	path   string
	digest string
}

var (
	connectorCacheMu sync.Mutex
	connectorCache   = make(map[string]cachedConnectorBinary)
)

func getCachedConnectorBinary(tb testing.TB, connectorDir, cmdPkg, binName string) (cachedConnectorBinary, string) {
	tb.Helper()
	repo := findRepoRoot(tb)
	if runtime.GOOS == "windows" && !strings.HasSuffix(binName, ".exe") {
		binName += ".exe"
	}
	key := connectorDir + "/" + cmdPkg
	connectorCacheMu.Lock()
	if cb, ok := connectorCache[key]; ok {
		connectorCacheMu.Unlock()
		return cb, binName
	}
	defer connectorCacheMu.Unlock()

	cacheDir, err := os.MkdirTemp("", "aip-test-connector-bin-")
	if err != nil {
		tb.Fatal(err)
	}
	dst := filepath.Join(cacheDir, binName)
	cmd := exec.Command("go", "build", "-ldflags=-w", "-o", dst, cmdPkg)
	cmd.Dir = filepath.Join(repo, connectorDir)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("build connector %s: %v\n%s", connectorDir, err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		tb.Fatal(err)
	}
	sum := sha256.Sum256(data)
	cb := cachedConnectorBinary{
		path:   dst,
		digest: hex.EncodeToString(sum[:]),
	}
	connectorCache[key] = cb
	return cb, binName
}

func stageCachedBinary(tb testing.TB, cb cachedConnectorBinary, root, binName string) (rel, digest string) {
	tb.Helper()
	rel = filepath.ToSlash(filepath.Join("bin", binName))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		tb.Fatal(err)
	}
	data, err := os.ReadFile(cb.path)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		tb.Fatal(err)
	}
	return rel, cb.digest
}

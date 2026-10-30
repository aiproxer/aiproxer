package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageLocalStub builds connectors/localstub into a discovery root with a
// closed manifest suitable for BuildBootstrap discover→trust→install.
func StageLocalStub(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/localstub", "./cmd/aip-backend-localstub", "aip-backend-localstub")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.localstub",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"local-stub",
    "credential_mode":"none",
    "access_scope":"any",
    "process_sharing":"per_instance",
    "execution_class":"inference"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

func findRepoRoot(tb testing.TB) string {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "connectors", "localstub", "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("repo root with connectors/localstub not found")
		}
		dir = parent
	}
}

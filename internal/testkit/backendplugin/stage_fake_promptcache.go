package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageFakePromptCache builds the prompt-cache-capable fake backendplugin (it
// advertises protocol minor 7 and prompt-cache residency) into a discovery root
// with a closed manifest suitable for BuildBootstrap discover->trust->install.
// Test capability only: the binary emits one renewable prompt-cache observation
// and a finished OS-command tool event on a successful execute.
func StageFakePromptCache(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, ".", "./internal/testkit/backendplugin/cmd/aip-backendplugin-fake-promptcache", "aip-backendplugin-fake-promptcache")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.fake-promptcache",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":7,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"fake",
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance",
    "execution_class":"inference"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

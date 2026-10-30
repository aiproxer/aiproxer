package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageOpenRouter builds connectors/openrouter into a discovery root with a
// closed manifest suitable for BuildBootstrap discover→trust→install.
func StageOpenRouter(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/openrouter", "./cmd/aip-backend-openrouter", "aip-backend-openrouter")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.openrouter",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"openrouter",
    "credential_mode":"static",
    "access_scope":"any",
    "process_sharing":"per_instance",
    "credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENROUTER_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"openrouter"}
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageCodex builds connectors/codex into a discovery root with a closed
// manifest exporting both openai-codex and openai-codex-app-server (per_instance).
func StageCodex(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/codex", "./cmd/aip-backend-codex", "aip-backend-codex")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.codex",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[
    {"kind":"openai-codex","credential_mode":"static","access_scope":"local_only","process_sharing":"per_instance","credential_contract":{"version":2,"class":"static_nonrotatable","source":"host_env","env_root":"OPENAI_CODEX_ACCESS_TOKEN","slot":"api_token","selection":"no_rotation","quota_domain":"codex"}},
    {"kind":"openai-codex-app-server","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"}
  ]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

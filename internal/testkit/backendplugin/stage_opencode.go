package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageOpenCode builds connectors/opencode into a discovery root with a closed
// manifest exporting both opencode-go and opencode-zen (per_instance).
func StageOpenCode(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/opencode", "./cmd/aip-backend-opencode", "aip-backend-opencode")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.opencode",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[
    {"kind":"opencode-go","credential_mode":"static","access_scope":"any","process_sharing":"per_instance","credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENCODE_GO_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"opencode-go"}},
    {"kind":"opencode-zen","credential_mode":"static","access_scope":"any","process_sharing":"per_instance","credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENCODE_ZEN_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"opencode-zen"}}
  ]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

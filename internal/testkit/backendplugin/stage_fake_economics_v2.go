package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageFakeEconomicsV2 builds the host-bound-V2-capable fake backendplugin (it
// advertises protocol minor 10 and the host_bound_economic_evidence_v2
// capability) into a discovery root with a closed manifest suitable for
// BuildBootstrap discover->trust->install.
//
// The staged connector is the ordinary fake executable in its V2 mode: it
// emits typed V2 provider claims that carry identity and quantity facts only
// (the provider's own account and request identities, the transport B-leg
// pseudonym the host supplied, and the provider's reported token quantities).
// It never prices, rates, selects a payer, applies a policy, or infers an
// account, so no money value exists anywhere in the fixture.
//
// Test capability only: every value the claim carries is supplied explicitly
// by the test that owns the instance configuration.
func StageFakeEconomicsV2(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, ".", "./internal/testkit/backendplugin/cmd/aip-backendplugin-fake-economics-v2", "aip-backendplugin-fake-economics-v2")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.fake-economics-v2",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":10,
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

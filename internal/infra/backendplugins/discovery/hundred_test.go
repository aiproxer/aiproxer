package discovery_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"go.uber.org/goleak"
)

//nolint:paralleltest // goleak.VerifyNone conflicts with parallel tests
func TestHundredSyntheticManifests_NoLaunch(t *testing.T) {
	defer goleak.VerifyNone(t)
	root := t.TempDir()
	for i := range 100 {
		name := fmt.Sprintf("p%03d.backendplugin.json", i)
		writeManifest(t, root, name, manifestBody(fmt.Sprintf("io.p%03d", i), fmt.Sprintf("k%03d", i), runtime.GOOS))
	}
	res, err := discovery.Discover(discovery.Config{ExplicitPaths: []string{root}, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 100 {
		t.Fatalf("got %d", len(res.Descriptors))
	}
	for _, d := range res.Descriptors {
		if d.Status != discovery.StatusDiscovered {
			t.Fatalf("%+v", d)
		}
	}
}

func writeManifest(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func manifestBody(pluginID, kind, goos string) string {
	exe := "bin/plugin"
	platforms := fmt.Sprintf(`[{"os":%q,"arch":"amd64"}]`, goos)
	if goos == "windows" {
		exe = "bin/plugin.exe"
	}
	if goos == "darwin" {
		platforms = `[{"os":"darwin","arch":"amd64"},{"os":"darwin","arch":"arm64"}]`
	}
	return fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":%q,
  "version":"1.0.0",
  "build_id":"b",
  "executable":%q,
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":%s,
  "exports":[{
    "kind":%q,
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`, pluginID, exe, platforms, kind)
}

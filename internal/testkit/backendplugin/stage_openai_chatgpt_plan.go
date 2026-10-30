package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Staged manifest file names for the coexistence root.
//
// A discovery root may hold more than one manifest: discovery scans the root's
// directory entries for every *.backendplugin.json file and gives each one a
// distinct safe id (the root's base name plus the manifest file name). The
// coexistence root therefore publishes the two connectors under separate file
// names so both are discoverable side by side.
const (
	// codexManifestFileName is the legacy private-Codex manifest file name in
	// the coexistence root.
	codexManifestFileName = "codex.backendplugin.json"
	// openAIChatGPTPlanManifestFileName is the new official ChatGPT-plan
	// manifest file name in the coexistence root.
	openAIChatGPTPlanManifestFileName = "openai-chatgpt-plan.backendplugin.json"
)

// StageOpenAIChatGPTPlan builds connectors/openaichatgptplan into a discovery
// root with a closed manifest exporting the direct-inference factory
// openai-chatgpt-plan (oauth_user, local_only, per_instance).
//
// The manifest mirrors the connector's packaged manifest template
// (connectors/openaichatgptplan/manifest/template.backendplugin.json). The
// connector module's parity suite keeps that template and the runtime
// descriptor in agreement, and the host re-checks the staged manifest against
// the live runtime descriptor at activation, so a posture drift fails the
// activation instead of passing silently.
func StageOpenAIChatGPTPlan(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/openaichatgptplan", "./cmd/aip-backend-openaichatgptplan", "aip-backend-openaichatgptplan")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	stageOpenAIChatGPTPlanManifest(tb, root, rel, digest)
	return root
}

// openAIChatGPTPlanReferenceFileName is the sidecar that carries the reference
// base URL to the connector's TEST-ONLY executable. It sits beside the staged
// binary, and only that binary reads it.
const openAIChatGPTPlanReferenceFileName = "responses-reference-base-url"

// StageOpenAIChatGPTPlanReference stages the connector's TEST-ONLY reference
// executable with a local reference endpoint.
//
// The connector's production configuration surface has no endpoint key: the
// selected registration's bearer credential is only ever sent to the documented
// public Responses resource. The staged host integration still needs to observe
// the exact wire request, so it runs the connector's test-only command
// (internal/testcmd), which takes its endpoint from a sidecar file beside the
// executable and constructs the same service through
// service.NewWithReferenceEndpoint.
//
// The staged manifest, the ABI surface, the descriptor and the connector
// service are the production ones. Only the endpoint the test observes differs,
// and no YAML or production constructor can reach it.
func StageOpenAIChatGPTPlanReference(tb testing.TB, referenceBaseURL string) (pluginRoot string) {
	tb.Helper()
	reference := strings.TrimSpace(referenceBaseURL)
	if reference == "" {
		tb.Fatal("StageOpenAIChatGPTPlanReference: the reference base URL is empty")
	}
	cb, binName := getCachedConnectorBinary(tb, "connectors/openaichatgptplan", "./internal/testcmd", "aip-backend-openaichatgptplan")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	if err := os.WriteFile(
		filepath.Join(root, "bin", openAIChatGPTPlanReferenceFileName),
		[]byte(reference),
		0o600,
	); err != nil {
		tb.Fatal(err)
	}
	stageOpenAIChatGPTPlanManifest(tb, root, rel, digest)
	return root
}

// stageOpenAIChatGPTPlanManifest writes the closed plugin manifest a staged
// openai-chatgpt-plan root publishes.
func stageOpenAIChatGPTPlanManifest(tb testing.TB, root, rel, digest string) {
	tb.Helper()
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.openai-chatgpt-plan",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"openai-chatgpt-plan",
    "credential_mode":"oauth_user",
    "access_scope":"local_only",
    "process_sharing":"per_instance",
    "execution_class":"inference"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
}

// StageCodexAndOpenAIChatGPTPlan stages the legacy private-Codex connector
// and the new official ChatGPT-plan connector into ONE discovery root.
//
// The root holds both real connector executables and two manifests with
// distinct plugin ids, so the host can discover, trust and configure the
// legacy and the new factory side by side while the new connector is still
// under validation (requirement 1.6). The legacy manifest is the byte
// identical manifest StageCodex writes; only its file name differs, because
// both manifests have to live in the same root.
func StageCodexAndOpenAIChatGPTPlan(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	root := tb.TempDir()
	stageConnectorInto(tb, root, StageCodex(tb), codexManifestFileName)
	stageConnectorInto(tb, root, StageOpenAIChatGPTPlan(tb), openAIChatGPTPlanManifestFileName)
	return root
}

// stageConnectorInto copies one staged connector (its executables and its
// manifest) into a shared discovery root under the given manifest file name.
//
// The manifest bytes are copied verbatim, so a connector staged in a shared
// root carries exactly the manifest its own staging helper publishes.
func stageConnectorInto(tb testing.TB, dstRoot, srcRoot, manifestName string) {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(srcRoot, "plugin.backendplugin.json"))
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRoot, manifestName), raw, 0o600); err != nil {
		tb.Fatal(err)
	}
	binDir := filepath.Join(srcRoot, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		tb.Fatal(err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(binDir, ent.Name()))
		if err != nil {
			tb.Fatal(err)
		}
		target := filepath.Join(dstRoot, "bin", ent.Name())
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			tb.Fatal(err)
		}
	}
}

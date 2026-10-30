package backendplugins_docs_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cursorSDKExample is the documented shape this test guards: the disabled
// cursorsdk backend row plus the discovery binding an operator enables with it.
//
// The connector that reads this configuration is external now, and it enforces
// two things the example has to keep telling the truth about: key material
// never appears in connector YAML (a literal api_key fails closed), and an
// omitted bridge_executable selects the packaged companion on linux/amd64
// while windows/amd64 needs the absolute installed launcher path.
type cursorSDKExample struct {
	Plugins struct {
		BackendDiscovery struct {
			Enabled   bool `yaml:"enabled"`
			Instances map[string]struct {
				Kind       string `yaml:"kind"`
				SecretRefs map[string]struct {
					Env string `yaml:"env"`
				} `yaml:"secret_refs"`
			} `yaml:"instances"`
		} `yaml:"backend_discovery"`
		Backends []struct {
			Kind   string         `yaml:"kind"`
			ID     string         `yaml:"id"`
			Config map[string]any `yaml:"config"`
		} `yaml:"backends"`
	} `yaml:"plugins"`
}

// normalizeConfigKey matches the connector's own field comparison: case
// insensitive with - and _ unified, so API-KEY and api_key cannot slip past.
func normalizeConfigKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
}

// TestExample_CursorSDKDeclaresHostSecretsNotAnInlineKey keeps the staged
// example from documenting a configuration the connector refuses.
//
// The example used to carry `api_key` and a bare `bridge_executable:
// aip-cursor-sdk-bridge`. Both are wrong now: the key comes only from
// CURSOR_API_KEY through a discovery binding, and a bare executable name
// bypasses the packaged private companion that linux/amd64 resolves by
// default. The example is disabled by default, so nothing else exercises it;
// a wrong field here is what an operator copies.
func TestExample_CursorSDKDeclaresHostSecretsNotAnInlineKey(t *testing.T) {
	t.Parallel()

	root := repoRootFromDocs(t)
	rel := "config/examples/cursor-sdk-experimental.yaml"
	raw := readAbs(t, filepath.Join(root, filepath.FromSlash(rel)))

	var doc cursorSDKExample
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}

	found := -1
	for i := range doc.Plugins.Backends {
		if doc.Plugins.Backends[i].Kind == "cursorsdk" {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatalf("%s: no cursorsdk backend row; the guide points operators here", rel)
	}
	backend := doc.Plugins.Backends[found]

	for name := range backend.Config {
		switch normalizeConfigKey(name) {
		case "api_key", "apikey", "api_keys", "apikeys":
			t.Fatalf("%s: cursorsdk config carries %q, but a literal credential field fails closed; "+
				"bind api_key through backend_discovery.instances secret_refs", rel, name)
		}
	}
	for name := range backend.Config {
		if normalizeConfigKey(name) == "bridge_executable" {
			t.Fatalf("%s: cursorsdk config sets bridge_executable; on linux/amd64 the packaged default "+
				"resolves only when the field is omitted, and a bare executable name bypasses the "+
				"private companion entirely", rel)
		}
	}

	if !strings.Contains(string(raw), `.exe`) || !strings.Contains(string(raw), "bridge_executable: \"C:/") {
		t.Fatalf("%s: the example no longer shows the windows/amd64 absolute launcher path; that "+
			"platform cannot resolve the packaged default from its staged executable", rel)
	}

	// Enabling discovery is what an operator does next, so the binding has to
	// be present in the documented shape rather than invented at that point.
	if doc.Plugins.BackendDiscovery.Enabled {
		inst, ok := doc.Plugins.BackendDiscovery.Instances[backend.ID]
		if !ok {
			t.Fatalf("%s: discovery is enabled but instance %q has no binding; an agent runtime "+
				"resolves secrets only from explicit secret_refs", rel, backend.ID)
		}
		if inst.Kind != "cursorsdk" {
			t.Fatalf("%s: instance %q binds kind %q, want cursorsdk", rel, backend.ID, inst.Kind)
		}
		if got := inst.SecretRefs["api_key"].Env; got != "CURSOR_API_KEY" {
			t.Fatalf("%s: instance %q binds api_key to %q, want the approved root CURSOR_API_KEY",
				rel, backend.ID, got)
		}
	}
}

// TestOperator_CursorSDKGuideDocumentsTheHostSecretPath guards the operator guide and
// the commented config sample against reintroducing the retired inline key.
//
// The docs are the only instruction an operator has for a connector this
// repository no longer compiles, so a guide that says "set api_key" would send
// every enablement down a rejected path.
func TestOperator_CursorSDKGuideDocumentsTheHostSecretPath(t *testing.T) {
	t.Parallel()

	root := repoRootFromDocs(t)
	guide := string(readAbs(t, filepath.Join(root, "docs", "cursor-sdk-backend.md")))

	if !strings.Contains(guide, "secret_refs") {
		t.Fatal("docs/cursor-sdk-backend.md does not document the backend_discovery.instances secret_refs binding")
	}
	if !strings.Contains(guide, "api_key: {env: CURSOR_API_KEY}") {
		t.Fatal("docs/cursor-sdk-backend.md does not show the api_key -> CURSOR_API_KEY binding verbatim")
	}
	for line := range strings.SplitSeq(guide, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| `api_key`") {
			t.Fatalf("docs/cursor-sdk-backend.md still lists api_key as a connector config key: %q; "+
				"the connector rejects it and the host binding is the only accepted source", line)
		}
	}
	if !strings.Contains(guide, "packaged default") {
		t.Fatal("docs/cursor-sdk-backend.md does not state that linux/amd64 uses the packaged default " +
			"companion when bridge_executable is omitted")
	}
	if !strings.Contains(guide, "absolute") {
		t.Fatal("docs/cursor-sdk-backend.md does not state the windows/amd64 absolute launcher requirement")
	}

	sample := readAbs(t, filepath.Join(root, "config", "config.yaml"))
	block := cursorsdkSampleBlock(t, sample)
	if strings.Contains(block, `api_key: ""`) {
		t.Fatal("config/config.yaml still offers an inline api_key placeholder in the cursorsdk sample; " +
			"a copied line fails closed")
	}
	if !strings.Contains(block, "secret_refs") {
		t.Fatal("config/config.yaml's cursorsdk sample does not mention the secret_refs binding")
	}
}

// cursorsdkSampleBlock returns the commented cursorsdk sample in
// config/config.yaml: from its marker to the first following line that is not
// part of the commented block. The sample is comment-only on purpose (the
// backend is disabled), so parsing the file cannot see it.
func cursorsdkSampleBlock(t *testing.T, sample string) string {
	t.Helper()

	lines := strings.Split(sample, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "# - kind: cursorsdk") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("config/config.yaml has no commented cursorsdk sample; the guide points operators at it")
	}
	end := start
	for end < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end]), "#") {
		end++
	}
	return strings.Join(lines[start:end], "\n")
}

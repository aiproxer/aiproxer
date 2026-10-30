package backendplugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

func TestMaterializeExampleConfig_InjectsDiscoveryForLocalStub(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "stub.yaml")
	body := "server:\n  address: \"127.0.0.1:1\"\nplugins:\n  backends:\n    - kind: local-stub\n      id: x\n      enabled: true\n  frontends:\n    - id: openai-responses\n      enabled: true\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := bpkit.MaterializeExampleConfig(t, src)
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "backend_discovery:") {
		t.Fatal("expected injected backend_discovery")
	}
	if !strings.Contains(text, "development_mode: true") {
		t.Fatal("expected development_mode")
	}
	if !strings.Contains(text, "frontends:") {
		t.Fatal("must preserve sibling plugins.frontends")
	}
}

func TestMaterializeExampleConfig_RewritesExistingDiscoveryWithoutEatingSiblings(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "stub.yaml")
	body := "plugins:\n  backend_discovery:\n    enabled: true\n    development_mode: true\n    paths:\n      - .aip-plugins/full/localstub\n  frontends:\n    - id: openai-responses\n      enabled: true\n  backends:\n    - kind: local-stub\n      id: x\n      enabled: true\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := bpkit.MaterializeExampleConfig(t, src)
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "frontends:") || !strings.Contains(text, "openai-responses") {
		t.Fatalf("siblings lost:\n%s", text)
	}
	if strings.Contains(text, ".aip-plugins/full/localstub") {
		t.Fatal("expected staged path rewrite")
	}
}

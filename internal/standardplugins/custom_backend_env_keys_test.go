package standardplugins

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"gopkg.in/yaml.v3"
)

func clearCustomEnvRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv(root, "")
	for i := 1; i <= config.MaxUpstreamMaxEnvIndex; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", root, i), "")
	}
}

func TestCollectNumberedEnvKeys_numberedFromRoot(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "k1")
	t.Setenv(root+"_2", "k2")
	t.Setenv(root+"_3", "k3")
	got, err := compatmode.ResolveEnvAPIKeysWithPolicy(root, osenv.Process{}, config.DefaultUpstreamMaxEnvIndex)
	if err != nil {
		t.Fatalf("ResolveEnvAPIKeysWithPolicy: %v", err)
	}
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveEnvAPIKeysWithPolicy(%q) = %#v, want %#v", root, got, want)
	}
}

func TestCollectNumberedEnvKeys_rejectsRoot1Suffix(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "k1")
	t.Setenv(root+"_1", "ignored")
	_, err := compatmode.ResolveEnvAPIKeysWithPolicy(root, osenv.Process{}, config.DefaultUpstreamMaxEnvIndex)
	if err == nil {
		t.Fatal("expected root_1 rejection")
	}
	if !strings.Contains(err.Error(), root+"_1") {
		t.Fatalf("error %q must name the misconfigured variable", err.Error())
	}
}

func TestCollectNumberedEnvKeys_scansThroughGaps(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "a")
	t.Setenv(root+"_2", "")
	t.Setenv(root+"_3", "c")
	got, err := compatmode.ResolveEnvAPIKeysWithPolicy(root, osenv.Process{}, config.DefaultUpstreamMaxEnvIndex)
	if err != nil {
		t.Fatalf("ResolveEnvAPIKeysWithPolicy: %v", err)
	}
	want := []string{"a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveEnvAPIKeysWithPolicy(%q) = %#v, want %#v", root, got, want)
	}
}

func TestCompatibleCredential_resolveEnvRootOnly(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	t.Setenv(root+"_2", "env-two")
	got := compatmode.ResolveEnvAPIKeys(root)
	want := []string{"env-one", "env-two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCompatibleEnvAPIKeys(%q) = %#v, want %#v", root, got, want)
	}
}

func TestCompatibleCredential_emptyRootIsNoAuth(t *testing.T) {
	t.Parallel()
	got := compatmode.ResolveEnvAPIKeys("")
	if len(got) != 0 {
		t.Fatalf("empty root want nil/empty, got %#v", got)
	}
}

func TestCompatibleCredential_decodeRejectsLiteralAPIKey(t *testing.T) {
	t.Parallel()
	raw := `backend_prefix: provider
base_url: https://api.example.com/v1
api_key: yaml-key
api_key_env_var_root: MY_PROVIDER_API_KEY
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	_, err := config.DecodeCompatibleModeConfig("inst", CustomOpenAILegacyCompatibleID, node)
	if err == nil {
		t.Fatal("expected forbidden api_key rejection")
	}
}

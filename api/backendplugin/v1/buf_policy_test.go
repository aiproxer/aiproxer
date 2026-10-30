package backendpluginv1_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// frozenBufLintExceptions are STANDARD lint rules that conflict with the
// frozen versioned backend-plugin ABI. Renaming the package, service, or
// Execute stream messages would break the negotiated wire contract, the
// generated Go API, and every executable connector, so api/buf.yaml must
// carry these exceptions explicitly instead of drifting unobserved.
// See https://github.com/aiproxer/aiproxer-enterprise/issues/715.
var frozenBufLintExceptions = []string{
	"PACKAGE_DIRECTORY_MATCH",
	"SERVICE_SUFFIX",
	"RPC_REQUEST_STANDARD_NAME",
	"RPC_RESPONSE_STANDARD_NAME",
}

func readBufConfig(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read api/%s: %v", name, err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse api/%s: %v", name, err)
	}
	return cfg
}

func stringSet(values []any) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok {
			set[s] = true
		}
	}
	return set
}

func TestBufLintKeepsStandardWithFrozenABIExceptions(t *testing.T) {
	t.Parallel()
	cfg := readBufConfig(t, "buf.yaml")

	lint, ok := cfg["lint"].(map[string]any)
	if !ok {
		t.Fatal("api/buf.yaml: missing lint section")
	}
	uses, ok := lint["use"].([]any)
	if !ok {
		t.Fatal("api/buf.yaml: missing lint.use section")
	}
	if !stringSet(uses)["STANDARD"] {
		t.Error("api/buf.yaml: lint.use must keep STANDARD so new proto drift stays observed")
	}
	excepts, ok := lint["except"].([]any)
	if !ok {
		t.Fatal("api/buf.yaml: missing lint.except section")
	}
	set := stringSet(excepts)
	for _, rule := range frozenBufLintExceptions {
		if !set[rule] {
			t.Errorf("api/buf.yaml: lint.except must retain frozen-ABI rule %q", rule)
		}
	}
}

func TestBufBreakingKeepsFileDetection(t *testing.T) {
	t.Parallel()
	cfg := readBufConfig(t, "buf.yaml")

	breaking, ok := cfg["breaking"].(map[string]any)
	if !ok {
		t.Fatal("api/buf.yaml: missing breaking section")
	}
	uses, ok := breaking["use"].([]any)
	if !ok {
		t.Fatal("api/buf.yaml: missing breaking.use section")
	}
	if !stringSet(uses)["FILE"] {
		t.Error("api/buf.yaml: breaking.use must keep FILE detection")
	}
}

func TestBufGenerateKeepsPinnedGoPlugins(t *testing.T) {
	t.Parallel()
	cfg := readBufConfig(t, "buf.gen.yaml")

	plugins, ok := cfg["plugins"].([]any)
	if !ok || len(plugins) == 0 {
		t.Fatal("api/buf.gen.yaml: missing plugins section")
	}
	want := map[string]bool{"protoc-gen-go": false, "protoc-gen-go-grpc": false}
	for _, entry := range plugins {
		plugin, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := plugin["local"].(string)
		if _, known := want[name]; !known {
			continue
		}
		want[name] = true
		opts, _ := plugin["opt"].([]any)
		if !stringSet(opts)["paths=source_relative"] {
			t.Errorf("api/buf.gen.yaml: plugin %q must keep paths=source_relative", name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("api/buf.gen.yaml: missing local plugin %q", name)
		}
	}
}

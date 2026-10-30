package submitnoop

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeHookConfig_empty(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeHookConfig(yaml.Node{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LifecycleProbe || cfg.Order != nil {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeHookConfig_unknownKey(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`extra: 1`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeHookConfig(n)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeHookConfig_order(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`order: 7`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeHookConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order == nil || *cfg.Order != 7 {
		t.Fatalf("%+v", cfg)
	}
}

package toolcallrepair_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_Defaults(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Mode != toolcallrepair.ModeConservative {
		t.Fatalf("mode=%q want %q", cfg.Mode, toolcallrepair.ModeConservative)
	}
	if cfg.MaxArgsBytes != toolcallrepair.DefaultMaxArgsBytes {
		t.Fatalf("max_args_bytes=%d want %d", cfg.MaxArgsBytes, toolcallrepair.DefaultMaxArgsBytes)
	}
	if cfg.OnUnrepairable != toolcallrepair.OnUnrepairablePassThrough {
		t.Fatalf("on_unrepairable=%q want %q", cfg.OnUnrepairable, toolcallrepair.OnUnrepairablePassThrough)
	}
}

func TestDecodeConfig_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: false\n"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := toolcallrepair.DecodeConfig(n)
	if err == nil {
		t.Fatal("plugin YAML must not accept enabled; enablement is PluginConfig.Enabled")
	}
}

func TestDecodeConfig_RejectsInvalidRanges(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("max_args_bytes: -1\n"), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := toolcallrepair.DecodeConfig(n); err == nil {
		t.Fatal("expected max_args_bytes range error")
	}
}

func TestDecodeConfig_MaxArgsBytesPresence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "omitted_defaults", raw: "{}\n", want: toolcallrepair.DefaultMaxArgsBytes},
		{name: "explicit_valid", raw: "max_args_bytes: 1024\n", want: 1024},
		{name: "explicit_zero_rejected", raw: "max_args_bytes: 0\n", wantErr: true},
		{name: "explicit_negative_rejected", raw: "max_args_bytes: -1\n", wantErr: true},
		{name: "above_upper_rejected", raw: "max_args_bytes: 9000000\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &n); err != nil {
				t.Fatal(err)
			}
			cfg, err := toolcallrepair.DecodeConfig(n)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if cfg.MaxArgsBytes != tc.want {
				t.Fatalf("max_args_bytes=%d want %d", cfg.MaxArgsBytes, tc.want)
			}
		})
	}
}

func TestDecodeConfig_SchemaLimitUpperBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "max_cache_bytes", raw: "schema:\n  max_cache_bytes: 999999999\n"},
		{name: "max_cache_entries", raw: "schema:\n  max_cache_entries: 999999\n"},
		{name: "max_schema_bytes", raw: "schema:\n  max_schema_bytes: 999999999\n"},
		{name: "max_nodes", raw: "schema:\n  max_nodes: 999999999\n"},
		{name: "max_properties", raw: "schema:\n  max_properties: 999999999\n"},
		{name: "max_nesting_depth", raw: "schema:\n  max_nesting_depth: 999999\n"},
		{name: "max_local_ref_depth", raw: "schema:\n  max_local_ref_depth: 999999\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &n); err != nil {
				t.Fatal(err)
			}
			if _, err := toolcallrepair.DecodeConfig(n); err == nil {
				t.Fatal("expected upper-bound rejection")
			}
		})
	}
}

func TestDecodeConfig_SchemaDefaults(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}\n"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schema.MaxCacheEntries <= 0 || cfg.Schema.MaxSchemaBytes <= 0 {
		t.Fatalf("schema limits not defaulted: %+v", cfg.Schema)
	}
}

func TestFeatureID(t *testing.T) {
	t.Parallel()
	if toolcallrepair.ID != "tool-call-repair" {
		t.Fatalf("ID=%q", toolcallrepair.ID)
	}
}

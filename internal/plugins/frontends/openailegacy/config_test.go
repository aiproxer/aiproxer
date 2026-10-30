package openailegacy

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yamlStr string
		want    bool
		wantErr bool
	}{
		{
			name:    "empty config",
			yamlStr: "",
			want:    false,
			wantErr: false,
		},
		{
			name:    "null config",
			yamlStr: "null",
			want:    false,
			wantErr: false,
		},
		{
			name:    "valid mapping",
			yamlStr: "expose_aip_usage_extensions: true",
			want:    true,
			wantErr: false,
		},
		{
			name:    "invalid mapping key",
			yamlStr: "unknown_key: true",
			want:    false,
			wantErr: true,
		},
		{
			name:    "invalid structure",
			yamlStr: "[]",
			want:    false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yamlStr), &n); err != nil {
				t.Fatalf("failed to unmarshal yaml string: %v", err)
			}

			cfg, err := DecodeConfig(n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				if cfg.ExposeAipUsageExtensions != tt.want {
					t.Errorf("ExposeAipUsageExtensions = %v, want %v", cfg.ExposeAipUsageExtensions, tt.want)
				}
			}
		})
	}
}

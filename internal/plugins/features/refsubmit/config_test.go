package refsubmit

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_Success(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		yaml     string
		expected Config
	}{
		{
			name: "empty",
			yaml: "",
		},
		{
			name: "null string",
			yaml: "null",
		},
		{
			name: "mapping",
			yaml: "order: 5\nmarker: test",
			expected: Config{
				Order:  func() *int { i := 5; return &i }(),
				Marker: "test",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &n); err != nil {
				t.Fatal(err)
			}
			cfg, err := DecodeConfig(n)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.expected.Order != nil {
				if cfg.Order == nil || *cfg.Order != *tc.expected.Order {
					t.Errorf("expected order %d, got %+v", *tc.expected.Order, cfg.Order)
				}
			} else if cfg.Order != nil {
				t.Errorf("expected nil order, got %d", *cfg.Order)
			}
			if cfg.Marker != tc.expected.Marker {
				t.Errorf("expected marker %q, got %q", tc.expected.Marker, cfg.Marker)
			}
		})
	}
}

func TestDecodeConfig_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "negative order",
			yaml:    "order: -1",
			wantErr: "order must be non-negative",
		},
		{
			name:    "scalar non-null",
			yaml:    "test",
			wantErr: "config must be a mapping or null",
		},
		{
			name:    "sequence",
			yaml:    "- item1\n- item2",
			wantErr: "config must be a mapping or null",
		},
		{
			name:    "invalid type",
			yaml:    "order: string",
			wantErr: "yaml: unmarshal errors",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &n); err != nil {
				t.Fatal(err)
			}
			_, err := DecodeConfig(n)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestDecodeConfig_EmptyNode(t *testing.T) {
	t.Parallel()
	// Kind 0
	_, err := DecodeConfig(yaml.Node{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// DocumentNode with no content
	_, err = DecodeConfig(yaml.Node{Kind: yaml.DocumentNode})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

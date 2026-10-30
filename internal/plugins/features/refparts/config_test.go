package refparts

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          string
		wantError      string
		wantOrder      *int
		wantSuffix     string
		wantRespPrefix string
	}{
		{
			name:      "empty string",
			input:     "",
			wantError: "",
		},
		{
			name:      "null scalar",
			input:     "null",
			wantError: "",
		},
		{
			name:           "valid mapping",
			input:          "order: 5\nsuffix: foo\nresponse_prefix: bar",
			wantOrder:      func() *int { v := 5; return &v }(),
			wantSuffix:     "foo",
			wantRespPrefix: "bar",
		},
		{
			name:      "negative order",
			input:     "order: -1",
			wantError: "order must be non-negative",
		},
		{
			name:      "invalid type (array)",
			input:     "[]",
			wantError: "config must be a mapping or null",
		},
		{
			name:      "invalid type (scalar non-null)",
			input:     "foo",
			wantError: "config must be a mapping or null",
		},
		{
			name:      "decode error type mismatch",
			input:     "order: [invalid]",
			wantError: "ref-request-suffix: yaml:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var n yaml.Node
			if err := yaml.Unmarshal([]byte(tt.input), &n); err != nil {
				if tt.input != "" {
					t.Fatalf("setup error: %v", err)
				}
			}

			cfg, err := DecodeConfig(n)

			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got none", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got %q", tt.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantOrder != nil {
				if cfg.Order == nil {
					t.Errorf("expected order %d, got nil", *tt.wantOrder)
				} else if *cfg.Order != *tt.wantOrder {
					t.Errorf("expected order %d, got %d", *tt.wantOrder, *cfg.Order)
				}
			} else if cfg.Order != nil {
				t.Errorf("expected nil order, got %d", *cfg.Order)
			}

			if cfg.Suffix != tt.wantSuffix {
				t.Errorf("expected suffix %q, got %q", tt.wantSuffix, cfg.Suffix)
			}

			if cfg.ResponsePrefix != tt.wantRespPrefix {
				t.Errorf("expected response prefix %q, got %q", tt.wantRespPrefix, cfg.ResponsePrefix)
			}
		})
	}
}

func TestDecodeConfig_empty(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(yaml.Node{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order != nil || cfg.Suffix != "" || cfg.ResponsePrefix != "" {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_null(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`null`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order != nil {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_valid(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: 1\nsuffix: sfx\nresponse_prefix: pre"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order == nil || *cfg.Order != 1 || cfg.Suffix != "sfx" || cfg.ResponsePrefix != "pre" {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_negativeOrder(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: -1"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for negative order")
	}
}

func TestDecodeConfig_invalidType(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`"some string"`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestDecodeConfig_sequenceType(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`- 1`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for sequence type")
	}
}

func TestDecodeConfig_decodeError(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`order: "not an int"`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeConfig(n)
	if err == nil {
		t.Fatal("expected error for decode failure")
	}
}

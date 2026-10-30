package reftraffictranscript

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		input                string
		wantError            string
		wantOrder            *int
		wantRedactSubstrings []string
	}{
		{
			name:                 "empty string",
			input:                "",
			wantError:            "",
			wantRedactSubstrings: []string{defaultSecret},
		},
		{
			name:                 "null scalar",
			input:                "null",
			wantError:            "",
			wantRedactSubstrings: []string{defaultSecret},
		},
		{
			name:                 "valid mapping without redact_substrings",
			input:                "order: 5",
			wantOrder:            func() *int { v := 5; return &v }(),
			wantRedactSubstrings: []string{defaultSecret},
		},
		{
			name:                 "valid mapping with empty redact_substrings",
			input:                "order: 5\nredact_substrings: []",
			wantOrder:            func() *int { v := 5; return &v }(),
			wantRedactSubstrings: []string{defaultSecret},
		},
		{
			name:                 "valid mapping with custom redact_substrings",
			input:                "order: 5\nredact_substrings: ['foo', 'bar']",
			wantOrder:            func() *int { v := 5; return &v }(),
			wantRedactSubstrings: []string{"foo", "bar"},
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
			wantError: "ref-traffic-transcript: yaml:",
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

			if len(cfg.RedactSubstrings) != len(tt.wantRedactSubstrings) {
				t.Errorf("expected redact_substrings length %d, got %d", len(tt.wantRedactSubstrings), len(cfg.RedactSubstrings))
			} else {
				for i, v := range cfg.RedactSubstrings {
					if v != tt.wantRedactSubstrings[i] {
						t.Errorf("expected redact_substrings[%d] = %q, got %q", i, tt.wantRedactSubstrings[i], v)
					}
				}
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
	if cfg.Order != nil || len(cfg.RedactSubstrings) != 1 || cfg.RedactSubstrings[0] != defaultSecret {
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
	if cfg.Order != nil || len(cfg.RedactSubstrings) != 1 || cfg.RedactSubstrings[0] != defaultSecret {
		t.Fatalf("%+v", cfg)
	}
}

func TestDecodeConfig_valid(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: 1\nredact_substrings: ['secret']"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order == nil || *cfg.Order != 1 || len(cfg.RedactSubstrings) != 1 || cfg.RedactSubstrings[0] != "secret" {
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

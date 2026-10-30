package refautoappend_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
)

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	order5 := 5
	tests := []struct {
		name    string
		yaml    string
		node    *yaml.Node
		want    refautoappend.Config
		wantErr string
	}{
		{
			name: "empty node kind 0",
			node: &yaml.Node{Kind: 0},
			want: refautoappend.Config{FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name: "empty document node",
			node: &yaml.Node{Kind: yaml.DocumentNode},
			want: refautoappend.Config{FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name: "empty string",
			yaml: "",
			want: refautoappend.Config{FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name: "null scalar",
			yaml: "null",
			want: refautoappend.Config{FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name: "empty mapping",
			yaml: "{}",
			want: refautoappend.Config{FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name: "mapping with custom file_text",
			yaml: "file_text: custom text",
			want: refautoappend.Config{FileText: "custom text"},
		},
		{
			name: "mapping with order and no file_text",
			yaml: "order: 5",
			want: refautoappend.Config{Order: &order5, FileText: "\n[ref-autoappend-file]\n"},
		},
		{
			name:    "mapping with negative order",
			yaml:    "order: -1",
			wantErr: "order must be non-negative",
		},
		{
			name:    "scalar non-null",
			yaml:    "not_a_mapping",
			wantErr: "config must be a mapping or null",
		},
		{
			name:    "sequence node",
			yaml:    "- item",
			wantErr: "config must be a mapping or null",
		},
		{
			name:    "invalid type for order",
			yaml:    "order: not_an_int",
			wantErr: "cannot unmarshal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var n yaml.Node
			if tc.node != nil {
				n = *tc.node
			} else {
				if err := yaml.Unmarshal([]byte(tc.yaml), &n); err != nil {
					t.Fatalf("setup error: %v", err)
				}
			}

			got, err := refautoappend.DecodeConfig(n)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.FileText != tc.want.FileText {
				t.Errorf("FileText: got %q, want %q", got.FileText, tc.want.FileText)
			}

			if (got.Order == nil) != (tc.want.Order == nil) {
				t.Errorf("Order nilness mismatch: got %v, want %v", got.Order, tc.want.Order)
			} else if got.Order != nil && *got.Order != *tc.want.Order {
				t.Errorf("Order: got %d, want %d", *got.Order, *tc.want.Order)
			}
		})
	}
}

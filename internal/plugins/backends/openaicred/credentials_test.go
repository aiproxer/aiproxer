package openaicred_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
)

func TestCredentialsFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		primary   string
		list      []string
		want      []string
		wantError bool
	}{
		{
			name: "api_keys_only",
			list: []string{" k1 ", "", "k2"},
			want: []string{"k1", "k2"},
		},
		{
			name:    "api_key_when_no_list",
			primary: "  sk  ",
			want:    []string{"sk"},
		},
		{
			name:    "primary_then_list_order",
			primary: "primary",
			list:    []string{"a", "b"},
			want:    []string{"primary", "a", "b"},
		},
		{
			name:      "empty_no_primary_no_list",
			wantError: true,
		},
		{
			name:      "empty_list_only_blanks",
			list:      []string{"", "  "},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openaicred.CredentialsFromConfig(tt.primary, tt.list)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got len=%d %#v want %#v", len(got), got, tt.want)
			}
			for i := range tt.want {
				if got[i].Secret != tt.want[i] {
					t.Fatalf("got %#v want secrets %#v", got, tt.want)
				}
			}
		})
	}
}

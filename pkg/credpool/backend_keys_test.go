package credpool_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

func TestBackendKeySecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		primary   string
		list      []string
		want      []string
		wantError bool
	}{
		{
			name:    "from_list_trims_and_skips_empty",
			primary: "",
			list:    []string{" k1 ", "", "k2"},
			want:    []string{"k1", "k2"},
		},
		{
			name:    "primary_fallback_when_no_list",
			primary: "  sk  ",
			list:    nil,
			want:    []string{"sk"},
		},
		{
			name:    "primary_then_list_order",
			primary: "primary",
			list:    []string{"a", "b"},
			want:    []string{"primary", "a", "b"},
		},
		{
			name:    "dedupe_primary_repeated_in_list",
			primary: "same",
			list:    []string{"same", "other"},
			want:    []string{"same", "other"},
		},
		{
			name:      "empty_errors_no_primary_no_list",
			primary:   "",
			list:      nil,
			wantError: true,
		},
		{
			name:      "empty_errors_list_only_blanks",
			primary:   "",
			list:      []string{"", "  "},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := credpool.BackendKeySecrets(tt.primary, tt.list)
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
				if got[i] != tt.want[i] {
					t.Fatalf("got %#v want %#v", got, tt.want)
				}
			}
		})
	}
}

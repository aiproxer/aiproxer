package credpool_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

func TestSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []credpool.Credential
		want []string
	}{
		{name: "nil", in: nil, want: nil},
		{name: "empty", in: []credpool.Credential{}, want: nil},
		{
			name: "preserves_order",
			in: []credpool.Credential{
				{ID: "a", Secret: "sk-a"},
				{ID: "b", Secret: "sk-b"},
			},
			want: []string{"sk-a", "sk-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := credpool.Secrets(tt.in)
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

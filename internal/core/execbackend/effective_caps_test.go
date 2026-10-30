package execbackend

import (
	"context"
	"maps"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEffectiveCaps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := aipapi.Call{}
	cand := routing.AttemptCandidate{}

	tests := []struct {
		name string
		be   Backend
		want aipapi.BackendCaps
	}{
		{
			name: "nil resolve uses static caps",
			be: Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			},
			want: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		},
		{
			name: "resolve caps overrides static",
			be: Backend{
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
					return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
				},
			},
			want: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EffectiveCaps(ctx, tt.be, call, cand)
			if !maps.Equal(got, tt.want) {
				t.Fatalf("want caps %v, got %v", tt.want, got)
			}
		})
	}
}

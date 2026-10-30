package aipapi_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSentinelErrorPrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "ErrInvalidCall", err: aipapi.ErrInvalidCall},
		{name: "ErrCollectLimitExceeded", err: aipapi.ErrCollectLimitExceeded},
		{name: "ErrNilEventStream", err: aipapi.ErrNilEventStream},
		{name: "ErrNilContext", err: aipapi.ErrNilContext},
		{name: "ErrNilFixedEventStream", err: aipapi.ErrNilFixedEventStream},
		{name: "ErrMaxRouteAttempts", err: aipapi.ErrMaxRouteAttempts},
		{name: "ErrAllCandidatesContextLimitExceeded", err: aipapi.ErrAllCandidatesContextLimitExceeded},
		{name: "ErrCapabilityReject", err: aipapi.ErrCapabilityReject},
		{name: "ErrHookMutation", err: aipapi.ErrHookMutation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.err.Error()
			if len(got) < len("aipapi: ") || got[:len("aipapi: ")] != "aipapi: " {
				t.Fatalf("prefix = %q, want aipapi-prefixed sentinel", got)
			}
		})
	}
}

func TestIsAllCandidatesContextLimitExceeded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sentinel", err: aipapi.ErrAllCandidatesContextLimitExceeded, want: true},
		{name: "wrapped", err: fmt.Errorf("plan: %w", aipapi.ErrAllCandidatesContextLimitExceeded), want: true},
		{name: "double_wrapped", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", aipapi.ErrAllCandidatesContextLimitExceeded)), want: true},
		{name: "nil", err: nil, want: false},
		{name: "other_sentinel", err: aipapi.ErrMaxRouteAttempts, want: false},
		{name: "unrelated", err: errors.New("some failure"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := aipapi.IsAllCandidatesContextLimitExceeded(tt.err); got != tt.want {
				t.Fatalf("IsAllCandidatesContextLimitExceeded(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

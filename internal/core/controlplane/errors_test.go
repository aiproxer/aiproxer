package controlplane_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestSentinelErrorsAreStable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		code cp.ErrorCode
	}{
		{controlplane.ErrDisabled, cp.ErrCodeDisabled},
		{controlplane.ErrUnavailable, cp.ErrCodeUnavailable},
		{controlplane.ErrDegraded, cp.ErrCodeDegraded},
		{controlplane.ErrInvalidQuery, cp.ErrCodeInvalidQuery},
		{controlplane.ErrTooBroad, cp.ErrCodeTooBroad},
		{controlplane.ErrUnsupportedFilter, cp.ErrCodeUnsupportedFilter},
		{controlplane.ErrUnsafeEvidence, cp.ErrCodeUnsafeEvidence},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Fatalf("sentinel error for %q must not be nil", c.code)
		}
		if got := controlplane.Classify(c.err); got != c.code {
			t.Fatalf("Classify(%v) = %q, want %q", c.err, got, c.code)
		}
	}
}

func TestClassifyWrappedErrors(t *testing.T) {
	t.Parallel()
	wrapped := errors.Join(controlplane.ErrTooBroad, errors.New("limit exceeded"))
	if got := controlplane.Classify(wrapped); got != cp.ErrCodeTooBroad {
		t.Fatalf("Classify(wrapped) = %q, want %q", got, cp.ErrCodeTooBroad)
	}
	if got := controlplane.Classify(nil); got != "" {
		t.Fatalf("Classify(nil) = %q, want empty", got)
	}
	if got := controlplane.Classify(errors.New("unrelated")); got != "" {
		t.Fatalf("Classify(unrelated) = %q, want empty", got)
	}
}

func TestClassifyUnsupportedFilterExposesFields(t *testing.T) {
	t.Parallel()
	err := controlplane.NewUnsupportedFilterError([]string{"cost_center"})
	if !errors.Is(err, controlplane.ErrUnsupportedFilter) {
		t.Fatalf("unsupported filter error must wrap ErrUnsupportedFilter")
	}
	fields := controlplane.UnsupportedFilterFields(err)
	if len(fields) != 1 || fields[0] != "cost_center" {
		t.Fatalf("unsupported filter fields lost: %#v", fields)
	}
}

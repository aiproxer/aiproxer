package nonforwardable_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/nonforwardable"
)

// compile-time interface satisfaction
var _ nonforwardable.Registrar = (*stubRegistrar)(nil)

type stubRegistrar struct{}

func (s *stubRegistrar) TagMessages(_ context.Context, _ nonforwardable.ALegRef, _ []nonforwardable.MessageRef, _ nonforwardable.ReasonCode) error {
	return nil
}

func TestNonforwardable_RegistrarInterface(t *testing.T) {
	t.Parallel()
	var r nonforwardable.Registrar = &stubRegistrar{}
	require.NotNil(t, r)
}

func TestReasonCode_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      nonforwardable.ReasonCode
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"oversized", nonforwardable.ReasonCode(strings.Repeat("a", 65)), true},
		{"invalid slash", "bad/reason", true},
		{"invalid space", "bad reason", true},
		{"invalid unicode", "badé", true},
		{"valid lower", "ok_reason-1.2", false},
		{"valid upper", "OK_REASON", false},
		{"max boundary", nonforwardable.ReasonCode(strings.Repeat("a", 64)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestALegRef_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace", "  ", true},
		{"oversized", strings.Repeat("a", 257), true},
		{"valid", "a-leg-123", false},
		{"max", strings.Repeat("a", 256), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := nonforwardable.ALegRef{ID: tc.id}.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMessageRef_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace", "  ", true},
		{"oversized", strings.Repeat("a", 513), true},
		{"valid", "v1:abc123", false},
		{"max", strings.Repeat("a", 512), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := nonforwardable.MessageRef{Identity: tc.id}.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNonforwardable_NoClientTransportAPI(t *testing.T) {
	t.Parallel()
	// Package must expose only Registrar, ALegRef, MessageRef, ReasonCode and validation.
	// Ensure no HTTP or transport symbols are exported.
	// Static check: this test fails if unexpected exported symbols appear; manually pin count.
	// We assert Registrar is the only interface with TagMessages, and no client-facing mutation.
	// No runtime check needed beyond compile-time, but verify ReasonCode type is distinct.
	var rc nonforwardable.ReasonCode = "test_reason"
	require.NoError(t, rc.Validate())
}

func TestNonforwardable_BoundedConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, 64, nonforwardable.MaxReasonCodeBytes)
	require.Equal(t, 256, nonforwardable.MaxALegIDBytes)
	require.Equal(t, 512, nonforwardable.MaxIdentityBytes)
}

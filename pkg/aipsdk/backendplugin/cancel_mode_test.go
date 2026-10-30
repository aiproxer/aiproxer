package backendplugin_test

import (
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestCancelMode_Validation(t *testing.T) {
	t.Parallel()

	validModes := []backendplugin.CancelMode{
		backendplugin.CancelModeNone,
		backendplugin.CancelModeProvider,
		backendplugin.CancelModeTransport,
		backendplugin.CancelModeCloseOnly,
	}

	for _, mode := range validModes {
		if err := backendplugin.ValidateCancelMode(mode); err != nil {
			t.Errorf("ValidateCancelMode(%q) = %v, want nil", mode, err)
		}
	}

	invalidModes := []backendplugin.CancelMode{
		backendplugin.CancelModeUnspecified,
		backendplugin.CancelMode("unknown_mode"),
		backendplugin.CancelMode("invalid"),
	}

	for _, mode := range invalidModes {
		if err := backendplugin.ValidateCancelMode(mode); err == nil {
			t.Errorf("ValidateCancelMode(%q) = nil, want error", mode)
		}
	}
}

func TestCancelOutcome_ProtoConversion(t *testing.T) {
	t.Parallel()

	modes := []struct {
		sdkMode   backendplugin.CancelMode
		protoMode backendpluginv1.CancelMode
	}{
		{backendplugin.CancelModeNone, backendpluginv1.CancelMode_CANCEL_MODE_NONE},
		{backendplugin.CancelModeProvider, backendpluginv1.CancelMode_CANCEL_MODE_PROVIDER},
		{backendplugin.CancelModeTransport, backendpluginv1.CancelMode_CANCEL_MODE_TRANSPORT},
		{backendplugin.CancelModeCloseOnly, backendpluginv1.CancelMode_CANCEL_MODE_CLOSE_ONLY},
		{backendplugin.CancelModeUnspecified, backendpluginv1.CancelMode_CANCEL_MODE_UNSPECIFIED},
	}

	for _, tc := range modes {
		t.Run(string(tc.sdkMode), func(t *testing.T) {
			t.Parallel()
			outcome := &backendplugin.CancelOutcome{
				Acknowledged: true,
				Detail:       "test-detail",
				Reason:       backendplugin.CancelReasonHost,
				Mode:         tc.sdkMode,
			}

			protoMsg, err := backendplugin.CancelOutcomeToProto(outcome)
			if err != nil {
				t.Fatalf("CancelOutcomeToProto failed: %v", err)
			}
			if protoMsg.GetMode() != tc.protoMode {
				t.Errorf("protoMsg.GetMode() = %v, want %v", protoMsg.GetMode(), tc.protoMode)
			}

			back, err := backendplugin.CancelOutcomeFromProto(protoMsg)
			if err != nil {
				t.Fatalf("CancelOutcomeFromProto failed: %v", err)
			}
			if back.Mode != tc.sdkMode {
				t.Errorf("back.Mode = %v, want %v", back.Mode, tc.sdkMode)
			}
			if back.Acknowledged != outcome.Acknowledged || back.Reason != outcome.Reason || back.Detail != outcome.Detail {
				t.Errorf("back = %+v, want %+v", back, outcome)
			}
		})
	}
}

func TestCancellationHandshakeNegotiated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		neg  backendplugin.Negotiation
		want bool
	}{
		{
			name: "compatible with minor 8 and feature",
			neg: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
			},
			want: true,
		},
		{
			name: "compatible with higher minor and feature",
			neg: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: 9,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake, "other_feature"},
			},
			want: true,
		},
		{
			name: "incompatible",
			neg: backendplugin.Negotiation{
				Compatible:      false,
				NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
			},
			want: false,
		},
		{
			name: "older minor even with feature",
			neg: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorPromptCacheResidency,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
			},
			want: false,
		},
		{
			name: "minor 8 without feature",
			neg: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
				EnabledFeatures: []string{backendplugin.FeaturePromptCacheResidency},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := backendplugin.CancellationHandshakeNegotiated(tt.neg)
			if got != tt.want {
				t.Errorf("CancellationHandshakeNegotiated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServerFrameCancelOutcome_ShapeValidation(t *testing.T) {
	t.Parallel()

	validFrame := backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameCancelOutcome,
		CancelOutcome: &backendplugin.CancelOutcome{
			Acknowledged: true,
			Reason:       backendplugin.CancelReasonClient,
			Mode:         backendplugin.CancelModeProvider,
		},
	}
	if err := validFrame.ValidateShape(); err != nil {
		t.Errorf("validFrame.ValidateShape() = %v, want nil", err)
	}

	invalidModeFrame := backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameCancelOutcome,
		CancelOutcome: &backendplugin.CancelOutcome{
			Acknowledged: true,
			Reason:       backendplugin.CancelReasonClient,
			Mode:         backendplugin.CancelMode("bogus"),
		},
	}
	if err := invalidModeFrame.ValidateShape(); err == nil {
		t.Errorf("invalidModeFrame.ValidateShape() = nil, want error")
	}
}

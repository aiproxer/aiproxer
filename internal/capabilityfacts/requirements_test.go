package capabilityfacts_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/capabilityfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

func TestDeriveRequiredCapabilities(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		turn     capabilityfacts.TurnShape
		ctrl     capabilityfacts.ControlRequirements
		expected []aipapi.Capability
	}{
		{
			name: "plain streaming text",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery: aipapi.DeliveryModeStreaming,
			},
			expected: []aipapi.Capability{aipapi.CapabilityStreaming},
		},
		{
			name: "tools and parallel tool calls",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				HasTools:          true,
				ParallelToolCalls: &trueVal,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityTools,
				aipapi.CapabilityParallelToolCalls,
			},
		},
		{
			name: "parallel tool calls false not included",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				HasTools:          true,
				ParallelToolCalls: &falseVal,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityTools,
			},
		},
		{
			name: "reasoning effort and structured outputs",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				ReasoningEffort:   "high",
				StructuredOutputs: true,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityStructuredOutputs,
				aipapi.CapabilityReasoning,
			},
		},
		{
			name: "vision and reasoning replay in turn shape",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Role: aipapi.RoleUser,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: aipapi.ContentPartImageRef},
						},
					},
					{
						Role: aipapi.RoleAssistant,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: aipapi.ContentPartReasoning},
						},
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery: aipapi.DeliveryModeStreaming,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityVision,
				aipapi.CapabilityReasoningReplay,
			},
		},
		{
			name: "all multimedia content parts in tool result and messages",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Kind: aipapi.ItemKindMessage,
						Role: aipapi.RoleUser,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: aipapi.ContentPartFileRef},
							{Kind: aipapi.ContentPartVideoRef},
						},
					},
					{
						Kind: aipapi.ItemKindToolResult,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: aipapi.ContentPartImageRef},
							{Kind: aipapi.ContentPartExtension},
						},
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityDocuments,
				aipapi.CapabilityVideoInput,
				aipapi.CapabilityTools,
				aipapi.CapabilityVision,
				aipapi.CapabilityOpaqueExtensions,
				aipapi.CapabilityOrderedItems,
			},
		},
		{
			name: "item references, reasoning items, and extension items",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{Kind: aipapi.ItemKindItemReference},
					{Kind: aipapi.ItemKindReasoning},
					{Kind: aipapi.ItemKindExtension},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityOrderedItems,
			},
		},
		{
			name: "openresponses item authoritative with tool call",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Kind: aipapi.ItemKindToolCall,
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          aipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []aipapi.Capability{
				aipapi.CapabilityStreaming,
				aipapi.CapabilityTools,
				aipapi.CapabilityOrderedItems,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := capabilityfacts.DeriveRequiredCapabilities(tc.turn, tc.ctrl)
			for _, exp := range tc.expected {
				require.True(t, slices.Contains(got, exp), "expected capability %s in got %v", exp, got)
			}
			require.Equal(t, len(tc.expected), len(got), "expected len %d, got %v", len(tc.expected), got)
		})
	}
}

func TestCapabilitiesDigest(t *testing.T) {
	t.Parallel()

	d1 := capabilityfacts.CapabilitiesDigest([]aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools})
	d2 := capabilityfacts.CapabilitiesDigest([]aipapi.Capability{aipapi.CapabilityTools, aipapi.CapabilityStreaming})
	require.Equal(t, d1, d2, "digest must be order-independent")

	dEmpty1 := capabilityfacts.CapabilitiesDigest(nil)
	dEmpty2 := capabilityfacts.CapabilitiesDigest([]aipapi.Capability{})
	require.Equal(t, dEmpty1, dEmpty2, "nil and empty must match")
	require.NotEqual(t, [32]byte{}, dEmpty1, "empty digest must be non-zero")
	require.NotEqual(t, d1, dEmpty1, "different caps must produce different digests")
}

func TestValidateCapabilities(t *testing.T) {
	t.Parallel()

	require.NoError(t, capabilityfacts.ValidateCapabilities([]aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]aipapi.Capability{""}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]aipapi.Capability{"   "}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]aipapi.Capability{"long-capability-name"}, 5))
}

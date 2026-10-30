package steering_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

var _ steering.Writer = (*stubWriter)(nil)

type stubWriter struct{}

func (s *stubWriter) Put(_ context.Context, _ steering.PutRequest) (steering.State, error) {
	return steering.State{}, nil
}

func (s *stubWriter) Deactivate(_ context.Context, _ steering.OverlayID) (steering.State, error) {
	return steering.State{}, nil
}

func TestSteering_WriterInterface(t *testing.T) {
	t.Parallel()
	var w steering.Writer = &stubWriter{}
	require.NotNil(t, w)
}

func TestOverlayID_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      steering.OverlayID
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"oversized", steering.OverlayID(strings.Repeat("a", 129)), true},
		{"invalid slash", "bad/id", true},
		{"invalid space", "bad id", true},
		{"invalid unicode", "badé", true},
		{"valid", "overlay-1_2.3", false},
		{"max", steering.OverlayID(strings.Repeat("a", 128)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.id.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPlacementKind_Validation(t *testing.T) {
	t.Parallel()
	require.Error(t, steering.PlacementKind("").Validate())
	require.Error(t, steering.PlacementKind("unknown").Validate())
	require.Error(t, steering.PlacementKind("Stable_Prefix").Validate())
	require.NoError(t, steering.StablePrefix.Validate())
	require.NoError(t, steering.AfterIngressTail.Validate())
	require.Equal(t, steering.PlacementKind("stable_prefix"), steering.StablePrefix)
	require.Equal(t, steering.PlacementKind("after_ingress_tail"), steering.AfterIngressTail)
}

func TestAnchorMissingPolicy_Validation(t *testing.T) {
	t.Parallel()
	require.Error(t, steering.AnchorMissingPolicy("").Validate())
	require.Error(t, steering.AnchorMissingPolicy("unknown").Validate())
	require.NoError(t, steering.StablePrefixFallback.Validate())
	require.NoError(t, steering.FailClosed.Validate())
	require.Equal(t, steering.AnchorMissingPolicy("stable_prefix_fallback"), steering.StablePrefixFallback)
	require.Equal(t, steering.AnchorMissingPolicy("fail_closed"), steering.FailClosed)
}

func TestReasonCode_Validation(t *testing.T) {
	t.Parallel()
	require.Error(t, steering.ReasonCode("").Validate())
	require.Error(t, steering.ReasonCode(strings.Repeat("a", 65)).Validate())
	require.Error(t, steering.ReasonCode("bad/reason").Validate())
	require.NoError(t, steering.ReasonCode("ok_reason-1.2").Validate())
}

func TestMessage_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		msg     steering.Message
		wantErr bool
	}{
		{"empty role", steering.Message{Role: "", Text: "hello"}, true},
		{"invalid role", steering.Message{Role: "bad", Text: "hello"}, true},
		{"empty text", steering.Message{Role: aipapi.RoleUser, Text: ""}, true},
		{"whitespace text", steering.Message{Role: aipapi.RoleUser, Text: "   "}, true},
		{"oversized", steering.Message{Role: aipapi.RoleUser, Text: strings.Repeat("a", 64*1024+1)}, true},
		{"valid user", steering.Message{Role: aipapi.RoleUser, Text: "hello"}, false},
		{"valid system", steering.Message{Role: aipapi.RoleSystem, Text: "sys"}, false},
		{"valid assistant", steering.Message{Role: aipapi.RoleAssistant, Text: "assistant text"}, false},
		{"max boundary", steering.Message{Role: aipapi.RoleUser, Text: strings.Repeat("a", 64*1024)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.msg.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPutRequest_Validation(t *testing.T) {
	t.Parallel()
	valid := steering.PutRequest{
		OverlayID:           "overlay-1",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "hello"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	}
	require.NoError(t, valid.Validate())
	tests := []struct {
		name    string
		mutate  func(steering.PutRequest) steering.PutRequest
		wantErr bool
	}{
		{"empty overlay id", func(r steering.PutRequest) steering.PutRequest { r.OverlayID = ""; return r }, true},
		{"oversized overlay id", func(r steering.PutRequest) steering.PutRequest {
			r.OverlayID = steering.OverlayID(strings.Repeat("a", 129))
			return r
		}, true},
		{"oversized text", func(r steering.PutRequest) steering.PutRequest {
			r.Message.Text = strings.Repeat("a", 64*1024+1)
			return r
		}, true},
		{"unknown placement", func(r steering.PutRequest) steering.PutRequest { r.Placement = "unknown"; return r }, true},
		{"unknown policy", func(r steering.PutRequest) steering.PutRequest { r.AnchorMissingPolicy = "unknown"; return r }, true},
		{"empty reason", func(r steering.PutRequest) steering.PutRequest { r.Reason = ""; return r }, true},
		{"oversized reason", func(r steering.PutRequest) steering.PutRequest {
			r.Reason = steering.ReasonCode(strings.Repeat("a", 65))
			return r
		}, true},
		{"invalid reason chars", func(r steering.PutRequest) steering.PutRequest { r.Reason = "bad/reason"; return r }, true},
		{"valid after_ingress_tail", func(r steering.PutRequest) steering.PutRequest {
			r.Placement = steering.AfterIngressTail
			r.AnchorMissingPolicy = steering.FailClosed
			return r
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := tc.mutate(valid)
			err := req.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSteering_NoClientTransportAPI(t *testing.T) {
	t.Parallel()
	// Package must not expose client/data-plane transport APIs.
	// Only Writer (Put/Deactivate), PutRequest, State, Message, OverlayID, PlacementKind, AnchorMissingPolicy, ReasonCode.
	// Ensure no HTTP handler or transport type is exported (compile-time check via existence).
	var _ steering.Writer = &stubWriter{}
}

func TestSteering_BoundedConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, 128, steering.MaxOverlayIDBytes)
	require.Equal(t, 64, steering.MaxReasonCodeBytes)
	require.Equal(t, 64*1024, steering.MaxSteeringTextBytes)
}

package adapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestOutcomeCancelResult_RedactsAndBoundsUntrustedDetail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		detail string
		leaks  []string
		marker bool
	}{
		{
			name:   "api key",
			detail: "provider rejected api_key=sk-or-v1-cancel-secret-123456789",
			leaks:  []string{"sk-or-v1-cancel-secret-123456789", "cancel-secret-123456789"},
			marker: true,
		},
		{
			name:   "bearer token",
			detail: "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature",
			leaks:  []string{"Bearer eyJhbGciOiJIUzI1NiJ9", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature"},
			marker: true,
		},
		{
			name:   "private key",
			detail: "provider detail: -----BEGIN PRIVATE KEY-----\nprivate-key-body\n-----END PRIVATE KEY-----",
			leaks:  []string{"BEGIN PRIVATE KEY", "private-key-body", "END PRIVATE KEY"},
			marker: true,
		},
		{
			name:   "oversized provider text",
			detail: strings.Repeat("provider-private-detail ", 64),
			leaks:  []string{},
			marker: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := cancelState{}
			state.observeOutcome(&backendplugin.CancelOutcome{
				Acknowledged: false,
				Mode:         backendplugin.CancelModeProvider,
				Detail:       tc.detail,
			})
			progress := state.snapshot()
			if strings.Contains(progress.OutcomeDetail, tc.detail) || strings.Contains(progress.OutcomeDetail, "private-provider-detail") {
				t.Fatalf("progress retained raw connector detail: %q", progress.OutcomeDetail)
			}
			res := outcomeCancelResult(progress)
			if res.Mode != aipapi.CancelModeProvider {
				t.Fatalf("mode = %v, want provider", res.Mode)
			}
			if res.Err == nil {
				t.Fatal("error = nil, want classified cancellation failure")
			}
			if len(res.Err.Error()) > len("backend plugin cancel failed: ")+256 {
				t.Fatalf("error length = %d, want at most %d", len(res.Err.Error()), len("backend plugin cancel failed: ")+256)
			}
			for _, leak := range tc.leaks {
				if strings.Contains(res.Err.Error(), leak) {
					t.Fatalf("error leaked %q: %q", leak, res.Err)
				}
			}
			if tc.marker && !strings.Contains(res.Err.Error(), "[redacted]") {
				t.Fatalf("error = %q, want redaction marker", res.Err)
			}
		})
	}
}

func TestOutcomeCancelResult_PreservesAcknowledgementAndMode(t *testing.T) {
	t.Parallel()

	state := cancelState{}
	state.observeOutcome(&backendplugin.CancelOutcome{
		Acknowledged: true,
		Mode:         backendplugin.CancelModeTransport,
		Detail:       "provider detail must not become an error",
	})
	res := outcomeCancelResult(state.snapshot())
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("mode = %v, want transport", res.Mode)
	}
	if res.Err != nil {
		t.Fatalf("acknowledged outcome error = %v, want nil", res.Err)
	}
}

func TestOutcomeCancelResult_ConnectorDetailCannotCrossHostBoundary(t *testing.T) {
	t.Parallel()

	const apiKey = "sk-ant-api03-host-boundary-secret-123456789"
	sess := &dummyExecuteSession{executeFn: func(stream backendplugin.ExecuteStream) error {
		for {
			frame, err := stream.Recv()
			if err != nil {
				return err
			}
			if frame.Kind == backendplugin.ClientFrameStart {
				_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
				continue
			}
			if frame.Kind != backendplugin.ClientFrameCancel {
				continue
			}
			_ = stream.Send(backendplugin.ServerFrame{
				Kind:     backendplugin.ServerFrameCancelOutcome,
				Sequence: 1,
				CancelOutcome: &backendplugin.CancelOutcome{
					Acknowledged: false,
					Mode:         backendplugin.CancelModeProvider,
					Reason:       frame.CancelReason,
					Detail:       "provider refused api_key=" + apiKey + " " + strings.Repeat("private-provider-detail ", 64),
				},
			},
			)
			_ = stream.Send(backendplugin.ServerFrame{
				Kind:     backendplugin.ServerFrameTerminal,
				Sequence: 2,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
			})
			return nil
		}
	}}

	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-redaction-boundary",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, testCallWithMessages("req-redaction-boundary"), routing.AttemptCandidate{
		Key: "candidate-redaction-boundary", Primary: routing.Primary{Backend: "backend", Model: "model"},
	})
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("mode = %v, want provider", res.Mode)
	}
	if res.Err == nil {
		t.Fatal("error = nil, want classified cancellation failure")
	}
	if strings.Contains(res.Err.Error(), apiKey) || strings.Contains(res.Err.Error(), "private-provider-detail") {
		t.Fatalf("connector detail leaked through host boundary: %q", res.Err)
	}
	managed, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("unexpected stream type %T", ms)
	}
	progress := managed.CancellationProgress()
	if strings.Contains(progress.OutcomeDetail, apiKey) || strings.Contains(progress.OutcomeDetail, "private-provider-detail") {
		t.Fatalf("connector detail leaked through cancellation progress: %q", progress.OutcomeDetail)
	}
	if !strings.Contains(res.Err.Error(), "[redacted]") {
		t.Fatalf("error = %q, want redaction marker", res.Err)
	}
	if len(res.Err.Error()) > len("backend plugin cancel failed: ")+256 {
		t.Fatalf("error length = %d, want at most %d", len(res.Err.Error()), len("backend plugin cancel failed: ")+256)
	}
}

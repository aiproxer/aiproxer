package adapter

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type e2eCancelFailingManaged struct {
	recvEntered atomic.Bool
	unblocked   chan struct{}
	cancelRes   aipapi.CancelResult
	cancelCalls atomic.Int32
	cancelCause aipapi.CancelCause
}

func (m *e2eCancelFailingManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	}
}

func (m *e2eCancelFailingManaged) Close() error { return nil }

func (m *e2eCancelFailingManaged) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalls.Add(1)
	m.cancelCause = cause
	close(m.unblocked)
	return m.cancelRes
}

func testWaitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestRED_Adapter_CancelOutcome_Matrix(t *testing.T) {
	t.Parallel()

	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
		EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
	}
	cand := routing.AttemptCandidate{Key: "cand-matrix", Primary: routing.Primary{Backend: "b1", Model: "m1"}}

	t.Run("1_unspecified_mode_acknowledged_returns_mode_none", func(t *testing.T) {
		t.Parallel()
		sess := &dummyExecuteSession{
			executeFn: func(stream backendplugin.ExecuteStream) error {
				for {
					f, err := stream.Recv()
					if err != nil {
						return err
					}
					if f.Kind == backendplugin.ClientFrameStart {
						_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
					}
					if f.Kind == backendplugin.ClientFrameCancel {
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameCancelOutcome,
							Sequence: 1,
							CancelOutcome: &backendplugin.CancelOutcome{
								Acknowledged: true,
								Mode:         backendplugin.CancelModeUnspecified,
								Reason:       f.CancelReason,
							},
						})
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameTerminal,
							Sequence: 2,
							Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
						})
						return nil
					}
				}
			},
		}

		ms, err := openStream(context.Background(), sess, Options{
			InstanceID:    "inst-m1",
			CancelTimeout: 500 * time.Millisecond,
			Negotiation:   neg,
		}, testCallWithMessages("req-m1"), cand)
		if err != nil {
			t.Fatalf("openStream failed: %v", err)
		}
		defer func() { _ = ms.Close() }()

		res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeNone {
			t.Fatalf("res.Mode = %v, want CancelModeNone (RED: was promoted to Provider)", res.Mode)
		}
		if res.Err != nil {
			t.Fatalf("res.Err = %v, want nil", res.Err)
		}
	})

	t.Run("2_negative_ack_with_detail_returns_error_and_preserves_mode", func(t *testing.T) {
		t.Parallel()
		sess := &dummyExecuteSession{
			executeFn: func(stream backendplugin.ExecuteStream) error {
				for {
					f, err := stream.Recv()
					if err != nil {
						return err
					}
					if f.Kind == backendplugin.ClientFrameStart {
						_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
					}
					if f.Kind == backendplugin.ClientFrameCancel {
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameCancelOutcome,
							Sequence: 1,
							CancelOutcome: &backendplugin.CancelOutcome{
								Acknowledged: false,
								Mode:         backendplugin.CancelModeProvider,
								Reason:       f.CancelReason,
								Detail:       "provider cancellation rejected",
							},
						})
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameTerminal,
							Sequence: 2,
							Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
						})
						return nil
					}
				}
			},
		}

		ms, err := openStream(context.Background(), sess, Options{
			InstanceID:    "inst-m2",
			CancelTimeout: 500 * time.Millisecond,
			Negotiation:   neg,
		}, testCallWithMessages("req-m2"), cand)
		if err != nil {
			t.Fatalf("openStream failed: %v", err)
		}
		defer func() { _ = ms.Close() }()

		res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeProvider {
			t.Fatalf("res.Mode = %v, want CancelModeProvider", res.Mode)
		}
		if res.Err == nil {
			t.Fatal("res.Err is nil, want non-nil classified error")
		}
		if strings.Contains(res.Err.Error(), "provider cancellation rejected") || !strings.Contains(res.Err.Error(), "[redacted]") {
			t.Fatalf("res.Err = %q, want classified redacted failure", res.Err.Error())
		}
	})

	t.Run("3_graceful_done_without_outcome_returns_mode_none", func(t *testing.T) {
		t.Parallel()
		sess := &dummyExecuteSession{
			executeFn: func(stream backendplugin.ExecuteStream) error {
				for {
					f, err := stream.Recv()
					if err != nil {
						return err
					}
					if f.Kind == backendplugin.ClientFrameStart {
						_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
					}
					if f.Kind == backendplugin.ClientFrameCancel {
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameTerminal,
							Sequence: 1,
							Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
						})
						return nil
					}
				}
			},
		}

		ms, err := openStream(context.Background(), sess, Options{
			InstanceID:    "inst-m3",
			CancelTimeout: 500 * time.Millisecond,
			Negotiation:   neg,
		}, testCallWithMessages("req-m3"), cand)
		if err != nil {
			t.Fatalf("openStream failed: %v", err)
		}
		defer func() { _ = ms.Close() }()

		res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeNone {
			t.Fatalf("res.Mode = %v, want CancelModeNone (RED: returned Transport)", res.Mode)
		}
		if res.Err != nil {
			t.Fatalf("res.Err = %v, want nil", res.Err)
		}
	})

	t.Run("4_forced_abort_deadline_expiry_returns_mode_transport", func(t *testing.T) {
		t.Parallel()
		sess := &dummyExecuteSession{
			executeFn: func(stream backendplugin.ExecuteStream) error {
				<-stream.Context().Done()
				return stream.Context().Err()
			},
		}

		ms, err := openStream(context.Background(), sess, Options{
			InstanceID:    "inst-m4",
			CancelTimeout: 50 * time.Millisecond,
			Negotiation:   neg,
		}, testCallWithMessages("req-m4"), cand)
		if err != nil {
			t.Fatalf("openStream failed: %v", err)
		}
		defer func() { _ = ms.Close() }()

		res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeTransport {
			t.Fatalf("res.Mode = %v, want CancelModeTransport", res.Mode)
		}
		if !errors.Is(res.Err, context.DeadlineExceeded) {
			t.Fatalf("res.Err = %v, want context.DeadlineExceeded", res.Err)
		}
	})

	t.Run("5_early_return_terminal_seen_negative_ack_returns_error", func(t *testing.T) {
		t.Parallel()
		sess := &dummyExecuteSession{
			executeFn: func(stream backendplugin.ExecuteStream) error {
				for {
					f, err := stream.Recv()
					if err != nil {
						return err
					}
					if f.Kind == backendplugin.ClientFrameStart {
						_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
					}
					if f.Kind == backendplugin.ClientFrameCancel {
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameCancelOutcome,
							Sequence: 1,
							CancelOutcome: &backendplugin.CancelOutcome{
								Acknowledged: false,
								Mode:         backendplugin.CancelModeProvider,
								Reason:       f.CancelReason,
								Detail:       "late failure detail",
							},
						})
						_ = stream.Send(backendplugin.ServerFrame{
							Kind:     backendplugin.ServerFrameTerminal,
							Sequence: 2,
							Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
						})
						return nil
					}
				}
			},
		}

		ms, err := openStream(context.Background(), sess, Options{
			InstanceID:    "inst-m5",
			CancelTimeout: 500 * time.Millisecond,
			Negotiation:   neg,
		}, testCallWithMessages("req-m5"), cand)
		if err != nil {
			t.Fatalf("openStream failed: %v", err)
		}
		defer func() { _ = ms.Close() }()

		res1 := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		_ = res1

		managed, ok := ms.(*managedStream)
		if !ok {
			t.Fatalf("unexpected stream type %T", ms)
		}
		if !managed.terminalSeen.Load() {
			t.Fatal("expected terminalSeen to be true")
		}

		res2 := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res2.Mode != aipapi.CancelModeProvider {
			t.Fatalf("res2.Mode = %v, want CancelModeProvider", res2.Mode)
		}
		if res2.Err == nil {
			t.Fatal("res2.Err is nil on terminalSeen negative-ack, want non-nil classified error")
		}
		if strings.Contains(res2.Err.Error(), "late failure detail") || !strings.Contains(res2.Err.Error(), "[redacted]") {
			t.Fatalf("res2.Err = %q, want classified redacted failure", res2.Err.Error())
		}
	})
}

func TestRED_Adapter_E2E_ForwardExecute_CancelFailure_PropagatesErrorAndMode(t *testing.T) {
	t.Parallel()

	sentinelErr := errors.New("upstream-provider-abort-failed")
	pluginManaged := &e2eCancelFailingManaged{
		unblocked: make(chan struct{}),
		cancelRes: aipapi.CancelResult{
			Mode: aipapi.CancelModeProvider,
			Err:  sentinelErr,
		},
	}

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
				return pluginManaged, nil
			})
		},
	}

	call := testCallWithMessages("req-e2e")
	cand := routing.AttemptCandidate{Key: "cand-e2e", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-e2e",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	testWaitUntil(t, 2*time.Second, func() bool { return pluginManaged.recvEntered.Load() })

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("res.Mode = %v, want CancelModeProvider", res.Mode)
	}
	if res.Err == nil {
		t.Fatal("res.Err is nil, want non-nil classified error across host-plugin boundary")
	}
	if strings.Contains(res.Err.Error(), "upstream-provider-abort-failed") || !strings.Contains(res.Err.Error(), "[redacted]") {
		t.Fatalf("res.Err = %q, want classified redacted failure", res.Err.Error())
	}
}

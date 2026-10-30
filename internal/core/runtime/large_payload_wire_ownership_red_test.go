package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// blockingTrackingStream blocks Recv until unblockRecv is called,
// and records when Cancel and Close are invoked.
type blockingTrackingStream struct {
	cancelCh    chan struct{}
	closeCh     chan struct{}
	blockCh     chan struct{}
	cancelOnce  sync.Once
	closeOnce   sync.Once
	unblockOnce sync.Once
}

func newBlockingTrackingStream() *blockingTrackingStream {
	return &blockingTrackingStream{
		cancelCh: make(chan struct{}, 1),
		closeCh:  make(chan struct{}, 1),
		blockCh:  make(chan struct{}),
	}
}

func (s *blockingTrackingStream) unblockRecv() {
	s.unblockOnce.Do(func() {
		close(s.blockCh)
	})
}

func (s *blockingTrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-s.blockCh:
		return aipapi.Event{}, io.EOF
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *blockingTrackingStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	return nil
}

func (s *blockingTrackingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelOnce.Do(func() {
		close(s.cancelCh)
	})
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

var _ aipapi.ManagedEventStream = (*blockingTrackingStream)(nil)

// TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome
// verifies Blocker 1 regression requirements:
// 1. Opens a blocking wire stream and returns it from ExecuteLargeBody.
// 2. Invokes real Executor.CancelALeg.
// 3. Asserts the backend stream's Cancel/Close is invoked promptly (compute not leaked).
// 4. Asserts exactly one terminal outcome is produced with LegOutcomeCanceled (no stale loser defaults, no duplicate legs).
func TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []usageevidence.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	backendStream := newBlockingTrackingStream()
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-cancel"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-cancel"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-cancel", "trace-wire-cancel")

	// 1. ExecuteLargeBody returns the open wire stream
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Concurrently read the stream to verify it's active
	recvErrCh := make(chan error, 1)
	go func() {
		_, rErr := res.Stream.Recv(context.Background())
		recvErrCh <- rErr
	}()

	// 2. Invoke real Executor.CancelALeg
	cancelCtx := execview.WithPrincipal(context.Background(), principal)
	err = ex.CancelALeg(cancelCtx, aipapi.ALegCancelRequest{
		ALegID: res.Facts.ALegID,
		Reason: "user cancelled request",
	})
	require.NoError(t, err)

	// 3. Assert the backend stream's Cancel/Close is invoked promptly
	select {
	case <-backendStream.cancelCh:
		// Promptly canceled via Cancel
	case <-backendStream.closeCh:
		// Promptly closed
	case <-time.After(2 * time.Second):
		t.Fatal("backend stream Cancel/Close was not invoked promptly after Executor.CancelALeg")
	}

	// Unblock backend Recv only after CancelALeg has completed, so the cancel path
	// deterministically claims the terminal outcome first by construction.
	backendStream.unblockRecv()

	// Recv should unblock promptly
	select {
	case rErr := <-recvErrCh:
		_ = rErr
	case <-time.After(2 * time.Second):
		t.Fatal("wire stream Recv did not unblock after cancellation")
	}

	// Close the wire stream (simulating wire frontend completion / teardown)
	_ = res.Stream.Close()

	// 4. Assert exactly one terminal outcome is produced
	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome to be produced")
	assert.Equal(t, usageevidence.LegOutcomeCanceled, recordedLegs[0].Outcome, "terminal outcome must be LegOutcomeCanceled, not stale loser-default")
}

func TestBlocker1_WireOwnershipTransfer_NormalEOF_ProducesSingleWinnerTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []usageevidence.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "hello"},
						{Kind: aipapi.EventResponseFinished},
					}),
				}, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-normal"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-normal"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-normal", "trace-wire-normal")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Consume all events to EOF
	for {
		_, err := res.Stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	_ = res.Stream.Close()

	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome on normal completion")
	assert.Equal(t, usageevidence.LegOutcomeWinner, recordedLegs[0].Outcome, "normal completion must produce LegOutcomeWinner")
}

func TestBlocker1_WireOwnershipTransfer_EarlyClose_ProducesSingleCanceledTerminalOutcome(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)

	var recordedLegs []usageevidence.CallLegUsageRecord
	var legsMu sync.Mutex
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
			legsMu.Lock()
			recordedLegs = append(recordedLegs, record)
			legsMu.Unlock()
			return nil
		},
	}

	backendStream := newBlockingTrackingStream()
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"wire-early-close"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	principal := execview.PrincipalView{ID: "usr-wire-early-close"}
	ctx := execview.WithPrincipal(context.Background(), principal)
	ctx = largebody.WithWireIdentity(ctx, "req-wire-early-close", "trace-wire-early-close")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	require.NotNil(t, res.Stream)

	// Close before consuming to EOF (early close)
	_ = res.Stream.Close()

	// Backend stream must be closed
	select {
	case <-backendStream.closeCh:
		// closed
	case <-time.After(2 * time.Second):
		t.Fatal("backend stream was not closed after wire stream Close")
	}

	legsMu.Lock()
	defer legsMu.Unlock()
	require.Len(t, recordedLegs, 1, "expected exactly one terminal leg outcome on early close")
	assert.Equal(t, usageevidence.LegOutcomeCanceled, recordedLegs[0].Outcome, "early close must produce LegOutcomeCanceled")
}

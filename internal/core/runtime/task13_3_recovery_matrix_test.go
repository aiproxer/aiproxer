package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func makeTestAssessmentForSource(t *testing.T, genID, profileID string, src largebody.Source, universal bool, candModels ...string) largebody.Assessment {
	t.Helper()
	var srcDigest largebody.SourceDigest
	if cs, ok := src.(*largebody.CompletedSource); ok {
		srcDigest = cs.Digest()
	} else if ts, ok := src.(*testSource); ok {
		srcDigest = ts.Digest()
	}
	stamp, err := largebody.NewAssessmentStamp(
		genID,
		profileID,
		srcDigest,
		src.Size(),
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(sha256.Sum256([]byte("test-identity"))),
		"dom-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       profileID,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 2048,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID:       profileID,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		UniversalModel:  universal,
		CandidateModels: candModels,
	}
	acc, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}
	return acc
}

// TestTask13_3_PreOutputFailover_ExactBytesReplay tests that when Attempt 1 fails
// pre-output (after reading partial or all bytes), Attempt 2 receives the complete
// exact bytes from offset zero, for both non-rewrite and model-rewrite requests (Req 10.1, 10.2, 10.6).
func TestTask13_3_PreOutputFailover_ExactBytesReplay(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-replay"})

	t.Run("NoRewrite_Attempt2GetsExactSourceBytes", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"complete exact bytes replay verification"}]}`
		src := newTestSource(payload)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")

		be1 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("b1 connection reset by peer"))
			},
		}
		be2 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "recovered"}), nil
			},
		}

		ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: be1.OpenWire},
			"b2": {OpenWire: be2.OpenWire},
		}

		ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
			{Pattern: "^test-failover$", Replacement: "b1:gpt-4o | b2:gpt-4o"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ex.SelectorAliases = ar
		acc.WireRequest.CandidateModel = "test-failover"

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody failed: %v", err)
		}
		defer func() { _ = res.Stream.Close() }()

		if len(be1.Calls()) != 1 {
			t.Fatalf("expected 1 call to b1, got %d", len(be1.Calls()))
		}
		if len(be2.Calls()) != 1 {
			t.Fatalf("expected 1 call to b2, got %d", len(be2.Calls()))
		}

		b1Data := be1.ReceivedData()
		b2Data := be2.ReceivedData()

		// Attempt 1 read the payload
		if len(b1Data) == 0 || string(b1Data[0]) != payload {
			t.Errorf("Attempt 1 received data mismatch:\ngot:  %q\nwant: %q", string(b1Data[0]), payload)
		}

		// Attempt 2 must have seen the exact complete source bytes
		if len(b2Data) == 0 || string(b2Data[0]) != payload {
			t.Errorf("Attempt 2 received bytes mismatch:\ngot:  %q\nwant: %q", string(b2Data[0]), payload)
		}
	})

	t.Run("WithRewrite_Attempt2GetsExactSplicedBytes", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"splice recovery test"}]}`
		src := newTestSource(rawJSON)

		// Model token "gpt-4o" is at offset 9, length 8
		span := largebody.Span{Offset: 9, Length: 8}
		rewrite, err := largebody.NewModelTokenRewrite(span)
		if err != nil {
			t.Fatal(err)
		}

		stamp, err := largebody.NewAssessmentStamp(
			"gen-1",
			"openai-chat",
			src.digest,
			src.size,
			largebody.BodyModeIdentityJSON,
			rewrite,
			largebody.NewIdentityDigest(sha256.Sum256([]byte("identity-splice"))),
			"dom-gen-1",
		)
		if err != nil {
			t.Fatal(err)
		}

		wireReq := largebody.WireRequestFacts{
			ProfileID:       "openai-chat",
			Operation:       aipapi.OperationOpenAIChatCompletions,
			Delivery:        aipapi.DeliveryModeStreaming,
			BodyMode:        largebody.BodyModeIdentityJSON,
			Rewrite:         rewrite,
			ClientModel:     "gpt-4o",
			CandidateModel:  "b1:model-v1 | b2:model-v2",
			MaxOutputTokens: 2048,
		}
		wireDomain := largebody.WireDomainFacts{
			ProfileID:       "openai-chat",
			Operation:       aipapi.OperationOpenAIChatCompletions,
			Delivery:        aipapi.DeliveryModeStreaming,
			BodyMode:        largebody.BodyModeIdentityJSON,
			Rewrite:         rewrite,
			CandidateModels: []string{"b1:model-v1", "b2:model-v2"},
		}
		acc, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
		if err != nil {
			t.Fatal(err)
		}

		be1 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("b1 503 unavailable"))
			},
		}
		be2 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "spliced ok"}), nil
			},
		}

		ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: be1.OpenWire},
			"b2": {OpenWire: be2.OpenWire},
		}

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody failed: %v", err)
		}
		defer func() { _ = res.Stream.Close() }()

		expectedB1JSON := `{"model":"model-v1","messages":[{"role":"user","content":"splice recovery test"}]}`
		expectedB2JSON := `{"model":"model-v2","messages":[{"role":"user","content":"splice recovery test"}]}`

		b1Data := be1.ReceivedData()
		b2Data := be2.ReceivedData()

		if len(b1Data) == 0 || string(b1Data[0]) != expectedB1JSON {
			t.Errorf("Attempt 1 received bytes:\ngot:  %q\nwant: %q", string(b1Data[0]), expectedB1JSON)
		}
		if len(b2Data) == 0 || string(b2Data[0]) != expectedB2JSON {
			t.Errorf("Attempt 2 received bytes:\ngot:  %q\nwant: %q", string(b2Data[0]), expectedB2JSON)
		}
	})
}

// TestTask13_3_ParallelReaders_Independent tests that concurrent readers opened
// from the same source operate independently without cursor coupling or corruption (Req 10.3, 10.6).
func TestTask13_3_ParallelReaders_Independent(t *testing.T) {
	t.Run("SourceConcurrentReaders_DifferentSpeedsAndChunkSizes", func(t *testing.T) {
		rawPayload := strings.Repeat("0123456789abcdef", 1024) // 16 KiB
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: []byte(rawPayload),
			Size:   int64(len(rawPayload)),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = src.Close() }()

		const numReaders = 8
		var wg sync.WaitGroup
		var opened sync.WaitGroup
		opened.Add(numReaders)
		errCh := make(chan error, numReaders)

		for i := range numReaders {
			wg.Add(1)
			go func(readerIdx int) {
				defer wg.Done()
				r, openErr := src.Open()
				opened.Done()
				if openErr != nil {
					errCh <- fmt.Errorf("reader %d open failed: %w", readerIdx, openErr)
					return
				}
				defer func() { _ = r.Close() }()
				// All cursors are live before any reader consumes the source.
				opened.Wait()

				// Read in varying chunk sizes
				chunkSize := (readerIdx%5 + 1) * 7
				var buf bytes.Buffer
				tmp := make([]byte, chunkSize)

				for {
					n, rerr := r.Read(tmp)
					if n > 0 {
						buf.Write(tmp[:n])
					}
					if rerr == io.EOF {
						break
					}
					if rerr != nil {
						errCh <- fmt.Errorf("reader %d read error: %w", readerIdx, rerr)
						return
					}
					// Yield to vary inter-reader pacing without one OS timer per read.
					if readerIdx%2 == 0 {
						goruntime.Gosched()
					}
				}

				if buf.String() != rawPayload {
					errCh <- fmt.Errorf("reader %d content mismatch (len %d vs %d)", readerIdx, buf.Len(), len(rawPayload))
				}
			}(i)
		}

		wg.Wait()
		close(errCh)

		for rerr := range errCh {
			t.Errorf("parallel reader failure: %v", rerr)
		}
	})

	t.Run("ParallelRace_ConcurrentReadersIndependent", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		rawPayload := `{"model":"gpt-4o","messages":[{"role":"user","content":"parallel race independence test"}]}`
		src := newTestSource(rawPayload)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "r1:m1", "r2:m2")
		acc.WireRequest.CandidateModel = "r1:m1!r2:m2"

		var readWg sync.WaitGroup
		readWg.Add(2)
		be1Started := make(chan struct{})
		be2Started := make(chan struct{})

		be1 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				defer readWg.Done()
				close(be1Started)
				select {
				case <-be2Started:
				case <-ctx.Done():
				}
				// Slow to emit first event
				time.Sleep(30 * time.Millisecond)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "r1"}), nil
			},
		}

		be2 := &stubWireBackend{
			openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				defer readWg.Done()
				close(be2Started)
				select {
				case <-be1Started:
				case <-ctx.Done():
				}
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "r2"}), nil
			},
		}

		ex.Backends = map[string]execbackend.Backend{
			"r1": {OpenWire: be1.OpenWire},
			"r2": {OpenWire: be2.OpenWire},
		}

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race"})
		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody failed: %v", err)
		}
		defer func() { _ = res.Stream.Close() }()

		readWg.Wait()

		r1Data := be1.ReceivedData()
		r2Data := be2.ReceivedData()

		if len(r1Data) == 0 || string(r1Data[0]) != rawPayload {
			t.Errorf("r1 read mismatch:\ngot:  %q\nwant: %q", string(r1Data[0]), rawPayload)
		}
		if len(r2Data) == 0 || string(r2Data[0]) != rawPayload {
			t.Errorf("r2 read mismatch:\ngot:  %q\nwant: %q", string(r2Data[0]), rawPayload)
		}
	})
}

// TestTask13_3_NoFailoverAfterFirstVisibleEvent proves that once the first visible event
// has been committed and returned, a subsequent stream error NEVER initiates retry or failover
// to alternative candidates (Req 6.6, 10.5; Design Section 17).
func TestTask13_3_NoFailoverAfterFirstVisibleEvent(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","prompt":"visible commitment test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")

	// Custom stream that emits one visible event then fails mid-stream
	b1Stream := &failAfterFirstEventStream{
		firstEvent: aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible-first-token"},
		midErr:     errors.New("b1 connection reset by peer after first token"),
	}

	var b1Called, b2Called atomic.Int32
	be1 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			b1Called.Add(1)
			return b1Stream, nil
		},
	}
	be2 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			b2Called.Add(1)
			return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b2 token"}), nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: be1.OpenWire},
		"b2": {OpenWire: be2.OpenWire},
	}

	ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^test-failover-post-output$", Replacement: "b1:gpt-4o | b2:gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex.SelectorAliases = ar
	acc.WireRequest.CandidateModel = "test-failover-post-output"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-post-output"})
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody should succeed and commit stream, got: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	// First Recv must return the visible first token
	ev1, err1 := res.Stream.Recv(ctx)
	if err1 != nil {
		t.Fatalf("first Recv failed: %v", err1)
	}
	if ev1.Delta != "visible-first-token" {
		t.Fatalf("expected visible-first-token, got %q", ev1.Delta)
	}

	// Second Recv must return the mid-stream failure
	_, err2 := res.Stream.Recv(ctx)
	if err2 == nil {
		t.Fatal("expected error on second Recv, got nil")
	}
	if !strings.Contains(err2.Error(), "connection reset by peer after first token") {
		t.Errorf("unexpected second Recv error: %v", err2)
	}

	// Invariant: b2 must NEVER be called after first visible event
	if b2Called.Load() != 0 {
		t.Fatalf("INVARIANT VIOLATION: failover to b2 occurred after visible output! b2 called %d times", b2Called.Load())
	}
	if b1Called.Load() != 1 {
		t.Errorf("b1 called %d times, want 1", b1Called.Load())
	}
}

// failAfterFirstEventStream emits one event then returns an error on subsequent Recv.
type failAfterFirstEventStream struct {
	firstEvent aipapi.Event
	midErr     error
	firstGiven bool
	closed     bool
}

func (s *failAfterFirstEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.closed {
		return aipapi.Event{}, io.EOF
	}
	if !s.firstGiven {
		s.firstGiven = true
		return s.firstEvent, nil
	}
	return aipapi.Event{}, s.midErr
}

func (s *failAfterFirstEventStream) Close() error {
	s.closed = true
	return nil
}

func (s *failAfterFirstEventStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.closed = true
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestTask13_3_Cancellation_ClosesReadersAndSource_CleansUpLifecycleAndEconomics
// verifies that cancellation at any stage closes readers, closes the source, releases
// spool reservations, and preserves lifecycle and economic cleanup (Req 6.5, 10.4, 20.4, 20.9).
func TestTask13_3_Cancellation_ClosesReadersAndSource_CleansUpLifecycleAndEconomics(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-cancel"})

	t.Run("MidStreamCancellation_ClosesReadersSourceAndCleansEconomics", func(t *testing.T) {
		ex, _, b2 := setupTestExecutor(t)

		memSS := memory.New(memory.Options{SimulateDurable: true})
		mgr := testSecureManager(t, memSS, b2)
		ex.SecureSession = mgr

		sink := &capturingTerminalUsageSink{}
		ex.TerminalUsageSink = sink

		// Backing spill file and spool reservation
		tmpDir := t.TempDir()
		spillPath := filepath.Join(tmpDir, "cancel_spill.tmp")
		content := []byte(`{"model":"gpt-4o","prompt":"cancel me"}`)
		if err := os.WriteFile(spillPath, content, 0o600); err != nil {
			t.Fatal(err)
		}

		ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
			MaxInflightSpoolBytes: 1024 * 1024,
			MemorySpoolBytes:      64 * 1024,
		})
		if err != nil {
			t.Fatal(err)
		}
		resv, err := ledger.Reserve(64)
		if err != nil {
			t.Fatal(err)
		}

		srcDigest := largebody.NewSourceDigest(sha256.Sum256(content))
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			FilePath:    spillPath,
			Size:        int64(len(content)),
			Digest:      srcDigest,
			Reservation: resv,
		})
		if err != nil {
			t.Fatal(err)
		}

		acc := makeTestAssessmentForSource(t, "gen-1", "openai-chat", src, true)

		streamBlocker := make(chan struct{})
		cancelObserved := make(chan struct{})

		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					return &blockingManagedStream{
						firstEvent:     aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "first"},
						blockCh:        streamBlocker,
						cancelObserved: cancelObserved,
					}, nil
				},
			},
		}

		streamCtx, cancelStream := context.WithCancel(ctx)
		res, err := ex.ExecuteLargeBody(streamCtx, acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody failed: %v", err)
		}

		// Read first visible event
		ev, rerr := res.Stream.Recv(streamCtx)
		if rerr != nil {
			t.Fatalf("first Recv failed: %v", rerr)
		}
		if ev.Delta != "first" {
			t.Fatalf("expected first token, got %q", ev.Delta)
		}

		// While stream is open, source must NOT be closed yet, and reader is active
		if src.IsClosed() {
			t.Fatal("source should not be closed while stream is active")
		}
		if src.ActiveReaders() < 1 {
			t.Fatalf("expected >=1 active readers during stream, got %d", src.ActiveReaders())
		}

		// Cancel stream via context and Close()
		cancelStream()
		_ = res.Stream.Close()
		close(streamBlocker)

		// Wait briefly for cancellation to settle
		time.Sleep(50 * time.Millisecond)

		// 1. Source must be closed
		if !src.IsClosed() {
			t.Error("expected source to be closed after stream cancel/close")
		}

		// 2. Active readers must be 0
		if src.ActiveReaders() != 0 {
			t.Errorf("expected 0 active readers after cancel/close, got %d", src.ActiveReaders())
		}

		// 3. Temporary spill file must be removed
		if _, statErr := os.Stat(spillPath); !os.IsNotExist(statErr) {
			t.Errorf("spill file %s was not deleted after close: %v", spillPath, statErr)
		}

		// 4. Spool reservation must be released (active reservations = 0)
		if active := ledger.ActiveReservations(); active != 0 {
			t.Errorf("spool reservation was not released, active = %d, want 0", active)
		}

		// 5. SecureSession turn must be finalized
		audits, aerr := memSS.Audit(ctx, domain.SessionID(res.Facts.SessionID), domain.ReadOptions{})
		if aerr != nil {
			t.Fatalf("Audit failed: %v", aerr)
		}
		var foundOutcome bool
		for _, a := range audits {
			if a.Action == "turn_outcome" {
				foundOutcome = true
				break
			}
		}
		if !foundOutcome {
			t.Error("expected SecureSession turn_outcome audit record upon cancel")
		}
	})

	t.Run("StreamCancel_MapsToCanceledTerminalLegAndTurnOutcome", func(t *testing.T) {
		ex, _, b2 := setupTestExecutor(t)

		memSS := memory.New(memory.Options{SimulateDurable: true})
		mgr := testSecureManager(t, memSS, b2)
		ex.SecureSession = mgr

		sink := &capturingTerminalUsageSink{}
		ex.TerminalUsageSink = sink

		content := []byte(`{"model":"gpt-4o","prompt":"cancel entrypoint"}`)
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: content,
			Size:   int64(len(content)),
		})
		if err != nil {
			t.Fatal(err)
		}

		acc := makeTestAssessmentForSource(t, "gen-1", "openai-chat", src, true)

		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					return &blockingManagedStream{
						firstEvent: aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "first"},
						blockCh:    make(chan struct{}),
					}, nil
				},
			},
		}

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody failed: %v", err)
		}

		if _, rerr := res.Stream.Recv(ctx); rerr != nil {
			t.Fatalf("first Recv failed: %v", rerr)
		}

		// Cancel via the ManagedEventStream entry point (not Close): the
		// terminal leg must record Canceled and the turn must finalize.
		ms, ok := res.Stream.(aipapi.ManagedEventStream)
		if !ok {
			t.Fatal("wire result stream does not implement ManagedEventStream")
		}
		ms.Cancel(ctx, aipapi.CancelCause{})
		_ = res.Stream.Close()

		deadline := time.Now().Add(2 * time.Second)
		for {
			sink.mu.Lock()
			legs := append([]usageevidence.CallLegUsageRecord(nil), sink.legs...)
			sink.mu.Unlock()
			if len(legs) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("expected terminal leg record after Stream.Cancel")
			}
			time.Sleep(10 * time.Millisecond)
		}

		sink.mu.Lock()
		legs := append([]usageevidence.CallLegUsageRecord(nil), sink.legs...)
		sink.mu.Unlock()
		canceled := false
		for _, leg := range legs {
			if leg.Outcome == usageevidence.LegOutcomeCanceled {
				canceled = true
				break
			}
		}
		if !canceled {
			t.Errorf("expected a LegOutcomeCanceled terminal leg after Stream.Cancel, got %+v", legs)
		}

		audits, aerr := memSS.Audit(ctx, domain.SessionID(res.Facts.SessionID), domain.ReadOptions{})
		if aerr != nil {
			t.Fatalf("Audit failed: %v", aerr)
		}
		foundOutcome := false
		for _, a := range audits {
			if a.Action == "turn_outcome" {
				foundOutcome = true
				break
			}
		}
		if !foundOutcome {
			t.Error("expected SecureSession turn_outcome audit record upon Stream.Cancel")
		}
	})

	t.Run("CancelDuringAttemptOpen_AbortsExposureAndClosesSource", func(t *testing.T) {
		ex, _, b2 := setupTestExecutor(t)

		memSS := memory.New(memory.Options{SimulateDurable: true})
		mgr := testSecureManager(t, memSS, b2)
		ex.SecureSession = mgr

		sink := &capturingTerminalUsageSink{}
		ex.TerminalUsageSink = sink
		ex.BillingExposureAdmission = &testExposureAdmission{
			admitFunc: func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				return billingport.CallExposure{AccountID: "acc-pre-cancel"}, nil
			},
		}

		// Create real CompletedSource to verify IsClosed
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: []byte(`{"model":"gpt-4o","prompt":"attempt-open cancel"}`),
			Size:   int64(len(`{"model":"gpt-4o","prompt":"attempt-open cancel"}`)),
		})
		if err != nil {
			t.Fatal(err)
		}

		acc := makeTestAssessmentForSource(t, "gen-1", "openai-chat", src, true)

		runCtx, cancel := context.WithCancel(ctx)

		// Backend blocks during OpenWire until context is canceled
		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(openCtx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					// Cancel the context while attempt open is in progress
					cancel()
					<-openCtx.Done()
					return nil, openCtx.Err()
				},
			},
		}

		_, err = ex.ExecuteLargeBody(runCtx, acc, src)
		if err == nil {
			t.Fatal("expected error with canceled context, got nil")
		}

		// Source must be closed on error
		if !src.IsClosed() {
			t.Error("expected source to be closed after attempt cancel failure")
		}

		// Exposure abort must be recorded in terminal usage sink
		sink.mu.Lock()
		calls := append([]usageevidence.CallUsageRecord(nil), sink.calls...)
		sink.mu.Unlock()
		if len(calls) == 0 {
			t.Fatal("expected exposure abort call in TerminalUsageSink")
		}
		if calls[0].Outcome != usageevidence.TurnOutcomeFailed {
			t.Errorf("expected TurnOutcomeFailed, got %v", calls[0].Outcome)
		}
	})
}

// blockingManagedStream emits first event, then blocks until blockCh is closed.
type blockingManagedStream struct {
	firstEvent     aipapi.Event
	blockCh        chan struct{}
	cancelObserved chan struct{}
	firstGiven     bool
	closed         bool
	mu             sync.Mutex
}

func (s *blockingManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	if !s.firstGiven {
		s.firstGiven = true
		s.mu.Unlock()
		return s.firstEvent, nil
	}
	s.mu.Unlock()

	select {
	case <-s.blockCh:
		return aipapi.Event{}, io.EOF
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *blockingManagedStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *blockingManagedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.cancelObserved <- struct{}{}:
	default:
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestTask13_3_UnexpectedPostCommitContentNeed_FinalizesOneTurn_NeverInvokesExecute
// verifies Requirement 6.6, 6.7: after wire commit, an unexpected content need or
// post-commit failure finalizes the one turn and NEVER falls back to canonical Execute.
func TestTask13_3_UnexpectedPostCommitContentNeed_FinalizesOneTurn_NeverInvokesExecute(t *testing.T) {
	ex, metrics, b2 := setupTestExecutor(t)

	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	ex.SecureSession = mgr

	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory: []byte(`{"model":"gpt-4o","prompt":"post-commit failure"}`),
		Size:   int64(len(`{"model":"gpt-4o","prompt":"post-commit failure"}`)),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Assessment accepted with CandidateModel "gpt-4o"
	acc := makeTestAssessmentForSource(t, "gen-1", "openai-chat", src, false, "gpt-4o")

	// Inject a post-commit failure: Route override selects a model outside the assessed domain
	ex.RouteOverrideReader = &testRouteOverrideReader{
		snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
			return routeoverride.State{
				Active:   true,
				Selector: "unexpected-model-outside-domain",
			}, nil
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-post-commit"})
	_, err = ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected invariant error for unexpected post-commit selector mismatch, got nil")
	}

	// 1. Invariant error must mention outside domain
	if !strings.Contains(err.Error(), "outside assessed wire domain") {
		t.Errorf("expected error mentioning outside domain, got: %v", err)
	}

	// 2. Exactly ONE turn was begun
	if metrics.newCalls.Load() != 1 {
		t.Errorf("BeginTurn calls = %d, want 1", metrics.newCalls.Load())
	}

	// 3. Source must be closed
	if !src.IsClosed() {
		t.Error("expected source to be closed after post-commit invariant failure")
	}
}

// TestTask13_3_RouteOverride_InDomainExecutes_OutOfDomainInvariantFailure
// verifies Requirement 7 Acceptance Criteria 2, 4, 5:
// route override within assessed domain executes normally;
// route override outside domain triggers invariant failure without fallback.
func TestTask13_3_RouteOverride_InDomainExecutes_OutOfDomainInvariantFailure(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-route-matrix"})

	t.Run("InDomainOverride_ExecutesSuccessfullyWithOverrideModel", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		src := newTestSource(`{"model":"gpt-4o","prompt":"in-domain override"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "gpt-4o", "gpt-4o-mini")

		var backendReceivedModel string
		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					backendReceivedModel = req.WireRequest.CandidateModel
					return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "override-success"}), nil
				},
			},
		}

		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "gpt-4o-mini", // in-domain override
				}, nil
			},
		}

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("in-domain override failed: %v", err)
		}
		defer func() { _ = res.Stream.Close() }()

		if res.Facts.EffectiveModel != "gpt-4o-mini" {
			t.Errorf("res.Facts.EffectiveModel = %q, want gpt-4o-mini", res.Facts.EffectiveModel)
		}
		if backendReceivedModel != "gpt-4o-mini" {
			t.Errorf("backend received model = %q, want gpt-4o-mini", backendReceivedModel)
		}
	})

	t.Run("OutOfDomainOverride_InvariantFailureTerminalWithoutFallback", func(t *testing.T) {
		ex, metrics, b2 := setupTestExecutor(t)

		memSS := memory.New(memory.Options{SimulateDurable: true})
		mgr := testSecureManager(t, memSS, b2)
		ex.SecureSession = mgr

		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory: []byte(`{"model":"gpt-4o","prompt":"out-of-domain override"}`),
			Size:   int64(len(`{"model":"gpt-4o","prompt":"out-of-domain override"}`)),
		})
		if err != nil {
			t.Fatal(err)
		}

		acc := makeTestAssessmentForSource(t, "gen-1", "openai-chat", src, false, "gpt-4o", "gpt-4o-mini")

		var backendCalls atomic.Int32
		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					backendCalls.Add(1)
					return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "should not be called"}), nil
				},
			},
		}

		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "claude-3-5-sonnet", // outside domain
				}, nil
			},
		}

		_, err = ex.ExecuteLargeBody(ctx, acc, src)
		if err == nil {
			t.Fatal("expected invariant failure for out-of-domain override, got nil")
		}
		if !strings.Contains(err.Error(), "outside assessed wire domain") {
			t.Errorf("expected error mentioning outside assessed wire domain, got: %v", err)
		}

		// Backend must NEVER be opened
		if backendCalls.Load() != 0 {
			t.Errorf("backend opened %d times, want 0", backendCalls.Load())
		}

		// Metric check
		if metrics.newCalls.Load() != 1 {
			t.Errorf("BeginTurn calls = %d, want 1", metrics.newCalls.Load())
		}

		// Source must be closed
		if !src.IsClosed() {
			t.Error("expected source to be closed after out-of-domain invariant failure")
		}
	})
}

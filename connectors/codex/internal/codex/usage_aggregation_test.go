package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestUsageEvidence_normalizesProviderAuthorityAndRejectsEmptyUsage(t *testing.T) {
	t.Parallel()
	if got := usageEvidence(&completedUsage{}); got != nil {
		t.Fatalf("empty provider usage = %#v, want nil", got)
	}
	input := int64(7)
	got := usageEvidence(&completedUsage{InputTokens: &input})
	if got == nil || !got.UsagePresence.InputTokens || got.Source != aipapi.UsageSourceProviderReported || got.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provider evidence = %#v", got)
	}
}

func TestCompletedResponseUsageRejectsNegativeCounters(t *testing.T) {
	t.Parallel()
	negative := int64(-1)
	event := (completedResponse{Usage: &completedUsage{InputTokens: &negative}}).usageEvent()
	if event != nil {
		t.Fatalf("negative request usage became provider evidence: %+v", event)
	}
}

func TestAccountingEvidence_preservesPresenceAndProviderMetadata(t *testing.T) {
	t.Parallel()
	usage := &NativeUsageEvidence{
		InputTokens: 41, OutputTokens: 5, TotalTokens: 46,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Source:        aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		DedupeKey: "codex-compaction:turn-1",
	}
	evidence := accountingEvidence(usage)
	if err := validateNativeUsageEvidenceForTest(evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.DedupeKey != usage.DedupeKey || evidence.InputTokens == nil || *evidence.InputTokens != 41 {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestNativeUsageSidebandStream_drainsEvidenceOnce(t *testing.T) {
	t.Parallel()
	input := int64(13)
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: input, UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}, io.EOF)
	sideband, ok := stream.(*nativeUsageSidebandStream)
	if !ok {
		t.Fatal("expected native sideband stream")
	}
	first := sideband.DrainAccountingEvidence()
	if len(first) != 1 || first[0].InputTokens == nil || *first[0].InputTokens != input {
		t.Fatalf("first drain = %#v", first)
	}
	if second := sideband.DrainAccountingEvidence(); len(second) != 0 {
		t.Fatalf("second drain = %#v, want empty", second)
	}
	if _, err := sideband.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("open error = %v, want EOF", err)
	}
}

func TestNativeUsageSidebandStreamDoesNotClaimPrimaryUsageAuthority(t *testing.T) {
	t.Parallel()
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: 13, UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}, io.EOF)
	state, ok := stream.(interface{ AccountingEvidenceEnabled() bool })
	if !ok {
		t.Fatal("compaction sideband does not expose explicit authority state")
	}
	if state.AccountingEvidenceEnabled() {
		t.Fatal("compaction sideband claimed authority over the wrapped primary stream")
	}
}

func TestNativeUsageSidebandStream_preservesOpenError(t *testing.T) {
	t.Parallel()
	openErr := errors.New("normal request failed")
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: 13, UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}, openErr)
	sideband, ok := stream.(*nativeUsageSidebandStream)
	if !ok {
		t.Fatal("expected native sideband stream")
	}
	if _, err := sideband.Recv(context.Background()); !errors.Is(err, openErr) {
		t.Fatalf("open error = %v, want %v", err, openErr)
	}
}

func TestNativeUsageSidebandStream_rejectsNilAndCanceledContext(t *testing.T) {
	t.Parallel()
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: 13, UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}, io.EOF)
	sideband, ok := stream.(*nativeUsageSidebandStream)
	if !ok {
		t.Fatal("expected native sideband stream")
	}
	var nilCtx context.Context
	if _, err := sideband.Recv(nilCtx); !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sideband.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestNativeContextTelemetry_snapshotIsFixedAndPrivate(t *testing.T) {
	t.Parallel()
	telemetry := newNativeContextTelemetry()
	telemetry.recordContext(101, 41, 1001, 501)
	telemetry.recordReasoning(reasoningTelemetryRequested)
	telemetry.recordCheckpoint(checkpointTelemetryHit)
	telemetry.recordCheckpoint(checkpointTelemetryMismatch)
	telemetry.recordCompaction(compactionTelemetrySuccess, 17)
	telemetry.recordCompaction(compactionTelemetryCanceled, 3)

	snapshot := telemetry.snapshot()
	if snapshot.ContextBeforeTokens != 101 || snapshot.ContextAfterTokens != 41 || snapshot.ContextBeforeBytes != 1001 || snapshot.ContextAfterBytes != 501 {
		t.Fatalf("context snapshot = %#v", snapshot)
	}
	if snapshot.ReasoningRequested != 1 || snapshot.CheckpointHits != 1 || snapshot.CheckpointMismatches != 1 {
		t.Fatalf("reasoning/checkpoint snapshot = %#v", snapshot)
	}
	if snapshot.CompactionSuccesses != 1 || snapshot.CompactionCanceled != 1 || snapshot.CompactionUsageTokens != 20 {
		t.Fatalf("compaction snapshot = %#v", snapshot)
	}
	for _, secret := range []string{"opaque-ciphertext-secret", "prompt-marker-secret", "account-token-secret"} {
		if strings.Contains(snapshot.String(), secret) {
			t.Fatalf("snapshot leaked %q: %s", secret, snapshot.String())
		}
	}
}

func TestNativeContextTelemetry_concurrentSnapshots(t *testing.T) {
	t.Parallel()
	start := time.Unix(100, 0)
	clock := start
	telemetry := newNativeContextTelemetryWithClock(func() time.Time { return clock })
	telemetry.recordLatency(start)
	clock = start.Add(17 * time.Millisecond)
	telemetry.recordLatency(start)
	if got := telemetry.snapshot().CompactionLatencyMillis; got != 17 {
		t.Fatalf("latency = %d, want 17", got)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			telemetry.recordReasoning(reasoningTelemetryRequested)
			_ = telemetry.snapshot().ReasoningRequested
		})
	}
	wg.Wait()
	if got := telemetry.snapshot().ReasoningRequested; got != 8 {
		t.Fatalf("reasoning requests = %d, want 8", got)
	}
}

func validateNativeUsageEvidenceForTest(e backendplugin.AccountingEvidence) error {
	return backendplugin.ValidateAccountingEvidence(e)
}

type usageAggregateTestStream struct {
	events []aipapi.Event
	err    error
	idx    int
}

func (s *usageAggregateTestStream) Recv(context.Context) (aipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return aipapi.Event{}, err
	}
	return aipapi.Event{}, io.EOF
}

func (s *usageAggregateTestStream) Close() error { return nil }

func (s *usageAggregateTestStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func isNativeContentEvent(kind aipapi.EventKind) bool {
	switch kind {
	case aipapi.EventTextDelta, aipapi.EventReasoningDelta, aipapi.EventReasoningSignatureDelta,
		aipapi.EventReasoningOpaqueDelta, aipapi.EventReasoningPart,
		aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
		aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
		return true
	default:
		return false
	}
}

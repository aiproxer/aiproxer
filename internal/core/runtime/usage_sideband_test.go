package runtime

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type usageSidebandStream struct {
	mu       sync.Mutex
	evidence []aipapi.Event
	event    aipapi.Event
	err      error
}

func (s *usageSidebandStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.event.Kind != "" {
		ev := s.event
		s.event = aipapi.Event{}
		return ev, nil
	}
	return aipapi.Event{}, s.err
}

func (s *usageSidebandStream) Close() error { return nil }

func (s *usageSidebandStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *usageSidebandStream) DrainUsageEvidence() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]aipapi.Event(nil), s.evidence...)
	s.evidence = nil
	return out
}

type usageSidebandObserver struct {
	mu     sync.Mutex
	events []usage.Event
}

func (o *usageSidebandObserver) OnUsage(_ context.Context, ev usage.Event) error {
	o.mu.Lock()
	o.events = append(o.events, ev)
	o.mu.Unlock()
	return nil
}

func TestConsumeBackendUsageEvidenceDrainsOnceWithoutCanonicalRecv(t *testing.T) {
	stream := &usageSidebandStream{
		evidence: []aipapi.Event{{
			Kind: aipapi.EventUsageDelta, InputTokens: 13,
			Accounting: aipapi.UsageAccountingMetadata{DedupeKey: "compaction-1"},
		}},
		event: aipapi.Event{Kind: aipapi.EventResponseFinished}, err: io.EOF,
	}
	rs := &retryRecvStream{responsePipeline: newResponsePipeline()}
	attempt := testAttemptSession(rs)
	rs.responsePipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), rs.facts, attempt, stream)
	rs.responsePipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), rs.facts, attempt, stream)

	if got := len(rs.responsePipeline.seenEventsCopy()); got != 1 {
		t.Fatalf("seen events = %d, want one sideband event", got)
	}
	if ev := rs.responsePipeline.seenEventsCopy()[0]; ev.Kind != aipapi.EventUsageDelta || ev.InputTokens != 13 {
		t.Fatalf("sideband event = %#v", ev)
	}
}

func TestUsageEvidenceDedupeKeySuppressesCanonicalReplay(t *testing.T) {
	rs := &retryRecvStream{responsePipeline: newResponsePipeline()}
	attempt := testAttemptSession(rs)
	first := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 13, Accounting: aipapi.UsageAccountingMetadata{DedupeKey: "compaction-1"}}
	if !attempt.rememberUsageEvidenceOnce(first) {
		t.Fatal("first evidence was unexpectedly treated as duplicate")
	}
	if attempt.rememberUsageEvidenceOnce(first) {
		t.Fatal("replayed evidence key was not deduplicated")
	}
}

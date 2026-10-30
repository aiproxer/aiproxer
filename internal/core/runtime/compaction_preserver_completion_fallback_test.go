package runtime_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

type completionFallbackPreserver struct {
	mu sync.Mutex

	requestTx       string
	requestPhase    compaction.Phase
	requestEvidence compaction.Evidence
	pending         bool
	finishTx        []string
	opened          int
}

func (*completionFallbackPreserver) ID() string { return "completion-fallback" }

func (*completionFallbackPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *completionFallbackPreserver) RequestOpened(_ context.Context, _ aipapi.Call, events []compaction.Event, _ compaction.PreservationMeta, _ compaction.Services) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, event := range events {
		if event.TransactionID != "" {
			p.opened++
			p.requestTx = event.TransactionID
			p.requestPhase = event.Phase
			p.requestEvidence = event.Evidence
			p.pending = true
		}
	}
	return nil
}

func (*completionFallbackPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *completionFallbackPreserver) AfterResponseRelease(_ context.Context, event aipapi.Event, meta compaction.PreservationMeta, _ compaction.Services) error {
	if event.Kind != aipapi.EventResponseFinished {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finishTx = append(p.finishTx, meta.TransactionID)
	if meta.TransactionID == p.requestTx {
		p.pending = false
	}
	return nil
}

func (p *completionFallbackPreserver) snapshot() (string, bool, []string, int, compaction.Phase, compaction.Evidence) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requestTx, p.pending, append([]string(nil), p.finishTx...), p.opened, p.requestPhase, p.requestEvidence
}

func TestCompactionPreserver_completionOnlyReleaseUsesCommittedRequestTransaction(t *testing.T) {
	d := compactiondetect.New(compactiondetect.Config{})
	p := &completionFallbackPreserver{}
	ex := compactionTestExecutor(t, d, nil)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionPreservers: []compaction.Preserver{p},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}
	firstCall := compactCall("completion-fallback", bigItems(bigText(40000), "tail-one", "tail-two"))
	first, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, first)

	secondCall := compactCall("completion-fallback", bigItems(bigText(6000), "tail-one", "tail-two"))
	secondCall.Session.AuthoritativeSessionID = firstCall.Session.AuthoritativeSessionID
	secondCall.Session.ResumeToken = firstCall.Session.ResumeToken
	second, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, second)
	requestTx, pending, finishTx, opened, phase, evidence := p.snapshot()
	if requestTx == "" || opened < 1 || phase != compaction.PhaseCompleted || evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("completion-only RequestOpened did not commit history transaction: tx=%q opened=%d phase=%q evidence=%q", requestTx, opened, phase, evidence)
	}
	if pending {
		t.Fatalf("returned ResponseFinished left transaction pending: tx=%q finishes=%v", requestTx, finishTx)
	}
	if len(finishTx) < 2 || finishTx[len(finishTx)-1] != requestTx {
		t.Fatalf("completion release did not use committed request transaction: request=%q finishes=%v", requestTx, finishTx)
	}
}

func TestCompactionPreserver_completionOnlyAbortRetainsPendingTransaction(t *testing.T) {
	d := compactiondetect.New(compactiondetect.Config{})
	p := &completionFallbackPreserver{}
	ex := compactionTestExecutor(t, d, nil)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionPreservers: []compaction.Preserver{p},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}
	firstCall := compactCall("completion-abort", bigItems(bigText(40000), "tail-one", "tail-two"))
	first, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, first)
	secondCall := compactCall("completion-abort", bigItems(bigText(6000), "tail-one", "tail-two"))
	secondCall.Session.AuthoritativeSessionID = firstCall.Session.AuthoritativeSessionID
	secondCall.Session.ResumeToken = firstCall.Session.ResumeToken
	second, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	requestTx, pending, finishTx, opened, phase, evidence := p.snapshot()
	if requestTx == "" || !pending || opened < 1 || phase != compaction.PhaseCompleted || evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("aborted completion request did not retain pending history transaction: tx=%q pending=%v finishes=%v opened=%d phase=%q evidence=%q", requestTx, pending, finishTx, opened, phase, evidence)
	}
	if len(finishTx) != 1 {
		t.Fatalf("aborted stream committed a response release: finishes=%v", finishTx)
	}
}

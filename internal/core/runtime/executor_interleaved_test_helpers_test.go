package runtime

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

var (
	testMemoStoreMu sync.RWMutex
	testMemoStores  = make(map[*Executor]interleavedthinking.MemoStore)
)

func RegisterTestMemoStore(ex *Executor, store interleavedthinking.MemoStore) {
	testMemoStoreMu.Lock()
	defer testMemoStoreMu.Unlock()
	testMemoStores[ex] = store
}

func GetTestMemoStore(ex *Executor) interleavedthinking.MemoStore {
	testMemoStoreMu.RLock()
	defer testMemoStoreMu.RUnlock()
	return testMemoStores[ex]
}

func NewTestInterleavedProcessor(t *testing.T, cfg interleavedthinking.Config, store interleavedthinking.MemoStore) InterleavedProcessor {
	t.Helper()
	cfg.Enabled = true
	proc, err := interleavedthinking.NewProcessor(cfg, store)
	if err != nil {
		t.Fatalf("newTestInterleavedProcessor: %v", err)
	}
	return newTestInterleavedProcessorAdapter(proc)
}

var (
	_ InterleavedProcessor = (*testInterleavedProcessorAdapter)(nil)
	_ InterleavedTurn      = (*testInterleavedTurnAdapter)(nil)
)

type testInterleavedProcessorAdapter struct {
	inner interleavedthinking.Processor
}

func newTestInterleavedProcessorAdapter(inner interleavedthinking.Processor) InterleavedProcessor {
	if inner == nil {
		return nil
	}
	return &testInterleavedProcessorAdapter{inner: inner}
}

func (a *testInterleavedProcessorAdapter) BeginTurn(ctx context.Context, in InterleavedTurnInput) (InterleavedTurn, error) {
	if a == nil || a.inner == nil {
		return nil, nil
	}
	featureIn := interleavedthinking.TurnInput{
		ALegID:              in.ALegID,
		Selector:            in.Selector,
		Backend:             in.Backend,
		Model:               in.Model,
		RequestID:           in.RequestID,
		StreamToClient:      in.StreamToClient,
		SuppressVisibleMemo: in.SuppressVisibleMemo,
	}
	turn, err := a.inner.BeginTurn(ctx, featureIn)
	if err != nil {
		return nil, err
	}
	if turn == nil {
		return nil, nil
	}
	return &testInterleavedTurnAdapter{inner: turn}, nil
}

func (a *testInterleavedProcessorAdapter) IsMemoVisibleToClient(ctx context.Context, aLegID string) bool {
	if a == nil || a.inner == nil {
		return false
	}
	return a.inner.IsMemoVisibleToClient(ctx, aLegID)
}

func (a *testInterleavedProcessorAdapter) MemoSteeringPutRequest(memo string) steering.PutRequest {
	if a == nil || a.inner == nil {
		return steering.PutRequest{}
	}
	return interleavedthinking.MemoPutRequest(memo)
}

func (a *testInterleavedProcessorAdapter) MemoSteeringOverlayID() steering.OverlayID {
	return steering.OverlayID(interleavedthinking.MemoOverlayID)
}

func (a *testInterleavedProcessorAdapter) IsMemoSteeringOverlay(overlayID string) bool {
	return interleavedthinking.IsMemoOverlay(overlayID)
}

type testInterleavedTurnAdapter struct {
	inner interleavedthinking.Turn
}

func (t *testInterleavedTurnAdapter) ShapeThinker(call aipapi.Call) (aipapi.Call, error) {
	if t == nil || t.inner == nil {
		return aipapi.CloneCall(call), nil
	}
	return t.inner.ShapeThinker(call)
}

func (t *testInterleavedTurnAdapter) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
	if t == nil || t.inner == nil {
		return []aipapi.Event{ev}, nil
	}
	events, err := t.inner.ObserveThinkerEvent(ev)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return slices.Clone(events), nil
}

func (t *testInterleavedTurnAdapter) FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (InterleavedMemo, error) {
	if t == nil || t.inner == nil {
		return InterleavedMemo{}, nil
	}
	res, err := t.inner.FinalizeThinkerStatus(ctx, interrupted, visibleCommitted)
	if err != nil {
		return InterleavedMemo{}, err
	}
	return InterleavedMemo{
		Text:       res.Text,
		Reference:  res.Reference,
		Version:    res.Version,
		HadContent: res.HadContent,
	}, nil
}

func (t *testInterleavedTurnAdapter) FinalizeThinker(ctx context.Context) (InterleavedMemo, error) {
	if t == nil || t.inner == nil {
		return InterleavedMemo{}, nil
	}
	res, err := t.inner.FinalizeThinker(ctx)
	if err != nil {
		return InterleavedMemo{}, err
	}
	return InterleavedMemo{
		Text:       res.Text,
		Reference:  res.Reference,
		Version:    res.Version,
		HadContent: res.HadContent,
	}, nil
}

func (t *testInterleavedTurnAdapter) ShapeExecutor(ctx context.Context, call aipapi.Call, memo InterleavedMemo) (aipapi.Call, error) {
	if t == nil || t.inner == nil {
		return aipapi.CloneCall(call), nil
	}
	featureMemo := interleavedthinking.MemoResult{
		Text:       memo.Text,
		Reference:  memo.Reference,
		Version:    memo.Version,
		HadContent: memo.HadContent,
	}
	return t.inner.ShapeExecutor(ctx, call, featureMemo)
}

func (t *testInterleavedTurnAdapter) Visible() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.Visible()
}

func (t *testInterleavedTurnAdapter) CanContinue() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.CanContinue()
}

func (t *testInterleavedTurnAdapter) ShapeDiagnostics() (string, int) {
	if t == nil || t.inner == nil {
		return "", 0
	}
	return t.inner.ShapeDiagnostics()
}

func (t *testInterleavedTurnAdapter) CommitExecutor(ctx context.Context) (int, error) {
	if t == nil || t.inner == nil {
		return -1, nil
	}
	return t.inner.CommitExecutor(ctx)
}

func (t *testInterleavedTurnAdapter) FlushVisible() []aipapi.Event {
	if t == nil || t.inner == nil {
		return nil
	}
	events := t.inner.FlushVisible()
	if len(events) == 0 {
		return nil
	}
	return slices.Clone(events)
}

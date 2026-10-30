package featurehost

import (
	"context"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// Compile-time interface assertions.
var (
	_ runtime.InterleavedProcessor = (*interleavedProcessorAdapter)(nil)
	_ runtime.InterleavedTurn      = (*interleavedTurnAdapter)(nil)
)

type interleavedProcessorAdapter struct {
	inner interleavedthinking.Processor
}

// NewInterleavedProcessorAdapter creates a new runtime.InterleavedProcessor wrapping
// the feature-owned interleavedthinking.Processor.
func NewInterleavedProcessorAdapter(inner interleavedthinking.Processor) runtime.InterleavedProcessor {
	if inner == nil {
		return nil
	}
	return &interleavedProcessorAdapter{inner: inner}
}

func (a *interleavedProcessorAdapter) BeginTurn(ctx context.Context, in runtime.InterleavedTurnInput) (runtime.InterleavedTurn, error) {
	if a == nil || a.inner == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
	return &interleavedTurnAdapter{inner: turn}, nil
}

func (a *interleavedProcessorAdapter) IsMemoVisibleToClient(ctx context.Context, aLegID string) bool {
	if a == nil || a.inner == nil {
		return false
	}
	return a.inner.IsMemoVisibleToClient(ctx, aLegID)
}

// MemoSteeringPutRequest forwards the feature-owned memo steering mutation;
// core persists it without interpreting feature semantics.
func (a *interleavedProcessorAdapter) MemoSteeringPutRequest(memo string) steering.PutRequest {
	if a == nil || a.inner == nil {
		return steering.PutRequest{}
	}
	return interleavedthinking.MemoPutRequest(memo)
}

// MemoSteeringOverlayID returns the feature-owned stable memo overlay identity.
func (a *interleavedProcessorAdapter) MemoSteeringOverlayID() steering.OverlayID {
	return steering.OverlayID(interleavedthinking.MemoOverlayID)
}

// IsMemoSteeringOverlay reports whether overlayID carries the thinker memo.
func (a *interleavedProcessorAdapter) IsMemoSteeringOverlay(overlayID string) bool {
	return interleavedthinking.IsMemoOverlay(overlayID)
}

type interleavedTurnAdapter struct {
	inner interleavedthinking.Turn
}

func (t *interleavedTurnAdapter) ShapeThinker(call aipapi.Call) (aipapi.Call, error) {
	if t == nil || t.inner == nil {
		return aipapi.CloneCall(call), nil
	}
	return t.inner.ShapeThinker(call)
}

func (t *interleavedTurnAdapter) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
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

func (t *interleavedTurnAdapter) FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (runtime.InterleavedMemo, error) {
	if t == nil || t.inner == nil {
		return runtime.InterleavedMemo{}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtime.InterleavedMemo{}, err
	}
	res, err := t.inner.FinalizeThinkerStatus(ctx, interrupted, visibleCommitted)
	if err != nil {
		return runtime.InterleavedMemo{}, err
	}
	return runtime.InterleavedMemo{
		Text:       res.Text,
		Reference:  res.Reference,
		Version:    res.Version,
		HadContent: res.HadContent,
	}, nil
}

func (t *interleavedTurnAdapter) FinalizeThinker(ctx context.Context) (runtime.InterleavedMemo, error) {
	if t == nil || t.inner == nil {
		return runtime.InterleavedMemo{}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtime.InterleavedMemo{}, err
	}
	res, err := t.inner.FinalizeThinker(ctx)
	if err != nil {
		return runtime.InterleavedMemo{}, err
	}
	return runtime.InterleavedMemo{
		Text:       res.Text,
		Reference:  res.Reference,
		Version:    res.Version,
		HadContent: res.HadContent,
	}, nil
}

func (t *interleavedTurnAdapter) ShapeExecutor(ctx context.Context, call aipapi.Call, memo runtime.InterleavedMemo) (aipapi.Call, error) {
	if t == nil || t.inner == nil {
		return aipapi.CloneCall(call), nil
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Call{}, err
	}
	featureMemo := interleavedthinking.MemoResult{
		Text:       memo.Text,
		Reference:  memo.Reference,
		Version:    memo.Version,
		HadContent: memo.HadContent,
	}
	return t.inner.ShapeExecutor(ctx, call, featureMemo)
}

func (t *interleavedTurnAdapter) Visible() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.Visible()
}

func (t *interleavedTurnAdapter) CanContinue() bool {
	if t == nil || t.inner == nil {
		return false
	}
	return t.inner.CanContinue()
}

func (t *interleavedTurnAdapter) ShapeDiagnostics() (string, int) {
	if t == nil || t.inner == nil {
		return "", 0
	}
	return t.inner.ShapeDiagnostics()
}

func (t *interleavedTurnAdapter) CommitExecutor(ctx context.Context) (int, error) {
	if t == nil || t.inner == nil {
		return -1, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return t.inner.CommitExecutor(ctx)
}

func (t *interleavedTurnAdapter) FlushVisible() []aipapi.Event {
	if t == nil || t.inner == nil {
		return nil
	}
	events := t.inner.FlushVisible()
	if len(events) == 0 {
		return nil
	}
	return slices.Clone(events)
}

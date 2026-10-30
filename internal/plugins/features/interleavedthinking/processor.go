package interleavedthinking

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TurnInput carries per-turn facts required by interleaved thinking.
type TurnInput struct {
	ALegID              string
	Selector            string
	Backend             string
	Model               string
	RequestID           string
	StreamToClient      string
	SuppressVisibleMemo bool
}

// MemoResult carries minimal evidence of captured thinker output.
type MemoResult struct {
	Text       string
	Reference  string
	Version    int64
	HadContent bool
}

// Processor is the feature-owned entry point for interleaved thinking per turn.
type Processor interface {
	BeginTurn(ctx context.Context, in TurnInput) (Turn, error)
	IsMemoVisibleToClient(ctx context.Context, aLegID string) bool
}

// Turn is the feature-owned lifecycle contract for a single turn.
type Turn interface {
	ShapeThinker(call aipapi.Call) (aipapi.Call, error)
	ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error)
	FinalizeThinker(ctx context.Context) (MemoResult, error)
	FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (MemoResult, error)
	ShapeExecutor(ctx context.Context, call aipapi.Call, memo MemoResult) (aipapi.Call, error)
	Visible() bool
	CanContinue() bool
	ShapeDiagnostics() (outcome string, turnsRemaining int)
	CommitExecutor(ctx context.Context) (int, error)
	FlushVisible() []aipapi.Event
}

var (
	_ Processor = (*processor)(nil)
	_ Turn      = (*featureTurn)(nil)
)

type processor struct {
	cfg          Config
	instructions string
	store        MemoStore
}

// NewProcessor constructs a feature-owned Processor.
func NewProcessor(cfg Config, store MemoStore) (Processor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	instructions, err := ResolveInstructions("", cfg.InstructionsFile, cfg.Instructions)
	if err != nil {
		return nil, err
	}
	return &processor{
		cfg:          cfg,
		instructions: instructions,
		store:        store,
	}, nil
}

func (p *processor) BeginTurn(ctx context.Context, in TurnInput) (Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rec := &Recorder{
		MaxMemoBytes:          p.cfg.EffectiveMaxMemoBytes(),
		SourceSelector:        strings.TrimSpace(in.Selector),
		Backend:               strings.TrimSpace(in.Backend),
		Model:                 strings.TrimSpace(in.Model),
		RequestID:             strings.TrimSpace(in.RequestID),
		RegularTurnsRemaining: p.cfg.EffectiveRegularTurnsRemaining(),
	}
	return &featureTurn{
		p:        p,
		input:    in,
		recorder: rec,
	}, nil
}

func (p *processor) IsMemoVisibleToClient(ctx context.Context, aLegID string) bool {
	if p == nil || p.store == nil || strings.TrimSpace(aLegID) == "" {
		return false
	}
	st, _, ok, err := p.store.LatestEntry(ctx, Scope(aLegID))
	if err != nil || !ok {
		return false
	}
	return st.VisibleToClient
}

type featureTurn struct {
	p             *processor
	input         TurnInput
	recorder      *Recorder
	lastMemo      MemoResult
	pendingUpdate *PendingMemoUpdate
	lastOutcome   MemoOutcome
	turnsAfter    int
}

func (t *featureTurn) Visible() bool {
	if t == nil || t.p == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(t.input.StreamToClient))
	if mode == "" {
		mode = t.p.cfg.EffectiveStreamToClient()
	}
	return mode == "visible"
}

func (t *featureTurn) CanContinue() bool {
	return t != nil && t.p != nil && t.p.store != nil
}

func (t *featureTurn) ShapeDiagnostics() (string, int) {
	if t == nil {
		return "", 0
	}
	return string(t.lastOutcome), t.turnsAfter
}

func (t *featureTurn) ShapeThinker(call aipapi.Call) (aipapi.Call, error) {
	return ShapeThinkerCall(call, t.p.instructions)
}

func (t *featureTurn) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
	if t.recorder == nil {
		if !t.Visible() {
			return nil, nil
		}
		return []aipapi.Event{ev}, nil
	}
	visibles := t.recorder.Observe(ev)
	if !t.Visible() {
		return nil, nil
	}
	return visibles, nil
}

func (t *featureTurn) FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (MemoResult, error) {
	if err := ctx.Err(); err != nil {
		return MemoResult{}, err
	}
	if t.recorder == nil {
		return MemoResult{}, nil
	}
	memoState := t.recorder.Finish(interrupted)
	memoState.VisibleToClient = !interrupted && visibleCommitted && t.Visible()
	if t.p.store == nil || strings.TrimSpace(t.input.ALegID) == "" {
		res := MemoResult{
			Text:       memoState.Memo,
			HadContent: t.recorder.HadContent(),
		}
		t.lastMemo = res
		return res, nil
	}
	ref, err := t.p.store.Put(ctx, Scope(t.input.ALegID), memoState)
	if err != nil {
		return MemoResult{}, fmt.Errorf("interleavedthinking: put memo: %w", err)
	}
	res := MemoResult{
		Text:       memoState.Memo,
		Reference:  ref.Key,
		Version:    ref.Version,
		HadContent: t.recorder.HadContent(),
	}
	t.lastMemo = res
	return res, nil
}

func (t *featureTurn) FinalizeThinker(ctx context.Context) (MemoResult, error) {
	return t.FinalizeThinkerStatus(ctx, false, t.Visible())
}

func (t *featureTurn) ShapeExecutor(ctx context.Context, call aipapi.Call, memo MemoResult) (aipapi.Call, error) {
	if err := ctx.Err(); err != nil {
		return aipapi.Call{}, err
	}
	if t.p.store == nil || strings.TrimSpace(t.input.ALegID) == "" {
		return aipapi.CloneCall(call), nil
	}
	var ref state.MemoRef
	if memo.Reference != "" {
		ref = state.MemoRef{Key: memo.Reference, Version: memo.Version}
	} else if t.lastMemo.Reference != "" {
		ref = state.MemoRef{Key: t.lastMemo.Reference, Version: t.lastMemo.Version}
	} else {
		_, latestRef, found, err := t.p.store.LatestEntry(ctx, Scope(t.input.ALegID))
		if err != nil {
			return aipapi.Call{}, err
		}
		if found {
			ref = latestRef
		}
	}
	if ref.Key == "" {
		t.lastOutcome = MemoOutcomeSkippedMissing
		return aipapi.CloneCall(call), nil
	}
	shaped, _, update, outcome, err := ShapeExecutorCall(
		ctx,
		call,
		t.p.store,
		Scope(t.input.ALegID),
		&ref,
		t.input.SuppressVisibleMemo,
	)
	if err != nil {
		return aipapi.Call{}, err
	}
	t.pendingUpdate = update
	t.lastOutcome = outcome
	if update != nil {
		t.turnsAfter = update.State.RegularTurnsRemaining
	}
	return shaped, nil
}

func (t *featureTurn) FlushVisible() []aipapi.Event {
	if t == nil || t.recorder == nil || !t.Visible() {
		return nil
	}
	return t.recorder.FlushVisibleSanitizer()
}

func (t *featureTurn) CommitExecutor(ctx context.Context) (int, error) {
	if t == nil || t.pendingUpdate == nil || t.p == nil || t.p.store == nil {
		return -1, nil
	}
	update := t.pendingUpdate
	t.pendingUpdate = nil
	if _, err := t.p.store.Update(ctx, Scope(t.input.ALegID), update.Ref, update.State); err != nil {
		return 0, fmt.Errorf("interleavedthinking: update memo budget: %w", err)
	}
	return update.State.RegularTurnsRemaining, nil
}

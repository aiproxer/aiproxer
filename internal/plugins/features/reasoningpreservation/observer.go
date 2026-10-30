package reasoningpreservation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

type StreamObserverFactory struct {
	cfg            Config
	store          TurnStore
	tel            *Telemetry
	id             string
	order          int
	lastDiag       atomic.Value
	postAppendHook PostAppendHook
}

func NewStreamObserverFactory(cfg Config, store TurnStore, tel ...*Telemetry) *StreamObserverFactory {
	return NewStreamObserverFactoryWithPostAppendHook(cfg, store, nil, tel...)
}

// NewStreamObserverFactoryWithPostAppendHook creates a factory with an immutable post-append hook.
// The hook is assigned once at construction and never mutated thereafter, avoiding data races.
// When compression is disabled the hook must be nil.
func NewStreamObserverFactoryWithPostAppendHook(cfg Config, store TurnStore, hook PostAppendHook, tel ...*Telemetry) *StreamObserverFactory {
	var t *Telemetry
	if len(tel) > 0 {
		t = tel[0]
	}
	if t == nil {
		t = NewTelemetry()
	}
	f := &StreamObserverFactory{
		cfg:            cfg,
		store:          store,
		tel:            t,
		id:             ID + "-observer",
		order:          0,
		postAppendHook: hook,
	}
	f.lastDiag.Store("")
	return f
}

func (f *StreamObserverFactory) ID() string { return f.id }
func (f *StreamObserverFactory) Order() int { return f.order }
func (f *StreamObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (f *StreamObserverFactory) LastSafeDiagnostic() string {
	if v, ok := f.lastDiag.Load().(string); ok {
		return v
	}
	return ""
}

func (f *StreamObserverFactory) recordOutcome(outcome SafeOutcome, counts map[string]int) {
	if f.tel != nil {
		f.tel.Record(outcome, counts)
	}
	diag, err := FormatSafeDiagnostic(outcome, "", counts)
	if err != nil {
		diag, _ = ProjectSafeError(err)
	}
	f.lastDiag.Store(diag)
}

func (f *StreamObserverFactory) Open(_ context.Context, meta response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	if f.store == nil {
		return nil, fmt.Errorf("%s: store is required", ID)
	}
	meta.BackendPrefixes = slices.Clone(meta.BackendPrefixes)
	match, err := ResolveMatch(f.cfg, CandidateIdentity{
		BackendID:       meta.BackendID,
		BackendPrefixes: meta.BackendPrefixes,
		Model:           meta.Model,
	})
	if err != nil || !MatchEligible(match.Kind) {
		return inertStreamObserver{}, nil
	}
	return &streamObserver{
		factory: f,
		cfg:     f.cfg,
		store:   f.store,
		meta:    meta,
		now:     time.Now,
	}, nil
}

type streamObserver struct {
	factory *StreamObserverFactory
	cfg     Config
	store   TurnStore
	meta    response.StreamMeta
	now     func() time.Time

	mu             sync.Mutex
	finished       bool
	oversized      bool
	parts          []aipapi.Part
	curText        strings.Builder
	curReason      *aipapi.ReasoningPart
	curReasonText  strings.Builder
	curReasonSig   strings.Builder
	reasoningBytes int
	toolID         string
	toolName       string
	toolArgs       strings.Builder
	toolOpen       bool
}

func (o *streamObserver) Observe(_ context.Context, ev aipapi.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished || o.oversized {
		return nil
	}
	switch ev.Kind {
	case aipapi.EventTextDelta:
		o.flushToolLocked()
		o.flushReasoningLocked()
		o.curText.WriteString(ev.Delta)
	case aipapi.EventReasoningDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		if o.curReason == nil {
			o.curReason = &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			}
		}
		o.curReasonText.WriteString(ev.Delta)
		o.reasoningBytes += len(ev.Delta)
	case aipapi.EventReasoningSignatureDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		if o.curReason == nil {
			o.curReason = &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectAnthropicThinkingV1,
			}
		}
		o.curReasonSig.WriteString(ev.Signature)
		if o.curReason.Dialect == aipapi.ReasoningDialectOpenAIChatTextV1 {
			o.curReason.Dialect = aipapi.ReasoningDialectAnthropicThinkingV1
		}
		o.reasoningBytes += len(ev.Signature)
	case aipapi.EventReasoningOpaqueDelta:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		if len(ev.Opaque) == 0 {
			break
		}
		opaque := append(json.RawMessage(nil), ev.Opaque...)
		o.parts = append(o.parts, aipapi.Part{
			Kind: aipapi.PartReasoning,
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
				Opaque:  opaque,
			},
		})
		o.reasoningBytes += len(opaque)
	case aipapi.EventReasoningPart:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		if err := o.captureExactReasoningPartLocked(ev.Reasoning); err != nil {
			return err
		}
	case aipapi.EventToolCallStarted:
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.flushToolLocked()
		o.toolOpen = true
		o.toolID = ev.ToolCallID
		o.toolName = ev.ToolName
		o.toolArgs.Reset()
	case aipapi.EventToolCallArgsDelta:
		if !o.toolOpen {
			o.toolOpen = true
			o.toolID = ev.ToolCallID
			o.toolName = ev.ToolName
		}
		o.toolArgs.WriteString(ev.Delta)
	case aipapi.EventToolCallFinished:
		o.flushTextLocked()
		o.flushReasoningLocked()
		if ev.ToolCallID != "" {
			o.toolID = ev.ToolCallID
		}
		if ev.ToolName != "" {
			o.toolName = ev.ToolName
		}
		if ev.Delta != "" && o.toolArgs.Len() == 0 {
			o.toolArgs.WriteString(ev.Delta)
		}
		o.flushToolLocked()
	case aipapi.EventAssistantImageRef:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.parts = append(o.parts, aipapi.Part{
			Kind:      aipapi.PartImageRef,
			ImageRef:  ev.AssistantRef,
			ImageMIME: ev.AssistantMIME,
		})
	case aipapi.EventAssistantFileRef:
		o.flushToolLocked()
		o.flushTextLocked()
		o.flushReasoningLocked()
		o.parts = append(o.parts, aipapi.Part{
			Kind:     aipapi.PartFileRef,
			FileRef:  ev.AssistantRef,
			FileMIME: ev.AssistantMIME,
			FileName: ev.AssistantName,
		})
	}
	if o.cfg.State.MaxReasoningBytesPerTurn > 0 && o.reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.markOversizeLocked()
	}
	return nil
}

func (o *streamObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		return nil
	}
	o.finished = true
	if outcome != response.OutcomeSuccessReleased {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if !o.captureEligible() {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if o.oversized {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": o.reasoningBytes})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	o.flushTextLocked()
	o.flushReasoningLocked()
	o.flushToolLocked()
	placed, _, err := DerivePlacementsFromParts(o.parts)
	if err != nil || len(placed) == 0 {
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	partition, ok := sessionPartitionOrMiss(o.meta.Session.AuthoritativeSessionID)
	if !ok {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	msg := aipapi.Message{Role: aipapi.RoleAssistant, Parts: o.parts}
	anchor, err := ComputeAnchor(msg)
	if err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	reasoningBytes := 0
	for _, p := range placed {
		reasoningBytes = int(aipapi.SaturatingAddInt64(int64(reasoningBytes), int64(aipapi.ReasoningPayloadBytes(p.Part.Reasoning))))
	}
	if reasoningBytes <= 0 || reasoningBytes > o.cfg.State.MaxReasoningBytesPerTurn {
		o.factory.recordOutcome(OutcomeOversize, map[string]int{"bytes": reasoningBytes})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	art := TurnArtifact{
		ID:             nextArtifactID(),
		Anchor:         anchor,
		SourceBackend:  o.meta.BackendID,
		SourceModel:    o.meta.Model,
		Reasoning:      placed,
		CreatedAt:      o.now().UTC(),
		ReasoningBytes: reasoningBytes,
	}
	sum, err := o.store.Append(ctx, partition, art)
	if err != nil {
		o.factory.recordOutcome(OutcomeStateError, map[string]int{"count": 1})
		o.clearPendingLocked()
		o.mu.Unlock()
		return nil
	}
	if sum.EvictedTurns > 0 || sum.ExpiredTurns > 0 || sum.EvictedBytes > 0 || sum.ExpiredBytes > 0 {
		o.factory.recordOutcome(OutcomeEvicted, map[string]int{
			"count": sum.EvictedTurns + sum.ExpiredTurns,
			"bytes": sum.EvictedBytes + sum.ExpiredBytes,
		})
	}
	o.factory.recordOutcome(OutcomeObserved, map[string]int{"count": 1, "bytes": reasoningBytes})
	var hook PostAppendHook
	var corr PostAppendCorrelation
	var hasCorr bool
	if o.factory.cfg.Compression.Enabled && o.factory.postAppendHook != nil {
		segs := ExtractSemanticSegments(art.Reasoning)
		if len(segs) == 0 {
			if o.factory.tel != nil {
				hasExact := false
				for _, pr := range art.Reasoning {
					if ClassifyReasoningPart(pr.Part) == ReplayExactRequired {
						hasExact = true
						break
					}
				}
				if hasExact {
					o.factory.tel.RecordShadowMeasurement(OutcomeExact, 0, 0, 0, 0, 0)
				} else {
					o.factory.tel.RecordShadowMeasurement(OutcomeCompIneligible, 0, 0, 0, 0, 0)
				}
			}
		} else {
			if o.factory.tel != nil {
				srcAll := 0
				for _, s := range segs {
					srcAll += len(s.Text)
				}
				// Eligible but may be below threshold; reservation stage will emit below_threshold if needed.
				// Mixed exact+semantic: eligible wins for flow, but also record exact for taxonomy completeness.
				o.factory.tel.RecordShadowMeasurement(OutcomeEligible, srcAll, 0, 0, 0, 0)
				hasExact := false
				for _, pr := range art.Reasoning {
					if ClassifyReasoningPart(pr.Part) == ReplayExactRequired {
						hasExact = true
						break
					}
				}
				if hasExact {
					o.factory.tel.RecordShadowMeasurement(OutcomeExact, 0, 0, 0, 0, 0)
				}
			}
			semDigest := computeSemanticDigest(art.Reasoning)
			egressHash := computeEgressPolicyRefHash(o.factory.cfg.Compression.EgressPolicyRef)
			srcBytes := 0
			for _, s := range segs {
				srcBytes += len(s.Text)
			}
			corr = PostAppendCorrelation{
				Partition:           partition,
				ArtifactID:          art.ID,
				Anchor:              anchor,
				OriginalDigest:      anchor,
				SemanticDigest:      semDigest,
				EgressPolicyRefHash: egressHash,
				SourceBytes:         srcBytes,
				TraceID:             o.meta.TraceID,
				ALegID:              o.meta.ALegID,
				BLegID:              o.meta.BLegID,
				BranchBinding:       o.meta.CandidateKey,
				Scope:               o.meta.Scope.Clone(),
				PolicyRevision:      o.factory.cfg.Compression.EgressPolicyRef,
			}
			hook = o.factory.postAppendHook
			hasCorr = true
		}
	}
	o.clearPendingLocked()
	o.mu.Unlock()
	if hasCorr && hook != nil {
		_ = hook(ctx, corr)
	}
	return nil
}

func (o *streamObserver) captureEligible() bool {
	match, err := ResolveMatch(o.cfg, CandidateIdentity{
		BackendID:       o.meta.BackendID,
		BackendPrefixes: o.meta.BackendPrefixes,
		Model:           o.meta.Model,
	})
	if err != nil {
		return false
	}
	return MatchEligible(match.Kind)
}

func (o *streamObserver) markOversizeLocked() {
	o.oversized = true
	o.clearPendingLocked()
}

func (o *streamObserver) clearPendingLocked() {
	o.parts = nil
	o.curText.Reset()
	o.curReason = nil
	o.curReasonText.Reset()
	o.curReasonSig.Reset()
	o.toolOpen = false
	o.toolID = ""
	o.toolName = ""
	o.toolArgs.Reset()
}

func (o *streamObserver) captureExactReasoningPartLocked(src *aipapi.ReasoningPart) error {
	if src == nil {
		return nil
	}
	cloned := &aipapi.ReasoningPart{
		Dialect:                 src.Dialect,
		Text:                    src.Text,
		Signature:               src.Signature,
		Summary:                 append(json.RawMessage(nil), src.Summary...),
		SummaryPresent:          src.SummaryPresent,
		Content:                 append(json.RawMessage(nil), src.Content...),
		ContentPresent:          src.ContentPresent,
		EncryptedContent:        append(json.RawMessage(nil), src.EncryptedContent...),
		EncryptedContentPresent: src.EncryptedContentPresent,
	}
	if src.Opaque != nil {
		cloned.Opaque = append(json.RawMessage(nil), src.Opaque...)
	}
	if len(cloned.Summary) == 0 && !src.SummaryPresent {
		cloned.Summary = nil
	}
	if len(cloned.Content) == 0 && !src.ContentPresent {
		cloned.Content = nil
	}
	if len(cloned.EncryptedContent) == 0 && !src.EncryptedContentPresent {
		cloned.EncryptedContent = nil
	}
	probe := aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: cloned}
	if err := aipapi.ValidateEventEnvelope(&probe); err != nil {
		return nil
	}
	add := aipapi.ReasoningPayloadBytes(cloned)
	if add <= 0 {
		return nil
	}
	next := int(aipapi.SaturatingAddInt64(int64(o.reasoningBytes), int64(add)))
	o.reasoningBytes = next
	if o.cfg.State.MaxReasoningBytesPerTurn > 0 && next > o.cfg.State.MaxReasoningBytesPerTurn {
		o.markOversizeLocked()
		return nil
	}
	o.parts = append(o.parts, aipapi.Part{
		Kind:      aipapi.PartReasoning,
		Reasoning: cloned,
	})
	return nil
}

func (o *streamObserver) flushTextLocked() {
	if o.curText.Len() == 0 {
		return
	}
	text := o.curText.String()
	o.curText.Reset()
	o.parts = append(o.parts, aipapi.TextPart(text))
}

func (o *streamObserver) flushReasoningLocked() {
	if o.curReason == nil {
		return
	}
	text := o.curReasonText.String()
	signature := o.curReasonSig.String()
	o.curReasonText.Reset()
	o.curReasonSig.Reset()
	if text == "" && signature == "" && len(o.curReason.Opaque) == 0 {
		o.curReason = nil
		return
	}
	o.curReason.Text = text
	o.curReason.Signature = signature
	o.parts = append(o.parts, clonePart(aipapi.Part{
		Kind:      aipapi.PartReasoning,
		Reasoning: o.curReason,
	}))
	o.curReason = nil
}

func (o *streamObserver) flushToolLocked() {
	if !o.toolOpen && o.toolArgs.Len() == 0 && o.toolID == "" {
		return
	}
	content := []byte(o.toolArgs.String())
	if len(content) == 0 {
		content = []byte("{}")
	}
	o.parts = append(o.parts, aipapi.Part{
		Kind:       aipapi.PartJSON,
		ToolCallID: o.toolID,
		ToolName:   o.toolName,
		Content:    content,
	})
	o.toolOpen = false
	o.toolID = ""
	o.toolName = ""
	o.toolArgs.Reset()
}

var artifactSeq atomic.Uint64

func nextArtifactID() string {
	return fmt.Sprintf("rp-%d", artifactSeq.Add(1))
}

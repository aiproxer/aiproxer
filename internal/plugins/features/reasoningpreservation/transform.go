package reasoningpreservation

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type AttemptTransform struct {
	cfg           Config
	store         TurnStore
	tel           *Telemetry
	id            string
	order         int
	companion     CompanionPolicy
	svc           CompressionServices
	adoptionStage CompletedAdoptionStage
}

func NewAttemptTransform(cfg Config, store TurnStore, tel ...*Telemetry) *AttemptTransform {
	var t *Telemetry
	if len(tel) > 0 {
		t = tel[0]
	}
	if t == nil {
		t = NewTelemetry()
	}
	return &AttemptTransform{
		cfg:           cfg,
		store:         store,
		tel:           t,
		id:            ID + "-transform",
		order:         0,
		adoptionStage: identityAdoptionStage,
	}
}

// NewAttemptTransformWithCompanionPolicyServicesAndStage is the full composition for bundle wiring.
func NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg Config, store TurnStore, svc CompressionServices, policy CompanionPolicy, stage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	t.svc = svc
	t.companion = policy
	if stage != nil {
		t.adoptionStage = stage
	}
	return t
}

func (t *AttemptTransform) ID() string { return t.id }
func (t *AttemptTransform) Order() int { return t.order }
func (t *AttemptTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

// compressionAttempt executes one eligible compression attempt end-to-end:
// shared classification, single poll, adoption guard, view selection,
// ephemeral candidate construction, and restore. It returns a typed result
// consumed by a single finalization path.
type compressionAttempt struct {
	cfg           Config
	cs            CompressionStore
	svc           CompressionServices
	tel           *Telemetry
	adoptionStage CompletedAdoptionStage
}

type compressionAttemptResult struct {
	call *aipapi.Call
	res  RestoreResult
}

func (c *compressionAttempt) run(ctx context.Context, call *aipapi.Call, meta request.AttemptMeta, arts []TurnArtifact, partition SessionPartition) (compressionAttemptResult, error) {
	classified, candidates, cerr := collectRestoreCandidates(call, arts, meta.ReplaySupport)
	if cerr != nil {
		pollRes := CompressionPollAttemptResult{Kind: PollKindPollError, Err: cerr}
		adoption := handleCompletedPollCandidate(ctx, c.cfg, c.cs, c.svc, c.tel, pollRes, call)
		if c.adoptionStage != nil {
			// The injected stage still sees the synthesized poll-error outcome, but
			// its projection is deliberately unused here: a poll error carries no
			// candidate to adopt, so this branch returns the state-error policy
			// result below instead of an adoption verdict.
			c.adoptionStage(ctx, adoption)
		}
		resTmp, rerr := applyStateErrorPolicy(c.cfg.OnStateError)
		if rerr != nil {
			return compressionAttemptResult{}, rerr
		}
		return compressionAttemptResult{call: call, res: resTmp}, nil
	}
	var pollCandidates []restoreCandidate
	for _, cand := range candidates {
		if !cand.Unsupported {
			pollCandidates = append(pollCandidates, cand)
		}
	}
	pollRes := pollOnceWithCandidates(ctx, c.cs, partition, pollCandidates, c.svc)
	if c.tel != nil && c.cfg.Compression.Enabled {
		switch pollRes.Kind {
		case PollKindPending:
			c.tel.RecordShadowMeasurement(OutcomePollPending, 0, 0, 0, 0, 0)
		case PollKindCompleted:
			c.tel.RecordShadowMeasurement(OutcomePollCompleted, 0, 0, 0, 0, 0)
		case PollKindFailed:
			c.tel.RecordShadowMeasurement(OutcomePollFailed, 0, 0, 0, 0, 0)
		case PollKindNotFound:
			c.tel.RecordShadowMeasurement(OutcomePollNotFound, 0, 0, 0, 0, 0)
		case PollKindUnavailable:
			c.tel.RecordShadowMeasurement(OutcomePollUnavailable, 0, 0, 0, 0, 0)
		case PollKindPollError:
			c.tel.RecordShadowMeasurement(OutcomePollError, 0, 0, 0, 0, 0)
		case PollKindNoPending:
			// No poll state occurred; implicit original fallback needs no taxonomy sample.
		}
	}
	adoption := handlePollAndGuardRaw(ctx, c.cfg, c.cs, c.svc, c.tel, pollRes, call)
	if c.adoptionStage != nil {
		adoption = c.adoptionStage(ctx, adoption)
	}
	if c.tel != nil && c.cfg.Compression.Enabled && c.cfg.Compression.Mode == CompressionShadow {
		if pollRes.Kind != PollKindNoPending && pollRes.Kind != "" {
			c.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
		} else if adoption.Outcome != AdoptionOutcomeNone {
			c.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
		}
	}
	viewDecisions := selectReasoningViews(ctx, c.cfg.Compression, c.cs, c.svc, partition, candidates, meta.ReplaySupport, meta)
	var ephemeralCandidates []restoreCandidate
	if c.cfg.Compression.Enabled && c.cfg.Compression.Mode == CompressionActive {
		getSur := func(id string) (*ReasoningSurrogate, bool) {
			st, ok, err := c.cs.GetCompressionState(ctx, partition, id)
			if err != nil || !ok || st.Surrogate == nil {
				return nil, false
			}
			return st.Surrogate, true
		}
		ephemeralCandidates = BuildEphemeralCandidates(candidates, viewDecisions, getSur)
	}
	restoreCandidates := candidates
	if ephemeralCandidates != nil {
		restoreCandidates = ephemeralCandidates
	}
	var resTmp RestoreResult
	var rerr error
	if c.cfg.Action == ActionObserve {
		resTmp = RestoreResult{Outcomes: outcomesFromClassifications(classified, nil)}
	} else if c.cfg.Action != ActionRestore {
		return compressionAttemptResult{}, fmt.Errorf("%s: unknown action %q", ID, c.cfg.Action)
	} else {
		resTmp, rerr = restoreWithCandidates(RestoreInput{
			Action:            c.cfg.Action,
			OnUnrepresentable: effectiveOnUnrepresentable(c.cfg, meta),
			OnStateError:      c.cfg.OnStateError,
			Call:              call,
			Artifacts:         arts,
			ReplaySupport:     meta.ReplaySupport,
			Eligible:          true,
		}, classified, restoreCandidates)
		if rerr != nil {
			return compressionAttemptResult{}, rerr
		}
	}
	if c.tel != nil && c.cfg.Compression.Mode == CompressionActive {
		used := false
		if len(viewDecisions) > 0 && resTmp.Mutated && resTmp.RestoredCount > 0 {
			for _, dec := range viewDecisions {
				if dec.Kind == ViewSurrogate {
					used = true
					break
				}
			}
		}
		if used {
			c.tel.RecordCompressionMeasurement(OutcomeActiveUsed, 0, 0, 0)
		} else {
			if len(viewDecisions) > 0 {
				c.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
			} else if pollRes.Kind != PollKindNoPending && pollRes.Kind != "" {
				c.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
			} else if adoption.Outcome != AdoptionOutcomeNone {
				c.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
			}
		}
	}
	return compressionAttemptResult{call: call, res: resTmp}, nil
}

func (t *AttemptTransform) HandleAttempt(ctx context.Context, call *aipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if call == nil {
		return request.AttemptDecision{}, fmt.Errorf("%s: call is required", ID)
	}
	if t.companion.BeforeMatch != nil {
		t.companion.BeforeMatch(call, meta)
	}
	if t.store == nil {
		return request.AttemptDecision{}, fmt.Errorf("%s: store is required", ID)
	}
	match, err := ResolveMatch(t.cfg, CandidateIdentity{
		BackendID:       meta.BackendID,
		BackendPrefixes: meta.BackendPrefixes,
		Model:           meta.Model,
	})
	if err != nil {
		return t.stateErrorDecision()
	}
	if !MatchEligible(match.Kind) {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	partition, ok := sessionPartitionOrMiss(meta.Session.AuthoritativeSessionID)
	if !ok {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	arts, err := t.store.Snapshot(ctx, partition)
	if err != nil {
		return t.stateErrorDecision()
	}
	if t.cfg.Compression.Enabled && (t.cfg.Compression.Mode == CompressionShadow || t.cfg.Compression.Mode == CompressionActive) {
		if cs, ok := t.store.(CompressionStore); ok {
			ca := compressionAttempt{
				cfg:           t.cfg,
				cs:            cs,
				svc:           t.svc,
				tel:           t.tel,
				adoptionStage: t.adoptionStage,
			}
			result, err := ca.run(ctx, call, meta, arts, partition)
			if err != nil {
				return request.AttemptDecision{}, err
			}
			return t.finalizeAttempt(ctx, result.call, meta, match, result.res)
		}
	}
	res, err := RestoreMissingReasoning(RestoreInput{
		Action:            t.cfg.Action,
		OnUnrepresentable: effectiveOnUnrepresentable(t.cfg, meta),
		OnStateError:      t.cfg.OnStateError,
		Call:              call,
		Artifacts:         arts,
		ReplaySupport:     meta.ReplaySupport,
		Eligible:          true,
	})
	if err != nil {
		return request.AttemptDecision{}, err
	}
	return t.finalizeAttempt(ctx, call, meta, match, res)
}

func (t *AttemptTransform) finalizeAttempt(ctx context.Context, call *aipapi.Call, meta request.AttemptMeta, match MatchResult, res RestoreResult) (request.AttemptDecision, error) {
	t.recordRestoreOutcome(res)
	if res.Exclude {
		reason := res.ReasonCode
		if reason == "" {
			reason = "unrepresentable_replay"
		}
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: reason}, nil
	}
	if t.companion.AfterRestore != nil {
		t.companion.AfterRestore(ctx, call, meta, match, res)
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func (t *AttemptTransform) recordRestoreOutcome(res RestoreResult) {
	if t == nil || t.tel == nil {
		return
	}
	if len(res.Outcomes) > 0 {
		for _, o := range res.Outcomes {
			counts := map[string]int{"count": 1}
			if o == OutcomeRestored {
				counts = map[string]int{"restored": max(1, res.RestoredCount), "bytes": res.RestoredBytes}
			}
			t.tel.Record(o, counts)
		}
		return
	}
	switch {
	case res.Exclude && res.ReasonCode == "state_error":
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	case res.Exclude && res.ReasonCode == "unrepresentable_replay":
		t.tel.Record(OutcomeUnrepresentable, map[string]int{"count": 1})
	case res.Mutated && res.RestoredCount > 0:
		t.tel.Record(OutcomeRestored, map[string]int{"restored": res.RestoredCount, "bytes": res.RestoredBytes})
	case res.ReasonCode == "state_error":
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	case res.ReasonCode == "unrepresentable":
		t.tel.Record(OutcomeUnrepresentable, map[string]int{"count": 1})
	}
}

func (t *AttemptTransform) stateErrorDecision() (request.AttemptDecision, error) {
	if t.tel != nil {
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	}
	if t.cfg.Action == ActionObserve {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	if t.cfg.OnStateError == PolicyReject {
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "state_error"}, nil
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// effectiveOnUnrepresentable unifies proxy-restored and client-sent reasoning
// under one per-candidate rule. When the attempt policy is log_skip,
// unrepresentable restores are withheld (continue without restore) instead of
// excluding the candidate; the executor strips client-sent reasoning for the
// same attempt and records the single downgrade. Under reject (or unset) the
// feature's on_unrepresentable keeps its existing scope and behaviour.
func effectiveOnUnrepresentable(cfg Config, meta request.AttemptMeta) string {
	if meta.AttemptPolicy == aipapi.AttemptLogSkip {
		return PolicyLogSkip
	}
	return cfg.OnUnrepresentable
}

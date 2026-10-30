package runtimehost

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

type reloadStateInitial struct {
	ActiveEffective   *config.EffectiveConfig
	ActiveSource      *configsource.ActiveSourceVersion
	ActiveSourceOwner *configsource.SourceOwnerSlot
	InitialResult     sdkreload.Result
	ModelGeneration   string
	HistoryCapacity   int
}

type reloadTerminalMeta struct {
	Trigger    sdkreload.Trigger
	Duration   time.Duration
	RecordedAt time.Time
}

type reloadStatusInput struct {
	ActiveGeneration    int64
	Busy, PendingSignal bool
	CoalescedSignals    int64
	RetainedGenerations int
	RetentionPressure   bool
}

// ReloadState owns active effective/source snapshot, last result/success/failure,
// source-integrity posture, model-generation fingerprint, bounded history, and
// canonical status composition (req 6.3-6.4, 7.1-7.8).
type ReloadState struct {
	mu                             sync.Mutex
	activeEff                      *config.EffectiveConfig
	activeSource                   *configsource.ActiveSourceVersion
	activeSourceOwner              *configsource.SourceOwnerSlot
	last, lastSuccess, lastFailure sdkreload.Result
	sourcePosture, modelGen        string
	historyCap                     int
	history                        []sdkreload.HistoryEntry
	shutdownSourceWaiter           func(context.Context) error
}

func newReloadState(in reloadStateInitial) *ReloadState {
	capacity := in.HistoryCapacity
	if capacity <= 0 {
		capacity = configreload.DefaultStatusHistoryCap
	}
	s := &ReloadState{
		activeEff: in.ActiveEffective, activeSource: cloneActiveSource(in.ActiveSource),
		activeSourceOwner: in.ActiveSourceOwner,
		sourcePosture:     "ok", modelGen: in.ModelGeneration, historyCap: capacity,
	}
	if s.activeSourceOwner == nil {
		s.activeSourceOwner = configsource.NewSourceOwnerSlot(nil)
	}
	if in.InitialResult.Category != "" {
		s.last = in.InitialResult.Clone()
		s.lastSuccess = s.last
	}
	return s
}

func cloneActiveSource(in *configsource.ActiveSourceVersion) *configsource.ActiveSourceVersion {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func (s *ReloadState) ActiveInput(trigger sdkreload.Trigger, attemptID, activeGeneration int64) attemptInput {
	if s == nil {
		return attemptInput{Trigger: trigger, AttemptID: attemptID, ActiveGeneration: activeGeneration}
	}
	s.mu.Lock()
	eff, src := s.activeEff, cloneActiveSource(s.activeSource)
	s.mu.Unlock()
	return attemptInput{
		Trigger: trigger, AttemptID: attemptID, ActiveGeneration: activeGeneration,
		ActiveEffective: eff, ActiveSource: src,
	}
}

// Apply applies one completed attempt outcome and terminal metadata. Busy or
// empty-category results return a defensive clone without mutating state.
func (s *ReloadState) Apply(outcome attemptOutcome, meta reloadTerminalMeta) sdkreload.Result {
	if s == nil {
		return outcome.Result.Clone()
	}
	res := outcome.Result.Clone()
	if res.Category == sdkreload.ResultBusy || res.Category == "" {
		return res
	}
	res.Category = sdkreload.NormalizeResultCategory(res.Category)
	committedAdoption := outcome.AdoptionReceipt != nil && outcome.AdoptionReceipt.confirms(s, outcome)
	if outcome.AdoptionReceipt != nil && !committedAdoption {
		res.Category = sdkreload.ResultInternalFailed
		res.ReasonCategory = configreload.StagePrepare
		return res
	}
	if outcome.SourceUpdate != nil && (res.Category == sdkreload.ResultPublished || res.Category == sdkreload.ResultNoop) {
		// A second delivery of an already adopted Linux effective-noop outcome
		// has no owner left to move. Treat it as an idempotent replay before
		// validating the now-empty handoff slot.
		if !committedAdoption {
			s.mu.Lock()
			replay := res.Category == sdkreload.ResultNoop && outcome.SourceUpdate.RequiresLease() &&
				sameActiveSource(s.activeSource, outcome.SourceUpdate) && s.activeSourceOwner.ValidFor(s.activeSource) && outcome.SourceOwnerSlot != nil && outcome.SourceOwnerSlot.IsEmpty()
			prior := s.last.Clone()
			s.mu.Unlock()
			if replay {
				return prior
			}
			if !outcome.SourceOwnerSlot.ValidFor(outcome.SourceUpdate) {
				res.Category = sdkreload.ResultInternalFailed
				res.ReasonCategory = configreload.StagePrepare
				return res
			}
		}
	}
	recordedAt := meta.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}
	var displaced *configsource.SourceLeaseOwner
	s.mu.Lock()
	if outcome.SourceUpdate != nil && !committedAdoption && (res.Category == sdkreload.ResultPublished || res.Category == sdkreload.ResultNoop) {
		var moved bool
		displaced, moved = outcome.SourceOwnerSlot.MoveMatchingTo(s.activeSourceOwner, outcome.SourceUpdate)
		if !moved {
			s.mu.Unlock()
			res.Category = sdkreload.ResultInternalFailed
			res.ReasonCategory = configreload.StagePrepare
			return res
		}
	}
	s.last = res
	switch res.Category {
	case sdkreload.ResultPublished:
		s.lastSuccess, s.sourcePosture = res, "ok"
		if outcome.EffectiveUpdate != nil && !committedAdoption {
			s.activeEff = outcome.EffectiveUpdate
			if fp := outcome.EffectiveUpdate.Identity.PublicFingerprint; fp != "" {
				s.modelGen = fp
			}
		}
		if outcome.SourceUpdate != nil && !committedAdoption {
			s.activeSource = cloneActiveSource(outcome.SourceUpdate)
		}
	case sdkreload.ResultNoop:
		s.lastFailure, s.sourcePosture = res, "ok" // includes source-baseline no-op (req 2.9)
		if outcome.SourceUpdate != nil {
			s.activeSource = cloneActiveSource(outcome.SourceUpdate)
		}
	case sdkreload.ResultSourceIntegrity:
		s.lastFailure, s.sourcePosture = res, "failed"
	default:
		s.lastFailure = res
	}
	s.appendHistoryLocked(res, meta.Trigger, meta.Duration, recordedAt)
	s.mu.Unlock()
	if displaced != nil {
		_ = displaced.RequestClose()
	}
	return res.Clone()
}

func sameActiveSource(a, b *configsource.ActiveSourceVersion) bool {
	return a != nil && b != nil && *a == *b
}

// adoptPublishedSource installs the already validated source/effective pair
// immediately after Manager.Publish commits. The source and owner are moved
// under the same state lock; the returned receipt can close only the displaced
// prior owner.
func (s *ReloadState) adoptPublishedSource(effective *config.EffectiveConfig, source *configsource.ActiveSourceVersion, ownerSlot *configsource.SourceOwnerSlot, published sdkreload.Result, receipt *sourceAdoptionReceipt) {
	if s == nil || source == nil || ownerSlot == nil || receipt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt.adopted {
		return
	}
	if ownerSlot.IsEmpty() {
		receipt.displaced = s.activeSourceOwner.Take()
	} else {
		receipt.displaced = ownerSlot.MoveTo(s.activeSourceOwner)
	}
	receipt.state, receipt.source, receipt.effective, receipt.published = s, *source, effective, published
	receipt.adopted = true
	s.activeSource, s.activeEff = source, effective
	if effective != nil && effective.Identity.PublicFingerprint != "" {
		s.modelGen = effective.Identity.PublicFingerprint
	}
	s.last, s.lastSuccess, s.sourcePosture = published, published, "ok"
}

func (s *ReloadState) requestCloseActiveSource() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	slot := s.activeSourceOwner
	s.activeSourceOwner = configsource.NewSourceOwnerSlot(nil)
	s.mu.Unlock()
	owner := slot.Take()
	if owner == nil {
		return nil
	}
	_ = owner.RequestClose()
	s.mu.Lock()
	s.shutdownSourceWaiter = owner.WaitClosed
	s.mu.Unlock()
	return nil
}

// closeActiveSource is retained as the shutdown finalizer surface: it detaches
// the owner and requests close without waiting for outstanding borrows.
func (s *ReloadState) closeActiveSource(context.Context) error {
	return s.requestCloseActiveSource()
}

func (s *ReloadState) waitForSourceClose(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	waiter := s.shutdownSourceWaiter
	s.mu.Unlock()
	if waiter == nil {
		return nil
	}
	return waiter(ctx)
}

type sourceAdoptionReceipt struct {
	once       sync.Once
	displaced  *configsource.SourceLeaseOwner
	adopted    bool
	state      *ReloadState
	source     configsource.ActiveSourceVersion
	effective  *config.EffectiveConfig
	published  sdkreload.Result
	cleanupErr error
}

// confirms binds a receipt to its original commit, even after a later commit
// replaced that baseline. A stale receipt may record history but cannot adopt.
func (r *sourceAdoptionReceipt) confirms(state *ReloadState, out attemptOutcome) bool {
	return r != nil && r.adopted && r.state == state && out.SourceUpdate != nil &&
		r.source == *out.SourceUpdate && r.effective == out.EffectiveUpdate && out.SourceOwnerSlot == nil &&
		r.published.Category == sdkreload.ResultPublished && out.Result.Category == sdkreload.ResultPublished &&
		r.published.AttemptID == out.Result.AttemptID && r.published.ActiveGeneration == out.Result.ActiveGeneration &&
		r.published.PreviousGeneration == out.Result.PreviousGeneration && r.published.ReasonCategory == out.Result.ReasonCategory
}

func (r *sourceAdoptionReceipt) closeDisplaced(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.displaced != nil {
			r.cleanupErr = r.displaced.RequestClose()
			r.displaced = nil
		}
	})
	return r.cleanupErr
}

func (s *ReloadState) appendHistoryLocked(res sdkreload.Result, trigger sdkreload.Trigger, d time.Duration, recordedAt time.Time) {
	if s.historyCap <= 0 {
		return
	}
	stage := res.ReasonCategory
	if stage == "" {
		stage = string(res.Category)
	}
	entry := sdkreload.HistoryEntry{
		AttemptID: res.AttemptID, Trigger: trigger.Kind, Stage: boundStageName(stage),
		Category: res.Category, ActiveGeneration: res.ActiveGeneration,
		CandidateGeneration: candidateGeneration(res), DurationMs: d.Milliseconds(),
		RestartFieldCount: res.RestartFieldCount, ReasonCategory: sanitizeHistoryReason(res.ReasonCategory),
		SafeActor: sanitizeHistoryActor(trigger.SafeActor), RecordedAt: recordedAt,
	}
	if len(s.history) < s.historyCap {
		s.history = append(s.history, entry)
		return
	}
	copy(s.history, s.history[1:])
	s.history[len(s.history)-1] = entry
}

func (s *ReloadState) Snapshot(in reloadStatusInput) sdkreload.Status {
	if s == nil {
		return sdkreload.Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var current *sdkreload.Result
	if in.Busy {
		cur := s.last
		current = &cur
	}
	controlDegraded := s.lastFailure.Category != "" &&
		s.lastFailure.Category != sdkreload.ResultPublished &&
		s.lastFailure.Category != sdkreload.ResultNoop
	posture := s.sourcePosture
	if posture == "" {
		posture = "unknown"
	}
	return sdkreload.Status{
		ActiveGeneration: in.ActiveGeneration, CurrentAttempt: current,
		LastResult: s.last, LastSuccess: s.lastSuccess, LastFailure: s.lastFailure,
		SourceIntegrity: posture, RetainedGenerations: in.RetainedGenerations,
		RetentionPressure: in.RetentionPressure, ControlDegraded: controlDegraded,
		ModelGeneration: s.modelGen, History: s.history, Busy: in.Busy,
		PendingSignal: in.PendingSignal, CoalescedSignals: in.CoalescedSignals,
	}.Clone()
}

func sanitizeHistoryActor(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	if looksLikeSecretText(s) {
		return configreload.RedactedPlaceholder
	}
	return s
}

func sanitizeHistoryReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if looksLikeSecretText(s) {
		return "other"
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func looksLikeSecretText(s string) bool {
	low := strings.ToLower(s)
	for _, sub := range []string{"password", "secret", "api_key", "apikey", "token", "bearer", "sk-"} {
		if strings.Contains(low, sub) {
			return true
		}
	}
	return false
}

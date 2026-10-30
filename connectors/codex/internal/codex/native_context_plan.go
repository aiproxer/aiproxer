package codex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
)

const (
	defaultContextFractionNumerator   = int64(1)
	defaultContextFractionDenominator = int64(3)
	defaultSafetyHeadroom             = int64(4096)
	defaultHardLimit                  = int64(128000)
	sparkHardLimit                    = int64(128000)
	sparkUsableCeiling                = int64(96000)
	sparkTrigger                      = int64(80000)
	sparkHeadroom                     = int64(32000)
	gpt5UsableCeiling                 = int64(250000)
	gpt5Trigger                       = int64(220000)
	gpt5Headroom                      = int64(30000)
	opaqueBytesPerEstimatedToken      = int64(4)
)

// CompactionModelProfile is connector-private metadata used by native context
// planning. It intentionally does not widen modelinventory.Model.
type CompactionModelProfile struct {
	ModelSlug             string
	ContextWindow         int64
	MaxContextWindow      int64
	AutoCompactTokenLimit int64
	CompHash              string
	DefaultReasoning      string
	SupportedReasoning    []string
	HardLimit             int64
	TriggerTokens         int64
	SafetyHeadroom        int64
	UsableContextCeiling  int64
	BudgetPolicyName      string
}

// ResolveCompactionModelProfile converts the Codex catalog profile into a safe
// planning profile. Missing or malformed catalog values are treated as absent.
func ResolveCompactionModelProfile(cat *catalog.Catalog, model string, cfg NativeCompactionConfig) (CompactionModelProfile, error) {
	model = strings.TrimSpace(model)
	p := CompactionModelProfile{ModelSlug: model, SafetyHeadroom: defaultSafetyHeadroom, BudgetPolicyName: "CodexHarnessHeadroomV1"}
	if cat != nil {
		if discovered, ok := cat.Profile(model); ok {
			p.ContextWindow = int64(discovered.ContextWindow)
			p.MaxContextWindow = int64(discovered.MaxContextWindow)
			p.AutoCompactTokenLimit = discovered.AutoCompactTokenLimit
			p.CompHash = discovered.CompHash
			p.DefaultReasoning = discovered.DefaultReasoningLevel
			p.SupportedReasoning = append([]string(nil), discovered.SupportedReasoningLevels...)
		}
	}
	knownHardLimit := p.MaxContextWindow
	if knownHardLimit <= 0 {
		knownHardLimit = p.ContextWindow
	}
	isSpark := strings.EqualFold(model, "gpt-5.3-codex-spark")
	isGPT5 := strings.HasPrefix(strings.ToLower(model), "gpt-5.")

	applyModelFamilyLimits(&p, isSpark, isGPT5, knownHardLimit)
	resolveProfileTrigger(&p, cfg, isSpark, isGPT5)
	if err := validateProfileLimits(&p, cfg); err != nil {
		return CompactionModelProfile{}, err
	}
	return p, nil
}

func applyModelFamilyLimits(p *CompactionModelProfile, isSpark, isGPT5 bool, knownHardLimit int64) {
	switch {
	case isSpark:
		p.HardLimit, p.UsableContextCeiling, p.SafetyHeadroom = sparkHardLimit, sparkUsableCeiling, sparkHeadroom
		if knownHardLimit > 0 && knownHardLimit < p.HardLimit {
			p.HardLimit = knownHardLimit
			p.UsableContextCeiling = max(int64(1), p.HardLimit-p.SafetyHeadroom)
		}
	case isGPT5 && knownHardLimit <= 0:
		p.UsableContextCeiling, p.SafetyHeadroom = gpt5UsableCeiling, gpt5Headroom
		p.HardLimit = gpt5UsableCeiling
	default:
		p.HardLimit = knownHardLimit
	}
	if p.HardLimit <= 0 {
		p.HardLimit = defaultHardLimit
	}
	if p.UsableContextCeiling <= 0 {
		p.UsableContextCeiling = p.HardLimit - p.SafetyHeadroom
	}
	if isGPT5 && !isSpark && knownHardLimit > 0 {
		p.SafetyHeadroom = gpt5Headroom
		p.UsableContextCeiling = p.HardLimit - p.SafetyHeadroom
	}
	if p.UsableContextCeiling > p.HardLimit {
		p.UsableContextCeiling = p.HardLimit
	}
	if p.ContextWindow <= 0 || p.ContextWindow > p.HardLimit {
		p.ContextWindow = p.HardLimit
	}
}

func resolveProfileTrigger(p *CompactionModelProfile, cfg NativeCompactionConfig, isSpark, isGPT5 bool) {
	if cfg.TriggerTokens > 0 {
		p.TriggerTokens = cfg.TriggerTokens
	} else if isSpark && p.AutoCompactTokenLimit == 0 {
		p.TriggerTokens = sparkTrigger
	} else if isGPT5 && p.AutoCompactTokenLimit == 0 {
		p.TriggerTokens = gpt5Trigger
	} else if p.AutoCompactTokenLimit > 0 {
		p.TriggerTokens = p.AutoCompactTokenLimit
		if isSpark && p.TriggerTokens > sparkTrigger {
			p.TriggerTokens = sparkTrigger
		}
		if p.TriggerTokens >= p.UsableContextCeiling {
			p.TriggerTokens = fallbackTriggerForProfile(isSpark, isGPT5, p.UsableContextCeiling)
		}
	} else {
		p.TriggerTokens = p.ContextWindow * defaultContextFractionNumerator / defaultContextFractionDenominator
	}
}

func validateProfileLimits(p *CompactionModelProfile, cfg NativeCompactionConfig) error {
	maximumTrigger := p.UsableContextCeiling - 1
	if maximumTrigger <= 0 || p.HardLimit <= p.SafetyHeadroom {
		return fmt.Errorf("profile_invalid: hard limit cannot leave retained and safety headroom")
	}
	if p.TriggerTokens > maximumTrigger {
		if cfg.TriggerTokens <= 0 {
			p.TriggerTokens = maximumTrigger
		} else {
			return fmt.Errorf("profile_invalid: trigger exceeds hard limit after retained and safety headroom")
		}
	}
	if p.TriggerTokens <= cfg.RetainedMessageTokens || p.TriggerTokens <= 0 || p.TriggerTokens >= p.HardLimit {
		return fmt.Errorf("profile_invalid: trigger must be positive and below hard limit")
	}
	return nil
}

func fallbackTriggerForProfile(spark, gpt5 bool, usable int64) int64 {
	trigger := usable * defaultContextFractionNumerator / defaultContextFractionDenominator
	if spark {
		trigger = sparkTrigger
	} else if gpt5 {
		trigger = gpt5Trigger
	}
	if trigger >= usable {
		trigger = usable - 1
	}
	return trigger
}

func profilesCompatibleForReplay(want, have CompactionModelProfile) bool {
	return strings.TrimSpace(want.ModelSlug) == strings.TrimSpace(have.ModelSlug) &&
		strings.TrimSpace(want.ModelSlug) != "" && strings.TrimSpace(have.ModelSlug) != "" &&
		compHashCompatible(want, have)
}

func compHashCompatible(want, have CompactionModelProfile) bool {
	wantHash, haveHash := strings.TrimSpace(want.CompHash), strings.TrimSpace(have.CompHash)
	return wantHash == "" || haveHash == "" || wantHash == haveHash
}

// CompactionEstimate is the only estimator input. Opaque items use recorded
// metadata or conservative byte estimates; their ciphertext is never tokenized.
type CompactionEstimate struct {
	Tokens         int64
	OpaqueBytes    int64
	MetadataTokens int64
	OpaqueTokens   int64
}

type NativeHistoryEstimator interface {
	Estimate(context.Context, NativeHistory) (CompactionEstimate, error)
}

type deterministicHistoryEstimator struct{}

func (deterministicHistoryEstimator) Estimate(_ context.Context, history NativeHistory) (CompactionEstimate, error) {
	var result CompactionEstimate
	for _, item := range history.Items {
		if _, ok := item.(opaqueResponseItem); ok {
			return CompactionEstimate{}, fmt.Errorf("opaque item reached ordinary estimator")
		}
		body, err := nativeItemJSON(item)
		if err != nil {
			return CompactionEstimate{}, err
		}
		// This is deliberately a conservative shape estimate for ordinary input.
		result.Tokens += int64((len(body) + 3) / 4)
	}
	return result, nil
}

type CheckpointView struct {
	Model                string
	CompHash             string
	SourcePrefixFP       []string
	Replacement          []inputItem
	Expired              bool
	OpaqueMetadataTokens []int64
	CompactionUsage      *NativeUsageEvidence
}

type CompactionPlanInput struct {
	Context        context.Context
	History        NativeHistory
	Profile        CompactionModelProfile
	Config         NativeContextConfig
	MarkerEligible bool
	Checkpoint     *CheckpointView
	InFlight       bool
	Estimator      NativeHistoryEstimator
	FullEstimate   *CompactionEstimate
}

type CompactionDecisionKind string

const (
	DecisionBypass      CompactionDecisionKind = "bypass"
	DecisionReuse       CompactionDecisionKind = "reuse"
	DecisionCreate      CompactionDecisionKind = "create"
	DecisionHardFailure CompactionDecisionKind = "hard_failure"
)

type CompactionPlan struct {
	Kind               CompactionDecisionKind
	Reason             string
	PrefixEnd          int
	LiveTailStart      int
	EffectiveTokens    int64
	ExpectedSavings    int64
	ExistingCheckpoint *CheckpointView
	SourcePrefixEnd    int
	SourcePrefixFP     []string
	EffectiveHistory   NativeHistory
}

func PlanCompaction(in CompactionPlanInput) CompactionPlan {
	bypass := func(reason string) CompactionPlan { return CompactionPlan{Kind: DecisionBypass, Reason: reason} }
	if in.Context == nil {
		in.Context = context.Background()
	}
	if !in.Config.Enabled || !in.Config.Compaction.Enabled {
		return bypass("disabled")
	}
	if in.Config.ReasoningContinuity == ContinuityRequired && !in.MarkerEligible {
		return bypass("continuity_not_eligible")
	}
	if !validPlanningProfile(in.Profile, in.Config.Compaction) {
		return hardFailure("profile_invalid")
	}
	if in.InFlight {
		return bypass("compaction_in_flight")
	}
	if in.Estimator == nil {
		in.Estimator = deterministicHistoryEstimator{}
	}

	effectiveHistory, sourcePrefixEnd, checkpointValid := effectiveHistory(in)
	if checkpointValid {
		effectiveEstimate, err := estimateHistory(in.Context, in.Estimator, effectiveHistory)
		if err != nil {
			return estimateFailure(err)
		}
		if effectiveEstimate.Tokens < in.Profile.TriggerTokens {
			return CompactionPlan{Kind: DecisionReuse, Reason: "checkpoint_reuse", EffectiveTokens: effectiveEstimate.Tokens, ExistingCheckpoint: in.Checkpoint, SourcePrefixEnd: sourcePrefixEnd, SourcePrefixFP: append([]string(nil), effectiveHistory.Fingerprints...), EffectiveHistory: effectiveHistory}
		}
	}
	if !checkpointValid {
		effectiveHistory = in.History
		sourcePrefixEnd = 0
	}
	var full CompactionEstimate
	var err error
	if sourcePrefixEnd == 0 && in.FullEstimate != nil {
		full = *in.FullEstimate
	} else {
		full, err = estimateHistory(in.Context, in.Estimator, effectiveHistory)
		if err != nil {
			return estimateFailure(err)
		}
	}
	if full.Tokens < in.Profile.TriggerTokens {
		return CompactionPlan{Kind: DecisionBypass, Reason: "below_trigger", EffectiveTokens: full.Tokens, SourcePrefixEnd: sourcePrefixEnd, EffectiveHistory: effectiveHistory}
	}
	liveTail := latestUserTail(effectiveHistory)
	if liveTail <= 0 || liveTail >= len(effectiveHistory.Items) {
		if full.Tokens >= in.Profile.HardLimit {
			return hardFailureReason("context_at_hard_limit", full.Tokens, effectiveHistory, sourcePrefixEnd)
		}
		return bypassPlan("no_safe_split", full.Tokens, effectiveHistory, sourcePrefixEnd)
	}
	split := latestSafeSplitBefore(effectiveHistory, liveTail)
	if !compactionPrefixExcludesLatestUserTail(effectiveHistory, split) {
		if full.Tokens >= in.Profile.HardLimit {
			return hardFailureReason("no_safe_split", full.Tokens, effectiveHistory, sourcePrefixEnd)
		}
		return bypassPlan("no_safe_split", full.Tokens, effectiveHistory, sourcePrefixEnd)
	}
	prefix := historySlice(effectiveHistory, 0, split)
	tail := historySlice(effectiveHistory, split, len(effectiveHistory.Items))
	prefixEstimate, prefixErr := estimateHistory(in.Context, in.Estimator, prefix)
	tailEstimate, tailErr := estimateHistory(in.Context, in.Estimator, tail)
	if prefixErr != nil || tailErr != nil {
		return hardFailureReason(estimateFailureReason(prefixErr, tailErr), full.Tokens, effectiveHistory, sourcePrefixEnd)
	}
	if tailEstimate.Tokens+in.Profile.SafetyHeadroom > in.Profile.HardLimit {
		return hardFailurePlan("live_tail_too_large", split, full.Tokens, effectiveHistory, sourcePrefixEnd)
	}
	retained := min(max(in.Config.Compaction.RetainedMessageTokens, 0), prefixEstimate.Tokens)
	savings := prefixEstimate.Tokens - retained
	if savings < in.Config.Compaction.MinSavingsTokens {
		if full.Tokens >= in.Profile.HardLimit {
			return hardFailurePlan("compaction_context_hard_failure", split, full.Tokens, effectiveHistory, sourcePrefixEnd)
		}
		return bypassPlan("insufficient_savings", full.Tokens, effectiveHistory, sourcePrefixEnd)
	}
	sourceEnd := sourcePrefixEndForSplit(effectiveHistory, in.History, in.Checkpoint, sourcePrefixEnd, split)
	return CompactionPlan{Kind: DecisionCreate, Reason: "threshold_crossed", PrefixEnd: split, LiveTailStart: split, EffectiveTokens: full.Tokens, ExpectedSavings: savings, SourcePrefixEnd: sourceEnd, SourcePrefixFP: append([]string(nil), in.History.Fingerprints[:sourceEnd]...), EffectiveHistory: effectiveHistory, ExistingCheckpoint: in.Checkpoint}
}

func validCheckpoint(checkpoint CheckpointView, profile CompactionModelProfile, history NativeHistory) bool {
	if checkpoint.Expired || !profilesCompatibleForReplay(profile, CompactionModelProfile{ModelSlug: checkpoint.Model, CompHash: checkpoint.CompHash}) {
		return false
	}
	if len(checkpoint.SourcePrefixFP) == 0 || len(checkpoint.SourcePrefixFP) > len(history.Fingerprints) {
		return false
	}
	for i, fp := range checkpoint.SourcePrefixFP {
		if history.Fingerprints[i] != fp {
			return false
		}
	}
	return true
}

func latestUserTail(history NativeHistory) int {
	for _, boundary := range slices.Backward(history.Boundaries) {
		if boundary.UserTurnStart {
			return boundary.ItemIndex
		}
	}
	return -1
}

func estimateHistory(ctx context.Context, estimator NativeHistoryEstimator, history NativeHistory) (CompactionEstimate, error) {
	if err := ctx.Err(); err != nil {
		return CompactionEstimate{}, err
	}
	normal := historyWithoutOpaque(history)
	result, err := estimator.Estimate(ctx, normal)
	if err != nil {
		return CompactionEstimate{}, err
	}
	opaqueBytes, opaqueTokens, metadataTokens, err := estimateOpaqueState(history)
	if err != nil {
		return CompactionEstimate{}, err
	}
	result.OpaqueBytes = opaqueBytes
	result.OpaqueTokens = opaqueTokens
	result.MetadataTokens = metadataTokens
	result.Tokens += opaqueTokens
	return result, nil
}

func estimateFailure(err error) CompactionPlan {
	return hardFailure(estimateFailureReason(err, nil))
}

func estimateFailureReason(errs ...error) string {
	for _, err := range errs {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "estimate_cancelled"
		}
	}
	return "estimate_failed"
}

func historyWithoutOpaque(history NativeHistory) NativeHistory {
	out := history
	out.Items = make([]inputItem, 0, len(history.Items))
	out.Fingerprints = make([]string, 0, len(history.Fingerprints))
	for i, item := range history.Items {
		if _, ok := item.(opaqueResponseItem); ok {
			continue
		}
		out.Items = append(out.Items, item)
		if i < len(history.Fingerprints) {
			out.Fingerprints = append(out.Fingerprints, history.Fingerprints[i])
		}
	}
	out.Boundaries = nativeTrajectoryBoundaries(out.Items)
	out.OpaqueMetadataTokens = nil
	return out
}

func estimateOpaqueState(history NativeHistory) (bytes, tokens, metadata int64, err error) {
	for i, item := range history.Items {
		opaque, ok := item.(opaqueResponseItem)
		if !ok {
			continue
		}
		body, err := nativeItemJSON(opaque)
		if err != nil {
			return 0, 0, 0, err
		}
		bytes += int64(len(body))
		if i < len(history.OpaqueMetadataTokens) {
			metadata += history.OpaqueMetadataTokens[i]
			tokens += history.OpaqueMetadataTokens[i]
			continue
		}
		// Opaque bytes are not tokenized; this is only a conservative size
		// estimate used when the provider supplied no token metadata.
		tokens += (int64(len(body)) + opaqueBytesPerEstimatedToken - 1) / opaqueBytesPerEstimatedToken
	}
	return bytes, tokens, metadata, nil
}

func effectiveHistory(in CompactionPlanInput) (NativeHistory, int, bool) {
	if in.Checkpoint == nil || !validCheckpoint(*in.Checkpoint, in.Profile, in.History) {
		return NativeHistory{}, 0, false
	}
	prefixLen := len(in.Checkpoint.SourcePrefixFP)
	items := append(cloneInputItems(in.Checkpoint.Replacement), in.History.Items[prefixLen:]...)
	metadata := append([]int64(nil), in.Checkpoint.OpaqueMetadataTokens...)
	metadata = append(metadata, metadataSlice(in.History.OpaqueMetadataTokens, prefixLen, len(in.History.Items))...)
	result := NativeHistory{Items: items, OpaqueMetadataTokens: metadata}
	result.Fingerprints = make([]string, 0, len(items))
	for _, item := range items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			return NativeHistory{}, 0, false
		}
		result.Fingerprints = append(result.Fingerprints, fp)
	}
	result.Boundaries = nativeTrajectoryBoundaries(items)
	return result, prefixLen, true
}

func historySlice(history NativeHistory, start, end int) NativeHistory {
	result := NativeHistory{Items: cloneInputItems(history.Items[start:end]), Fingerprints: append([]string(nil), history.Fingerprints[start:end]...)}
	result.Boundaries = nativeTrajectoryBoundaries(result.Items)
	result.OpaqueMetadataTokens = metadataSlice(history.OpaqueMetadataTokens, start, end)
	return result
}

func metadataSlice(metadata []int64, start, end int) []int64 {
	if start < 0 || end < start || start > len(metadata) {
		return nil
	}
	if end > len(metadata) {
		end = len(metadata)
	}
	return append([]int64(nil), metadata[start:end]...)
}

func validPlanningProfile(profile CompactionModelProfile, cfg NativeCompactionConfig) bool {
	if profile.HardLimit <= 0 || profile.TriggerTokens <= 0 || profile.TriggerTokens >= profile.HardLimit || profile.SafetyHeadroom < 0 {
		return false
	}
	usable := profile.UsableContextCeiling
	if usable <= 0 || usable > profile.HardLimit {
		usable = profile.HardLimit - profile.SafetyHeadroom
	}
	if usable <= 0 || profile.TriggerTokens >= usable {
		return false
	}
	// Retention is an output-window constraint, not a subtraction from the
	// pre-compaction trigger. The trigger must leave the named usable ceiling;
	// the retained window is checked separately when savings are calculated.
	return cfg.RetainedMessageTokens >= 0 && cfg.RetainedMessageTokens < usable
}

func sourcePrefixEndForSplit(effective, original NativeHistory, checkpoint *CheckpointView, checkpointSourceEnd, split int) int {
	if checkpoint == nil || checkpointSourceEnd == 0 {
		return split
	}
	replacementLen := len(checkpoint.Replacement)
	if split <= replacementLen {
		return checkpointSourceEnd
	}
	return min(len(original.Items), checkpointSourceEnd+split-replacementLen)
}

func bypassPlan(reason string, tokens int64, history NativeHistory, sourcePrefixEnd int) CompactionPlan {
	return CompactionPlan{Kind: DecisionBypass, Reason: reason, EffectiveTokens: tokens, EffectiveHistory: history, SourcePrefixEnd: sourcePrefixEnd}
}

func hardFailure(reason string) CompactionPlan {
	return CompactionPlan{Kind: DecisionHardFailure, Reason: reason}
}

func hardFailurePlan(reason string, split int, tokens int64, history NativeHistory, sourcePrefixEnd int) CompactionPlan {
	return CompactionPlan{Kind: DecisionHardFailure, Reason: reason, PrefixEnd: split, LiveTailStart: split, EffectiveTokens: tokens, EffectiveHistory: history, SourcePrefixEnd: sourcePrefixEnd}
}

func hardFailureReason(reason string, tokens int64, history NativeHistory, sourcePrefixEnd int) CompactionPlan {
	return CompactionPlan{Kind: DecisionHardFailure, Reason: reason, EffectiveTokens: tokens, EffectiveHistory: history, SourcePrefixEnd: sourcePrefixEnd}
}

func latestSafeSplitBefore(history NativeHistory, before int) int {
	for i := before; i > 0; i-- {
		if i < len(history.Boundaries) && history.Boundaries[i].PairSafe {
			return i
		}
	}
	return 0
}

// compactionPrefixExcludesLatestUserTail is the planner/coordinator boundary
// invariant. The split index is the first item retained in the live request;
// it may equal the latest user's boundary, but it must never be after it.
func compactionPrefixExcludesLatestUserTail(history NativeHistory, split int) bool {
	liveTail := latestUserTail(history)
	return split > 0 && split < len(history.Items) && liveTail > 0 && split <= liveTail &&
		split < len(history.Boundaries) && history.Boundaries[split].PairSafe
}

package telemetry

// This file is requirement 7.6's counter surface and the arithmetic behind it.
//
// The counters requirement 7.6 names are eligible occurrences, rewritten occurrences,
// skipped occurrences by bounded reason, the byte accounting - bytes-before, bytes-after,
// bytes-saved - and the two failure counters, expansion failures and mandatory-buffer
// overflows. Every one of them is an integer here, and every dimension they break down by
// is a closed enum projected through its own String method.
//
// THE SAVING ARITHMETIC is the part that can be wrong in a way that flatters the feature,
// so it is stated once, here, and implemented once.
//
// Both directions replace a DECODED string value with another decoded string value, and
// the shared engine measures decoded-value lengths in both directions ([rewrite.Stats]).
// It measures DECODED lengths rather than raw wire lengths on purpose: the figure answers
// "how many bytes of value did the model stop seeing", and a client that spelled a path
// with escapes has the same value either way.
//
//	OUTBOUND. The replacement is the alias, and requirement 9.1 guarantees the alias is
//	strictly shorter than the real root it replaces. Each pass observes a saving
//	opportunity. Audit can observe the SAME candidate bytes in both passes;
//	their sum is not a distinct candidate's potential or realized saving.
//
//	INBOUND. The replacement is the real root, and the real root is necessarily LONGER than
//	the alias it replaced - the alias only ever existed because it was shorter. So before
//	- after is NEGATIVE, and a negative value is not a saving: expanding an alias back to a
//	real path COSTS bytes. That cost is reported as [InboundCounters.BytesGrown] rather
//	than folded into a saving figure, because the operator sizing a rollout needs to see
//	it and a "bytes saved" field is the wrong place to hide it.
//
// The two are never summed into one figure, and that is the substantive decision rather
// than a presentation one: summing them would report a deployment that virtualizes
// heavily as LOSING bytes on every expansion. That is arithmetically true and
// operationally useless - the inbound cost is the price of the outbound saving, paid on
// the client's side rather than the model's, and one net number would hide exactly the
// two facts an operator needs. [TotalCounters.PassObservedOpportunityBytes] therefore
// sums outbound PASS observations only, not distinct candidate savings.
//
// Finally, no published saving is ever negative. Requirement 9.1 makes a negative outbound
// delta unreachable through the rewriter, but the projection does not DEPEND on that
// guarantee: clamping is one comparison, and a "bytes saved" field that could read negative
// would be the single most misleading number this package could publish.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// The size of each tally array. Each is one MORE than the size of the closed vocabulary
// it indexes, so the last slot is where a value outside that vocabulary lands. It is a
// bounded slot rather than a drop and rather than an out-of-range index, which is what
// keeps a hostile or future value countable AND keeps its label bounded.
//
// The last slot is never reached by a healthy build, so a series holding one is itself the
// signal that a dimension was fed a condition this build does not define.
const (
	// outboundOutcomeSlots is one slot per member of the outbound outcome vocabulary
	// ([outbound.OutcomeWorkspaceUnresolved] is the last) plus ONE bounded slot for a
	// value outside it.
	//
	// The extra slot is what makes the fold distinguishable from the last genuine member.
	// Sharing it would report a value this build does not define as though the workspace
	// had failed to resolve, so the series would contradict the WorkspaceUnresolved scalar,
	// which the switch in recordOutcome increments only for a genuine one.
	outboundOutcomeSlots = 5
	// expansionOutcomeSlots is one slot per member of the expansion outcome vocabulary
	// ([expansion.OutcomeRejected] is the last) plus ONE bounded slot for a value
	// outside it.
	//
	// Sharing that slot with the last member is worse here than in the outbound
	// direction, because the last member is a client-facing REFUSAL: an unrecognised
	// decision published as OutcomeRejected asserts refusals that never happened while
	// the Rejected scalar stays zero.
	expansionOutcomeSlots = 4
	skipReasonSlots       = 12
	expansionReasonSlots  = 14
	// passSlots is the size of the per-pass breakdown array: one slot per member of the
	// outbound pass vocabulary - the unattributed value, the early pass, and the late pass -
	// plus ONE bounded slot for a value outside it.
	//
	// The extra slot is what makes the fold distinguishable from the last genuine member.
	// Sharing it would report a value this build does not define as though the late pass
	// had produced it, and for this dimension that would be a wrong figure rather than a
	// merely mislabelled one: the whole point of the breakdown is which pass measured what.
	passSlots = 4
)

// The bounded slots every out-of-vocabulary value folds into. None of them is a genuine
// member's slot; see [outboundOutcomeSlots], [expansionOutcomeSlots], and [passSlots].
const (
	outboundOutcomeUnknownSlot  = outboundOutcomeSlots - 1
	expansionOutcomeUnknownSlot = expansionOutcomeSlots - 1
	passUnknownSlot             = passSlots - 1
)

// labelPass is the bounded label of one per-pass breakdown slot.
//
// The fallback renders through the vocabulary's own total [outbound.Pass] String method
// with a value outside the closed set, so "a pass this build does not define" is the
// dimension's own answer rather than a second spelling of it.
func labelPass(slot int) string {
	if slot >= passSlots {
		return outbound.Pass(outOfVocabularyOrdinal).String()
	}
	return outbound.Pass(slot).String()
}

// expansionReasonMembers is the finalizer's decision vocabulary in slot order, minus the
// no-decision value.
//
// It is an explicit list rather than arithmetic on the ordinal because the no-decision value
// occupies ordinal zero and is DOCUMENTED never to be reported: [expansion.ReasonNone] is
// not a decision any pass makes. Recording it would publish an empty label - a bounded
// series whose one entry names nothing - so the vocabulary this projection counts starts at
// the first real reason and the slot count above is one more than this list's length, which
// is the bounded fallback for a value outside the vocabulary.
var expansionReasonMembers = [...]expansion.Reason{
	expansion.ReasonExpanded,
	expansion.ReasonAuditMode,
	expansion.ReasonNoAlias,
	expansion.ReasonNoSelectors,
	expansion.ReasonArgsAbsent,
	expansion.ReasonPayloadNotObject,
	expansion.ReasonRootUnusable,
	expansion.ReasonMappingInactive,
	expansion.ReasonArgsUnparseable,
	expansion.ReasonMalformedReservedAlias,
	expansion.ReasonWorkspaceMismatch,
	expansion.ReasonExpandedTooLarge,
	expansion.ReasonInvalidRewrite,
}

// expansionReasonUnknownSlot is the bounded slot every reason outside
// [expansionReasonMembers] folds into, including the no-decision value.
const expansionReasonUnknownSlot = len(expansionReasonMembers)

// rootReasonSlots is the size of the root-refusal tally array: one slot per member of
// [rootReasons] plus one bounded slot for a code outside it.
//
// The extra slot is what makes [rootReasonUnknownSlot] distinct from the last genuine
// member. Sharing that slot would make a hostile root reason indistinguishable from the
// reserved-namespace-collision code, which is precisely the code an operator most needs to
// tell apart.
const rootReasonSlots = len(rootReasons) + 1

// Tally is one bounded label and how often it occurred.
//
// It is a struct rather than a retained string for two reasons. The label is rendered at
// snapshot time from the closed vocabulary rather than stored as text, so this recorder
// never HOLDS a string that could carry content; and the pairs are emitted in vocabulary
// order, so identical traffic produces byte-identical output and a metrics export does not
// reorder itself between reads.
type Tally struct {
	// Reason is the bounded label drawn from a closed enum's own String method. It never
	// contains path, alias, workspace-tag, suffix, tool-name, tool-call-ID, or hash bytes
	// (requirements.md 7.7).
	Reason string `json:"reason"`
	// Count is how many times that label occurred. It is never negative.
	Count int64 `json:"count"`
}

// labelOutboundOutcome is the bounded label of one outbound outcome series slot.
//
// The last slot is the enum's own bounded "unknown" rather than a fresh literal, because
// one build's idea of "a value this code does not define" should not be a second spelling
// the enum does not also publish.
func labelOutboundOutcome(slot int) string {
	if slot >= outboundOutcomeSlots {
		return outbound.Outcome(outboundOutcomeSlots).String()
	}
	return outbound.Outcome(slot).String()
}

// labelExpansionOutcome is the bounded label of one expansion outcome series slot.
func labelExpansionOutcome(slot int) string {
	if slot >= expansionOutcomeSlots {
		return expansion.Outcome(expansionOutcomeSlots).String()
	}
	return expansion.Outcome(slot).String()
}

// rootReasons is the lexical core's own refusal vocabulary in slot order.
//
// It is spelled out rather than indexed arithmetically because this vocabulary is a STRING
// enum, not an ordinal one: there is no arithmetic that turns a slot index back into a
// member, so the order has to be stated. The slice is the single place that order exists,
// which is what keeps the recording fold and the label projection from disagreeing about
// which slot a code lands in.
var rootReasons = [...]pathvirtualization.SkipReason{
	pathvirtualization.SkipReasonNone,
	pathvirtualization.SkipReasonEmptyRoot,
	pathvirtualization.SkipReasonRelativeRoot,
	pathvirtualization.SkipReasonMalformedVolumeRoot,
	pathvirtualization.SkipReasonDeviceNamespace,
	pathvirtualization.SkipReasonReservedNamespaceCollision,
}

// rootReasonUnknownSlot is the bounded slot every code outside [rootReasons] folds into.
//
// It is the last slot rather than a drop for the reason the other vocabularies use one:
// an exported named string type accepts a value a caller assembled from a real project
// root, and folding it keeps the series bounded and the label content-free without making
// the counter silently wrong.
const rootReasonUnknownSlot = rootReasonSlots - 1

// labelRootReason is the bounded label of one root-refusal series slot.
//
// The fallback renders through the vocabulary's own total [pathvirtualization.SkipReason]
// method with a value outside the closed set, so the label for "a code this build does not
// define" is the lexical core's own answer rather than a second spelling of it.
func labelRootReason(slot int) string {
	if slot >= len(rootReasons) {
		return pathvirtualization.SkipReason("\x00out-of-vocabulary").String()
	}
	return rootReasons[slot].String()
}

// labelSkipReason is the bounded label of one per-surface skip series slot.
//
// Slot zero is the engine's no-skip value, which is never recorded, so the published series
// starts at one; a zero count is dropped for the same reason.
func labelSkipReason(slot int) string {
	if slot <= 0 || slot >= skipReasonSlots {
		return rewrite.SkipReasonNone.String()
	}
	return rewrite.SkipReason(slot).String()
}

// outOfVocabularyOrdinal is the ordinal handed to a vocabulary's own total String method to
// obtain ITS answer for "a value this build does not define".
//
// Every closed enum in this feature ends its own String method with a bounded "unknown"
// fallback, and the fallback is the whole reason those methods are total. Rendering through
// it rather than spelling a second "unknown" here is what keeps one build's idea of
// out-of-vocabulary from becoming a second label the vocabularies do not publish.
const outOfVocabularyOrdinal = 0xFF

// labelExpansionReason is the bounded label of one expansion decision series slot.
//
// The fallback is the finalizer's own bounded answer rather than a second spelling of it.
func labelExpansionReason(slot int) string {
	if slot >= len(expansionReasonMembers) {
		return expansion.Reason(outOfVocabularyOrdinal).String()
	}
	return expansionReasonMembers[slot].String()
}

// projectTallies renders one counted array into vocabulary-ordered label/count pairs,
// dropping the empty slots.
//
// It is the ONLY place a series is built, so every series in this package is ordered and
// bounded the same way and none of them can grow by traffic.
func projectTallies(slots int, counts []int64, label func(int) string) []Tally {
	var out []Tally
	for slot := 0; slot < slots && slot < len(counts); slot++ {
		if counts[slot] == 0 {
			continue
		}
		out = append(out, Tally{Reason: label(slot), Count: counts[slot]})
	}
	return out
}

// DirectionCounters is one direction's measurement tally: occurrences, replacements, and
// the decoded-value byte accounting.
//
// The byte fields are all measured over DECODED string values, which is what makes them
// comparable across a client that spells a path with escapes and one that does not.
type DirectionCounters struct {
	// Eligible counts selected leaves the mapping accepted.
	Eligible int64 `json:"eligible"`
	// Rewritten counts eligible leaves whose published value differed from the value the
	// client sent. In rewrite mode this equals Eligible, because the alias is strictly
	// shorter than the prefix it replaces; it is a separate counter so the measurement mode
	// can report eligibility without a mutation.
	Rewritten int64 `json:"rewritten"`
	// BytesBefore is the total DECODED length of the eligible values as the client spelled
	// them.
	BytesBefore int64 `json:"bytes_before"`
	// BytesAfter is the total DECODED length of the values this direction published.
	BytesAfter int64 `json:"bytes_after"`
	// BytesSaved is BytesBefore minus BytesAfter, clamped at zero.
	//
	// It is never negative. On the INBOUND direction it is therefore always zero, because
	// expanding an alias back to a real root grows the value; that growth is reported as
	// [InboundCounters.BytesGrown] instead. On the OUTBOUND direction it combines
	// pass observations, not distinct candidates; its JSON name makes that explicit.
	BytesSaved int64 `json:"pass_observed_opportunity_bytes"`
	// Skipped is the total number of surfaces that contributed no rewrite.
	Skipped int64 `json:"skipped"`
	// Skips breaks Skipped down by the shared engine's closed reason vocabulary. It holds an
	// entry only for a reason that occurred, in vocabulary order.
	Skips []Tally `json:"skips,omitempty"`
}

// BytesGrown returns the DECODED byte cost of publishing in this direction, clamped at zero.
//
// It is a method rather than a field of [DirectionCounters] so the inbound growth cannot be
// mistaken for a direction-independent property of the type: the outbound direction's value
// is a saving by construction, and only one of the two directions has a growth figure at
// all. An operator reads the two as the benefit and the price.
func (c DirectionCounters) BytesGrown() int64 {
	if c.BytesAfter <= c.BytesBefore {
		return 0
	}
	return c.BytesAfter - c.BytesBefore
}

// PassCounters is one outbound pass's own measurement: the occurrences it saw and the
// byte accounting it performed.
//
// It exists to make the REALIZED per-candidate saving calculable, which is what
// requirements.md 9.5 asks for and what [OutboundCounters.ByPass] is read for. The
// arithmetic that makes it necessary is a fact about the shipped two-pass composition, not
// about this package: both passes observe one candidate, a rewrite publishes the alias so
// the late pass measures nothing, and an audit publishes nothing so the late pass measures
// the same figure again. Summed, the two modes were indistinguishable from a feature that
// saved twice as much, and an audit deployment's headline figure was exactly double a
// rewrite deployment's for identical traffic.
//
// So an operator reads the row of the pass that measured each candidate FIRST. With the
// shipped composition that is the early pass, and its row is byte-identical to what a
// rewrite deployment publishes as its whole total - which is exactly the figure a measured
// rollout is meant to predict. A later shaping pass that introduced a path the early pass
// never saw would publish that new saving in its OWN row rather than folding it into a
// headline total, so such a case is visible instead of averaged away.
//
// Every field is an integer or a bounded label, so the value is safe to serialize as a
// metric attribute set (requirements.md 7.7).
type PassCounters struct {
	// Pass is the bounded label of the pass that produced this row, drawn from the closed
	// pass vocabulary. It never contains path, alias, workspace-tag, suffix, tool-name,
	// tool-call-ID, or hash bytes.
	Pass string `json:"pass"`
	// Reports is how many reports that pass contributed.
	Reports int64 `json:"reports"`
	// Eligible is the number of selected leaves that pass's mappings accepted.
	Eligible int64 `json:"eligible"`
	// Rewritten is the number of eligible leaves whose published value differed from the
	// value the client sent. In rewrite mode this equals Eligible.
	Rewritten int64 `json:"rewritten"`
	// BytesBefore is the total DECODED length of the eligible values as the client spelled
	// them.
	BytesBefore int64 `json:"bytes_before"`
	// BytesAfter is the total DECODED length of the values that pass published.
	BytesAfter int64 `json:"bytes_after"`
	// BytesSaved is THIS PASS's observed saving opportunity, clamped per observation
	// at zero. Rewrite realizes it on that pass; audit only measures it.
	//
	// It is never negative, for the same reason [DirectionCounters.BytesSaved] is not: a
	// published saving that could read negative would be the single most misleading number
	// this package could publish, and the projection does not depend on requirement 9.1 to
	// prevent it. It is a per-observation clamped sum rather than the row's own clamped net
	// so that the rows partition [TotalCounters.PassObservedOpportunityBytes] exactly; see
	// [OutboundCounters.ByPass].
	BytesSaved int64 `json:"bytes_saved"`
}

// OutboundCounters is the content-free outcome of the OUTBOUND direction: the two passes
// that replace real paths with aliases before a backend sees them.
//
// Every field is an integer or a bounded-label tally, so the whole value is safe to
// serialize as a metric attribute set (requirements.md 7.7).
type OutboundCounters struct {
	// Reports is how many outbound reports this generation recorded, across BOTH passes. It
	// is the denominator every rate on this value is read against.
	Reports int64 `json:"reports"`
	// ByPass breaks observations down by the pass that measured them. These rows
	// are not distinct candidates: audit may observe the same path in both passes.
	// It holds one entry per pass that reported, in vocabulary order, so the
	// series is bounded by the closed pass vocabulary whatever traffic arrives. A pass
	// that reported and measured nothing keeps its row, with a non-zero Reports count and
	// zero figures: "the pass ran and found nothing" and "the pass did not run" are
	// different facts, and only the second is an absent row.
	//
	// The rows PARTITION [OutboundCounters.Virtualized]: their eligible occurrences sum to
	// it and their opportunities sum to [TotalCounters.PassObservedOpportunityBytes]. The breakdown therefore
	// restates the same measurement rather than adding a second accounting of it.
	ByPass []PassCounters `json:"by_pass,omitempty"`
	// Virtualized is the rewrite direction's own tally: eligible occurrences, published
	// replacements, and the byte accounting requirements 7.6 and 9.5 ask for.
	Virtualized DirectionCounters `json:"virtualized"`
	// TransformFailed counts requirements.md 8.2's fail-open condition: an unexpected
	// internal transformation failure before any alias became model-visible. The real path
	// was preserved and the call continued, which is exactly why it is counted separately
	// rather than as a rewrite.
	TransformFailed int64 `json:"transform_failed"`
	// RootUnusable counts passes that published nothing because the authoritative project
	// root is not one of the five supported absolute forms, or is spelled inside the fixed
	// reserved alias namespace (requirement 1.8).
	RootUnusable int64 `json:"root_unusable"`
	// WorkspaceUnresolved counts late passes that had no pinned workspace view to read at
	// all. Only the late pass can reach it, because only the late pass reads a view at all;
	// see [outbound.OutcomeWorkspaceUnresolved].
	WorkspaceUnresolved int64 `json:"workspace_unresolved"`
	// Outcomes is the pass-level outcome series over the outbound vocabulary, which also
	// accounts for the routine outcome the scalar counters above deliberately do not carry.
	Outcomes []Tally `json:"outcomes,omitempty"`
	// RootReasons breaks RootUnusable down by the lexical core's own bounded refusal code.
	RootReasons []Tally `json:"root_reasons,omitempty"`
}

// InboundCounters is the content-free outcome of the INBOUND direction: the finalizer that
// replaces aliases with real paths before a client's tool call runs.
//
// Every field is an integer or a bounded-label tally.
type InboundCounters struct {
	// Reports is how many expansion decisions this generation recorded.
	Reports int64 `json:"reports"`
	// Expanded counts decisions that published an expanded argument document
	// (requirements.md 4.1).
	Expanded int64 `json:"expanded"`
	// Noop counts decisions that published nothing and refused nothing. It is counted
	// separately from Rejected because "nothing to do" and "nothing proved" are different
	// operational facts, and a deployment that only ever sees no-ops has learned nothing
	// about whether its virtualization is doing anything.
	Noop int64 `json:"noop"`
	// Rejected counts refusal decisions. Rewrite applies them fail closed;
	// audit reports the hypothetical refusal but passes the original call.
	Rejected int64 `json:"rejected"`
	// Restore is the expansion direction's own tally: eligible occurrences, replacements
	// performed, and the byte accounting. Its BytesSaved is always zero and its
	// [DirectionCounters.BytesGrown] is the inbound cost; see the file comment.
	Restore DirectionCounters `json:"restore"`
	// Outcomes is the pass-level outcome series over the expansion vocabulary.
	Outcomes []Tally `json:"outcomes,omitempty"`
	// Reasons breaks the decision down by the finalizer's own closed reason vocabulary,
	// which is also the string it publishes as a toolcall.Result.ReasonCode. It holds an
	// entry only for a reason that occurred, in vocabulary order.
	Reasons []Tally `json:"reasons,omitempty"`
	// RootReasons breaks the unusable-root decisions down by the lexical core's own bounded
	// refusal code.
	RootReasons []Tally `json:"root_reasons,omitempty"`
	// MandatoryOverflows counts completed calls whose assembled arguments exceeded the
	// mandatory bound THIS PASS declared (requirements.md 7.6).
	//
	// Read it together with [Inventory.MandatoryMaxArgsBytes]: the inventory field is what
	// the generation declared and this one is what the pass actually saw. The tool-call
	// assembler's own past-the-bound REFUSAL is not counted here and cannot be - the
	// assembler decides it before any finalizer runs, so this pass never observes it. What
	// this counter observes is the reachable neighbouring case: the effective assembly bound
	// is the maximum over every declaring finalizer, so a deployment where another pass
	// declares a larger bound hands this pass calls past its own declaration, and that
	// disagreement is the fact an operator who lowered the bound needs to see.
	MandatoryOverflows int64 `json:"mandatory_overflows"`
}

// BytesGrown returns the inbound direction's decoded byte cost.
//
// It is published as a field rather than left as the [DirectionCounters] method alone
// because it is the number an operator reads next to [TotalCounters.PassObservedOpportunityBytes], and
// naming both at the same level is what stops the two being compared as if they were
// opposites.
func (c InboundCounters) BytesGrown() int64 { return c.Restore.BytesGrown() }

// TotalCounters is the generation-wide tally across both directions.
//
// The counters here are the ones that mean the same thing in either direction:
// occurrences, eligibility, and the OUTBOUND saving.
type TotalCounters struct {
	// Reports is how many reports this generation recorded across both directions. The
	// denominator every rate on this value is read against.
	Reports int64 `json:"reports"`
	// Eligible is the summed eligible-occurrence count across both directions.
	Eligible int64 `json:"eligible"`
	// Rewritten is the summed replacement count across both directions.
	Rewritten int64 `json:"rewritten"`
	// PassObservedOpportunityBytes sums outbound per-pass opportunities, clamped
	// per observation. It is NOT candidate savings: audit can observe the same
	// bytes twice, and later shaping can introduce or remove paths. Candidate
	// savings cannot be inferred from this sum without a candidate-level reader.
	PassObservedOpportunityBytes int64 `json:"pass_observed_opportunity_bytes"`
	// Skipped is the total number of surfaces that contributed no rewrite in either
	// direction, over the shared engine's own closed reason vocabulary.
	Skipped int64 `json:"skipped"`
}

// Snapshot is the complete content-free projection of one generation at one instant.
//
// It is a value: it shares no state with the recorder, so a reader may hold it while traffic
// continues. Two recorders fed the same reports publish byte-identical snapshots, because
// every series is emitted in closed-vocabulary order.
type Snapshot struct {
	// Inventory is the bounded configuration shape this generation was compiled with. It is
	// copied into every snapshot so a metrics export carries the configuration it was
	// measured under, rather than requiring a second read that could disagree.
	Inventory Inventory `json:"inventory"`
	// Outbound is the two outbound passes' tally.
	Outbound OutboundCounters `json:"outbound"`
	// Inbound is the expansion finalizer's tally.
	Inbound InboundCounters `json:"inbound"`
	// Total is the generation-wide tally.
	Total TotalCounters `json:"total"`
}

// tallySet is one direction's occurrence, replacement, and byte accounting.
type tallySet struct {
	eligible    int64
	rewritten   int64
	bytesBefore int64
	bytesAfter  int64
	skipped     int64
	skips       [skipReasonSlots]int64
}

// outboundCounters is the recorder's mutable outbound half. See [inboundCounters].
type outboundCounters struct {
	reports             int64
	byPass              [passSlots]tallySet
	passReports         [passSlots]int64
	passSaved           [passSlots]int64
	virtualized         tallySet
	transformFailed     int64
	rootUnusable        int64
	workspaceUnresolved int64
	outcomes            [outboundOutcomeSlots]int64
	rootReasons         [rootReasonSlots]int64
}

// inboundCounters is the recorder's mutable inbound half.
type inboundCounters struct {
	reports            int64
	expanded           int64
	noop               int64
	rejected           int64
	mandatoryOverflows int64
	restore            tallySet
	outcomes           [expansionOutcomeSlots]int64
	reasons            [expansionReasonSlots]int64
	rootReasons        [rootReasonSlots]int64
}

// totalCounters is the recorder's mutable generation-wide tally.
type totalCounters struct {
	reports   int64
	eligible  int64
	rewritten int64
	skipped   int64
	// saved is the OUTBOUND saving only. It is not a net of both directions, for the reason
	// the file comment gives.
	saved int64
}

// record folds one engine statistics value into this direction's tally and returns how many
// surfaces it skipped.
//
// The SAVING is clamped in the snapshot rather than here, which is deliberate: the two raw
// totals are the measurement and they must be accumulated exactly as measured, so that
// clamping a saving can never distort what the pass actually saw. The clamp is therefore
// applied on read, to the already-folded totals.
//
// The skip count is returned rather than read back off the tally because the generation-wide
// tally needs this observation's contribution, and reading the running total would fold the
// same history in once per report.
func (t *tallySet) record(stats rewrite.Stats) int64 {
	t.eligible += int64(stats.Eligible)
	t.rewritten += int64(stats.Rewritten)
	t.bytesBefore += int64(stats.BytesBefore)
	t.bytesAfter += int64(stats.BytesAfter)
	var skipped int64
	for _, skip := range stats.Skips {
		// A reason outside the engine's own closed vocabulary is DROPPED rather than
		// folded into the unknown slot, and the asymmetry with the outcome tallies is
		// deliberate: the engine documents a reason it cannot represent as never being
		// recorded at all, so an entry naming one is a malformed report rather than an
		// observation of a condition this build does not define.
		if skip.Reason == rewrite.SkipReasonNone || int(skip.Reason) >= skipReasonSlots {
			continue
		}
		t.skips[skip.Reason] += int64(skip.Count)
		skipped += int64(skip.Count)
	}
	t.skipped += skipped
	return skipped
}

// recordOutcome counts one bounded outbound outcome.
func (a *outboundCounters) recordOutcome(outcome outbound.Outcome) {
	switch outcome {
	case outbound.OutcomeProjectRootUnusable:
		a.rootUnusable++
	case outbound.OutcomeTransformationFailed:
		a.transformFailed++
	case outbound.OutcomeWorkspaceUnresolved:
		a.workspaceUnresolved++
	}
	a.outcomes[outcomeSlot(outcome)]++
}

// recordPass folds one report's statistics into the row of the pass that produced it.
//
// The row's SAVING is clamped per observation rather than derived from the row's own raw
// totals, and that is a requirement of the projection rather than a presentation choice:
// the rows must PARTITION the generation total, and [totalCounters.saved] is a per-observation
// clamped sum. A row derived as one clamped net would swallow every per-observation clamp in
// the row, so the row and the total would disagree for the same traffic - which is the
// disagreement requirement 9.5's figure cannot afford. The raw totals are still
// accumulated exactly as measured, so the row's measurement is untouched by the clamp.
func (a *outboundCounters) recordPass(pass outbound.Pass, stats rewrite.Stats) {
	slot := passSlot(pass)
	a.passReports[slot]++
	a.byPass[slot].record(stats)
	a.passSaved[slot] += nonNegative(int64(stats.BytesBefore) - int64(stats.BytesAfter))
}

// passRows projects the per-pass tally onto the published series, dropping the passes that
// never reported.
//
// A pass that reported but measured nothing KEEPS its row, with a non-zero
// [PassCounters.Reports] and zero figures throughout. That is the shape that makes
// requirement 9.5's figure recoverable: "the late pass ran and found nothing" and "the late
// pass did not run" are different operational facts, and only the second one is an absent
// row.
func (a *outboundCounters) passRows() []PassCounters {
	var rows []PassCounters
	for slot := range passSlots {
		if a.passReports[slot] == 0 {
			continue
		}
		set := a.byPass[slot].snapshot()
		rows = append(rows, PassCounters{
			Pass:        labelPass(slot),
			Reports:     a.passReports[slot],
			Eligible:    set.Eligible,
			Rewritten:   set.Rewritten,
			BytesBefore: set.BytesBefore,
			BytesAfter:  set.BytesAfter,
			BytesSaved:  a.passSaved[slot],
		})
	}
	return rows
}

// recordRootReason counts one bounded root refusal code.
func (a *outboundCounters) recordRootReason(reason pathvirtualization.SkipReason) {
	a.rootReasons[rootReasonSlot(reason)]++
}

// recordOutcome counts one bounded expansion decision.
func (a *inboundCounters) recordOutcome(outcome expansion.Outcome, reason expansion.Reason) {
	switch outcome {
	case expansion.OutcomeExpanded:
		a.expanded++
	case expansion.OutcomeNoop:
		a.noop++
	case expansion.OutcomeRejected:
		a.rejected++
	}
	a.outcomes[expansionOutcomeSlot(outcome)]++
	a.reasons[expansionReasonSlot(reason)]++
}

// recordRootReason counts one bounded root refusal code.
func (a *inboundCounters) recordRootReason(reason pathvirtualization.SkipReason) {
	a.rootReasons[rootReasonSlot(reason)]++
}

// recordOutboundBytes folds the outbound byte accounting into the generation tally.
func (t *totalCounters) recordOutboundBytes(stats rewrite.Stats) {
	// The clamp is applied per observation rather than on read, because the generation
	// total is a SUM of per-observation savings and summing raw negatives would let one
	// pathological observation subtract from every other one.
	t.saved += nonNegative(int64(stats.BytesBefore) - int64(stats.BytesAfter))
}

// snapshot projects the mutable outbound half onto the published value.
func (a outboundCounters) snapshot() OutboundCounters {
	return OutboundCounters{
		Reports:             a.reports,
		ByPass:              a.passRows(),
		Virtualized:         a.virtualized.snapshot(),
		TransformFailed:     a.transformFailed,
		RootUnusable:        a.rootUnusable,
		WorkspaceUnresolved: a.workspaceUnresolved,
		Outcomes:            projectTallies(outboundOutcomeSlots, a.outcomes[:], labelOutboundOutcome),
		RootReasons:         projectTallies(rootReasonSlots, a.rootReasons[:], labelRootReason),
	}
}

// snapshot projects the mutable inbound half onto the published value.
func (a inboundCounters) snapshot() InboundCounters {
	return InboundCounters{
		Reports:            a.reports,
		Expanded:           a.expanded,
		Noop:               a.noop,
		Rejected:           a.rejected,
		Restore:            a.restore.snapshot(),
		Outcomes:           projectTallies(expansionOutcomeSlots, a.outcomes[:], labelExpansionOutcome),
		Reasons:            projectTallies(expansionReasonSlots, a.reasons[:], labelExpansionReason),
		RootReasons:        projectTallies(rootReasonSlots, a.rootReasons[:], labelRootReason),
		MandatoryOverflows: a.mandatoryOverflows,
	}
}

// snapshot projects the mutable generation-wide tally onto the published value.
func (t totalCounters) snapshot() TotalCounters {
	return TotalCounters{
		Reports:                      t.reports,
		Eligible:                     t.eligible,
		Rewritten:                    t.rewritten,
		PassObservedOpportunityBytes: t.saved,
		Skipped:                      t.skipped,
	}
}

// snapshot projects one direction's tally onto the published value.
func (t tallySet) snapshot() DirectionCounters {
	return DirectionCounters{
		Eligible:    t.eligible,
		Rewritten:   t.rewritten,
		BytesBefore: t.bytesBefore,
		BytesAfter:  t.bytesAfter,
		BytesSaved:  nonNegative(t.bytesBefore - t.bytesAfter),
		Skipped:     t.skipped,
		Skips:       projectTallies(skipReasonSlots, t.skips[:], labelSkipReason),
	}
}

// nonNegative clamps a delta at zero.
//
// A negative delta is not a saving, on either side, and requirement 9.1 already makes the
// outbound case unreachable through the rewriter. The clamp is here so the published value
// does not DEPEND on that guarantee: a "bytes saved" field that could read negative would be
// the single most misleading number this package could publish.
//
// It takes and returns int64 because the accumulator it clamps is one. The engine's own
// counters are ints, and every fold into a published value goes through this function, so
// the widening happens exactly once and in one place rather than at each arithmetic site.
func nonNegative(delta int64) int64 {
	if delta <= 0 {
		return 0
	}
	return delta
}

// outcomeSlot maps a bounded outbound outcome onto its series position, folding anything
// outside the closed vocabulary into the last slot.
//
// The fold is what makes the array total: a hostile or future value lands in a bounded slot
// with a bounded label instead of indexing out of range or vanishing.
func outcomeSlot(outcome outbound.Outcome) int {
	if int(outcome) >= outboundOutcomeUnknownSlot {
		return outboundOutcomeUnknownSlot
	}
	return int(outcome)
}

// passSlot maps a bounded pass onto its breakdown position, folding anything outside the
// closed vocabulary into [passUnknownSlot].
//
// The fold is a bounded slot rather than a drop, for the reason the other vocabularies use
// one: a pass is an exported numeric type a future build could extend, and losing the
// measurement entirely would be worse than reporting it under a label that says the
// recorder did not recognise the pass.
func passSlot(pass outbound.Pass) int {
	if int(pass) >= passSlots {
		return passUnknownSlot
	}
	return int(pass)
}

// expansionOutcomeSlot maps a bounded expansion outcome onto its series position, folding
// anything outside the closed vocabulary into the last slot.
func expansionOutcomeSlot(outcome expansion.Outcome) int {
	if int(outcome) >= expansionOutcomeUnknownSlot {
		return expansionOutcomeUnknownSlot
	}
	return int(outcome)
}

// expansionReasonSlot maps a bounded expansion reason onto its series position, folding
// anything outside [expansionReasonMembers] - including the no-decision value - into
// [expansionReasonUnknownSlot].
//
// The search is over the fixed list of constants rather than over arithmetic on the ordinal,
// so the no-decision value and any value this build does not define land in the same bounded
// slot instead of producing an empty or out-of-range series entry.
func expansionReasonSlot(reason expansion.Reason) int {
	for slot, member := range expansionReasonMembers {
		if member == reason {
			return slot
		}
	}
	return expansionReasonUnknownSlot
}

// rootReasonSlot maps a bounded root refusal code onto its series position.
//
// The search is over the fixed set of constants in [rootReasons] rather than over parsed
// text, so nothing a caller supplied is ever interpreted as a vocabulary member. Anything
// else - including any value assembled from a real project root - folds into
// [rootReasonUnknownSlot] and therefore renders with a bounded label.
func rootReasonSlot(reason pathvirtualization.SkipReason) int {
	for slot, member := range rootReasons {
		if member == reason {
			return slot
		}
	}
	return rootReasonUnknownSlot
}

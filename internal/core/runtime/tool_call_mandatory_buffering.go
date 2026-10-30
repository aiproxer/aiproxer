package runtime

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Mandatory completeness requirements at the tool-call assembly chokepoint.
//
// A finalizer may publish the optional [toolcall.BufferingRequirement]
// capability to declare that it must receive the complete assembled tool-call
// arguments before the call may be released. The shared assembly limit cannot
// express that on its own: past the shared limit the assembler historically
// gave up, replayed the original stream fragments, and never ran any finalizer,
// which silently bypassed a completeness requirement.
//
// This file holds the assembler-side projection of that capability. It is
// feature-neutral: it names argument size and overflow behavior only, never a
// concrete feature, payload domain, or target namespace.
//
// WHAT IS PROJECTED WHERE, AND WHY
//
// A declaration says two things that are independent of each other: WHICH CALLS
// it governs, and WHAT ITS OWN LIMIT IS for the calls it does govern. Folding
// both into one assembler-wide bound plus one boolean at composition time
// cannot represent either, and every defect this file exists to prevent follows
// from that compression:
//
//   - one BOOLEAN cannot express which calls are in scope, so a call a selective
//     declarer would never inspect inherited its bound AND its refusal policy
//     (requirements 4.6 and 4.7);
//   - one MAXIMUM cannot enforce each declarer's own limit, because the largest
//     bound silently absorbs every smaller one (requirement 4.5);
//   - one BOOLEAN cannot express how many requirements a call carries, so the
//     first declarer to run satisfied all of them (requirement 4.6).
//
// So the projection here is a TABLE, resolved once from the already sorted
// finalizer list: one entry per declarer, each keeping its own validated spec,
// its own identity, and its own applicability capability. Applicability and
// requirement state are then derived PER TOOL CALL, in
// [toolCallAssembler.deriveCallRequirements], from the tool name, the tool
// definition, and the catalog - the only three inputs a consumer may use, so no
// declarer can be forced to expose payload or request state to be enforced.

// Bounded classification codes for a completed tool call the assembler refuses
// closed because a declared mandatory completeness requirement could not be
// honored. They are the whole classification payload of the typed error: no
// path, alias, workspace tag, argument content, tool-call ID, or leg identity
// ever appears in it.
const (
	// ReasonMandatoryBufferingOverflow marks a call whose assembled arguments
	// exceed a bound a finalizer declared together with
	// [toolcall.OverflowReject].
	ReasonMandatoryBufferingOverflow = "mandatory_buffering_overflow"

	// ReasonMandatoryBufferingDeclarationInvalid marks a call refused because a
	// finalizer published a present-but-unusable [toolcall.BufferingSpec].
	ReasonMandatoryBufferingDeclarationInvalid = "mandatory_buffering_declaration_invalid"

	// ReasonMandatoryBufferingIncomplete marks a call refused because an
	// unrelated finalizer failed or produced an unusable result, or because a
	// finalizer with a declared mandatory completeness requirement failed itself,
	// before every applicable requirement could be decided on the complete
	// arguments.
	ReasonMandatoryBufferingIncomplete = "mandatory_buffering_incomplete"
)

// ErrMandatoryBuffering is the stable root error for a completed tool call the
// assembler refused closed, so that no possibly alias-bearing argument reaches
// the client without the finalizer that must decide on it having decided.
var ErrMandatoryBuffering = errors.New("tool call assembler: mandatory buffering requirement not honored")

// MandatoryBufferingError reports that the tool-call assembler refused exactly
// one completed tool call closed at the assembly chokepoint. It is the typed
// counterpart of the [toolcall.RejectError] a finalizer returns for its own
// decision, covering the three cases the assembler itself decides: an assembled
// call past a declared mandatory bound with [toolcall.OverflowReject], a
// present-but-unusable mandatory declaration, and an undecided applicable
// requirement
// ([ReasonMandatoryBufferingIncomplete]).
//
// It is content-free by construction. FinalizerID and ToolCallID are carried
// for programmatic classification only and are deliberately absent from
// Error(), exactly as in [toolcall.RejectError].
type MandatoryBufferingError struct {
	// Reason is one of [ReasonMandatoryBufferingOverflow],
	// [ReasonMandatoryBufferingDeclarationInvalid], or
	// [ReasonMandatoryBufferingIncomplete].
	Reason string

	// FinalizerID identifies the declarer whose requirement decided the refusal,
	// for diagnostics only. It is the declarer whose OWN bound was exceeded, or
	// the declarer that had not yet decided, so a refusal always names the
	// participant responsible for it.
	FinalizerID string

	// ToolCallID is the refused call's identifier, for classification only.
	ToolCallID string

	// MaxArgsBytes is the bound that was exceeded: the declarer's OWN declared
	// bound, or the effective assembly ceiling when the call outgrew the
	// buffering limit itself. It is zero for a declaration that was unusable at
	// any bound and for an undecided requirement, where no bound was involved.
	MaxArgsBytes int
}

func (e *MandatoryBufferingError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrMandatoryBuffering.Error()
	}
	return fmt.Sprintf("%s (%s)", ErrMandatoryBuffering, e.Reason)
}

func (e *MandatoryBufferingError) Is(target error) bool { return target == ErrMandatoryBuffering }

// IsMandatoryBufferingError reports whether err is or wraps a
// [MandatoryBufferingError].
func IsMandatoryBufferingError(err error) bool {
	var mbe *MandatoryBufferingError
	return errors.As(err, &mbe)
}

// mandatoryDeclaration is ONE finalizer's published completeness requirement,
// retained individually so the assembler can enforce that declarer's own limit
// and track that declarer's own requirement state, rather than an aggregate that
// answers for all of them at once.
type mandatoryDeclaration struct {
	// index is the declarer's position in the materialized finalizer chain, which
	// is what lets a per-call requirement set be joined back to the iteration
	// without re-asserting any capability.
	index int

	// id is the declaring finalizer's identity, for diagnostics only.
	id string

	// spec is the declaration exactly as the declarer published it. It is never
	// merged with any other declarer's, so enforcement cannot lose the smaller of
	// two bounds.
	spec toolcall.BufferingSpec

	// valid records whether spec validates. An invalid spec still gets an entry:
	// it is a publisher error rather than an absent capability, and the call it
	// governs must fail closed rather than degrade.
	valid bool

	// applicability is the declarer's own [toolcall.BufferingApplicability]
	// answer, or nil when it published none - in which case the conservative
	// reading applies and the declaration governs every call.
	applicability toolcall.BufferingApplicability

	// observesOnly records that the declarer asked for the complete arguments
	// to MEASURE rather than to DECIDE, by declaring
	// [toolcall.CompletenessBestEffort]. Its bound still widens assembly - that
	// is the only way a call larger than the shared default becomes observable at
	// all - but it never binds the call: the requirement starts satisfied, so no
	// failure of this or of any other finalizer can refuse a call through it.
	observesOnly bool
}

// requiresDecision reports whether this declaration must be decided before the
// call it governs may be released. A best-effort declarer must not: it asked for
// the complete document in order to watch it, and turning an unrelated
// finalizer's failure into a hard rejection of a call it never decided about
// would make enabling measurement into a new source of refusals.
func (d *mandatoryDeclaration) requiresDecision() bool {
	return !d.observesOnly
}

// requiresRejectPastOwnBound reports what this declaration requires of a call
// whose assembled arguments are past the declarer's OWN bound. A present-but-
// malformed declaration qualifies: no bound could ever satisfy it, so releasing
// the fragments would be the very bypass the capability exists to prevent.
func (d *mandatoryDeclaration) requiresRejectPastOwnBound() bool {
	return !d.valid || d.spec.Overflow == toolcall.OverflowReject
}

// appliesToTool reports whether this declaration governs a call to toolName.
//
// The answer is the declarer's own whenever it published one, and the
// conservative "every call" otherwise. Either way it is derived from the tool
// name, the tool definition, and the catalog alone.
//
// The declarer's answer runs inside the extension safety boundary on detached
// inputs. A capability method is still extension code: it may panic, and its
// tool and catalog arguments are mutable Go values. Handing it the assembler's
// own catalog would let one declarer's bug corrupt later finalization and
// future calls. A panic is treated as applicable, because an undeterminable
// scope for a mandatory declaration must fail closed rather than disappear.
//
// Each declarer receives its own copy, so mutation cannot change a sibling's
// applicability decision and bypass that sibling's mandatory requirement.
func (d *mandatoryDeclaration) appliesToTool(toolName string, tool aipapi.ToolDef, catalog []aipapi.ToolDef) bool {
	if d.applicability == nil {
		return true
	}
	isolatedCatalog := cloneToolCatalog(catalog)
	isolatedTool := lookupToolDef(isolatedCatalog, tool.Name)
	if tool.Name != toolName {
		isolatedTool = lookupToolDef(isolatedCatalog, toolName)
	}
	applies, err := safety.CallValue(safety.BoundaryExtension, "tool_call_buffering_applicability", func() (bool, error) {
		return d.applicability.ToolCallBufferingApplies(toolName, isolatedTool, isolatedCatalog), nil
	})
	if err != nil {
		return true
	}
	return applies
}

// mandatoryBuffering is the per-attempt projection of every finalizer that
// publishes [toolcall.BufferingRequirement], computed once when the assembler
// is built from the already sorted finalizer list.
//
// declarations is the table every decision is made from. The scalar fields below
// are the assembler-wide ANSWERS to the questions that do not depend on any one
// call - which declarations exist at all, whether any of them is unusable, and
// the widest ceiling any of them could ask for - and they are what
// [toolCallAssembler.deriveCallRequirements] narrows per call.
type mandatoryBuffering struct {
	// declarations holds one entry per finalizer that published the capability
	// with either a well-formed mandatory bound or a present-but-unusable spec,
	// in chain order. A finalizer that opted in with the zero spec gets no entry:
	// it declares no requirement at all, exactly as if it had never opted in.
	declarations []*mandatoryDeclaration

	// assemblyMaxArgsBytes is the assembler-wide CEILING: the shared legacy bound
	// raised to the widest bound any declaration asks for, never lowered, and
	// never above [aipapi.MaxEventDeltaBytes]. A single call's buffering limit is
	// a NARROWING of this value, taken over the declarations that govern that
	// call, so this field is the invariant the per-call limit can never exceed.
	assemblyMaxArgsBytes int

	// rejectPastBound records that at least one declaration requires
	// [toolcall.OverflowReject], so SOME call past its applicable bound must be
	// refused instead of replayed as its original fragments. Whether a given
	// call is that call is decided per call; this field only answers "could any
	// be".
	rejectPastBound bool

	// rejectFinalizerID is the deterministic first declarer that set
	// rejectPastBound, for diagnostics only.
	rejectFinalizerID string

	// declaredCount is how many finalizers satisfy the capability assertion at
	// all. Zero means no finalizer opted in, so nothing about the pre-existing
	// assembly behavior changes for any call.
	declaredCount int

	// mandatoryBoundDeclared records that at least one declaration is a
	// well-formed BINDING completeness requirement. It is the only thing that
	// turns on the requirement 4.6 failure clause: a call may not fall back to
	// replaying its original fragments while an APPLICABLE requirement is still
	// undecided. A zero-spec opt-in, a present-but-malformed declaration, and a
	// best-effort declarer never set it.
	mandatoryBoundDeclared bool

	// invalidDeclaration records that some finalizer published the capability
	// with a [toolcall.BufferingSpec] that does not validate.
	invalidDeclaration bool

	// invalidFinalizerID is the deterministic first finalizer with an unusable
	// declaration, for diagnostics only.
	invalidFinalizerID string
}

// resolveMandatoryBuffering projects the optional capability declarations onto a
// TABLE of per-declarer requirements plus the assembler-wide answers derived
// from it.
//
// It branches on the capability assertion itself, never on
// [toolcall.BufferingSpec.DeclaresMandatoryBound] alone, because a declaration
// that is PRESENT but malformed is a publisher error rather than an absent
// capability: degrading it to the legacy pass-through would downgrade a
// mandatory completeness requirement into optional handling, the single
// direction requirements 4.5, 4.6, and 8.3 forbid. Such a declaration is
// therefore recorded as invalid and its unusable bound is never used to raise the
// assembly ceiling, and the tool calls it governs are refused closed.
//
// legacyMaxArgsBytes is the already clamped shared assembly bound. It is the
// floor of the ceiling, so raising the bound for a mandatory finalizer can never
// narrow what an ordinary finalizer already receives, and no finalizer's own size
// policy is read or widened here. Each declarer's own bound IS kept, however, and
// is enforced for the calls that declarer governs - see
// [callRequirement.enforcementFor].
func resolveMandatoryBuffering(finalizers []toolcall.Finalizer, legacyMaxArgsBytes int) mandatoryBuffering {
	mb := mandatoryBuffering{assemblyMaxArgsBytes: legacyMaxArgsBytes}
	for index, fin := range finalizers {
		if fin == nil {
			continue
		}
		req, ok := fin.(toolcall.BufferingRequirement)
		if !ok {
			// Case 1: the finalizer never opted in. Pre-existing behavior.
			continue
		}
		mb.declaredCount++
		spec := req.ToolCallBufferingRequirement()
		decl := &mandatoryDeclaration{
			index:         index,
			id:            fin.ID(),
			spec:          spec,
			valid:         spec.Validate() == nil,
			applicability: applicabilityOf(fin),
			observesOnly:  spec.Completeness == toolcall.CompletenessBestEffort,
		}
		if !decl.valid {
			// Case 3: opted in, but the declaration cannot bound anything.
			mb.declarations = append(mb.declarations, decl)
			if !mb.invalidDeclaration {
				mb.invalidDeclaration = true
				mb.invalidFinalizerID = decl.id
			}
			continue
		}
		if !spec.DeclaresMandatoryBound() {
			// Opted in with the zero spec: observationally identical to case 1, so
			// it gets no entry and can never become an applicable requirement.
			continue
		}
		// Case 2: a well-formed mandatory bound applies, to the calls this
		// declarer says it applies to. A best-effort declarer gets the same entry,
		// so its own bound is still enforced and still widens the ceiling, but it
		// does not make the requirement BINDING for the call: nothing can refuse
		// through it.
		mb.declarations = append(mb.declarations, decl)
		if decl.requiresDecision() {
			mb.mandatoryBoundDeclared = true
		}
		if spec.MaxArgsBytes > mb.assemblyMaxArgsBytes {
			mb.assemblyMaxArgsBytes = spec.MaxArgsBytes
		}
		if spec.Overflow == toolcall.OverflowReject && !mb.rejectPastBound {
			mb.rejectPastBound = true
			mb.rejectFinalizerID = decl.id
		}
	}
	if mb.assemblyMaxArgsBytes > aipapi.MaxEventDeltaBytes {
		mb.assemblyMaxArgsBytes = aipapi.MaxEventDeltaBytes
	}
	return mb
}

// applicabilityOf reads the declarer's own per-call scope, or nil when it
// published none.
func applicabilityOf(fin toolcall.Finalizer) toolcall.BufferingApplicability {
	appl, _ := fin.(toolcall.BufferingApplicability)
	return appl
}

// declaresAny reports whether any finalizer published the optional capability at
// all. A false answer is the assembler's fast path: nothing about the
// pre-existing assembly behavior can change for any call.
func (m mandatoryBuffering) declaresAny() bool {
	return m.declaredCount > 0
}

// callRequirement is ONE applicable mandatory completeness requirement for ONE
// tool call.
//
// It is per call and per declarer on purpose. Requirement state is tracked as one
// entry per declarer rather than as a single flag, so satisfying one declarer can
// never satisfy another that has not decided yet, and releasing a document is
// possible only once every entry has left the pending state.
type callRequirement struct {
	// decl is the declarer this requirement came from.
	decl *mandatoryDeclaration

	// pending is true until this declarer has produced a usable decision: a Pass,
	// a usable Rewrite, or an explicit Reject. It is NOT cleared by invoking the
	// declarer, which is the difference between "the requirement is satisfied" and
	// "the requirement was attempted".
	pending bool
}

// enforcement is what one declarer's OWN bound requires of one call's assembled
// argument size. It is per declarer because a chain can carry several declared
// bounds at once, and honouring the widest one says nothing about the others.
type enforcement int

const (
	// enforcementInvoke means the document is inside the declarer's own bound, so
	// the declarer is invoked exactly like any other.
	enforcementInvoke enforcement = iota
	// enforcementReject means the document is past the declarer's own bound and it
	// declared [toolcall.OverflowReject], so the call is refused closed before the
	// declarer is handed a document it declared it cannot need.
	enforcementReject
	// enforcementPassThrough means the document is past the declarer's own bound
	// and it declared [toolcall.OverflowPassThrough], which promises the
	// pre-existing behavior past the declared bound: the declarer is not invoked.
	enforcementPassThrough
)

// enforcementFor reports what this declarer's own published bound requires for a
// document of argsBytes. An unusable declaration can be honored at no size, so it
// requires a refusal at every size.
//
// The bound is compared against the document AS IT WOULD BE HANDED TO THIS
// DECLARER, not against the original fragments: a declarer that must reconstruct
// a document needs the document it is given, so a bound exceeded only after an
// earlier finalizer rewrote the arguments is exceeded for real.
func (c *callRequirement) enforcementFor(argsBytes int) enforcement {
	if !c.decl.valid {
		return enforcementReject
	}
	if argsBytes <= c.decl.spec.MaxArgsBytes {
		return enforcementInvoke
	}
	if c.decl.spec.Overflow == toolcall.OverflowReject {
		return enforcementReject
	}
	return enforcementPassThrough
}

// callRequirements is the set of APPLICABLE mandatory requirements for one tool
// call, initially derived at call start and extended for remaining finalizers
// when an actual tool-name rewrite makes another declaration applicable.
type callRequirements struct {
	// items holds one entry per declarer whose published scope covers this call,
	// in chain order.
	items []callRequirement

	// limitBytes is this call's physical buffering ceiling: the shared legacy
	// bound raised to the widest APPLICABLE declared bound. It is never raised by a
	// declaration that does not govern the call, so a call no declaration covers
	// is bounded exactly as it was before the capability existed.
	limitBytes int
}

// add records that decl governs this call and widens the call's physical ceiling
// to the declarer's own bound.
//
// The requirement starts satisfied for a declarer that only OBSERVES: it declared
// [toolcall.CompletenessBestEffort], so nothing can leave it pending and therefore
// nothing can refuse a call through it. A declarer that must DECIDE starts pending,
// and only a usable result from it clears that.
func (r *callRequirements) add(decl *mandatoryDeclaration) {
	item := callRequirement{decl: decl, pending: decl.requiresDecision()}
	// Initial derivation appends in chain order. Dynamic activation can insert a
	// previously inapplicable declarer before an existing one; retain chain order
	// so firstPendingID still identifies the first undecided participant.
	if len(r.items) > 0 && r.items[len(r.items)-1].decl.index > decl.index {
		index := sort.Search(len(r.items), func(i int) bool { return r.items[i].decl.index > decl.index })
		r.items = slices.Insert(r.items, index, item)
	} else {
		r.items = append(r.items, item)
	}
	if decl.valid && decl.spec.MaxArgsBytes > r.limitBytes {
		r.limitBytes = decl.spec.MaxArgsBytes
	}
}

// requirementFor returns the applicable requirement declared by the finalizer at
// chain position index, or nil when that finalizer declared nothing applicable to
// this call. A nil receiver answers nil, which is what keeps a call with no
// applicable requirement on the identical legacy path.
func (r *callRequirements) requirementFor(index int) *callRequirement {
	if r == nil {
		return nil
	}
	for i := range r.items {
		if r.items[i].decl.index == index {
			return &r.items[i]
		}
	}
	return nil
}

// pendingCount is how many applicable requirements have not yet been decided.
func (r *callRequirements) pendingCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for i := range r.items {
		if r.items[i].pending {
			n++
		}
	}
	return n
}

// firstPendingID is the deterministic identity of the first declarer that has
// not decided yet, for diagnostics only. It names the participant a refusal is
// about, which for two mandatory declarers is the SECOND one rather than
// whichever happened to run first.
func (r *callRequirements) firstPendingID() string {
	if r == nil {
		return ""
	}
	for i := range r.items {
		if r.items[i].pending {
			return r.items[i].decl.id
		}
	}
	return ""
}

// refusesPastLimit reports whether some applicable declaration forbids releasing
// a call that outgrew this call's buffering ceiling. A call with no applicable
// requirement never refuses here, whatever else is on the chain.
func (r *callRequirements) refusesPastLimit() bool {
	if r == nil {
		return false
	}
	for i := range r.items {
		if r.items[i].decl.requiresRejectPastOwnBound() {
			return true
		}
	}
	return false
}

// firstRefusing returns the deterministic first applicable declarer that forbids
// releasing this call past its own bound.
func (r *callRequirements) firstRefusing() *mandatoryDeclaration {
	if r == nil {
		return nil
	}
	for i := range r.items {
		if r.items[i].decl.requiresRejectPastOwnBound() {
			return r.items[i].decl
		}
	}
	return nil
}

// unusableDeclarationRefusal is the refusal for a call governed by a
// present-but-malformed declaration. It is returned before any finalizer runs, so
// no bound could have satisfied the requirement and nothing may be released.
func (r *callRequirements) unusableDeclarationRefusal(toolCallID string) error {
	if r == nil {
		return nil
	}
	for i := range r.items {
		if !r.items[i].decl.valid {
			return &MandatoryBufferingError{
				Reason:      ReasonMandatoryBufferingDeclarationInvalid,
				FinalizerID: r.items[i].decl.id,
				ToolCallID:  toolCallID,
			}
		}
	}
	return nil
}

// refusalPastLimit is the typed, content-free refusal for a call that outgrew
// this call's buffering ceiling while an applicable declaration forbade it. The
// reported bound is the ceiling that was exceeded, and the reported declarer is
// the deterministic first one that forbids the release.
func (r *callRequirements) refusalPastLimit(toolCallID string) error {
	decl := r.firstRefusing()
	if decl == nil {
		return nil
	}
	if !decl.valid {
		return &MandatoryBufferingError{
			Reason:      ReasonMandatoryBufferingDeclarationInvalid,
			FinalizerID: decl.id,
			ToolCallID:  toolCallID,
		}
	}
	return &MandatoryBufferingError{
		Reason:       ReasonMandatoryBufferingOverflow,
		FinalizerID:  decl.id,
		ToolCallID:   toolCallID,
		MaxArgsBytes: r.limitBytes,
	}
}

// overflowRefusal is the typed, content-free refusal for a call past ONE
// declarer's own bound, with that declarer's own bound as the reported value. It
// is what makes a declaration enforceable independently of every wider cap on the
// same chain.
func overflowRefusal(toolCallID, finalizerID string, maxArgsBytes int) error {
	return &MandatoryBufferingError{
		Reason:       ReasonMandatoryBufferingOverflow,
		FinalizerID:  finalizerID,
		ToolCallID:   toolCallID,
		MaxArgsBytes: maxArgsBytes,
	}
}

// refusalIncomplete is the typed, content-free refusal for a call whose
// applicable requirements could not all be decided because an unrelated
// finalizer failed first, or because a declarer failed itself. No bound was
// exceeded, so MaxArgsBytes stays zero and FinalizerID names the declarer that
// had not decided.
func refusalIncomplete(toolCallID, finalizerID string) error {
	return &MandatoryBufferingError{
		Reason:      ReasonMandatoryBufferingIncomplete,
		FinalizerID: finalizerID,
		ToolCallID:  toolCallID,
	}
}

// refusal wraps a typed refusal with the chokepoint that produced it, so a
// caller can classify it with errors.Is/errors.As without parsing text.
func refusal(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("tool call finalization: %w", err)
}

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Kept equal to toolcallrepair repair.DefaultMaxArgsBytes by
// TestDefaultToolCallFinalizationMaxArgsBytesMatchCore.
const defaultToolCallFinalizationMaxArgsBytes = 64 * 1024

// Per-call bounds do not bound an interleaved stream. These attempt-local
// limits bound concurrent argument payloads, empty-fragment overhead, and
// identity bookkeeping independently. Payloads also exist in original events
// and growable assembly slices; this is a logical byte budget, not an RSS cap.
const (
	maxActiveToolCallBuffers = 16
	maxAttemptToolArgsBytes  = 2 * aipapi.MaxEventDeltaBytes
	maxToolCallFragments     = aipapi.MaxItems
)

// ErrToolCallBufferBudget signals exhaustion of attempt-local assembly resources.
// It carries no tool identity or argument content.
var ErrToolCallBufferBudget = errors.New("tool call assembler: attempt buffering budget exhausted")

type toolCallBuffer struct {
	id           string
	name         string
	messageIndex int
	originals    []aipapi.Event
	args         []byte

	// requirements is the set of APPLICABLE mandatory completeness requirements for
	// this call, derived once at call start from the tool name, the tool
	// definition, and the catalog. It is nil for a call no declaration governs,
	// which is what keeps that call byte-identical to the pre-existing behavior.
	requirements *callRequirements

	// mandatorySafe is the document a declarer that published an applicable
	// mandatory completeness requirement was shown, and returned a usable result
	// for. It is retained so a LATER finalizer's unusable result can never fall
	// back on the original fragments over a decision that was already made on it.
	//
	// It is nil until such a requirement has actually been satisfied for this call,
	// so the retention is exactly ONE bounded reference per active tool call and
	// never a growing store: it is written only by a declarer that has just
	// decided, and it dies with the buffer. See
	// [toolCallAssembler.unusableResult] for the decision it drives.
	mandatorySafe *toolCallDocument
}

// toolCallDocument is one complete tool-call document: the arguments the assembler
// is carrying for a call plus the tool name they belong to.
type toolCallDocument struct {
	name string
	args []byte
	// rewrote records whether this document already differs from the original
	// fragments, which is exactly the condition under which the release synthesizes
	// a new canonical lifecycle instead of replaying those fragments unchanged.
	rewrote bool
}

// retainMandatorySafe records the document the assembler is carrying as this call's
// mandatory-safe result.
//
// It is called only on an applicable DECLARER's own usable result, so it is called
// at most once per declarer and at most once per call in the single-declarer case.
// The arguments it stores are the private copy the assembler already hands to the
// next finalizer, so nothing is copied here and nothing the stored slice aliases
// can still be mutated: every later rewrite REPLACES the assembler's arguments
// with a fresh copy rather than editing them in place.
func (b *toolCallBuffer) retainMandatorySafe(name string, args []byte, rewrote bool) {
	b.mandatorySafe = &toolCallDocument{name: name, args: args, rewrote: rewrote}
}

// mandatorySafeRelease is the release lifecycle for this call's retained
// mandatory-safe result: the synthesized canonical lifecycle when the document
// already differs from the original fragments, and the untouched fragments when it
// does not.
//
// The second branch is byte-identical to the pre-existing replay, which is what
// keeps a call whose declaring finalizer changed nothing - or a call with no
// earlier rewrite in front of it - behaving precisely as it did before, including
// the fragment boundaries a client already saw.
func (b *toolCallBuffer) mandatorySafeRelease() []aipapi.Event {
	if b.mandatorySafe == nil {
		return nil
	}
	if b.mandatorySafe.rewrote {
		return synthesizeRewriteLifecycle(b, b.mandatorySafe.name, b.mandatorySafe.args)
	}
	return slices.Clone(b.originals)
}

// toolCallAssembler is owned by a single retryRecvStream and driven only from
// that stream's Recv loop (no concurrent access).
type toolCallAssembler struct {
	finalizers   []toolcall.Finalizer
	maxArgsBytes int
	catalog      []aipapi.ToolDef

	// mandatory is the projection of every finalizer that publishes the
	// optional toolcall.BufferingRequirement capability: one table entry per
	// declarer, holding that declarer's own validated bound and its own
	// applicability capability. It owns no per-call state; maxArgsBytes keeps its
	// pre-existing meaning as the clamped legacy shared bound, which is the FLOOR
	// of every per-call buffering ceiling, not something any finalizer reads here.
	mandatory mandatoryBuffering

	active      map[string]*toolCallBuffer
	passThrough map[string]struct{}
	completed   map[string]struct{}
	// refusing holds tool calls already refused closed past their applicable
	// buffering ceiling, together with the refusal they will produce. Every later
	// fragment is held and dropped so that no possibly alias-bearing argument is
	// released, and only the finished event yields the typed refusal.
	refusing map[string]error
	drain    []aipapi.Event
	// drainErr is delivered only after the preserved lifecycle has drained.
	drainErr error
}

func newToolCallAssembler(finalizers []toolcall.Finalizer, maxArgsBytes int, catalog []aipapi.ToolDef) *toolCallAssembler {
	fs := toolcall.MaterializeSorted(finalizers)
	if len(fs) == 0 || len(catalog) == 0 {
		return nil
	}
	maxArgsBytes = clampToolCallFinalizationMaxArgsBytes(maxArgsBytes)
	return &toolCallAssembler{
		finalizers:   fs,
		maxArgsBytes: maxArgsBytes,
		catalog:      cloneToolCatalog(catalog),
		mandatory:    resolveMandatoryBuffering(fs, maxArgsBytes),
		active:       make(map[string]*toolCallBuffer),
		passThrough:  make(map[string]struct{}),
		completed:    make(map[string]struct{}),
		refusing:     make(map[string]error),
	}
}

func clampToolCallFinalizationMaxArgsBytes(maxArgsBytes int) int {
	if maxArgsBytes <= 0 {
		return defaultToolCallFinalizationMaxArgsBytes
	}
	if maxArgsBytes > aipapi.MaxEventDeltaBytes {
		return aipapi.MaxEventDeltaBytes
	}
	return maxArgsBytes
}

func (a *toolCallAssembler) enabled() bool {
	return a != nil && len(a.finalizers) > 0 && len(a.catalog) > 0
}

// hasActiveCalls reports that this assembler is still holding at least one
// ordinary call whose canonical lifecycle has not been produced yet. Such a call
// contributed no client-visible boundary at all, so any consumer that must reason
// about the complete ordinary boundary of a held candidate cannot represent it and
// has to treat it as unresolved.
//
// A call refused closed for exceeding an applicable mandatory bound counts too:
// it left active for refusing, but its refusal is still pending - no finished
// event has released or reported it - so a completion boundary drawn now would
// publish a successful answer over an undecided overflow. Only a call the
// assembler already released (pass-through) or decided (completed or refused
// at finish) is resolved.
//
// The assembler is owned by the single receive loop, so this is a plain read at
// that owner's serialization boundary and takes no lock.
func (a *toolCallAssembler) hasActiveCalls() bool {
	return a != nil && (len(a.active) > 0 || len(a.refusing) > 0)
}

func (a *toolCallAssembler) popDrain() (aipapi.Event, bool) {
	if a == nil {
		return aipapi.Event{}, false
	}
	if len(a.drain) == 0 {
		return aipapi.Event{}, false
	}
	ev := a.drain[0]
	a.drain[0] = aipapi.Event{} // drop large Delta references from the backing array
	a.drain = a.drain[1:]
	if len(a.drain) == 0 {
		a.drain = nil
	}
	return ev, true
}

func (a *toolCallAssembler) enqueue(evs ...aipapi.Event) {
	if a == nil || len(evs) == 0 {
		return
	}
	a.drain = append(a.drain, evs...)
}

// prepend puts events at the front of the drain in their given order, ahead of
// events already queued. A withhold release must prepend: its held output is
// earlier than any assembler output still queued, so appending would replay in
// the wrong order.
func (a *toolCallAssembler) prepend(evs ...aipapi.Event) {
	if a == nil || len(evs) == 0 {
		return
	}
	combined := make([]aipapi.Event, 0, len(evs)+len(a.drain))
	combined = append(combined, evs...)
	combined = append(combined, a.drain...)
	a.drain = combined
}

func (a *toolCallAssembler) popDrainError() error {
	if a == nil || len(a.drain) != 0 {
		return nil
	}
	err := a.drainErr
	a.drainErr = nil
	return err
}

// hasDrainError reports whether a finalizer error is waiting to surface once the
// preserved lifecycle has drained. It reads the pending error without consuming
// it, so a correction withhold can release its held output before the error
// reaches the client.
func (a *toolCallAssembler) hasDrainError() bool {
	return a != nil && len(a.drain) == 0 && a.drainErr != nil
}

// completionError closes unresolved mandatory work at a stream boundary.
// Cancellation retains its own terminal cause; EOF and response_finished must
// not turn a previously decided refusal into successful completion.
func (a *toolCallAssembler) completionError() error {
	if a == nil {
		return nil
	}
	ids := make([]string, 0, len(a.refusing)+len(a.active))
	for id := range a.refusing {
		ids = append(ids, id)
	}
	for id, buf := range a.active {
		if buf.requirements.pendingCount() > 0 || buf.requirements.unusableDeclarationRefusal(id) != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil
	}
	id := ids[0]
	if err := a.refusing[id]; err != nil {
		return err
	}
	if err := a.active[id].requirements.unusableDeclarationRefusal(id); err != nil {
		return refusal(err)
	}
	return refusal(refusalIncomplete(id, a.active[id].requirements.firstPendingID()))
}

// ingest handles one backend event after BTP. held=true means the event must not
// continue on the normal tool path; any finalized lifecycle is queued on drain.
//
// steerGate is the synchronous steer decision for the current attempt, owned by
// the single receive loop: it performs no I/O, sees the call's current name and
// the finalizer's steer request, and marks the attempt when it accepts. A nil
// gate declines, so an ActionSteer with no gate releases the call exactly as an
// ActionPass does.
func (a *toolCallAssembler) ingest(ctx context.Context, ev aipapi.Event, meta toolcall.Meta, steerGate func(name string, req toolcall.SteerRequest) bool) (held bool, err error) {
	if !a.enabled() {
		return false, nil
	}
	if ev.Kind == aipapi.EventResponseFinished {
		err := a.completionError()
		return err != nil, err
	}
	switch ev.Kind {
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
	default:
		return false, nil
	}
	id := strings.TrimSpace(ev.ToolCallID)
	if id == "" {
		return false, nil
	}
	_, active := a.active[id]
	_, passing := a.passThrough[id]
	_, completed := a.completed[id]
	_, refusing := a.refusing[id]
	if !active && !passing && !completed && !refusing &&
		len(a.active)+len(a.passThrough)+len(a.completed)+len(a.refusing) >= aipapi.MaxItems {
		return true, ErrToolCallBufferBudget
	}
	if ev.Kind == aipapi.EventToolCallStarted && !active && !passing && !completed && !refusing && len(a.active) >= maxActiveToolCallBuffers {
		return true, ErrToolCallBufferBudget
	}
	if ev.Kind == aipapi.EventToolCallArgsDelta && active {
		if len(a.active[id].originals) >= maxToolCallFragments {
			return true, ErrToolCallBufferBudget
		}
		bytes := 0
		for _, buf := range a.active {
			bytes += len(buf.args)
		}
		if len(ev.Delta) > maxAttemptToolArgsBytes-bytes {
			return true, ErrToolCallBufferBudget
		}
	}

	if _, ok := a.passThrough[id]; ok {
		return false, nil
	}
	if _, ok := a.refusing[id]; ok {
		return a.ingestRefused(ev, id)
	}

	switch ev.Kind {
	case aipapi.EventToolCallStarted:
		return a.ingestStarted(ev, id), nil
	case aipapi.EventToolCallArgsDelta:
		return a.ingestDelta(ev, id), nil
	case aipapi.EventToolCallFinished:
		return a.ingestFinished(ctx, ev, id, meta, steerGate)
	default:
		return false, nil
	}
}

func (a *toolCallAssembler) ingestStarted(ev aipapi.Event, id string) bool {
	if _, done := a.completed[id]; done {
		a.passThrough[id] = struct{}{}
		return false
	}
	if buf, ok := a.active[id]; ok {
		a.enqueue(slices.Clone(buf.originals)...)
		a.enqueue(ev)
		delete(a.active, id)
		a.passThrough[id] = struct{}{}
		return true
	}
	a.active[id] = &toolCallBuffer{
		id:           id,
		name:         ev.ToolName,
		messageIndex: ev.MessageIndex,
		originals:    []aipapi.Event{ev},
		// Applicability is decided HERE, at call start, because it is what decides how
		// much of this call the assembler has to buffer at all. Deriving it later
		// could no longer change the buffering, which is exactly how a call a
		// selective declarer never inspects ended up refused for a bound that never
		// governed it.
		requirements: a.deriveCallRequirements(ev.ToolName),
	}
	return true
}

// deriveCallRequirements resolves the APPLICABLE mandatory completeness
// requirements for one tool call, from the tool name, the tool definition, and the
// catalog - and from nothing else.
//
// The three inputs are the only ones a declarer's own applicability answer is
// allowed to read, so no payload byte, request view, or leg identity can make a
// declarer enforce or skip its bound on this call. Reading them here rather than
// at composition time is what makes the result per call: the same chain answers
// differently for a tool one declarer was configured for and a tool it was not.
//
// A nil answer means no declaration governs the call, which is the signal for the
// whole call to keep its byte-identical pre-existing behavior: the shared legacy
// cap, the pre-existing release-the-fragments fallback, and no refusal.
func (a *toolCallAssembler) deriveCallRequirements(toolName string) *callRequirements {
	if !a.mandatory.declaresAny() {
		// No finalizer opted in at all: nothing about the pre-existing assembly
		// behavior can change for any call.
		return nil
	}
	// Applicability is authority, not a guess about a name another finalizer might
	// produce. A near-miss name must not inherit an exact-name requirement.
	tool := lookupToolDef(a.catalog, toolName)
	reqs := &callRequirements{limitBytes: a.maxArgsBytes}
	for _, decl := range a.mandatory.declarations {
		if !decl.appliesToTool(toolName, tool, a.catalog) {
			continue
		}
		reqs.add(decl)
	}
	if len(reqs.items) == 0 {
		return nil
	}
	// The per-call ceiling is the same maximum as the assembler-wide one taken over
	// a SUBSET of the same declarations, so it can never exceed it. Stating the
	// relationship here rather than assuming it keeps a future change to either
	// computation from quietly raising a call's buffering past the documented cap.
	if reqs.limitBytes > a.mandatory.assemblyMaxArgsBytes {
		reqs.limitBytes = a.mandatory.assemblyMaxArgsBytes
	}
	return reqs
}

// activateRemainingRequirements records authority acquired by an actual name
// rewrite. It runs before the next finalizer, not merely when the declarer is
// reached: an interposed optional failure must see the newly pending decision.
// Existing requirements are never reset or removed, and processed finalizers
// are never revisited.
func (a *toolCallAssembler) activateRemainingRequirements(buf *toolCallBuffer, afterIndex int, name string, argsBytes int) error {
	tool := lookupToolDef(a.catalog, name)
	for _, decl := range a.mandatory.declarations {
		if decl.index <= afterIndex || buf.requirements.requirementFor(decl.index) != nil {
			continue
		}
		if !decl.appliesToTool(name, tool, a.catalog) {
			continue
		}
		if buf.requirements == nil {
			buf.requirements = &callRequirements{limitBytes: a.maxArgsBytes}
		}
		buf.requirements.add(decl)
		if err := buf.requirements.unusableDeclarationRefusal(buf.id); err != nil {
			return refusal(err)
		}
		req := buf.requirements.requirementFor(decl.index)
		switch req.enforcementFor(argsBytes) {
		case enforcementReject:
			return refusal(overflowRefusal(buf.id, decl.id, decl.spec.MaxArgsBytes))
		case enforcementPassThrough:
			req.pending = false
		case enforcementInvoke:
		}
	}
	return nil
}

// ingestRefused keeps a tool call that is already refused closed entirely away
// from the client: every further fragment is held and dropped, and only the
// finished event yields the typed refusal that was already decided when the call
// outgrew its applicable ceiling. Nothing alias-bearing can therefore be released
// on a call whose mandatory requirement was not honored.
func (a *toolCallAssembler) ingestRefused(ev aipapi.Event, id string) (bool, error) {
	if ev.Kind != aipapi.EventToolCallFinished {
		return true, nil
	}
	err := a.refusing[id]
	delete(a.refusing, id)
	a.completed[id] = struct{}{}
	return true, err
}

func (a *toolCallAssembler) ingestDelta(ev aipapi.Event, id string) bool {
	buf, ok := a.active[id]
	if !ok {
		a.passThrough[id] = struct{}{}
		return false
	}
	delta := ev.Delta
	// The ceiling is this CALL's: the shared legacy bound, raised only by a
	// declaration that actually governs the tool this call names. A call no
	// declaration governs is bounded exactly as it was before the capability
	// existed, and can never be refused on account of a bound it never governed.
	limit := a.maxArgsBytes
	if reqs := buf.requirements; reqs != nil {
		limit = reqs.limitBytes
	}
	// Overflow-safe: never add len(buf.args)+len(delta) (can wrap on extreme caps).
	if len(buf.args) > limit || len(delta) > limit-len(buf.args) {
		if reqs := buf.requirements; reqs != nil && reqs.refusesPastLimit() {
			// An applicable declarer must never see the fragments replayed unchanged
			// past its own bound, and an unusable declaration can never be honored at
			// any bound. Refuse the call closed: the buffer is dropped without being
			// released, and the typed refusal is deferred to the finished event so no
			// alias-bearing argument reaches the client in the meantime.
			delete(a.active, id)
			a.refusing[id] = refusal(reqs.refusalPastLimit(id))
			return true
		}
		buf.originals = append(buf.originals, ev)
		a.enqueue(slices.Clone(buf.originals)...)
		delete(a.active, id)
		a.passThrough[id] = struct{}{}
		return true
	}
	buf.originals = append(buf.originals, ev)
	buf.args = append(buf.args, delta...)
	return true
}

func (a *toolCallAssembler) ingestFinished(ctx context.Context, ev aipapi.Event, id string, meta toolcall.Meta, steerGate func(name string, req toolcall.SteerRequest) bool) (bool, error) {
	buf, ok := a.active[id]
	if !ok {
		a.passThrough[id] = struct{}{}
		return false, nil
	}
	buf.originals = append(buf.originals, ev)
	delete(a.active, id)
	a.completed[id] = struct{}{}

	// A preserved lifecycle must cross the ordinary downstream stages before a
	// later optional finalizer's error terminalizes the attempt. Refusals have no
	// events and remain immediate; a downstream failure/cancellation still wins.
	emit, err := a.finalizeCall(ctx, buf, meta, steerGate)
	a.enqueue(emit...)
	if err != nil && len(emit) != 0 {
		a.drainErr = err
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

func (a *toolCallAssembler) finalizeCall(ctx context.Context, buf *toolCallBuffer, meta toolcall.Meta, steerGate func(name string, req toolcall.SteerRequest) bool) ([]aipapi.Event, error) {
	// An APPLICABLE declarer that published a present-but-unusable declaration
	// cannot be honored at any bound, and treating it as "did not opt in" would
	// downgrade a mandatory completeness requirement into optional pass-through
	// handling. Refuse this call closed before any finalizer runs and before any
	// original fragment is released. A declarer whose scope does not cover this
	// call has no entry here and cannot refuse it.
	if err := buf.requirements.unusableDeclarationRefusal(buf.id); err != nil {
		return nil, refusal(err)
	}

	name := buf.name
	args := append([]byte(nil), buf.args...)
	rewrote := false

	for index, fin := range a.finalizers {
		if fin == nil {
			continue
		}
		// req is this finalizer's own requirement for THIS call, or nil when it
		// declared none that covers it. Nothing about it changes the finalizer's
		// position in the chain or the order the chain runs in.
		req := buf.requirements.requirementFor(index)
		if req != nil {
			// Requirement 4.5: each declarer's OWN bound is enforced here, for this
			// call, rather than being assumed from the widest bound on the chain.
			// Without this, a declarer's smaller limit would be unenforceable
			// whenever a larger shared cap or a second declarer existed, because
			// aggregation can only ever take the maximum.
			switch req.enforcementFor(len(args)) {
			case enforcementReject:
				return nil, refusal(overflowRefusal(buf.id, req.decl.id, req.decl.spec.MaxArgsBytes))
			case enforcementPassThrough:
				// The declarer published OverflowPassThrough, whose promise is the
				// pre-existing behavior past its own bound: it is not invoked for this
				// call. Its requirement is discharged rather than left pending, since
				// waiting for a decision that will never be asked for would refuse
				// every such call instead of preserving it.
				req.pending = false
				continue
			case enforcementInvoke:
			}
		}
		// Defensive catalog copy per Finalize (ADR); assembler catalog is owned.
		catalogCopy := cloneToolCatalog(a.catalog)
		tool := lookupToolDef(catalogCopy, name)
		// Detached metadata per Finalize: the views are value structs, but
		// their maps and slices are shared with every finalizer on the chain
		// when handed over once. A finalizer that mutates what it was given
		// must not reach the next finalizer's decision or the producer's
		// snapshot through that sharing.
		finMeta := cloneFinalizerMeta(meta)
		call := toolcall.CompletedCall{
			ToolCallID: buf.id,
			ToolName:   name,
			ArgsJSON:   append([]byte(nil), args...),
		}
		op := "tool_call_finalizer:" + fin.ID()
		res, err := safety.CallValue(safety.BoundaryExtension, op, func() (toolcall.Result, error) {
			return fin.Finalize(ctx, call, tool, catalogCopy, finMeta)
		})
		if err != nil {
			return a.unusableResult(buf, err)
		}
		if res.Steer != nil && res.Action != toolcall.ActionSteer {
			// A steer request is meaningful only with ActionSteer. Any other
			// action carrying one is unusable and takes the pre-existing
			// unusable-result fallback instead of its own action's behavior, so a
			// stray field can never turn a pass, rewrite, or reject into a hidden
			// continuation.
			return a.unusableResult(buf, nil)
		}
		switch res.Action {
		case toolcall.ActionSteer:
			steer, ok := res.UsableSteer()
			if !ok {
				// A missing or invalid steer request is unusable, not a decision.
				return a.unusableResult(buf, nil)
			}
			if steerGate != nil {
				// The gate owns acceptance: it marks the attempt as withholding
				// when it accepts. Either answer continues the chain exactly as
				// if this finalizer had returned ActionPass.
				steerGate(name, *steer)
			}
			fallthrough
		case toolcall.ActionPass:
			if req != nil {
				// Only a binding decision protects this document against later
				// failures. An observer must not change legacy fallback semantics.
				req.pending = false
				if req.decl.requiresDecision() {
					buf.retainMandatorySafe(name, args, rewrote)
				}
			}
			continue
		case toolcall.ActionReject:
			if req != nil {
				req.pending = false
			}
			return nil, &toolcall.RejectError{ReasonCode: res.ReasonCode, ToolCallID: buf.id}
		case toolcall.ActionRewrite:
			if !rewriteEnvelopeValid(res) {
				// No error: the finalizer returned a well-formed Result the assembler
				// cannot use, so there is no failure of its own to surface. It is NOT a
				// decision either, so any requirement it declared is still pending.
				return a.unusableResult(buf, nil)
			}
			previousName := name
			name = strings.TrimSpace(res.ToolName)
			args = append([]byte(nil), res.ArgsJSON...)
			rewrote = true
			if req != nil {
				req.pending = false
				if req.decl.requiresDecision() {
					buf.retainMandatorySafe(name, args, true)
				}
			}
			if name != previousName {
				if err := a.activateRemainingRequirements(buf, index, name, len(args)); err != nil {
					return nil, err
				}
			}
		default:
			// Likewise for an action outside the closed vocabulary: unusable, but not
			// reported as an error by the finalizer that produced it, and not a
			// decision either.
			return a.unusableResult(buf, nil)
		}
	}
	if !rewrote {
		return slices.Clone(buf.originals), nil
	}
	return synthesizeRewriteLifecycle(buf, name, args), nil
}

// unusableResult is the assembler's single fallback for a finalizer that failed,
// panicked, or returned a result it cannot use. It has exactly three answers, and
// the pre-existing replay is the last of them:
//
//  1. an UNDECIDED applicable requirement refuses closed. The declarer has not
//     decided on these arguments, so releasing them is exactly the bypass the
//     capability exists to prevent (requirements 4.6 and 8.3). A declarer's OWN
//     error or panic lands here too, because being invoked is not deciding - which
//     is why the flag is cleared on the usable result rather than on the call;
//  2. every applicable requirement SATISFIED releases the document those declarers
//     were shown and accepted, PRESERVING the mandatory-safe result rather than
//     discarding it and replaying the original - possibly alias-bearing -
//     fragments over it. cause is the later finalizer's own failure and still
//     surfaces;
//  3. anything else - no applicable requirement, or requirements discharged by a
//     declarer's own pass-through policy - replays the original fragments
//     unchanged. For a call no declaration governs that is the pre-existing
//     assembler behavior, byte for byte.
//
// WHY THIS IS NOT AN ORDERING RULE. It reads no finalizer's position, reorders
// nothing, and changes nothing about how the chain is materialized: answer 2 is
// decided entirely from the requirements that are already satisfied, so it holds
// for a later finalizer at any order above the last satisfied declarer, and answer
// 3 is unchanged for every call that published no such requirement. Refusing
// instead of preserving was considered and is worse: it converts a correctly
// expanded call into a hard client reject because of a failure that has nothing to
// do with the decision already made.
func (a *toolCallAssembler) unusableResult(buf *toolCallBuffer, cause error) ([]aipapi.Event, error) {
	if reqs := buf.requirements; reqs != nil {
		if pending := reqs.pendingCount(); pending > 0 {
			return nil, refusal(refusalIncomplete(buf.id, reqs.firstPendingID()))
		}
		if buf.mandatorySafe != nil {
			return buf.mandatorySafeRelease(), cause
		}
	}
	return slices.Clone(buf.originals), nil
}

func cloneToolCatalog(catalog []aipapi.ToolDef) []aipapi.ToolDef {
	if len(catalog) == 0 {
		return nil
	}
	out := make([]aipapi.ToolDef, len(catalog))
	for i, t := range catalog {
		out[i] = t
		if t.Parameters != nil {
			out[i].Parameters = append([]byte(nil), t.Parameters...)
		}
	}
	return out
}

// cloneFinalizerMeta detaches one finalizer invocation's metadata from the
// producer snapshot and from every other finalizer on the chain.
//
// toolcall.Meta travels by value, but Scope, Session, and Workspace carry
// maps and slices by reference. Classification is scalar and needs no work;
// everything else reference-typed is cloned, preserving nils. A finalizer
// that writes through what it was handed then corrupts only its own copy,
// and the next finalizer still reads the authoritative views.
//
// The parameter itself is the working copy: meta is passed by value, so
// replacing its reference fields and returning it detaches the invocation
// without an extra named local.
func cloneFinalizerMeta(meta toolcall.Meta) toolcall.Meta {
	meta.Scope = meta.Scope.Clone()
	meta.Session.Labels = maps.Clone(meta.Session.Labels)
	meta.Workspace.Labels = maps.Clone(meta.Workspace.Labels)
	meta.Workspace.Markers = slices.Clone(meta.Workspace.Markers)
	return meta
}

func rewriteEnvelopeValid(res toolcall.Result) bool {
	return strings.TrimSpace(res.ToolName) != "" && res.ArgsJSON != nil && json.Valid(res.ArgsJSON)
}

func synthesizeRewriteLifecycle(buf *toolCallBuffer, name string, args []byte) []aipapi.Event {
	return []aipapi.Event{
		{
			Kind:         aipapi.EventToolCallStarted,
			ToolCallID:   buf.id,
			ToolName:     name,
			MessageIndex: buf.messageIndex,
		},
		{
			Kind:         aipapi.EventToolCallArgsDelta,
			ToolCallID:   buf.id,
			ToolName:     name,
			Delta:        string(args),
			MessageIndex: buf.messageIndex,
		},
		{
			Kind:         aipapi.EventToolCallFinished,
			ToolCallID:   buf.id,
			ToolName:     name,
			MessageIndex: buf.messageIndex,
		},
	}
}

func lookupToolDef(catalog []aipapi.ToolDef, name string) aipapi.ToolDef {
	for _, t := range catalog {
		if t.Name == name {
			return t
		}
	}
	return aipapi.ToolDef{}
}

// Bounded, attempt-local capture of the one proxy-owned control call for
// task 4.1 of agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Capture State; requirements 3.2, 3.5, 5.1-5.6, 8.5, 10.3).
//
// This file owns capture only. It is not wired into the response pipeline, it
// never calls Provider.Handle, and it names no concrete feature: ownership comes
// from the trusted activation the request path already froze, so the only control
// truth it reads is that activation's immutable projection. Wording, provider
// keys, and schema stay the control provider's own policy, so nothing here
// interprets an argument member or a value.
//
// The capture is single-owner, exactly like the ordinary tool-call assembler it
// will run ahead of: one backend Recv loop drives it and no other goroutine may
// touch it, so it owns no lock, no timer, and no background work.
package runtime

import (
	"crypto/sha256"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// Sticky, bounded, content-free protocol reasons. None of them ever carries a
// call ID, a tool name, or an argument byte.
const (
	// controlReasonIDInvalid marks a call ID that can never form a valid
	// CompletedCall: empty, whitespace-only, invalid UTF-8, or longer than the
	// SDK identifier bound while a canonical event may legally carry 8 KiB.
	controlReasonIDInvalid = "control_call_id_invalid"
	// controlReasonDuplicateStart marks a second start for an already claimed
	// control call ID.
	controlReasonDuplicateStart = "control_call_duplicate_start"
	// controlReasonDuplicateFinish marks a second finish for one control call.
	controlReasonDuplicateFinish = "control_call_duplicate_finish"
	// controlReasonMultipleCalls marks a second distinct control call, which V1
	// never admits per response.
	controlReasonMultipleCalls = "control_call_multiple_calls"
	// controlReasonNameConflict marks a lifecycle event that reuses a claimed
	// control call ID under a different tool name.
	controlReasonNameConflict = "control_call_name_conflict"
	// controlReasonArgsAfterFinish marks arguments streamed after the finish.
	controlReasonArgsAfterFinish = "control_call_args_after_finish"
	// controlReasonArgsOverflow marks arguments beyond the frozen args budget.
	controlReasonArgsOverflow = "control_call_args_overflow"
	// controlReasonArgsMalformed marks captured arguments that are not a bounded,
	// syntactically well-formed JSON document within the canonical depth bound.
	controlReasonArgsMalformed = "control_call_args_malformed"
	// controlReasonBeforeStart marks a directly named control fragment that
	// arrives with no started control call behind it.
	controlReasonBeforeStart = "control_call_before_start"
	// controlReasonResultObserved marks a tool result for a claimed control call
	// ID. The proxy owns that execution, so a client-side result is private
	// input and can never become ordinary release.
	controlReasonResultObserved = "control_call_result_observed"
	// controlReasonMalformedItem marks a control-owned tool call that shares its
	// canonical carrier with a tool result. The shared backend receive does not
	// validate the event envelope before this stage, so such a mixed carrier can
	// reach the capture intact; it is private malformed input that can never be
	// handed off.
	controlReasonMalformedItem = "control_call_malformed_item"
	// controlReasonUnterminated marks a started control call that the response
	// closed without finishing.
	controlReasonUnterminated = "control_call_unterminated"
)

// controlClaimedIDCapacity is the fixed number of distinct control call IDs one
// response may correlate. V1 admits exactly one legitimate control call; the
// remaining slots exist so a malformed sequence that keeps inventing control IDs
// still swallows its own name-less fragments instead of leaking them to the
// ordinary client tool path. Exceeding it is a bounded protocol error, never a
// forgotten ID.
const controlClaimedIDCapacity = 16

// controlCallMaxJSONDepth is the canonical JSON depth bound a completed control
// call's arguments must respect.
const controlCallMaxJSONDepth = aipapi.MaxJSONDepth

// errControlCallCorrelationExhausted is the static, content-free protocol error
// a caller receives when correlation privacy can no longer be preserved inside
// the bounded correlation window. The response owner must abort the attempt
// safely rather than continue with a partially forgotten control call.
var errControlCallCorrelationExhausted = errors.New("runtime: control tool call correlation capacity exhausted")

// controlCallObservation is the per-event verdict of the private capture. A
// caller drops Claimed events before the ordinary tool-call assembler, its
// finalizers, tool policy, tool reactors, and client release; it invokes the
// control provider at most once, on the single Completed event.
type controlCallObservation struct {
	claimed   bool
	completed bool
	call      controltool.CompletedCall
	reason    string
	fatal     error
}

// Claimed reports whether the event stays inside the private protocol path and
// must not reach the ordinary tool path or the client.
func (o controlCallObservation) Claimed() bool { return o.claimed }

// Completed reports whether this event closed the one legitimate control
// invocation. It is true at most once per capture, so the provider can never be
// invoked twice for one response.
func (o controlCallObservation) Completed() bool { return o.completed }

// Call returns the completed invocation. The caller owns its bytes; they never
// alias a canonical event or the capture's own buffer.
func (o controlCallObservation) Call() controltool.CompletedCall { return o.call }

// Invalid reports whether the capture holds a sticky protocol-invalid reason.
func (o controlCallObservation) Invalid() bool { return o.reason != "" }

// Reason returns the bounded, content-free invalid reason, or "".
func (o controlCallObservation) Reason() string { return o.reason }

// Fatal returns the bounded protocol error that correlation privacy could not be
// preserved, or nil.
func (o controlCallObservation) Fatal() error { return o.fatal }

// controlCallCapture is the private, attempt-local owner of the response-side
// control-call state machine. It is discarded with its response, is never shared
// across attempts, and holds no global state.
type controlCallCapture struct {
	// toolName and maxArgs are frozen values copied from the approved projection.
	// The name comparison is exact and case-sensitive; the budget is never
	// recomputed from a spec, a request, or a wire event.
	toolName string
	maxArgs  int

	// claimed is the bounded correlation set. Only fixed-size SHA-256 digests of
	// claimed call IDs are kept, so an oversized opaque ID still gets correlation
	// for its later name-less fragments while no raw oversized ID is retained.
	claimed  [controlClaimedIDCapacity][sha256.Size]byte
	claimedN int
	// fatalErr is sticky. Once correlation cannot be preserved, every further
	// control event reports it so the owner can abort safely.
	fatalErr error

	started    bool
	finished   bool
	idUnusable bool
	// callID is the exact opaque ID of the current call, retained only while it
	// is usable and released as soon as the call is handed off or invalidated.
	callID string
	// args is the bounded deep-owned argument buffer. It is allocated on first
	// use, never grows past maxArgs, and is released at handoff or invalidation.
	args      []byte
	failed    bool
	reason    string
	completed bool
}

// newControlCallCapture builds the capture for one response from the trusted
// activation frozen by the request path. An absent or inactive activation
// returns nil, so the ordinary response path performs no hashing, no allocation,
// and no comparison at all: the nil capture answers every event as unclaimed.
func newControlCallCapture(a *controlToolActivation) *controlCallCapture {
	if !a.active() {
		return nil
	}
	return &controlCallCapture{
		toolName: a.projection.ToolName(),
		maxArgs:  a.projection.MaxArgsBytes(),
	}
}

// observe decides one canonical backend event. Every non-control event, and
// every control event that belongs to an ordinary tool, returns the zero
// observation immediately: nothing is buffered, nothing is delayed, and the
// event is not rewritten.
func (c *controlCallCapture) observe(ev aipapi.Event) controlCallObservation {
	if c == nil {
		return controlCallObservation{}
	}
	switch ev.Kind {
	case aipapi.EventToolCallStarted:
		return c.observeStart(ev)
	case aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
		return c.observeFragment(ev)
	case aipapi.EventItem:
		return c.observeItem(ev)
	default:
		return controlCallObservation{}
	}
}

// observeStart handles a tool-call start. The prepared tool name is the only
// trustworthy ownership signal available on the wire, and it is trustworthy only
// because trusted activation already proved that no client-owned tool carries the
// same name. A start for an already claimed control ID under a different name is
// an identity conflict, not an ordinary tool.
func (c *controlCallCapture) observeStart(ev aipapi.Event) controlCallObservation {
	if ev.ToolName != c.toolName {
		if c.correlates(ev.ToolCallID) {
			return c.sunk(controlReasonNameConflict)
		}
		return controlCallObservation{}
	}
	return c.openCall(ev.ToolCallID)
}

// openCall is the single start choke point. It records the control call ID as a
// correlation key first, so a settled capture still keeps the new ID's later
// name-less fragments private, and only then decides whether the capture may
// reopen call state.
func (c *controlCallCapture) openCall(id string) controlCallObservation {
	already, fatal := c.claimID(id)
	if fatal != nil {
		return controlCallObservation{claimed: true, fatal: fatal}
	}
	if c.failed {
		// A settled capture is a sink: the new ID is now correlated, but no raw
		// identity is reopened, no argument byte is buffered, and no invocation
		// is ever handed off again.
		return c.sticky()
	}
	if already || c.started {
		reason := controlReasonMultipleCalls
		if already {
			reason = controlReasonDuplicateStart
		}
		return c.swallow(reason)
	}
	c.started = true
	if !controlCallIDUsable(id) {
		// The ID stays a correlation key so its later name-less fragments keep
		// being swallowed, but it can never form a valid call and no oversized
		// raw ID is retained. The protocol is invalid from here on, and no
		// argument byte is buffered for a call that cannot complete.
		c.idUnusable = true
		return c.swallow(controlReasonIDInvalid)
	}
	c.callID = id
	return controlCallObservation{claimed: true}
}

// observeFragment handles name-less args and finish events. They correlate only
// by an exactly claimed control call ID; nothing else can attach them to the
// protocol.
func (c *controlCallCapture) observeFragment(ev aipapi.Event) controlCallObservation {
	if !c.correlates(ev.ToolCallID) {
		if ev.ToolName != c.toolName {
			return controlCallObservation{}
		}
		// A directly named control fragment with no started call behind it is
		// malformed input. It is swallowed here, never handed to ordinary tool
		// processing, and its ID is recorded so later name-less fragments for it
		// stay swallowed too.
		reason := controlReasonBeforeStart
		if c.started {
			reason = controlReasonMultipleCalls
		}
		if _, fatal := c.claimID(ev.ToolCallID); fatal != nil {
			return controlCallObservation{claimed: true, fatal: fatal}
		}
		return c.sunk(reason)
	}
	if c.fatalErr != nil {
		return controlCallObservation{claimed: true, fatal: c.fatalErr}
	}
	if c.failed {
		return c.sticky()
	}
	if ev.ToolName != "" && ev.ToolName != c.toolName {
		return c.swallow(controlReasonNameConflict)
	}
	if c.finished {
		if ev.Kind == aipapi.EventToolCallFinished {
			return c.swallow(controlReasonDuplicateFinish)
		}
		return c.swallow(controlReasonArgsAfterFinish)
	}
	if ev.Kind == aipapi.EventToolCallArgsDelta {
		return c.observeArgs(ev.Delta)
	}
	return c.observeFinish(ev.ToolCallID)
}

// observeArgs appends one argument fragment. The budget is checked before any
// copy or append, in the overflow-safe subtraction form, so a single oversized
// chunk can never grow or allocate the buffer past the frozen budget.
func (c *controlCallCapture) observeArgs(delta string) controlCallObservation {
	if len(c.args) > c.maxArgs || len(delta) > c.maxArgs-len(c.args) {
		return c.swallow(controlReasonArgsOverflow)
	}
	// Growth is sized by the same clamp, because a plain append may round its
	// capacity up past the frozen budget.
	need := len(c.args) + len(delta)
	if need > cap(c.args) {
		size := max(need, 2*cap(c.args))
		buf := make([]byte, len(c.args), min(size, c.maxArgs))
		copy(buf, c.args)
		c.args = buf
	}
	c.args = append(c.args, delta...)
	return controlCallObservation{claimed: true}
}

// observeFinish closes the captured call. A completed invocation requires a start,
// a usable ID, bounded and well-formed complete arguments, and this finish.
func (c *controlCallCapture) observeFinish(id string) controlCallObservation {
	c.finished = true
	// Defensive identity invariants. A usable call ID is fixed at start and a
	// finish can only reach here by correlating to the live call, because every
	// other claimed-ID path is already sticky-invalid.
	if c.idUnusable || c.callID == "" {
		return c.swallow(controlReasonIDInvalid)
	}
	if id != c.callID {
		return c.swallow(controlReasonMultipleCalls)
	}
	if !controlArgsWellFormed(c.args, c.maxArgs) {
		return c.swallow(controlReasonArgsMalformed)
	}
	call := controltool.CompletedCall{ToolCallID: c.callID, ToolName: c.toolName, ArgsJSON: c.args}
	if err := controltool.ValidateCompletedCall(call, c.maxArgs); err != nil {
		return c.swallow(controlReasonIDInvalid)
	}
	c.completed = true
	c.release()
	return controlCallObservation{claimed: true, completed: true, call: call}
}

// observeItem handles the complete-call bypass. Both tool payloads of the
// carrier are judged together, before any ordinary early return. The shared
// backend receive does not validate the event envelope before this stage, so a
// carrier can present a tool call and a tool result at once, and neither payload
// may be allowed to hide proxy-owned control traffic behind the other.
func (c *controlCallCapture) observeItem(ev aipapi.Event) controlCallObservation {
	item := ev.Item
	if item == nil {
		return controlCallObservation{}
	}
	return c.observeItemPayloads(item.ToolCall, item.ToolResult)
}

// observeItemPayloads decides one canonical item from both of its tool identity
// payloads. A claimed tool result is private input whatever accompanies it; a
// prepared-name tool call is control-owned; and a control-owned call sharing its
// carrier with a result is malformed private input that can never be handed off.
// Only an item whose payloads are all unrelated to this response stays ordinary,
// because ordinary envelope validation, not this capture, owns a mixed shape
// that concerns no proxy-owned call.
//
// Ownership classification therefore precedes reason selection for a control-owned
// payload: a prepared-name call is proxy-owned whatever it shares its carrier
// with, so its call ID is recorded as a correlation key before any reason can
// short-circuit the item. Recording it later would leave its own name-less
// fragments to escape to the ordinary tool path, and would let a full correlation
// window swallow a new ID instead of raising the fatal the owner must abort on.
// Reason precedence is unchanged: a claimed result still outranks the mixed shape.
func (c *controlCallCapture) observeItemPayloads(call *aipapi.ToolCallItem, result *aipapi.ToolResultItem) controlCallObservation {
	controlOwned := call != nil && call.Name == c.toolName
	resultOwned := result != nil && c.correlates(result.CallID)
	if controlOwned && resultOwned {
		// Both payloads are proxy-owned. Record the prepared-name call ID before
		// the result verdict returns; a full window yields the static fatal, since
		// forgetting the ID is exactly what the bound exists to prevent.
		if _, fatal := c.claimID(call.CallID); fatal != nil {
			return controlCallObservation{claimed: true, fatal: fatal}
		}
		return c.sunk(controlReasonResultObserved)
	}
	if resultOwned {
		return c.sunk(controlReasonResultObserved)
	}
	if call == nil {
		return controlCallObservation{}
	}
	if call.Name != c.toolName {
		if c.correlates(call.CallID) {
			return c.sunk(controlReasonNameConflict)
		}
		return controlCallObservation{}
	}
	if result != nil {
		return c.rejectMixedControlItem(call)
	}
	return c.completeItemCall(call)
}

// rejectMixedControlItem settles a control-owned tool call that shares its
// carrier with a tool result. The call ID is still recorded as a bounded
// correlation key first, so that ID's later name-less fragments stay private,
// but no raw identity is stored, no argument byte is buffered, and the
// invocation is never handed off. A full correlation window yields the static
// fatal verdict instead.
func (c *controlCallCapture) rejectMixedControlItem(call *aipapi.ToolCallItem) controlCallObservation {
	if _, fatal := c.claimID(call.CallID); fatal != nil {
		return controlCallObservation{claimed: true, fatal: fatal}
	}
	return c.sunk(controlReasonMalformedItem)
}

// completeItemCall hands off one complete item-carried control invocation. The
// arguments are copied into an exactly sized owned buffer so the completed call
// owns its bytes, never aliases the upstream canonical item, and never exposes a
// capacity past the frozen budget.
func (c *controlCallCapture) completeItemCall(call *aipapi.ToolCallItem) controlCallObservation {
	already, fatal := c.claimID(call.CallID)
	if fatal != nil {
		return controlCallObservation{claimed: true, fatal: fatal}
	}
	if c.failed {
		// A settled capture is a sink: the new ID is now correlated, but the
		// handoff stays closed and the first reason is preserved.
		return c.sticky()
	}
	if already || c.started || c.completed {
		reason := controlReasonMultipleCalls
		if already {
			reason = controlReasonDuplicateStart
		}
		return c.swallow(reason)
	}
	// An item-carried control call is already a complete lifecycle, so the call
	// state is started and finished before its identity and arguments are judged.
	c.started = true
	c.finished = true
	if !controlCallIDUsable(call.CallID) {
		return c.swallow(controlReasonIDInvalid)
	}
	if !controlArgsWellFormed(call.Arguments, c.maxArgs) {
		return c.swallow(controlReasonArgsMalformed)
	}
	// The arguments are copied into a buffer sized to exactly the validated length
	// and never grown from, so the handed-off bytes cannot carry a capacity past
	// the frozen budget the way a growth-based clone rounds up on a non-power-of-two
	// budget. The copy runs after every size and shape check, so an invalid call is
	// never copied at all.
	ownedArgs := make([]byte, len(call.Arguments))
	copy(ownedArgs, call.Arguments)
	completed := controltool.CompletedCall{
		ToolCallID: call.CallID,
		ToolName:   c.toolName,
		ArgsJSON:   ownedArgs,
	}
	if err := controltool.ValidateCompletedCall(completed, c.maxArgs); err != nil {
		return c.swallow(controlReasonIDInvalid)
	}
	c.completed = true
	c.release()
	return controlCallObservation{claimed: true, completed: true, call: completed}
}

// closeResponse is the normal end of the response stream. An unfinished captured
// call is invalid, because a model that never finished its control invocation
// must not have partial arguments treated as a completion. A call that already
// completed stays valid so the terminal owner can still read its evidence, and a
// successful response finish is never confused with cancellation.
func (c *controlCallCapture) closeResponse() controlCallObservation {
	if c == nil {
		return controlCallObservation{}
	}
	// A revoked validity outranks a completed call: a malformed sequence after
	// handoff must still let the terminal owner clear any pending outcome.
	if c.failed {
		return c.sticky()
	}
	if c.completed {
		return controlCallObservation{}
	}
	if !c.started {
		return controlCallObservation{}
	}
	return c.swallow(controlReasonUnterminated)
}

// discard releases every private buffer, raw ID, and correlation key. It is the
// cancellation, attempt-loss, and replacement-owner path, so a replacement owner
// inherits no stale call ID and no stale argument bytes.
func (c *controlCallCapture) discard() {
	if c == nil {
		return
	}
	*c = controlCallCapture{toolName: c.toolName, maxArgs: c.maxArgs}
}

// invalid reports whether the capture holds a sticky protocol-invalid reason.
func (c *controlCallCapture) invalid() bool { return c != nil && c.failed }

// reasonCode returns the bounded, content-free invalid reason, or "".
func (c *controlCallCapture) reasonCode() string {
	if c == nil {
		return ""
	}
	return c.reason
}

// correlates reports whether id is one of the control call IDs this response
// claimed. The live call answers by exact string comparison; every other claim
// costs one bounded digest lookup, so a name-less fragment can never be
// attributed to an ordinary tool and an ordinary ID can never be attributed to
// the control protocol.
func (c *controlCallCapture) correlates(id string) bool {
	if c == nil {
		return false
	}
	if c.callID != "" && id == c.callID {
		return true
	}
	if c.claimedN == 0 {
		return false
	}
	key := sha256.Sum256([]byte(id))
	for i := range c.claimedKeys() {
		if c.claimed[i] == key {
			return true
		}
	}
	return false
}

// claimID records a control call ID as a correlation key. It reports whether the
// ID was already claimed, and a static protocol error when the bounded window is
// exhausted. An unusable ID is still recorded: swallowing its later name-less
// fragments is exactly the privacy property that makes an oversized ID safe.
func (c *controlCallCapture) claimID(id string) (already bool, fatal error) {
	if c.fatalErr != nil {
		return false, c.fatalErr
	}
	key := sha256.Sum256([]byte(id))
	for i := range c.claimedKeys() {
		if c.claimed[i] == key {
			return true, nil
		}
	}
	if c.claimedN == controlClaimedIDCapacity {
		c.fatalErr = errControlCallCorrelationExhausted
		return false, c.fatalErr
	}
	c.claimed[c.claimedN] = key
	c.claimedN++
	return false, nil
}

func (c *controlCallCapture) claimedKeys() [][sha256.Size]byte {
	return c.claimed[:c.claimedN]
}

// sunk keeps a control-owned event inside the private protocol path without
// disturbing a verdict the capture has already settled. A still-valid capture
// swallows the new reason; an already-failed one only reports.
func (c *controlCallCapture) sunk(reason string) controlCallObservation {
	if c.failed {
		return c.sticky()
	}
	return c.swallow(reason)
}

// sticky reports the capture's settled verdict and changes nothing. A fatal
// correlation failure outranks the nonfatal invalid reason, because only the
// fatal tells the caller to abort the attempt rather than merely treat the
// control call as failed. The first nonfatal reason is still the capture's own
// state, so diagnostics keep the reason the stream actually produced.
func (c *controlCallCapture) sticky() controlCallObservation {
	if c.fatalErr != nil {
		return controlCallObservation{claimed: true, fatal: c.fatalErr}
	}
	return controlCallObservation{claimed: true, reason: c.reason}
}

// swallow keeps an event inside the private protocol path and makes the invalid
// reason sticky. The first reason wins, so the protocol outcome stays stable for
// the rest of the response and no malformed event can ever become client tool
// execution.
func (c *controlCallCapture) swallow(reason string) controlCallObservation {
	if !c.failed {
		c.failed = true
		c.reason = reason
		c.release()
	}
	return c.sticky()
}

// release drops the private argument bytes and the exact raw call ID. A handed
// off CompletedCall owns the buffer from that moment, and an invalidated call
// keeps no argument bytes at all; only the fixed-size correlation digest stays.
func (c *controlCallCapture) release() {
	c.args = nil
	c.callID = ""
}

// controlCallIDUsable mirrors the SDK CompletedCall identity rules exactly:
// non-empty, within the 256-byte identifier bound, and valid UTF-8. A canonical
// event may legally carry an 8 KiB ID, so an oversized ID is invalid input rather
// than an out-of-range panic.
func controlCallIDUsable(id string) bool {
	return strings.TrimSpace(id) != "" &&
		len(id) <= controltool.MaxIdentifierBytes &&
		utf8.ValidString(id)
}

// controlArgsWellFormed is the generic control-call shape gate: a bounded, valid
// UTF-8 JSON document within the canonical depth bound. It is deliberately
// provider-neutral — it inspects no member name and no value, so provider keys
// and schema remain the control provider's own policy, which the provider still
// enforces in its strict decode.
//
// Every numeric limit is set explicitly from the frozen args budget rather than
// inherited from a jsonshape profile. A profile default would silently impose an
// unrelated envelope policy here (a 16 KiB key cap, object/array fan-out and
// number-width caps, or duplicate-name rejection) on a control call the provider
// has not decoded yet. Each structural count and decoded scalar is bounded by the
// raw JSON byte budget, so these limits are implied by the budget itself and
// reject nothing a valid document of that size can contain. ToolArgumentsLimits
// is deliberately not used: it adds duplicate-name rejection and the tool-repair
// fan-out policy, both of which belong to the provider.
func controlArgsWellFormed(args []byte, maxArgs int) bool {
	if len(args) == 0 || len(args) > maxArgs {
		return false
	}
	_, err := jsonshape.Preflight(args, jsonshape.Limits{
		MaxBytes:             int64(maxArgs),
		MaxDepth:             controlCallMaxJSONDepth,
		MaxTokens:            maxArgs,
		MaxArrayElems:        maxArgs,
		MaxObjectKeys:        maxArgs,
		MaxStringBytes:       maxArgs,
		MaxKeyBytes:          maxArgs,
		MaxNumberBytes:       maxArgs,
		RejectDuplicateNames: false,
	})
	return err == nil
}

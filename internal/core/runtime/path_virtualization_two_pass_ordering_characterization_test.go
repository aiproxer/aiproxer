package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 1.3. Requirements 5.2, 5.3, 5.4.
//
// Design sections consulted: "Existing Architecture and Placement" (the
// fourteen-step backend-bound order and its consequences), "Canonical Outbound
// Rewriter", "Testing Strategy / Runtime-integration", "Boundary Commitments /
// Out of Boundary", and "Revalidation Triggers".
//
// WHAT THIS TEST IS
//
// A permanent regression guard for the backend-bound two-pass ordering the
// feature will depend on. Task 1.3 asks for two orderings to be proved:
//
//  1. candidate attempt transforms run before the request-part hooks;
//  2. PTB capture and Backend.Open run after the request-part hooks and after the
//     final conversation-view reassertion.
//
// Both orderings already hold in the current runtime, so this test is GREEN by
// construction and is meant to stay green. It pins the ordering so that Tasks
// 5.1/5.2 - and any later refactor of the open-attempt sequence - cannot silently
// invert it. It is not a pending RED condition and it is not waiting on any
// Task 5.x deliverable: nothing this spec schedules is expected to change these
// assertions, and none of them is satisfiable or unsatisfiable by the feature
// plugin itself.
//
// WHAT IT DOES NOT CLAIM
//
// Nothing about the concrete feature, which does not exist yet. The two outbound
// passes below are test-local stand-ins installed on exactly the two planes the
// real feature will use (candidate attempt transform and request-part hook) and
// running exactly the pure prefix substitution the real rewriter runs. That is
// deliberate: it makes the ordering assertions about the runtime's own sequence,
// not about the feature, so the guard is meaningful before, during, and after
// Tasks 5.1/5.2 land.
//
// WHAT IT DELIBERATELY STAYS AWAY FROM
//
// design.md "Out of Boundary" removes conversation-view message visibility
// semantics (and the boundary context repeats that existing
// conversation-view projection retains its current ownership) from this spec.
// The fixture therefore keeps the conversation view non-trivial - one
// never_backend tag plus one active steering overlay resolved under the strict
// fail-closed anchor policy - but anchors that overlay on the FIRST USER message,
// which neither outbound pass mutates. Rewriting a message that an overlay is
// anchored on is an anchor-identity concern owned elsewhere; this task asserts
// only that the two passes and the backend-bound stages are ordered correctly
// and that no late stage restores the real prefix.
//
// EXTENSION POINTS USED
//
//	1. candidate attempt transform            request.AttemptTransform.HandleAttempt
//	2. candidate sizing / context eligibility  runtime.EligibilityResolver.Check
//	3. request-part hook (second pass)         sdkhooks.RequestPartHook.HandleRequestParts
//	4. final conversation-view reassertion     runtime.ConversationViewObserver, final stage
//	5. per-turn buffer materialization         traffic.Observer, proxy-to-backend leg
//	6. Backend.Open                            execbackend.Backend.Open
//
// No internal function name, private field, reflection probe, or call-graph
// shape is inspected; stage ordinals are assigned by the test's own observer
// callbacks. All failure messages are requirement-numbered and path-content-free
// (stage ordinals, counts, and byte totals only).
//
// Determinism: every checkpoint ordinal comes from one mutex-guarded monotonic
// counter, every scan walks slices (never a map), the shared rewriter re-marshals
// through encoding/json, which sorts object keys, and the executor RNG and clock
// are pinned, so repeated runs produce identical ordinals and byte totals.
//
// No production behavior changes here.

const (
	// twoPassRealRoot is the authoritative client-visible workspace project root
	// published by the workspace resolver. It is a fixed test fixture value.
	twoPassRealRoot = "/home/dev/workspaces/aip-path-virtualization-worktree/packages/agent-runtime"

	// twoPassVirtualRoot is the fixed V1 reserved alias form (POSIX flavor plus a
	// 20-character workspace tag) the outbound passes write onto the
	// path-bearing backend-effective surface.
	twoPassVirtualRoot = "/.__aip_v1__/w_0123456789abcdefghij/"

	// twoPassPathSuffix is the path-bearing suffix carried by the historical
	// argument value. It is identical in the real and virtualized forms, so any
	// difference at a later stage is attributable to the root prefix alone.
	twoPassPathSuffix = "src/main.go"

	twoPassToolName   = "read_file"
	twoPassToolCallID = "two-pass-1"
	twoPassAnchorText = "two-pass-first-user"
	twoPassSteerText  = "two-pass-steering"
	twoPassLocalText  = "two-pass-local-only"
	twoPassTailText   = "two-pass-continue"
)

// twoPassCheckpoint names one semantic checkpoint this characterization
// compares. The labels are stable failure-message tokens; the ordering itself is
// proven by the monotonic sequence each checkpoint records at run time.
const (
	twoPassCheckpointAttemptTransform = "attempt_transform"
	twoPassCheckpointRequestPartHook  = "request_part_hook"
	twoPassCheckpointViewReassert     = "conversation_view_reassertion"
	twoPassCheckpointPTB              = "per_turn_buffer"
	twoPassCheckpointBackendOpen      = "backend_open"
)

// twoPassRealRootBytes is the real-root prefix plus the segment separator the
// shared rewriter removes for every occurrence it virtualizes.
const twoPassRealRootBytes = len(twoPassRealRoot) + 1

// twoPassVirtualRootBytes is the reserved-alias prefix the rewriter substitutes.
const twoPassVirtualRootBytes = len(twoPassVirtualRoot)

// twoPassOccurrenceBytes is the per-occurrence saving one outbound pass makes.
const twoPassOccurrenceBytes = twoPassRealRootBytes - twoPassVirtualRootBytes

// twoPassRealArgumentDocument is the complete path-bearing tool-call argument
// document on the backend-effective history surface, real-root form.
func twoPassRealArgumentDocument() string {
	return `{"path":"` + twoPassRealRoot + "/" + twoPassPathSuffix + `","limit":10}`
}

// twoPassAnchorMessage is the first user message. It carries no path, so its
// message identity is stable across both outbound passes; that makes it the
// anchor target of the steering overlay without importing conversation-view
// anchor-identity semantics into this task.
func twoPassAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassAnchorText)}}
}

// twoPassToolCallMessage is the historical path-bearing tool call the client
// replays. It is the only surface the shared outbound rewriter mutates.
func twoPassToolCallMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: twoPassToolCallID,
		ToolName:   twoPassToolName,
		Content:    json.RawMessage(twoPassRealArgumentDocument()),
	}}}
}

// twoPassCall builds the ingress call: one path-bearing historical tool call,
// one never_backend-tagged local note, and one terminal forwardable user
// message.
func twoPassCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "two-pass:m"},
		Tools: []aipapi.ToolDef{{
			Name:       twoPassToolName,
			Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			twoPassAnchorMessage(),
			twoPassToolCallMessage(),
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassTailText)}},
		},
	}
}

// twoPassSnapshot is the frozen per-turn conversation view: one never_backend tag
// plus one active steering overlay anchored, under the strict fail-closed anchor
// policy, on the first user message. A non-trivial view is required so the final
// reassertion is a real stage rather than a no-op.
func twoPassSnapshot(t *testing.T) conversationprojection.Snapshot {
	t.Helper()
	local := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}}
	localID, err := conversationprojection.MessageIdentityOf(local)
	if err != nil {
		t.Fatalf("fixture: local message identity: %v", err)
	}
	anchorID, err := conversationprojection.MessageIdentityOf(twoPassAnchorMessage())
	if err != nil {
		t.Fatalf("fixture: anchor message identity: %v", err)
	}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: "two_pass_fixture"}},
		Steering: []conversationprojection.Overlay{{
			OverlayID:   "ov-two-pass",
			Revision:    1,
			SlotOrdinal: 1,
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: twoPassSteerText},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		}},
	}
}

// twoPassReader serves the frozen conversation view, matching the
// single-snapshot per-turn invariant.
type twoPassReader struct {
	mu    sync.Mutex
	snap  conversationprojection.Snapshot
	reads int
}

func (r *twoPassReader) Snapshot(context.Context, string) (conversationprojection.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	return r.snap, nil
}

func (r *twoPassReader) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// twoPassWorkspaceResolver publishes the authoritative project root the outbound
// passes derive their mapping from.
type twoPassWorkspaceResolver struct{ root string }

func (r twoPassWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: r.root}, nil
}

// twoPassScan is the path-content-free observation of one backend-effective
// surface: remaining real-root and reserved-alias occurrences on the
// path-bearing tool-call argument, how many argument documents were observed,
// the serialized call size a sizing observer would account for, that size
// expressed in real-root form, whether every argument document is still valid
// canonical JSON, and whether its non-path fields survived. No path value, path
// suffix, ID, or digest ever reaches a failure message through this struct.
type twoPassScan struct {
	realHits       int
	virtualHits    int
	argLeaves      int
	callBytes      int
	realFormSize   int
	validArgs      bool
	shapePreserved bool
}

// twoPassShapePreserved reports whether one tool-call argument document still
// carries exactly the selected path field plus its untouched non-path sibling.
func twoPassShapePreserved(doc []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return false
	}
	if len(fields) != 2 {
		return false
	}
	if _, ok := fields["path"]; !ok {
		return false
	}
	var limit int
	if err := json.Unmarshal(fields["limit"], &limit); err != nil {
		return false
	}
	return limit == 10
}

// twoPassScanCall measures the path-bearing argument of every historical tool
// call in the call's message authority plus the serialized call size.
func twoPassScanCall(call aipapi.Call) twoPassScan {
	out := twoPassScan{validArgs: true, shapePreserved: true}
	raw, err := json.Marshal(call)
	if err != nil {
		return twoPassScan{validArgs: false}
	}
	out.callBytes = len(raw)
	out.realFormSize = len(bytes.ReplaceAll(raw, []byte(twoPassVirtualRoot), []byte(twoPassRealRoot+"/")))
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != aipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			out.argLeaves++
			doc := string(part.Content)
			out.realHits += strings.Count(doc, twoPassRealRoot)
			out.virtualHits += strings.Count(doc, twoPassVirtualRoot)
			if !json.Valid(part.Content) {
				out.validArgs = false
				continue
			}
			if !twoPassShapePreserved(part.Content) {
				out.shapePreserved = false
			}
		}
	}
	if out.argLeaves == 0 {
		out.validArgs = false
	}
	return out
}

// twoPassScanDocument measures an already-serialized backend-bound body.
func twoPassScanDocument(body []byte) twoPassScan {
	var call aipapi.Call
	if err := json.Unmarshal(body, &call); err != nil {
		return twoPassScan{validArgs: false, callBytes: len(body)}
	}
	return twoPassScanCall(call)
}

// twoPassRewrite is the one pure rewriter both outbound passes share, matching
// design.md "Canonical Outbound Rewriter". It mutates only the path-bearing
// "path" leaf of completed historical tool-call argument documents, so an
// idempotent reapplication is observable as a zero rewrite count, and it reports
// how many occurrences it changed.
func twoPassRewrite(call *aipapi.Call) int {
	if call == nil {
		return 0
	}
	changed := 0
	for mi := range call.Messages {
		for pi := range call.Messages[mi].Parts {
			part := &call.Messages[mi].Parts[pi]
			if part.Kind != aipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			rewritten, hits := twoPassRewriteArgumentDocument(part.Content)
			if hits == 0 || !json.Valid(rewritten) {
				continue
			}
			part.Content = rewritten
			changed += hits
		}
	}
	return changed
}

// twoPassRewriteArgumentDocument substitutes the real root prefix with the fixed
// reserved alias on the path-bearing leaf only, leaving every other field and the
// JSON shape intact.
func twoPassRewriteArgumentDocument(doc []byte) ([]byte, int) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return doc, 0
	}
	raw, ok := fields["path"]
	if !ok {
		return doc, 0
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return doc, 0
	}
	if !strings.HasPrefix(value, twoPassRealRoot+"/") {
		return doc, 0
	}
	encoded, err := json.Marshal(twoPassVirtualRoot + strings.TrimPrefix(value, twoPassRealRoot+"/"))
	if err != nil {
		return doc, 0
	}
	fields["path"] = json.RawMessage(encoded)
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return doc, 0
	}
	return rewritten, 1
}

// twoPassEligibilityCall is one invocation of the candidate sizing / context
// eligibility port: the stage ordinal it ran at and the call it was shown.
//
// The port is invoked twice per attempt on the selected candidate: first
// preliminarily while the candidate is evaluated (the eligibility/preflight step
// that design.md lists right after the attempt transforms), and again when the
// runtime re-derives after the request-part hooks have run (the stage whose
// runtime log labels its decisions "post_request_hooks"). Both invocations use
// the same supported port, so a single-wins flag would silently pick one and
// hide the other; the recorder therefore keeps an ordered list of every call.
//
// The ordering assertions measure the FIRST (preliminary) invocation, because
// requirements.md 5.3 is about admission observing the savings produced by the
// early attempt transform - that is the only invocation whose input can have been
// changed by that transform. The later invocations are asserted separately to
// still see a virtualized representation, which is requirements.md 5.4. No
// assertion constrains how many times the port runs.
type twoPassEligibilityCall struct {
	stage int
	scan  twoPassScan
}

// twoPassRecorder is the single monotonic sequence shared by all checkpoints,
// plus the path-content-free observation each one made.
type twoPassRecorder struct {
	mu  sync.Mutex
	seq int

	attemptAt  int
	partAt     int
	reassertAt int
	ptbAt      int
	openAt     int

	attemptScan     twoPassScan
	attemptRewrites int
	eligCalls       []twoPassEligibilityCall
	partScan        twoPassScan
	partRewrites    int
	ptbScan         twoPassScan
	openScan        twoPassScan
	openCall        aipapi.Call
}

func (r *twoPassRecorder) mark(checkpoint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	switch checkpoint {
	case twoPassCheckpointAttemptTransform:
		r.attemptAt = r.seq
	case twoPassCheckpointRequestPartHook:
		r.partAt = r.seq
	case twoPassCheckpointViewReassert:
		r.reassertAt = r.seq
	case twoPassCheckpointPTB:
		r.ptbAt = r.seq
	case twoPassCheckpointBackendOpen:
		r.openAt = r.seq
	}
}

func (r *twoPassRecorder) noteAttempt(call aipapi.Call, rewrites int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attemptScan = twoPassScanCall(call)
	r.attemptRewrites = rewrites
}

func (r *twoPassRecorder) noteEligibility(call aipapi.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.eligCalls = append(r.eligCalls, twoPassEligibilityCall{stage: r.seq, scan: twoPassScanCall(call)})
}

func (r *twoPassRecorder) notePart(call aipapi.Call, rewrites int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partScan = twoPassScanCall(call)
	r.partRewrites = rewrites
}

func (r *twoPassRecorder) notePTB(scan twoPassScan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ptbScan = scan
}

func (r *twoPassRecorder) noteOpen(call aipapi.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openScan = twoPassScanCall(call)
	r.openCall = aipapi.CloneCall(call)
}

// twoPassStages holds this run's checkpoint ordinals. They are per-run counters
// and are only ever compared against other ordinals from the same run.
type twoPassStages struct {
	attempt  int
	part     int
	reassert int
	ptb      int
	open     int
}

// twoPassObservation is the complete path-content-free record of one scenario.
type twoPassObservation struct {
	order         twoPassStages
	turnResolved  bool
	snapshotReads int

	attemptRewrites int
	eligCalls       []twoPassEligibilityCall
	partRewrites    int

	attemptScan     twoPassScan
	partScan        twoPassScan
	ptbScan         twoPassScan
	openScan        twoPassScan
	backendOpenCall aipapi.Call

	steeringRestored     bool
	steeringAfterAnchor  bool
	localNoteFiltered    bool
	anchorMessagePresent bool
}

func (r *twoPassRecorder) observation(turnResolved bool, reads int) twoPassObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := r.openCall
	return twoPassObservation{
		order:         twoPassStages{attempt: r.attemptAt, part: r.partAt, reassert: r.reassertAt, ptb: r.ptbAt, open: r.openAt},
		turnResolved:  turnResolved,
		snapshotReads: reads,

		attemptRewrites: r.attemptRewrites,
		eligCalls:       append([]twoPassEligibilityCall(nil), r.eligCalls...),
		partRewrites:    r.partRewrites,

		attemptScan:     r.attemptScan,
		partScan:        r.partScan,
		ptbScan:         r.ptbScan,
		openScan:        r.openScan,
		backendOpenCall: call,

		steeringRestored:     twoPassCountText(call, twoPassSteerText) == 1,
		steeringAfterAnchor:  twoPassSteeredAfterAnchor(call),
		localNoteFiltered:    !twoPassHasText(call, twoPassLocalText),
		anchorMessagePresent: twoPassHasText(call, twoPassAnchorText),
	}
}

// twoPassOrder is the stage-ordinal report used in every ordering failure
// message. Only monotonic ordinals are emitted; nothing path-bearing, and no
// ordinal from another run, is ever mixed in.
type twoPassOrder struct {
	Attempt     int
	Eligibility []int
	PartHook    int
	Reassert    int
	PTB         int
	Open        int
}

// twoPassOrderOf renders one run's ordinals as a failure-message fragment.
func twoPassOrderOf(got twoPassObservation) twoPassOrder {
	order := twoPassOrder{
		Attempt:  got.order.attempt,
		PartHook: got.order.part,
		Reassert: got.order.reassert,
		PTB:      got.order.ptb,
		Open:     got.order.open,
	}
	for _, call := range got.eligCalls {
		order.Eligibility = append(order.Eligibility, call.stage)
	}
	return order
}

func (o twoPassOrder) String() string {
	stages := make([]string, 0, len(o.Eligibility))
	for _, stage := range o.Eligibility {
		stages = append(stages, strconv.Itoa(stage))
	}
	return fmt.Sprintf("attempt_stage=%d eligibility_stages=[%s] request_part_stage=%d reassert_stage=%d ptb_stage=%d backend_open_stage=%d",
		o.Attempt, strings.Join(stages, " "), o.PartHook, o.Reassert, o.PTB, o.Open)
}

// twoPassOutboundPass is the shared body of both outbound passes. When virtualize
// is false the pass still runs and still records its checkpoint, but it performs
// no rewrite, which is what the control scenario relies on.
type twoPassOutboundPass struct {
	rec        *twoPassRecorder
	virtualize bool
}

func (*twoPassOutboundPass) ID() string                        { return "path-virtualization-outbound" }
func (*twoPassOutboundPass) Order() int                        { return 0 }
func (*twoPassOutboundPass) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

// HandleAttempt implements request.AttemptTransform: the first outbound pass.
func (p *twoPassOutboundPass) HandleAttempt(
	_ context.Context,
	call *aipapi.Call,
	_ request.AttemptMeta,
	_ request.Services,
) (request.AttemptDecision, error) {
	p.rec.mark(twoPassCheckpointAttemptTransform)
	rewrites := 0
	if p.virtualize {
		rewrites = twoPassRewrite(call)
	}
	p.rec.noteAttempt(*call, rewrites)
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// HandleRequestParts implements sdkhooks.RequestPartHook: the second, idempotent
// outbound pass that reapplies the same pure rewrite after later shaping.
func (p *twoPassOutboundPass) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	p.rec.mark(twoPassCheckpointRequestPartHook)
	rewrites := 0
	if p.virtualize {
		rewrites = twoPassRewrite(call)
	}
	p.rec.notePart(*call, rewrites)
	return nil
}

// twoPassEligibilityObserver is the existing candidate sizing / context
// eligibility port. It records only the call it was shown, keeping requirements
// 5.3 and 5.4 observable without coupling to internal call-graph trivia.
type twoPassEligibilityObserver struct{ rec *twoPassRecorder }

func (o *twoPassEligibilityObserver) Check(
	_ context.Context,
	_ routing.AttemptCandidate,
	call aipapi.Call,
	facts modelcatalog.EffectiveFacts,
) modelcatalog.EligibilityDecision {
	o.rec.noteEligibility(call)
	return modelcatalog.EligibilityDecision{
		IsEligible: true,
		Reason:     modelcatalog.EligibilityEligible,
		Facts:      facts,
	}
}

// twoPassViewObserver records the final conversation-view projection stage, the
// supported seam for "final conversation-view reassertion".
type twoPassViewObserver struct{ rec *twoPassRecorder }

func (o *twoPassViewObserver) OnProjection(stage string, _ conversationprojection.ProjectionSummary) {
	if stage == conversationprojection.StageFinal {
		o.rec.mark(twoPassCheckpointViewReassert)
	}
}

func (*twoPassViewObserver) OnProjectionFailure(string) {}

func (*twoPassViewObserver) OnAnchorFallback(string, conversationprojection.AnchorMissingPolicy) {}

func (*twoPassViewObserver) OnAnchorFailure(conversationprojection.AnchorMissingPolicy) {}

// twoPassTrafficObserver records the proxy-to-backend leg, i.e. the per-turn
// buffer the runtime materializes immediately before Backend.Open.
type twoPassTrafficObserver struct{ rec *twoPassRecorder }

func (o *twoPassTrafficObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	if ev.Leg != traffic.LegPTB {
		return nil
	}
	o.rec.mark(twoPassCheckpointPTB)
	o.rec.notePTB(twoPassScanDocument(ev.Body))
	return nil
}

// twoPassBackend is the final provider translation boundary. It records the
// backend-bound call it was handed and returns a minimal finishing stream.
func twoPassBackend(rec *twoPassRecorder) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			rec.mark(twoPassCheckpointBackendOpen)
			rec.noteOpen(call)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

// twoPassRun executes one scenario: the same fixture, the same frozen
// conversation view, and the same two outbound passes, differing only in whether
// the passes rewrite the path-bearing history. Execute errors are reduced to a
// boolean because a denial reason can carry anchor identities and overlay IDs,
// which must never reach a failure message.
func twoPassRun(t *testing.T, virtualize bool) twoPassObservation {
	t.Helper()
	rec := &twoPassRecorder{}
	pass := &twoPassOutboundPass{rec: rec, virtualize: virtualize}
	reader := &twoPassReader{snap: twoPassSnapshot(t)}

	bus := hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{pass}})
	ex := runtime.TestExecutor()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua store: %v", err)
	}
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       twoPassWorkspaceResolver{root: twoPassRealRoot},
		TrafficObserver: &twoPassTrafficObserver{rec: rec},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{pass},
		}),
	})
	ex.ConversationViewReader = reader
	ex.ConversationViewObserver = &twoPassViewObserver{rec: rec}
	ex.EligibilityResolver = &twoPassEligibilityObserver{rec: rec}
	ex.Backends = map[string]execbackend.Backend{"two-pass": twoPassBackend(rec)}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	stream, err := ex.Execute(execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{}), twoPassCall())
	if err == nil {
		_, err = aipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}
	return rec.observation(err == nil, reader.readCount())
}

func twoPassCountText(call aipapi.Call, text string) int {
	count := 0
	scan := func(msgs []aipapi.Message) {
		for _, msg := range msgs {
			for _, part := range msg.Parts {
				if part.Text == text {
					count++
				}
			}
		}
	}
	scan(call.Instructions)
	scan(call.Messages)
	return count
}

func twoPassHasText(call aipapi.Call, text string) bool {
	return twoPassCountText(call, text) > 0
}

// twoPassSteeredAfterAnchor reports that the steering overlay was restored after
// its anchor message, i.e. that the frozen placement survived both outbound
// passes. Ordinals are positional, not stage ordinals.
func twoPassSteeredAfterAnchor(call aipapi.Call) bool {
	anchor := twoPassMessageOrdinal(call, twoPassAnchorText)
	steering := twoPassMessageOrdinal(call, twoPassSteerText)
	return anchor >= 0 && steering > anchor
}

func twoPassMessageOrdinal(call aipapi.Call, text string) int {
	ordinal := 0
	for _, msgs := range [][]aipapi.Message{call.Instructions, call.Messages} {
		for _, msg := range msgs {
			for _, part := range msg.Parts {
				if part.Text == text {
					return ordinal
				}
			}
			ordinal++
		}
	}
	return -1
}

// TestOutboundAttempt_BackendBoundTwoPassOrderingCharacterization is the
// permanent regression guard for requirements.md 5.2, 5.3 and 5.4.
//
// It drives exactly one outbound attempt through the real executor and asserts,
// from the semantic checkpoints of design.md "Existing Architecture and
// Placement", that:
//
//   - the candidate attempt transform runs before candidate sizing / context
//     eligibility and before the request-part hook;
//   - the request-part hook runs after the early rewrite and before the final
//     conversation-view reassertion;
//   - the reassertion, the per-turn buffer, and Backend.Open all run after the
//     request-part hook, in that order;
//   - the path rewrite performed by the early pass is still on the backend-bound
//     request, with no real-root occurrence reintroduced, and candidate sizing
//     observed the virtualized representation.
//
// A second run of the identical fixture with both passes inert supplies the
// rewrite-loss oracle: it reaches Backend.Open carrying the real-root history,
// so an early rewrite lost anywhere after the attempt transform would make the
// characterized run look exactly like it. Only content shapes and byte totals are
// compared across the two runs; stage ordinals are per-run counters and are only
// ever compared within a single run.
func TestOutboundAttempt_BackendBoundTwoPassOrderingCharacterization(t *testing.T) {
	t.Parallel()

	// Control: identical fixture, both outbound passes present but inert.
	control := twoPassRun(t, false)
	// Characterized: identical fixture, both outbound passes rewriting.
	got := twoPassRun(t, true)

	// Scaffolding guards. Every assertion below reads these, so they run first
	// and fail on their own when the fixture itself stops exercising the
	// sequence.
	if !control.turnResolved {
		t.Fatalf("fixture: the inert-pass control run must complete one turn: %s", twoPassOrderOf(control))
	}
	if !got.turnResolved {
		t.Fatalf("fixture: the rewriting run must complete one turn with no pre-backend denial: %s", twoPassOrderOf(got))
	}
	for _, checkpoint := range []struct {
		name  string
		stage int
	}{
		{"attempt transform", got.order.attempt},
		{"request-part hook", got.order.part},
		{"conversation reassert", got.order.reassert},
		{"per-turn buffer", got.order.ptb},
		{"Backend.Open", got.order.open},
	} {
		if checkpoint.stage == 0 {
			t.Fatalf("fixture: the %s checkpoint must be reached: %s", checkpoint.name, twoPassOrderOf(got))
		}
	}
	if len(got.eligCalls) == 0 {
		t.Fatalf("fixture: candidate sizing / context eligibility must be invoked: %s", twoPassOrderOf(got))
	}
	if got.attemptRewrites < 1 || got.attemptScan.virtualHits < 1 || got.attemptScan.realHits != 0 {
		t.Fatalf("fixture: the early outbound pass must virtualize the path-bearing tool history: %s rewrites=%d virtual_hits=%d real_hits=%d valid_json=%t",
			twoPassOrderOf(got), got.attemptRewrites, got.attemptScan.virtualHits, got.attemptScan.realHits, got.attemptScan.validArgs)
	}
	if got.snapshotReads < 1 {
		t.Fatalf("fixture: the frozen conversation view must be consulted at least once: reads=%d", got.snapshotReads)
	}

	// design.md "Existing Architecture and Placement": candidate attempt
	// transforms (step 2) -> candidate admission and preliminary
	// eligibility/preflight (step 3) -> request-part hooks (step 4).
	t.Run("attempt_transform_precedes_candidate_sizing_and_request_part_hook", func(t *testing.T) {
		order := twoPassOrderOf(got)
		first := got.eligCalls[0].stage
		if order.Attempt >= first {
			t.Fatalf("requirements.md 5.3 and design.md \"Existing Architecture and Placement\" step 2 before step 3 - the early outbound pass must precede preliminary candidate eligibility so sizing observes the virtualization savings: %s",
				order)
		}
		if first >= order.PartHook {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 3 before step 4 - preliminary candidate eligibility must run before the request-part hook, which is the second outbound pass: %s",
				order)
		}
	})

	// requirements.md 5.3: candidate sizing / context eligibility observes the
	// virtualized backend-effective representation, and the saving is real.
	t.Run("candidate_sizing_observes_virtualized_representation", func(t *testing.T) {
		order := twoPassOrderOf(got)
		for i, call := range got.eligCalls {
			if call.scan.virtualHits < 1 || call.scan.realHits != 0 {
				t.Fatalf("requirements.md 5.3/5.4 - candidate sizing invocation %d must be shown the virtualized representation, never the real-root one: %s invocation=%d virtual_hits=%d real_hits=%d",
					i, order, i, call.scan.virtualHits, call.scan.realHits)
			}
		}
		first := got.eligCalls[0].scan
		savings := first.realFormSize - first.callBytes
		if want := first.virtualHits * twoPassOccurrenceBytes; savings != want || savings <= 0 {
			t.Fatalf("requirements.md 5.3 - the realized savings must be visible to candidate sizing: %s savings_bytes=%d expected_savings_bytes=%d occurrences=%d",
				order, savings, want, first.virtualHits)
		}
	})

	// design.md "Canonical Outbound Rewriter": one pure rewriter shared by both
	// passes, reapplication is idempotent (requirements.md 2.9).
	t.Run("request_part_hook_is_idempotent_second_outbound_pass", func(t *testing.T) {
		order := twoPassOrderOf(got)
		if got.partScan.virtualHits < 1 || got.partScan.realHits != 0 {
			t.Fatalf("requirements.md 5.2/5.4 and design.md \"Canonical Outbound Rewriter\" - the second outbound pass must observe the early pass already applied, otherwise the rewrite was lost between passes: %s virtual_hits=%d real_hits=%d late_rewrites=%d",
				order, got.partScan.virtualHits, got.partScan.realHits, got.partRewrites)
		}
		if got.partRewrites != 0 {
			t.Fatalf("requirements.md 2.9 - reapplying virtualization to an already virtualized backend-effective call must be idempotent: %s late_rewrites=%d",
				order, got.partRewrites)
		}
	})

	// design.md "Existing Architecture and Placement" step 4 before step 6, and
	// requirements.md 5.4: the reassertion must not be the first late writer of
	// the backend-bound request, it is the last one before the backend stages.
	t.Run("request_part_hook_precedes_conversation_view_reassertion", func(t *testing.T) {
		order := twoPassOrderOf(got)
		if order.PartHook >= order.Reassert {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 4 before step 6 - the request-part hook must run before the final conversation-view reassertion: %s",
				order)
		}
	})

	// design.md "Existing Architecture and Placement" step 6 before step 9 and
	// step 10, plus requirements.md 5.2.
	t.Run("reassertion_precedes_ptb_and_backend_open", func(t *testing.T) {
		order := twoPassOrderOf(got)
		if order.Reassert >= order.PTB {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 9 - the final conversation-view reassertion must complete before the per-turn buffer is materialized: %s",
				order)
		}
		if order.Reassert >= order.Open {
			t.Fatalf("requirements.md 5.2/5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 10 - the final conversation-view reassertion must complete before Backend.Open: %s",
				order)
		}
		if order.PTB >= order.Open {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 9 before step 10 - the per-turn buffer must be materialized before Backend.Open: %s",
				order)
		}
	})

	// requirements.md 5.2: the backend-effective request carries virtualized
	// path-bearing tool history before final provider translation and
	// Backend.Open; late shaping and reassertion must not restore real paths
	// (requirements.md 5.4), and the mutation keeps the canonical document valid
	// (requirements.md 8.5).
	t.Run("backend_bound_request_carries_virtualized_history", func(t *testing.T) {
		order := twoPassOrderOf(got)
		if got.ptbScan.virtualHits < 1 || got.ptbScan.realHits != 0 {
			t.Fatalf("requirements.md 5.2 and design.md \"Testing Strategy\" (\"PTB/backend ingress receives virtualized eligible tool history\") - the per-turn buffer must carry the virtualized path-bearing tool history: %s virtual_hits=%d real_hits=%d valid_json=%t body_bytes=%d",
				order, got.ptbScan.virtualHits, got.ptbScan.realHits, got.ptbScan.validArgs, got.ptbScan.callBytes)
		}
		if got.openScan.virtualHits < 1 || got.openScan.realHits != 0 {
			t.Fatalf("requirements.md 5.2/5.4 - Backend.Open must receive the virtualized path-bearing tool history, so late shaping and the conversation-view reassertion must not restore real paths: %s virtual_hits=%d real_hits=%d valid_json=%t call_bytes=%d",
				order, got.openScan.virtualHits, got.openScan.realHits, got.openScan.validArgs, got.openScan.callBytes)
		}
		if !got.openScan.validArgs || !got.ptbScan.validArgs {
			t.Fatalf("requirements.md 8.5 - the backend-bound tool-call argument must remain valid canonical JSON: %s valid_json_ptb=%t valid_json_open=%t argument_leaves=%d",
				order, got.ptbScan.validArgs, got.openScan.validArgs, got.openScan.argLeaves)
		}
		if !got.openScan.shapePreserved {
			t.Fatalf("requirements.md 2.8 and 8.5 - the outbound rewrite must preserve the non-path argument fields: %s shape_preserved=%t argument_leaves=%d",
				order, got.openScan.shapePreserved, got.openScan.argLeaves)
		}
	})

	// requirements.md 5.4 together with the boundary context ("existing
	// conversation-view projection retains its current ownership") and design.md
	// "Out of Boundary" ("Conversation-view message visibility semantics"): the
	// two outbound passes must not change what the conversation view does, and
	// the final reassertion must still restore the overlay exactly once at its
	// frozen placement on the backend-bound call.
	t.Run("conversation_view_semantics_survive_both_outbound_passes", func(t *testing.T) {
		order := twoPassOrderOf(got)
		if !got.anchorMessagePresent {
			t.Fatalf("boundary context \"Adjacent expectations\" and design.md \"Out of Boundary\" - conversation-view projection must retain its current ownership: %s anchor_message_present=%t",
				order, got.anchorMessagePresent)
		}
		if !got.steeringRestored {
			t.Fatalf("boundary context \"Adjacent expectations\" and design.md \"Out of Boundary\" - the active steering overlay must be restored exactly once on the backend-bound call: %s steering_restored=%t",
				order, got.steeringRestored)
		}
		if !got.steeringAfterAnchor {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" - the final reassertion must rebuild steering at the frozen placement, not at the head of the request: %s steering_after_anchor=%t",
				order, got.steeringAfterAnchor)
		}
		if !got.localNoteFiltered {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" - a never_backend-tagged message must stay filtered out of the backend-bound request: %s local_note_filtered=%t",
				order, got.localNoteFiltered)
		}
	})

	// Task 1.3's fixture requirement: an early path rewrite must be detectable if
	// it is lost later. The inert control proves the detection is real, because it
	// is the same fixture and the same two passes with the rewrite suppressed and
	// it does reach Backend.Open - carrying the real-root history instead. Any
	// rewrite lost between the attempt transform and the backend bound therefore
	// reproduces the control's shape and fails the subtest above.
	t.Run("early_rewrite_is_detectable_if_lost_before_backend_open", func(t *testing.T) {
		order := twoPassOrderOf(got)
		controlOrder := twoPassOrderOf(control)
		if controlOrder.Open == 0 || controlOrder.Attempt == 0 {
			t.Fatalf("fixture: the inert control run must still reach the attempt transform and Backend.Open: %s", controlOrder)
		}
		if control.openScan.realHits < 1 || control.openScan.virtualHits != 0 {
			t.Fatalf("fixture: the inert control run must reach Backend.Open carrying the real-root history, which is the shape a lost early rewrite would reproduce: %s virtual_hits=%d real_hits=%d",
				controlOrder, control.openScan.virtualHits, control.openScan.realHits)
		}
		if got.openScan.realHits == control.openScan.realHits {
			t.Fatalf("fixture: the rewriting run must be distinguishable from the inert control run at the backend bound: %s virtual_hits=%d real_hits=%d control_virtual_hits=%d control_real_hits=%d",
				order, got.openScan.virtualHits, got.openScan.realHits, control.openScan.virtualHits, control.openScan.realHits)
		}
		if delta := control.openScan.callBytes - got.openScan.callBytes; delta != twoPassOccurrenceBytes {
			t.Fatalf("fixture: the only difference between the rewriting and the inert control backend-bound requests must be the virtualized path prefix: %s control_body_bytes=%d rewritten_body_bytes=%d expected_delta_bytes=%d",
				order, control.openScan.callBytes, got.openScan.callBytes, twoPassOccurrenceBytes)
		}
		if got.openScan.callBytes != got.openScan.realFormSize-twoPassOccurrenceBytes {
			t.Fatalf("requirements.md 5.2 - the backend-bound request must be smaller than its real-root form by exactly the virtualized occurrence: %s savings_bytes=%d expected_savings_bytes=%d",
				order, got.openScan.realFormSize-got.openScan.callBytes, twoPassOccurrenceBytes)
		}
	})
}

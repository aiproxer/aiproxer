package runtime_test

// Spec: b-leg-path-virtualization Task 5.2. Requirements 5.2, 5.4, 8.5.
//
// Design sections consulted: "Existing Architecture and Placement" (the fourteen-step
// backend-bound order and its consequences, steps 2, 3, 4, 6, 8, 9, 10),
// "Canonical Outbound Rewriter", "Testing Strategy / Runtime-integration",
// "Boundary Commitments / Out of Boundary", and "Revalidation Triggers".
//
// WHAT THIS TEST IS
//
// The runtime integration regression Task 1.3 could not write: Task 1.3 deliberately
// installed test-local stand-ins on the two outbound planes so that its ordering
// assertions stay about the runtime's own sequence, and it explicitly claims nothing
// about the concrete feature. This file installs the REAL feature contributions -
// outbound.AttemptTransform on the candidate attempt plane and outbound.RequestPartHook
// on the request-part-hook bus - and proves the one property design.md's consequence
// list states as a runtime obligation:
//
//   "runtime tests must prove conversation-view reassertion/adaptation do not restore
//   real path-bearing history before PTB/backend open"
//
// (requirements.md 5.2 and 5.4).
//
// WHY THE FIXTURE INTRODUCES A REAL PATH AFTER THE EARLY PASS
//
// A regression that only virtualizes in the early pass would be satisfied by the early
// pass alone and could not tell the late pass apart from a no-op. So the run installs
// one LATE REQUEST SHAPING participant that sorts BEFORE the feature hook inside the
// same request-part chain and puts a second real-root tool call onto the history. That
// is exactly the hazard design.md step 4 exists for: any later shaping between the
// candidate attempt stage and the backend-bound request may restore a real path, and
// only a pass that runs after all of it can catch it. The feature hook sorts last (a
// fixed order above every shipped request-part participant), so it runs after the
// shaper. With the hook installed the backend bound carries two virtualized surfaces;
// with the hook contributing nothing the second one reaches Backend.Open as a real
// path, which is what makes this regression RED without the contribution and GREEN
// with it.
//
// WHAT IT REUSES FROM TASK 1.3
//
// The executor harness: the checkpoint recorder, the frozen conversation-view reader
// and snapshot, the eligibility observer, the view observer, the backend recorder, and
// the stage-ordinal renderer. Task 1.3's assertions are untouched; this file adds only
// new helpers and new assertions in its own test function. The two thin marker types
// below exist because the real passes have nowhere to record a stage ordinal: each
// delegates every decision to the real contribution and adds nothing but the ordinal
// the shared monotonic counter needs.
//
// BOUNDARY
//
// design.md "Out of Boundary" removes conversation-view message visibility semantics,
// so the fixture keeps the same non-trivial frozen view Task 1.3 used and asserts only
// that its semantics survive both real passes. A real path that appears after the early
// pass is introduced by an explicit request-part participant on the test side, never by
// the runtime, and no production file under internal/core is touched.
//
// EXTENSION POINTS USED
//
//	1. candidate attempt transform            request.AttemptTransform.HandleAttempt
//	2. candidate sizing / context eligibility  runtime.EligibilityResolver.Check
//	3. late request shaping (fixture)          sdkhooks.RequestPartHook.HandleRequestParts
//	4. the feature's late pass under test      sdkhooks.RequestPartHook.HandleRequestParts
//	5. final conversation-view reassertion     runtime.ConversationViewObserver, final stage
//	6. per-turn buffer materialization         traffic.Observer, proxy-to-backend leg
//	7. Backend.Open                            execbackend.Backend.Open
//
// No internal function name, private field, reflection probe, or call-graph shape is
// inspected; stage ordinals come from the harness's own observer callbacks. Every
// failure message is requirement-numbered and path-content-free: occurrence counts,
// byte totals, and stage ordinals only. The workspace identity tag is derived in-test
// for comparison and is never formatted into a message.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// The two real contributions satisfy the extension points the runtime chains them on.
// The assertions below then drive them through exactly these interfaces.
var (
	_ request.AttemptTransform = (*outbound.AttemptTransform)(nil)
	_ sdkhooks.RequestPartHook = (*outbound.RequestPartHook)(nil)
)

const (
	// hookRegEarlySuffix and hookRegLateSuffix are the two distinct path-bearing
	// suffixes on the fixture's historical tool calls. They differ so a real path
	// restored onto one surface can never be mistaken for the other, and each is
	// identical in real and virtualized form, so any difference at a later stage is
	// attributable to the root prefix alone.
	hookRegEarlySuffix = "src/main.go"
	hookRegLateSuffix  = "src/late_shaped.go"

	// hookRegEarlyLimit and hookRegLateLimit are the two non-path sibling values.
	// They differ so requirement 2.8's byte-for-byte preservation of a non-path
	// argument is a real check on each surface rather than a repeated one.
	hookRegEarlyLimit = 10
	hookRegLateLimit  = 20

	// hookRegPathField is the argument member the shipped built-in profile claims for
	// the fixture's exact tool name. Spelling it explicitly keeps the fixture
	// independent of the optional schema-inference step, so this regression measures
	// the runtime ordering and nothing else.
	hookRegPathField = "file_path"

	// hookRegPayloadField is the payload-concept sibling no shipped profile claims.
	// Its value deliberately spells the real root, so requirement 2.4's refusal is
	// observable at the backend bound: it must still be real-root text there.
	hookRegPayloadField = "content"

	// hookRegShapingOrder places the fixture's late request-shaping participant below
	// every shipped request-part order and far below the feature hook's fixed order,
	// so the chain really runs shaper-then-hook.
	hookRegShapingOrder = 1
)

// hookRegArgumentDocument is one path-bearing tool-call argument document in the form
// the fixture writes it: the selected path member, the payload-concept sibling, and
// one non-path sibling.
func hookRegArgumentDocument(root, suffix string, limit int) string {
	return `{"` + hookRegPathField + `":"` + root + "/" + suffix + `",` +
		`"` + hookRegPayloadField + `":"literal ` + root + ` text","limit":` +
		strconv.Itoa(limit) + `}`
}

// hookRegToolCallMessage is one historical path-bearing tool call the client replays.
func hookRegToolCallMessage(callID, root, suffix string, limit int) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: callID,
		ToolName:   twoPassToolName,
		Content:    json.RawMessage(hookRegArgumentDocument(root, suffix, limit)),
	}}}
}

// hookRegCall builds the ingress call. It is Task 1.3's fixture with one difference
// that matters here: the historical tool call names the argument member the shipped
// built-in profile claims, so the REAL feature rewriter selects it by exact-name
// profile rather than by inference. The never_backend note and the terminal
// forwardable message are kept so the final conversation-view reassertion is a real
// stage rather than a no-op.
func hookRegCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "two-pass:m"},
		Tools: []aipapi.ToolDef{{
			Name: twoPassToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + hookRegPathField +
				`":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			twoPassAnchorMessage(),
			hookRegToolCallMessage("hook-reg-early", twoPassRealRoot, hookRegEarlySuffix, hookRegEarlyLimit),
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassTailText)}},
		},
	}
}

// hookRegShaping is the fixture's late request-shaping participant: the only thing in
// this run that puts a real path onto the history AFTER the early pass has run.
//
// It is deliberately NOT a rewriter. Its whole job is to be the hazard the feature's
// late pass exists to catch: a participant that sorts before it in the same
// request-part chain and restores real-root path-bearing tool history. It mutates
// nothing else and returns no error, so the chain always continues.
type hookRegShaping struct{}

func (*hookRegShaping) ID() string                        { return "path-virtualization-late-shaping-fixture" }
func (*hookRegShaping) Order() int                        { return hookRegShapingOrder }
func (*hookRegShaping) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (*hookRegShaping) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	call.Messages = append(call.Messages,
		hookRegToolCallMessage("hook-reg-late", twoPassRealRoot, hookRegLateSuffix, hookRegLateLimit))
	return nil
}

// hookRegAttemptMarker records the candidate-attempt stage ordinal and then delegates
// every decision to the real outbound.AttemptTransform.
//
// The real pass owns the behavior under test; this marker owns nothing but the
// monotonic ordinal the shared harness counter needs. Identity, order, and failure mode
// are the real pass's, so the bus and the diagnostics see the real participant.
type hookRegAttemptMarker struct {
	rec  *twoPassRecorder
	real *outbound.AttemptTransform
}

func (m *hookRegAttemptMarker) ID() string                        { return m.real.ID() }
func (m *hookRegAttemptMarker) Order() int                        { return m.real.Order() }
func (m *hookRegAttemptMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *hookRegAttemptMarker) HandleAttempt(
	ctx context.Context,
	call *aipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (request.AttemptDecision, error) {
	m.rec.mark(twoPassCheckpointAttemptTransform)
	return m.real.HandleAttempt(ctx, call, meta, svc)
}

// hookRegPartMarker records the request-part stage ordinal and then delegates every
// decision to the real outbound.RequestPartHook.
//
// It is the same shape as hookRegAttemptMarker and for the same reason. Because it
// wraps the feature hook rather than a stand-in, the ordinal it records is the stage
// the REAL late pass ran at, which is what lets the ordering assertion below be a
// statement about the feature and not about the fixture.
type hookRegPartMarker struct {
	rec  *twoPassRecorder
	real *outbound.RequestPartHook
}

func (m *hookRegPartMarker) ID() string                        { return m.real.ID() }
func (m *hookRegPartMarker) Order() int                        { return m.real.Order() }
func (m *hookRegPartMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *hookRegPartMarker) HandleRequestParts(
	ctx context.Context,
	call *aipapi.Call,
	meta sdkhooks.PartMeta,
) error {
	m.rec.mark(twoPassCheckpointRequestPartHook)
	return m.real.HandleRequestParts(ctx, call, meta)
}

// hookRegTraffic records the proxy-to-backend leg, i.e. the per-turn buffer the runtime
// materializes immediately before Backend.Open.
//
// It records the raw body rather than a scan, because this regression must measure the
// REAL derived alias, which is a function of the authoritative project root and is
// therefore not a fixed literal the harness can spell out.
type hookRegTraffic struct {
	rec  *twoPassRecorder
	mu   sync.Mutex
	body []byte
}

func (o *hookRegTraffic) OnObservation(_ context.Context, ev traffic.Observation) error {
	if ev.Leg != traffic.LegPTB {
		return nil
	}
	o.rec.mark(twoPassCheckpointPTB)
	o.mu.Lock()
	o.body = append([]byte(nil), ev.Body...)
	o.mu.Unlock()
	return nil
}

func (o *hookRegTraffic) captured() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.body
}

// hookRegReports collects the bounded reports the two real passes emit.
//
// It is mutex-guarded because a reporter is documented as callable concurrently and one
// instance of each pass is shared by every attempt of the turn.
type hookRegReports struct {
	mu      sync.Mutex
	attempt []outbound.Report
	part    []outbound.Report
}

func (r *hookRegReports) onAttempt(report outbound.Report) {
	r.mu.Lock()
	r.attempt = append(r.attempt, report)
	r.mu.Unlock()
}

func (r *hookRegReports) onPart(report outbound.Report) {
	r.mu.Lock()
	r.part = append(r.part, report)
	r.mu.Unlock()
}

func (r *hookRegReports) oneAttempt(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempt) != 1 {
		t.Fatalf("early outbound pass reports = %d, want exactly 1: one candidate attempt runs one pass", len(r.attempt))
	}
	return r.attempt[0]
}

func (r *hookRegReports) onePart(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.part) != 1 {
		t.Fatalf("late outbound pass reports = %d, want exactly 1: one request-part chain runs one pass", len(r.part))
	}
	return r.part[0]
}

// hookRegScan is the path-content-free measurement of every selected path-bearing
// tool-call argument document on one backend-bound surface.
//
// It counts the two states the requirement 5.2 assertion is about - a selected path
// value still carrying the real root, and one carrying the derived alias - plus the two
// things requirements.md 2.4 and 2.8 require to survive every stage. No path value,
// suffix, identifier, digest, or workspace tag ever reaches a failure message through
// this struct.
type hookRegScan struct {
	documents   int  // argument documents observed
	realRoot    int  // selected path values still carrying the real root
	virtual     int  // selected path values carrying the derived alias
	limits      int  // sum of the non-path sibling values across documents
	payloadKept int  // documents whose payload-concept sibling still spells the real root
	validArgs   bool // every observed argument document is one complete JSON value
	shapeKept   bool // every document kept the selected, payload, and non-path members
	callBytes   int
}

// hookRegMeasure scans one canonical call's selected path-bearing argument documents.
//
// The scan walks both canonical authorities, so the measurement is a statement about the
// backend-bound request rather than about whichever representation candidate adaptation
// happened to choose. alias is the alias this feature derives from the authoritative
// project root; it is compared, never formatted.
func hookRegMeasure(call aipapi.Call, alias string) hookRegScan {
	out := hookRegScan{validArgs: true, shapeKept: true}
	raw, err := json.Marshal(call)
	if err != nil {
		return hookRegScan{validArgs: false}
	}
	out.callBytes = len(raw)

	documents := make([][]byte, 0, 4)
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != aipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			documents = append(documents, part.Content)
		}
	}
	for _, item := range call.Items {
		if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
			continue
		}
		documents = append(documents, item.ToolCall.Arguments)
	}
	out.documents = len(documents)

	for _, doc := range documents {
		if !json.Valid(doc) {
			out.validArgs = false
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(doc, &fields); err != nil || len(fields) != 3 {
			out.validArgs = false
			out.shapeKept = false
			continue
		}
		var selected string
		switch {
		case json.Unmarshal(fields[hookRegPathField], &selected) != nil:
			out.shapeKept = false
			continue
		case strings.Contains(selected, alias):
			out.virtual++
		case strings.Contains(selected, twoPassRealRoot):
			out.realRoot++
		default:
			out.shapeKept = false
		}
		var limit int
		if err := json.Unmarshal(fields["limit"], &limit); err != nil {
			out.shapeKept = false
			continue
		}
		out.limits += limit
		var payload string
		if err := json.Unmarshal(fields[hookRegPayloadField], &payload); err != nil {
			out.shapeKept = false
			continue
		}
		if strings.Contains(payload, twoPassRealRoot) {
			out.payloadKept++
		} else {
			out.shapeKept = false
		}
	}
	if out.documents == 0 {
		out.validArgs = false
		out.shapeKept = false
	}
	return out
}

// hookRegMeasureBody measures an already-serialized backend-bound body.
func hookRegMeasureBody(body []byte, alias string) hookRegScan {
	var call aipapi.Call
	if err := json.Unmarshal(body, &call); err != nil {
		return hookRegScan{validArgs: false, callBytes: len(body)}
	}
	return hookRegMeasure(call, alias)
}

// hookRegResolver builds the shipped conservative policy a deployment gets with no
// operator configuration: the built-in profile layer, which claims the fixture's
// argument member on the fixture's exact tool name.
func hookRegResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles(builtin) reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver(builtin) reject = %q, want none", reject)
	}
	return resolver
}

// hookRegAliasOf derives the alias this feature publishes for the fixture root.
//
// It is the fixture-root specialization of the shared expAliasOf helper rather than a
// second derivation, so both harnesses in this package answer "the alias for a root"
// from one implementation. The value is returned for comparison only. Nothing in this
// file formats it and no failure message contains it: requirement 7.7 forbids the
// workspace identity tag from reaching any observable dimension.
func hookRegAliasOf(t *testing.T) string {
	t.Helper()
	return expAliasOf(t, twoPassRealRoot)
}

// hookRegEligibility observes the candidate sizing / context eligibility port and
// records the selected path-bearing arguments the port is shown.
//
// It cannot reuse Task 1.3's observer, and the reason is the point of this file: that
// observer counts a hardcoded literal alias, while this regression must measure the
// alias the feature actually derives from the authoritative project root. Everything
// else about the seam is identical - the same supported port, invoked on the selected
// candidate the same way, with the same always-eligible answer so the run reaches
// Backend.Open.
type hookRegEligibility struct {
	rec   *twoPassRecorder
	alias string

	mu    sync.Mutex
	scans []hookRegScan
}

func (o *hookRegEligibility) Check(
	_ context.Context,
	_ routing.AttemptCandidate,
	call aipapi.Call,
	facts modelcatalog.EffectiveFacts,
) modelcatalog.EligibilityDecision {
	// The shared counter records the stage ordinal this invocation ran at; the
	// per-regression scan records what this invocation was shown. Both appends happen
	// inside one Check call, so the Nth entry of each belongs to the same invocation.
	o.rec.noteEligibility(call)
	o.mu.Lock()
	o.scans = append(o.scans, hookRegMeasure(call, o.alias))
	o.mu.Unlock()
	return modelcatalog.EligibilityDecision{
		IsEligible: true,
		Reason:     modelcatalog.EligibilityEligible,
		Facts:      facts,
	}
}

func (o *hookRegEligibility) observed() []hookRegScan {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]hookRegScan(nil), o.scans...)
}

// hookRegObservation is the complete content-free record of one run of this regression.
type hookRegObservation struct {
	order    twoPassStages
	turnDone bool
	reports  *hookRegReports

	// eligStages and eligScans are parallel: the Nth entry of each belongs to the Nth
	// eligibility invocation. The stages come from Task 1.3's shared counter, the
	// scans from this file's own measurement.
	eligStages           []int
	eligScans            []hookRegScan
	ptbScan              hookRegScan
	openScan             hookRegScan
	steeringRestored     bool
	steeringAfterAnchor  bool
	localNoteFiltered    bool
	anchorMessagePresent bool

	// label is the scenario's bounded, content-free failure-message token.
	label string
	// anchorMissing reduces Execute's error to the single observable denial shape
	// this spec cares about. The error TEXT is never read, because a denial reason can
	// carry anchor identities and overlay IDs.
	anchorMissing bool
	// backendOpenCall and ptbBody are the two backend-bound surfaces, captured by the
	// shared harness, so a scenario can measure its own selected payload at both.
	backendOpenCall aipapi.Call
	ptbBody         []byte
	// snapshotReads, failureStages, anchorFailures, and fallbacks are the conversation-
	// view diagnostics the runtime published for this run.
	snapshotReads  int
	failureStages  []string
	anchorFailures int
	fallbacks      int
}

// hookRegOrderOf renders this run's ordinals through Task 1.3's own renderer, so a
// failure here names every stage exactly the way the ordering characterization does.
func hookRegOrderOf(got hookRegObservation) twoPassOrder {
	calls := make([]twoPassEligibilityCall, 0, len(got.eligStages))
	for _, stage := range got.eligStages {
		calls = append(calls, twoPassEligibilityCall{stage: stage})
	}
	return twoPassOrderOf(twoPassObservation{order: got.order, eligCalls: calls})
}

// hookRegScenario selects the fixture shape ONE harness run uses.
//
// There is exactly one end-to-end PTB/Backend.Open harness in this file. Task 5.3
// extends it rather than adding a second: the scenarios differ only in which message
// the frozen overlay is anchored on, which compiled feature policy applies, and
// whether a structural perturbation runs on the request-part plane before the feature
// hook. Everything else - the frozen conversation-view reader, the workspace
// resolver handed to BOTH real passes, the eligibility observer, the traffic
// observer, the recording backend, the seeded RNG, and the pinned clock - is
// identical across every scenario, so a difference in outcome is attributable to the
// variable under test and nothing else.
type hookRegScenario struct {
	// label is a bounded, content-free failure-message token.
	label string

	// call builds the ingress call and snapshot builds the frozen per-turn view. Both
	// are required; a scenario that cannot supply them is a fixture bug.
	call     func() *aipapi.Call
	snapshot func(*testing.T) conversationprojection.Snapshot

	// resolver builds the compiled feature policy. Nil selects the shipped built-in
	// layer, which is what a deployment with no operator configuration gets.
	resolver func(*testing.T) *pathvirtualization.Resolver

	// lateShaper installs the fixture's late request-shaping participant, the only
	// thing in a standard run that puts a real path onto the history AFTER the early
	// pass has run. It APPENDS a complete message, which is an insertion, so a
	// scenario whose overlay is anchored on a message the early pass rewrites must
	// leave it off: an inserted complete message legitimately breaks one-to-one
	// trajectory lineage and requirements.md 5.10 requires that to fail closed.
	lateShaper bool

	// perturbation is an additional request-part participant that runs BEFORE the
	// feature hook and structurally alters the trajectory. Nil installs none.
	perturbation sdkhooks.RequestPartHook

	// withLatePass selects whether the real late pass contributes. The control run
	// keeps the marker wired and hands it a typed nil of the real pass type, so the
	// request-part stage is reached and recorded identically and the run is exactly
	// "the late pass contributed nothing" with no other variable changed.
	withLatePass bool
}

// hookRegStandardScenario is Task 5.2's own fixture: the overlay is anchored on the
// FIRST user message, which neither outbound pass mutates, and the late shaping
// participant reintroduces one real-root tool call.
func hookRegStandardScenario(t *testing.T, withLatePass bool) hookRegScenario {
	t.Helper()
	return hookRegScenario{
		label:        "standard_late_shaped_history",
		call:         hookRegCall,
		snapshot:     func(*testing.T) conversationprojection.Snapshot { return twoPassSnapshot(t) },
		lateShaper:   true,
		withLatePass: withLatePass,
	}
}

// hookRegDriftScenario wires Task 1.3's anchor-drift fixture - one never_backend note
// plus one ACTIVE after-message steering overlay anchored, under the strict fail-closed
// policy, on the PRE-virtualization identity of a path-bearing message - into the same
// harness. The anchored message is the one the real early pass rewrites, which is
// exactly the shape requirements.md 5.9 and 5.10 are about.
func hookRegDriftScenario(t *testing.T, label string, anchor aipapi.Message, resolver func(*testing.T) *pathvirtualization.Resolver, perturbation sdkhooks.RequestPartHook) hookRegScenario {
	t.Helper()
	return hookRegScenario{
		label:        label,
		call:         func() *aipapi.Call { return driftIngressCall(anchor) },
		snapshot:     func(*testing.T) conversationprojection.Snapshot { return driftSnapshot(t, anchor) },
		resolver:     resolver,
		perturbation: perturbation,
		withLatePass: true,
	}
}

// hookRegRun executes the scenario: the selected fixture and frozen conversation view,
// the real early outbound pass, one late request-shaping participant that reintroduces a
// real-root tool call, the optional structural perturbation, and the real late outbound
// pass.
//
// Execute errors are reduced to a boolean because a denial reason can carry anchor
// identities and overlay IDs, which must never reach a failure message.
func hookRegRun(t *testing.T, scenario hookRegScenario) hookRegObservation {
	t.Helper()

	rec := &twoPassRecorder{}
	reports := &hookRegReports{}
	ptb := &hookRegTraffic{rec: rec}
	reader := &twoPassReader{snap: scenario.snapshot(t)}
	alias := hookRegAliasOf(t)
	elig := &hookRegEligibility{rec: rec, alias: alias}
	resolver := hookRegResolver(t)
	if scenario.resolver != nil {
		resolver = scenario.resolver(t)
	}

	// The early pass reads the workspace projection the runtime already pinned onto its
	// attempt metadata. The late pass reads that SAME PIN through the public SDK context
	// projection, because sdkhooks.PartMeta carries no workspace view at all. Neither
	// pass is handed a workspace authority here, which is the point: one pin, one alias,
	// no second fact about the project root for the two passes to disagree about.
	authority := twoPassWorkspaceResolver{root: twoPassRealRoot}

	// The control keeps the marker wired so the request-part stage is reached and
	// recorded identically, and hands it a TYPED NIL of the real pass type. The real
	// pass documents the nil receiver as safe and inert, so the control is exactly
	// "the late pass contributed nothing" with no other variable changed: same chain,
	// same stage ordinal, same reassertion, same adaptation, same backend.
	late := (*outbound.RequestPartHook)(nil)
	if scenario.withLatePass {
		late = outbound.NewRequestPartHook(
			rewrite.ModeRewrite,
			resolver,
			outbound.WithHookReporter(reports.onPart),
		)
	}
	partHooks := make([]sdkhooks.RequestPartHook, 0, 3)
	if scenario.lateShaper {
		partHooks = append(partHooks, &hookRegShaping{})
	}
	if scenario.perturbation != nil {
		// The perturbation sorts below every shipped request-part order and below the
		// feature hook's fixed order, so the chain really runs
		// shaper-then-perturbation-then-feature-hook.
		partHooks = append(partHooks, scenario.perturbation)
	}
	partHooks = append(partHooks, &hookRegPartMarker{rec: rec, real: late})

	bus := hooks.New(hooks.Config{RequestPartHooks: partHooks})
	ex := runtime.TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua store: %v", err)
	}
	ex.Store = store
	ex.Bus = bus
	early := outbound.NewAttemptTransform(
		rewrite.ModeRewrite,
		resolver,
		outbound.WithReporter(reports.onAttempt),
	)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       authority,
		TrafficObserver: ptb,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{&hookRegAttemptMarker{rec: rec, real: early}},
		}),
	})
	ex.ConversationViewObserver = &hookRegViewObserver{driftViewObserver: &driftViewObserver{rec: rec, view: &twoPassViewObserver{rec: rec}}}
	ex.ConversationViewReader = reader
	ex.EligibilityResolver = elig
	ex.Backends = map[string]execbackend.Backend{"two-pass": twoPassBackend(rec)}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	// The request runs on the ordinary ingress path rather than the detached
	// auxiliary path: the detached path deliberately projects an EMPTY workspace
	// view, so the early pass would have no authoritative project root to derive a
	// mapping from. On this path the runtime resolves the workspace through the same
	// snapshot resolver the late pass is handed, and pins the result onto the attempt
	// metadata the early pass reads - which is what makes one shared workspace
	// resolver the right thing to wire for both.
	stream, execErr := ex.Execute(context.Background(), scenario.call())
	if execErr == nil {
		_, execErr = aipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}

	raw := rec.observation(execErr == nil, reader.readCount())
	stages := make([]int, 0, len(raw.eligCalls))
	for _, call := range raw.eligCalls {
		stages = append(stages, call.stage)
	}
	viewObserver, ok := ex.ConversationViewObserver.(*hookRegViewObserver)
	if !ok {
		panic("hookRegRun: the conversation-view observer is not the harness observer")
	}
	view := viewObserver.driftViewObserver
	return hookRegObservation{
		order:      raw.order,
		turnDone:   raw.turnResolved,
		reports:    reports,
		eligStages: stages,
		eligScans:  elig.observed(),
		ptbScan:    hookRegMeasureBody(ptb.captured(), alias),
		openScan:   hookRegMeasure(raw.backendOpenCall, alias),

		steeringRestored:     raw.steeringRestored,
		steeringAfterAnchor:  raw.steeringAfterAnchor,
		localNoteFiltered:    raw.localNoteFiltered,
		anchorMessagePresent: raw.anchorMessagePresent,

		label:           scenario.label,
		anchorMissing:   errors.Is(execErr, conversationprojection.ErrAnchorMissing),
		backendOpenCall: raw.backendOpenCall,
		ptbBody:         ptb.captured(),
		snapshotReads:   reader.readCount(),
		failureStages:   view.failureStages(),
		anchorFailures:  len(view.anchorFailurePolicies()),
		fallbacks:       len(view.fallbackPolicies()),
	}
}

// TestOutboundAttempt_RequestPartHookPreservesVirtualizedHistoryThroughBackendOpen is
// the runtime integration regression for requirements.md 5.2, 5.4, and 8.5, and it
// drives the feature's REAL late pass rather than a stand-in.
//
// The run installs the real candidate attempt transform and the real request-part hook,
// plus one late request-shaping participant that reintroduces a real-root tool call
// between them. It then asserts, from the runtime's own semantic stages:
//
//   - the candidate attempt stage precedes candidate sizing/context eligibility and the
//     request-part stage, and both eligibility invocations observe the virtualized
//     representation;
//   - the request-part stage, recorded at the stage the REAL hook ran at, precedes the
//     final conversation-view reassertion, which precedes the per-turn buffer, which
//     precedes Backend.Open;
//   - the real hook rewrote exactly the one surface the early pass had left real and
//     published no bytes for the surface the early pass already virtualized;
//   - the reassertion and candidate adaptation preserve BOTH virtualized surfaces
//     through the per-turn buffer and Backend.Open, with no real-root occurrence on
//     either selected path value, every argument document still one complete JSON
//     value, and every non-path and payload-concept sibling intact;
//   - the frozen conversation view still restores its overlay once at its frozen
//     placement and still filters the never_backend note.
//
// The rewrite-loss oracle. The identical fixture, with the late pass reached but inert,
// reaches Backend.Open carrying the reintroduced surface as a real path, which is
// exactly what a missing, misordered, or non-contributing late pass would reproduce.
func TestOutboundAttempt_RequestPartHookPreservesVirtualizedHistoryThroughBackendOpen(t *testing.T) {
	t.Parallel()

	// Control: identical fixture, identical real early pass, and an identical
	// request-part chain in which the late pass is reached but contributes nothing.
	control := hookRegRun(t, hookRegStandardScenario(t, false))
	got := hookRegRun(t, hookRegStandardScenario(t, true))

	// Scaffolding guards. Every assertion below reads these, so they run first.
	order := hookRegOrderOf(got)
	if !control.turnDone {
		t.Fatalf("fixture: the run without the late pass must still complete one turn: %s", hookRegOrderOf(control))
	}
	if !got.turnDone {
		t.Fatalf("fixture: the run with the late pass must complete one turn with no pre-backend denial: %s", order)
	}
	for _, checkpoint := range []struct {
		name  string
		stage int
	}{
		{"candidate attempt stage", got.order.attempt},
		{"request-part stage", got.order.part},
		{"conversation-view reassertion", got.order.reassert},
		{"per-turn buffer", got.order.ptb},
		{"Backend.Open", got.order.open},
	} {
		if checkpoint.stage == 0 {
			t.Fatalf("fixture: the %s checkpoint must be reached: %s", checkpoint.name, order)
		}
	}
	if len(got.eligStages) == 0 || len(got.eligScans) != len(got.eligStages) {
		t.Fatalf("fixture: candidate sizing / context eligibility must be invoked and observed: %s eligibility_invocations=%d observed_scans=%d",
			order, len(got.eligStages), len(got.eligScans))
	}
	if got.openScan.documents != 2 {
		t.Fatalf("fixture: the backend-bound request must carry both path-bearing tool calls, one virtualized by the early pass and one restored by the fixture's late shaping: %s argument_documents=%d",
			order, got.openScan.documents)
	}

	// design.md "Existing Architecture and Placement" steps 2, 3, and 4.
	t.Run("real_attempt_transform_precedes_candidate_sizing_and_request_part_stage", func(t *testing.T) {
		first := got.eligStages[0]
		if got.order.attempt >= first {
			t.Fatalf("requirements.md 5.3 and design.md \"Existing Architecture and Placement\" step 2 before step 3 - the real early pass must precede preliminary candidate eligibility: %s",
				order)
		}
		if first >= got.order.part {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 3 before step 4 - preliminary candidate eligibility must run before the request-part stage that carries the real late pass: %s",
				order)
		}
	})

	// design.md "Existing Architecture and Placement" step 4 before step 6, and step 6
	// before steps 9 and 10. These are the orderings the late pass depends on and the
	// ones the reassertion must not be able to invert.
	t.Run("real_late_pass_precedes_reassertion_ptb_and_backend_open", func(t *testing.T) {
		if got.order.part >= got.order.reassert {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 4 before step 6 - the real late pass must run before the final conversation-view reassertion: %s",
				order)
		}
		if got.order.reassert >= got.order.ptb {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 9 - the final conversation-view reassertion must complete before the per-turn buffer is materialized: %s",
				order)
		}
		if got.order.reassert >= got.order.open {
			t.Fatalf("requirements.md 5.2/5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 10 - the final conversation-view reassertion must complete before Backend.Open: %s",
				order)
		}
		if got.order.ptb >= got.order.open {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 9 before step 10 - the per-turn buffer must be materialized before Backend.Open: %s",
				order)
		}
	})

	// requirements.md 5.3: candidate sizing and the post-hook rederivation observe the
	// virtualized representation, so admission is never charged for savings it cannot
	// see.
	t.Run("candidate_sizing_and_post_hook_rederivation_see_virtualized_representation", func(t *testing.T) {
		// The FIRST invocation is the preliminary admission check that runs right after
		// the whole candidate attempt stage, so it can only have seen the early pass's
		// work: exactly the one surface that surface started with, virtualized.
		// The SECOND is the post-hook rederivation, and it is the one that proves the
		// late pass ran inside the chain - it must see BOTH surfaces virtualized,
		// including the one the fixture's late shaping introduced.
		want := []int{1, 2}
		if len(got.eligScans) != len(want) {
			t.Fatalf("fixture: eligibility must be invoked once per attempt stage boundary, invocations=%d", len(got.eligScans))
		}
		for i, scan := range got.eligScans {
			if scan.virtual < want[i] || scan.realRoot != 0 {
				t.Fatalf("requirements.md 5.3/5.4 - eligibility invocation %d must be shown the virtualized representation, never the real-root one: %s invocation=%d stage=%d virtual=%d real_root=%d minimum_virtual=%d",
					i, order, i, got.eligStages[i], scan.virtual, scan.realRoot, want[i])
			}
		}
		if first := got.eligScans[0]; first.virtual != 1 {
			t.Fatalf("requirements.md 5.3 - the preliminary eligibility check runs before the request-part stage, so it must observe exactly the early pass's savings and nothing more: %s virtual=%d real_root=%d",
				order, first.virtual, first.realRoot)
		}
		if second := got.eligScans[1]; second.virtual != 2 {
			t.Fatalf("requirements.md 5.4 - the post-hook rederivation runs after the real late pass, so it must observe BOTH surfaces virtualized: %s virtual=%d real_root=%d",
				order, second.virtual, second.realRoot)
		}
	})

	// design.md "Canonical Outbound Rewriter" with requirements.md 2.9: the late pass
	// reapplies the SAME pure rewriter to the SAME mapping, so the surface the early
	// pass already virtualized contributes nothing and only the newly reintroduced
	// real-root occurrence is rewritten.
	t.Run("real_late_pass_rewrites_only_the_reintroduced_real_path", func(t *testing.T) {
		early := got.reports.oneAttempt(t)
		if early.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.3 - the early outbound pass must run the shared rewriter, outcome = %v", early.Outcome)
		}
		if early.Stats.Rewritten != 1 || early.Stats.Eligible != 1 {
			t.Fatalf("fixture: the real early pass must virtualize exactly the one path-bearing tool call, eligible=%d rewritten=%d",
				early.Stats.Eligible, early.Stats.Rewritten)
		}

		late := got.reports.onePart(t)
		if late.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.2/8.2 - the late outbound pass must run the shared rewriter and report its bounded outcome, outcome = %v", late.Outcome)
		}
		if late.Stats.Rewritten != 1 || late.Stats.Eligible != 1 {
			t.Fatalf("requirements.md 5.4 - the late outbound pass must catch exactly the real path the later shaping introduced, eligible=%d rewritten=%d",
				late.Stats.Eligible, late.Stats.Rewritten)
		}
		if late.Stats.BytesSaved() <= 0 {
			t.Fatalf("requirements.md 7.6 - the late outbound pass must report the saving it realized, saved=%d", late.Stats.BytesSaved())
		}
	})

	// requirements.md 5.2 and 5.4 with design.md "Testing Strategy"
	// ("PTB/backend ingress receives virtualized eligible tool history"): the
	// reassertion and candidate adaptation are the last writers of the backend-bound
	// request before the per-turn buffer and Backend.Open, and neither may restore a
	// real path-bearing history.
	t.Run("reassertion_and_candidate_adaptation_preserve_virtualized_history", func(t *testing.T) {
		for _, stage := range []struct {
			name string
			scan hookRegScan
		}{
			{"per_turn_buffer", got.ptbScan},
			{"Backend.Open", got.openScan},
		} {
			if stage.scan.documents != 2 {
				t.Fatalf("requirements.md 5.2 - %s must carry both path-bearing tool call argument documents, documents=%d valid_json=%t body_bytes=%d",
					stage.name, stage.scan.documents, stage.scan.validArgs, stage.scan.callBytes)
			}
			if stage.scan.virtual != 2 || stage.scan.realRoot != 0 {
				t.Fatalf("requirements.md 5.2 and 5.4 - %s must carry the virtualized path-bearing history on both surfaces; late request shaping, the final conversation-view reassertion, and candidate adaptation must not restore a real root: %s virtual=%d real_root=%d",
					stage.name, order, stage.scan.virtual, stage.scan.realRoot)
			}
			if !stage.scan.validArgs {
				t.Fatalf("requirements.md 8.5 - %s must receive one complete JSON value per tool-call argument document: %s valid_json=%t",
					stage.name, order, stage.scan.validArgs)
			}
			if !stage.scan.shapeKept {
				t.Fatalf("requirements.md 2.4, 2.8, and 8.5 - %s must keep the selected, payload-concept, and non-path argument members intact: %s shape_kept=%t",
					stage.name, order, stage.scan.shapeKept)
			}
		}
		wantLimits := hookRegEarlyLimit + hookRegLateLimit
		if got.ptbScan.limits != wantLimits || got.openScan.limits != wantLimits {
			t.Fatalf("requirements.md 2.8 - the non-path sibling of each argument must survive both real passes byte-for-byte: %s per_turn_buffer_non_path_sum=%d backend_open_non_path_sum=%d expected_sum=%d",
				order, got.ptbScan.limits, got.openScan.limits, wantLimits)
		}
		if got.ptbScan.payloadKept != 2 || got.openScan.payloadKept != 2 {
			t.Fatalf("requirements.md 2.4 - no shipped profile claims the payload-concept member, so it must still be real-root text at the backend bound: %s per_turn_buffer_payload_kept=%d backend_open_payload_kept=%d",
				order, got.ptbScan.payloadKept, got.openScan.payloadKept)
		}
	})

	// design.md "Out of Boundary" ("Conversation-view message visibility semantics")
	// and the boundary context's adjacent expectations: neither real pass may change
	// what the frozen conversation view does.
	t.Run("conversation_view_semantics_survive_both_real_outbound_passes", func(t *testing.T) {
		if !got.anchorMessagePresent {
			t.Fatalf("boundary context and design.md \"Out of Boundary\" - conversation-view projection must retain its current ownership: %s anchor_message_present=%t",
				order, got.anchorMessagePresent)
		}
		if !got.steeringRestored {
			t.Fatalf("boundary context and design.md \"Out of Boundary\" - the active steering overlay must be restored exactly once on the backend-bound call: %s steering_restored=%t",
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

	// The rewrite-loss oracle. The identical fixture with the late pass absent reaches
	// Backend.Open carrying the reintroduced surface as a real path, which is precisely
	// what a missing, misordered, or non-contributing late pass would reproduce. This
	// is what makes the assertion above a real guard rather than a tautology.
	t.Run("a_missing_late_pass_is_detectable_at_the_backend_bound", func(t *testing.T) {
		controlOrder := hookRegOrderOf(control)
		if control.openScan.documents != 2 {
			t.Fatalf("fixture: the run without the late pass must still carry both argument documents: %s argument_documents=%d",
				controlOrder, control.openScan.documents)
		}
		if control.openScan.realRoot < 1 {
			t.Fatalf("fixture: the run without the late pass must reach Backend.Open carrying the reintroduced surface as a real path, which is the shape a lost late rewrite reproduces: %s virtual=%d real_root=%d",
				controlOrder, control.openScan.virtual, control.openScan.realRoot)
		}
		if got.openScan.realRoot != 0 {
			t.Fatalf("requirements.md 5.4 - no real-root occurrence may reach Backend.Open once the late pass is wired: %s real_root=%d",
				order, got.openScan.realRoot)
		}
		if delta := control.openScan.callBytes - got.openScan.callBytes; delta <= 0 {
			t.Fatalf("fixture: the backend-bound request must be strictly smaller once the late pass publishes its alias: %s control_body_bytes=%d backend_open_body_bytes=%d",
				order, control.openScan.callBytes, got.openScan.callBytes)
		}
	})
}

// ---------------------------------------------------------------------------
// Task 9.2 remediation: requirements.md 5.4 with its enforcement named, over the
// SHIPPED wiring rather than a hand-wired one.
//
// WHY THIS IS A SEPARATE TEST
//
// The run above could not tell where the late pass's project root came from: the harness
// handed the pass a pkg/aipsdk/workspace.Resolver, so a regression in the SDK context
// projection would have been invisible to it. Worse, that injection is what the shipped
// composition did NOT do - the standard table's factory binds no authority at all - so the
// late pass was inert in the only shipping deployment, and a real path restored by later
// request shaping reached Backend.Open with nothing to stop it. Requirement 5.4 had no
// enforcement at all.
//
// The fix is not a second injection: the late pass now READS the runtime's per-turn PIN
// from the public SDK context projection, the same pin the early pass reads out of its
// attempt metadata. This harness therefore hands BOTH passes no workspace authority
// whatsoever - hookRegRun wires the runtime snapshot with one resolver and passes nothing
// to either constructor - so every assertion below is attributable to the pin alone.
//
// WHAT IS ASSERTED
//
//   - requirements.md 5.4: the surface the late shaper restored is re-virtualized, so no
//     real-root occurrence reaches Backend.Open, while the surface the early pass already
//     virtualized is untouched;
//   - the control, which is the same run with the late pass reached but inert, DOES reach
//     Backend.Open carrying a real path - which is what makes the assertion above a real
//     guard rather than a tautology;
//   - requirements.md 5.6: both selected path values carry the SAME alias at
//     Backend.Open. hookRegMeasure counts a document as virtual only when it contains the
//     alias derived from the pinned root, so two virtual documents are two instances of
//     one workspace tag and therefore one pin, not two resolutions that happened to agree;
//   - and the late pass really ran: its bounded report says so, so the clean backend bound
//     cannot be attributed to some other participant.
// ---------------------------------------------------------------------------

// TestOutboundAttempt_LatePassIsArmedByTheRuntimesPinnedWorkspaceAlone is the
// requirement 5.4 guard whose subject is the SOURCE of the late pass's project root: the
// runtime's per-turn pin, projected onto the public SDK context seam, and nothing else.
func TestOutboundAttempt_LatePassIsArmedByTheRuntimesPinnedWorkspaceAlone(t *testing.T) {
	t.Parallel()

	got := hookRegRun(t, hookRegStandardScenario(t, true))
	control := hookRegRun(t, hookRegStandardScenario(t, false))
	order := hookRegOrderOf(got)

	if !got.turnDone || !control.turnDone {
		t.Fatalf("fixture: both runs must complete one turn: with_late_pass=%s control=%s", order, hookRegOrderOf(control))
	}
	if got.order.part == 0 || got.order.open == 0 {
		t.Fatalf("fixture: the request-part stage and Backend.Open must both be reached: %s", order)
	}

	// requirements.md 5.4, and the report that shows the late pass ran rather than some
	// other participant having produced the clean surface.
	late := got.reports.onePart(t)
	if late.Outcome != outbound.OutcomeRewriterRan {
		t.Fatalf("requirements.md 5.4 - the late pass must have run off the pinned workspace view: outcome=%v root_reason=%q",
			late.Outcome, late.RootReason)
	}
	if late.Stats.Rewritten != 1 {
		t.Fatalf("requirements.md 5.4 - the late pass must virtualize exactly the one surface later shaping restored, rewritten=%d eligible=%d",
			late.Stats.Rewritten, late.Stats.Eligible)
	}
	early := got.reports.oneAttempt(t)
	if early.Stats.Rewritten != 1 {
		t.Fatalf("fixture: the early pass must virtualize exactly the one historical surface, rewritten=%d", early.Stats.Rewritten)
	}
	if got.openScan.documents != 2 || got.openScan.virtual != 2 {
		t.Fatalf("requirements.md 5.6 - both selected path values must carry the SAME alias derived from the one pinned view at Backend.Open: %s documents=%d virtual=%d real_root=%d",
			order, got.openScan.documents, got.openScan.virtual, got.openScan.realRoot)
	}
	if got.openScan.realRoot != 0 {
		t.Fatalf("requirements.md 5.4 - no real-root occurrence may reach Backend.Open: %s virtual=%d real_root=%d",
			order, got.openScan.virtual, got.openScan.realRoot)
	}

	// The oracle. This is the shape a lost late rewrite reproduces, and it is reachable
	// by changing nothing but the late pass's contribution.
	if control.openScan.realRoot < 1 {
		t.Fatalf("fixture: the run without the late pass must reach Backend.Open carrying the restored surface as a real path: %s virtual=%d real_root=%d",
			hookRegOrderOf(control), control.openScan.virtual, control.openScan.realRoot)
	}
	if delta := control.openScan.callBytes - got.openScan.callBytes; delta <= 0 {
		t.Fatalf("fixture: the backend-bound request must be strictly smaller once the late pass publishes its alias: %s control_body_bytes=%d backend_open_body_bytes=%d",
			order, control.openScan.callBytes, got.openScan.callBytes)
	}
}

// ---------------------------------------------------------------------------
// Task 5.3 extension: the same harness, driven with the anchor on a message the real
// early pass rewrites.
//
// WHY THIS EXTENDS THIS FILE
//
// Task 1.3 deliberately anchored its overlay on the FIRST user message, which neither
// outbound pass mutates, so its assertions stay about the runtime's own ordering. Task
// 1.3's anchor-identity half then showed what happens when the anchor IS the message a
// rewrite mutates: the frozen anchor stops resolving and the executor denies the turn
// pre-backend. Design.md "4A" assigns the generic repair for that to Task 5.3, and the
// Implementation Notes for 5.2 require this file - not a second harness - to carry it.
//
// WHY THE FIXTURE'S LATE SHAPER IS OFF HERE
//
// hookRegShaping APPENDS a complete message, which is an insertion. A one-to-one
// trajectory lineage must refuse an insertion (requirements.md 5.10), so installing it
// here would conflate "the late pass catches a restored real path" with "the trajectory
// changed shape". The drift scenarios therefore install no late shaper, which is the one
// variable they change relative to the standard scenario.
//
// NO WEAKENING, NO PATH LEAKAGE
//
// The standard scenario's fixture, resolver, shaper, assertions, and control run are
// untouched. Every message below is requirement-numbered and path-content-free:
// occurrence counts, byte totals, stage ordinals, and booleans. The alias and the real
// root are compared, never formatted, and the workspace tag is derived in-test only.
// ---------------------------------------------------------------------------

// hookRegPerturbationOrder places the fixture's structural perturbation immediately
// after its late shaper and far below the feature hook's fixed order, so the chain
// really runs shaper-then-perturbation-then-feature-hook.
const hookRegPerturbationOrder = hookRegShapingOrder + 1

// hookRegPerturbation is the fixture's structural-perturbation participant. It is not
// a rewriter and it is not a path hazard: its whole job is to make the trajectory
// structurally ambiguous AFTER the early pass has already virtualized the anchored
// message, so the final reassertion has to refuse to carry the placement forward.
//
// It also records the two guards the negative assertions depend on: that the anchored
// message was locatable, and that its content-derived identity had ALREADY drifted by
// the time the perturbation ran. Without the second guard a passing denial could be a
// fixture artifact rather than a lineage refusal.
type hookRegPerturbation struct {
	label  string
	loc    driftLocator
	stored conversationprojection.MessageAnchor
	apply  func(call *aipapi.Call)

	mu             sync.Mutex
	locatorFound   bool
	identityDrift  bool
	applied        bool
	mutatedMessage int
}

// hookRegPerturbationFor builds the perturbation for one anchored surface.
func hookRegPerturbationFor(t *testing.T, label string, anchor aipapi.Message, loc driftLocator, apply func(call *aipapi.Call)) *hookRegPerturbation {
	t.Helper()
	return &hookRegPerturbation{
		label:  label,
		loc:    loc,
		stored: driftStoredAnchor(t, anchor),
		apply:  apply,
	}
}

func (p *hookRegPerturbation) ID() string {
	return "path-virtualization-lineage-perturbation-" + p.label
}
func (p *hookRegPerturbation) Order() int                        { return hookRegPerturbationOrder }
func (p *hookRegPerturbation) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (p *hookRegPerturbation) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if msg, found := driftFindAnchor(*call, p.loc); found {
		p.locatorFound = true
		if id, err := conversationprojection.MessageIdentityOf(msg); err == nil {
			p.identityDrift = id != p.stored.Identity
		}
	}
	before := len(call.Messages)
	p.apply(call)
	p.applied = true
	p.mutatedMessage = len(call.Messages) - before
	return nil
}

func (p *hookRegPerturbation) observation() (locatorFound, identityDrift, applied bool, delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.locatorFound, p.identityDrift, p.applied, p.mutatedMessage
}

// hookRegViewObserver records the final conversation-view stage for BOTH outcomes.
//
// The shared Task 1.3 observer marks the reassertion checkpoint only on a SUCCESSFUL
// final projection, which is right for a run that is supposed to succeed. A negative
// regression has to observe the stage it denies AT, so this thin wrapper additionally
// marks the checkpoint when the final stage fails. It delegates every other callback
// to Task 1.3's observer, which is where the anchor-failure and fallback publications
// are counted.
type hookRegViewObserver struct {
	*driftViewObserver
}

func (o *hookRegViewObserver) OnProjectionFailure(stage string) {
	o.driftViewObserver.OnProjectionFailure(stage)
	if stage == conversationprojection.StageFinal {
		o.rec.mark(twoPassCheckpointViewReassert)
	}
}

// hookRegSteeringAfterAnchor measures one backend-bound call's steering placement
// relative to its anchored message, reusing Task 1.3's content-free locator.
func hookRegSteeringAfterAnchor(call aipapi.Call, loc driftLocator) (copies int, immediatelyAfter bool) {
	return driftSteeringPlacement(call, loc)
}

// The six structural perturbations. Each one changes the trajectory in exactly one
// documented way. The equal-cardinality cases are the anti-ordinal oracle: they keep
// every frozen ordinal slot occupied, so an implementation that relocated the overlay
// by position would succeed and be caught here instead.
var (
	// An appended complete message grows the trajectory beyond the frozen baseline.
	hookRegInsertMessage = func(call *aipapi.Call) {
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("lineage-perturbation-inserted")},
		})
	}

	// Removing the leading path-free control message shrinks the trajectory.
	hookRegDeleteMessage = func(call *aipapi.Call) {
		call.Messages = call.Messages[1:]
	}

	// Prepending a system message while removing the leading control message keeps the
	// trajectory length IDENTICAL while changing the role at the frozen leading slot, so
	// the anchored message no longer occupies the ordinal its frozen anchor named.
	hookRegRoleShift = func(call *aipapi.Call) {
		rest := call.Messages[1:]
		call.Messages = append([]aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("lineage-perturbation-system")},
		}}, rest...)
	}

	// Swapping the anchored message with its predecessor keeps every ordinal occupied but
	// changes the structure at both frozen slots.
	hookRegReorder = func(call *aipapi.Call) {
		call.Messages[0], call.Messages[1] = call.Messages[1], call.Messages[0]
	}

	// Changing the anchored message's ordered part kind is a structural change even
	// though the trajectory length, the role, and the message count are untouched.
	hookRegPartKindChange = func(call *aipapi.Call) {
		for mi := range call.Messages {
			for pi := range call.Messages[mi].Parts {
				part := &call.Messages[mi].Parts[pi]
				if part.Kind != aipapi.PartJSON {
					continue
				}
				part.Kind = aipapi.PartText
				part.Text = "lineage-perturbation-kind-changed"
				part.Content = nil
				return
			}
		}
	}

	// Renaming the anchored part's stable tool-call ID is a stable-identity change the
	// lineage proof compares and identity alone cannot see, because a legacy PartJSON
	// atom omits the tool-call ID's neighbours from the hashed content.
	hookRegToolCallIDChange = func(call *aipapi.Call) {
		for mi := range call.Messages {
			for pi := range call.Messages[mi].Parts {
				part := &call.Messages[mi].Parts[pi]
				if part.Kind != aipapi.PartJSON {
					continue
				}
				part.ToolCallID = "lineage-perturbation-renamed"
				return
			}
		}
	}
)

// hookRegCallFromBody rebuilds a canonical call from an already-serialized backend-bound
// body so the per-turn buffer can be measured the same way as Backend.Open.
func hookRegCallFromBody(body []byte) aipapi.Call {
	var call aipapi.Call
	if len(body) == 0 {
		return call
	}
	if err := json.Unmarshal(body, &call); err != nil {
		return aipapi.Call{}
	}
	return call
}

// TestOutboundAttempt_RequestPartHookPreservesVirtualizedAnchoredHistoryThroughBackendOpen
// is requirements.md 5.2, 5.4, 5.9, and 8.5 with the frozen steering overlay anchored on
// the very message the feature rewrites.
//
// For three legacy surfaces - the stock-reachable PartJSON argument, the operator-profile
// PartToolResult opaque text, and the operator-profile PartToolResult structured Content -
// it drives the real executor with both real outbound passes and asserts:
//
//   - the turn completes with no conversation-view failure, no anchor failure, and no
//     fallback, so the final reassertion neither denied it nor downgraded the stored
//     fail-closed policy;
//   - the frozen conversation view is read exactly once, so carry-forward reused
//     request-local evidence and did not re-read the store;
//   - the anchored selected payload carries the derived alias and no real-root
//     occurrence at BOTH the per-turn buffer and Backend.Open, with every non-path
//     sibling intact, so final reassertion and candidate adaptation restored no real path;
//   - the overlay is present exactly ONCE and IMMEDIATELY after the same logical message
//     at Backend.Open even though that message's content hash changed, which is
//     requirements.md 5.9's carried placement;
//   - the never_backend-tagged message is still absent at the backend bound.
func TestOutboundAttempt_RequestPartHookPreservesVirtualizedAnchoredHistoryThroughBackendOpen(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		label    string
		anchor   aipapi.Message
		loc      driftLocator
		resolver func(*testing.T) *pathvirtualization.Resolver
	}{
		{
			label:  driftSurfaceCallArgument,
			anchor: driftCallAnchorMessage(),
			loc:    driftCallLocator(),
			// nil resolver: the shipped built-in layer alone already claims this exact
			// tool name's argument member, so this surface needs no operator config.
			resolver: nil,
		},
		{
			label:    driftSurfaceResultOpaqueText,
			anchor:   driftResultTextAnchorMessage(),
			loc:      driftResultTextLocator(),
			resolver: driftOpaqueResultResolver,
		},
		{
			label:    driftSurfaceResultStructured,
			anchor:   driftResultContentAnchorMessage(),
			loc:      driftResultContentLocator(),
			resolver: driftStructuredResultResolver,
		},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			t.Parallel()

			got := hookRegRun(t, hookRegDriftScenario(t, scenario.label, scenario.anchor, scenario.resolver, nil))
			order := hookRegOrderOf(got)

			// Scaffolding. Every assertion below reads these.
			if !got.turnDone {
				t.Fatalf("requirements.md 5.9 - a provably one-to-one structure-preserving rewrite must not deny the turn: scenario=%s turn_done=%t anchor_missing=%t projection_failure_stages=%d anchor_failures=%d anchor_fallbacks=%d reached_reassertion=%d reached_ptb=%d backend_open_stage=%d",
					got.label, got.turnDone, got.anchorMissing, len(got.failureStages), got.anchorFailures,
					got.fallbacks, got.order.reassert, got.order.ptb, got.order.open)
			}
			if got.anchorMissing || len(got.failureStages) != 0 || got.anchorFailures != 0 {
				t.Fatalf("requirements.md 5.9 - the final reassertion must carry the resolved placement, not fail closed: scenario=%s anchor_missing=%t projection_failure_stages=%d anchor_failures=%d",
					got.label, got.anchorMissing, len(got.failureStages), got.anchorFailures)
			}
			if got.fallbacks != 0 {
				t.Fatalf("design.md \"4A\" constraint 1 - the stored fail-closed policy must never be downgraded to a stable-prefix fallback: scenario=%s anchor_fallbacks=%d",
					got.label, got.fallbacks)
			}
			if got.order.reassert == 0 || got.order.ptb == 0 || got.order.open == 0 {
				t.Fatalf("fixture: the reassertion, per-turn buffer, and Backend.Open stages must all be reached: scenario=%s", got.label)
			}
			if got.snapshotReads != 1 {
				t.Fatalf("design.md \"4A\" constraint 3 - the final reassertion must reuse the FROZEN request-local snapshot and must not read the conversation-view store again: scenario=%s snapshot_reads=%d",
					got.label, got.snapshotReads)
			}
			ptbCall := hookRegCallFromBody(got.ptbBody)
			for _, stage := range []struct {
				name string
				call aipapi.Call
			}{
				{"per_turn_buffer", ptbCall},
				{"Backend.Open", got.backendOpenCall},
			} {
				anchor, found := driftFindAnchor(stage.call, scenario.loc)
				if !found {
					t.Fatalf("fixture: the anchored message must survive both real passes and candidate adaptation: scenario=%s stage=%s",
						got.label, stage.name)
				}
				part := driftInspectPart(anchor, scenario.loc, driftAliasOf(t))
				if !part.found || !part.selectedCarriesAlias || part.selectedCarriesRealRoot {
					t.Fatalf("requirements.md 5.2 and 5.4 - final reassertion and candidate adaptation must not restore a real root into path-bearing tool history: scenario=%s stage=%s selected_carries_alias=%t selected_carries_real_root=%t %s",
						got.label, stage.name, part.selectedCarriesAlias, part.selectedCarriesRealRoot, order)
				}
				if part.nonPathSum != scenario.loc.expectedNonPath() {
					t.Fatalf("requirements.md 2.8 - every non-selected member of the anchored payload must survive both real passes byte-for-byte: scenario=%s stage=%s non_path_sum=%d expected_non_path_sum=%d",
						got.label, stage.name, part.nonPathSum, scenario.loc.expectedNonPath())
				}
				copies, immediatelyAfter := hookRegSteeringAfterAnchor(stage.call, scenario.loc)
				if copies != 1 || !immediatelyAfter {
					t.Fatalf("requirements.md 5.9 and design.md \"4A\" - the overlay must be restored exactly once at the SAME logical boundary even though that message's content hash changed: scenario=%s stage=%s steering_copies=%d steering_immediately_after_anchor=%t",
						got.label, stage.name, copies, immediatelyAfter)
				}
				if _, present := driftFindTextMessage(stage.call, twoPassLocalText); present {
					t.Fatalf("design.md \"4A\" constraint 6 - a never_backend-tagged message must stay out of the backend-bound request: scenario=%s stage=%s",
						got.label, stage.name)
				}
			}

			// design.md "Existing Architecture and Placement" step 6 before steps 9 and 10.
			if got.order.reassert >= got.order.ptb || got.order.ptb >= got.order.open {
				t.Fatalf("requirements.md 5.2/5.4 - the final reassertion must complete before the per-turn buffer, which must complete before Backend.Open: scenario=%s %s",
					got.label, order)
			}
			// The real late pass contributes nothing here: the early pass already
			// published the alias, so reapplying it is idempotent (requirements.md 2.9).
			if late := got.reports.onePart(t); late.Stats.Rewritten != 0 {
				t.Fatalf("requirements.md 2.9 - the idempotent late pass must contribute nothing once the early pass published the alias: scenario=%s late_rewritten=%d",
					got.label, late.Stats.Rewritten)
			}
			if early := got.reports.oneAttempt(t); early.Stats.Rewritten != 1 {
				t.Fatalf("fixture: the real early pass must virtualize exactly the one selected path payload: scenario=%s attempt_rewritten=%d",
					got.label, early.Stats.Rewritten)
			}
		})
	}
}

// TestOutboundAttempt_AmbiguousTrajectoryLineageDeniesTurnPreBackend is requirements.md
// 5.10 sentence 2 at the runtime boundary.
//
// Each scenario installs a structural perturbation on the request-part plane AFTER the
// real early pass has already virtualized the anchored message, so the frozen anchor
// genuinely cannot resolve and only a proven one-to-one lineage could rescue the
// placement. Three of the six keep the trajectory length identical, which is what makes
// them the anti-ordinal oracle: a reassertion that relocated the overlay by position
// would find a slot, succeed, and reach Backend.Open.
//
// The observable denial is the triple the executor actually publishes: the
// ErrAnchorMissing-shaped failure from Execute, exactly one AnchorFailClosed publication
// with zero fallbacks, and neither the per-turn buffer nor Backend.Open being reached.
func TestOutboundAttempt_AmbiguousTrajectoryLineageDeniesTurnPreBackend(t *testing.T) {
	t.Parallel()

	locator := driftCallLocator()
	for _, perturbation := range []struct {
		label string
		apply func(call *aipapi.Call)
		// equalCardinality marks the anti-ordinal oracle cases: they keep the
		// trajectory length unchanged, so a reassertion that relocated the overlay by
		// position would also find a slot and succeed.
		equalCardinality bool
	}{
		{label: "trajectory_cardinality_grew_by_insertion", apply: hookRegInsertMessage},
		{label: "trajectory_cardinality_shrank_by_deletion", apply: hookRegDeleteMessage},
		{label: "equal_cardinality_ordinal_shift_with_role_change", apply: hookRegRoleShift, equalCardinality: true},
		{label: "equal_cardinality_reorder", apply: hookRegReorder, equalCardinality: true},
		{label: "equal_cardinality_part_kind_change", apply: hookRegPartKindChange, equalCardinality: true},
		{label: "equal_cardinality_stable_tool_call_id_change", apply: hookRegToolCallIDChange, equalCardinality: true},
	} {
		t.Run(perturbation.label, func(t *testing.T) {
			t.Parallel()

			anchor := driftCallAnchorMessage()
			perturb := hookRegPerturbationFor(t, perturbation.label, anchor, locator, perturbation.apply)
			got := hookRegRun(t, hookRegDriftScenario(t, perturbation.label, anchor, nil, perturb))
			order := hookRegOrderOf(got)

			locatorFound, identityDrift, applied, delta := perturb.observation()
			if !locatorFound || !identityDrift {
				t.Fatalf("fixture: the anchored message must be locatable AND already identity-drifted before the perturbation runs, otherwise the denial below proves nothing: scenario=%s locator_found=%t identity_drift=%t applied=%t message_delta=%d",
					got.label, locatorFound, identityDrift, applied, delta)
			}
			if !applied {
				t.Fatalf("fixture: the structural perturbation must reach the request-part stage: scenario=%s", got.label)
			}
			if perturbation.equalCardinality && delta != 0 {
				t.Fatalf("fixture: an equal-cardinality perturbation must keep the trajectory length unchanged so a positional relocation would also succeed: scenario=%s message_delta=%d",
					got.label, delta)
			}
			if !perturbation.equalCardinality && delta == 0 {
				t.Fatalf("fixture: a trajectory-length perturbation must actually change the trajectory length: scenario=%s message_delta=%d",
					got.label, delta)
			}
			if got.order.reassert == 0 {
				t.Fatalf("fixture: the final reassertion stage must be reached: scenario=%s %s", got.label, order)
			}
			if got.order.part == 0 || got.order.reassert <= got.order.part {
				t.Fatalf("fixture: the perturbation runs on the request-part plane, so the reassertion stage must come after it: scenario=%s message_delta=%d %s",
					got.label, delta, order)
			}

			if got.turnDone {
				t.Fatalf("requirements.md 5.10 - ambiguous lineage must not guess by position, so the turn must not reach Backend.Open: scenario=%s turn_done=%t ptb_stage=%d backend_open_stage=%d",
					got.label, got.turnDone, got.order.ptb, got.order.open)
			}
			if !got.anchorMissing {
				t.Fatalf("requirements.md 5.10 - the final reassertion must fail closed with the anchor-missing failure when lineage cannot be proven: scenario=%s anchor_missing=%t projection_failure_stages=%d anchor_failures=%d",
					got.label, got.anchorMissing, len(got.failureStages), got.anchorFailures)
			}
			if got.anchorFailures != 1 {
				t.Fatalf("design.md \"4A\" constraint 1 - the stored fail-closed policy must publish exactly one anchor failure: scenario=%s anchor_failures=%d",
					got.label, got.anchorFailures)
			}
			if got.fallbacks != 0 {
				t.Fatalf("design.md \"4A\" constraint 1 - the fail-closed policy must never be downgraded to a stable-prefix fallback: scenario=%s anchor_fallbacks=%d",
					got.label, got.fallbacks)
			}
			if got.order.ptb != 0 {
				t.Fatalf("requirements.md 5.10 - the denial must happen before the per-turn buffer: scenario=%s reached_ptb=%d",
					got.label, got.order.ptb)
			}
			if got.order.open != 0 {
				t.Fatalf("requirements.md 5.10 - no candidate may reach Backend.Open when the lineage proof fails: scenario=%s backend_open_stage=%d",
					got.label, got.order.open)
			}
			if got.snapshotReads != 1 {
				t.Fatalf("design.md \"4A\" constraint 3 - the refusal must reuse the frozen snapshot without a second store read: scenario=%s snapshot_reads=%d",
					got.label, got.snapshotReads)
			}
		})
	}
}

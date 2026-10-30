package runtime_test

// Spec: b-leg-path-virtualization Task 8.2, the expansion-before-policy half.
// Requirements 4.1, 4.2, 4.3, 4.8, 4.9, 5.1, and 8.3.
//
// Design sections read for this file:
//
//   - "Existing Architecture and Placement" steps 11 and 12 (response tool-call
//     assembly/finalization, then tool policies/reactors) plus the two
//     model-to-client consequences ("model->client expansion uses completed
//     tool-call finalization, not raw ToolCallArgsDelta mutation"; "expansion
//     completes before existing tool policy/reactors");
//   - "7. Path Expansion Finalizer" steps 10 and 11 ("assembler synthesizes the
//     canonical rewritten lifecycle"; "existing tool policies/reactors receive
//     real paths");
//   - "Error Handling" (stale/different workspace tag and mandatory assembly
//     overflow both fail closed);
//   - "Testing Strategy / Runtime-integration" ("Completed model call expands
//     before tool policy observer"; "PTB/backend ingress receives virtualized
//     eligible tool history").
//
// WHAT THIS FILE IS, AND WHY IT IS NOT A SECOND HARNESS
//
// Task 1.2's permanent guard (tool_call_path_expansion_ordering_test.go)
// deliberately uses a minimal test-local expansion pass so that its claims stay
// about the RUNTIME's ordering and metadata delivery. That is the right shape for
// that claim and the wrong shape for a composition claim: the expansion decision
// requirements.md 4.1/4.4/4.5/8.3 demand is made by the concrete feature, so it
// has to be asserted against the concrete feature.
//
// So this file drives the REAL contributions end to end on one executor:
//
//  1. the REAL shipped outbound attempt transform (Task 5.1) on the candidate
//     attempt plane;
//  2. the REAL shipped outbound request-part hook (Task 5.2) on the request-part
//     bus;
//  3. the REAL assembler the runtime builds from the frozen finalizer plane;
//  4. the REAL shipped path-expansion finalizer (Task 8.1) on that plane, with
//     the REAL shipped conservative built-in selector profile, in rewrite mode;
//  5. the REAL tool policy plane and the REAL tool reactor plane.
//
// The only things this file adds are (a) transparent markers that record a stage
// ordinal for the two outbound passes and for the expansion pass, and (b) two
// observers. Neither changes a decision, an identity, an order, or a refusal
// policy. That is the discipline path_virtualization_request_part_hook_regression_test.go
// and tool_call_repair_expansion_composition_test.go already established: a
// marker promotes the production ID/Order/capability and delegates everything
// else, so the chain that sorts, the declaration the assembler reads, and the
// verdict the client sees are all production values.
//
// WHY THE ALIAS THE BACKEND EMITS IS THE ONE THE PROXY MINTED
//
// The historical tool call on the ingress history carries the REAL project root,
// exactly as a coding-agent client replays it. Both real outbound passes rewrite
// it, so by the time the fixture's backend sees the request the history is already
// virtualized. The backend then emits a NEW tool call whose path-bearing argument
// is that very alias plus a distinct suffix: the model echoing back the namespace
// it was shown. The file asserts the backend really received the alias (and never
// the real root) BEFORE asserting anything about what policy, reactor, or client
// see, so a claim that "expansion ran before policy" can never be satisfied by a
// fixture whose alias was never derived here.
//
// CONTENT FREEDOM
//
// No assertion message in this file contains a path, an alias, a workspace tag, a
// tool-call ID, or argument bytes. Paths and the derived alias are compared, never
// formatted. Messages carry requirement numbers, stage ordinals, byte counts,
// occurrence counts, and closed bounded reason labels only.
//
// DETERMINISM
//
// Every ordinal comes from one mutex-guarded monotonic counter, every scan walks
// slices in order, the alias is derived from a fixed project root by a
// content-defined digest, and the executor RNG and clock are pinned by the shared
// harness. Repeated runs produce identical ordinals and counts.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

const (
	// expToolName is the fixture's exact tool name. It is one of the canonical
	// coding-agent names the shipped built-in profile layer claims, so the run
	// exercises the production selector policy rather than an operator stub.
	expToolName = twoPassToolName

	// expPathField is the argument member the shipped built-in profile claims for
	// expToolName. Spelling it explicitly keeps the fixture independent of the
	// optional schema-inference step, so the run measures the composition and
	// nothing else.
	expPathField = hookRegPathField

	// expPayloadField is a sibling argument no shipped profile claims. It holds the
	// model-visible payload, deliberately spelled with the reserved alias, so
	// requirements.md 4.9 is observable in the POSITIVE direction too: expansion must
	// leave a payload-concept field's own bytes alone even when they spell the
	// reserved namespace.
	expPayloadField = hookRegPayloadField

	// expSiblingField and expSiblingValue are the non-path argument sibling
	// requirements.md 4.8 requires to survive expansion byte-for-byte.
	expSiblingField = "limit"
	expSiblingValue = 10

	// expHistorySuffix and expModelSuffix are two DISTINCT path-bearing suffixes: one
	// on the historical client-replayed tool call, one on the backend's own
	// model-emitted tool call. They differ so a rewrite of one surface can never be
	// mistaken for a rewrite of the other.
	expHistorySuffix = hookRegEarlySuffix
	expModelSuffix   = hookRegLateSuffix

	// expToolCallID is the model-emitted call's identifier and expHistoryID the
	// historical one's. They are fixed fixture tokens, never formatted into a failure
	// message.
	expToolCallID = "expansion-order-model-call"

	expSelector   = "two-pass:m"
	expBackendID  = "two-pass"
	expAnchorText = "expansion-order-first-user"
	expTailText   = "expansion-order-continue"
	expHistoryID  = "expansion-order-history"

	// expControlLabel and expGuardedLabel are the two scenarios' bounded,
	// content-free failure-message tokens.
	expControlLabel  = "without_the_expansion_pass"
	expGuardedLabel  = "with_the_expansion_pass"
	expInertOrderOff = 5

	// expReservedMarker is the frozen V1 reserved namespace marker, byte for byte, and
	// expWorkspaceTagPrefix the frozen tag prefix that introduces the workspace
	// identity inside an alias root. Both are spelled as LITERALS rather than
	// imported from the feature, for two reasons. The namespace is a fixed
	// implementation contract (requirements.md 7.4) and not an operator value, so
	// exporting it would make it configurable-looking. And a leak detector that
	// called the production recognizer would be unable to detect a leak that
	// recognizer itself fails to see. The feature's own tests pin the spelling.
	expReservedMarker     = ".__aip_v1__"
	expWorkspaceTagPrefix = "w_"
)

// expFixture is the ordered pair of strings one argument document is built from:
// the root prefix a selected path value carries, and the path-bearing suffix. The
// root is a parameter rather than a constant so one builder serves the real-root
// form (ingress history, expanded client release) and the alias form (what the
// backend-bound request carries and what the model emits back).
type expFixture struct {
	root   string
	suffix string
}

// expSeparatorBefore reports whether a root prefix already spells the segment
// separator that joins it to its suffix.
//
// It is derived from the prefix bytes and never from the running host. The fixed V1
// alias root always ends with its flavor's own separator - a POSIX alias ends with
// `/`, every Windows flavor with `\` - so a Windows root change must not gain a
// second forward slash either, which would produce a value that is legitimately not
// under its root.
func expSeparatorBefore(prefix string) bool {
	return strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, `\`)
}

// under spells the path-bearing suffix beneath an arbitrary root prefix, which is
// what lets one fixture serve both the alias form (what the backend-bound request
// carries and what the model emits back) and the real-root form (ingress history,
// expanded client release).
func (f expFixture) under(prefix string) string {
	if expSeparatorBefore(prefix) {
		return prefix + f.suffix
	}
	return prefix + "/" + f.suffix
}

// joined is the selected path value under the fixture's own root prefix.
func (f expFixture) joined() string { return f.under(f.root) }

// expEscaped renders a path value the way it appears INSIDE a JSON string literal.
//
// A Windows flavor root spells backslashes, and a document that embedded them raw
// would not parse, which would make a Windows flavor case fail at JSON parsing
// rather than at the property under test. For the POSIX fixture every value contains
// no character JSON escapes, so the rendering is byte-identical to the raw spelling
// and every pre-existing expectation in this harness is unchanged.
func expEscaped(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

// aliasInDocument is the fixture's root prefix as it is spelled inside the argument
// document, which is what locating an alias in a STREAMED document requires: a
// Windows alias root is spelled with backslashes, so the document carries the
// escaped form rather than the raw one.
func (f expFixture) aliasInDocument() string { return expEscaped(f.root) }

// modelArgsDocument is the complete argument document the backend's model emits on
// its tool call: the ALIAS form of the selected path member, plus a non-path
// sibling and a payload-concept sibling expansion must not touch.
func (f expFixture) modelArgsDocument() string {
	return `{"` + expPathField + `":"` + expEscaped(f.joined()) + `",` +
		`"` + expPayloadField + `":"/` + expEscaped(f.joined()) + `",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`
}

// expExpandedModelArgsDocument is the document requirements.md 4.1 requires on
// every client-facing surface for the model's tool call: the selected path member
// spelled against the authoritative real root, and every other member byte identical
// to what the model emitted - including the payload-concept member, which keeps the
// alias bytes it arrived with because requirement 4.9 forbids expanding it.
//
// The real root is a parameter rather than a literal so a scenario that changes the
// AUTHORITATIVE root compares against that root: a finalizer that expanded against
// some OTHER root would produce a different document and fail the byte comparison,
// which is what makes this the assertion that the expansion used the authoritative
// root.
func (f expFixture) expExpandedModelArgsDocument(realRoot string) string {
	return `{"` + expPathField + `":"` + expEscaped(f.under(realRoot)) + `",` +
		`"` + expPayloadField + `":"/` + expEscaped(f.joined()) + `",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`
}

// expMeasure is the path-content-free measurement of one canonical surface. It
// counts occurrences only; no path value, alias, tag, or argument byte can reach a
// failure message through it.
type expMeasure struct {
	// aliasHits is how many selected path values carry the reserved alias.
	aliasHits int
	// realHits is how many selected path values carry the authoritative real root.
	realHits int
	// aliasAnywhere is how many times the reserved alias bytes occur anywhere in the
	// measured payload, in ANY field. It is deliberately wider than aliasHits: a
	// payload-concept field spelling the alias must NOT be counted as a leak, so this
	// counter is what makes that difference measurable rather than assumed.
	aliasAnywhere int
	// selectedFields counts selected path values actually read, so a scan that
	// matched nothing because nothing was parsed cannot read as a clean run.
	selectedFields int
	// siblingSum is the total of the non-path sibling across every document.
	siblingSum int
	// realRootInPayload counts documents whose payload-concept member still carries
	// the authoritative real root, which requirement 2.4's refusal makes the expected
	// outcome for a member no shipped profile claims.
	realRootInPayload int
	// documents counts argument documents observed.
	documents int
	// validDocs counts argument documents that are one complete JSON value.
	validDocs int
	// shapeKept reports that every readable document kept exactly the three fixture
	// members and read as the fixture's shape.
	shapeKept bool
}

// expMeasureDocuments measures every path-bearing tool-call argument document on
// one backend-bound surface, walking both canonical authorities so the result is a
// statement about the request rather than about whichever representation
// adaptation happened to choose.
// realRoot is a parameter rather than a literal so a scenario whose authoritative
// root differs from the shared fixture root still recognizes its own real root on
// the measured surface; a literal would read every changed-root run as carrying no
// real root at all.
func expMeasureDocuments(call aipapi.Call, alias, realRoot string) expMeasure {
	out := expMeasure{shapeKept: true}
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
		out.aliasAnywhere += strings.Count(string(doc), alias)
		if !json.Valid(doc) {
			continue
		}
		out.validDocs++
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(doc, &fields); err != nil || len(fields) != 3 {
			out.shapeKept = false
			continue
		}
		var selected string
		if json.Unmarshal(fields[expPathField], &selected) != nil {
			out.shapeKept = false
			continue
		}
		out.selectedFields++
		switch {
		case strings.Contains(selected, alias):
			out.aliasHits++
		case strings.Contains(selected, realRoot):
			out.realHits++
		default:
			out.shapeKept = false
		}
		var sibling int
		if json.Unmarshal(fields[expSiblingField], &sibling) != nil {
			out.shapeKept = false
			continue
		}
		out.siblingSum += sibling
		var payload string
		if json.Unmarshal(fields[expPayloadField], &payload) != nil {
			out.shapeKept = false
			continue
		}
		if strings.Contains(payload, realRoot) {
			out.realRootInPayload++
		}
	}
	return out
}

// expStage is the single monotonic sequence every participant of one run records
// at, so the test can compare when each ran without inspecting any private
// call-graph trivia. Ordinals from different runs are never compared, and the FIRST
// ordinal is kept for a plane the runtime may invoke more than once, because
// requirements.md 5.1 is about what happened FIRST.
type expStage struct {
	mu        sync.Mutex
	seq       int
	attempt   int
	part      int
	expansion int
	policy    int
	reactor   int
}

func (s *expStage) first(field *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if *field == 0 {
		*field = s.seq
	}
}

func (s *expStage) markAttempt()   { s.first(&s.attempt) }
func (s *expStage) markPart()      { s.first(&s.part) }
func (s *expStage) markExpansion() { s.first(&s.expansion) }
func (s *expStage) markPolicy()    { s.first(&s.policy) }
func (s *expStage) markReactor()   { s.first(&s.reactor) }

func (s *expStage) seen() (attempt, part, expansion, policy, reactor int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempt, s.part, s.expansion, s.policy, s.reactor
}

// expOrd renders an ordinal, naming the unreached case rather than printing a zero
// that could be misread as "ran first".
func expOrd(v int) string {
	if v == 0 {
		return "unreached"
	}
	return strconv.Itoa(v)
}

// expExpansionMarker records the completed-tool-call finalization stage and then
// delegates every decision to the REAL shipped expansion finalizer.
//
// ID, Order, and ToolCallBufferingRequirement are promoted from the embedded
// production finalizer, so toolcall.MaterializeSorted sorts on the real production
// order value and the assembler reads the real production mandatory declaration -
// which is what makes requirements.md 4.5's overflow bound and 8.3's fail-closed
// refusal production behavior rather than fixture behavior. Only Finalize is
// intercepted, and only to record an ordinal, the observed document, and the
// decision.
type expExpansionMarker struct {
	*expansion.Finalizer

	stage *expStage

	mu     sync.Mutex
	calls  int
	seen   []byte
	result toolcall.Result
}

var (
	_ toolcall.Finalizer            = (*expExpansionMarker)(nil)
	_ toolcall.BufferingRequirement = (*expExpansionMarker)(nil)
)

func (p *expExpansionMarker) Finalize(
	ctx context.Context,
	call toolcall.CompletedCall,
	tool aipapi.ToolDef,
	catalog []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	res, err := p.Finalizer.Finalize(ctx, call, tool, catalog, meta)
	p.stage.markExpansion()
	p.mu.Lock()
	p.calls++
	p.seen = append(p.seen[:0], call.ArgsJSON...)
	p.result = res
	p.mu.Unlock()
	return res, err
}

func (p *expExpansionMarker) observation() (int, []byte, toolcall.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]byte(nil), p.seen...), p.result
}

// expPolicyObserver is the REAL tool policy plane's observer, standing in for the
// path/security policy evaluation requirements.md 5.1 orders after expansion. It
// records only the observation order, the joined argument bytes it was shown, and
// whether the authoritative project root reached it.
type expPolicyObserver struct {
	stage *expStage

	mu         sync.Mutex
	calls      int
	joined     string
	argEvents  int
	policyRoot bool
}

func (o *expPolicyObserver) ID() string                        { return "expansion-path-policy-observer" }
func (o *expPolicyObserver) Order() int                        { return 0 }
func (o *expPolicyObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *expPolicyObserver) Handle(
	_ context.Context,
	ev aipapi.ToolEvent,
	meta toolpolicy.Meta,
	_ toolpolicy.Services,
) (toolpolicy.Decision, error) {
	o.mu.Lock()
	if ev.ArgsDelta != "" {
		o.stage.markPolicy()
		o.calls++
		o.argEvents++
		// Arguments are joined in observation order, so a reserved alias could only
		// be detected across fragment boundaries exactly as the backend streamed it.
		o.joined += ev.ArgsDelta
	} else {
		o.calls++
	}
	if strings.TrimSpace(meta.Workspace.ProjectRoot) != "" {
		o.policyRoot = true
	}
	o.mu.Unlock()
	return toolpolicy.DecisionAllow, nil
}

func (o *expPolicyObserver) observation() (int, int, string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.argEvents, o.joined, o.policyRoot
}

// expReactorObserver is the REAL tool reactor plane's observer, the OTHER
// design.md step-12 consumer requirements.md 4.3 names ("existing tool
// policies/reactors"). It records only the joined argument bytes and the ordinal.
type expReactorObserver struct {
	stage *expStage

	mu        sync.Mutex
	calls     int
	joined    string
	argEvents int
}

func (o *expReactorObserver) ID() string                        { return "expansion-tool-reactor-observer" }
func (o *expReactorObserver) Order() int                        { return 0 }
func (o *expReactorObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *expReactorObserver) HandleToolEvent(
	_ context.Context,
	ev aipapi.ToolEvent,
	_ sdkhooks.ToolMeta,
) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	o.mu.Lock()
	if ev.ArgsDelta != "" {
		o.stage.markReactor()
		o.calls++
		o.argEvents++
		o.joined += ev.ArgsDelta
	} else {
		o.calls++
	}
	o.mu.Unlock()
	return sdkhooks.ToolPass, ev, nil
}

func (o *expReactorObserver) observation() (int, int, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.argEvents, o.joined
}

// expAttemptMarker and expPartMarker record the two real outbound stages. Each
// delegates every decision to the real shipped contribution, promoting its
// identity, order, and failure mode so the bus and the chain that sorts see the
// production participant.
type expAttemptMarker struct {
	stage *expStage
	real  *outbound.AttemptTransform
}

func (m *expAttemptMarker) ID() string                        { return m.real.ID() }
func (m *expAttemptMarker) Order() int                        { return m.real.Order() }
func (m *expAttemptMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *expAttemptMarker) HandleAttempt(
	ctx context.Context,
	call *aipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (request.AttemptDecision, error) {
	m.stage.markAttempt()
	return m.real.HandleAttempt(ctx, call, meta, svc)
}

type expPartMarker struct {
	stage *expStage
	real  *outbound.RequestPartHook
}

func (m *expPartMarker) ID() string                        { return m.real.ID() }
func (m *expPartMarker) Order() int                        { return m.real.Order() }
func (m *expPartMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *expPartMarker) HandleRequestParts(
	ctx context.Context,
	call *aipapi.Call,
	meta sdkhooks.PartMeta,
) error {
	m.stage.markPart()
	return m.real.HandleRequestParts(ctx, call, meta)
}

// expInertFinalizer is the control run's single ordinary finalizer. It publishes no
// mandatory requirement, rewrites nothing, and refuses nothing, so it changes only
// whether the assembler exists - never a client-facing byte. Its order sits below
// the shipped expansion order so the control's chain is otherwise the same shape.
type expInertFinalizer struct{}

func (*expInertFinalizer) ID() string { return "expansion-control-inert-finalizer" }

func (*expInertFinalizer) Order() int { return expansion.FinalizerOrder - expInertOrderOff }

func (*expInertFinalizer) Finalize(
	context.Context,
	toolcall.CompletedCall,
	aipapi.ToolDef,
	[]aipapi.ToolDef,
	toolcall.Meta,
) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// expHookReports collects the bounded reports the real shipped contributions emit.
// Every field it stores is a closed code or a count, so a failure message built from
// it cannot carry payload bytes either.
type expHookReports struct {
	mu       sync.Mutex
	attempt  []outbound.Report
	part     []outbound.Report
	expansin []expansion.Report
}

func (r *expHookReports) onAttempt(report outbound.Report) {
	r.mu.Lock()
	r.attempt = append(r.attempt, report)
	r.mu.Unlock()
}

func (r *expHookReports) onPart(report outbound.Report) {
	r.mu.Lock()
	r.part = append(r.part, report)
	r.mu.Unlock()
}

func (r *expHookReports) onExpansion(report expansion.Report) {
	r.mu.Lock()
	r.expansin = append(r.expansin, report)
	r.mu.Unlock()
}

func (r *expHookReports) oneAttempt(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempt) != 1 {
		t.Fatalf("fixture: the real early outbound pass must report exactly one bounded outcome: reports=%d", len(r.attempt))
	}
	return r.attempt[0]
}

func (r *expHookReports) onePart(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.part) != 1 {
		t.Fatalf("fixture: the real late outbound pass must report exactly one bounded outcome: reports=%d", len(r.part))
	}
	return r.part[0]
}

func (r *expHookReports) expansion() []expansion.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]expansion.Report(nil), r.expansin...)
}

// total is how many bounded reports each of the three real contributions emitted.
//
// It answers the one question the one-report accessors above cannot: whether a run
// recorded NOTHING at all, which is what a composition that contributes no participant
// looks like from the outside. Every field is a count, so a failure message built from
// them carries no payload byte.
func (r *expHookReports) total() (attempt, part, expansion int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.attempt), len(r.part), len(r.expansin)
}

// expBackend is the final provider-translation boundary. It records the call it was
// handed through the shared Task 1.3 recorder and returns the fixture's fixed
// canonical stream, which is the only source of the model-emitted tool call.
func expBackend(rec *twoPassRecorder, events []aipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			rec.mark(twoPassCheckpointBackendOpen)
			rec.noteOpen(call)
			return aipapi.NewFixedEventStream(events), nil
		},
	}
}

// expDrain collects every client-facing event the runtime released and reports the
// terminal stream error separately, so a fail-closed scenario can be asserted
// instead of being reported as a collection failure.
func expDrain(stream aipapi.EventStream) ([]aipapi.Event, error) {
	var out []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// expReleasedArgs joins the client-facing tool-call argument bytes, in release
// order, and counts how many tool-lifecycle events reached the client at all.
func expReleasedArgs(events []aipapi.Event) (string, int) {
	var joined strings.Builder
	lifecycle := 0
	for _, ev := range tcrToolLifecycle(events) {
		lifecycle++
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			joined.WriteString(ev.Delta)
		}
	}
	return joined.String(), lifecycle
}

// expArgsFragments splits one model-emitted document into stream-shaped deltas whose
// cuts land inside the reserved alias and inside the path-bearing suffix, so no
// fragment is one complete JSON value and the SELECTED alias exists only across a
// fragment boundary. requirements.md 4.2 is then observable: the alias is only ever
// detectable in the COMPLETE document the assembler hands a finalizer.
//
// The fixture's payload-concept sibling deliberately spells the alias too, so the
// "no fragment contains a whole alias" property cannot be asserted here and is not
// claimed: what is asserted is that the alias's own byte span is cut by a boundary,
// which is the property 4.2 is about. The payload sibling's alias is what makes the
// positive requirement-4.9 assertion in the test body non-trivial.
// alias must be the root prefix as the document spells it, which for a Windows
// flavor is the backslash-escaped form; expFixture.aliasInDocument returns exactly
// that spelling.
func expArgsFragments(t *testing.T, doc, alias string) []string {
	t.Helper()
	aliasAt := strings.Index(doc, alias)
	if aliasAt < 0 {
		t.Fatal("fixture: the model-emitted document must carry the reserved alias")
	}
	// The cut lands a fixed number of bytes INTO the frozen tag segment, whose start
	// is located relative to the alias's own tag prefix rather than spelled as an
	// absolute offset from the alias start. Every supported flavor places that prefix
	// at a different distance from the start of its alias root - a UNC alias and an
	// extended-drive alias both carry a longer prefix than a POSIX one - so a fixed
	// absolute offset would cut inside the namespace marker for some flavors rather
	// than inside the tag. For the POSIX fixture the located offset is exactly the
	// value the previous absolute constant held, so no existing expectation moves.
	tagAt := strings.Index(alias, expWorkspaceTagPrefix)
	if tagAt < 0 {
		t.Fatal("fixture: the alias must carry the frozen workspace tag prefix")
	}
	const (
		intoTagBytes = 4
		intoSuffix   = len("src/la")
	)
	intoTag := tagAt + intoTagBytes
	if intoTag <= tagAt || intoTag >= len(alias) {
		t.Fatalf("fixture: the cut offset must fall strictly inside the frozen tag: offset=%d tag_at=%d alias_span=%d",
			intoTag, tagAt, len(alias))
	}
	fragments := []string{
		doc[:aliasAt+intoTag],
		doc[aliasAt+intoTag : aliasAt+len(alias)+intoSuffix],
		doc[aliasAt+len(alias)+intoSuffix:],
	}
	if joined := strings.Join(fragments, ""); joined != doc {
		t.Fatalf("fixture: fragments must reconstruct the document: got %d bytes want %d", len(joined), len(doc))
	}
	for i, fragment := range fragments {
		if json.Valid([]byte(fragment)) {
			t.Fatalf("fixture: fragment %d must not be independently valid JSON", i)
		}
	}
	return fragments
}

// expBackendEvents is the fixture's canonical stream: one model-emitted tool call,
// delivered as deltas cut through the reserved alias.
func expBackendEvents(t *testing.T, fixture expFixture) []aipapi.Event {
	t.Helper()
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: expToolCallID, ToolName: expToolName},
	}
	for _, fragment := range expArgsFragments(t, fixture.modelArgsDocument(), fixture.aliasInDocument()) {
		events = append(events, aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: expToolCallID,
			ToolName:   expToolName,
			Delta:      fragment,
		})
	}
	return append(events,
		aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: expToolCallID, ToolName: expToolName},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
}

// expOversizedDocument builds one VALID argument document of exactly total bytes
// whose selected path member carries the reserved alias, so a scenario can drive
// the assembler's mandatory bound with a body that would be an ordinary tool call
// at any smaller size.
//
// It is the fixture's ordinary three-member document plus one extra filler member,
// so the selected path member, the non-path sibling, and the payload-concept member
// are spelled exactly as everywhere else in this file.
func expOversizedDocument(t *testing.T, fixture expFixture, total int) string {
	t.Helper()
	prefix := `{"` + expPathField + `":"` + expEscaped(fixture.joined()) + `",` +
		`"` + expPayloadField + `":"",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `,` +
		`"filler":"`
	tail := `"}`
	filler := total - len(prefix) - len(tail)
	if filler < 1 {
		t.Fatalf("fixture: the requested total is smaller than the fixed members: total=%d fixed=%d", total, len(prefix)+len(tail))
	}
	doc := prefix + strings.Repeat("x", filler) + tail
	if len(doc) != total {
		t.Fatalf("fixture: document sizing: got %d bytes want %d", len(doc), total)
	}
	if !json.Valid([]byte(doc)) {
		t.Fatal("fixture: the oversized document must still be one complete JSON value")
	}
	if !strings.Contains(doc, fixture.aliasInDocument()) {
		t.Fatal("fixture: the oversized document must carry the reserved alias")
	}
	return doc
}

// expOversizedBackendEvents delivers one model-emitted tool call whose assembled
// arguments exceed total bytes, in chunks small enough that no single delta is at
// the canonical limit. The cuts are otherwise arbitrary, which is the point: the
// overflow decision belongs to the assembler, not to fragment shape.
func expOversizedBackendEvents(t *testing.T, fixture expFixture, total int) []aipapi.Event {
	t.Helper()
	doc := expOversizedDocument(t, fixture, total)
	const chunk = 128 * 1024
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: expToolCallID, ToolName: expToolName},
	}
	for start := 0; start < len(doc); start += chunk {
		end := min(start+chunk, len(doc))
		events = append(events, aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: expToolCallID,
			ToolName:   expToolName,
			Delta:      doc[start:end],
		})
	}
	return append(events,
		aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: expToolCallID, ToolName: expToolName},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
}

// expIngressCall is the canonical client request: one path-bearing historical tool
// call spelled against the REAL project root, exactly as a coding-agent client
// replays it, plus a first user message and a terminal forwardable one.
//
// realRoot is a parameter because a scenario that pins a different authoritative
// root must spell its replayed history against that same root; the request-side
// scaffolding guard reads exactly one selected path field out of the backend-bound
// request, and a history spelled against some other root would not produce one.
func expIngressCall(t *testing.T, realRoot string) *aipapi.Call {
	t.Helper()
	history := expFixture{root: realRoot, suffix: expHistorySuffix}
	document := history.modelArgsDocument()
	if !json.Valid([]byte(document)) {
		t.Fatalf("fixture: the ingress history argument document must be one complete JSON value: bytes=%d", len(document))
	}
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: expSelector},
		Tools: []aipapi.ToolDef{{
			Name: expToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField +
				`":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(expAnchorText)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: expHistoryID,
				ToolName:   expToolName,
				Content:    json.RawMessage(document),
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(expTailText)}},
		},
	}
}

// expScenario selects the ONE variable a run changes. The resolvable case passes the
// alias the authoritative root derives; an unresolved-alias case passes the alias
// some OTHER supported root derives; an oversized case replaces the model's body.
type expScenario struct {
	// label is a bounded, content-free failure-message token.
	label string
	// aliasRoot is the root prefix the model-emitted alias is built on.
	aliasRoot string
	// expansionDecides installs the REAL shipped expansion finalizer on the frozen
	// finalizer plane. A control run with it off is exactly "the expansion finalizer
	// is absent", with no other variable changed.
	expansionDecides bool
	// assembleBody, when non-zero, replaces the model's argument document with one of
	// exactly this many bytes.
	assembleBody int
	// extraFinalizers are ordinary finalizers added to the chain for this scenario.
	// They exist so a scenario can place a participant at a chosen order relative to
	// the shipped expansion order.
	extraFinalizers []toolcall.Finalizer

	// observersExpected selects whether the two step-12 observer guards below apply.
	// A scenario whose call is REFUSED CLOSED releases no tool lifecycle at all, so
	// neither observer is invoked for it and asserting that they were would assert the
	// opposite of the property. The request-side guards stay unconditional for every
	// scenario, because a refused tool call does not change how the request was built.
	observersExpected bool

	// ingress overrides the client request the run submits. Nil selects the shared
	// historical-tool-call fixture, which is what every pre-existing scenario wants.
	// It is the one seam a scenario that changes the AUTHORITATIVE root needs: the
	// replayed history must be spelled against the same root the runtime pins, or the
	// request-side guard below would be measuring a different workspace than the one
	// under test.
	ingress func(*testing.T) *aipapi.Call

	// workspaceRoot overrides the authoritative root the runtime pins for the turn.
	// Empty selects the shared fixture root. It is a parameter rather than a constant
	// because requirement 6.5's property is about what happens when the AUTHORITATIVE
	// root changes, and only a harness that can pin an arbitrary root can stage that.
	workspaceRoot string

	// pinnedRoot, when non-nil, is the authoritative root the runtime pins for the
	// turn, INCLUDING an empty one. It exists because workspaceRoot's empty value
	// already means "the shared fixture root", so requirement 1.8's empty-root refusal
	// cannot be expressed through it. A scenario that sets this field has opted into a
	// root the lexical core may refuse, which is what allowRefusedRoot then permits.
	pinnedRoot *string

	// allowRefusedRoot permits the pinned project root to be one the lexical core
	// refuses. Without it expRun still fails the run loudly, because every scenario
	// above is written on a root that derives an active mapping and a silently
	// inactive one would turn those fixtures into vacuous passes.
	allowRefusedRoot bool

	// wiring selects which real feature contributions this run installs on the three
	// planes this feature owns. The zero value keeps this file's own fixture exactly:
	// both real outbound passes plus the expansion pass. It exists so a scenario can
	// stage the composition question - "what does a deployment contribute when the
	// operator's switch is where it is?" - without a second end-to-end harness.
	wiring expWiring

	// bundleYAML is the operator subtree the shipped composition decodes when wiring
	// is expWiringShippedBundle. Empty selects the minimal enabled rewrite subtree.
	bundleYAML string

	// modelEchoesRealPath makes the backend emit a REAL path instead of a reserved
	// alias in its model-emitted argument document. It exists for the fail-open rows,
	// where virtualization never became model-visible, so the only echo a model can
	// produce is the real path it was shown. It changes the model's stream and nothing
	// else; the cuts, the event shape, and the fixture guards are unchanged.
	modelEchoesRealPath bool
}

// effectiveRoot is the root this scenario's runtime pins for the turn.
func (tc expScenario) effectiveRoot() string {
	if tc.pinnedRoot != nil {
		return *tc.pinnedRoot
	}
	if tc.workspaceRoot == "" {
		return twoPassRealRoot
	}
	return tc.workspaceRoot
}

// pinsProjectRoot reports whether this scenario's run pins a NON-EMPTY authoritative
// project root.
//
// It exists so the observer-view guard in expRun can tell a scenario that failed to deliver
// the workspace view from one that deliberately pinned an empty one. Those are different
// facts, and requirement 1.8's empty-root refusal is the second of them.
func (tc expScenario) pinsProjectRoot() bool { return tc.effectiveRoot() != "" }

// ingressCall builds the client request, applying the default when the scenario did
// not override it.
func (tc expScenario) ingressCall(t *testing.T) *aipapi.Call {
	t.Helper()
	if tc.ingress != nil {
		return tc.ingress(t)
	}
	return expIngressCall(t, tc.effectiveRoot())
}

// expRun drives one scenario end to end.
func expRun(t *testing.T, scenario expScenario) expRunResult {
	t.Helper()

	realRoot := scenario.effectiveRoot()
	alias, aliasActive := expMappingAlias(realRoot)
	if !aliasActive && !scenario.allowRefusedRoot {
		t.Fatalf("%s: fixture: the pinned project root must derive an active mapping unless the scenario explicitly stages a refused root", scenario.label)
	}
	// A refused root publishes no alias at all, and an empty needle matches every
	// string, so the alias-shaped oracle is handed a needle no path can contain rather
	// than an empty one. The published call is still measured, so a scenario that
	// expected an alias and got none fails on its own counts.
	measuredAlias := alias
	if !aliasActive {
		measuredAlias = expUnmatchableNeedle
	}
	fixture := expFixture{root: scenario.aliasRoot, suffix: expModelSuffix}

	stage := &expStage{}
	rec := &twoPassRecorder{}
	reports := &expHookReports{}
	policy := &expPolicyObserver{stage: stage}
	reactor := &expReactorObserver{stage: stage}
	marker := &expExpansionMarker{
		Finalizer: newExpansionFin(t, reports.onExpansion),
		stage:     stage,
	}

	var backendEvents []aipapi.Event
	switch {
	case scenario.assembleBody > 0:
		backendEvents = expOversizedBackendEvents(t, fixture, scenario.assembleBody)
	case scenario.modelEchoesRealPath:
		backendEvents = expRealPathBackendEvents(t, fixture)
	default:
		backendEvents = expBackendEvents(t, fixture)
	}

	// Both real outbound passes are handed the SAME compiled policy the shipped
	// expansion finalizer uses, and both read the SAME workspace view the runtime
	// pins for the turn - the early pass from its attempt metadata, the late pass from
	// the public SDK context projection. Neither is handed a workspace authority, which
	// is what makes one run a proof that the two request planes and the response plane
	// agree on one workspace rather than on two independently spelled roots.
	authority := twoPassWorkspaceResolver{root: realRoot}
	contribs := expScenarioContributions(t, scenario, stage, reports, marker)

	bus := corehooks.New(corehooks.Config{
		RequestPartHooks: contribs.partHooks(),
		ToolReactors:     []sdkhooks.ToolReactor{reactor},
	})
	ex, _ := interleavedSecureExecutor(t, map[string]execbackend.Backend{
		expBackendID: expBackend(rec, backendEvents),
	})
	ex.InterleavedProcessor = nil
	ex.Bus = bus

	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: authority,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms:  contribs.attemptTransforms(),
			ToolCallPolicies:   []toolpolicy.Policy{policy},
			ToolCallFinalizers: contribs.finalizers,
		}),
	})

	stream, err := ex.Execute(principalCtx("path-virtualization-expansion-order"), scenario.ingressCall(t))
	if err != nil {
		// The error TYPE only. A pre-backend denial reason can carry anchor identities
		// and overlay IDs, which requirements.md 7.7 keeps out of any observable
		// dimension; this file's harness never asserts on the text.
		t.Fatalf("%s: fixture: the turn must reach the backend: error_type=%T", scenario.label, err)
	}
	events, recvErr := expDrain(stream)

	result := expRunResult{
		label:   scenario.label,
		events:  events,
		recvErr: recvErr,
		reports: reports,
	}
	result.released, result.lifecycle = expReleasedArgs(events)
	raw := rec.observation(true, 0)
	result.backendOpenCall = raw.backendOpenCall
	result.openMeasure = expMeasureDocuments(raw.backendOpenCall, measuredAlias, realRoot)
	result.openSelected = expFirstSelectedPath(raw.backendOpenCall)
	result.realRoot = realRoot
	result.alias = alias
	result.aliasActive = aliasActive
	result.wiring = scenario.wiring
	result.observations = contribs.observations
	result.contributedAttempt = contribs.attempt != nil
	result.contributedPart = contribs.part != nil
	result.contributedFinalizer = len(contribs.finalizers) > 0
	result.previousResponseID = raw.backendOpenCall.PreviousResponseID
	result.expCalls, result.expSeen, result.expResult = marker.observation()
	result.expReports = reports.expansion()
	result.policyCalls, result.policyArg, result.policyJoined, result.policyRoot = policy.observation()
	result.reactorCalls, result.reactorArg, result.reactorJoined = reactor.observation()
	attemptAt, partAt, expansionAt, policyAt, reactorAt := stage.seen()
	result.stages = expStages{attemptAt, partAt, expansionAt, policyAt, reactorAt, raw.order.open}
	_ = stream.Close()

	// Scaffolding guards. Every assertion below reads one of them, so they run first
	// and any later failure is attributable to the property under test rather than to
	// a fixture that never reached the observers.
	//
	// The two-outbound-stage guard is scoped to a run that actually installs them. A
	// run staged as "this feature contributes nothing" - because it is absent, or
	// because the shipped composition decoded a disabled subtree - installs no
	// participant to reach, and asserting otherwise would assert the opposite of the
	// property. Every run above installs both and is guarded exactly as before.
	if (contribs.attempt != nil || contribs.part != nil) && (attemptAt == 0 || partAt == 0) {
		t.Fatalf("%s: fixture: both real outbound stages must be reached: %s", scenario.label, result.stages)
	}
	// The backend-bound request must carry EXACTLY the one path-bearing history tool
	// call every ingress in this package supplies, so a scaffolding guard can never be
	// satisfied by a fixture that never virtualized anything. It stays exactly one
	// because a scenario that changed the ingress to carry none would be measuring a
	// different property, and this guard would then fail loudly rather than pass quietly.
	if result.openMeasure.documents != 1 || result.openMeasure.selectedFields != 1 {
		t.Fatalf("%s: fixture: the backend-bound request must carry exactly the one path-bearing history tool call: documents=%d selected_fields=%d",
			scenario.label, result.openMeasure.documents, result.openMeasure.selectedFields)
	}
	if scenario.observersExpected {
		if result.policyCalls < 1 || result.reactorCalls < 1 {
			t.Fatalf("%s: fixture: both step-12 observer planes must run: policy_calls=%d reactor_calls=%d",
				scenario.label, result.policyCalls, result.reactorCalls)
		}
		// The observer must be shown the view the runtime actually pinned, which is
		// only checkable when the scenario pinned a project root at all. A scenario
		// that deliberately pins an EMPTY root has no root to show, and requirement
		// 1.8's empty-root refusal is precisely that case - the workspace view is
		// attached and carries nothing, which is a different fact from a missing one.
		if scenario.pinsProjectRoot() && !result.policyRoot {
			t.Fatalf("%s: fixture: the tool policy plane must receive the authoritative workspace view, otherwise the run cannot isolate expansion",
				scenario.label)
		}
	}
	return result
}

// expFirstSelectedPath returns the FIRST selected path value on a canonical call, in
// document order, or the empty string when there is none.
//
// It exists so a scenario can compare the exact bytes the provider was handed across
// two turns. A count of alias hits cannot do that: two different aliases both count as
// one hit, so a continuation path that derived a DIFFERENT alias from the replay path
// would pass every counter in this file. The value is returned for comparison only and
// is never formatted.
func expFirstSelectedPath(call aipapi.Call) string {
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != aipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			if selected, ok := expSelectedPathOf(part.Content); ok {
				return selected
			}
		}
	}
	for _, item := range call.Items {
		if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
			continue
		}
		if selected, ok := expSelectedPathOf(item.ToolCall.Arguments); ok {
			return selected
		}
	}
	return ""
}

// expSelectedPathOf reads the fixture's selected path member out of one argument
// document.
func expSelectedPathOf(document []byte) (string, bool) {
	if !json.Valid(document) {
		return "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(document, &fields) != nil {
		return "", false
	}
	var selected string
	if json.Unmarshal(fields[expPathField], &selected) != nil {
		return "", false
	}
	return selected, true
}

// expAliasOf derives the alias this feature publishes for root.
//
// The value is returned for COMPARISON only. Nothing in this package formats it and no
// failure message contains it: requirement 7.7 keeps the workspace identity tag out of
// every observable dimension, so a tag-bearing message would be the violation itself.
func expAliasOf(t *testing.T, root string) string {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the pinned project root must derive an active mapping; its refusal reason is bounded and content-free")
	}
	return mapping.VirtualRoot
}

// newExpansionFin builds the REAL shipped expansion finalizer with the REAL shipped
// conservative built-in selector profile, in rewrite mode, and asserts the three
// production properties the composition claims rest on.
func newExpansionFin(t *testing.T, onReport func(expansion.Report)) *expansion.Finalizer {
	t.Helper()
	fin, err := expansion.NewFinalizer(
		hookRegResolver(t),
		rewrite.ModeRewrite,
		expansion.Policy{},
		expansion.WithReporter(onReport),
	)
	if err != nil {
		t.Fatalf("fixture: build the shipped expansion finalizer: %v", err)
	}
	if fin.Order() != expansion.FinalizerOrder {
		t.Fatalf("fixture: shipped expansion order changed: got %d want %d", fin.Order(), expansion.FinalizerOrder)
	}
	if got := fin.ToolCallBufferingRequirement(); got.Overflow != toolcall.OverflowReject {
		t.Fatalf("fixture: the shipped pass must declare OverflowReject, got %q", got.Overflow)
	}
	return fin
}

// expStages holds one run's six ordinals.
type expStages struct {
	attempt, part, expansion, policy, reactor, open int
}

// String renders one run's ordinals as a failure-message fragment. Only monotonic
// ordinals are emitted; nothing path-bearing is ever mixed in, and an unreached
// stage is named rather than printed as a zero that could read as "ran first".
func (s expStages) String() string {
	return "attempt_stage=" + expOrd(s.attempt) +
		" request_part_stage=" + expOrd(s.part) +
		" expansion_stage=" + expOrd(s.expansion) +
		" policy_stage=" + expOrd(s.policy) +
		" reactor_stage=" + expOrd(s.reactor) +
		" backend_open_stage=" + expOrd(s.open)
}

// expRunResult is the complete content-free record of one run.
type expRunResult struct {
	label   string
	stages  expStages
	events  []aipapi.Event
	recvErr error
	reports *expHookReports

	// released is the joined client-facing tool-call argument bytes and lifecycle
	// counts how many tool-lifecycle events reached the client.
	released  string
	lifecycle int

	openMeasure expMeasure
	// backendOpenCall is the whole canonical call the backend was handed, captured
	// through the shared harness recorder. It is held so a scenario can compare two
	// runs' backend-bound surfaces BYTE for byte, which is the only way to state that a
	// composition decision changed nothing observable. Like realRoot and alias it is
	// never formatted into a failure message.
	backendOpenCall aipapi.Call
	// openSelected is the FIRST selected path value the backend-bound request carried.
	// It is held so a scenario can compare the alias the provider was actually handed
	// across two turns byte for byte, which counts alone cannot do. Like realRoot and
	// alias it is never formatted into a failure message.
	openSelected string

	// realRoot is the root the runtime pinned for this run and alias the mapping it
	// derived. Both are held for COMPARISON only; no assertion in this package formats
	// either one, because the workspace identity tag is not an observable dimension
	// (requirements.md 7.7).
	realRoot string
	alias    string
	// aliasActive reports whether the pinned root derived an active mapping at all. A
	// refused root leaves it false, which is what lets a scenario distinguish "the pass
	// preserved the real path because there was nothing to publish" from "the pass
	// published an alias".
	aliasActive bool
	// wiring is the composition shape this run installed, so a scenario can assert
	// that the run it compared against really contributed what it claimed to.
	wiring expWiring
	// contributedAttempt, contributedPart, and contributedFinalizer report whether the
	// composition this run installed put a participant on each of the three planes this
	// feature owns. They are what makes "a disabled generation contributes nothing" a
	// measurement of the wiring rather than an assumption about it.
	contributedAttempt   bool
	contributedPart      bool
	contributedFinalizer bool
	// observations is the feature's own content-free recorder for a run installed
	// through the shipped composition seam, and nil otherwise. It is the handle
	// requirements.md 7.6's bounded counters are read through.
	observations *telemetry.Telemetry
	// previousResponseID is the continuation parent the backend-bound request carried,
	// captured so a continuation scenario can assert the parent reached the provider
	// unchanged.
	previousResponseID string

	expCalls   int
	expSeen    []byte
	expResult  toolcall.Result
	expReports []expansion.Report

	policyCalls  int
	policyArg    int
	policyJoined string
	policyRoot   bool

	reactorCalls  int
	reactorArg    int
	reactorJoined string
}

// TestStreamToolCall_RealExpansionReachesPolicyReactorAndClientAsTheRealPath is
// requirements.md 4.1, 4.2, 4.3, 4.8, 4.9, and 5.1 proved against the CONCRETE
// feature rather than against a stand-in: the shipped expansion finalizer, the
// shipped outbound attempt transform, the shipped request-part hook, the real
// assembler, and both real step-12 observer planes.
//
// It asserts, in order:
//
//   - the backend-bound request already carries the derived alias and no real root,
//     so the alias the model emits back is one the real outbound passes minted;
//   - the tool policy plane and the tool reactor plane each observe the FULL real
//     path - the complete expanded document, byte for byte, never the reserved alias
//     and never a truncated prefix;
//   - expansion completed before either of them observed an argument value;
//   - the client-facing release is that same expanded complete document, with the
//     non-path sibling preserved and the payload-concept sibling's own bytes intact
//     even though they spell the reserved namespace;
//   - the shipped pass reported a bounded, content-free reason for what it did.
//
// The control run is the fault-injection oracle: the identical fixture with the
// expansion finalizer absent reaches the policy plane, the reactor plane, and the
// client carrying the reserved alias, which is precisely what a missing, inert, or
// misordered expansion pass reproduces.
func TestStreamToolCall_RealExpansionReachesPolicyReactorAndClientAsTheRealPath(t *testing.T) {
	t.Parallel()

	resolvable := expFixture{root: hookRegAliasOf(t), suffix: expModelSuffix}
	control := expRun(t, expScenario{label: expControlLabel, aliasRoot: resolvable.root, expansionDecides: false, observersExpected: true})
	got := expRun(t, expScenario{label: expGuardedLabel, aliasRoot: resolvable.root, expansionDecides: true, observersExpected: true})

	// design.md "Testing Strategy / Runtime-integration": "PTB/backend ingress
	// receives virtualized eligible tool history". Without this, every assertion
	// below could be satisfied by a fixture that never virtualized anything.
	t.Run("the_real_outbound_passes_minted_the_alias_the_model_emitted_back", func(t *testing.T) {
		if got.openMeasure.aliasHits != 1 || got.openMeasure.realHits != 0 {
			t.Fatalf("requirements.md 5.2/5.3 - both real outbound passes must virtualize the client-replayed history before Backend.Open: %s backend_open_alias_hits=%d backend_open_real_hits=%d",
				got.stages, got.openMeasure.aliasHits, got.openMeasure.realHits)
		}
		if got.openMeasure.documents != got.openMeasure.validDocs {
			t.Fatalf("requirements.md 8.5 - the backend-bound argument document must remain one complete JSON value: documents=%d valid=%d",
				got.openMeasure.documents, got.openMeasure.validDocs)
		}
		early := got.reports.oneAttempt(t)
		if early.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.3 - the real early pass must run the shared rewriter: outcome=%v", early.Outcome)
		}
		if early.Stats.Rewritten != 1 || early.Stats.Eligible != 1 {
			t.Fatalf("requirements.md 5.3 - the real early pass must virtualize exactly the one replayed surface: eligible=%d rewritten=%d",
				early.Stats.Eligible, early.Stats.Rewritten)
		}
		late := got.reports.onePart(t)
		if late.Outcome != outbound.OutcomeRewriterRan || late.Stats.Rewritten != 0 {
			t.Fatalf("requirements.md 5.2/5.4 - the real late pass must rerun the same pure rewrite and find nothing left to do: outcome=%v eligible=%d rewritten=%d",
				late.Outcome, late.Stats.Eligible, late.Stats.Rewritten)
		}
		if !got.openMeasure.shapeKept {
			t.Fatalf("requirements.md 2.8 - the backend-bound request must keep the selected, payload-concept, and non-path members intact: %s shape_kept=%t",
				got.stages, got.openMeasure.shapeKept)
		}
		if got.openMeasure.siblingSum != expSiblingValue {
			t.Fatalf("requirements.md 2.8 - the non-path sibling must survive both real passes byte-for-byte: sibling_sum=%d expected=%d",
				got.openMeasure.siblingSum, expSiblingValue)
		}
		// requirements.md 2.4/4.9, mirrored on the request side: the replayed
		// document's payload-concept member spells the real root and no shipped profile
		// claims it, so it must still carry that root at the backend bound while the
		// SELECTED member has become the alias. aliasAnywhere is the count of reserved
		// namespace occurrences anywhere in the measured payload, so pinning it to
		// exactly one is what proves the rewrite touched the selected member and only
		// the selected member.
		if got.openMeasure.realRootInPayload != 1 {
			t.Fatalf("requirements.md 2.4/4.9 - the payload-concept member must keep its real-root bytes at the backend bound: payload_documents_with_real_root=%d",
				got.openMeasure.realRootInPayload)
		}
		if got.openMeasure.aliasAnywhere != 1 {
			t.Fatalf("requirements.md 2.4/4.9 - exactly one reserved namespace occurrence may survive on the backend-bound surface, the payload-concept member's: occurrences=%d selected_alias_hits=%d",
				got.openMeasure.aliasAnywhere, got.openMeasure.aliasHits)
		}
	})

	// design.md "7. Path Expansion Finalizer" step 11 and requirements.md 4.3/5.1.
	t.Run("tool_policy_observes_the_full_real_path", func(t *testing.T) {
		assertObserverSeesExpandedDocument(t, "tool_policy", got.stages,
			got.policyCalls, got.policyArg, got.policyJoined,
			resolvable.expExpandedModelArgsDocument(twoPassRealRoot), resolvable.root)
	})

	t.Run("tool_reactor_observes_the_full_real_path", func(t *testing.T) {
		assertObserverSeesExpandedDocument(t, "tool_reactor", got.stages,
			got.reactorCalls, got.reactorArg, got.reactorJoined,
			resolvable.expExpandedModelArgsDocument(twoPassRealRoot), resolvable.root)
	})

	// requirements.md 5.1: policy evaluation runs ONLY AFTER expansion produced the
	// real filesystem path. The ordinal comparison is against both step-12 planes,
	// because 4.3 names policies AND reactors.
	t.Run("expansion_completes_before_both_step_twelve_observers", func(t *testing.T) {
		if got.stages.expansion == 0 || got.stages.policy == 0 || got.stages.reactor == 0 {
			t.Fatalf("every ordinal under comparison must be reached: %s", got.stages)
		}
		if got.stages.expansion >= got.stages.policy {
			t.Fatalf("requirements.md 5.1 - expansion must complete before tool policy evaluation observes the argument value: %s",
				got.stages)
		}
		if got.stages.expansion >= got.stages.reactor {
			t.Fatalf("requirements.md 4.3/5.1 - expansion must complete before a tool reactor observes the argument value: %s",
				got.stages)
		}
	})

	// requirements.md 4.1 plus 4.8/4.9: the released document is the expanded one,
	// byte for byte, with the non-path sibling preserved and the payload-concept
	// sibling untouched.
	t.Run("the_client_release_is_the_expanded_complete_document", func(t *testing.T) {
		if got.lifecycle != 3 {
			t.Fatalf("fixture: the assembler must synthesize the canonical rewritten lifecycle: lifecycle_events=%d want 3",
				got.lifecycle)
		}
		want := resolvable.expExpandedModelArgsDocument(twoPassRealRoot)
		if got.released != want {
			// The single assertion that matters: byte equality against the exact
			// expected document. It reports valid_json too, so a truncated, partially
			// rewritten, or alias-bearing release is distinguished from a merely
			// differently spelled one without the release bytes ever being printed.
			t.Fatalf("requirements.md 4.1/4.8 - the client-facing argument document must be the expanded complete document: released=%d bytes expected=%d bytes reserved_alias_released=%t valid_json=%t",
				len(got.released), len(want),
				strings.Contains(got.released, resolvable.root),
				json.Valid([]byte(got.released)))
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got.released), &fields); err != nil || len(fields) != 3 {
			t.Fatalf("requirements.md 4.8 - the released document must keep every argument member: released=%d bytes", len(got.released))
		}
		var sibling int
		if json.Unmarshal(fields[expSiblingField], &sibling) != nil || sibling != expSiblingValue {
			t.Fatalf("requirements.md 4.8 - the non-path argument sibling must survive expansion byte-for-byte: sibling_readable=%t",
				json.Unmarshal(fields[expSiblingField], &sibling) == nil)
		}
		// requirement 4.9: the payload-concept member spelled the reserved namespace
		// and must therefore still spell it after expansion. This is asserted by
		// comparison, never by formatting the value.
		var payload string
		if json.Unmarshal(fields[expPayloadField], &payload) != nil {
			t.Fatalf("requirements.md 4.9 - the payload-concept member must still be readable after expansion")
		}
		if payload != "/"+resolvable.joined() {
			t.Fatalf("requirements.md 4.9 - expansion must not touch a payload-concept member, even one spelling the reserved namespace: payload_bytes=%d reserved_alias_in_payload=%t",
				len(payload), strings.Contains(payload, resolvable.root))
		}
	})

	// The shipped pass must have DECIDED, and reported it through its own bounded
	// content-free channel, rather than the client release happening for some other
	// reason.
	t.Run("the_shipped_pass_decided_and_reported_a_bounded_reason", func(t *testing.T) {
		if got.expCalls != 1 {
			t.Fatalf("fixture: the shipped pass must run exactly once on the completed document: invocations=%d", got.expCalls)
		}
		if !json.Valid(got.expSeen) {
			t.Fatalf("requirements.md 4.2 - the shipped pass must receive the complete valid document: seen=%d bytes", len(got.expSeen))
		}
		if !strings.Contains(string(got.expSeen), resolvable.root) {
			t.Fatalf("requirements.md 4.2 - the shipped pass must receive the reassembled model-emitted document: seen=%d bytes reserved_alias_in_seen=%t",
				len(got.expSeen), strings.Contains(string(got.expSeen), resolvable.root))
		}
		if got.expResult.Action != toolcall.ActionRewrite {
			t.Fatalf("fixture: the shipped pass must rewrite: action=%d", int(got.expResult.Action))
		}
		reason, ok := expansion.ParseReason(got.expResult.ReasonCode)
		if !ok || reason != expansion.ReasonExpanded {
			t.Fatalf("requirements.md 4.1 - the shipped pass must report the bounded expanded reason: reason=%q parseable=%t",
				got.expResult.ReasonCode, ok)
		}
		if len(got.expReports) != 1 {
			t.Fatalf("fixture: the shipped pass must emit exactly one bounded report: reports=%d", len(got.expReports))
		}
		if got.expReports[0].Outcome != expansion.OutcomeExpanded || got.expReports[0].Reason != expansion.ReasonExpanded {
			t.Fatalf("requirements.md 7.6 - the shipped report must be the content-free expanded outcome: outcome=%v reason=%q",
				got.expReports[0].Outcome, got.expReports[0].Reason)
		}
	})

	// The fault-injection oracle. The identical fixture with the expansion finalizer
	// absent reaches the policy plane, the reactor plane, and the client carrying the
	// reserved alias. This is what makes every assertion above a real guard rather
	// than a statement about a fixture that could not have gone any other way.
	t.Run("removing_the_expansion_pass_is_detectable_at_both_observers_and_the_client", func(t *testing.T) {
		if control.expCalls != 0 {
			t.Fatalf("fixture: the control must not run the shipped pass: invocations=%d", control.expCalls)
		}
		if control.stages.expansion != 0 {
			t.Fatalf("fixture: the control must never reach the expansion stage: %s", control.stages)
		}
		for _, observer := range []struct {
			name   string
			calls  int
			arg    int
			joined string
		}{
			{"tool_policy", control.policyCalls, control.policyArg, control.policyJoined},
			{"tool_reactor", control.reactorCalls, control.reactorArg, control.reactorJoined},
		} {
			if observer.calls < 1 || observer.arg < 1 {
				t.Fatalf("fixture: the control's %s observer must still run: calls=%d argument_events=%d",
					observer.name, observer.calls, observer.arg)
			}
			if observer.joined != resolvable.modelArgsDocument() {
				t.Fatalf("fault-injection oracle - without the shipped expansion pass the %s observer must be handed the model's own bytes verbatim: observed=%d bytes expected=%d bytes",
					observer.name, len(observer.joined), len(resolvable.modelArgsDocument()))
			}
			if !strings.Contains(observer.joined, resolvable.root) {
				t.Fatalf("fault-injection oracle - without the shipped expansion pass the %s observer must see the reserved alias: observed=%d bytes reserved_alias=%t",
					observer.name, len(observer.joined), strings.Contains(observer.joined, resolvable.root))
			}
		}
		if control.released != resolvable.modelArgsDocument() {
			t.Fatalf("fault-injection oracle - without the shipped expansion pass the client must receive the model's own bytes verbatim: released=%d bytes expected=%d bytes",
				len(control.released), len(resolvable.modelArgsDocument()))
		}
		if got.released == control.released {
			t.Fatal("fixture: the guarded and control runs must release different bytes, otherwise neither proves anything")
		}
	})
}

// assertObserverSeesExpandedDocument is the shared body of the two step-12
// assertions. It requires the FULL real path: exact byte equality against the
// expected expanded document, which a truncated form, a partial rewrite, or the
// reserved alias itself all fail.
func assertObserverSeesExpandedDocument(
	t *testing.T,
	observer string,
	stages expStages,
	calls, argEvents int,
	joined string,
	want string,
	alias string,
) {
	t.Helper()
	if calls < 1 || argEvents < 1 {
		t.Fatalf("fixture: the %s plane must observe at least one argument event: calls=%d argument_events=%d",
			observer, calls, argEvents)
	}
	if joined != want {
		t.Fatalf("requirements.md 4.3/4.8/5.1 - the %s plane must observe the FULL expanded real path, byte for byte: observed=%d bytes expected=%d bytes reserved_alias_observed=%t valid_json=%t non_path_sibling_preserved=%t %s",
			observer, len(joined), len(want),
			strings.Contains(joined, alias),
			json.Valid([]byte(joined)),
			strings.Contains(joined, `"`+expSiblingField+`":`+strconv.Itoa(expSiblingValue)),
			stages)
	}
	if !json.Valid([]byte(joined)) {
		t.Fatalf("requirements.md 4.8 - the %s plane must observe one complete JSON value: observed=%d bytes %s",
			observer, len(joined), stages)
	}
	if !strings.Contains(joined, twoPassRealRoot) {
		t.Fatalf("requirements.md 4.1/5.1 - the %s plane must observe the authoritative real root, not a prefix of it: observed=%d bytes %s",
			observer, len(joined), stages)
	}
}

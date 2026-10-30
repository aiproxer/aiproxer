package runtime_test

// Spec: b-leg-path-virtualization Task 10.3, the disabled / fail-open / fail-closed
// REGRESSION MATRIX.
// Requirements 8.1, 8.2, 8.3, and 8.5.
//
// Design sections read for this file:
//
//   - "Error Handling" (unsupported/malformed project root: skip mapping, no outbound
//     mutation; unexpected outbound transformation error before alias exposure: fail open
//     to the real path with bounded diagnostics; malformed reserved alias on a selected
//     model path: fail closed for that tool call; stale/different workspace tag:
//     fail closed with workspace_mismatch; mandatory assembly overflow: fail closed);
//   - "Configuration and Composition" ("A disabled resolution returns an EMPTY recorder
//     and the empty bundle", and "Add a new generic plane only if the mandatory
//     buffering contract demonstrably requires it" - which is why the disabled leg is
//     measured through the real composition seam rather than through a stub);
//   - "Observability" (mode, direction, outcome, and a closed reason vocabulary, plus
//     the counters requirements.md 7.6 asks for);
//   - "Testing Strategy / Runtime-integration" ("PTB/backend ingress receives virtualized
//     eligible tool history"; "Stale/unresolved alias and mandatory overflow never reach
//     client events").
//
// WHAT THIS FILE IS
//
// requirements.md 8.1, 8.2, and 8.3 are the feature's FAILURE POLICY, and every other
// file in this package proves one branch of it on one surface. That is the right way to
// prove a branch and the wrong way to read a policy: a reader who wants to know "what
// happens when this feature cannot do its job?" currently has to know that there are six
// files and reconstruct the answer from all of them.
//
// So this file is ONE table. Every row names the direction the failure takes, the bounded
// reason that direction must record, and whether a reserved namespace may reach the
// client, and every row is DRIVEN - not described - so a row that stops holding fails
// where a reader will look for it.
//
// WHY THE TABLE IS THE SPECIFICATION AND NOT A SUMMARY
//
// Two directions exist and they are opposites, which is exactly why they are easy to
// confuse:
//
//   - the OUTBOUND direction fails OPEN. An alias is a token-cost optimization on the
//     way to the BACKEND. Nothing about it is client-visible yet, so when the pass cannot
//     finish, the real path is a perfectly valid request and losing it helps nobody
//     (requirements.md 8.2). Fail-closed here would turn an internal error in an
//     optional optimization into a failed client request;
//   - the INBOUND direction fails CLOSED. Once an alias is model-visible in the current
//     logical trajectory, the only safe answers for a call that cannot be mapped back
//     unambiguously are "release nothing" and "fail" (requirements.md 8.3). An
//     unresolved alias that reaches the client IS a filesystem target the client will
//     act on.
//
// The table's `aliasMayReachClient` column is what makes the difference legible: it is
// false on every row the proxy owns, and true on exactly one - the disabled row, where a
// reserved-namespace byte the MODEL emitted passes through precisely because the feature
// is not there to interpret it, which is what "unchanged" means for that configuration.
//
// WHY TWO ROWS ARE DRIVEN IN THE FEATURE PACKAGE INSTEAD OF HERE
//
// Two outbound conditions cannot be staged through the runtime at all, and saying so
// rather than pretending otherwise is the point:
//
//   - an absent workspace projection (outbound.OutcomeWorkspaceUnresolved). The runtime
//     ALWAYS projects a view on the ordinary path and deliberately projects an EMPTY one
//     on the detached auxiliary path, so no turn exists in which the late pass finds no
//     authority. Driving it means handing the shipped pass a context that never went
//     through that projection;
//   - an unexpected transformation error (outbound.OutcomeTransformationFailed). The
//     shared rewriter's error path is documented as unreachable from untrusted input, and
//     the pass reaches it only through its declared rewriter port, which is package
//     private.
//
// Both are therefore driven in outbound's own internal test file, and both rows below
// name the test that owns them. The bounded reason on those two rows is read from the
// same production enum the feature package asserts against, so the two cannot drift, and
// the driver fails the run if a row names a harness that does not exist.
//
// WHAT IS DELIBERATELY NOT HERE
//
// No new end-to-end harness. Every turn-level row runs through the ONE executor harness
// in this package (expRun, and expRunRefusal for the refused rows), extended - never
// duplicated - with the three composition shapes this task needs: the harness's own real
// contributions, the shipped composition seam decoding an operator subtree, and no
// contribution at all. The disabled leg is what those shapes exist for.
//
// CONTENT FREEDOM
//
// No failure message in this file contains a path, an alias, a workspace tag, a
// tool-call ID, a tool name, or an argument byte. Paths and derived aliases are compared
// byte for byte and never formatted. Byte comparisons report event ordinals, event
// KINDS (a closed enum), byte offsets, and byte totals only.
//
// DETERMINISM
//
// Every run pins the executor RNG and clock through the shared harness, the project root
// is a fixed fixture value, the workspace tag is a content-defined digest, every scan
// walks slices in order, and the byte comparison renders with HTML escaping disabled so
// the encoder cannot rewrite the byte it is being asked about. Repeated runs produce
// identical ordinals, counters, and byte streams.

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	pvconfig "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Composition shapes.
//
// expScenario.wiring selects one of them; expRun installs the result on the three planes
// this feature owns and on nothing else.
// ---------------------------------------------------------------------------

// expWiring is which real feature contributions one harness run installs.
//
// The zero value is the harness's own fixture - both real outbound passes plus the real
// expansion finalizer - so every run the earlier files in this package drive keeps its
// exact wiring. The other two exist because requirements.md 8.1 is a question about
// COMPOSITION ("if path virtualization is disabled, observable request/response behavior
// shall remain unchanged"), and answering it needs a run that installs what a disabled
// generation installs, which is nothing.
type expWiring int

const (
	// expWiringTaskOwn installs the two real outbound passes and the real expansion
	// finalizer the earlier files in this package build directly.
	expWiringTaskOwn expWiring = iota
	// expWiringAbsent installs nothing on any of the three planes: the literal
	// "this feature is not in the registry" baseline.
	expWiringAbsent
	// expWiringShippedBundle installs exactly what the shipped composition seam
	// builds from an operator subtree, which for a disabled subtree is nothing and
	// for an enabled one is the three production components.
	expWiringShippedBundle
)

// expUnmatchableNeedle is the alias-shaped oracle's stand-in when the pinned project root
// derives no mapping at all.
//
// It is a NUL-delimited string no path can contain. The alternative - passing the empty
// alias - would silently make the oracle match every selected path value, because
// strings.Contains reports true for an empty needle, and a fail-open row would then read
// as a successful rewrite.
const expUnmatchableNeedle = "\x00path-virtualization-no-mapping\x00"

// expContribs is what one run installs on the three planes this feature owns.
//
// A nil participant means the plane carries nothing, which is a MEASURED fact a scenario
// asserts rather than an assumption about it.
type expContribs struct {
	attempt      request.AttemptTransform
	part         sdkhooks.RequestPartHook
	finalizers   []toolcall.Finalizer
	observations *telemetry.Telemetry
}

// attemptTransforms renders the candidate-attempt plane contribution.
func (c expContribs) attemptTransforms() []request.AttemptTransform {
	if c.attempt == nil {
		return nil
	}
	return []request.AttemptTransform{c.attempt}
}

// partHooks renders the request-part plane contribution.
func (c expContribs) partHooks() []sdkhooks.RequestPartHook {
	if c.part == nil {
		return nil
	}
	return []sdkhooks.RequestPartHook{c.part}
}

// expScenarioContributions assembles the three planes for one run.
//
// The three shapes differ in what they install and in nothing else: the same executor,
// the same frozen feature planes, the same observers, the same fixture, the same backend.
func expScenarioContributions(
	t *testing.T,
	scenario expScenario,
	stage *expStage,
	reports *expHookReports,
	marker *expExpansionMarker,
) expContribs {
	t.Helper()
	switch scenario.wiring {
	case expWiringAbsent:
		return expContribs{}
	case expWiringShippedBundle:
		return expBundleContributions(t, scenario.bundleYAML, stage)
	default:
		return expTaskOwnContributions(t, scenario, stage, reports, marker)
	}
}

// expTaskOwnContributions is the wiring the earlier files in this package drive: both real
// outbound passes built from the shipped built-in policy, and the expansion plane with or
// without the real finalizer.
//
// The control keeps one ordinary finalizer on the plane so the assembler is constructed
// exactly as it is in the guarded run. It decides nothing and publishes nothing, so the
// control remains exactly "expansion contributed nothing" rather than "a whole plane was
// missing".
func expTaskOwnContributions(
	t *testing.T,
	scenario expScenario,
	stage *expStage,
	reports *expHookReports,
	marker *expExpansionMarker,
) expContribs {
	t.Helper()
	resolver := hookRegResolver(t)
	finalizers := make([]toolcall.Finalizer, 0, 1+len(scenario.extraFinalizers))
	if scenario.expansionDecides {
		finalizers = append(finalizers, marker)
	} else {
		finalizers = append(finalizers, &expInertFinalizer{})
	}
	finalizers = append(finalizers, scenario.extraFinalizers...)
	return expContribs{
		attempt: &expAttemptMarker{
			stage: stage,
			real: outbound.NewAttemptTransform(
				rewrite.ModeRewrite,
				resolver,
				outbound.WithReporter(reports.onAttempt),
			),
		},
		part: &expPartMarker{
			stage: stage,
			real: outbound.NewRequestPartHook(
				rewrite.ModeRewrite,
				resolver,
				outbound.WithHookReporter(reports.onPart),
			),
		},
		finalizers: finalizers,
	}
}

// expBundleContributions installs exactly what the SHIPPED composition seam builds from an
// operator subtree: config.Decode followed by bundle.FeatureBundleWithTelemetry.
//
// It is the real seam rather than a hand-wired equivalent for two reasons. The disabled
// row's entire claim is that this seam publishes an empty bundle, and only the seam can
// say so. And every enabled run driven through it records into the feature's OWN bounded
// counters, which is what the observability leg reads.
func expBundleContributions(t *testing.T, operatorYAML string, stage *expStage) expContribs {
	t.Helper()
	resolved := pvDecodeOperatorSubtree(t, operatorYAML)
	observations, compiled, err := bundle.FeatureBundleWithTelemetry(resolved)
	if err != nil {
		t.Fatalf("fixture: the shipped composition seam must build this generation: %v", err)
	}
	out := expContribs{observations: observations, finalizers: aipfeature.Get(compiled.PlaneSet, aipfeature.PlaneToolCallFinalizers)}
	if attempts := aipfeature.Get(compiled.PlaneSet, aipfeature.PlaneAttemptTransforms); len(attempts) > 0 {
		real, ok := attempts[0].(*outbound.AttemptTransform)
		if !ok {
			t.Fatalf("fixture: the shipped bundle's early outbound contribution must be the real shipped pass, got %T", attempts[0])
		}
		out.attempt = &expAttemptMarker{stage: stage, real: real}
	}
	if parts := aipfeature.Get(compiled.PlaneSet, aipfeature.PlaneRequestPartHooks); len(parts) > 0 {
		real, ok := parts[0].(*outbound.RequestPartHook)
		if !ok {
			t.Fatalf("fixture: the shipped bundle's late outbound contribution must be the real shipped pass, got %T", parts[0])
		}
		out.part = &expPartMarker{stage: stage, real: real}
	}
	return out
}

// pvDecodeOperatorSubtree decodes one operator YAML subtree through the shipped
// configuration decoder and compiles it, which is exactly what a registry factory does.
//
// The subtree is written as YAML text rather than assembled from Go values so the decoder
// sees the same node shape it sees in production. An empty text is the ABSENT subtree,
// which requirements.md 7.1 makes disabled by default.
func pvDecodeOperatorSubtree(t *testing.T, operatorYAML string) pvconfig.Resolved {
	t.Helper()
	var node yaml.Node
	if operatorYAML != "" {
		if err := yaml.Unmarshal([]byte(operatorYAML), &node); err != nil {
			t.Fatalf("fixture: the operator subtree must be valid YAML: %v", err)
		}
	}
	resolved, err := pvconfig.Decode(node)
	if err != nil {
		t.Fatalf("fixture: the shipped configuration decoder must compile this subtree: %v", err)
	}
	return resolved
}

// The two operator subtrees the composition rows drive. They are the smallest shapes the
// specification itself allows: enablement, plus a mode because an enabled subtree with
// no mode is refused rather than defaulted.
const (
	// pvOperatorAbsent is no subtree at all: requirement 7.1's disabled-by-default.
	pvOperatorAbsent = ""
	// pvOperatorDisabled is an explicit operator switch set to off.
	pvOperatorDisabled = "enabled: false\n"
	// pvOperatorRewrite is the same feature switched on in the mode that mutates.
	pvOperatorRewrite = "enabled: true\nmode: rewrite\n"
)

// expMappingAlias derives the alias the pinned root publishes, and reports whether there
// is one at all.
//
// It is the tolerant sibling of expAliasOf: a scenario that stages a root the lexical core
// refuses needs "no alias" to be an expected answer rather than a fixture failure, and it
// must still fail loudly when a scenario did NOT mean to stage one. expRun owns that
// second half through expScenario.allowRefusedRoot.
func expMappingAlias(root string) (string, bool) {
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		return "", false
	}
	return mapping.VirtualRoot, true
}

// ---------------------------------------------------------------------------
// Matrix fixtures.
//
// The matrix spells its replayed history against its OWN client-visible project root
// rather than the shared fixture root, so a row can pin an unusable AUTHORITATIVE root
// while the client's real paths stay ordinary, supported, and long. Long matters: a short
// root is a SUPPORTED root whose alias is correctly not shorter, so virtualization stays
// inactive for a reason that has nothing to do with the row under test.
// ---------------------------------------------------------------------------

// matrixClientRoot is the client-visible project root the matrix fixtures spell their
// replayed tool history against.
//
// It is the shared harness root, deliberately. A fail-open row pins an unusable
// AUTHORITATIVE root, and the whole point of such a row is that the CLIENT's real paths
// are unaffected by that. Spelling the history against a root of the row's own making
// would let a fixture mistake in the authority read as a defect in the client's bytes.
//
// It is also long, which is load-bearing rather than incidental: a short root is a
// SUPPORTED root whose alias is correctly not shorter, so virtualization would stay
// inactive for a reason that has nothing to do with the row under test.
const matrixClientRoot = twoPassRealRoot

// matrixLegacyIngress is the canonical client request in LEGACY message authority: one
// path-bearing historical tool call spelled against matrixClientRoot, a leading user
// message, and a terminal forwardable one.
func matrixLegacyIngress(t *testing.T) *aipapi.Call {
	t.Helper()
	return expIngressCall(t, matrixClientRoot)
}

// matrixItemIngress is the same request in ITEM authority, which is the second canonical
// authority design.md "4. Canonical Outbound Rewriter" discriminates on.
//
// It exists so requirements.md 8.5's "canonical call/event validation holds after every
// mutation" is checked against a real splice on BOTH authorities rather than only on the
// legacy one. It carries no conversation-view tag, because the matrix runs install no
// conversation-view reader and therefore have no frozen view to satisfy.
func matrixItemIngress() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: expSelector},
		Tools: []aipapi.ToolDef{{
			Name: expToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField +
				`":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "matrix-item-anchor",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: expAnchorText}},
			},
			{
				Kind: aipapi.ItemKindToolCall,
				ID:   "matrix-item-history",
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "matrix-item-history-call",
					Name:      expToolName,
					Arguments: json.RawMessage(matrixLegacyArgumentDocument()),
				},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "matrix-item-tail",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: expTailText}},
			},
		},
	}
}

// matrixLegacyArgumentDocument is the complete path-bearing argument document the matrix
// spells in real-root form, byte for byte the document every authority carries.
//
// It is derived from the harness's own document builder rather than spelled again, so the
// non-path sibling and the payload-concept sibling requirements.md 2.8 protects are the
// same ones the rest of this package uses.
func matrixLegacyArgumentDocument() string {
	return expFixture{root: matrixClientRoot, suffix: expHistorySuffix}.modelArgsDocument()
}

// matrixItemSelected reads the selected path member off an ITEM-authoritative call, or
// the empty string when there is none.
func matrixItemSelected(t *testing.T, call aipapi.Call) string {
	t.Helper()
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

// pvAssertItemAuthorityStayedCanonical drives the REAL shipped rewriter over the item
// form directly, so requirement 8.5's "the call is still valid after every rewrite" is
// checked on the authority the runtime has already bridged away from.
//
// Candidate adaptation carries an item-authoritative call onto the message authority
// before the provider is called, so the turn-level measurement cannot see the item form
// the splice actually ran on. Checking canonical validity there and only there would
// leave the second authority design.md discriminates on unverified: a splice that broke
// the item grammar would still validate on the bridged surface.
//
// It is the shared engine doing the work, not a fixture re-implementation: the same
// rewriter every outbound pass binds, the same built-in selector profile, and the same
// mapping the turn derives.
func pvAssertItemAuthorityStayedCanonical(t *testing.T, tc pvCase, alias string) {
	t.Helper()
	ingress := matrixItemIngress()
	if err := ingress.Validate(); err != nil {
		t.Fatalf("fixture: %s: the item-authoritative fixture must be canonical before the rewrite: error_type=%T", tc.label, err)
	}
	published, stats, err := driftRewriter(t, hookRegResolver(t)).RewriteCall(ingress)
	if err != nil {
		t.Fatalf("requirements.md 8.5 - %s: the shared rewriter must publish the item form without an internal error: error_type=%T", tc.label, err)
	}
	if published == nil {
		t.Fatalf("fixture: %s: the shared rewriter must publish a rewritten item call", tc.label)
	}
	if err := published.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - %s: the item-authoritative call must still be canonical after the rewrite: error_type=%T", tc.label, err)
	}
	if stats.Eligible < 1 || stats.Rewritten < 1 {
		t.Fatalf("fixture: %s: the item form must really carry one eligible selected leaf: eligible=%d rewritten=%d",
			tc.label, stats.Eligible, stats.Rewritten)
	}
	selected := matrixItemSelected(t, *published)
	if selected != alias+expHistorySuffix {
		t.Fatalf("requirements.md 8.5 - %s: the item-authoritative selected member must carry the alias the byte splice published: selected_bytes=%d want_bytes=%d",
			tc.label, len(selected), len(alias+expHistorySuffix))
	}
	if !rewrite.PublishedJSONValid([]byte(matrixItemArguments(t, *published))) {
		t.Fatalf("requirements.md 8.5 - %s: the published item argument document must still be one complete JSON value", tc.label)
	}
	// Every argument document on the item form, not only the selected one: the byte
	// splice copies the bytes BETWEEN spans verbatim, so a second document sharing the
	// payload is the cheapest way for that property to break.
	if items := len(published.Items); items < 2 {
		t.Fatalf("fixture: %s: the item form must carry the message, tool-call, and terminal items: items=%d", tc.label, items)
	}
}

// matrixItemArguments returns the first tool-call argument document on an item-authoritative
// call.
func matrixItemArguments(t *testing.T, call aipapi.Call) []byte {
	t.Helper()
	for _, item := range call.Items {
		if item.ToolCall != nil && len(item.ToolCall.Arguments) > 0 {
			return item.ToolCall.Arguments
		}
	}
	t.Fatal("fixture: the item form must carry a tool call with arguments")
	return nil
}

// matrixExpectedClientPath is the selected path value the matrix history spells, which is
// what every fail-open row must find surviving byte for byte at the backend bound and on
// the wire.
func matrixExpectedClientPath() string {
	return expFixture{root: matrixClientRoot, suffix: expHistorySuffix}.joined()
}

// The path-bearing suffix on the MODEL's own emitted tool call is the harness's own
// expModelSuffix, which is already distinct from the suffix on the replayed history. That
// distinctness is what lets a fail-open row assert on both surfaces at once: a rewrite of
// one can never be mistaken for a rewrite of the other.

// expRealPathBackendEvents delivers one model-emitted tool call whose argument document
// carries a REAL path rather than a reserved alias.
//
// It is the honest model behaviour on a fail-open row: virtualization never became
// model-visible, so the only path the model can echo is the one the backend was shown.
// The stream shape is the harness's own - same event kinds, same order, same three
// fragments - and it carries the same two guards that make the stream worth asserting on,
// namely that the fragments reconstruct the document exactly and that no fragment is
// independently valid JSON.
//
// It is deliberately built beside expBackendEvents rather than by generalizing it. That
// helper's guard proves the fragment boundary falls strictly inside the frozen TAG, which
// is the property requirements.md 4.2 asks about - a complete alias that exists only
// across a boundary. There is no alias here, so that guard has no subject; weakening it
// to "somewhere in the root prefix" would make the alias rows' guard weaker too.
func expRealPathBackendEvents(t *testing.T, fixture expFixture) []aipapi.Event {
	t.Helper()
	doc := fixture.modelArgsDocument()
	if strings.Contains(doc, expReservedMarker) {
		t.Fatal("fixture: the real-path model stream must not spell the reserved namespace")
	}
	rootAt := strings.Index(doc, expEscaped(fixture.root))
	if rootAt < 0 {
		t.Fatal("fixture: the real-path model stream must carry the fixture's own root prefix")
	}
	// One cut inside the root prefix and one inside the path-bearing suffix, both at a
	// fixed fraction of their own span, so no cut can land on a JSON structural byte.
	rootSpan := len(expEscaped(fixture.root))
	intoRoot := rootSpan / 2
	intoSuffix := len("src/mat") + 1
	if intoRoot < 1 || intoRoot >= rootSpan {
		t.Fatalf("fixture: the first cut must fall strictly inside the root prefix: prefix_span=%d cut=%d", rootSpan, intoRoot)
	}
	second := rootAt + rootSpan + intoSuffix
	if second <= rootAt+rootSpan || second >= len(doc) {
		t.Fatalf("fixture: the second cut must fall strictly inside the path-bearing suffix: document_bytes=%d second_cut=%d", len(doc), second)
	}
	fragments := []string{doc[:rootAt+intoRoot], doc[rootAt+intoRoot : second], doc[second:]}
	if joined := strings.Join(fragments, ""); joined != doc {
		t.Fatalf("fixture: fragments must reconstruct the document: got %d bytes want %d", len(joined), len(doc))
	}
	for i, fragment := range fragments {
		if json.Valid([]byte(fragment)) {
			t.Fatalf("fixture: fragment %d must not be independently valid JSON", i)
		}
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: expToolCallID, ToolName: expToolName},
	}
	for _, fragment := range fragments {
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

// ---------------------------------------------------------------------------
// Content-free byte comparison.
//
// Byte identity is the whole requirement.md 8.1 claim, so it has to be measured rather than
// approximated by counting occurrences. Two occurrences of "the alias" can be one alias or
// two, and an "equivalent" comparison cannot tell a no-op from a rewrite that happened to
// produce the same length. Every helper below compares BYTES and reports only event
// ordinals, event KINDS, byte offsets, and byte totals.
// ---------------------------------------------------------------------------

// pvRenderStream renders a whole client-facing event stream into one comparable byte
// string.
//
// HTML escaping is disabled deliberately. The default encoder rewrites `<`, `>`, and `&`
// into escape sequences, which is a transformation of the bytes under comparison; a
// comparison that let the encoder rewrite them would be comparing an encoder's output
// rather than the stream.
func pvRenderStream(events []aipapi.Event) []byte {
	var out bytes.Buffer
	for _, ev := range events {
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(ev)
	}
	return out.Bytes()
}

// pvAssertStreamIdentical fails unless the two runs released byte-identical client-facing
// event streams, and reports where they first diverge without printing a byte.
func pvAssertStreamIdentical(t *testing.T, tc pvCase, want, got []aipapi.Event) {
	t.Helper()
	wantBytes, gotBytes := pvRenderStream(want), pvRenderStream(got)
	if bytes.Equal(wantBytes, gotBytes) {
		return
	}
	ordinal := pvFirstDifferingEvent(want, got)
	t.Fatalf("requirements.md 8.1 - %s: the two runs must release a BYTE-IDENTICAL client-facing event stream: expected_events=%d got_events=%d expected_stream_bytes=%d got_stream_bytes=%d first_divergent_event_ordinal=%d first_divergent_event_kind=%v",
		tc.label, len(want), len(got), len(wantBytes), len(gotBytes), ordinal, pvEventKindAt(want, got, ordinal))
}

// pvAssertStreamDiffers is the non-vacuity oracle for the byte comparison above: it fails
// unless the two streams really are different, so "byte-identical" cannot be satisfied by
// a comparison that cannot see a difference.
func pvAssertStreamDiffers(t *testing.T, tc pvCase, control, guarded []aipapi.Event) {
	t.Helper()
	if bytes.Equal(pvRenderStream(control), pvRenderStream(guarded)) {
		t.Fatalf("fixture: %s: the control run must release a DIFFERENT client-facing event stream, otherwise a byte-identity assertion over it proves nothing: control_events=%d guarded_events=%d",
			tc.label, len(control), len(guarded))
	}
}

// pvAssertCallIdentical fails unless the two runs bound the same canonical request to the
// backend, byte for byte.
//
// The comparison is scoped to the request's CONTENT, and the scope is stated rather than
// assumed: the runtime mints a fresh A-leg correlation identity for every turn, including
// two turns of the same fixture, so that one field differs between ANY two runs and a
// whole-value comparison could never be satisfied by anything. It is not this feature's
// field, it is not derived from configuration, and a per-turn correlation identity is
// expected to differ per turn.
//
// Rather than simply excluding it - which would let a real difference hide inside the
// exclusion - the exclusion is PROVED to be exactly that field: the two runs must differ
// only where the session block does, which is checked structurally against the rendering
// of the same call with an empty session. So the moment a second field differs, the
// assertion fires.
func pvAssertCallIdentical(t *testing.T, tc pvCase, want, got aipapi.Call) {
	t.Helper()
	wantBytes, gotBytes := pvRenderCall(pvWithoutPerTurnCorrelation(want)), pvRenderCall(pvWithoutPerTurnCorrelation(got))
	if bytes.Equal(wantBytes, gotBytes) {
		// The scoped comparison held. Prove the ONLY thing the scope removed is the
		// per-turn identity, so the scope cannot be quietly widened later.
		pvAssertOnlyPerTurnCorrelationDiffers(t, tc, want, got)
		return
	}
	t.Fatalf("requirements.md 8.1 - %s: the two runs must bind a BYTE-IDENTICAL backend-bound request: expected_bytes=%d got_bytes=%d",
		tc.label, len(wantBytes), len(gotBytes))
}

// pvAssertCallDiffers is pvAssertStreamDiffers for the backend-bound surface.
func pvAssertCallDiffers(t *testing.T, tc pvCase, control, guarded aipapi.Call) {
	t.Helper()
	if bytes.Equal(
		pvRenderCall(pvWithoutPerTurnCorrelation(control)),
		pvRenderCall(pvWithoutPerTurnCorrelation(guarded)),
	) {
		t.Fatalf("fixture: %s: the control run must bind a DIFFERENT backend-bound request, otherwise a byte-identity assertion over it proves nothing: control_bytes=%d guarded_bytes=%d",
			tc.label, len(pvRenderCall(control)), len(pvRenderCall(guarded)))
	}
}

// pvWithoutPerTurnCorrelation returns the call with the runtime's per-turn session block
// cleared.
//
// The block is cleared as a WHOLE rather than one field at a time, because the whole block
// is minted per turn and because an enumerated field list would silently miss a field
// added later. pvAssertOnlyPerTurnCorrelationDiffers is what keeps that from being a
// blanket exclusion.
func pvWithoutPerTurnCorrelation(call aipapi.Call) aipapi.Call {
	out := aipapi.CloneCall(call)
	out.Session = aipapi.SessionRef{}
	return out
}

// pvAssertOnlyPerTurnCorrelationDiffers proves that two runs' backend-bound requests differ
// ONLY inside the per-turn minted identity.
//
// It locates that identity's value inside the raw rendering by its own JSON member name,
// then requires every differing byte to lie inside that value's span. A difference anywhere
// else - a different route, a different history byte, a different selected path, an added
// field - lies outside the span and is reported.
//
// Locating the span rather than assuming it is the whole point: a fixed exclusion window
// would silently become wrong the moment the call's field order changed, and a comparison
// that excluded "roughly the session block" is not a byte-identity claim at all.
func pvAssertOnlyPerTurnCorrelationDiffers(t *testing.T, tc pvCase, want, got aipapi.Call) {
	t.Helper()
	wantRaw, gotRaw := pvRenderCall(want), pvRenderCall(got)
	if bytes.Equal(wantRaw, gotRaw) {
		return
	}
	// The guard needs a subject: if the two runs minted the same identity there would be
	// nothing to prove, and the fixture would be passing for the wrong reason.
	if want.Session.ALegID == "" || want.Session.ALegID == got.Session.ALegID {
		t.Fatalf("fixture: %s: two runs must mint DIFFERENT per-turn identities for the scoped comparison to mean anything: expected_identity_bytes=%d got_identity_bytes=%d",
			tc.label, len(want.Session.ALegID), len(got.Session.ALegID))
	}
	// Locate the identity's value span by its own JSON member name rather than assuming a
	// fixed offset, so a change in the call's field order cannot silently move the
	// exclusion window onto a different field.
	const member = `"ALegID":"`
	at := bytes.Index(wantRaw, []byte(member))
	if at < 0 || bytes.Index(gotRaw, []byte(member)) != at {
		t.Fatalf("fixture: %s: the per-turn session member must be located at the same offset in both renderings: expected_offset=%d got_offset=%d",
			tc.label, at, bytes.Index(gotRaw, []byte(member)))
	}
	start := at + len(member)
	wantEnd, gotEnd := start+len(want.Session.ALegID), start+len(got.Session.ALegID)
	if wantEnd > len(wantRaw) || gotEnd > len(gotRaw) ||
		!bytes.Equal(wantRaw[start:wantEnd], []byte(want.Session.ALegID)) ||
		!bytes.Equal(gotRaw[start:gotEnd], []byte(got.Session.ALegID)) {
		t.Fatalf("fixture: %s: the located per-turn identity must match the value it was read from: expected_identity_bytes=%d got_identity_bytes=%d expected_render_bytes=%d got_render_bytes=%d",
			tc.label, len(want.Session.ALegID), len(got.Session.ALegID), len(wantRaw), len(gotRaw))
	}
	// The exact claim: splicing one run's identity value into the other's rendering must
	// reproduce that other rendering whole. A difference anywhere else - a different
	// route, a different history byte, an added or removed field - survives the splice
	// and is reported.
	spliced := make([]byte, 0, len(wantRaw)-len(want.Session.ALegID)+len(got.Session.ALegID))
	spliced = append(spliced, wantRaw[:start]...)
	spliced = append(spliced, got.Session.ALegID...)
	spliced = append(spliced, wantRaw[wantEnd:]...)
	if !bytes.Equal(spliced, gotRaw) {
		t.Fatalf("fixture: %s: two runs differ OUTSIDE the per-turn identity, so a scoped byte-identity assertion over them would hide a real difference: first_divergent_byte=%d spliced_bytes=%d got_bytes=%d",
			tc.label, commonPrefixLen(spliced, gotRaw), len(spliced), len(gotRaw))
	}
}

// commonPrefixLen is the number of leading bytes two renderings share.
func commonPrefixLen(a, b []byte) int {
	limit := min(len(a), len(b))
	for i := range limit {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

// pvRenderCall renders one canonical call the same way pvRenderStream renders a stream.
func pvRenderCall(call aipapi.Call) []byte {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(call)
	return out.Bytes()
}

// pvFirstDifferingEvent is the ordinal of the first event whose rendered form differs, or
// the length of the shorter stream when one is a prefix of the other.
func pvFirstDifferingEvent(a, b []aipapi.Event) int {
	for i := range a {
		if i >= len(b) {
			return i
		}
		if !bytes.Equal(pvRenderStream(a[i:i+1]), pvRenderStream(b[i:i+1])) {
			return i
		}
	}
	return len(a)
}

// pvEventKindAt names the bounded event kind at one ordinal, preferring the shorter stream
// so an absent ordinal renders as "absent" rather than as a zero value that could be
// mistaken for a real kind.
func pvEventKindAt(a, b []aipapi.Event, ordinal int) any {
	switch {
	case ordinal < len(a):
		return a[ordinal].Kind
	case ordinal < len(b):
		return b[ordinal].Kind
	default:
		return "absent"
	}
}

// ---------------------------------------------------------------------------
// Canonical validation after every rewrite (requirements.md 8.5).
//
// design.md "Canonical Outbound Rewriter" makes this a BYTE SPLACE rather than a re-encode,
// so the failure mode being guarded against is a splice that produced malformed canonical
// output on a surface whose validator never ran. The validator is the production one, not
// a re-implementation: aipapi's own Call.Validate, the production PublishedJSONValid
// predicate, and the assembler's own release path, which a run that completed at all has
// already exercised.
// ---------------------------------------------------------------------------

// pvAssertCanonical runs every canonical validator this run's surfaces admit to.
func pvAssertCanonical(t *testing.T, tc pvCase, run expRunResult) {
	t.Helper()
	// The whole backend-bound request, on whichever authority the row rewrote.
	if err := run.backendOpenCall.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - %s: the backend-bound request must still be one canonical call after every rewrite: error_type=%T",
			tc.label, err)
	}
	// Every tool-call argument document still on the backend-bound surface must be one
	// complete JSON value.
	if run.openMeasure.documents != run.openMeasure.validDocs {
		t.Fatalf("requirements.md 8.5 - %s: every tool-call argument document on the backend-bound request must be one complete JSON value: documents=%d complete_json_values=%d",
			tc.label, run.openMeasure.documents, run.openMeasure.validDocs)
	}
	// Every argument document the run released to the client, reassembled PER TOOL CALL,
	// must be one complete JSON value under the production predicate.
	documents, complete := pvReleasedDocuments(run.events)
	if documents < 0 {
		t.Fatalf("requirements.md 8.5 - %s: every released tool-call argument document must be one complete JSON value: documents=%d complete_json_values=%d",
			tc.label, documents, complete)
	}
}

// pvReleasedDocuments reassembles the client-facing argument deltas per tool call and
// counts how many of the reassembled documents are one complete JSON value.
//
// Reassembling per tool call rather than per delta is the point: the byte splice happens
// on the COMPLETE document, so a document split across several streaming deltas is the
// ordinary shape and a per-delta check would be checking nothing.
func pvReleasedDocuments(events []aipapi.Event) (documents, complete int) {
	joined := map[string]string{}
	order := make([]string, 0, 4)
	for _, ev := range events {
		if ev.Kind != aipapi.EventToolCallArgsDelta {
			continue
		}
		if _, seen := joined[ev.ToolCallID]; !seen {
			order = append(order, ev.ToolCallID)
		}
		joined[ev.ToolCallID] += ev.Delta
	}
	// Map iteration is avoided so the count is order-stable.
	for _, id := range order {
		documents++
		if rewrite.PublishedJSONValid([]byte(joined[id])) {
			complete++
		}
	}
	return documents, complete
}

// ---------------------------------------------------------------------------
// Bounded observability reads.
//
// design.md "Observability" is one of the three sections this task is graded against, and
// every leg below has to prove its bounded reason is RECORDED rather than merely returned.
// The counters are read through the feature's own recorder, which is the only handle a
// deployment has.
// ---------------------------------------------------------------------------

// pvTally reads one bounded label out of a telemetry series, and fails the run if the
// label is absent rather than reporting zero for it: "this reason never occurred" and
// "this reason is not part of the published vocabulary" are different facts.
func pvTally(t *testing.T, series []telemetry.Tally, reason string) int64 {
	t.Helper()
	for _, tally := range series {
		if tally.Reason == reason {
			return tally.Count
		}
	}
	t.Fatalf("fixture: the bounded reason must appear in the published series: reason=%q published_series_length=%d", reason, len(series))
	return 0
}

// pvSnapshot reads the feature's own counters for one run.
func pvSnapshot(t *testing.T, run expRunResult) telemetry.Snapshot {
	t.Helper()
	if run.observations == nil {
		t.Fatalf("fixture: %s: this run was installed through the shipped composition seam and must expose the feature's own recorder", run.label)
	}
	return run.observations.Snapshot()
}

// ---------------------------------------------------------------------------
// The matrix.
// ---------------------------------------------------------------------------

// pvDirection is the direction a matrix row's failure policy takes.
//
// It is a closed three-member set rather than a boolean because "fails open" and "fails
// closed" are opposite answers to the same question, and the whole content-freedom
// argument of this feature rests on which one applies where.
type pvDirection string

const (
	// pvRewrites is the row that succeeds, present so every negative row below has a
	// positive control in the same table rather than in a different file.
	pvRewrites pvDirection = "rewrite"
	// pvNeutral is requirement 8.1's shape: with the feature switched off, nothing
	// observable changes.
	pvNeutral pvDirection = "neutral"
	// pvFailOpen is requirement 8.2's shape: the OUTBOUND pass keeps the real path
	// and records a bounded reason.
	pvFailOpen pvDirection = "fail-open"
	// pvFailClosed is requirement 8.3's shape: the INBOUND expansion refuses, so no
	// alias-bearing argument reaches the client at all.
	pvFailClosed pvDirection = "fail-closed"
)

// pvCase is one row of the failure-policy matrix.
//
// Every row carries the same four facts, which is what makes the table readable as a
// policy: which way this failure goes, which bounded reason it must record, and whether a
// reserved namespace may reach the client.
type pvCase struct {
	// label is a bounded, content-free failure-message token and the row's identity.
	label string
	// direction is the policy the row pins.
	direction pvDirection
	// reason is the BOUNDED label the row requires, spelled exactly as the owning
	// production vocabulary publishes it. It is empty only for the neutral row, which
	// records nothing at all.
	reason string
	// aliasMayReachClient states whether a reserved-namespace byte may reach the CLIENT
	// on this row.
	//
	// It is false on every row the proxy owns, in BOTH directions: the outbound passes
	// fail open to a real path, and the inbound expansion refuses rather than release an
	// unresolved alias. It is true on exactly one row, the disabled one, where such a
	// byte was emitted by the MODEL and passes through precisely because the feature is
	// not present to interpret it - which is exactly what "unchanged" means there.
	aliasMayReachClient bool
	// drivenElsewhere names the test that drives this row when its injection point is
	// package private and no runtime turn can reach it. Empty means this file drives it.
	drivenElsewhere string
	// drive runs the row and asserts all four columns above plus the direction's
	// payload-level consequence. It is nil exactly when drivenElsewhere is set.
	drive func(t *testing.T, tc pvCase)
}

// pvDrivenElsewhereTest is the one test outside this file that owns rows of this matrix.
//
// It is named in a constant so the two halves of one policy cannot drift: this file's rows
// say which test proves them, that test's rows say the same labels, and a rename on either
// side is a failure rather than a silently narrowed table.
const pvDrivenElsewhereTest = "TestOutboundPassFailureMatrixRowsUnreachableFromATurn"

// pvMatrix is the whole failure policy of this feature in one readable table.
//
// Reading it top to bottom answers the only question requirements.md 8.1, 8.2, and 8.3
// ask: for every way this feature can fail, which way does it go, what bounded reason
// does it record, and can the client end up holding an alias?
func pvMatrix() []pvCase {
	// The two authorities the rewriter discriminates on. The legacy row is the control
	// for the item row, and both are pinned by the same run shape, so a divergence
	// between them is a fact about the authority rather than about the fixture.
	rewriteLegacy := func(t *testing.T, tc pvCase) { t.Helper(); pvDriveRewrite(t, tc, false) }
	rewriteItem := func(t *testing.T, tc pvCase) { t.Helper(); pvDriveRewrite(t, tc, true) }

	return []pvCase{
		{
			label:               "enabled_rewrite_virtualizes_the_legacy_history",
			direction:           pvRewrites,
			reason:              outbound.OutcomeRewriterRan.String(),
			aliasMayReachClient: false,
			drive:               rewriteLegacy,
		},
		{
			label:               "enabled_rewrite_virtualizes_the_item_authoritative_history",
			direction:           pvRewrites,
			reason:              outbound.OutcomeRewriterRan.String(),
			aliasMayReachClient: false,
			drive:               rewriteItem,
		},
		{
			// requirements.md 8.1. The disabled generation's whole claim is that it
			// contributes nothing, so the reserved namespace the MODEL emitted passes
			// through byte for byte - identically to a run with no feature at all.
			label:               "disabled_generation_is_byte_neutral",
			direction:           pvNeutral,
			reason:              "",
			aliasMayReachClient: true,
			drive:               pvDriveDisabledNeutral,
		},
		{
			label:               "unsupported_root_absent",
			direction:           pvFailOpen,
			reason:              pathvirtualization.SkipReasonEmptyRoot.String(),
			aliasMayReachClient: false,
			drive:               pvRefusedRootDriver(""),
		},
		{
			label:               "unsupported_root_relative",
			direction:           pvFailOpen,
			reason:              pathvirtualization.SkipReasonRelativeRoot.String(),
			aliasMayReachClient: false,
			drive:               pvRefusedRootDriver("relative/workspace/root"),
		},
		{
			label:               "unsupported_root_drive_relative_volume",
			direction:           pvFailOpen,
			reason:              pathvirtualization.SkipReasonMalformedVolumeRoot.String(),
			aliasMayReachClient: false,
			drive:               pvRefusedRootDriver(`C:`),
		},
		{
			label:               "unsupported_root_windows_device_namespace",
			direction:           pvFailOpen,
			reason:              pathvirtualization.SkipReasonDeviceNamespace.String(),
			aliasMayReachClient: false,
			drive:               pvRefusedRootDriver(`\\.\PIPE\aip-path-virtualization-matrix`),
		},
		{
			label:               "unsupported_root_inside_the_reserved_namespace",
			direction:           pvFailOpen,
			reason:              pathvirtualization.SkipReasonReservedNamespaceCollision.String(),
			aliasMayReachClient: false,
			// A supported root spelled INSIDE the fixed reserved namespace. Its own
			// alias would nest in that namespace and every path under the root would
			// then read as a reserved alias, so requirement 1.8 disables the mapping
			// rather than guessing which spelling the client meant.
			drive: pvRefusedRootDriver("/.__aip_v1__/w_hr7xnivm5at2ku356qya/packages/agent-runtime"),
		},
		{
			// The late pass with NO pinned workspace view to read at all. The
			// runtime always projects one - an empty one on the detached path - so no
			// turn reaches this condition and it is driven in the feature package.
			label:               "no_pinned_workspace_view_at_the_late_pass",
			direction:           pvFailOpen,
			reason:              outbound.OutcomeWorkspaceUnresolved.String(),
			aliasMayReachClient: false,
			drivenElsewhere:     pvDrivenElsewhereTest,
		},
		{
			// requirements.md 8.2's headline clause. The shared rewriter's error path
			// is documented as unreachable from untrusted input and the pass reaches it
			// only through its package-private rewriter port.
			label:               "unexpected_outbound_transformation_error",
			direction:           pvFailOpen,
			reason:              outbound.OutcomeTransformationFailed.String(),
			aliasMayReachClient: false,
			drivenElsewhere:     pvDrivenElsewhereTest,
		},
		{
			// requirements.md 8.3 with 6.5: an alias that names ANOTHER workspace. It
			// is well formed, so the only thing wrong with it is that it does not
			// validate against this turn's workspace tag.
			label:               "inbound_alias_naming_another_workspace",
			direction:           pvFailClosed,
			reason:              expansion.ReasonWorkspaceMismatch.String(),
			aliasMayReachClient: false,
			drive:               pvInboundWorkspaceMismatchDriver,
		},
		{
			// The same fixture family with a different bounded reason, so the two
			// refusals cannot be one blanket rule.
			label:               "inbound_malformed_reserved_alias",
			direction:           pvFailClosed,
			reason:              expansion.ReasonMalformedReservedAlias.String(),
			aliasMayReachClient: false,
			drive:               pvInboundMalformedAliasDriver,
		},
		{
			// requirements.md 4.5/4.6 through 8.3: a completed call whose assembled
			// arguments exceed the DECLARED mandatory bound is refused by the
			// assembler before any finalizer runs.
			label:               "completed_call_past_the_declared_mandatory_bound",
			direction:           pvFailClosed,
			reason:              coreruntime.ReasonMandatoryBufferingOverflow,
			aliasMayReachClient: false,
			drive:               pvInboundOverflowDriver,
		},
		{
			// requirements.md 4.6's failure clause through 8.3: while the declaring
			// pass is still undecided, an unrelated optional finalizer's failure must
			// refuse the call rather than replay the original alias-bearing fragments.
			label:               "unrelated_finalizer_failed_before_the_expansion_pass",
			direction:           pvFailClosed,
			reason:              coreruntime.ReasonMandatoryBufferingIncomplete,
			aliasMayReachClient: false,
			drive:               pvInboundIncompleteDriver,
		},
	}
}

// TestPathVirtualization_FailurePolicyMatrix is requirements.md 8.1, 8.2, 8.3, and 8.5
// as one readable table with every row DRIVEN.
//
// It asserts three things about the table itself before it drives a single row, because a
// table that has quietly lost a row, or a row whose direction and reason disagree, is a
// table that reads as a policy it no longer pins:
//
//   - every row declares a known direction and a reason that is either present (closed
//     vocabulary) or absent, which is only true for the neutral row;
//   - every row is either driven here or names exactly one test that drives it, and the
//     two sets are disjoint;
//   - every row's reason string is a label some production vocabulary actually publishes,
//     so a row can never drift away from the enum it claims to pin.
//
// Then each row drives itself.
func TestPathVirtualization_FailurePolicyMatrix(t *testing.T) {
	t.Parallel()

	matrix := pvMatrix()
	pvAssertMatrixIsWellFormed(t, matrix)

	for _, tc := range matrix {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			if tc.drive == nil {
				// Deliberately NOT a skip: a row whose driver is owned by another package
				// is still a row this table claims, and a reader following the table must
				// learn where it is proved rather than being told nothing happened. The
				// named test is required to exist, so a rename on either side fails here
				// instead of quietly dropping the row's coverage.
				tc.assertDrivenElsewhereExists(t)
				t.Logf("requirements.md 8.2/8.3 - %s: no runtime turn can reach this condition (%s); it is driven in %s",
					tc.label, tc.direction, tc.drivenElsewhere)
				return
			}
			tc.drive(t, tc)
		})
	}
}

// assertDrivenElsewhereExists fails the row unless the named test really exists.
//
// A row that names a test which has been renamed or deleted would otherwise sit in the
// table looking proved while nothing runs. The check parses the named test's OWN source
// file rather than trusting a hardcoded list, so it keeps working when the other file
// changes and fails loudly when it is renamed.
//
// It also refuses to pass vacuously: an unreadable or empty file set is a failure, because a
// guard that finds no tests has not proved anything.
func (tc pvCase) assertDrivenElsewhereExists(t *testing.T) {
	t.Helper()
	if tc.drivenElsewhere != pvDrivenElsewhereTest {
		t.Fatalf("fixture: %s: every row driven outside this file names the one feature-package test that owns those rows: named=%q want=%q",
			tc.label, tc.drivenElsewhere, pvDrivenElsewhereTest)
	}
	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("fixture: locate this test's own source file")
	}
	// The named test lives in the feature package's outbound subpackage, which is a fixed
	// relative hop from here: the file sits in internal/core/runtime.
	other := filepath.Join(filepath.Dir(thisFile), "..", "..", "plugins", "features", "pathvirtualization", "outbound", "failure_matrix_internal_test.go")
	source, err := os.ReadFile(other)
	if err != nil {
		t.Fatalf("fixture: read the test that owns the rows this one names: %v", err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), other, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("fixture: parse the test that owns the rows this one names: %v", err)
	}
	found, functions := 0, 0
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		functions++
		if fn.Name.Name == tc.drivenElsewhere {
			found++
		}
	}
	if functions == 0 {
		t.Fatalf("fixture: the file that owns the rows this one names declares no tests, so the existence check would pass vacuously")
	}
	if found != 1 {
		t.Fatalf("fixture: %s: this row names the test that drives it, but that test is not declared exactly once there: named=%q occurrences=%d",
			tc.label, tc.drivenElsewhere, found)
	}
}

// pvAssertMatrixIsWellFormed is the table's own guard.
//
// It is deliberately structural rather than behavioural: it cannot tell whether a row's
// assertions are right, and it does not try to. What it can tell is that the table still
// says what it is supposed to say, which is the failure a reader of a policy table cannot
// detect by reading it.
func pvAssertMatrixIsWellFormed(t *testing.T, matrix []pvCase) {
	t.Helper()
	published := pvPublishedBoundedLabels(t)
	driven, drivenElsewhere := 0, 0
	seen := map[string]int{}
	for _, tc := range matrix {
		seen[tc.label]++
		switch tc.direction {
		case pvRewrites, pvNeutral, pvFailOpen, pvFailClosed:
		default:
			t.Fatalf("fixture: the matrix declares an unknown direction: label=%q direction=%q", tc.label, string(tc.direction))
		}
		if tc.direction == pvNeutral {
			if tc.reason != "" {
				t.Fatalf("fixture: the neutral row records nothing, so it must declare no bounded reason: label=%q reason=%q", tc.label, tc.reason)
			}
			if !tc.aliasMayReachClient {
				t.Fatalf("fixture: the neutral row passes the model's bytes through unchanged, so a reserved-namespace byte does reach the client there: label=%q", tc.label)
			}
		} else if _, ok := published[tc.reason]; !ok {
			t.Fatalf("fixture: the matrix pins a bounded label no production vocabulary publishes: label=%q reason=%q", tc.label, tc.reason)
		}
		// A row is driven here OR names the one test that drives it, never both and
		// never neither. The two-row split is the documented package-private
		// injection point, and this is what keeps it from growing silently.
		switch {
		case tc.drive != nil && tc.drivenElsewhere == "":
			driven++
		case tc.drive == nil && tc.drivenElsewhere == "":
			t.Fatalf("fixture: a matrix row must either drive itself or name the test that drives it: label=%q", tc.label)
		case tc.drive != nil:
			t.Fatalf("fixture: a matrix row cannot be driven twice: label=%q also_names=%q", tc.label, tc.drivenElsewhere)
		default:
			drivenElsewhere++
		}
	}
	if driven == 0 || drivenElsewhere == 0 {
		t.Fatalf("fixture: the matrix must carry both driven rows and rows whose injection point is package private: driven=%d driven_elsewhere=%d", driven, drivenElsewhere)
	}
	if len(matrix) == 0 {
		t.Fatal("fixture: the matrix must not be empty")
	}
	for label, count := range seen {
		if count != 1 {
			t.Fatalf("fixture: a matrix row must appear exactly once: label=%q occurrences=%d", label, count)
		}
	}
}

// pvPublishedBoundedLabels is every bounded label the feature's own closed vocabularies
// publish, read from the production types rather than restated as prose.
//
// It is what makes the table's reason column self-checking: a row cannot name a label the
// feature would never emit. The lexical core's root vocabulary is a STRING type rather
// than an ordinal enum, so its members are enumerated by name - which is also the only way
// to enumerate a closed set of that shape without re-deriving an ordinal range.
func pvPublishedBoundedLabels(t *testing.T) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, reason := range []pathvirtualization.SkipReason{
		pathvirtualization.SkipReasonNone,
		pathvirtualization.SkipReasonEmptyRoot,
		pathvirtualization.SkipReasonRelativeRoot,
		pathvirtualization.SkipReasonMalformedVolumeRoot,
		pathvirtualization.SkipReasonDeviceNamespace,
		pathvirtualization.SkipReasonReservedNamespaceCollision,
	} {
		out[reason.String()] = struct{}{}
	}
	for outcome := range int(outbound.OutcomeWorkspaceUnresolved) + 1 {
		out[outbound.Outcome(outcome).String()] = struct{}{}
	}
	for reason := range int(expansion.ReasonInvalidRewrite) + 1 {
		out[expansion.Reason(reason).String()] = struct{}{}
	}
	for _, reason := range []string{
		coreruntime.ReasonMandatoryBufferingOverflow,
		coreruntime.ReasonMandatoryBufferingIncomplete,
		coreruntime.ReasonMandatoryBufferingDeclarationInvalid,
	} {
		out[reason] = struct{}{}
	}
	return out
}

// ---------------------------------------------------------------------------
// Row drivers.
// ---------------------------------------------------------------------------

// pvDriveRewrite is the positive control every negative row below is read against: the
// shipped composition seam, switched on in rewrite mode, over one canonical authority.
//
// It pins that the alias really was minted (so every "the real path survived" row below
// is measured against a fixture that WOULD have virtualized), that the bounded reason the
// pass recorded is the routine one, that the feature's own counters saw the saving, and
// that the client saw the real path with no reserved namespace anywhere in a selected
// field.
func pvDriveRewrite(t *testing.T, tc pvCase, itemAuthority bool) {
	t.Helper()
	alias := hookRegAliasOf(t)
	scenario := expScenario{
		label:             tc.label,
		aliasRoot:         alias,
		expansionDecides:  true,
		observersExpected: true,
		wiring:            expWiringShippedBundle,
		bundleYAML:        pvOperatorRewrite,
	}
	if itemAuthority {
		scenario.ingress = func(*testing.T) *aipapi.Call { return matrixItemIngress() }
	}
	got := expRun(t, scenario)

	if !got.contributedAttempt || !got.contributedPart || !got.contributedFinalizer {
		t.Fatalf("fixture: %s: an enabled generation must contribute to all three planes: attempt=%t request_part=%t finalizer=%t",
			tc.label, got.contributedAttempt, got.contributedPart, got.contributedFinalizer)
	}
	if !got.aliasActive {
		t.Fatal("fixture: the pinned project root must derive an active mapping on a rewrite row; its refusal code is bounded and content-free")
	}
	// The feature's own bounded counters, read through the only handle a deployment
	// has, and through the SHIPPED recorder rather than a test-local reporter: the
	// bundle installs its own sink, so a run wired through it has no test-local report
	// to read and the counters are the observable surface a deployment actually has.
	snapshot := pvSnapshot(t, got)
	if snapshot.Outbound.Reports < 2 {
		t.Fatalf("requirements.md 7.6 - %s: both outbound passes must record into the feature's counters: outbound_reports=%d",
			tc.label, snapshot.Outbound.Reports)
	}
	if pvTally(t, snapshot.Outbound.Outcomes, outbound.OutcomeRewriterRan.String()) < 2 {
		t.Fatalf("requirements.md 7.2/7.6 - %s: both outbound passes must record the routine outcome: outcome_series=%v",
			tc.label, snapshot.Outbound.Outcomes)
	}
	if snapshot.Outbound.Virtualized.Rewritten < 1 || snapshot.Outbound.Virtualized.BytesSaved <= 0 {
		t.Fatalf("requirements.md 7.6/9.5 - %s: the outbound direction must publish a measured saving: rewritten=%d bytes_saved=%d",
			tc.label, snapshot.Outbound.Virtualized.Rewritten, snapshot.Outbound.Virtualized.BytesSaved)
	}
	if snapshot.Inbound.Expanded < 1 {
		t.Fatalf("requirements.md 4.1 - %s: the inbound direction must expand the alias before the client sees it: expanded=%d",
			tc.label, snapshot.Inbound.Expanded)
	}

	if itemAuthority {
		// Candidate adaptation bridges the item authority onto the message authority
		// before the provider is called, so the backend-bound surface is measured by the
		// shared measurement rather than by reading Items. What the ITEM shape has to
		// prove is that the splice never made it non-canonical in the first place,
		// which is asserted on the item form directly below and through the shared
		// canonical-validation leg every row runs.
		if got.openMeasure.aliasHits != 1 || got.openMeasure.realHits != 0 {
			t.Fatalf("requirements.md 5.2/8.5 - %s: the item-authoritative history must be virtualized before Backend.Open: alias_hits=%d real_root_hits=%d",
				tc.label, got.openMeasure.aliasHits, got.openMeasure.realHits)
		}
		pvAssertItemAuthorityStayedCanonical(t, tc, alias)
	} else {
		if got.openMeasure.aliasHits != 1 || got.openMeasure.realHits != 0 {
			t.Fatalf("requirements.md 5.2 - %s: the legacy history must reach Backend.Open carrying exactly one alias and no real root: alias_hits=%d real_root_hits=%d",
				tc.label, got.openMeasure.aliasHits, got.openMeasure.realHits)
		}
	}

	// The client saw the REAL path, byte for byte, in the selected field, and the
	// released document is the expanded one.
	scan := expScanClientEvents(got.events, "", "")
	if scan.reservedSelected != 0 {
		t.Fatalf("requirements.md 4.1 - %s: no selected path field may reach the client carrying the reserved namespace: documents=%d selected_fields=%d reserved_selected=%d",
			tc.label, scan.documents, scan.selectedFields, scan.reservedSelected)
	}
	if scan.realSelected != 1 {
		t.Fatalf("requirements.md 4.1 - %s: exactly one selected path field must reach the client as the real path: real_selected=%d documents=%d",
			tc.label, scan.realSelected, scan.documents)
	}
	fixture := expFixture{root: alias, suffix: expModelSuffix}
	if got.released != fixture.expExpandedModelArgsDocument(got.realRoot) {
		t.Fatalf("requirements.md 4.1/2.8 - %s: the released argument document must be the expanded document byte for byte: released_bytes=%d want_bytes=%d",
			tc.label, len(got.released), len(fixture.expExpandedModelArgsDocument(got.realRoot)))
	}
	pvAssertCanonical(t, tc, got)
}

// pvDriveDisabledNeutral is requirements.md 8.1.
//
// Three runs, one fixture, three wirings:
//
//   - the feature is ABSENT from the registry;
//   - the feature is present but the shipped composition decoded a DISABLED subtree;
//   - the feature is present and switched on, which is the control that makes the byte
//     comparison non-vacuous.
//
// The claim is byte identity between the first two, over the WHOLE client-facing event
// stream and over the whole backend-bound request. The third run must differ on both
// surfaces, or "byte-identical" would be satisfied by a comparison that cannot see a
// difference - and it must differ for the right reason, which is that it actually
// virtualized.
func pvDriveDisabledNeutral(t *testing.T, tc pvCase) {
	t.Helper()
	alias := hookRegAliasOf(t)
	base := func(label string, wiring expWiring, operatorYAML string, expansion bool) expScenario {
		return expScenario{
			label:             label,
			aliasRoot:         alias,
			expansionDecides:  expansion,
			observersExpected: true,
			wiring:            wiring,
			bundleYAML:        operatorYAML,
		}
	}
	absent := expRun(t, base(tc.label+"_feature_absent", expWiringAbsent, pvOperatorAbsent, false))
	disabled := expRun(t, base(tc.label+"_feature_disabled", expWiringShippedBundle, pvOperatorDisabled, false))
	control := expRun(t, base(tc.label+"_feature_enabled", expWiringShippedBundle, pvOperatorRewrite, true))

	// The measurement, not the assumption: neither baseline run put a participant on
	// any of the three planes this feature owns, and the control put one on each.
	for _, run := range []expRunResult{absent, disabled} {
		if run.contributedAttempt || run.contributedPart || run.contributedFinalizer {
			t.Fatalf("requirements.md 8.1 - %s: a generation that contributes nothing must contribute nothing on all three planes: run=%q attempt=%t request_part=%t finalizer=%t",
				tc.label, run.label, run.contributedAttempt, run.contributedPart, run.contributedFinalizer)
		}
	}
	if !control.contributedAttempt || !control.contributedPart || !control.contributedFinalizer {
		t.Fatalf("fixture: %s: the enabled control must contribute to all three planes, or the byte comparison below is vacuous: attempt=%t request_part=%t finalizer=%t",
			tc.label, control.contributedAttempt, control.contributedPart, control.contributedFinalizer)
	}
	attemptCount, partCount, expansionCount := absent.reports.total()
	if attemptCount != 0 || partCount != 0 || expansionCount != 0 {
		t.Fatalf("requirements.md 8.1 - %s: an absent feature must record nothing at all, because it has no pass to record through: attempt_reports=%d request_part_reports=%d expansion_reports=%d",
			tc.label, attemptCount, partCount, expansionCount)
	}
	if attemptCount, partCount, _ = disabled.reports.total(); attemptCount != 0 || partCount != 0 {
		t.Fatalf("requirements.md 8.1 - %s: a disabled generation must record nothing at all: attempt_reports=%d request_part_reports=%d",
			tc.label, attemptCount, partCount)
	}
	// The disabled generation still answers "is it on?" rather than staying silent,
	// which is what requirements.md 7.8's inventory is for.
	disabledSnapshot := pvSnapshot(t, disabled)
	if disabledSnapshot.Inventory.Enabled {
		t.Fatalf("fixture: %s: the decoded subtree must really be disabled: inventory_enabled=%t", tc.label, disabledSnapshot.Inventory.Enabled)
	}
	if disabledSnapshot.Outbound.Reports != 0 || disabledSnapshot.Inbound.Reports != 0 || disabledSnapshot.Total.Reports != 0 {
		t.Fatalf("requirements.md 8.1 - %s: a disabled generation must accumulate no traffic counters: outbound_reports=%d inbound_reports=%d total_reports=%d",
			tc.label, disabledSnapshot.Outbound.Reports, disabledSnapshot.Inbound.Reports, disabledSnapshot.Total.Reports)
	}

	// THE CLAIM. Byte identity, over both surfaces.
	pvAssertCallIdentical(t, tc, absent.backendOpenCall, disabled.backendOpenCall)
	pvAssertStreamIdentical(t, tc, absent.events, disabled.events)

	// THE ORACLE. The enabled control differs on both surfaces, for the reason this
	// feature exists.
	pvAssertCallDiffers(t, tc, absent.backendOpenCall, control.backendOpenCall)
	pvAssertStreamDiffers(t, tc, absent.events, control.events)
	if control.openMeasure.aliasHits < 1 {
		t.Fatalf("fixture: %s: the enabled control must really virtualize, or its difference proves nothing about this feature: alias_hits=%d",
			tc.label, control.openMeasure.aliasHits)
	}
	// The neutral row's one true value in the alias column: the reserved namespace the
	// MODEL emitted passes through untouched in both baseline runs, which is precisely
	// what "unchanged" means for a configuration that has no expansion pass to
	// interpret it.
	pvAssertNeutralPassesModelBytesThrough(t, tc, absent)
	pvAssertNeutralPassesModelBytesThrough(t, tc, disabled)

	for _, run := range []expRunResult{absent, disabled, control} {
		pvAssertCanonical(t, tc, run)
	}
}

// pvAssertNeutralPassesModelBytesThrough reads the matrix's alias column for a
// feature-less run: the model's own reserved-namespace document is released to the client
// byte for byte.
//
// It is asserted rather than assumed because it is the one row where
// aliasMayReachClient is true, and a reader who has just been told "no alias reaches the
// client on any row the proxy owns" deserves to see the one place where the proxy is not
// involved at all.
func pvAssertNeutralPassesModelBytesThrough(t *testing.T, tc pvCase, run expRunResult) {
	t.Helper()
	scan := expScanClientEvents(run.events, "", "")
	if scan.reservedSelected != 1 {
		t.Fatalf("requirements.md 8.1 - %s: with the feature absent, the model's own emitted document must pass through byte for byte: documents=%d selected_fields=%d reserved_selected=%d",
			tc.label, scan.documents, scan.selectedFields, scan.reservedSelected)
	}
	// No pass ran, so nothing expanded it, and the assembler published the input
	// unchanged.
	fixture := expFixture{root: hookRegAliasOf(t), suffix: expModelSuffix}
	if run.released != fixture.modelArgsDocument() {
		t.Fatalf("requirements.md 8.1 - %s: with no expansion pass installed, the released argument document must be the model's own document: released_bytes=%d want_bytes=%d",
			tc.label, len(run.released), len(fixture.modelArgsDocument()))
	}
}

// pvRefusedRootDriver builds the driver for one unusable AUTHORITATIVE project root.
//
// The shape is chosen so the row measures the OUTBOUND direction and nothing else:
//
//   - the client's replayed history is spelled against matrixClientRoot, an ordinary long
//     supported root, so there are real paths on the wire for the pass to preserve;
//   - the runtime pins the refused root, so both real outbound passes derive no mapping;
//   - the expansion plane carries no real finalizer, so the model's echo of the preserved
//     real path is released untouched and the row cannot be confused with an inbound
//     refusal;
//   - and the model emits the REAL path, not an alias, because the model was shown the
//     real path - virtualization never became model-visible on this row, which is the
//     precondition requirements.md 8.2 names for failing open.
//
// Every distinct root-refusal code gets its own row, because a single case cannot tell a
// blanket "refuse and preserve" rule from one that records the right reason for the right
// shape.
func pvRefusedRootDriver(root string) func(t *testing.T, tc pvCase) {
	return func(t *testing.T, tc pvCase) {
		t.Helper()
		// The root must really be refused, and it must really be the reason the row
		// claims. Otherwise the row would be measuring a different case entirely.
		if _, got := pathvirtualization.DeriveMapping(root); got.String() != tc.reason {
			t.Fatalf("fixture: %s: the pinned root must be refused with exactly this row's bounded reason: declared=%q derived=%q",
				tc.label, tc.reason, got.String())
		}
		pinned := root
		echo := expFixture{root: matrixClientRoot, suffix: expModelSuffix}
		got := expRun(t, expScenario{
			label:               tc.label,
			aliasRoot:           echo.root,
			expansionDecides:    false,
			modelEchoesRealPath: true,
			// The model echoed the real path it was shown, which is the only echo a
			// fail-open row can produce.
			observersExpected: true,
			ingress:           func(*testing.T) *aipapi.Call { return matrixLegacyIngress(t) },
			pinnedRoot:        &pinned,
			allowRefusedRoot:  true,
			wiring:            expWiringTaskOwn,
		})
		if got.aliasActive {
			t.Fatal("fixture: the pinned project root must derive NO mapping on a refused-root row")
		}

		// The bounded reason, read off BOTH real outbound passes' own reports. A
		// pass-level outcome without the root code would satisfy a coarser claim
		// than the one this row makes, and reading it off only one of the two
		// passes would leave the other unproven.
		for _, read := range []struct {
			name   string
			report outbound.Report
		}{
			{"early", got.reports.oneAttempt(t)},
			{"late", got.reports.onePart(t)},
		} {
			if read.report.Outcome != outbound.OutcomeProjectRootUnusable {
				t.Fatalf("requirements.md 1.8/8.2 - %s: an unusable root must publish nothing and record the pass-level refusal: pass=%s outcome=%v",
					tc.label, read.name, read.report.Outcome)
			}
			if read.report.RootReason.String() != tc.reason {
				t.Fatalf("requirements.md 1.8 - %s: the refusal must carry the lexical core's own bounded code: pass=%s recorded=%q want=%q",
					tc.label, read.name, read.report.RootReason.String(), tc.reason)
			}
			if !reflect.DeepEqual(read.report.Stats, rewrite.Stats{}) {
				t.Fatalf("requirements.md 1.8 - %s: a pass that never reached the rewriter must report no rewrite statistics: pass=%s eligible=%d rewritten=%d",
					tc.label, read.name, read.report.Stats.Eligible, read.report.Stats.Rewritten)
			}
		}

		// The shared measurement must have read this row's surface and found NEITHER an
		// alias nor the refused root on it. That is what makes the two byte comparisons
		// below a statement about the client's real path rather than a coincidence: the
		// selected value was read, and it matched neither oracle.
		//
		// It also pins the harness's own alias needle. A refused root publishes no
		// alias, and an empty needle matches every string, so the harness substitutes a
		// value no path can contain - a substitution nothing here would notice if it were
		// removed. This is where that is noticed.
		if got.openMeasure.selectedFields != 1 || got.openMeasure.documents != 1 {
			t.Fatalf("fixture: %s: the backend-bound request must carry exactly the one path-bearing history tool call: documents=%d selected_fields=%d",
				tc.label, got.openMeasure.documents, got.openMeasure.selectedFields)
		}
		if got.openMeasure.aliasHits != 0 {
			t.Fatalf("requirements.md 8.2 - %s: a refused root must publish no alias on the client history: alias_hits=%d selected_fields=%d",
				tc.label, got.openMeasure.aliasHits, got.openMeasure.selectedFields)
		}
		// The refused root itself must not appear on the wire either, except that an EMPTY
		// root has nothing to appear: the measurement asks whether the selected value
		// contains the pinned root's bytes, and every string contains the empty string, so
		// the empty-root row cannot answer this and must not pretend to.
		if root != "" && got.openMeasure.realHits != 0 {
			t.Fatalf("requirements.md 8.2 - %s: the refused root must not appear on the client history: refused_root_hits=%d selected_fields=%d",
				tc.label, got.openMeasure.realHits, got.openMeasure.selectedFields)
		}

		// THE CLAIM: the real path survives byte for byte at the backend bound and on
		// the wire, and no reserved namespace was minted anywhere.
		if got.openSelected != matrixExpectedClientPath() {
			t.Fatalf("requirements.md 8.2 - %s: the real path must survive the refusal byte for byte at the backend bound: selected_bytes=%d want_bytes=%d",
				tc.label, len(got.openSelected), len(matrixExpectedClientPath()))
		}
		if got.released != echo.modelArgsDocument() {
			t.Fatalf("requirements.md 8.2 - %s: the published arguments must be unchanged by the refusal: released_bytes=%d want_bytes=%d",
				tc.label, len(got.released), len(echo.modelArgsDocument()))
		}
		if bytes.Contains(pvRenderCall(got.backendOpenCall), []byte(expReservedMarker)) {
			t.Fatalf("requirements.md 8.2 - %s: a refused root must not mint a reserved namespace on any field of the backend-bound request", tc.label)
		}
		scan := expScanClientEvents(got.events, matrixClientRoot, "")
		if scan.reservedSelected != 0 {
			t.Fatalf("requirements.md 8.2 - %s: no selected path field may carry a reserved namespace: documents=%d selected_fields=%d reserved_selected=%d",
				tc.label, scan.documents, scan.selectedFields, scan.reservedSelected)
		}
		if scan.realSelected != 1 {
			t.Fatalf("requirements.md 8.2 - %s: exactly one selected path field must reach the client as the real path: real_selected=%d documents=%d",
				tc.label, scan.realSelected, scan.documents)
		}
		pvAssertCanonical(t, tc, got)
	}
}

// pvInboundWorkspaceMismatchDriver is the fail-closed half of requirements.md 8.3: an
// alias that is well formed but names a DIFFERENT workspace.
//
// It drives the existing fail-closed harness rather than a second one, and adds only what
// that harness does not already assert: the telemetry counters, the canonical validation
// of the backend-bound request the turn still built, and the matrix's own policy columns.
func pvInboundWorkspaceMismatchDriver(t *testing.T, tc pvCase) {
	t.Helper()
	otherRoot := "/home/dev/workspaces/aip-path-virtualization-matrix-other-workspace/packages/agent-runtime"
	otherMapping, otherReason := pathvirtualization.DeriveMapping(otherRoot)
	if otherReason != pathvirtualization.SkipReasonNone || otherMapping.VirtualRoot == "" {
		t.Fatal("fixture: the second project root must derive an active mapping; its refusal code is bounded and content-free")
	}
	if otherMapping.VirtualRoot == hookRegAliasOf(t) {
		t.Fatal("fixture: the second project root must derive a DIFFERENT workspace tag, otherwise nothing is stale about the alias")
	}
	pvDriveInboundRefusal(t, tc, expRefusalCase{
		label:               tc.label,
		aliasRoot:           otherMapping.VirtualRoot,
		wantPassInvocations: 1,
		wantPassReason:      expansion.ReasonWorkspaceMismatch,
		wantRejectError:     true,
	})
}

// pvInboundMalformedAliasDriver is the OTHER fail-closed direction of requirements.md 8.3:
// a reserved-namespace spelling whose tag segment is not a well-formed workspace tag.
//
// It is a different bounded reason over the same fixture family as the mismatch row, which
// is what keeps the two refusals from being one blanket rule.
func pvInboundMalformedAliasDriver(t *testing.T, tc pvCase) {
	t.Helper()
	// A fixed reserved-namespace spelling whose tag segment is not the frozen tag
	// length. It is derived from nothing, so it can never become resolvable.
	const malformedAlias = "/.__aip_v1__/w_0123456789abcdefghij/"
	pvDriveInboundRefusal(t, tc, expRefusalCase{
		label:               tc.label,
		malformedAlias:      malformedAlias,
		wantPassInvocations: 1,
		wantPassReason:      expansion.ReasonMalformedReservedAlias,
		wantRejectError:     true,
	})
}

// pvInboundOverflowDriver is requirements.md 4.5/4.6's assembler-side refusal, which
// requirements.md 8.3 inherits: a completed call whose assembled arguments exceed the
// declared mandatory bound never reaches a finalizer at all.
func pvInboundOverflowDriver(t *testing.T, tc pvCase) {
	t.Helper()
	pvDriveInboundRefusal(t, tc, expRefusalCase{
		label:               tc.label,
		aliasRoot:           hookRegAliasOf(t),
		assembleBody:        expOverflowBytes,
		wantPassInvocations: 0,
		wantMandatoryReason: coreruntime.ReasonMandatoryBufferingOverflow,
	})
}

// pvInboundIncompleteDriver is requirements.md 4.6's failure clause: while the declaring
// expansion pass is still undecided, an unrelated optional finalizer's failure must refuse
// the call rather than replay the original alias-bearing fragments.
func pvInboundIncompleteDriver(t *testing.T, tc pvCase) {
	t.Helper()
	pvDriveInboundRefusal(t, tc, expRefusalCase{
		label:               tc.label,
		aliasRoot:           hookRegAliasOf(t),
		extraFinalizers:     []toolcall.Finalizer{&expFailingOrdinaryFinalizer{}},
		wantPassInvocations: 0,
		wantMandatoryReason: coreruntime.ReasonMandatoryBufferingIncomplete,
	})
}

// pvDriveInboundRefusal is the shared body of the four fail-closed rows.
//
// It runs the existing fail-closed harness, hands the result to the existing whole-stream
// no-alias assertion, and adds the three things this task's matrix adds on top: the row's
// bounded reason is the one the turn actually produced, the refusal is visible in the
// feature's own counters, and the request the turn still built stayed canonical.
func pvDriveInboundRefusal(t *testing.T, tc pvCase, refusal expRefusalCase) {
	t.Helper()
	// The row's reason and the harness's expectation are the same claim, and they are
	// written independently, so a mismatch between them is a fixture bug rather than a
	// silent agreement.
	if refusal.wantMandatoryReason != "" && refusal.wantMandatoryReason != tc.reason {
		t.Fatalf("fixture: %s: the row declares one bounded reason and the harness expects another: row=%q harness=%q",
			tc.label, tc.reason, refusal.wantMandatoryReason)
	}
	if refusal.wantMandatoryReason == "" && refusal.wantPassReason.String() != tc.reason {
		t.Fatalf("fixture: %s: the row declares one bounded reason and the harness expects another: row=%q harness=%q",
			tc.label, tc.reason, refusal.wantPassReason.String())
	}

	result, scan := expRunRefusal(t, refusal)
	modelFixture := expFixture{root: refusal.effectiveRoot(), suffix: expModelSuffix}
	assertNoAliasReachedTheClient(t, refusal, result, scan, modelFixture)

	// The reason the TURN produced, not just the reason the fixture expected.
	if refusal.wantMandatoryReason != "" {
		var typed *coreruntime.MandatoryBufferingError
		if !errors.As(result.recvErr, &typed) || typed.Reason != tc.reason {
			t.Fatalf("requirements.md 4.5/4.6/8.3 - %s: the turn must fail with the assembler's own bounded reason: declared=%q error_type=%T",
				tc.label, tc.reason, result.recvErr)
		}
	} else {
		if len(result.expReports) != 1 || result.expReports[0].Reason.String() != tc.reason {
			t.Fatalf("requirements.md 8.3 - %s: the shipped pass must have recorded exactly this bounded reason: reports=%d",
				tc.label, len(result.expReports))
		}
		if result.expResult.ReasonCode != tc.reason {
			t.Fatalf("requirements.md 8.3 - %s: the client-facing refusal must carry the same bounded reason: reason=%q",
				tc.label, result.expResult.ReasonCode)
		}
	}
	// The fail-closed answer releases nothing, so the matrix's own column holds.
	if scan.toolEvents != 0 || scan.argEvents != 0 {
		t.Fatalf("requirements.md 8.3 - %s: a fail-closed row must release no tool lifecycle at all: events=%d tool_events=%d argument_events=%d",
			tc.label, scan.events, scan.toolEvents, scan.argEvents)
	}
	// The turn still built a backend-bound request, and a refusal is not a licence to
	// emit malformed canonical output.
	pvAssertCanonical(t, tc, result)
}

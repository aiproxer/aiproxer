package outbound_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// The tests in this file drive the pass through the REAL candidate-attempt
// extension point the runtime itself calls:
// internal/core/extensions.RunCandidateAttemptTransformStage, over the canonical
// aipapi.Call value it mutates in place.
//
// They are deliberately black-box against the runtime. No executor, no routing
// plan, no backend, and no conversation view is involved, so every assertion here
// is about what the pass does to the candidate call at the one stage position
// requirements.md 5.3 and 5.5 constrain, and none of it depends on stage ordering
// that the Task 1.3 characterization already owns.

// stageTransform runs one pass through the real stage, exactly as the executor
// does, and returns the stage result together with the call the stage left behind.
func stageTransform(t *testing.T, transform request.AttemptTransform, call *aipapi.Call, meta request.AttemptMeta) extensions.AttemptTransformStageResult {
	t.Helper()

	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil, []request.AttemptTransform{transform}, call, meta, request.Services{},
	)
	if err != nil {
		t.Fatalf("requirements.md 5.2 - the candidate attempt stage must accept a well-formed outbound pass: %v", err)
	}
	if res.Excluded {
		t.Fatalf("requirements.md 5.5 - the pass must never exclude a candidate: reason=%q", res.ReasonCode)
	}
	return res
}

// TestCandidateAttemptStagePublishesVirtualizedHistoryBeforeSizing proves the
// placement requirements.md 5.3 asks for, observed at the real stage: after the
// stage runs, the candidate call the runtime will hand to candidate sizing carries
// the virtual alias on its path-bearing tool history and no real root on that field.
func TestCandidateAttemptStagePublishesVirtualizedHistoryBeforeSizing(t *testing.T) {
	t.Parallel()

	call := attemptItemCall(attemptPathArguments)
	transform := attemptRewriteTransform(t)
	stageTransform(t, transform, call, attemptWorkspace())
	if err := call.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the stage must leave the call canonically valid: %v", err)
	}
	encoded := attemptMarshal(t, *call)
	if !strings.Contains(encoded, `"file_path":"`+attemptVirtual+`"`) {
		t.Fatalf("requirements.md 5.3 - the candidate call must reach sizing with virtualized path-bearing history")
	}
	if strings.Contains(encoded, `"file_path":"`+attemptTarget+`"`) {
		t.Fatalf("requirements.md 5.3 - the candidate call must not reach sizing with a real-root path-bearing field")
	}
}

// TestCandidateAttemptStageRollsBackAFailedPass proves requirement 8.2 at the real
// stage rather than only in isolation: the stage's own rollback plus the pass's
// declared fail-open mode mean that a pass which DID return an error leaves the
// candidate carrying its real paths and the turn continues.
func TestCandidateAttemptStageRollsBackAFailedPass(t *testing.T) {
	t.Parallel()

	// The real rewriter cannot fail on a client payload, so the failing pass is a
	// stand-in that returns an error after partially rewriting the call - the same
	// hazard the feature's own fail-open branch refuses to publish.
	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	failing := &mutatingErroringTransform{err: errors.New("unexpected transformation failure")}
	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil, []request.AttemptTransform{failing}, call, attemptWorkspace(), request.Services{},
	)
	if err != nil {
		t.Fatalf("a fail-open stage must continue the candidate: %v", err)
	}
	if res.Excluded {
		t.Fatal("a fail-open stage failure must not exclude the candidate")
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("the stage must roll a failed pass back to the call the runtime owns")
	}
}

// TestCandidateAttemptStageRecoversAPanickingFailOpenPass proves the stage's recovery
// path agrees with requirement 8.2 for this pass's declared failure mode: a panic
// inside a fail-open participant is contained, the partial mutation it had already
// applied is rolled back, and the turn continues.
func TestCandidateAttemptStageRecoversAPanickingFailOpenPass(t *testing.T) {
	t.Parallel()

	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil, []request.AttemptTransform{&panicTransform{}}, call, attemptWorkspace(), request.Services{},
	)
	if err != nil {
		t.Fatalf("a fail-open pass that panics must let the candidate continue: %v", err)
	}
	if res.Excluded {
		t.Fatal("a fail-open pass that panics must not exclude the candidate")
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("the stage must roll a panicking pass back to the call the runtime owns")
	}
}

// TestAttemptTransformContributesThroughTheFeaturePlane proves the pass is
// registrable on the real extension plane the standard distribution composes
// through, using only the published SDK contract. Task 9.2 owns wiring the feature
// into the stock bundle; what this test pins is that the plane accepts the pass,
// validates it, materializes it in the stage's sort order, and hands it to the
// stage.
func TestAttemptTransformContributesThroughTheFeaturePlane(t *testing.T) {
	t.Parallel()

	rec := &attemptRecorder{}
	transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))

	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "path-virtualization", []request.AttemptTransform{transform}); err != nil {
		t.Fatalf("contribute the pass to the attempt-transform plane: %v", err)
	}
	frozen := cs.Freeze()
	if err := frozen.Validate(); err != nil {
		t.Fatalf("the frozen plane set must validate: %v", err)
	}

	frozenTransforms := aipfeature.PlaneAttemptTransforms.RequestMaterializer(
		aipfeature.Get(frozen, aipfeature.PlaneAttemptTransforms),
	)
	// The id is a LITERAL: comparing it with outbound.TransformID would move with the
	// implementation and hide a drifted stable identity.
	const wantID = "path-virtualization-attempt-transform"
	if len(frozenTransforms) != 1 || frozenTransforms[0].ID() != wantID {
		t.Fatalf("frozen transforms = %d, want the one pass by its stable id %q", len(frozenTransforms), wantID)
	}

	call := attemptItemCall(attemptPathArguments)
	if _, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil, frozenTransforms, call, attemptWorkspace(), request.Services{},
	); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !strings.Contains(attemptMarshal(t, *call), attemptVirtual) {
		t.Fatalf("the pass contributed through the real plane must virtualize the candidate")
	}
	if rec.only(t).Stats.Rewritten != 1 {
		t.Fatalf("the pass contributed through the real plane must report its rewrite")
	}
}

// TestAttemptTransformSortsIntoTheCandidateAttemptStageOrder proves the pass's
// declared order places it inside the candidate-attempt stage ordering rather than
// at an extreme: it must run after any participant that shapes the call earlier and
// before any that runs later, which is what lets candidate sizing and
// token-accounting preflight observe the savings (requirements.md 5.3).
func TestAttemptTransformSortsIntoTheCandidateAttemptStageOrder(t *testing.T) {
	t.Parallel()

	// The stand-ins are pinned to literal orders around the stage's default position.
	// Deriving them from outbound.OrderAttemptTransform would carry the pass along with
	// them, so moving it to an extreme would stay invisible here.
	late := &orderingTransform{id: "zzz-late-participant", order: 1}
	early := &orderingTransform{id: "aaa-early-participant", order: -1}
	mine := attemptRewriteTransform(t)

	sorted := request.MaterializeAttemptsSorted([]request.AttemptTransform{late, mine, early})
	if len(sorted) != 3 {
		t.Fatalf("sorted transforms = %d, want 3", len(sorted))
	}
	if sorted[0] != request.AttemptTransform(early) || sorted[1] != request.AttemptTransform(mine) || sorted[2] != request.AttemptTransform(late) {
		t.Fatalf("the feature pass must sort between the earlier and later participants: order = [%s %s %s]",
			sorted[0].ID(), sorted[1].ID(), sorted[2].ID())
	}
}

// TestAttemptTransformConcurrentCandidatesAgreeOnTheAlias proves requirement 5.6
// under real concurrency: one shared pass instance, many candidates, one alias, and
// one report per candidate.
func TestAttemptTransformConcurrentCandidatesAgreeOnTheAlias(t *testing.T) {
	t.Parallel()

	const candidates = 16
	rec := &concurrentRecorder{}
	transform := attemptRewriteTransform(t, outbound.WithReporter(rec.record))

	var (
		wg        sync.WaitGroup
		published = make([]string, candidates)
	)
	for i := range candidates {
		wg.Go(func() {
			call := attemptItemCall(attemptPathArguments)
			if _, err := transform.HandleAttempt(context.Background(), call, attemptWorkspace(), request.Services{}); err != nil {
				return
			}
			encoded, err := json.Marshal(call)
			if err != nil {
				return
			}
			published[i] = string(encoded)
		})
	}
	wg.Wait()

	for i, got := range published {
		if got == "" {
			t.Fatalf("candidate %d did not complete", i)
		}
		if got != published[0] {
			t.Fatalf("requirements.md 5.6 - candidate %d disagreed with candidate 0 on the derived alias", i)
		}
		if !strings.Contains(got, attemptVirtual) {
			t.Fatalf("candidate %d was not virtualized", i)
		}
	}
	if got := rec.count(); got != candidates {
		t.Fatalf("reports = %d, want %d: the pass must record exactly one outcome per attempt", got, candidates)
	}
}

// TestAttemptTransformRunsAgainstTheFullFixtureCorpus is the aggregate proof that
// the pass composes: every fixture flavor, mode, authority, and argument shape
// reaches a canonically valid candidate, is idempotent under reapplication, and
// publishes nothing at all in audit mode.
func TestAttemptTransformRunsAgainstTheFullFixtureCorpus(t *testing.T) {
	t.Parallel()

	// One project root per supported lexical flavor, so the corpus proves the pass
	// derives a usable mapping on all five (requirements.md 1.2) rather than only on
	// the POSIX spelling every other fixture in this file uses.
	//
	// Every root is deliberately LONG. A short root is a supported root whose alias is
	// not strictly shorter than the root itself, which correctly leaves outbound
	// virtualization inactive (requirement 1.4); that condition is covered on its own
	// by TestAttemptTransformLeavesCallUnchangedWhenTheAliasDoesNotShorten, and here
	// every flavor must actually rewrite.
	roots := []string{
		attemptRoot,
		`C:\Users\dev\workspaces\aiproxer-example-project`,
		`\\build01\dev\workspaces\aiproxer-example-project`,
		`\\?\C:\Users\dev\workspaces\aiproxer-example-project`,
		`\\?\UNC\build01\dev\workspaces\aiproxer-example-project`,
	}
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		for _, root := range roots {
			separator := "/"
			if !strings.HasPrefix(root, "/") {
				separator = "\\"
			}
			path := root + separator + "pkg/aipapi/call.go"
			// Every body is rendered through encoding/json rather than string
			// concatenation, because a Windows root carries backslashes that must be
			// escaped to remain one valid JSON document (requirements.md 8.5).
			//
			// eligible marks a body whose /file_path holds the real root, which is
			// the only case a rewrite-mode pass must replace. A body that merely
			// mentions the root somewhere no profile claims is required to keep it
			// (requirements.md 2.3), and a relative value is not a workspace path at
			// all (requirements.md out-of-scope "relative-path rewriting").
			bodies := []struct {
				name     string
				body     any
				eligible bool
				want     string
			}{
				{
					name:     "selected_with_payload_sibling",
					body:     map[string]any{"file_path": path, "content": "literal " + path + " text", "limit": 10},
					eligible: true,
				},
				{
					name:     "selected_with_string_array_sibling",
					body:     map[string]any{"file_path": path, "paths": []string{path}},
					eligible: true,
				},
				{name: "selected_relative", body: map[string]any{"file_path": "relative/path"}, want: "relative/path"},
				{name: "unselected_mentions_root", body: map[string]any{"other": path}},
				{name: "no_path_at_all", body: map[string]any{}},
			}
			for _, fixture := range bodies {
				encoded, err := json.Marshal(fixture.body)
				if err != nil {
					t.Fatalf("encode corpus argument body: %v", err)
				}
				for _, authority := range []struct {
					name  string
					build func(string) *aipapi.Call
				}{
					{name: "item_authority", build: attemptItemCall},
					{name: "legacy_message_parts", build: attemptLegacyCall},
				} {
					transform := outbound.NewAttemptTransform(mode, attemptResolver(t))
					call := authority.build(string(encoded))
					if err := call.Validate(); err != nil {
						t.Fatalf("fixture call must be canonical: %v", err)
					}
					meta := attemptWorkspace()
					meta.Workspace.ProjectRoot = root
					ingress := attemptMarshal(t, *call)

					if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
						t.Fatalf("mode=%v flavor fixture failed: %v", mode, err)
					}
					if err := call.Validate(); err != nil {
						t.Fatalf("requirements.md 8.5 - mode=%v produced a non-canonical call: %v", mode, err)
					}
					// Applying the pass twice must be a no-op the second time.
					published := attemptMarshal(t, *call)
					if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
						t.Fatalf("reapplication failed: %v", err)
					}
					if second := attemptMarshal(t, *call); second != published {
						t.Fatalf("requirements.md 2.9 - reapplication was not idempotent")
					}
					if mode == rewrite.ModeAudit && published != ingress {
						t.Fatalf("requirements.md 7.3 - audit mode must publish nothing")
					}
					got := attemptSelectedPath(t, call)
					switch {
					case mode != rewrite.ModeRewrite:
						// Audit published nothing, so the ingress value must survive.
						if want := attemptSelectedPathOfIngress(t, ingress); got != want {
							t.Fatalf("%s body=%s: audit mode changed the selected value from %q to %q",
								authority.name, fixture.name, want, got)
						}
					case fixture.eligible:
						if got == path || !strings.Contains(got, ".__aip_v1__") {
							t.Fatalf("%s body=%s: the selected path-bearing field must reach the backend as a reserved V1 alias",
								authority.name, fixture.name)
						}
					default:
						if got != fixture.want {
							t.Fatalf("%s body=%s: selected value = %q, want %q: nothing here is a workspace path",
								authority.name, fixture.name, got, fixture.want)
						}
					}
				}
			}
		}
	}
}

// attemptSelectedLeaf returns the raw JSON value of the single selected /file_path
// leaf of the call's historical tool call. It is nil when the call has no such
// surface, when the argument document's root is not an object, or when the leaf is
// absent, which are three different shapes the adversarial probes must be able to
// distinguish.
func attemptSelectedLeaf(t *testing.T, call *aipapi.Call) json.RawMessage {
	t.Helper()

	document := attemptToolCallDocument(call)
	if len(document) == 0 {
		return nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(document, &decoded); err != nil {
		return nil
	}
	return decoded["file_path"]
}

// attemptSelectedPath returns the decoded string value of the selected /file_path
// leaf, or the empty string when the leaf is absent or is not a string. It reads the
// published canonical bytes, so it observes what the backend would receive rather than
// what this package believes it wrote.
func attemptSelectedPath(t *testing.T, call *aipapi.Call) string {
	t.Helper()

	leaf := attemptSelectedLeaf(t, call)
	if len(leaf) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(leaf, &value); err != nil {
		// A refused non-string leaf is a legitimate published state, not a broken
		// fixture, so the shape is reported as "no string selected value".
		return ""
	}
	return value
}

// attemptSelectedPathOfIngress is attemptSelectedPath over a marshalled ingress call,
// used to prove that audit mode changed nothing rather than to state an expectation.
func attemptSelectedPathOfIngress(t *testing.T, marshalled string) string {
	t.Helper()

	var call aipapi.Call
	if err := json.Unmarshal([]byte(marshalled), &call); err != nil {
		t.Fatalf("decode ingress call: %v", err)
	}
	return attemptSelectedPath(t, &call)
}

// attemptToolCallDocument returns the raw argument document of the call's first
// historical tool call, over whichever authority the call uses.
func attemptToolCallDocument(call *aipapi.Call) []byte {
	if call.HasItemAuthority() {
		for i := range call.Items {
			if call.Items[i].ToolCall != nil {
				return call.Items[i].ToolCall.Arguments
			}
		}
		return nil
	}
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartJSON && part.ToolName == attemptTool {
				return part.Content
			}
		}
	}
	return nil
}

// concurrentRecorder is a race-safe report sink for the concurrency test.
type concurrentRecorder struct {
	mu       sync.Mutex
	reported int
}

func (r *concurrentRecorder) record(outbound.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reported++
}

func (r *concurrentRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reported
}

// mutatingErroringTransform is a stand-in pass that mutates the call and then
// returns an error, so the stage's own rollback and fail-open path can be observed
// through the real extension point.
type mutatingErroringTransform struct {
	err error
}

func (*mutatingErroringTransform) ID() string                        { return "path-virtualization-erroring-stand-in" }
func (*mutatingErroringTransform) Order() int                        { return 0 }
func (*mutatingErroringTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (e *mutatingErroringTransform) HandleAttempt(_ context.Context, call *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	call.Items[1].ToolCall.Arguments = json.RawMessage(`{"file_path":"/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"}`)
	return request.AttemptDecision{}, e.err
}

// orderingTransform is a stand-in participant used only to observe the stage's
// sort rule.
type orderingTransform struct {
	id    string
	order int
}

func (o *orderingTransform) ID() string                      { return o.id }
func (o *orderingTransform) Order() int                      { return o.order }
func (*orderingTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (*orderingTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// panicTransform is a stand-in pass that panics, to observe the stage's recovery
// and fail-open decision for a fail-open participant.
type panicTransform struct{}

func (*panicTransform) ID() string                        { return "path-virtualization-panic-stand-in" }
func (*panicTransform) Order() int                        { return 0 }
func (*panicTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (*panicTransform) HandleAttempt(_ context.Context, call *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	// Mutating before the panic is what makes the rollback assertion live: a stand-in
	// that touched nothing would leave a stage that never rolled back indistinguishable
	// from one that did.
	call.Items[1].ToolCall.Arguments = json.RawMessage(`{"file_path":"/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"}`)
	panic("stand-in pass panic")
}

package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 1.2. Requirements 4.1, 4.2, 5.1 and
// design.md sections "Existing Architecture and Placement" (steps 11/12:
// response tool-call assembly/finalization then tool policies/reactors;
// "model->client expansion uses completed tool-call finalization, not raw
// ToolCallArgsDelta mutation"; "expansion completes before existing tool
// policy/reactors"), "Completed Tool-Call Finalizer Metadata" (additive
// toolcall.Meta Workspace view populated from the same authoritative request
// views already used for tool policy/reactor metadata), "Path Expansion
// Finalizer" (steps 1/4/7/11), and "Testing Strategy" ("Completed model call
// expands before tool policy observer").
//
// THIS IS NOW A PERMANENT GREEN REGRESSION TEST. It was delivered as a
// deliberately RED characterization (the file used to be named
// tool_call_path_expansion_ordering_red_test.go and the function carried a
// TestRED_ prefix) because a completed tool-call finalizer had no authoritative
// workspace view and could therefore neither derive the real project root nor
// expand a model-emitted alias. Task 6.2 made the runtime populate
// toolcall.Meta.Workspace from the same frozen authoritative request views the
// tool policy and tool reactor planes already read, which closed that gap, and
// the RED condition this file existed to characterize ended there. Every
// assertion below is preserved in substance; only the reflection probe that
// stood in for the not-yet-existing SDK contract was replaced by the direct
// field access it stood in for (Task 8.2's obligation, tasks.md Implementation
// Notes), and the RED labels were dropped from the three messages that carried
// them. A file named *_red_test.go denotes a deliberately failing
// characterization in this repository, so keeping that name for a permanently
// passing guard would misreport its own status; the two-pass characterization
// and the request-part-hook regression in this package are the precedent for
// that naming.
//
// The composition claims here are about RUNTIME ordering and metadata delivery,
// so the expansion pass stays a deliberately minimal test-local finalizer whose
// whole behavior is visible in this file. The claims about the CONCRETE feature
// - the real shipped expansion finalizer, the real outbound attempt transform and
// request-part hook, and the client release - are proved in
// path_virtualization_expansion_before_policy_test.go and
// path_virtualization_expansion_fail_closed_test.go.
//
// The test drives one model-emitted tool call whose path-bearing argument
// arrives as stream deltas cut through the fixed V1 reserved alias and the
// surrounding JSON tokens, then observes two existing extension points only:
// the tool-call finalization plane and the tool policy plane. No raw
// ToolCallArgsDelta byte is ever rewritten here; the expansion pass works
// exclusively on the completed argument document the assembler hands to a
// finalizer, and the policy observer only joins the argument fragments it was
// shown, because the alias exists only across fragment boundaries.
//
// The gap this used to pin, and still pins: expansion must happen on completed
// valid JSON inside finalization, and the downstream tool policy observer must
// be handed the real path rather than the reserved alias. requirements.md 5.1
// requires policy evaluation only after expansion has produced the real
// filesystem path; 4.1 requires expansion before the client-facing argument
// event is released; 4.2 requires expansion on completed valid JSON rather than
// per-fragment.

const (
	// orderingRealRoot is the authoritative client-visible workspace project root
	// published by the workspace resolver. It is a fixed test fixture value.
	orderingRealRoot = "/home/dev/workspaces/aip-path-virtualization-worktree"

	// orderingVirtualRoot is the fixed V1 reserved alias form (POSIX flavor plus a
	// 20-character workspace tag) a backend may emit back to the proxy.
	orderingVirtualRoot = "/.__aip_v1__/w_0123456789abcdefghij/"

	orderingToolName   = "read_file"
	orderingToolCallID = "ordering-1"
)

// orderingArgsDocument is the complete model-emitted argument document: one
// path-bearing value carrying the reserved V1 alias and one non-path argument
// that expansion must preserve byte-for-byte.
func orderingArgsDocument() string {
	return `{"path":"` + orderingVirtualRoot + `src/main.go","limit":10}`
}

// orderingExpandedArgsDocument is the document the policy and client planes must
// observe once the alias has been expanded to the authoritative project root.
func orderingExpandedArgsDocument() string {
	return strings.Replace(orderingArgsDocument(), orderingVirtualRoot, orderingRealRoot+"/", 1)
}

// orderingArgsFragments splits orderingArgsDocument into stream-shaped deltas.
// The first cut lands inside the reserved alias's 20-character workspace tag and
// the second lands inside the path-bearing JSON string value, so no single
// fragment matches the alias and no fragment is independently valid JSON. Any
// observation of the alias therefore has to come from the completed document,
// never from a raw fragment.
func orderingArgsFragments(t *testing.T) []string {
	t.Helper()
	doc := orderingArgsDocument()
	aliasAt := strings.Index(doc, orderingVirtualRoot)
	if aliasAt < 0 {
		t.Fatal("fixture document must contain the reserved virtual alias")
	}
	const (
		intoTag    = len("/.__aip_v1__/w_") + 4
		intoSuffix = len("src/ma")
	)
	fragments := []string{
		doc[:aliasAt+intoTag],
		doc[aliasAt+intoTag : aliasAt+len(orderingVirtualRoot)+intoSuffix],
		doc[aliasAt+len(orderingVirtualRoot)+intoSuffix:],
	}
	if joined := strings.Join(fragments, ""); joined != doc {
		t.Fatalf("fragments must reconstruct the document: got %d bytes want %d", len(joined), len(doc))
	}
	for i, fragment := range fragments {
		if strings.Contains(fragment, orderingVirtualRoot) {
			t.Fatalf("fragment %d must not contain the whole reserved alias", i)
		}
		if json.Valid([]byte(fragment)) {
			t.Fatalf("fragment %d must not be independently valid JSON", i)
		}
	}
	return fragments
}

// orderingWorkspaceResolver publishes the authoritative project root the same way
// a production resolver would, so the tool policy plane and, after Task 6.1, the
// tool-call finalization plane read one and the same authoritative view.
type orderingWorkspaceResolver struct{ root string }

func (r orderingWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: r.root}, nil
}

// orderingStage is the single monotonic sequence shared by the finalizer and the
// tool policy observer, so the test can compare when each stage ran without
// inspecting any private call-graph trivia.
type orderingStage struct {
	mu         sync.Mutex
	seq        int
	finishAt   int
	policyAt   int
	policySeen int
}

func (s *orderingStage) recordFinalizer() {
	s.mu.Lock()
	s.finishAt = s.nextLocked()
	s.mu.Unlock()
}

func (s *orderingStage) recordPolicy(argsDelta bool) {
	s.mu.Lock()
	if argsDelta {
		if s.policyAt == 0 {
			s.policyAt = s.nextLocked()
		}
		s.policySeen++
	}
	s.mu.Unlock()
}

func (s *orderingStage) nextLocked() int {
	s.seq++
	return s.seq
}

func (s *orderingStage) snapshot() (finishAt, policyAt, policySeen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishAt, s.policyAt, s.policySeen
}

// orderingExpansionFinalizer is the minimal completed-call expansion pass this
// file uses to keep its claims about the RUNTIME's ordering and metadata
// delivery, independent of the concrete feature's selector and mapping policy.
// It expands the reserved V1 alias back to the authoritative project root inside
// the COMPLETED argument document the assembler supplies (never a raw stream
// delta), and it may only use the generic tool-call finalization metadata as
// authority. design.md "Path Expansion Finalizer" step 1 reads exactly this field.
//
// The REAL shipped pass, the one the composition claims are about, is driven
// end to end in path_virtualization_expansion_before_policy_test.go.
type orderingExpansionFinalizer struct {
	stage *orderingStage

	mu          sync.Mutex
	calls       int
	completed   []byte
	rootPresent bool
	identBound  bool
}

func (*orderingExpansionFinalizer) ID() string { return "path-expansion" }

func (*orderingExpansionFinalizer) Order() int { return 0 }

func (f *orderingExpansionFinalizer) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	f.stage.recordFinalizer()

	// Direct field access, replacing the reflection probe this file used to
	// carry. The probe reached meta.Workspace.ProjectRoot through
	// reflect.FieldByName so that the file compiled before Task 6.1 added the
	// field, and it returned ("", false) for the whole period the runtime left
	// the field unpopulated. Task 6.2 populates it, so the probe now reads the
	// same value the reflection read, and the access is a compile-time-checked
	// field selection instead of a by-name lookup that would silently report
	// "absent" if the field were ever renamed or retyped. The two normalizations
	// the probe performed are preserved exactly: the root is whitespace-trimmed,
	// and a trimmed-empty root counts as absent.
	root := strings.TrimSpace(meta.Workspace.ProjectRoot)
	rootPresent := root != ""

	f.mu.Lock()
	f.calls++
	f.completed = append(f.completed[:0], call.ArgsJSON...)
	f.rootPresent = rootPresent
	f.identBound = strings.TrimSpace(meta.TraceID) != "" || strings.TrimSpace(meta.ALegID) != "" ||
		strings.TrimSpace(meta.BLegID) != ""
	f.mu.Unlock()

	if !rootPresent || !bytes.Contains(call.ArgsJSON, []byte(orderingVirtualRoot)) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	expanded := bytes.Replace(call.ArgsJSON, []byte(orderingVirtualRoot), []byte(root+"/"), 1)
	if !json.Valid(expanded) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   expanded,
		ReasonCode: toolcall.ReasonValidPassThrough,
	}, nil
}

func (f *orderingExpansionFinalizer) snapshot() (int, []byte, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]byte(nil), f.completed...), f.rootPresent, f.identBound
}

// orderingPathObserver is the existing tool policy extension point standing in for
// the path/security policy evaluation that must observe the real filesystem path.
// It records only the observation order, the joined argument bytes it was shown,
// and whether it received the authoritative project root.
type orderingPathObserver struct {
	stage *orderingStage

	mu         sync.Mutex
	calls      int
	joined     string
	policyRoot bool
}

func (o *orderingPathObserver) ID() string                        { return "path-security-observer" }
func (o *orderingPathObserver) Order() int                        { return 0 }
func (o *orderingPathObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *orderingPathObserver) Handle(
	_ context.Context,
	ev aipapi.ToolEvent,
	meta toolpolicy.Meta,
	_ toolpolicy.Services,
) (toolpolicy.Decision, error) {
	o.stage.recordPolicy(ev.ArgsDelta != "")

	o.mu.Lock()
	o.calls++
	if strings.TrimSpace(meta.Workspace.ProjectRoot) != "" {
		o.policyRoot = true
	}
	// Arguments are joined in observation order so the alias can only be
	// detected across fragment boundaries, exactly as the backend streamed it.
	o.joined += ev.ArgsDelta
	o.mu.Unlock()
	return toolpolicy.DecisionAllow, nil
}

func (o *orderingPathObserver) snapshot() (int, string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.joined, o.policyRoot
}

// TestStreamToolCall_PathExpansionPrecedesToolPolicyEvaluation proves that
// the completed tool-call finalization plane, not the raw stream deltas, is the
// place where a model-emitted reserved alias can be expanded, and that the tool
// policy plane observes the expanded real path afterwards.
//
// It was delivered RED: a finalizer received no authoritative workspace view, so
// the alias survived into policy evaluation and into the client-facing release.
// Task 6.2 populated that view, which is what turned it green. It is kept as a
// permanent guard because both halves are still exactly the properties
// requirements.md 4.1, 4.2, and 5.1 demand, and neither is observable from the
// feature's own tests.
func TestStreamToolCall_PathExpansionPrecedesToolPolicyEvaluation(t *testing.T) {
	t.Parallel()

	stage := &orderingStage{}
	fin := &orderingExpansionFinalizer{stage: stage}
	observer := &orderingPathObserver{stage: stage}

	backendEvents := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: orderingToolCallID, ToolName: orderingToolName},
	}
	for _, fragment := range orderingArgsFragments(t) {
		backendEvents = append(backendEvents, aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: orderingToolCallID,
			ToolName:   orderingToolName,
			Delta:      fragment,
		})
	}
	backendEvents = append(backendEvents,
		aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: orderingToolCallID, ToolName: orderingToolName},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)

	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, aipapi.NewFixedEventStream(backendEvents)),
	}, extensions.SnapshotOptions{
		Workspace: orderingWorkspaceResolver{root: orderingRealRoot},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies: []toolpolicy.Policy{observer},
		}),
	})
	// A zero bound selects the runtime's shared finalization cap; this
	// characterization is about ordering and metadata, not about that cap.
	ex.SetToolCallFinalizers([]toolcall.Finalizer{fin}, 0)

	call := pdBaseCall("openai:gpt-4")
	call.Tools = []aipapi.ToolDef{{
		Name:       orderingToolName,
		Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}}}`),
	}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}

	stream, err := ex.Execute(principalCtx("path-virtualization-ordering"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	events := tcrCollect(t, stream)
	_ = stream.Close()

	released := strings.Builder{}
	for _, ev := range tcrToolLifecycle(events) {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			released.WriteString(ev.Delta)
		}
	}

	finCalls, finCompleted, finRootPresent, finIdentBound := fin.snapshot()
	observerCalls, observerJoined, observerRoot := observer.snapshot()
	finishAt, policyAt, policyArgsSeen := stage.snapshot()

	// Scaffolding guards: every failure below must be attributable to the
	// metadata/ordering gap, not to a fixture that never reached the observers.
	if finCalls < 1 {
		t.Fatalf("completed tool call must reach tool-call finalization: invocations=%d", finCalls)
	}
	if observerCalls < 1 {
		t.Fatalf("tool policy plane must observe the completed tool call: observations=%d", observerCalls)
	}
	if !observerRoot {
		t.Fatal("tool policy must receive the authoritative workspace project root, otherwise this fixture cannot isolate the finalization metadata gap")
	}

	t.Run("finalization_metadata_carries_authoritative_workspace", func(t *testing.T) {
		t.Parallel()
		if !finRootPresent {
			t.Fatalf("requirements.md 4.1/5.1 and design.md \"Completed Tool-Call Finalizer Metadata\" - tool-call finalization metadata must carry the authoritative workspace project root so a finalizer can expand a model-emitted reserved alias before policy evaluation; workspace_root_reachable=%t trace_identity_bound=%t",
				finRootPresent, finIdentBound)
		}
	})

	t.Run("expansion_operates_on_completed_argument_json", func(t *testing.T) {
		t.Parallel()
		// Control for requirements.md 4.2: expansion input is the single completed
		// document, never an independent raw fragment. This also proves the RED
		// subtests above are not caused by a fixture the finalizer cannot see.
		if finCalls != 1 {
			t.Fatalf("one completed tool call must produce exactly one finalization pass, got %d", finCalls)
		}
		if !json.Valid(finCompleted) {
			t.Fatalf("finalizer must receive one complete valid JSON argument document, got %d bytes", len(finCompleted))
		}
		if string(finCompleted) != orderingArgsDocument() {
			t.Fatalf("finalizer must receive the streamed fragments reassembled into the complete document: got %d bytes want %d",
				len(finCompleted), len(orderingArgsDocument()))
		}
		if policyArgsSeen < 1 {
			t.Fatalf("tool policy must observe at least one argument event, got %d", policyArgsSeen)
		}
	})

	t.Run("tool_policy_observes_real_path_not_reserved_alias", func(t *testing.T) {
		t.Parallel()
		aliasSeen := strings.Contains(observerJoined, orderingVirtualRoot)
		realSeen := strings.Contains(observerJoined, orderingRealRoot)
		if aliasSeen || !realSeen {
			t.Fatalf("requirements.md 5.1/4.3 - tool policy evaluation must run only after required expansion has produced the real filesystem path; policy observed %d argument bytes with reserved_alias=%t real_root=%t valid_json=%t non_path_argument_preserved=%t",
				len(observerJoined), aliasSeen, realSeen, json.Valid([]byte(observerJoined)),
				strings.Contains(observerJoined, `"limit":10`))
		}
		if !json.Valid([]byte(observerJoined)) {
			t.Fatalf("requirements.md 4.8 - expansion must preserve JSON validity for the observer: got %d bytes", len(observerJoined))
		}
		if !strings.Contains(observerJoined, `"limit":10`) {
			t.Fatalf("requirements.md 4.8 - expansion must preserve non-path argument fields: got %d argument bytes", len(observerJoined))
		}
	})

	t.Run("expansion_completes_before_policy_observation", func(t *testing.T) {
		t.Parallel()
		if policyAt == 0 {
			t.Fatalf("tool policy must observe an argument event for the completed call, argument_events=%d", policyArgsSeen)
		}
		if finishAt >= policyAt {
			t.Fatalf("requirements.md 5.1 - expansion must complete before tool policy evaluation observes the argument value; finalization_stage=%d policy_stage=%d",
				finishAt, policyAt)
		}
	})

	t.Run("client_facing_arguments_released_expanded", func(t *testing.T) {
		t.Parallel()
		releasedArgs := released.String()
		aliasReleased := strings.Contains(releasedArgs, orderingVirtualRoot)
		realReleased := strings.Contains(releasedArgs, orderingRealRoot)
		if aliasReleased || !realReleased {
			t.Fatalf("requirements.md 4.1 - the reserved virtual alias must be expanded to the real project root before the client-facing tool-call argument event is released; released %d argument bytes with reserved_alias=%t real_root=%t valid_json=%t",
				len(releasedArgs), aliasReleased, realReleased, json.Valid([]byte(releasedArgs)))
		}
		if releasedArgs != orderingExpandedArgsDocument() {
			t.Fatalf("requirements.md 4.8 - the released argument document must be the expanded complete document: got %d bytes want %d",
				len(releasedArgs), len(orderingExpandedArgsDocument()))
		}
	})
}

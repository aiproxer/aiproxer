package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 6.2. Requirements 4.1, 5.1, 5.7 and
// design.md "5. Completed Tool-Call Finalizer Metadata".
//
// This is the producer-side half of the proof. The end-to-end file
// tool_call_finalization_meta_test.go drives a real request through the executor
// and shows a finalizer's in-place writes stay inside the finalizer; that file
// deliberately observes only exported behavior. This file covers the two things
// it cannot reach without package-internal access:
//
//   - the accessor the runtime actually uses, recvTurnFacts.viewsFor, hands out
//     an independent deep copy on every call, so detachment is a property of the
//     producer and not of any particular consumer;
//   - the population site in responsePipeline.prepareRecvEvent leaves the
//     additive toolcall.Meta views at their zero value when the frozen facts
//     report no authoritative request views, instead of inventing one or failing
//     the request.
//
// No assertion message prints a path, identity, or label value: failures report
// booleans, counts, and lengths only.

const (
	fcmiScopeLabelKey   = "synthetic_scope_label"
	fcmiScopeLabelValue = "synthetic-scope-label-value"
	fcmiSessionLabelKey = "synthetic_session_label"
	fcmiWorkspaceKey    = "synthetic_workspace_label"
	fcmiMarker          = "synthetic-workspace-marker"
	fcmiRole            = "synthetic-role"

	fcmiToolName   = "fcmi_tool"
	fcmiToolCallID = "fcmi-1"
	fcmiToolArgs   = `{"synthetic":"value"}`

	// fcmiAttemptSeq is the attempt sequence the fixture's B-leg record carries,
	// so the test can prove the pre-existing sequence field passes through
	// untouched instead of merely being non-negative.
	fcmiAttemptSeq = 3
)

// fcmiMutationSentinel is the marker a consumer writes into every payload it is
// handed, standing in for a misbehaving tool-call finalizer.
const fcmiMutationSentinel = "fcmi_mutation_sentinel"

// fcmiViews builds the authoritative request snapshot a real request yields.
// Values are synthetic and never real paths or identity material.
func fcmiViews() execctx.Views {
	return execctx.Views{
		Scope: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectService,
			PrincipalID:  scope.Known("synthetic-principal"),
			Roles:        []string{fcmiRole},
			SafeClaims:   map[string]string{fcmiScopeLabelKey: fcmiScopeLabelValue},
			PolicyLabels: map[string]string{fcmiScopeLabelKey: fcmiScopeLabelValue},
		},
		Session: session.SessionView{
			AuthoritativeSessionID: "synthetic-session",
			Labels:                 map[string]string{fcmiSessionLabelKey: fcmiScopeLabelValue},
		},
		Workspace: workspace.WorkspaceView{
			ID:          "synthetic-workspace-id",
			ProjectRoot: "synthetic-project-root",
			Markers:     []string{fcmiMarker},
			Labels:      map[string]string{fcmiWorkspaceKey: fcmiScopeLabelValue},
		},
	}
}

// fcmiFacts is one request's frozen facts carrying the authoritative snapshot.
func fcmiFacts() recvTurnFacts {
	return testRecvTurnFacts(recvTurnFacts{
		traceID:     "synthetic-trace",
		aLegID:      "synthetic-a-leg",
		recvViews:   fcmiViews(),
		recvViewsOK: true,
	})
}

// fcmiSentinelCount counts how many consumer-written markers a snapshot carries.
func fcmiSentinelCount(v execctx.Views) int {
	found := 0
	for _, m := range []map[string]string{v.Scope.SafeClaims, v.Scope.PolicyLabels, v.Session.Labels, v.Workspace.Labels} {
		if _, ok := m[fcmiMutationSentinel]; ok {
			found++
		}
	}
	for _, role := range v.Scope.Roles {
		if role == fcmiMutationSentinel {
			found++
		}
	}
	for _, marker := range v.Workspace.Markers {
		if marker == fcmiMutationSentinel {
			found++
		}
	}
	return found
}

// fcmiWritableFieldCount counts the reference-typed payloads a snapshot exposes.
func fcmiWritableFieldCount(v execctx.Views) int {
	n := 0
	for _, m := range []map[string]string{v.Scope.SafeClaims, v.Scope.PolicyLabels, v.Session.Labels, v.Workspace.Labels} {
		if m != nil {
			n++
		}
	}
	if len(v.Scope.Roles) > 0 {
		n++
	}
	if len(v.Workspace.Markers) > 0 {
		n++
	}
	return n
}

// fcmiWriteEveryField writes the mutation marker in place through every
// reference-typed payload of a consumer's view triple.
func fcmiWriteEveryField(v execctx.Views) {
	if v.Scope.SafeClaims != nil {
		v.Scope.SafeClaims[fcmiMutationSentinel] = fcmiMutationSentinel
	}
	if v.Scope.PolicyLabels != nil {
		v.Scope.PolicyLabels[fcmiMutationSentinel] = fcmiMutationSentinel
	}
	if len(v.Scope.Roles) > 0 {
		v.Scope.Roles[0] = fcmiMutationSentinel
	}
	if v.Session.Labels != nil {
		v.Session.Labels[fcmiMutationSentinel] = fcmiMutationSentinel
	}
	if v.Workspace.Labels != nil {
		v.Workspace.Labels[fcmiMutationSentinel] = fcmiMutationSentinel
	}
	if len(v.Workspace.Markers) > 0 {
		v.Workspace.Markers[0] = fcmiMutationSentinel
	}
}

// TestRecvTurnFacts_ViewsForHandsOutADetachedCopyPerCall is the producer-side
// detachment proof for design.md "5. Completed Tool-Call Finalizer Metadata":
// viewsFor is the single accessor the response pipeline uses for every tool
// plane, and each call returns an independent deep copy of the frozen facts. A
// consumer that writes in place therefore cannot reach the producer snapshot, and
// a later consumer gets the pristine state again.
//
// The non-vacuity guard is the landed-write count: if the fixture exposed nothing
// mutable, or if the handed copy were not the consumer's own, the writes would
// not be observable on it and the isolation assertions below would prove nothing.
func TestRecvTurnFacts_ViewsForHandsOutADetachedCopyPerCall(t *testing.T) {
	t.Parallel()

	facts := fcmiFacts()
	want := fcmiViews()
	writable := fcmiWritableFieldCount(want)
	if writable != 6 {
		t.Fatalf("fixture guard: every reference-typed payload must be populated for this proof to mean anything: writable_payloads=%d", writable)
	}

	first, ok := facts.viewsFor(context.Background())
	if !ok {
		t.Fatal("frozen request views must be reported as present")
	}
	fcmiWriteEveryField(first)
	if got := fcmiSentinelCount(first); got != writable {
		t.Fatalf("non-vacuity guard: the consumer's in-place writes must land on the copy it was handed: writable_payloads=%d sentinel_payloads=%d", writable, got)
	}

	// The producer snapshot is untouched by the consumer's writes.
	if got := fcmiSentinelCount(facts.recvViews); got != 0 {
		t.Errorf("a consumer's in-place writes must not reach the producer snapshot (sentinel_payloads=%d)", got)
	}
	if !reflect.DeepEqual(facts.recvViews, want) {
		t.Error("a consumer's in-place writes must not change any producer view field")
	}

	// A later consumer of the same request sees pristine state again.
	second, ok := facts.viewsFor(context.Background())
	if !ok {
		t.Fatal("frozen request views must remain present for every consumer")
	}
	if got := fcmiSentinelCount(second); got != 0 {
		t.Errorf("a later consumer must receive a pristine copy, not the previous consumer's mutated state (sentinel_payloads=%d)", got)
	}
	if !reflect.DeepEqual(second, want) {
		t.Error("every consumer must observe the same authoritative request state by value")
	}

	// Two consumers of the same request do not share payloads.
	if reflect.ValueOf(first.Workspace.Labels).Pointer() == reflect.ValueOf(second.Workspace.Labels).Pointer() {
		t.Error("each consumer must receive its own workspace label map")
	}
}

// fcmiMetadataProbe records what one completed-tool-call finalization pass was
// handed. It is a real toolcall.Finalizer driven by the real assembler, so the
// population site under test is the production one.
type fcmiMetadataProbe struct {
	calls int

	scopePresent     bool
	sessionPresent   bool
	workspacePresent bool
	writablePayloads int
	sentinelPayloads int
	traceIDsPresent  bool
	attemptSeq       int
}

func (*fcmiMetadataProbe) ID() string { return "fcmi-metadata-probe" }

func (*fcmiMetadataProbe) Order() int { return 0 }

func (f *fcmiMetadataProbe) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	f.scopePresent = meta.Scope.SubjectKind != "" || meta.Scope.PrincipalID.String() != "" ||
		meta.Scope.WorkspaceID.String() != "" || len(meta.Scope.Roles) > 0 ||
		len(meta.Scope.SafeClaims) > 0 || len(meta.Scope.PolicyLabels) > 0 || meta.Scope.Origin != ""
	f.sessionPresent = meta.Session.AuthoritativeSessionID != "" || meta.Session.ClientSessionHint != "" ||
		meta.Session.ALegID != "" || meta.Session.IsNew || meta.Session.WorkspaceID != "" ||
		meta.Session.ResumeEligible || len(meta.Session.Labels) > 0 || meta.Session.TurnID != ""
	f.workspacePresent = meta.Workspace.ID != "" || meta.Workspace.ProjectRoot != "" ||
		meta.Workspace.DirtyTree || len(meta.Workspace.Markers) > 0 || len(meta.Workspace.Labels) > 0
	f.traceIDsPresent = meta.TraceID != "" && meta.ALegID != "" && meta.BLegID != ""
	f.attemptSeq = meta.AttemptSeq

	// Count the reference-typed payloads this metadata actually exposes, and how
	// many of them accept an in-place write. Together they certify that the
	// populated case below is not passing because the fixture was empty.
	before := execctx.Views{
		Scope:     meta.Scope,
		Session:   meta.Session,
		Workspace: meta.Workspace,
	}
	f.writablePayloads = fcmiWritableFieldCount(before)
	fcmiWriteEveryField(before)
	f.sentinelPayloads = fcmiSentinelCount(before)

	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// fcmiDriveFinalization runs one complete tool-call lifecycle through the real
// response pipeline population site. It reports whether the pipeline held any of
// those events for finalization and how many it turned into an error, so a caller
// can distinguish "no metadata" from "no finalization happened".
func fcmiDriveFinalization(t *testing.T, facts recvTurnFacts, probe *fcmiMetadataProbe) (swallowed bool, failures int) {
	t.Helper()

	assembler := newToolCallAssembler([]toolcall.Finalizer{probe}, 0, []aipapi.ToolDef{{Name: fcmiToolName}})
	if assembler == nil || !assembler.enabled() {
		t.Fatal("fixture guard: the assembler must be active for a tool catalog and a finalizer")
	}
	attempt := &attemptSession{
		toolFinal: assembler,
		bleg:      b2bua.BLegRecord{BLegID: "synthetic-b-leg", Seq: fcmiAttemptSeq},
	}
	pipeline := newResponsePipeline()

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: fcmiToolCallID, ToolName: fcmiToolName},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: fcmiToolCallID, ToolName: fcmiToolName, Delta: fcmiToolArgs},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: fcmiToolCallID, ToolName: fcmiToolName},
	} {
		prepared := pipeline.prepareRecvEvent(context.Background(), facts, attempt, ev, nil)
		if prepared.err != nil {
			failures++
		}
		swallowed = swallowed || prepared.swallowed
	}
	return swallowed, failures
}

// TestPrepareRecvEvent_PresentRequestViewsReachFinalizerMetadata is the positive
// control for the population site: a request whose frozen facts carry
// authoritative views hands them to the finalizer, and every reference-typed
// payload it hands is populated and writable. It certifies that the assertions in
// TestPrepareRecvEvent_AbsentRequestViewsLeaveFinalizerMetadataZero are observing
// a populated-when-populated site rather than a site that never carries anything.
func TestPrepareRecvEvent_PresentRequestViewsReachFinalizerMetadata(t *testing.T) {
	t.Parallel()

	probe := &fcmiMetadataProbe{}
	swallowed, failures := fcmiDriveFinalization(t, fcmiFacts(), probe)

	if failures != 0 {
		t.Fatalf("a complete tool call must finalize without error: pipeline_error_events=%d", failures)
	}
	if !swallowed {
		t.Fatal("fixture guard: the assembler must hold the tool lifecycle events for finalization")
	}
	if probe.calls != 1 {
		t.Fatalf("one completed tool call must produce exactly one finalization pass: invocations=%d", probe.calls)
	}
	if !probe.scopePresent || !probe.sessionPresent || !probe.workspacePresent {
		t.Errorf("finalization metadata must carry the authoritative request views: scope=%t session=%t workspace=%t",
			probe.scopePresent, probe.sessionPresent, probe.workspacePresent)
	}
	if probe.writablePayloads != 6 {
		t.Errorf("non-vacuity guard: every reference-typed payload must be reachable through the metadata: writable_payloads=%d", probe.writablePayloads)
	}
	if probe.sentinelPayloads != probe.writablePayloads {
		t.Errorf("non-vacuity guard: an in-place write must land on every payload the metadata exposes: writable_payloads=%d sentinel_payloads=%d",
			probe.writablePayloads, probe.sentinelPayloads)
	}
	if !probe.traceIDsPresent {
		t.Error("the pre-existing trace identity fields must reach finalization alongside the additive views")
	}
	if probe.attemptSeq != fcmiAttemptSeq {
		t.Errorf("the pre-existing attempt sequence must pass through unchanged: attempt_seq=%d", probe.attemptSeq)
	}
}

// TestPrepareRecvEvent_AbsentRequestViewsLeaveFinalizerMetadataZero pins the
// ok-guard semantics of the population site. recvTurnFacts.viewsFor reports
// absence for a request whose frozen facts carry no view snapshot; that absence
// is a supported state, not a failure, so the runtime must neither invent views
// for it nor fail the request over it. The additive views therefore stay at their
// zero value - the documented "no authoritative view" state that preserves the
// pre-existing local/anonymous identity semantics - while the pre-existing trace
// identity fields still reach the finalizer.
func TestPrepareRecvEvent_AbsentRequestViewsLeaveFinalizerMetadataZero(t *testing.T) {
	t.Parallel()

	facts := testRecvTurnFacts(recvTurnFacts{traceID: "synthetic-trace", aLegID: "synthetic-a-leg"})
	if _, ok := facts.viewsFor(context.Background()); ok {
		t.Fatal("fixture guard: these facts must report the request views as absent")
	}

	probe := &fcmiMetadataProbe{}
	swallowed, failures := fcmiDriveFinalization(t, facts, probe)

	if failures != 0 {
		t.Fatalf("an absent request view snapshot must never fail the tool call: pipeline_error_events=%d", failures)
	}
	if !swallowed {
		t.Fatal("fixture guard: the assembler must hold the tool lifecycle events for finalization")
	}
	if probe.calls != 1 {
		t.Fatalf("one completed tool call must produce exactly one finalization pass: invocations=%d", probe.calls)
	}
	if probe.scopePresent || probe.sessionPresent || probe.workspacePresent {
		t.Errorf("absent request views must leave the additive metadata at its zero value: scope=%t session=%t workspace=%t",
			probe.scopePresent, probe.sessionPresent, probe.workspacePresent)
	}
	if probe.writablePayloads != 0 || probe.sentinelPayloads != 0 {
		t.Errorf("absent request views must expose no reference-typed payload: writable_payloads=%d sentinel_payloads=%d",
			probe.writablePayloads, probe.sentinelPayloads)
	}
	if !probe.traceIDsPresent {
		t.Error("the pre-existing trace identity fields must still reach finalization when request views are absent")
	}
	if probe.attemptSeq != fcmiAttemptSeq {
		t.Errorf("the pre-existing attempt sequence must still pass through when request views are absent: attempt_seq=%d", probe.attemptSeq)
	}
}

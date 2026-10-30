package toolcall_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 6.1. Requirements 4.1, 5.1, 6.1 and
// design.md "5. Completed Tool-Call Finalizer Metadata".
//
// toolcall.Meta now carries the same authoritative scope/session/workspace
// views hooks.ToolMeta already carries, so both planes read one snapshot. This
// file pins the two consequences the design's "Existing finalizers remain
// source compatible" and "No client-provided raw metadata becomes authority"
// sentences imply: existing finalizers keep compiling and behaving identically,
// and the view payloads crossing that boundary cannot be reached back into by
// a finalizer.

// authoritativeViews stands in for the runtime's own request snapshot: the
// producer retains it across the finalization pass and is the only owner of
// its reference-typed payloads.
type authoritativeViews struct {
	scope     scope.PrincipalScopeView
	session   session.SessionView
	workspace workspace.WorkspaceView
}

// producerViews is the proxy-validated snapshot a real request yields. Values
// are synthetic and never real paths, aliases, or identity material.
func producerViews() authoritativeViews {
	return authoritativeViews{
		scope: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectService,
			PrincipalID:  scope.Known("synthetic-principal"),
			Roles:        []string{"synthetic-role"},
			SafeClaims:   map[string]string{"synthetic_claim": "synthetic-value"},
			PolicyLabels: map[string]string{"synthetic_label": "synthetic-value"},
		},
		session: session.SessionView{
			AuthoritativeSessionID: "synthetic-session",
			Labels:                 map[string]string{"synthetic_label": "synthetic-value"},
		},
		workspace: workspace.WorkspaceView{
			ID:          "synthetic-workspace-id",
			ProjectRoot: "synthetic-project-root",
			Markers:     []string{"synthetic-marker"},
			Labels:      map[string]string{"synthetic_label": "synthetic-value"},
		},
	}
}

// finalizationMeta mirrors how the runtime must populate the additive fields:
// it hands the finalizer a view detached from the snapshot it retains, using
// the SDK's own [scope.PrincipalScopeView.Clone] for Scope. This is the same
// discipline hooks.ToolMeta already relies on.
func finalizationMeta(src authoritativeViews) toolcall.Meta {
	sessionView := src.session
	sessionView.Labels = maps.Clone(src.session.Labels)
	workspaceView := src.workspace
	workspaceView.Labels = maps.Clone(src.workspace.Labels)
	workspaceView.Markers = slices.Clone(src.workspace.Markers)
	return toolcall.Meta{
		Scope:     src.scope.Clone(),
		Session:   sessionView,
		Workspace: workspaceView,
	}
}

// sharedMeta is the deliberately wrong producer: it hands the finalizer the
// producer's own maps and slices. It exists so the isolation tests below can
// prove they would actually catch a leak instead of passing vacuously.
func sharedMeta(src authoritativeViews) toolcall.Meta {
	return toolcall.Meta{Scope: src.scope, Session: src.session, Workspace: src.workspace}
}

// mutatingFinalizer writes to every reference-typed field reachable through
// Meta, in place, and keeps a deep copy of Scope for later.
type mutatingFinalizer struct {
	keptScope scope.PrincipalScopeView
	calls     int
}

func (mutatingFinalizer) ID() string { return "meta-isolation-probe" }

func (mutatingFinalizer) Order() int { return 0 }

func (f *mutatingFinalizer) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	meta.Scope.SafeClaims["synthetic_claim"] = "finalizer-write"
	meta.Scope.PolicyLabels["synthetic_label"] = "finalizer-write"
	meta.Scope.Roles[0] = "finalizer-write"
	meta.Session.Labels["synthetic_label"] = "finalizer-write"
	meta.Workspace.Labels["synthetic_label"] = "finalizer-write"
	meta.Workspace.Markers[0] = "finalizer-write"

	f.calls++
	f.keptScope = meta.Scope.Clone()
	f.keptScope.SafeClaims["synthetic_claim"] = "finalizer-retained-write"
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// TestMeta_FinalizerCannotMutateProducerViewsThroughSharedMapOrSlice is the
// ownership-boundary proof: with the documented producer detachment, every
// in-place write a finalizer can make on the additive views is confined to the
// finalizer.
func TestMeta_FinalizerCannotMutateProducerViewsThroughSharedMapOrSlice(t *testing.T) {
	t.Parallel()

	src := producerViews()
	want := producerViews()
	meta := finalizationMeta(src)
	finalizer := &mutatingFinalizer{}

	if _, err := finalizer.Finalize(context.Background(), toolcall.CompletedCall{}, aipapi.ToolDef{}, nil, meta); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer invocations = %d, want 1", finalizer.calls)
	}

	if got, wantGot := src.scope.SafeClaims["synthetic_claim"], want.scope.SafeClaims["synthetic_claim"]; got != wantGot {
		t.Error("producer scope safe claims mutated through finalizer metadata")
	}
	if got, wantGot := src.scope.PolicyLabels["synthetic_label"], want.scope.PolicyLabels["synthetic_label"]; got != wantGot {
		t.Error("producer scope policy labels mutated through finalizer metadata")
	}
	if got, wantGot := src.scope.Roles[0], want.scope.Roles[0]; got != wantGot {
		t.Error("producer scope roles mutated through finalizer metadata")
	}
	if got, wantGot := src.session.Labels["synthetic_label"], want.session.Labels["synthetic_label"]; got != wantGot {
		t.Error("producer session labels mutated through finalizer metadata")
	}
	if got, wantGot := src.workspace.Labels["synthetic_label"], want.workspace.Labels["synthetic_label"]; got != wantGot {
		t.Error("producer workspace labels mutated through finalizer metadata")
	}
	if got, wantGot := src.workspace.Markers[0], want.workspace.Markers[0]; got != wantGot {
		t.Error("producer workspace markers mutated through finalizer metadata")
	}

	// The finalizer's own retained deep copy is independent in both directions:
	// its post-Finalize write must not reach the metadata it was handed.
	if got := finalizer.keptScope.SafeClaims["synthetic_claim"]; got != "finalizer-retained-write" {
		t.Error("finalizer must be able to retain and mutate its own cloned Scope view")
	}
	if got := meta.Scope.SafeClaims["synthetic_claim"]; got != "finalizer-write" {
		t.Error("metadata handed to the finalizer must reflect the finalizer's own writes, proving the isolation assertions above had teeth")
	}
}

// TestMeta_SharedMetadataWouldLeakIntoProducer proves the previous test is not
// vacuous: the same finalizer against non-detached metadata does reach the
// producer, which is exactly why the runtime must clone before handing views
// to a finalizer.
func TestMeta_SharedMetadataWouldLeakIntoProducer(t *testing.T) {
	t.Parallel()

	src := producerViews()
	meta := sharedMeta(src)
	finalizer := &mutatingFinalizer{}

	if _, err := finalizer.Finalize(context.Background(), toolcall.CompletedCall{}, aipapi.ToolDef{}, nil, meta); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if src.scope.SafeClaims["synthetic_claim"] == "synthetic-value" ||
		src.scope.PolicyLabels["synthetic_label"] == "synthetic-value" ||
		src.scope.Roles[0] == "synthetic-role" ||
		src.session.Labels["synthetic_label"] == "synthetic-value" ||
		src.workspace.Labels["synthetic_label"] == "synthetic-value" ||
		src.workspace.Markers[0] == "synthetic-marker" {
		t.Error("non-detached metadata must demonstrably share mutable state with the producer")
	}
}

// TestMeta_ViewReassignmentInsideFinalizerCannotReachCaller proves the
// additive fields are value views handed to Finalize by value: replacing a
// whole view inside a finalizer is invisible to the caller, exactly like the
// pre-existing trace fields.
type reassigningFinalizer struct {
	calls int
}

func (reassigningFinalizer) ID() string { return "meta-reassignment-probe" }

func (reassigningFinalizer) Order() int { return 0 }

func (f *reassigningFinalizer) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	meta.Scope = scope.PrincipalScopeView{SubjectKind: scope.SubjectLocal}
	meta.Session = session.SessionView{AuthoritativeSessionID: "finalizer-written"}
	meta.Workspace = workspace.WorkspaceView{ProjectRoot: "finalizer-written"}
	meta.TraceID = "finalizer-written"
	meta.AttemptSeq = -1
	f.calls++
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func TestMeta_ViewReassignmentInsideFinalizerCannotReachCaller(t *testing.T) {
	t.Parallel()

	src := producerViews()
	meta := finalizationMeta(src)
	meta.TraceID = "synthetic-trace"
	meta.ALegID = "synthetic-a-leg"
	meta.BLegID = "synthetic-b-leg"
	meta.AttemptSeq = 4
	finalizer := &reassigningFinalizer{}

	if _, err := finalizer.Finalize(context.Background(), toolcall.CompletedCall{}, aipapi.ToolDef{}, nil, meta); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer invocations = %d, want 1", finalizer.calls)
	}

	if meta.Scope.SubjectKind != scope.SubjectService || meta.Session.AuthoritativeSessionID != "synthetic-session" ||
		meta.Workspace.ProjectRoot != "synthetic-project-root" || meta.TraceID != "synthetic-trace" || meta.AttemptSeq != 4 {
		t.Error("Finalize receives Meta by value; local reassignment must not reach the caller")
	}
}

// TestMeta_ExistingFinalizerRemainsSourceCompatible uses the exact pre-change
// Finalize signature and the exact pre-change Meta field reads. It must keep
// satisfying toolcall.Finalizer and observing identical metadata and results
// whether or not the additive views are populated.
type legacyFinalizer struct {
	id      string
	order   int
	trace   string
	aLeg    string
	bLeg    string
	attempt int
	result  toolcall.Result
}

func (f *legacyFinalizer) ID() string { return f.id }

func (f *legacyFinalizer) Order() int { return f.order }

func (f *legacyFinalizer) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	f.trace = meta.TraceID
	f.aLeg = meta.ALegID
	f.bLeg = meta.BLegID
	f.attempt = meta.AttemptSeq
	f.result = toolcall.Result{Action: toolcall.ActionRewrite, ToolName: "synthetic-tool", ArgsJSON: []byte(`{"synthetic":true}`), ReasonCode: toolcall.ReasonValidPassThrough}
	return f.result, nil
}

var _ toolcall.Finalizer = (*legacyFinalizer)(nil)

func TestMeta_ExistingFinalizerRemainsSourceCompatible(t *testing.T) {
	t.Parallel()

	for _, populated := range []bool{false, true} {
		meta := toolcall.Meta{TraceID: "synthetic-trace", ALegID: "synthetic-a-leg", BLegID: "synthetic-b-leg", AttemptSeq: 2}
		if populated {
			meta = finalizationMeta(producerViews())
			meta.TraceID = "synthetic-trace"
			meta.ALegID = "synthetic-a-leg"
			meta.BLegID = "synthetic-b-leg"
			meta.AttemptSeq = 2
		}
		finalizer := &legacyFinalizer{id: "legacy", order: 3}

		got, err := finalizer.Finalize(context.Background(), toolcall.CompletedCall{}, aipapi.ToolDef{}, nil, meta)
		if err != nil {
			t.Fatalf("populated=%t finalize: %v", populated, err)
		}
		if finalizer.trace != "synthetic-trace" || finalizer.aLeg != "synthetic-a-leg" ||
			finalizer.bLeg != "synthetic-b-leg" || finalizer.attempt != 2 {
			t.Errorf("populated=%t legacy finalizer must observe identical identity metadata", populated)
		}
		if got.Action != toolcall.ActionRewrite || got.ToolName != "synthetic-tool" ||
			got.ReasonCode != toolcall.ReasonValidPassThrough || string(got.ArgsJSON) != `{"synthetic":true}` {
			t.Errorf("populated=%t legacy finalizer result changed: action=%v tool_name_set=%t reason_set=%t args_len=%d",
				populated, got.Action, got.ToolName != "", got.ReasonCode != "", len(got.ArgsJSON))
		}
		if finalizer.result.Action != got.Action || finalizer.result.ToolName != got.ToolName ||
			finalizer.result.ReasonCode != got.ReasonCode || string(finalizer.result.ArgsJSON) != string(got.ArgsJSON) {
			t.Error("finalizer must observe the same Result it returned")
		}
	}

	// Existing action and reason-code enums are untouched by the extension.
	if toolcall.ActionPass == toolcall.ActionUnspecified || toolcall.ReasonValidPassThrough == "" {
		t.Error("existing Action/ReasonCode constants must stay distinct and populated")
	}
}

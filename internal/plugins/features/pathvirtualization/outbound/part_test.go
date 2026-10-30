package outbound_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// This file is the behavioural proof for the feature's SECOND outbound pass: the
// idempotent request-part hook of design.md "Existing Architecture and Placement"
// step 4, which reapplies the SAME pure rewriter after later request shaping so the
// final conversation-view reassertion and candidate adaptation cannot hand
// Backend.Open a real path-bearing tool history (requirements.md 5.2, 5.4).
//
// Everything here reuses Task 5.1's fixtures deliberately: the same project root, the
// same literal expected alias, the same compiled policy, and the same item/legacy
// call builders. That is what makes the cross-pass claims measurable rather than
// asserted - because both passes are fed byte-identical input, any difference between
// what they publish is a difference between the passes and nothing else.

// partPinnedWorkspace is the runtime's per-turn pin, projected onto the public SDK
// context seam the request-part stage reads. sdkhooks.PartMeta carries no workspace
// view, so this projection is the ONLY way the late pass reaches the authoritative
// project root - and because it is a snapshot rather than a resolution instruction,
// reading it cannot reach a host or observe a root other than the pinned one.
func partPinnedWorkspace(root string) context.Context {
	return aipworkspace.WithWorkspaceView(context.Background(),
		aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root})
}

// partUnpinnedWorkspace is the state the runtime leaves a context in when it pins no
// workspace view at all.
func partUnpinnedWorkspace() context.Context {
	return context.Background()
}

// partHookRecorder collects the bounded reports the hook emits.
type partHookRecorder struct {
	reports []outbound.Report
}

func (r *partHookRecorder) record(report outbound.Report) { r.reports = append(r.reports, report) }

func (r *partHookRecorder) only(t *testing.T) outbound.Report {
	t.Helper()
	if len(r.reports) != 1 {
		t.Fatalf("reports = %d, want exactly 1: one request-part pass emits one bounded report", len(r.reports))
	}
	return r.reports[0]
}

// partRewriteHook builds the hook under test in rewrite mode over the fixture policy.
func partRewriteHook(t *testing.T, opts ...outbound.HookOption) *outbound.RequestPartHook {
	t.Helper()
	return outbound.NewRequestPartHook(rewrite.ModeRewrite, attemptResolver(t), opts...)
}

// partMeta is an empty request-part metadata value.
//
// Every field is left at its zero value on purpose: the hook must derive its mapping
// from the workspace view alone, so a mapping that could be keyed to a trace ID, A-leg
// ID, B-leg ID, attempt ordinal, or backend identity would not be reachable from this
// input (requirements.md 5.7).
var partMeta = sdkhooks.PartMeta{}

// TestRequestPartHookFastSkipsAnAlreadyVirtualizedCall is the fast-skip proof
// requirements.md 2.9 asks for and design.md 233 states for this pass: reapplying
// virtualization to an already virtualized backend-effective call must cost nothing
// and change nothing.
//
// The two-pass drive is what makes this a real idempotence proof rather than a
// hand-built fixture. The early pass virtualizes, and the late pass is then shown
// exactly that output. A hook that recomputed, re-encoded, or otherwise disturbed the
// payload would either report a non-zero Eligible count or publish different bytes,
// and both are asserted here.
func TestRequestPartHookFastSkipsAnAlreadyVirtualizedCall(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))

	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	earlyReport := attemptRecorder{}
	early := attemptRewriteTransform(t, outbound.WithReporter(earlyReport.record))
	if _, err := early.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("the early outbound pass must not surface an error: %v", err)
	}
	if got := earlyReport.only(t); got.Stats.Rewritten != 1 || got.Stats.Eligible != 1 {
		t.Fatalf("fixture: the early outbound pass must virtualize the one selected leaf, eligible=%d rewritten=%d",
			got.Stats.Eligible, got.Stats.Rewritten)
	}
	if got := string(call.Items[1].ToolCall.Arguments); !strings.Contains(got, attemptAlias) {
		t.Fatalf("fixture: the early outbound pass must have replaced the real root on the selected leaf; got %s", got)
	}

	before := attemptMarshal(t, *call)
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - the late outbound pass must fail open rather than surface an error: %v", err)
	}
	after := attemptMarshal(t, *call)
	if after != before {
		t.Fatalf("requirements.md 2.9 and design.md \"Canonical Outbound Rewriter\" - reapplying virtualization to an already virtualized call must be a byte-identical no-op")
	}

	report := rec.only(t)
	if report.Outcome != outbound.OutcomeRewriterRan {
		t.Fatalf("report outcome = %v, want %v: the pass ran the shared rewriter", report.Outcome, outbound.OutcomeRewriterRan)
	}
	if report.Stats.Eligible != 0 || report.Stats.Rewritten != 0 {
		t.Fatalf("requirements.md 2.9 - an already virtualized call must report zero eligible and zero rewritten occurrences, eligible=%d rewritten=%d",
			report.Stats.Eligible, report.Stats.Rewritten)
	}
	if report.Stats.BytesBefore != 0 || report.Stats.BytesAfter != 0 || report.Stats.BytesSaved() != 0 {
		t.Fatalf("requirements.md 7.6 - an already virtualized call must report zero bytes before, after, and saved, before=%d after=%d saved=%d",
			report.Stats.BytesBefore, report.Stats.BytesAfter, report.Stats.BytesSaved())
	}
}

// TestRequestPartHookCatchesARealPathIntroducedAfterTheFirstPass is the other half of
// the same property, and the one that gives the idempotence requirement its meaning.
//
// A fast-skip that also skipped everything else would satisfy "no partial rewrite"
// and violate requirements.md 5.4 at the same time. This probe reintroduces real-root
// history AFTER the early pass has run - exactly what late request shaping, a
// reasserted tool result, or a candidate adaptation could do - and requires the hook
// to catch precisely that surface, leave the already-virtualized surface untouched,
// and leave every non-path field byte-for-byte intact (requirements.md 2.8, 8.5).
func TestRequestPartHookCatchesARealPathIntroducedAfterTheFirstPass(t *testing.T) {
	t.Parallel()

	transform := attemptRewriteTransform(t)
	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	if _, err := transform.HandleAttempt(t.Context(), call, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("the early outbound pass must not surface an error: %v", err)
	}
	firstArgs := string(call.Items[1].ToolCall.Arguments)
	if !strings.Contains(firstArgs, attemptAlias) {
		t.Fatalf("fixture: the early outbound pass must have virtualized the first tool call")
	}

	// Later shaping reintroduces a real-root tool call next to the virtualized one.
	const lateSuffix = "internal/core/runtime/executor.go"
	call.Items = append(call.Items, aipapi.Item{
		Kind: aipapi.ItemKindToolCall, ID: "item_call_late", Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID: "call_late", Name: attemptTool,
			Arguments: json.RawMessage(`{"file_path":"` + attemptRoot + "/" + lateSuffix + `","limit":10}`),
		},
	})
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture: the reintroduced tool call must be canonical: %v", err)
	}

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}

	report := rec.only(t)
	if report.Stats.Eligible != 1 || report.Stats.Rewritten != 1 {
		t.Fatalf("requirements.md 5.4 - the late outbound pass must catch exactly the real path that appeared after the first pass, eligible=%d rewritten=%d",
			report.Stats.Eligible, report.Stats.Rewritten)
	}
	if report.Stats.BytesSaved() <= 0 {
		t.Fatalf("requirements.md 7.6 - the late outbound pass must report the saving it realized, saved=%d", report.Stats.BytesSaved())
	}
	if got := string(call.Items[1].ToolCall.Arguments); got != firstArgs {
		t.Fatalf("requirements.md 2.9 - the late outbound pass must not disturb the surface the first pass already virtualized")
	}
	lateArgs := string(call.Items[3].ToolCall.Arguments)
	if !strings.Contains(lateArgs, attemptAlias+lateSuffix) {
		t.Fatalf("requirements.md 5.4 - the late outbound pass must virtualize the reintroduced real path; got %s", lateArgs)
	}
	if strings.Contains(lateArgs, attemptRoot) {
		t.Fatalf("requirements.md 5.2 - no real-root occurrence may survive on the reintroduced surface; got %s", lateArgs)
	}
	if !strings.Contains(lateArgs, `"limit":10`) {
		t.Fatalf("requirements.md 2.8 and 8.5 - the non-path sibling field must survive the late pass byte-for-byte; got %s", lateArgs)
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the published call must remain canonical: %v", err)
	}
	// The payload-concept sibling of the first surface is still real-root text and must
	// stay that way: requirement 2.4's refusal is not a function of which pass runs.
	if !strings.Contains(attemptMarshal(t, *call), `"content":"literal `+attemptRoot+` text"`) {
		t.Fatalf("requirements.md 2.4 - an unselected payload-concept field must survive both passes unchanged")
	}
}

// TestBothOutboundPassesDeriveTheSameWorkspaceAlias is requirements.md 5.6 stated as a
// measurement: the early pass and the late pass must publish the identical value for
// identical input, because both derive it from the same authoritative project root
// through the same pure rules and neither caches anything.
func TestBothOutboundPassesDeriveTheSameWorkspaceAlias(t *testing.T) {
	t.Parallel()

	earlyCall := attemptItemCall(attemptPathArguments)
	early := attemptRewriteTransform(t)
	if _, err := early.HandleAttempt(t.Context(), earlyCall, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("the early outbound pass must not surface an error: %v", err)
	}

	lateCall := attemptItemCall(attemptPathArguments)
	rec := &partHookRecorder{}
	late := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	if err := late.HandleRequestParts(partPinnedWorkspace(attemptRoot), lateCall, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}

	earlyPublished := attemptMarshal(t, *earlyCall)
	latePublished := attemptMarshal(t, *lateCall)
	if earlyPublished != latePublished {
		t.Fatalf("requirements.md 5.6 - both outbound passes must publish the identical value for identical input")
	}
	if !strings.Contains(earlyPublished, attemptAlias) {
		t.Fatalf("requirements.md 1.1 - the published value must carry the fixed V1 alias for the fixture root")
	}
	if got := rec.only(t).Stats.Rewritten; got != 1 {
		t.Fatalf("fixture: the late pass must virtualize the same single leaf, rewritten=%d", got)
	}
}

// TestRequestPartHookDerivesTheMappingFromThePinnedWorkspaceView pins where the late
// pass reads its project root from, and that it derives per invocation.
//
// The hook is handed an EMPTY PartMeta, so nothing but the runtime's pinned projection
// can supply a root. The pass re-derives the mapping from that pin on every invocation
// rather than caching one, which is what keeps requirement 5.6's "same alias for every
// retry, race participant, and failover candidate of one logical A-leg turn" true
// without any stored mapping state, and requirement 6.2's restart-equivalence true by
// construction.
//
// The second half proves the derivation FOLLOWS THE PIN: driven with two different
// pinned roots the pass publishes two different aliases, so nothing about the mapping is
// remembered from a previous invocation. It cannot read any other root source, because
// the pass holds no workspace authority at all - see
// TestTheLatePassNeverHoldsOrCallsAWorkspaceResolver.
func TestRequestPartHookDerivesTheMappingFromThePinnedWorkspaceView(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))

	// One shared instance, driven twice on the SAME pin. Both invocations must
	// re-derive the same alias from the pin they were handed, so their publications are
	// byte-identical and neither is a value remembered from an earlier request.
	first := attemptItemCall(attemptPathArguments)
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), first, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	second := attemptItemCall(attemptPathArguments)
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), second, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	published := attemptMarshal(t, *first)
	if got := attemptMarshal(t, *second); got != published {
		t.Fatalf("requirements.md 5.6 - two invocations on one pinned root must publish identical bytes, got %s", got)
	}
	if !strings.Contains(published, attemptAlias) {
		t.Fatalf("fixture: the pass must virtualize the surface under the first pinned root; got %s", published)
	}
	if len(rec.reports) != 2 {
		t.Fatalf("reports = %d, want one bounded report per invocation", len(rec.reports))
	}
	for i, report := range rec.reports {
		if report.Stats.Eligible != 1 || report.Stats.Rewritten != 1 {
			t.Fatalf("invocation %d must rewrite the one selected leaf, eligible=%d rewritten=%d",
				i, report.Stats.Eligible, report.Stats.Rewritten)
		}
	}

	// A DIFFERENT pin on the same shared instance must produce a different alias: one
	// generation-scoped instance serves every request, so a remembered mapping would
	// publish the previous request's alias here. The second root is long enough that
	// its own alias is strictly shorter, so activation is not what is under test.
	const otherRoot = "/srv/other/checkouts/aiproxer-example-project"
	otherCall := attemptItemCall(`{"file_path":"` + otherRoot + `/src/main.go"}`)
	if err := hook.HandleRequestParts(partPinnedWorkspace(otherRoot), otherCall, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	otherPublished := attemptMarshal(t, *otherCall)
	if !strings.Contains(otherPublished, ".__aip_v1__") {
		t.Fatalf("fixture: the pass must virtualize the surface under the second pinned root; got %s", otherPublished)
	}
	if strings.Contains(otherPublished, otherRoot) {
		t.Fatalf("requirements.md 5.6 - the second pinned root must be virtualized from its OWN tag, not a remembered one; got %s", otherPublished)
	}
	if strings.Contains(otherPublished, attemptAlias) {
		t.Fatalf("requirements.md 5.6 - the second pinned root must not reuse the first pin's alias; got %s", otherPublished)
	}
}

// TestRequestPartHookFailsOpenWithoutAPinnedWorkspaceView is requirements.md 8.2 at
// the one condition only this pass can meet: the early pass is handed the pinned view
// as attempt metadata, so an ABSENT view is a condition the late pass observes and the
// early pass never can.
//
// The pass must preserve the real path, keep the request-part chain moving, and record a
// bounded reason. There is no resolver error to suppress any more, which is the point:
// reading a projection cannot fail in a way that carries a path.
func TestRequestPartHookFailsOpenWithoutAPinnedWorkspaceView(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))

	call := attemptItemCall(attemptPathArguments)
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before := attemptMarshal(t, *call)

	if err := hook.HandleRequestParts(partUnpinnedWorkspace(), call, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - an absent pinned view must fail open rather than surface an error: %v", err)
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("requirements.md 8.2 - an unresolved workspace must leave the request exactly as the runtime built it")
	}
	report := rec.only(t)
	if report.Outcome != outbound.OutcomeWorkspaceUnresolved {
		t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeWorkspaceUnresolved)
	}
	if report.RootReason != pathvirtualization.SkipReasonNone {
		t.Fatalf("an unresolved workspace is not a root-shape refusal, root_reason = %q", report.RootReason)
	}
	if !reflectStatsZero(report.Stats) {
		t.Fatalf("requirements.md 8.2 - a pass that never reached the rewriter must not report statistics it never earned: %+v", report.Stats)
	}
	assertNoContentLeak(t, report)
}

// TestRequestPartHookRecordsTheMapperRefusalForAnUnusableRoot proves the hook answers
// requirement 1.8 the same way the early pass does: a relative, malformed, device, or
// reserved-namespace root disables rewriting for that mapping and records the mapper's
// own bounded code rather than guessing.
func TestRequestPartHookRecordsTheMapperRefusalForAnUnusableRoot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		root string
		want pathvirtualization.SkipReason
	}{
		{name: "empty", root: "", want: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: "relative/path/workspace", want: pathvirtualization.SkipReasonRelativeRoot},
		{name: "reserved_namespace", root: attemptAlias + "nested", want: pathvirtualization.SkipReasonReservedNamespaceCollision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &partHookRecorder{}
			hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
			call := attemptItemCall(`{"file_path":"` + attemptTarget + `"}`)
			before := attemptMarshal(t, *call)

			if err := hook.HandleRequestParts(partPinnedWorkspace(tc.root), call, partMeta); err != nil {
				t.Fatalf("requirements.md 8.2 - an unusable root must fail open rather than surface an error: %v", err)
			}
			if after := attemptMarshal(t, *call); after != before {
				t.Fatalf("requirements.md 1.8 - rewriting must be disabled for an unusable root, not attempted")
			}
			report := rec.only(t)
			if report.Outcome != outbound.OutcomeProjectRootUnusable {
				t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeProjectRootUnusable)
			}
			if report.RootReason != tc.want {
				t.Fatalf("report root reason = %q, want %q", report.RootReason, tc.want)
			}
			assertNoContentLeak(t, report)
		})
	}
}

// TestRequestPartHookIsInactiveWhenTheAliasIsNotShorter is requirements.md 1.4 applied
// to this pass: a root whose alias is not strictly shorter leaves outbound
// virtualization inactive for the whole call, and the shared rewriter reports that
// once rather than the hook inventing a decision of its own.
func TestRequestPartHookIsInactiveWhenTheAliasIsNotShorter(t *testing.T) {
	t.Parallel()

	const shortRoot = "/w"
	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))

	call := attemptItemCall(`{"file_path":"` + shortRoot + `/src/main.go","limit":10}`)
	before := attemptMarshal(t, *call)
	if err := hook.HandleRequestParts(partPinnedWorkspace(shortRoot), call, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - an inactive mapping must fail open rather than surface an error: %v", err)
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("requirements.md 1.4 - a mapping whose alias is not shorter must leave every surface unchanged")
	}
	report := rec.only(t)
	if report.Outcome != outbound.OutcomeRewriterRan {
		t.Fatalf("report outcome = %v, want %v", report.Outcome, outbound.OutcomeRewriterRan)
	}
	if !hasSkip(report.Stats, rewrite.SkipReasonMappingInactive) {
		t.Fatalf("requirements.md 1.4 - the shared rewriter must record its own inactive-mapping refusal, skips=%+v", report.Stats.Skips)
	}
	if report.Stats.Eligible != 0 || report.Stats.Rewritten != 0 {
		t.Fatalf("an inactive mapping must report no occurrence, eligible=%d rewritten=%d", report.Stats.Eligible, report.Stats.Rewritten)
	}
	assertNoContentLeak(t, report)
}

// TestRequestPartHookAuditModeIsTheAuditToggleOfTheSameRewriter is requirements.md 7.3 applied
// to the late pass: audit is the same walk with publication switched off, so the
// statistics are the rewrite's own numbers and the canonical request is returned
// exactly as it arrived.
//
// The fixture runs the early pass in REWRITE mode and the late pass in AUDIT mode. A
// measurably smaller late report than the early one would mean the two modes drifted,
// which is the failure requirement 7.3 exists to prevent.
func TestRequestPartHookAuditModeIsTheAuditToggleOfTheSameRewriter(t *testing.T) {
	t.Parallel()

	earlyReport := attemptRecorder{}
	early := attemptRewriteTransform(t, outbound.WithReporter(earlyReport.record))
	earlyCall := attemptItemCall(attemptPathArguments)
	if _, err := early.HandleAttempt(t.Context(), earlyCall, attemptWorkspace(), request.Services{}); err != nil {
		t.Fatalf("the early outbound pass must not surface an error: %v", err)
	}
	want := earlyReport.only(t).Stats

	auditRec := &partHookRecorder{}
	audit := outbound.NewRequestPartHook(
		rewrite.ModeAudit, attemptResolver(t),
		outbound.WithHookReporter(auditRec.record),
	)
	auditCall := attemptItemCall(attemptPathArguments)
	before := attemptMarshal(t, *auditCall)
	if err := audit.HandleRequestParts(partPinnedWorkspace(attemptRoot), auditCall, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	if after := attemptMarshal(t, *auditCall); after != before {
		t.Fatalf("requirements.md 7.3 - audit mode must not mutate the canonical request at all")
	}
	got := auditRec.only(t).Stats
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements.md 7.3 and 9.5 - audit and rewrite must report the shared rewriter's identical numbers, audit=%+v rewrite=%+v", got, want)
	}
	if got.Rewritten == 0 {
		t.Fatalf("fixture: the audit pass must report what a rewrite would have published")
	}
}

// TestRequestPartHookIdentityOrderAndFailureMode pins the three properties the
// runtime's own contract reads, and pins the interfaces the pass must satisfy.
//
// The ORDER is the load-bearing one. design.md "Existing Architecture and Placement"
// step 4 places this pass after every later request shaping, so it has to sort last
// inside the request-part chain: a pass that ran first could not catch a real path an
// earlier participant restored. The request-part bus sorts ascending by order, then ID,
// then registration index (internal/core/hooks.StableParticipantLess), so a fixed order
// above every shipped request-part order is what makes "last" a property of this pass
// rather than a property of the bundle.
func TestRequestPartHookIdentityOrderAndFailureMode(t *testing.T) {
	t.Parallel()

	var hook sdkhooks.RequestPartHook = partRewriteHook(t)
	// The identity and the order are pinned as LITERALS. Comparing the accessors with
	// outbound.PartHookID and outbound.OrderRequestPartHook would move with the
	// implementation, so drifting the exported contract would keep the comparison green.
	const wantID = "path-virtualization-request-part-hook"
	const wantOrder = 1000
	if got := hook.ID(); got != wantID {
		t.Fatalf("ID() = %q, want the fixed %q: identity must stay low-cardinality and stable across builds", got, wantID)
	}
	// Every request-part hook shipped in this repository sits at or below order 100
	// (partsnoop is the highest, at 100), so 1000 is what makes "last" a property of
	// this pass rather than of whatever else a deployment installs.
	if got := hook.Order(); got != wantOrder {
		t.Fatalf("Order() = %d, want the fixed %d: design.md \"Existing Architecture and Placement\" step 4 - the late outbound pass must sort after every shipped request-part participant",
			got, wantOrder)
	}
	if got := hook.FailureMode(); got != sdkhooks.FailOpen {
		t.Fatalf("FailureMode() = %v, want %v: requirements.md 8.2 makes this direction fail open", got, sdkhooks.FailOpen)
	}
	if outbound.PartHookID == outbound.TransformID {
		t.Fatal("the two outbound passes must have distinct fixed identities so a failure names the one that failed")
	}
}

// TestRequestPartHookIsNilSafe proves the pass survives the states a shared instance
// can actually be observed in, including a typed nil the request-part bus would still
// call into.
func TestRequestPartHookIsNilSafe(t *testing.T) {
	t.Parallel()

	var absent *outbound.RequestPartHook
	var iface sdkhooks.RequestPartHook = absent
	// Literals again: a typed nil must report the same fixed identity as a live pass,
	// and a comparison against the exported constants would drift with them.
	if got := iface.ID(); got != "path-virtualization-request-part-hook" {
		t.Fatalf("ID() = %q, want %q", got, "path-virtualization-request-part-hook")
	}
	if got := iface.Order(); got != 1000 {
		t.Fatalf("Order() = %d, want %d", got, 1000)
	}
	if got := iface.FailureMode(); got != sdkhooks.FailOpen {
		t.Fatalf("FailureMode() = %v, want %v", got, sdkhooks.FailOpen)
	}
	call := attemptItemCall(attemptPathArguments)
	before := attemptMarshal(t, *call)
	if err := iface.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("an absent pass must publish nothing and surface no error: %v", err)
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("an absent pass must leave the call exactly as it arrived")
	}
	if err := iface.HandleRequestParts(partPinnedWorkspace(attemptRoot), nil, partMeta); err != nil {
		t.Fatalf("an absent call is not this pass's error to report: %v", err)
	}
}

// TestTheLatePassDistinguishesAnEmptyPinnedViewFromAnAbsentOne pins the two verdicts a
// consumer can now observe, and the deliberate detached-path consequence of the second.
//
// requirements.md 8.1 makes an unconfigured feature unobservable, and 5.7 forbids
// substituting any other root source. Both states publish nothing, but they are not the
// same state:
//
//   - an ABSENT projection means no authority was published at all. That is the
//     unresolved-workspace verdict, and deliberately NOT a root-shape refusal: the root
//     is not what is missing here, the authority that would have supplied it is, and a
//     report that said otherwise would send an operator to configure a root spelling
//     that is not the problem.
//   - an ATTACHED EMPTY view is a genuine view with no project root in it, which is
//     exactly what the executor's DETACHED auxiliary path pins
//     (internal/core/runtime/executor_prepare_detached.go). DeriveMapping is the
//     authority that refuses an empty root, so this is the unusable-root verdict with
//     the mapper's own bounded code - and because the projection always overwrites, a
//     detached child cannot inherit its parent's project root through the context chain
//     and virtualize against a workspace it does not belong to.
func TestTheLatePassDistinguishesAnEmptyPinnedViewFromAnAbsentOne(t *testing.T) {
	t.Parallel()

	empty := aipworkspace.WithWorkspaceView(context.Background(), aipworkspace.WorkspaceView{})

	emptyRec := &partHookRecorder{}
	emptyHook := partRewriteHook(t, outbound.WithHookReporter(emptyRec.record))
	emptyCall := attemptItemCall(attemptPathArguments)
	emptyBefore := attemptMarshal(t, *emptyCall)
	if err := emptyHook.HandleRequestParts(empty, emptyCall, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - an empty pinned view must fail open rather than surface an error: %v", err)
	}
	if after := attemptMarshal(t, *emptyCall); after != emptyBefore {
		t.Fatalf("requirements.md 8.1 and 5.7 - an empty pinned view must publish nothing")
	}
	emptyReport := emptyRec.only(t)
	if emptyReport.Outcome != outbound.OutcomeProjectRootUnusable {
		t.Fatalf("report outcome = %v, want %v", emptyReport.Outcome, outbound.OutcomeProjectRootUnusable)
	}
	if emptyReport.RootReason != pathvirtualization.SkipReasonEmptyRoot {
		t.Fatalf("report root reason = %q, want %q", emptyReport.RootReason, pathvirtualization.SkipReasonEmptyRoot)
	}
	if !reflectStatsZero(emptyReport.Stats) {
		t.Fatalf("a pass that never reached the rewriter must not report statistics it never earned: %+v", emptyReport.Stats)
	}
	assertNoContentLeak(t, emptyReport)

	absentRec := &partHookRecorder{}
	absentHook := partRewriteHook(t, outbound.WithHookReporter(absentRec.record))
	absentCall := attemptItemCall(attemptPathArguments)
	absentBefore := attemptMarshal(t, *absentCall)
	if err := absentHook.HandleRequestParts(partUnpinnedWorkspace(), absentCall, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - an absent pinned view must fail open rather than surface an error: %v", err)
	}
	if after := attemptMarshal(t, *absentCall); after != absentBefore {
		t.Fatalf("requirements.md 8.1 and 5.7 - without the authoritative workspace view the pass must publish nothing")
	}
	absentReport := absentRec.only(t)
	if absentReport.Outcome != outbound.OutcomeWorkspaceUnresolved {
		t.Fatalf("report outcome = %v, want %v", absentReport.Outcome, outbound.OutcomeWorkspaceUnresolved)
	}
	if absentReport.RootReason != pathvirtualization.SkipReasonNone {
		t.Fatalf("an absent workspace authority is not a root-shape refusal, root_reason = %q", absentReport.RootReason)
	}
	assertNoContentLeak(t, absentReport)
}

// TestRequestPartHookPreservesCanonicalValidation is requirements.md 8.5 for this
// pass: the runtime re-validates the call after every request-part hook, so a
// publication that left the document unreadable would fail the whole attempt.
func TestRequestPartHookPreservesCanonicalValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		call *aipapi.Call
	}{
		{name: "item_authority", call: attemptItemCall(attemptPathArguments)},
		{name: "legacy_authority", call: attemptLegacyCall(attemptPathArguments)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hook := partRewriteHook(t)
			if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), tc.call, partMeta); err != nil {
				t.Fatalf("the late outbound pass must not surface an error: %v", err)
			}
			// The validation below is about a PUBLICATION: a pass that published nothing
			// would leave the ingress call valid and prove nothing, so the rewrite itself
			// is asserted first.
			if published := attemptMarshal(t, *tc.call); !strings.Contains(published, attemptVirtual) {
				t.Fatalf("requirements.md 5.2 - the pass must publish the virtualized call on the %s authority; nothing reached the backend-bound request", tc.name)
			}
			if err := tc.call.Validate(); err != nil {
				t.Fatalf("requirements.md 8.5 - the published call must remain canonical: %v", err)
			}
		})
	}
}

// TestRequestPartHookOnAnAbsentCallPublishesNothing is the one state the hook must
// survive that the nil-receiver case above cannot reach: a PRESENT pass handed an
// ABSENT call.
//
// The request-part bus rejects an absent call before it chains, so this state is not
// reachable through the runtime today. It is still worth pinning, because the pass
// resolves the workspace, derives a mapping, and binds a rewriter before it looks at
// the call at all: an absent call must come out the other side as a bounded report and
// nothing else, never as a nil dereference on the hot path.
func TestRequestPartHookOnAnAbsentCallPublishesNothing(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), nil, partMeta); err != nil {
		t.Fatalf("an absent call is not this pass's error to report: %v", err)
	}
	report := rec.only(t)
	if report.Outcome != outbound.OutcomeRewriterRan {
		t.Fatalf("report outcome = %v, want %v: the pass reached the rewriter, which answered an absent call with nothing to publish",
			report.Outcome, outbound.OutcomeRewriterRan)
	}
	if !reflectStatsZero(report.Stats) {
		t.Fatalf("an absent call can contribute no occurrence: %+v", report.Stats)
	}
}

// TestRequestPartHookLeavesAnUnreadablePayloadUntouched is requirements.md 2.3 and 8.2
// at the late stage: a tool-call argument document that is not one complete JSON value
// is refused whole, never read partially.
//
// A late pass runs on a request the runtime has already reshaped, so an unreadable
// payload is far more likely here than at the early pass. If the pass were to publish
// a partial read it would replace a real path on the selected member while leaving the
// rest of a malformed document behind, and the runtime's post-hook re-validation would
// then fail the whole attempt.
func TestRequestPartHookLeavesAnUnreadablePayloadUntouched(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	truncated := json.RawMessage(`{"file_path":"` + attemptTarget + `",`)
	call := attemptItemCall(attemptPathArguments)
	call.Items[1].ToolCall.Arguments = truncated

	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - an unreadable payload must fail open rather than surface an error: %v", err)
	}
	if got := call.Items[1].ToolCall.Arguments; string(got) != string(truncated) {
		t.Fatalf("requirements.md 2.3 - an unreadable payload must be left exactly as it arrived; a partial read was published")
	}
	report := rec.only(t)
	if report.Stats.Eligible != 0 || report.Stats.Rewritten != 0 {
		t.Fatalf("an unreadable payload must report no occurrence, eligible=%d rewritten=%d",
			report.Stats.Eligible, report.Stats.Rewritten)
	}
	if !hasSkip(report.Stats, rewrite.SkipReasonPayloadInvalid) {
		t.Fatalf("requirements.md 3.7 - the rewriter must record its own bounded refusal for an unreadable payload, skips=%+v", report.Stats.Skips)
	}
	assertNoContentLeak(t, report)
}

// TestRequestPartHookLeavesAForeignReservedAliasUntouched is the outbound half of
// requirement 1.11: a path already spelled inside the fixed V1 reserved namespace with
// a tag this workspace did not derive is an unresolved reserved alias, and an OUTBOUND
// pass must leave it exactly as it found it.
//
// The inbound direction owns what happens to such an alias, and that is Task 8.x. What
// this pass must never do is treat one as a real-root path, rewrite it into a second
// alias, or fold its tag into this workspace's identity. The mapper's segment-boundary
// real-root substitution is what makes that impossible.
func TestRequestPartHookLeavesAForeignReservedAliasUntouched(t *testing.T) {
	t.Parallel()

	const foreignAlias = "/.__aip_v1__/w_zzzzzzzzzzzzzzzzzzzz"
	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	call := attemptItemCall(`{"file_path":"` + foreignAlias + `/pkg/aipapi/call.go","limit":10}`)
	before := attemptMarshal(t, *call)

	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("requirements.md 8.2 - a reserved alias must fail open rather than surface an error: %v", err)
	}
	if after := attemptMarshal(t, *call); after != before {
		t.Fatalf("requirements.md 1.11 - an outbound pass must leave a reserved alias carrying another workspace's tag untouched")
	}
	report := rec.only(t)
	if report.Stats.Eligible != 0 || report.Stats.Rewritten != 0 {
		t.Fatalf("requirements.md 1.11 - a reserved alias is not a virtualizable real-root occurrence, eligible=%d rewritten=%d",
			report.Stats.Eligible, report.Stats.Rewritten)
	}
	assertNoContentLeak(t, report)
}

// TestRequestPartHookDoesNotMutateTheCallerOwnedInput proves the shared rewriter's
// purity reaches the caller's own value: the hook publishes onto the working call the
// runtime handed it, but it never writes through a payload byte that value still owns.
//
// The held copy is the fixture value BEFORE the pass ran. If the pass had mutated in
// place rather than publishing a deep copy, the held copy would differ while the
// published call looked correct - the alias and the real root would share one backing
// array, and a later stage that still held the pre-pass value would see a half-rewritten
// document.
func TestRequestPartHookDoesNotMutateTheCallerOwnedInput(t *testing.T) {
	t.Parallel()

	call := attemptItemCall(attemptPathArguments)
	// shared aliases the argument document's backing array, so an in-place splice into
	// the caller's buffer is still observable after the pass replaces the call's own
	// value. A deep clone could not observe that: nothing else holds a reference to it.
	shared := call.Items[1].ToolCall.Arguments
	sharedBefore := string(shared)

	hook := partRewriteHook(t)
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), call, partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}

	if got := string(shared); got != sharedBefore {
		t.Fatalf("requirements.md 2.8 - the pass must not write through a payload byte the caller still owns")
	}
	published := call.Items[1].ToolCall.Arguments
	if string(published) == sharedBefore {
		t.Fatalf("fixture: the pass must have published a rewritten argument document")
	}
	if &published[0] == &shared[0] {
		t.Fatalf("requirements.md 2.8 - the published document must not alias the caller's own bytes")
	}
}

// TestRequestPartHookReportsAreContentFree is requirements.md 7.7 for this pass: every
// label it publishes is a compile-time literal, and the whole report marshals to bytes
// that name no path, alias, workspace tag, tool, or call.
func TestRequestPartHookReportsAreContentFree(t *testing.T) {
	t.Parallel()

	rec := &partHookRecorder{}
	hook := partRewriteHook(t, outbound.WithHookReporter(rec.record))
	if err := hook.HandleRequestParts(partPinnedWorkspace(attemptRoot), attemptItemCall(attemptPathArguments), partMeta); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	assertNoContentLeak(t, rec.only(t))

	for _, outcome := range []outbound.Outcome{
		outbound.OutcomeRewriterRan,
		outbound.OutcomeProjectRootUnusable,
		outbound.OutcomeTransformationFailed,
		outbound.OutcomeWorkspaceUnresolved,
	} {
		label := outcome.String()
		if label == "" || label == "unknown" || strings.ContainsAny(label, "/\\.:") {
			t.Fatalf("outcome label %q is not a fixed low-cardinality token", label)
		}
	}
}

// reflectStatsZero reports whether a statistics value is the zero value.
//
// It uses a deep comparison rather than == because Stats carries a slice of recorded
// skip reasons, which is not comparable.
func reflectStatsZero(stats rewrite.Stats) bool {
	return reflect.DeepEqual(stats, rewrite.Stats{})
}

// hasSkip reports whether the shared rewriter recorded a bounded reason.
func hasSkip(stats rewrite.Stats, want rewrite.SkipReason) bool {
	for _, skip := range stats.Skips {
		if skip.Reason == want {
			return true
		}
	}
	return false
}

// assertNoContentLeak fails when a bounded report marshals to bytes naming anything
// this feature must never expose.
func assertNoContentLeak(t *testing.T, report outbound.Report) {
	t.Helper()

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{
		attemptRoot, attemptTarget, attemptAlias, attemptVirtual,
		".__aip_v1__", "hr7xnivm5at2ku356qya", attemptTool, attemptCallID,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("requirements.md 7.7 - the bounded report leaked content-bearing text")
		}
	}
}

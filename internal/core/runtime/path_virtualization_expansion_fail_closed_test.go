package runtime_test

// Spec: b-leg-path-virtualization Task 8.2, the fail-closed half.
// Requirements 4.4, 4.5, 4.6, and 8.3.
//
// Design sections read for this file:
//
//   - "7. Path Expansion Finalizer" step 8 ("if reserved alias is malformed or carries
//     a different workspace tag/flavor/drive, reject with bounded reason") and step 10
//     (the assembler, not the pass, synthesizes what the client sees);
//   - "6. Mandatory Buffering / Completeness Contract" ("if an applicable call
//     exceeds that mandatory bound, reject before any alias-bearing argument reaches
//     the client");
//   - "Error Handling" ("Malformed reserved alias on a selected model path: fail closed
//     for that tool call"; "Stale/different workspace tag or incompatible alias
//     flavor: fail closed with workspace_mismatch"; "Mandatory assembly overflow: fail
//     closed for that tool call");
//   - "Existing Architecture and Placement" steps 11/12 (the fail-closed decision is
//     made at finalization, so it happens BEFORE any policy, reactor, or client event);
//   - "Testing Strategy / Runtime-integration" ("Stale/unresolved alias and mandatory
//     overflow never reach client events").
//
// WHY THIS FILE EXISTS ALONGSIDE THE COMPOSITION FILE
//
// The composition guard next door proves the happy path: expansion happens and the
// real path reaches policy, reactor, and client. Requirements.md 4.4, 4.5, and 8.3
// are about the OTHER answers, and their whole point is that a call which CANNOT be
// expanded unambiguously is not released at all. That claim is only worth something
// if it is asserted over what the CLIENT actually received, not over what a
// finalizer returned, because a refusal can be honored on paper and defeated in the
// stream: the assembler could replay original fragments, an unrelated finalizer's
// fallback could reintroduce them, or a lifecycle event could slip through while the
// arguments are held.
//
// So every case below asserts over the WHOLE emitted client-facing event stream,
// with two independent detectors:
//
//   - a LITERAL byte scan for the frozen reserved namespace marker over every
//     serialized client event, which is the simplest possible leak oracle and does
//     not depend on the same recognizer whose silence it is checking;
//   - the PRODUCTION reserved-alias recognizer applied to every selected path field
//     of every released argument document, so the answer does not depend on how the
//     literal is spelled.
//
// It also counts the tool-lifecycle events that reached the client at all, because a
// fail-closed tool call releases none: not the started event, not the arguments, not
// the finished event. A client that can see a tool call without its arguments is in
// exactly the state this feature must not create.
//
// NON-VACUITY
//
// Each case runs against the identical harness with only the alias (or the size)
// changed, and the resolvable case is asserted in the same test function as a
// positive control that DOES release one expanded tool call. A stream scanner that
// reports nothing is therefore distinguishable from a stream that was never scanned.
//
// CONTENT FREEDOM
//
// No failure message contains a path, an alias, a workspace tag, a tool-call ID, or
// argument bytes. Messages carry requirement numbers, byte counts, occurrence counts,
// bounded reason labels, and typed-error classification results only.
//
// DETERMINISM
//
// One mutex-guarded monotonic counter per run, one pinned project root, one
// content-defined alias derivation, a pinned RNG and clock, and a fixture whose size
// is computed from the declared mandatory bound rather than from a mutable
// production field.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// expFailingOrder is the absolute order of the unrelated optional finalizer used by
// the "unrelated failure" case.
//
// It is an absolute constant rather than an offset from the shipped expansion order
// so that the order property under test is genuinely load-bearing: the case passes
// only because the shipped expansion order sits ABOVE it. Lowering the shipped order
// below it trips the fixture guard that asserts the ordering precondition, which is
// the reported fault injection for this file.
//
// A leak is NOT reachable through this route alone: with the unrelated finalizer
// above the declaring pass instead, the assembler's accepted post-declaration replay
// residual would release the originals with no error. That residual is owned by
// Task 12.2 and must not be closed with a core ordering rule.
const expFailingOrder = 1

// expOverflowBytes is the assembled argument size the mandatory-overflow case drives.
// It is derived from the DECLARED default bound rather than from any mutable
// production field, so raising the effective bound cannot quietly turn the fixture
// into an ordinary expandable call.
const expOverflowBytes = toolcall.DefaultMandatoryMaxArgsBytes + 64*1024

// expClientScan is the path-content-free measurement of a WHOLE client-facing event
// stream. Counts only; no payload byte can reach a failure message through it.
type expClientScan struct {
	// events is how many events of any kind reached the client.
	events int
	// toolEvents is how many tool-lifecycle events reached the client at all.
	toolEvents int
	// argEvents and argBytes describe the released argument deltas.
	argEvents int
	argBytes  int
	// markerEvents is how many client events' rendered form carries the frozen
	// reserved namespace marker bytes ANYWHERE - in any field of any event.
	markerEvents int
	// unrenderedEvents is how many client events the scanner could not render at all.
	// It must be zero: an unrendered event is an event the marker oracle did not read.
	unrenderedEvents int
	// documents is how many distinct tool calls released an argument document that
	// parsed as one complete JSON value.
	documents int
	// selectedFields is how many selected path fields were read off those documents.
	selectedFields int
	// reservedSelected is how many of those the PRODUCTION recognizer reports as
	// carrying the reserved namespace.
	reservedSelected int
	// realSelected is how many carry the authoritative real root.
	realSelected int
	// newRootFields is how many carry the NEW authoritative root's bytes, where that
	// root is not the shared fixture root. It is the requirement 6.5 rebinding oracle:
	// a stale alias EXPANDED against the new root would show up here while every
	// other zero in this struct still held, because the tool lifecycle would still be
	// withheld. Empty disables it.
	newRootFields int
	// staleTagFields is how many carry the PRIOR workspace's identity tag. It is the
	// same oracle from the other side: the stale alias must not be handed back to the
	// client as an ordinary path either. Empty disables it.
	staleTagFields int
}

// expScanClientEvents measures the whole emitted stream.
//
// newRoot and staleTag are the two requirement 6.5 rebinding oracles, read off the
// same selected path values. Both are empty for every pre-existing case, which leaves
// their counters at zero by construction rather than by an assertion being skipped.
//
// Every event is rendered to JSON with HTML escaping DISABLED and scanned for the
// marker bytes, and every released argument document is reassembled PER TOOL CALL
// and parsed, so a document split across several deltas is still read as one
// document and a single held delta cannot hide a leak.
//
// Rendering without HTML escaping matters: the default encoder rewrites `<`, `>`
// and `&` into `\u00xx`, so an escaping-sensitive oracle could in principle miss a
// marker byte it had itself rewritten. An event the encoder cannot render is counted
// rather than skipped silently, so a scan that quietly examined less than it claims
// is visible.
func expScanClientEvents(events []aipapi.Event, newRoot, staleTag string) expClientScan {
	var out expClientScan
	var buf bytes.Buffer
	joined := map[string]string{}
	order := make([]string, 0, 4)
	for _, ev := range events {
		out.events++
		buf.Reset()
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		switch err := enc.Encode(ev); {
		case err != nil:
			out.unrenderedEvents++
		default:
			if bytes.Contains(buf.Bytes(), []byte(expReservedMarker)) {
				out.markerEvents++
			}
		}
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			out.toolEvents++
		default:
			continue
		}
		if ev.Kind != aipapi.EventToolCallArgsDelta {
			continue
		}
		out.argEvents++
		out.argBytes += len(ev.Delta)
		if _, seen := joined[ev.ToolCallID]; !seen {
			order = append(order, ev.ToolCallID)
		}
		joined[ev.ToolCallID] += ev.Delta
	}
	// Map iteration is deliberately avoided so the measurement is order-stable.
	for _, id := range order {
		doc := joined[id]
		if !json.Valid([]byte(doc)) {
			continue
		}
		out.documents++
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(doc), &fields); err != nil {
			continue
		}
		var selected string
		if json.Unmarshal(fields[expPathField], &selected) != nil {
			continue
		}
		out.selectedFields++
		if pathvirtualization.ScanReservedAlias([]byte(selected)) != pathvirtualization.ReservedAliasAbsent {
			out.reservedSelected++
		}
		if strings.Contains(selected, twoPassRealRoot) {
			out.realSelected++
		}
		if newRoot != "" && strings.Contains(selected, newRoot) {
			out.newRootFields++
		}
		if staleTag != "" && strings.Contains(selected, staleTag) {
			out.staleTagFields++
		}
	}
	return out
}

// expFailingOrdinaryFinalizer is the unrelated OPTIONAL finalizer of the
// "unrelated failure" case. It publishes no mandatory requirement, never touches the
// document, and always fails - which is the pre-existing shape Task 7.2's assembler
// fallback replays the original fragments for. It exists so requirement 4.6's
// failure clause is reachable: while the declaring pass is still undecided, that
// fallback must refuse the call closed instead of replaying alias-bearing bytes.
type expFailingOrdinaryFinalizer struct{}

func (*expFailingOrdinaryFinalizer) ID() string { return "expansion-unrelated-failing-finalizer" }

func (*expFailingOrdinaryFinalizer) Order() int { return expFailingOrder }

func (*expFailingOrdinaryFinalizer) Finalize(
	context.Context,
	toolcall.CompletedCall,
	aipapi.ToolDef,
	[]aipapi.ToolDef,
	toolcall.Meta,
) (toolcall.Result, error) {
	return toolcall.Result{}, errors.New("expansion unrelated finalizer failure")
}

// expRefusalCase is one fail-closed scenario plus the bounded reason it must produce
// and the classification it must be reachable through.
type expRefusalCase struct {
	// label is a bounded, content-free failure-message token.
	label string
	// aliasRoot is the root prefix the model-emitted alias is built on. It is either
	// the authoritative root's own derived alias (resolvable) or a DIFFERENT
	// supported root's derived alias (stale/unresolved).
	aliasRoot string
	// malformedAlias replaces the derived alias with a fixed reserved-namespace
	// spelling whose tag segment is not a well-formed workspace tag. It is
	// deliberately not derived from any root, so it cannot accidentally become
	// resolvable.
	malformedAlias string
	// assembleBody, when non-zero, replaces the model's argument document with one of
	// exactly this many bytes.
	assembleBody int
	// extraFinalizers are added to the chain for this case.
	extraFinalizers []toolcall.Finalizer
	// wantPassInvocations is how many times the shipped expansion pass must have been
	// invoked. It is one when the PASS decides the case (the two unresolved-alias
	// shapes) and zero when the ASSEMBLER refuses before any finalizer runs (the
	// mandatory-overflow shape, which is decided by the declared bound) or before the
	// declaring pass gets its turn (the unrelated-failure shape, decided by
	// requirement 4.6's chokepoint). Reporting it rather than assuming it is what keeps
	// "the pass decided" and "the assembler decided" from being confused.
	wantPassInvocations int
	// wantPassReason is the bounded expansion reason the shipped pass must report when
	// it does decide. It is only read when wantPassInvocations is one.
	wantPassReason expansion.Reason
	// wantMandatoryReason, when non-empty, is the assembler's own bounded reason the
	// refusal must classify as.
	wantMandatoryReason string
	// wantRejectError requires the refusal to be the shipped pass's own typed
	// [toolcall.RejectError].
	wantRejectError bool
	// workspaceRoot overrides the authoritative root the runtime pins for the turn.
	// Empty selects the shared fixture root. It exists so a requirement 6.5 case can
	// pin a root that is NOT the one aliasRoot's alias was minted under, which is the
	// only shape in which a stale alias exists at all.
	workspaceRoot string
	// newRoot is the authoritative root this case pins. It is read by the REBINDING
	// oracle, which counts how many released selected path fields carry it: a stale
	// alias that was expanded against the new root would show up there, and a
	// workspace_mismatch refusal must leave it at zero. Empty disables that counter,
	// which is only correct for a case whose pinned root IS the shared fixture root.
	newRoot string
	// staleTag is the prior workspace's identity tag, read by the SAME oracle to
	// prove the stale alias is not handed back to the client as an ordinary path
	// either. Empty disables it.
	staleTag string
}

// effectiveRoot is the root prefix the model-emitted alias is actually built on for
// this case. It is derived rather than read from aliasRoot directly so a caller
// cannot build a guard against the wrong root for a malformed case.
func (tc expRefusalCase) effectiveRoot() string {
	if tc.malformedAlias != "" {
		return tc.malformedAlias
	}
	return tc.aliasRoot
}

// effectiveWorkspaceRoot is the authoritative root this case's run pins. It is
// derived the same way so the case cannot declare a root change the harness ignores.
func (tc expRefusalCase) effectiveWorkspaceRoot() string {
	if tc.workspaceRoot == "" {
		return twoPassRealRoot
	}
	return tc.workspaceRoot
}

// expRunRefusal drives one fail-closed case and returns the whole released stream
// plus the terminal error.
func expRunRefusal(t *testing.T, tc expRefusalCase) (expRunResult, expClientScan) {
	t.Helper()
	result := expRun(t, expScenario{
		label:             tc.label,
		aliasRoot:         tc.effectiveRoot(),
		expansionDecides:  true,
		assembleBody:      tc.assembleBody,
		extraFinalizers:   tc.extraFinalizers,
		observersExpected: false,
		workspaceRoot:     tc.workspaceRoot,
		// The request-side path is identical for every case, so the guards in expRun
		// that cover it are unconditional and already reported a clean fixture.
	})
	return result, expScanClientEvents(result.events, tc.newRoot, tc.staleTag)
}

// assertNoAliasReachedTheClient is the shared body of every fail-closed assertion.
//
// It is deliberately about the STREAM, not about a return value: the caller separately
// asserts that the refusal was typed and bounded, and this function proves that
// nothing alias-bearing rode along with it.
func assertNoAliasReachedTheClient(t *testing.T, tc expRefusalCase, result expRunResult, scan expClientScan, modelFixture expFixture) {
	t.Helper()
	// The marker oracle must have read every event. An unrendered event is an event
	// the literal-byte scan did not examine, so this guard runs before any zero below
	// is trusted.
	if scan.unrenderedEvents != 0 {
		t.Fatalf("fixture: %s: the marker oracle must be able to read every client event: events=%d unrendered=%d",
			tc.label, scan.events, scan.unrenderedEvents)
	}
	// The reserved namespace must not appear in ANY field of ANY client event, not
	// only in the selected path field. This is the literal-byte oracle, and it is
	// independent of the production recognizer used just below.
	if scan.markerEvents != 0 {
		t.Fatalf("requirements.md 4.4/4.5/8.3 - %s: the reserved namespace reached a client-facing event: events=%d marker_carrying_events=%d",
			tc.label, scan.events, scan.markerEvents)
	}
	// And the production recognizer must find no reserved alias in any selected path
	// field of any released argument document. documents/selectedFields are reported
	// so a stream that released nothing is visibly different from one that released a
	// clean document.
	if scan.reservedSelected != 0 {
		t.Fatalf("requirements.md 4.4/8.3 - %s: a selected path field still carried the reserved namespace: documents=%d selected_fields=%d reserved_selected=%d",
			tc.label, scan.documents, scan.selectedFields, scan.reservedSelected)
	}
	// A fail-closed tool call releases no lifecycle at all. A client that can observe a
	// started event without arguments, or a finished event with none, is exactly the
	// half-released state requirements.md 4.4 forbids.
	if scan.toolEvents != 0 {
		t.Fatalf("requirements.md 4.4/4.5/8.3 - %s: a refused tool call still released client-facing tool lifecycle events: events=%d tool_events=%d",
			tc.label, scan.events, scan.toolEvents)
	}
	if scan.argEvents != 0 || scan.argBytes != 0 {
		t.Fatalf("requirements.md 4.4/4.5/8.3 - %s: a refused tool call still released argument deltas: argument_events=%d argument_bytes=%d",
			tc.label, scan.argEvents, scan.argBytes)
	}
	if result.released != "" || result.lifecycle != 0 {
		t.Fatalf("requirements.md 4.4/8.3 - %s: a refused tool call still released joined arguments: released=%d bytes lifecycle_events=%d",
			tc.label, len(result.released), result.lifecycle)
	}
	if result.expResult.ArgsJSON != nil {
		t.Fatalf("requirements.md 4.4/8.3 - %s: a refusal must publish no document at all: published_bytes=%d",
			tc.label, len(result.expResult.ArgsJSON))
	}
	// The REBINDING oracle. Every zero above would still hold if the stale alias had
	// been EXPANDED against the new authoritative root rather than refused: the tool
	// lifecycle would be withheld either way, so only a direct count of released
	// selected path fields carrying the new root's bytes separates those two outcomes.
	// It reports counts only, and is disabled - by leaving the case's fields empty -
	// for every case that does not change the authoritative root.
	if scan.newRootFields != 0 {
		t.Fatalf("requirements.md 6.5 - %s: a released selected path field carries the NEW authoritative root's bytes: selected_fields=%d new_root_fields=%d documents=%d",
			tc.label, scan.selectedFields, scan.newRootFields, scan.documents)
	}
	if scan.staleTagFields != 0 {
		t.Fatalf("requirements.md 6.5 - %s: a released selected path field still carries the PRIOR workspace identity: selected_fields=%d stale_tag_fields=%d documents=%d",
			tc.label, scan.selectedFields, scan.staleTagFields, scan.documents)
	}
	if result.expCalls != tc.wantPassInvocations {
		t.Fatalf("fixture: %s: the shipped pass invocation count: got %d want %d",
			tc.label, result.expCalls, tc.wantPassInvocations)
	}
	if tc.wantPassInvocations == 1 {
		// The fixture must really have put the reserved namespace in front of the
		// shipped pass, and the pass must have refused on the bounded reason the design
		// names for this shape.
		if !strings.Contains(string(result.expSeen), expReservedMarker) {
			t.Fatalf("fixture: %s: the shipped pass must really have received an alias-bearing document: seen=%d bytes marker_in_seen=%t",
				tc.label, len(result.expSeen), strings.Contains(string(result.expSeen), expReservedMarker))
		}
		if result.expResult.Action != toolcall.ActionReject {
			t.Fatalf("fixture: %s: the shipped pass must refuse the call closed: action=%d", tc.label, int(result.expResult.Action))
		}
		reason, ok := expansion.ParseReason(result.expResult.ReasonCode)
		if !ok || reason != tc.wantPassReason {
			t.Fatalf("requirements.md 4.4/8.3 - %s: the refusal must carry the bounded reason the design names: reason=%q parseable=%t action=%d",
				tc.label, result.expResult.ReasonCode, ok, int(result.expResult.Action))
		}
	}
	if tc.wantRejectError {
		var reject *toolcall.RejectError
		if !errors.As(result.recvErr, &reject) || reject == nil {
			t.Fatalf("requirements.md 4.4/8.3 - %s: the turn must fail with the shipped pass's typed refusal, got %T",
				tc.label, result.recvErr)
		}
		if reject.ReasonCode != tc.wantPassReason.String() {
			t.Fatalf("requirements.md 4.4/8.3 - %s: the typed refusal must carry the same bounded reason: reason=%q",
				tc.label, reject.ReasonCode)
		}
	}
	if tc.wantMandatoryReason != "" {
		if !coreruntime.IsMandatoryBufferingError(result.recvErr) {
			t.Fatalf("requirements.md 4.5/4.6 - %s: the turn must fail with the assembler's typed refusal, got %T",
				tc.label, result.recvErr)
		}
		var refusal *coreruntime.MandatoryBufferingError
		if !errors.As(result.recvErr, &refusal) || refusal == nil {
			t.Fatalf("requirements.md 4.5/4.6 - %s: want a typed MandatoryBufferingError, got %T",
				tc.label, result.recvErr)
		}
		if refusal.Reason != tc.wantMandatoryReason {
			t.Fatalf("requirements.md 4.5/4.6 - %s: the assembler's bounded reason: got %q want %q",
				tc.label, refusal.Reason, tc.wantMandatoryReason)
		}
		if refusal.MaxArgsBytes != 0 && refusal.MaxArgsBytes > expOverflowBytes {
			t.Fatalf("requirements.md 4.5 - %s: the reported bound must be the effective assembly bound, not a fabricated one: reported=%d assembled=%d",
				tc.label, refusal.MaxArgsBytes, expOverflowBytes)
		}
		if strings.Contains(refusal.Error(), twoPassRealRoot) {
			t.Fatalf("requirements.md 7.7 - %s: the assembler's refusal text must be content-free", tc.label)
		}
	}
	// The model-emitted fixture must really have carried a path value under the
	// reserved namespace, or the case could pass because nothing was ever asked.
	if !strings.Contains(modelFixture.modelArgsDocument(), expReservedMarker) {
		t.Fatalf("fixture: %s: the model-emitted fixture must carry the reserved namespace", tc.label)
	}
}

// TestStreamToolCall_UnresolvedAliasAndMandatoryOverflowNeverReachClientEvents is
// requirements.md 4.4, 4.5, 4.6, and 8.3 over the whole client-facing event stream.
//
// It covers, in order:
//
//   - POSITIVE CONTROL: the resolvable alias DOES reach the client as the real path,
//     with the same scanner reporting one clean document. This is what makes every
//     zero below distinguishable from a stream that was never scanned;
//   - a reserved alias naming a DIFFERENT workspace, refused with the bounded
//     workspace_mismatch reason;
//   - a malformed reserved tag, refused with the bounded malformed_reserved_alias
//     reason - a different label over the same fixture family, so the two refusals
//     cannot be one blanket rule;
//   - a completed call whose assembled arguments exceed the DECLARED mandatory bound,
//     refused by the assembler with the typed overflow error;
//   - an unrelated optional finalizer failing BEFORE the declaring pass decides,
//     refused by the assembler rather than replaying the original fragments.
func TestStreamToolCall_UnresolvedAliasAndMandatoryOverflowNeverReachClientEvents(t *testing.T) {
	t.Parallel()

	resolvable := expFixture{root: hookRegAliasOf(t), suffix: expModelSuffix}

	// The stale/unresolved alias is the alias a DIFFERENT supported project root
	// derives. It is a well-formed reserved alias, so the only thing wrong with it is
	// that it does not name this workspace - which is requirements.md 6.5's
	// stale-workspace shape reached through 4.4 and 8.3.
	otherRoot := "/home/dev/workspaces/aip-path-virtualization-other-workspace/packages/agent-runtime"
	otherMapping, otherReason := pathvirtualization.DeriveMapping(otherRoot)
	if otherReason != pathvirtualization.SkipReasonNone || otherMapping.VirtualRoot == "" {
		t.Fatal("fixture: the second project root must derive an active mapping; its refusal code is bounded and content-free")
	}
	if otherMapping.VirtualRoot == resolvable.root {
		t.Fatal("fixture: the second project root must derive a DIFFERENT workspace tag")
	}

	// The malformed tag is a fixed reserved-namespace spelling whose tag segment is
	// not twenty characters of the frozen alphabet. It is not derived from any root, so
	// it can never accidentally become resolvable.
	const malformedAlias = "/.__aip_v1__/w_0123456789abcdefghij/"

	// The failing optional finalizer must sort BELOW the shipped expansion order for
	// the case to mean what it claims.
	if expFailingOrder >= expansion.FinalizerOrder {
		t.Fatalf("fixture: the unrelated failing finalizer must sort below the shipped expansion order: failing=%d expansion=%d",
			expFailingOrder, expansion.FinalizerOrder)
	}

	// The overflow fixture must exceed the declared default bound and stay inside the
	// canonical per-event limit, so the ONLY thing that can decide the call is the
	// mandatory bound.
	if expOverflowBytes <= toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("fixture: the overflow body must exceed the declared mandatory bound: body=%d bound=%d",
			expOverflowBytes, toolcall.DefaultMandatoryMaxArgsBytes)
	}
	if expOverflowBytes > aipapi.MaxEventDeltaBytes {
		t.Fatalf("fixture: the overflow body must stay inside the canonical payload ceiling: body=%d ceiling=%d",
			expOverflowBytes, aipapi.MaxEventDeltaBytes)
	}

	cases := []expRefusalCase{
		{
			label:               "alias_naming_another_workspace",
			aliasRoot:           otherMapping.VirtualRoot,
			wantPassInvocations: 1,
			wantPassReason:      expansion.ReasonWorkspaceMismatch,
			wantRejectError:     true,
		},
		{
			label:               "malformed_reserved_tag",
			malformedAlias:      malformedAlias,
			wantPassInvocations: 1,
			wantPassReason:      expansion.ReasonMalformedReservedAlias,
			wantRejectError:     true,
		},
		{
			// The declared mandatory bound is enforced by the ASSEMBLER, which refuses
			// before any finalizer is invoked, so the shipped pass is never reached here.
			label:               "assembled_arguments_past_the_declared_mandatory_bound",
			aliasRoot:           resolvable.root,
			assembleBody:        expOverflowBytes,
			wantPassInvocations: 0,
			wantMandatoryReason: coreruntime.ReasonMandatoryBufferingOverflow,
		},
		{
			// requirements.md 4.6: an unrelated optional finalizer fails before the
			// declaring pass decides, so the assembler's own chokepoint refuses and the
			// shipped pass is never reached.
			label:               "unrelated_optional_finalizer_failed_before_the_declaring_pass",
			aliasRoot:           resolvable.root,
			extraFinalizers:     []toolcall.Finalizer{&expFailingOrdinaryFinalizer{}},
			wantPassInvocations: 0,
			wantMandatoryReason: coreruntime.ReasonMandatoryBufferingIncomplete,
		},
	}

	// POSITIVE CONTROL. The identical fixture with the RESOLVABLE alias must release
	// exactly one expanded tool call, with the same scanner reporting one clean
	// selected field. Every zero asserted below is therefore a real negative and not
	// an artifact of an empty or unscanned stream.
	//
	// Note what the control does NOT assert: markerEvents is NOT zero here, and that is
	// CORRECT. The fixture's payload-concept member deliberately spells the reserved
	// namespace, and requirements.md 4.9 forbids expansion from touching it, so the
	// released document legitimately still carries the marker in a NON-selected field.
	// The control therefore pins the marker to the released argument document and pins
	// the SELECTED field to the real root - which is exactly the discrimination the
	// fail-closed cases turn around, and what proves the marker detector works on a
	// stream that really does carry the marker.
	t.Run("the_resolvable_alias_releases_one_clean_selected_field_for_the_same_scanner", func(t *testing.T) {
		result := expRun(t, expScenario{
			label:             "resolvable_alias_positive_control",
			aliasRoot:         resolvable.root,
			expansionDecides:  true,
			observersExpected: true,
		})
		scan := expScanClientEvents(result.events, "", "")
		if result.recvErr != nil {
			t.Fatalf("fixture: the resolvable alias must not fail the turn: %T", result.recvErr)
		}
		if scan.unrenderedEvents != 0 {
			t.Fatalf("fixture: the marker oracle must be able to read every client event: events=%d unrendered=%d",
				scan.events, scan.unrenderedEvents)
		}
		// Non-vacuity: the scanner must actually have read one selected path field out
		// of one released document, or every zero asserted below would be vacuous.
		if scan.documents != 1 || scan.selectedFields != 1 {
			t.Fatalf("fixture: the positive control must release exactly one readable selected path field: documents=%d selected_fields=%d argument_events=%d",
				scan.documents, scan.selectedFields, scan.argEvents)
		}
		if scan.reservedSelected != 0 {
			t.Fatalf("fixture: the positive control must release no reserved namespace in a selected path field: documents=%d selected_fields=%d reserved_selected=%d",
				scan.documents, scan.selectedFields, scan.reservedSelected)
		}
		if scan.realSelected != 1 {
			t.Fatalf("requirements.md 4.1 - the positive control must release the authoritative real root in the selected path field: real_selected=%d",
				scan.realSelected)
		}
		if scan.toolEvents != 3 || scan.argEvents != 1 {
			t.Fatalf("fixture: the positive control must release the synthesized canonical lifecycle: tool_events=%d argument_events=%d",
				scan.toolEvents, scan.argEvents)
		}
		if scan.markerEvents != scan.argEvents {
			t.Fatalf("requirements.md 4.9 - the payload-concept member's reserved namespace may survive expansion, but only inside the released argument document: marker_carrying_events=%d argument_events=%d events=%d",
				scan.markerEvents, scan.argEvents, scan.events)
		}
	})

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			result, scan := expRunRefusal(t, tc)
			if result.realRoot != tc.effectiveWorkspaceRoot() {
				t.Fatalf("fixture: %s: the run must pin the case's own authoritative root: root_changed=%t",
					tc.label, result.realRoot != twoPassRealRoot)
			}
			// design.md "Existing Architecture and Placement" steps 11 and 12 put tool-call
			// assembly/finalization before the tool policy and reactor planes, so a call
			// refused during finalization must never reach either observer. This is the
			// negative counterpart of the ordering proof in the before-policy test, and it
			// would catch a refusal that leaked past finalization into the step-12 planes.
			if result.policyCalls != 0 || result.reactorCalls != 0 {
				t.Fatalf("requirements.md 4.4/8.3 - a refused call must not reach the tool policy or reactor plane: policy_calls=%d reactor_calls=%d",
					result.policyCalls, result.reactorCalls)
			}
			if result.recvErr == nil {
				t.Fatalf("requirements.md 4.4/4.5/4.6/8.3 - %s: a call that cannot be expanded unambiguously must not complete: events=%d tool_events=%d argument_events=%d",
					tc.label, scan.events, scan.toolEvents, scan.argEvents)
			}
			if tc.wantPassInvocations == 1 {
				// The two unresolved-alias cases are decided by the shipped pass, so its
				// own bounded report must agree with the typed refusal the stream saw.
				if len(result.expReports) != 1 {
					t.Fatalf("fixture: %s: the shipped pass must emit exactly one bounded report: reports=%d",
						tc.label, len(result.expReports))
				}
				if result.expReports[0].Outcome != expansion.OutcomeRejected {
					t.Fatalf("requirements.md 7.6 - %s: the shipped report must be the content-free rejected outcome: outcome=%v",
						tc.label, result.expReports[0].Outcome)
				}
				if result.expReports[0].Reason != tc.wantPassReason {
					t.Fatalf("requirements.md 4.4/8.3 - %s: the shipped report's bounded reason: got %q want %q",
						tc.label, result.expReports[0].Reason, tc.wantPassReason)
				}
			}
			assertNoAliasReachedTheClient(t, tc, result, scan, expFixture{root: tc.effectiveRoot(), suffix: expModelSuffix})
		})
	}
}

package runtime_test

// Spec: b-leg-path-virtualization Task 10.2, conversation-view composition half.
// Requirements 2.8, 5.8, 5.9, 5.10, 8.6, 8.7.
//
// Design sections consulted: "Existing Architecture and Placement" (the ordered
// runtime stages and their consequences), "4A. Transform-Stable Conversation-View
// Reassertion" (its four authority rules and its seven constraints), "Ownership",
// and "Testing Strategy / Runtime-integration" plus "Protocol/certification".
//
// WHAT THIS FILE ADDS
//
// Tasks 1.3, 5.2, and 5.3 already characterized the anchor-identity hazard and
// repaired it, one anchored surface and one structural perturbation at a time.
// What was still missing is the CANONICAL AUTHORITY / FAMILY dimension:
//
//   1. A legacy chat history carrying BOTH a path-bearing tool call AND a path-bearing
//      tool result, driven through the real runtime, with every selected surface
//      measured at the per-turn buffer and at Backend.Open. The delivered fixtures
//      anchor exactly one of the two surfaces per run, so they cannot show that a
//      tool-result surface behaves correctly in a history that also carries a
//      tool-call surface.
//
//   2. The item-authoritative (OpenResponses) history. Its two path-bearing surfaces
//      are tool-call and tool-result ITEMS, which are NOT message-anchor identities
//      at all: AtomOfItem refuses every non-message item kind, ComputeItemAnchors and
//      ItemAnchorAt skip them, and resolveAnchorInItems never returns their index.
//      The lineage/anchor machinery therefore does not apply to them and must NOT be
//      forced to. This file proves that non-regression positively, at both the
//      canonical and the conversation-view seams.
//
//   3. The carried overlay, measured against the identity the backend-bound message
//      ACTUALLY has. Delivered Task 5.2 assertions show the placement survived; they
//      do not show that the anchor identity drifted at the same backend bound, so
//      "the placement was preserved" could not be distinguished from "nothing
//      happened". The premise is asserted here in the same run as the survival.
//
//   4. The ambiguous-lineage refusal, with the perturbation measured as running
//      AFTER the anchored identity drifted and BEFORE the reassertion, so the denial
//      is attributable to the structural proof and not to a never-drifted anchor. One
//      equal-cardinality anti-ordinal perturbation and one cardinality-changing
//      perturbation are used: a positional relocation would satisfy both, so their
//      refusal is what "never relocate by ordinal" means operationally.
//
// NO CARTESIAN COVERAGE
//
// This file pairs no frontend with any backend. It asserts canonical-authority
// properties (what the rewrite and the projection do to a trajectory) and then
// observes the single backend-bound surface the shared harness records. The protocol
// FAMILY half of this task - one representative adapter per family, never a matrix -
// lives in internal/plugins/features/pathvirtualization/protocol_family_sentinel_test.go,
// because core must not import protocol wire codecs even from a test.
//
// HARNESS REUSE
//
// There is exactly one end-to-end PTB/Backend.Open harness in this package and this
// file adds none. It reuses hookRegScenario/hookRegRun (the one harness), the Task 1.3
// fixture and observers (driftIngressCall, driftSnapshot, driftStoredAnchor,
// driftRewriter, driftAliasOf, driftFindAnchor, driftInspectPart, driftSteeringPlacement),
// the Task 5.3 lineage perturbation (hookRegPerturbationFor and its structural oracles),
// and the Task 5.2 report collectors. Only the canonical fixtures, the content-free
// probes those fixtures need, and the assertions are new.
//
// OBSERVABILITY
//
// No path, alias, workspace tag, tool-call identity, or argument byte ever reaches a
// failure message. Every observation is a boolean, a count, an index, or a stage
// ordinal; selected and unselected document members are compared, never printed.
//
// DETERMINISM
//
// Every stage ordinal comes from the shared monotonic recorder, every walk is over
// slices rather than maps, the pure seam involves no concurrency, and the executor RNG
// and clock are pinned by the shared harness, so -count=5 is stable.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ---------------------------------------------------------------------------
// Legacy chat family fixture: one history, two path-bearing surfaces.
// ---------------------------------------------------------------------------

const (
	// famLegacyFirstText is the leading path-free user message. Its identity is
	// stable across every outbound pass, which makes it the anchor target of the
	// overlay this fixture's positive run carries: if the anchor identity also
	// drifted, the survival assertions would be measuring the lineage carry-forward
	// instead of the ordinary exact-resolution path.
	famLegacyFirstText = "family-legacy-first-user"

	// famLegacyReasonCode and famLegacyOverlayID are bounded, content-free labels
	// for the one never_backend tag and the one active after-message overlay.
	famLegacyReasonCode = "legacy_family_fixture"
	famLegacyOverlayID  = "ov-legacy-family"

	// famLegacySteerText is the model-visible steering payload. It carries no path, so
	// the overlay's own identity is stable too.
	famLegacySteerText = "family-legacy-steering"

	// famLegacySlotOrdinal is the frozen placement the overlay is stored with.
	famLegacySlotOrdinal = 1
)

// famLegacySurfaces are the two path-bearing surfaces of the legacy chat history,
// located by Task 1.3's stable-identity locators so no assertion depends on an
// ordinal.
var famLegacySurfaces = []struct {
	label string
	loc   driftLocator
}{
	{driftSurfaceCallArgument, driftCallLocator()},
	{driftSurfaceResultStructured, driftResultContentLocator()},
}

// famLegacyCall builds the ingress call: one path-bearing historical tool call, one
// path-bearing historical tool result, one never_backend-tagged local note, and one
// terminal forwardable user message.
//
// The two path-bearing messages are the delivered Task 1.3 fixtures reused verbatim,
// so the argument and structured-result documents, their selected member, and their
// non-path siblings are the same ones every other characterization in this package
// measures. What is new is that they coexist in ONE history: a rewrite that only
// understood tool-call arguments, or one that only understood tool-result documents,
// would pass every other fixture in this package and fail here.
func famLegacyCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "two-pass:m"},
		Tools: []aipapi.ToolDef{{
			Name: twoPassToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + driftPathField +
				`":{"type":"string"},"limit":{"type":"integer"},"` + driftResultField +
				`":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(famLegacyFirstText)}},
			driftCallAnchorMessage(),
			driftResultContentAnchorMessage(),
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassTailText)}},
		},
	}
}

// famLegacySnapshot is the frozen per-turn conversation view for the legacy chat
// fixture: one never_backend tag plus one ACTIVE after-message steering overlay
// anchored, under the strict fail-closed anchor-missing policy, on the leading
// path-free user message.
//
// Anchoring on a path-free message is what keeps this fixture's survival assertions
// about the ORDINARY exact-resolution path. The identity-drift premise and the
// lineage carry-forward are measured separately, on the anchored path-bearing
// messages, by the carried-overlay test below.
func famLegacySnapshot(t *testing.T) conversationprojection.Snapshot {
	t.Helper()
	localID, err := conversationprojection.MessageIdentityOf(
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}})
	if err != nil {
		t.Fatalf("fixture: local message identity: %v", err)
	}
	anchorID, err := conversationprojection.MessageIdentityOf(
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(famLegacyFirstText)}})
	if err != nil {
		t.Fatalf("fixture: anchor message identity: %v", err)
	}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: famLegacyReasonCode}},
		Steering: []conversationprojection.Overlay{{
			OverlayID:   famLegacyOverlayID,
			Revision:    1,
			SlotOrdinal: famLegacySlotOrdinal,
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: famLegacySteerText},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		}},
	}
}

// famScenario is the one harness scenario for the legacy chat family. It installs no
// late shaping participant: this fixture asserts that the real early pass virtualizes
// both selected surfaces on its own, and an inserted complete message would additionally
// break the one-to-one lineage the projection depends on.
func famScenario(t *testing.T, label string, resolver func(*testing.T) *pathvirtualization.Resolver) hookRegScenario {
	t.Helper()
	return hookRegScenario{
		label:    label,
		call:     famLegacyCall,
		snapshot: func(*testing.T) conversationprojection.Snapshot { return famLegacySnapshot(t) },
		resolver: resolver,
		// Both real passes stay armed in every run, so the only variable between the
		// positive run and the loss oracle is the compiled policy.
		withLatePass: true,
	}
}

// famEmptyResolver is the non-vacuity oracle for the whole-body real-root scan: a
// compiled policy with no profiles and no schema-assisted inference, which selects no
// location for any tool. The identical fixture under this policy reaches Backend.Open
// carrying the real root on both path-bearing surfaces, so a scan that reported "no real
// root" against the positive run would be measuring nothing.
func famEmptyResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	resolver, reject := pathvirtualization.NewResolver(nil, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: an empty compiled policy must compile: reject=%q", reject)
	}
	return resolver
}

// famDocumentCompare is the content-free comparison of one selected-location
// document as the ingress call spells it and as the backend-bound call spells it.
//
// selectedChanged is the mutation requirement 5.2 asks for; unselectedEqual versus
// unselectedCount is requirement 2.8's byte-for-byte preservation of every member the
// rewrite did not select, expressed as counts so no member value is ever formatted.
type famDocumentCompare struct {
	ingressValid     bool
	backendValid     bool
	selectedAlias    bool
	selectedRealRoot bool
	selectedChanged  bool
	unselectedCount  int
	unselectedEqual  int
	memberCount      int
}

// famCompareDocument compares the ingress and backend-bound forms of one
// selected-location document.
//
// selected names the member the policy claims; alias is the derived alias, which is
// compared and never formatted. Every other member must be byte-identical, which is
// the strongest available statement of requirement 2.8 for a document whose member set
// the fixture does not enumerate in the assertion.
func famCompareDocument(ingress, backend []byte, selected, alias string) famDocumentCompare {
	out := famDocumentCompare{}
	var before, after map[string]json.RawMessage
	if json.Unmarshal(ingress, &before) != nil {
		return out
	}
	if json.Unmarshal(backend, &after) != nil {
		return out
	}
	out.ingressValid = true
	out.backendValid = true
	out.memberCount = len(after)
	var ingressSelected, backendSelected string
	if json.Unmarshal(before[selected], &ingressSelected) != nil {
		out.ingressValid = false
	}
	if json.Unmarshal(after[selected], &backendSelected) != nil {
		out.backendValid = false
	}
	out.selectedAlias = strings.Contains(backendSelected, alias)
	out.selectedRealRoot = strings.Contains(backendSelected, twoPassRealRoot)
	out.selectedChanged = ingressSelected != backendSelected
	for name, raw := range before {
		if name == selected {
			continue
		}
		out.unselectedCount++
		if bytes.Equal(raw, after[name]) {
			out.unselectedEqual++
		}
	}
	return out
}

// famSelectedDocument returns the selected-location document for one locator from a
// canonical call, using Task 1.3's stable-identity locator rather than an ordinal.
func famSelectedDocument(call aipapi.Call, loc driftLocator) ([]byte, bool) {
	msg, ok := driftFindAnchor(call, loc)
	if !ok {
		return nil, false
	}
	for _, part := range msg.Parts {
		if part.Kind != loc.kind || part.ToolCallID != loc.id {
			continue
		}
		if loc.field == driftSelectedText {
			return []byte(part.Text), part.Text != ""
		}
		return part.Content, len(part.Content) > 0
	}
	return nil, false
}

// famToolIdentityPreserved reports whether the anchored message still names the exact
// canonical tool identity it was sent with. Comparing against a constant is
// content-free; formatting the value would not be.
func famToolIdentityPreserved(call aipapi.Call, loc driftLocator) bool {
	msg, ok := driftFindAnchor(call, loc)
	if !ok {
		return false
	}
	for _, part := range msg.Parts {
		if part.Kind != loc.kind || part.ToolCallID != loc.id {
			continue
		}
		return part.ToolName == twoPassToolName
	}
	return false
}

// famRealRootFree is the whole-body requirement 5.2 oracle: no byte of the
// backend-bound request may carry the authoritative real root.
func famRealRootFree(call aipapi.Call) (free bool, occurrences int) {
	raw, err := json.Marshal(call)
	if err != nil {
		return false, -1
	}
	n := strings.Count(string(raw), twoPassRealRoot)
	return n == 0, n
}

// TestConversationView_LegacyChatHistoryVirtualizesBothPathBearingSurfaces is
// requirements.md 2.8, 5.2, 5.4, 5.8, and 8.7 on the legacy chat (message authority)
// family with a tool call AND a tool result in the same history.
//
// The delivered Task 1.3 and Task 5.2 fixtures anchor exactly one path-bearing message
// per run, so neither can distinguish "the tool-call argument surface works" from "the
// structured tool-result surface works" when both are present. This fixture drives the
// real executor with both real outbound passes and the feature's real rewriter, then
// measures each selected surface at the per-turn buffer and at Backend.Open:
//
//   - both selected members carry the derived alias and no real-root occurrence;
//   - every unselected member of both documents survives byte-for-byte;
//   - the stable tool-call identity and canonical tool name survive on both surfaces,
//     which is what requirement 2.8's "preserve tool call IDs, tool names" means;
//   - no byte of the whole backend-bound request carries the real root;
//   - the real early pass selected and rewrote exactly the two expected occurrences and
//     the idempotent late pass contributed nothing (requirement 2.9);
//   - the frozen conversation view still restores its overlay at its placement and
//     still filters the never_backend note, so the final reassertion and candidate
//     adaptation are real stages in this run.
//
// The rewrite-loss oracle is the shared harness's: the identical fixture whose late
// pass is reached but inert reaches Backend.Open carrying at least one real path,
// which is exactly what a missing, misordered, or non-contributing late pass, or a
// rewriter that only understood one of the two surfaces, would reproduce.
func TestConversationView_LegacyChatHistoryVirtualizesBothPathBearingSurfaces(t *testing.T) {
	t.Parallel()

	control := hookRegRun(t, famScenario(t, "legacy_chat_family_history_nothing_selected", famEmptyResolver))
	got := hookRegRun(t, famScenario(t, "legacy_chat_family_history", driftStructuredResultResolver))
	order := hookRegOrderOf(got)

	// Scaffolding. Every assertion below reads these.
	if !control.turnDone {
		t.Fatalf("fixture: the run whose compiled policy selects nothing must still complete one turn: %s", hookRegOrderOf(control))
	}
	if !got.turnDone {
		t.Fatalf("fixture: the legacy chat family run must complete one turn with no pre-backend denial: %s", order)
	}
	if got.snapshotReads != 1 {
		t.Fatalf("fixture: the frozen conversation view must be read exactly once: snapshot_reads=%d %s", got.snapshotReads, order)
	}
	if got.order.reassert == 0 || got.order.ptb == 0 || got.order.open == 0 {
		t.Fatalf("fixture: the reassertion, per-turn buffer, and Backend.Open stages must all be reached: %s", order)
	}

	ingress := *famLegacyCall()
	alias := driftAliasOf(t)
	for _, surface := range famLegacySurfaces {
		ingressDoc, ok := famSelectedDocument(ingress, surface.loc)
		if !ok {
			t.Fatalf("fixture: the ingress history must carry the selected document for surface=%s", surface.label)
		}
		controlDoc, ok := famSelectedDocument(control.backendOpenCall, surface.loc)
		if !ok {
			t.Fatalf("fixture: the control must reach Backend.Open with the surface=%s message present", surface.label)
		}
		// The rewrite-loss oracle, per surface: with a policy that selects nothing,
		// that same selected member is still a real path at the backend bound. A
		// measurement that reported "virtualized" without being able to report the
		// opposite would be measuring nothing.
		controlCmp := famCompareDocument(ingressDoc, controlDoc, driftPathField, alias)
		if !controlCmp.selectedRealRoot || controlCmp.selectedAlias {
			t.Fatalf("fixture: the control's Backend.Open must still carry a real path and no alias for surface=%s, otherwise the loss oracle below proves nothing: selected_real_root=%t selected_carries_alias=%t %s",
				surface.label, controlCmp.selectedRealRoot, controlCmp.selectedAlias, hookRegOrderOf(control))
		}

		for _, stage := range []struct {
			name string
			call aipapi.Call
		}{
			{"per_turn_buffer", hookRegCallFromBody(got.ptbBody)},
			{"Backend.Open", got.backendOpenCall},
		} {
			backendDoc, ok := famSelectedDocument(stage.call, surface.loc)
			if !ok {
				t.Fatalf("requirements.md 2.8 - the %s surface must survive both real passes and candidate adaptation: stage=%s surface=%s",
					surface.label, stage.name, surface.label)
			}
			cmp := famCompareDocument(ingressDoc, backendDoc, driftPathField, alias)
			if !cmp.ingressValid || !cmp.backendValid {
				t.Fatalf("requirements.md 2.8 - both forms of the %s document must be one complete JSON value: stage=%s surface=%s ingress_valid=%t backend_valid=%t",
					surface.label, stage.name, surface.label, cmp.ingressValid, cmp.backendValid)
			}
			// The same assertion for both stages, one iteration each, so a failure
			// names the surface and the stage it happened at.
			if !cmp.selectedAlias || cmp.selectedRealRoot {
				t.Errorf("requirements.md 5.2 and 5.4 - final reassertion and candidate adaptation must not restore a real root into %s tool history: stage=%s selected_carries_alias=%t selected_carries_real_root=%t %s",
					surface.label, stage.name, cmp.selectedAlias, cmp.selectedRealRoot, order)
				continue
			}
			if !cmp.selectedChanged {
				t.Errorf("requirements.md 5.2 - the %s selected member must actually have been virtualized: stage=%s member_count=%d",
					surface.label, stage.name, cmp.memberCount)
				continue
			}
			if cmp.unselectedEqual != cmp.unselectedCount || cmp.unselectedCount == 0 {
				t.Errorf("requirements.md 2.8 - every non-selected member of the %s payload must survive byte-for-byte, and the fixture must declare at least one: stage=%s unselected_equal=%d unselected_count=%d",
					surface.label, stage.name, cmp.unselectedEqual, cmp.unselectedCount)
				continue
			}
			if !famToolIdentityPreserved(stage.call, surface.loc) {
				t.Errorf("requirements.md 2.8 - the %s stable tool-call identity and canonical tool name must survive: stage=%s surface=%s",
					surface.label, stage.name, surface.label)
			}
		}
	}

	// The whole-body oracle: requirement 5.2 says the backend-effective request
	// contains virtualized path-bearing tool history. A whole-body scan catches a
	// restoration anywhere, not only in the two located surfaces.
	if free, occurrences := famRealRootFree(got.backendOpenCall); !free {
		t.Errorf("requirements.md 5.2 - no byte of the backend-bound request may carry the authoritative real root: real_root_occurrences=%d %s",
			occurrences, order)
	}

	// Both selected surfaces in one history means exactly two occurrences, one per
	// surface, and the idempotent late pass contributes nothing once the early pass
	// has published the alias.
	early := got.reports.oneAttempt(t)
	if early.Stats.Eligible != len(famLegacySurfaces) || early.Stats.Rewritten != len(famLegacySurfaces) {
		t.Errorf("fixture: the real early pass must select and virtualize exactly one occurrence per path-bearing surface: surface_count=%d eligible=%d rewritten=%d",
			len(famLegacySurfaces), early.Stats.Eligible, early.Stats.Rewritten)
	}
	if early.Stats.BytesSaved() <= 0 {
		t.Errorf("fixture: the real early pass must report a positive byte saving: bytes_saved=%d", early.Stats.BytesSaved())
	}
	if late := got.reports.onePart(t); late.Stats.Rewritten != 0 {
		t.Errorf("requirements.md 2.9 - the idempotent late pass must contribute nothing once the early pass published the alias: late_rewritten=%d",
			late.Stats.Rewritten)
	}

	// The frozen conversation view stayed authoritative: the overlay is restored once
	// at its placement and the never_backend note is still absent, so the reassertion
	// and adaptation above were real stages rather than no-ops.
	copies, immediatelyAfter := famLegacySteeringPlacement(got.backendOpenCall)
	if copies != 1 || !immediatelyAfter {
		t.Errorf("design.md \"4A\" constraint 1 - overlay lifecycle must be unchanged: the projection-owned overlay must be present exactly once at its frozen placement at Backend.Open: copies=%d immediately_after_anchor=%t %s",
			copies, immediatelyAfter, order)
	}
	if _, present := driftFindTextMessage(got.backendOpenCall, twoPassLocalText); present {
		t.Errorf("design.md \"4A\" constraint 6 - a never_backend-tagged message must stay out of the backend-bound request %s", order)
	}
	if got.order.reassert >= got.order.ptb || got.order.ptb >= got.order.open {
		t.Errorf("requirements.md 5.2/5.4 - the final reassertion must complete before the per-turn buffer, which must complete before Backend.Open: %s", order)
	}
}

// famLegacySteeringPlacement measures the steering overlay's placement relative to the
// fixture's path-free anchor, reusing Task 1.3's content-free locator.
func famLegacySteeringPlacement(call aipapi.Call) (copies int, immediatelyAfter bool) {
	copies = 0
	anchorAt := -1
	steerAt := -1
	for i, msg := range call.Messages {
		for _, part := range msg.Parts {
			switch {
			case part.Kind == aipapi.PartText && part.Text == famLegacySteerText:
				if steerAt < 0 {
					steerAt = i
				}
				copies++
			case part.Kind == aipapi.PartText && part.Text == famLegacyFirstText && anchorAt < 0:
				anchorAt = i
			}
		}
	}
	return copies, anchorAt >= 0 && steerAt == anchorAt+1
}

// ---------------------------------------------------------------------------
// Item-authoritative (OpenResponses) family fixture.
// ---------------------------------------------------------------------------

const (
	// famItemFirstText and famItemTailText are the two path-free message items. Only
	// message items can carry an anchor identity, so these are the only items the
	// conversation-view machinery can name at all.
	famItemFirstText = "family-item-first-user"
	famItemTailText  = "family-item-continue"

	// famItemReasonCode and famItemOverlayID bound the one never_backend tag and the
	// one active after-message overlay of the item-authoritative fixture.
	famItemReasonCode = "item_family_fixture"
	famItemOverlayID  = "ov-item-family"
	famItemSteerText  = "family-item-steering"

	// famItemFirstID and famItemLocalID are the stable item identities the fixture
	// addresses items by. They are compared, never formatted.
	famItemFirstID  = "fam-item-first"
	famItemLocalID  = "fam-item-local"
	famItemCallID   = "fam-item-call"
	famItemCallID_2 = "fam-item-result"
)

// famItemIndices are the fixture's fixed item slots, addressed by construction rather
// than by search so a structural change to the fixture cannot silently move them.
const (
	famItemMessageFirst = 0
	famItemToolCall     = 1
	famItemToolResult   = 2
	famItemMessageLocal = 3
	famItemMessageTail  = 4
)

// famItemCall builds the item-authoritative ingress call: two path-free message items,
// one path-bearing tool-call item, one path-bearing tool-result item, one
// never_backend-tagged message item, and one terminal message item.
//
// The tool-result item carries its selected location as a structured JSON content part
// and no Output, which is the shape requirement 2.2 and 3.2 address and the shape the
// item-authority rewriter path selects through ResultJSONPointers.
func famItemCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "two-pass:m"},
		Tools: []aipapi.ToolDef{{
			Name: twoPassToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + driftPathField +
				`":{"type":"string"},"limit":{"type":"integer"},"` + driftResultField +
				`":{"type":"integer"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      famItemFirstID,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: famItemFirstText}},
			},
			{
				Kind: aipapi.ItemKindToolCall,
				ID:   famItemCallID,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    famItemCallID,
					Name:      twoPassToolName,
					Arguments: json.RawMessage(driftArgumentDocument()),
				},
			},
			{
				Kind: aipapi.ItemKindToolResult,
				ID:   famItemCallID_2,
				ToolResult: &aipapi.ToolResultItem{
					CallID: famItemCallID,
					Name:   twoPassToolName,
					Parts: []aipapi.ContentPart{{
						Kind: aipapi.ContentPartJSON,
						Text: string(driftStructuredResultDocument()),
					}},
				},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      famItemLocalID,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: twoPassLocalText}},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "fam-item-tail",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: famItemTailText}},
			},
		},
	}
}

// famItemSnapshot is the frozen per-turn view for the item-authoritative fixture: one
// never_backend tag on the local message item plus one ACTIVE after-message overlay
// anchored, under the strict fail-closed policy, on the leading path-free message
// item.
//
// The anchor is a message item on purpose. Requirement 5.9's carry-forward exists for
// a complete message whose content hash drifted; an item-authoritative trajectory's
// path-bearing surfaces are tool-call and tool-result ITEMS, which carry no anchor
// identity at all, so anchoring the overlay on one would force machinery onto a shape
// it does not own.
func famItemSnapshot(t *testing.T) conversationprojection.Snapshot {
	t.Helper()
	localID, err := conversationprojection.ItemIdentityOf(famItemCall().Items[famItemMessageLocal])
	if err != nil {
		t.Fatalf("fixture: local message item identity: %v", err)
	}
	anchorID, err := conversationprojection.ItemIdentityOf(famItemCall().Items[famItemMessageFirst])
	if err != nil {
		t.Fatalf("fixture: anchor message item identity: %v", err)
	}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: famItemReasonCode}},
		Steering: []conversationprojection.Overlay{{
			OverlayID:   famItemOverlayID,
			Revision:    1,
			SlotOrdinal: 1,
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: famItemSteerText},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		}},
	}
}

// famItemMessageAnchorIdentity returns the anchor identity the stored overlay names.
func famItemMessageAnchorIdentity(t *testing.T) conversationprojection.MessageAnchor {
	t.Helper()
	id, err := conversationprojection.ItemIdentityOf(famItemCall().Items[famItemMessageFirst])
	if err != nil {
		t.Fatalf("fixture: anchor message item identity: %v", err)
	}
	return conversationprojection.MessageAnchor{Identity: id, Occurrence: 1}
}

// The two bounded labels the item-authoritative assertions use for the path-bearing
// surfaces. They are stable tokens; the items' stable identities are never formatted.
const (
	famItemToolCallSlot   = "item_tool_call_arguments"
	famItemToolResultSlot = "item_tool_result_structured_content"
)

// famItemSurfaces are the two path-bearing item surfaces, in a fixed order so counts are
// comparable and never map-ordered.
var famItemSurfaces = []string{famItemToolCallSlot, famItemToolResultSlot}

// famItemDocumentsOf returns the two path-bearing documents of an item-authoritative
// call, each located by the tool-call identity it carries rather than by an ordinal.
//
// Ordinal addressing is deliberately not used: the reasserted trajectory carries the
// injected overlay item, so an index-based lookup would report a clean scan for the wrong
// item the moment the projection inserts anything. Locating by the stable tool-call
// identity keeps every measurement tied to the surface it names. A missing surface is
// reported through ok so a structural change fails loudly instead of reading as clean.
func famItemDocumentsOf(call aipapi.Call) (map[string][]byte, bool) {
	out := map[string][]byte{}
	for _, item := range call.Items {
		switch {
		case item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil &&
			item.ToolCall.CallID == famItemCallID:
			if len(item.ToolCall.Arguments) == 0 {
				return nil, false
			}
			out[famItemToolCallSlot] = item.ToolCall.Arguments
		case item.Kind == aipapi.ItemKindToolResult && item.ToolResult != nil &&
			item.ToolResult.CallID == famItemCallID:
			if len(item.ToolResult.Parts) != 1 ||
				item.ToolResult.Parts[0].Kind != aipapi.ContentPartJSON {
				return nil, false
			}
			out[famItemToolResultSlot] = []byte(item.ToolResult.Parts[0].Text)
		}
	}
	if len(out) != len(famItemSurfaces) {
		return nil, false
	}
	return out, true
}

// famItemMessageUnchanged reports whether every message item of the rewritten call is
// byte-identical to its ingress form. Requirement 2.7 forbids this feature from
// inspecting or rewriting ordinary message content, so on the item authority the two
// path-free message items must come through the rewrite untouched.
func famItemMessageUnchanged(before, after aipapi.Call) (compared int, equal bool) {
	equal = true
	if len(before.Items) != len(after.Items) {
		return 0, false
	}
	for i := range before.Items {
		if before.Items[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		left, errLeft := json.Marshal(before.Items[i])
		right, errRight := json.Marshal(after.Items[i])
		compared++
		if errLeft != nil || errRight != nil || !bytes.Equal(left, right) {
			equal = false
		}
	}
	return compared, equal
}

// famItemSteeringPlacement measures the overlay's placement relative to the anchored
// message item, reusing the anchor's stable item identity rather than an ordinal so no
// assertion depends on the injected copy's index.
func famItemSteeringPlacement(call aipapi.Call, anchor conversationprojection.MessageAnchor) (copies int, immediatelyAfter bool) {
	anchorAt := -1
	steerAt := -1
	for i, item := range call.Items {
		if item.Kind != aipapi.ItemKindMessage || len(item.Content) != 1 {
			continue
		}
		if item.Content[0].Kind == aipapi.ContentPartText && item.Content[0].Text == famItemSteerText {
			if steerAt < 0 {
				steerAt = i
			}
			copies++
			continue
		}
		if id, err := conversationprojection.ItemIdentityOf(item); err == nil && id == anchor.Identity {
			anchorAt = i
		}
	}
	return copies, anchorAt >= 0 && steerAt == anchorAt+1
}

// famItemPresent reports whether a message item carrying the given text survived.
func famItemPresent(call aipapi.Call, text string) bool {
	for _, item := range call.Items {
		if item.Kind != aipapi.ItemKindMessage {
			continue
		}
		for _, part := range item.Content {
			if part.Kind == aipapi.ContentPartText && part.Text == text {
				return true
			}
		}
	}
	return false
}

// TestConversationView_ItemAuthoritativeHistoryNeedsNoLineageCarryForward is
// requirements.md 2.8, 5.9, and 5.10 on the item-authoritative (OpenResponses) history,
// and the explicit non-regression argument that tool-call and tool-result ITEMS are not
// message-anchor identities.
//
// The point of the test is a negative one, stated positively. On the item authority the
// trajectory is a sequence of items, and only ItemKindMessage items carry an anchor
// identity at all: AtomOfItem refuses every other kind with ErrNonMessageItem,
// ComputeItemAnchors skips them, ItemAnchorAt refuses them, and resolveAnchorInItems
// never returns their index. Requirement 5.9's carry-forward therefore has nothing to
// carry for those two items, and requirement 5.10's fail-closed rule has nothing to
// guess about them. The correct behavior is to virtualize them exactly like their
// message-authority counterparts and to leave the anchor machinery alone.
//
// The test proves:
//
//   - both path-bearing item surfaces are selected and virtualized, with every
//     unselected member byte-for-byte intact and the stable item identities, call
//     identities, tool names, and item order intact;
//   - the two message items come through the rewrite byte-identical (requirement 2.7);
//   - the two tool items are NOT anchor identities, before or after the rewrite, so
//     anchoring machinery is not applicable to them and is not forced onto them;
//   - the final reassertion restores the overlay exactly once at the anchored message
//     item's boundary through the ORDINARY exact-resolution path, because that message
//     item's identity did not drift: the stored anchor is asserted to equal the
//     post-rewrite identity, which is what makes "no lineage was needed" a fact rather
//     than an assumption;
//   - never_backend filtering still removed exactly the tagged message item, and the
//     reasserted call still passes canonical validation and adaptation verification.
func TestConversationView_ItemAuthoritativeHistoryNeedsNoLineageCarryForward(t *testing.T) {
	t.Parallel()

	ingress := famItemCall()
	if err := ingress.Validate(); err != nil {
		t.Fatalf("fixture: the item-authoritative ingress call must be canonically valid: %v", err)
	}
	snap := famItemSnapshot(t)
	stored := famItemMessageAnchorIdentity(t)
	alias := driftAliasOf(t)

	// -----------------------------------------------------------------------
	// THE NON-FORCING ARGUMENT, AS A DURABLE ASSERTION.
	//
	// Nothing here changes when path virtualization is added, removed, or extended,
	// so it is asserted first and everything else is read against it: the tool-call
	// and tool-result ITEMS are not message-anchor identities, before or after the
	// rewrite.
	// -----------------------------------------------------------------------
	rewritten, stats, err := driftRewriter(t, driftStructuredResultResolver(t)).RewriteCall(ingress)
	if err != nil {
		t.Fatalf("fixture: the real item-authority rewrite: %v", err)
	}
	if rewritten == ingress {
		t.Fatalf("fixture: the real rewriter must publish a changed call for this fixture, otherwise nothing below measures a mutation")
	}
	if err := rewritten.Validate(); err != nil {
		t.Fatalf("fixture: the rewritten item-authoritative call must stay canonically valid: %v", err)
	}

	famItemAnchor := famItemMessageAnchorIdentity(t)
	for _, stage := range []struct {
		name string
		call *aipapi.Call
	}{
		{"ingress", ingress},
		{"rewritten", rewritten},
	} {
		for _, slot := range []struct {
			index int
			kind  aipapi.ItemKind
		}{
			{famItemToolCall, aipapi.ItemKindToolCall},
			{famItemToolResult, aipapi.ItemKindToolResult},
		} {
			item := stage.call.Items[slot.index]
			if item.Kind != slot.kind {
				t.Fatalf("fixture: item slot %d must be a %s on the %s call: kind=%s", slot.index, slot.kind, stage.name, item.Kind)
			}
			if _, err := conversationprojection.ItemIdentityOf(item); !errors.Is(err, conversationprojection.ErrNonMessageItem) {
				t.Fatalf("design.md \"4A\" and requirements.md 5.10 - a %s item must not be usable as a message anchor identity: stage=%s item_identity_error_is_non_message=%t",
					slot.kind, stage.name, errors.Is(err, conversationprojection.ErrNonMessageItem))
			}
			if _, err := conversationprojection.ItemAnchorAt(stage.call.Items, slot.index); !errors.Is(err, conversationprojection.ErrNonMessageItem) {
				t.Fatalf("design.md \"4A\" and requirements.md 5.10 - ItemAnchorAt must refuse a %s item instead of anchoring the overlay on it: stage=%s item_anchor_error_is_non_message=%t",
					slot.kind, stage.name, errors.Is(err, conversationprojection.ErrNonMessageItem))
			}
		}
		anchors, err := conversationprojection.ComputeItemAnchors(stage.call.Items)
		if err != nil {
			t.Fatalf("fixture: ComputeItemAnchors on the %s call: %v", stage.name, err)
		}
		// Only the message items can be anchors, so the count is the message-item count
		// and not the trajectory length. The two tool slots are the difference between
		// the two counts, which is the whole point.
		if len(anchors) != len(famItemMessageIndexes) || len(anchors) >= len(stage.call.Items) {
			t.Fatalf("design.md \"4A\" and requirements.md 5.10 - only message items may produce an anchor identity, so the anchor count must be the message-item count and strictly below the trajectory length: stage=%s anchors=%d message_items=%d items=%d",
				stage.name, len(anchors), len(famItemMessageIndexes), len(stage.call.Items))
		}
		for _, anchor := range anchors {
			if _, found, err := conversationprojection.ResolveAnchor(stage.call.Items, anchor); err != nil || !found {
				t.Fatalf("fixture: every computed anchor must resolve on the %s call: resolved=%t err=%v", stage.name, found, err)
			}
		}
		// The anchor the fixture actually stores resolves to the leading MESSAGE item
		// and to nothing else, which is the property the carry-forward path would have
		// to violate to be involved at a tool slot at all.
		anchorIdx, anchorFound, anchorErr := conversationprojection.ResolveAnchor(stage.call.Items, famItemAnchor)
		if anchorErr != nil || !anchorFound || anchorIdx != famItemMessageFirst {
			t.Fatalf("fixture: the stored anchor must resolve to the leading message item only: stage=%s resolved_index=%d resolved=%t err=%v",
				stage.name, anchorIdx, anchorFound, anchorErr)
		}
	}

	// -----------------------------------------------------------------------
	// THE TWO PATH-BEARING ITEM SURFACES ARE VIRTUALIZED EXACTLY LIKE THEIR
	// MESSAGE-AUTHORITY COUNTERPARTS.
	// -----------------------------------------------------------------------
	if stats.Eligible != len(famLegacySurfaces) || stats.Rewritten != len(famLegacySurfaces) {
		t.Fatalf("fixture: the real item-authority rewrite must select and virtualize exactly one occurrence per path-bearing item surface: surface_count=%d eligible=%d rewritten=%d bytes_saved=%d",
			len(famLegacySurfaces), stats.Eligible, stats.Rewritten, stats.BytesSaved())
	}
	ingressDocs, ok := famItemDocumentsOf(*ingress)
	if !ok {
		t.Fatalf("fixture: the ingress item call must carry both path-bearing documents at their fixed slots")
	}
	rewrittenDocs, ok := famItemDocumentsOf(*rewritten)
	if !ok {
		t.Fatalf("fixture: the rewritten item call must carry both path-bearing documents at their fixed slots")
	}
	for _, slot := range []string{famItemToolCallSlot, famItemToolResultSlot} {
		cmp := famCompareDocument(ingressDocs[slot], rewrittenDocs[slot], driftPathField, alias)
		if !cmp.ingressValid || !cmp.backendValid {
			t.Fatalf("fixture: both forms of the %s document must be one complete JSON value: ingress_valid=%t backend_valid=%t",
				slot, cmp.ingressValid, cmp.backendValid)
		}
		if !cmp.selectedAlias || cmp.selectedRealRoot || !cmp.selectedChanged {
			t.Fatalf("requirements.md 2.1, 2.2, and 5.2 - the %s selected member must carry the derived alias and no real root: selected_carries_alias=%t selected_carries_real_root=%t selected_changed=%t",
				slot, cmp.selectedAlias, cmp.selectedRealRoot, cmp.selectedChanged)
		}
		if cmp.unselectedEqual != cmp.unselectedCount || cmp.unselectedCount == 0 {
			t.Fatalf("requirements.md 2.8 - every non-selected member of the %s payload must survive byte-for-byte, and the fixture must declare at least one: unselected_equal=%d unselected_count=%d",
				slot, cmp.unselectedEqual, cmp.unselectedCount)
		}
	}
	if compared, equal := famItemMessageUnchanged(*ingress, *rewritten); !equal || compared == 0 {
		t.Fatalf("requirements.md 2.7 - ordinary message content must not be inspected or rewritten on the item authority: message_items_compared=%d all_byte_identical=%t",
			compared, equal)
	}
	if !famItemShapeStable(*ingress, *rewritten) {
		t.Fatalf("requirements.md 2.8 - item ordering, item count, stable item identities, tool-call identities, and tool names must survive the item-authority rewrite")
	}

	// -----------------------------------------------------------------------
	// THE CONVERSATION VIEW IS UNAFFECTED, AND EXACT RESOLUTION IS THE NORMAL PATH.
	// -----------------------------------------------------------------------
	// Projection and reassertion errors are reduced to booleans, never printed: a denial
	// reason can carry an overlay identity and a message-identity digest, and requirement
	// 7.7 keeps both out of every observable dimension. This mirrors how the delivered
	// Task 1.3 harness reduces Execute's error for the same reason.
	projected, evidence, projErr := conversationprojection.Project(*ingress, snap)
	if projErr != nil {
		t.Fatalf("requirements.md 5.10 - the after-message anchor must resolve against the item-authoritative A-leg truth during early projection: anchor_missing=%t projection_failed=%t",
			errors.Is(projErr, conversationprojection.ErrAnchorMissing),
			errors.Is(projErr, conversationprojection.ErrProjectionFailed))
	}
	if evidence.FilteredCount != 1 {
		t.Fatalf("design.md \"4A\" constraint 6 - never_backend filtering must remain authoritative: filtered_count=%d", evidence.FilteredCount)
	}
	if len(evidence.Provenance) != 1 || evidence.Provenance[0].ResolvedKind != conversationprojection.PlacementAfterMessage {
		t.Fatalf("fixture: early projection must record exactly one after-message overlay provenance entry: provenance_count=%d", len(evidence.Provenance))
	}
	if evidence.Provenance[0].ResolvedAnchor == nil || *evidence.Provenance[0].ResolvedAnchor != stored {
		t.Fatalf("design.md \"4A\" constraint 1 - early projection must carry the stored anchor forward verbatim, not a re-derived one: resolved_anchor_matches_stored=%t",
			evidence.Provenance[0].ResolvedAnchor != nil && *evidence.Provenance[0].ResolvedAnchor == stored)
	}
	filtered, err := conversationprojection.FilterNeverBackend(*ingress, snap)
	if err != nil {
		t.Fatalf("fixture: FilterNeverBackend over the item-authoritative call: projection_failed=%t",
			errors.Is(err, conversationprojection.ErrProjectionFailed))
	}

	// The item authority needs the same frozen filtered baseline the message authority
	// does; supplying it here proves the reassertion had every piece of request-local
	// evidence available and still did not need to carry anything.
	cleaned, _, err := driftRewriter(t, driftStructuredResultResolver(t)).RewriteCall(&projected)
	if err != nil {
		t.Fatalf("fixture: the real rewrite of the early projection: %v", err)
	}
	anchorIdentityAfter, err := conversationprojection.ItemIdentityOf(cleaned.Items[famItemMessageFirst])
	if err != nil {
		t.Fatalf("fixture: anchor message item identity after the rewrite: %v", err)
	}
	if anchorIdentityAfter != stored.Identity {
		t.Fatalf("fixture: the anchored message item carries no path, so its identity must not drift; the item authority's survival case is the ordinary exact-resolution path, not the carry-forward: stored_anchor_equals_post_rewrite_identity=%t",
			anchorIdentityAfter == stored.Identity)
	}
	if _, found, err := conversationprojection.ResolveAnchor(cleaned.Items, stored); err != nil || !found {
		t.Fatalf("fixture: the stored anchor must resolve against the rewritten trajectory by itself, which is what makes exact resolution the normal path: resolved=%t err=%v", found, err)
	}

	reasserted, reassertEvidence, reassertErr := conversationprojection.Reassert(*cleaned, snap, evidence.Provenance, filtered)
	if reassertErr != nil {
		t.Fatalf("requirements.md 5.10 - an item-authoritative trajectory the rewrite left structurally intact must not be refused: anchor_missing=%t projection_failed=%t",
			errors.Is(reassertErr, conversationprojection.ErrAnchorMissing),
			errors.Is(reassertErr, conversationprojection.ErrProjectionFailed))
	}
	if err := reasserted.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the reasserted item-authoritative call must stay canonically valid: %v", err)
	}
	copies, immediatelyAfter := famItemSteeringPlacement(reasserted, stored)
	if copies != 1 || !immediatelyAfter {
		t.Fatalf("design.md \"4A\" constraint 1 - overlay lifecycle must be unchanged on the item authority: the overlay must be present exactly once at the anchored message item's boundary: copies=%d immediately_after_anchor=%t",
			copies, immediatelyAfter)
	}
	if famItemPresent(reasserted, twoPassLocalText) {
		t.Fatalf("design.md \"4A\" constraint 6 - a never_backend-tagged message item must stay out of the reasserted trajectory")
	}
	reassertedDocs, ok := famItemDocumentsOf(reasserted)
	if !ok {
		t.Fatalf("fixture: the reasserted item call must carry both path-bearing documents at their fixed slots")
	}
	for _, slot := range []string{famItemToolCallSlot, famItemToolResultSlot} {
		cmp := famCompareDocument(rewrittenDocs[slot], reassertedDocs[slot], driftPathField, alias)
		if !cmp.selectedAlias || cmp.selectedRealRoot {
			t.Fatalf("requirements.md 5.4 - the final reassertion must not restore a real root into %s tool history: selected_carries_alias=%t selected_carries_real_root=%t",
				slot, cmp.selectedAlias, cmp.selectedRealRoot)
		}
		if cmp.unselectedEqual != cmp.unselectedCount {
			t.Fatalf("requirements.md 2.8 - the final reassertion must preserve every non-selected member of the %s payload byte-for-byte: unselected_equal=%d unselected_count=%d",
				slot, cmp.unselectedEqual, cmp.unselectedCount)
		}
	}
	if adaptErr := conversationprojection.VerifyAdaptationPreservesProjection(reasserted, reasserted, snap, reassertEvidence.Provenance); adaptErr != nil {
		t.Fatalf("design.md \"4A\" - adaptation verification must still hold on the item authority: projection_failed=%t",
			errors.Is(adaptErr, conversationprojection.ErrProjectionFailed))
	}
}

// famItemMessageIndexes are the fixture's message-item slots. The count of this slice is
// the anchor count a correct projection must produce, so the expectation cannot drift away
// from the fixture.
var famItemMessageIndexes = []int{famItemMessageFirst, famItemMessageLocal, famItemMessageTail}

// famItemShapeStable reports whether the rewritten item trajectory preserves everything
// requirement 2.8 and design.md "4A" name as structural: item count, item order, item
// kinds, stable item identities, roles, statuses, tool-call identities, and tool names.
// Payload bytes are excluded because those are exactly what the rewrite may change.
func famItemShapeStable(before, after aipapi.Call) bool {
	if len(before.Items) != len(after.Items) {
		return false
	}
	for i := range before.Items {
		b, a := before.Items[i], after.Items[i]
		if b.Kind != a.Kind || b.ID != a.ID || b.Status != a.Status || b.Role != a.Role || b.Phase != a.Phase {
			return false
		}
		if len(b.Content) != len(a.Content) {
			return false
		}
		for j := range b.Content {
			if b.Content[j].Kind != a.Content[j].Kind {
				return false
			}
		}
		if (b.ToolCall == nil) != (a.ToolCall == nil) {
			return false
		}
		if b.ToolCall != nil && (b.ToolCall.CallID != a.ToolCall.CallID || b.ToolCall.Name != a.ToolCall.Name) {
			return false
		}
		if (b.ToolResult == nil) != (a.ToolResult == nil) {
			return false
		}
		if b.ToolResult != nil &&
			(b.ToolResult.CallID != a.ToolResult.CallID || b.ToolResult.Name != a.ToolResult.Name) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Carried overlay: the frozen logical boundary after identity drift.
// ---------------------------------------------------------------------------

// TestConversationView_LegacyAfterMessageOverlaySurvivesVirtualizationAtTheFrozenBoundary
// is requirements.md 5.9 and 5.2 at the runtime boundary, with the identity-drift
// premise asserted in the SAME run as the survival.
//
// The delivered Task 5.2 regression asserts that the overlay is restored exactly once
// immediately after the anchored message. That assertion alone cannot distinguish "the
// placement was carried through a content rewrite" from "the anchored message's identity
// never moved, so the ordinary exact-resolution path handled it": the second reading
// would make the assertion pass with the whole carry-forward machinery absent. This test
// closes that gap by measuring, at Backend.Open, that the anchored message's
// content-derived identity is no longer the identity the frozen overlay names, while the
// overlay is still exactly one copy immediately after it.
//
// Both drifting legacy surfaces are covered: the tool-call argument member and the
// opaque tool-result text line. The structured tool-result member is deliberately NOT
// in this set, because its rewrite changes model-visible bytes with zero identity drift
// (Task 1.3's own control), so it would silently degrade this test back into the
// vacuous reading. That surface's exact-resolution success is already pinned by the
// delivered seam characterization.
func TestConversationView_LegacyAfterMessageOverlaySurvivesVirtualizationAtTheFrozenBoundary(t *testing.T) {
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
			// The shipped built-in profile alone already claims this tool's argument
			// member, so the stock-reachable surface needs no operator configuration.
			resolver: nil,
		},
		{
			label:    driftSurfaceResultOpaqueText,
			anchor:   driftResultTextAnchorMessage(),
			loc:      driftResultTextLocator(),
			resolver: driftOpaqueResultResolver,
		},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			t.Parallel()

			stored := driftStoredAnchor(t, scenario.anchor)
			beforeIdentity, err := conversationprojection.MessageIdentityOf(scenario.anchor)
			if err != nil {
				t.Fatalf("fixture: anchored message identity before the rewrite: %v", err)
			}
			got := hookRegRun(t, hookRegDriftScenario(t, scenario.label, scenario.anchor, scenario.resolver, nil))
			order := hookRegOrderOf(got)

			// Scaffolding. Every assertion below reads these.
			if !got.turnDone {
				t.Fatalf("requirements.md 5.9 - a provably one-to-one, structure-preserving rewrite must not deny the turn: surface=%s anchor_missing=%t projection_failure_stages=%d anchor_failures=%d anchor_fallbacks=%d %s",
					got.label, got.anchorMissing, len(got.failureStages), got.anchorFailures, got.fallbacks, order)
			}
			if got.anchorMissing || len(got.failureStages) != 0 || got.anchorFailures != 0 || got.fallbacks != 0 {
				t.Fatalf("requirements.md 5.9 and design.md \"4A\" constraint 1 - the final reassertion must carry the resolved placement, and the stored fail-closed policy must never be downgraded to a stable-prefix fallback: surface=%s anchor_missing=%t projection_failure_stages=%d anchor_failures=%d anchor_fallbacks=%d",
					got.label, got.anchorMissing, len(got.failureStages), got.anchorFailures, got.fallbacks)
			}
			if got.snapshotReads != 1 {
				t.Fatalf("design.md \"4A\" constraint 3 - the final reassertion must reuse the FROZEN request-local snapshot and must not read the conversation-view store again: surface=%s snapshot_reads=%d",
					got.label, got.snapshotReads)
			}
			if got.order.reassert == 0 || got.order.ptb == 0 || got.order.open == 0 {
				t.Fatalf("fixture: the reassertion, per-turn buffer, and Backend.Open stages must all be reached: surface=%s %s", got.label, order)
			}

			// -----------------------------------------------------------------------
			// THE PREMISE, MEASURED AT THE BACKEND BOUND: the anchored message's
			// content-derived identity is no longer the identity the frozen overlay
			// names, and never was it equal to the post-rewrite identity.
			// -----------------------------------------------------------------------
			atOpen, ok := driftFindAnchor(got.backendOpenCall, scenario.loc)
			if !ok {
				t.Fatalf("fixture: the anchored message must survive both real passes and candidate adaptation: surface=%s", got.label)
			}
			openIdentity, err := conversationprojection.MessageIdentityOf(atOpen)
			if err != nil {
				t.Fatalf("fixture: anchored message identity at Backend.Open: %v", err)
			}
			if openIdentity == stored.Identity {
				t.Fatalf("fixture: the backend-bound anchored message must NOT still carry the stored anchor identity; otherwise this test measures the ordinary exact-resolution path instead of requirements.md 5.9's carried placement: surface=%s stored_anchor_still_resolves=%t",
					got.label, openIdentity == stored.Identity)
			}
			if openIdentity == beforeIdentity {
				t.Fatalf("fixture: the backend-bound anchored message must not have kept its pre-rewrite identity either; the drift assertion above would be vacuous: surface=%s pre_rewrite_identity_preserved=%t",
					got.label, openIdentity == beforeIdentity)
			}
			if len(openIdentity) != len(beforeIdentity) {
				t.Fatalf("fixture: identity drift must be a content change, not an identity-shape change: surface=%s", got.label)
			}

			// -----------------------------------------------------------------------
			// AND THE PLACEMENT SURVIVED IT, AT BOTH BACKEND BOUND SURFACES.
			// -----------------------------------------------------------------------
			for _, stage := range []struct {
				name string
				call aipapi.Call
			}{
				{"per_turn_buffer", hookRegCallFromBody(got.ptbBody)},
				{"Backend.Open", got.backendOpenCall},
			} {
				part := driftInspectPart(mustAnchor(t, stage.call, scenario.loc, got.label, stage.name), scenario.loc, driftAliasOf(t))
				if !part.found || !part.selectedCarriesAlias || part.selectedCarriesRealRoot {
					t.Fatalf("requirements.md 5.2 and 5.4 - final reassertion and candidate adaptation must not restore a real root into path-bearing tool history: surface=%s stage=%s selected_carries_alias=%t selected_carries_real_root=%t",
						got.label, stage.name, part.selectedCarriesAlias, part.selectedCarriesRealRoot)
				}
				if part.nonPathSum != scenario.loc.expectedNonPath() {
					t.Fatalf("requirements.md 2.8 - every non-selected member of the anchored payload must survive byte-for-byte: surface=%s stage=%s non_path_sum=%d expected_non_path_sum=%d",
						got.label, stage.name, part.nonPathSum, scenario.loc.expectedNonPath())
				}
				copies, immediatelyAfter := hookRegSteeringAfterAnchor(stage.call, scenario.loc)
				if copies != 1 || !immediatelyAfter {
					t.Fatalf("requirements.md 5.9 and design.md \"4A\" - the overlay must remain at the SAME frozen logical boundary after the anchored message's identity drifted, and must not be relocated: surface=%s stage=%s steering_copies=%d steering_immediately_after_anchor=%t",
						got.label, stage.name, copies, immediatelyAfter)
				}
				if _, present := driftFindTextMessage(stage.call, twoPassLocalText); present {
					t.Fatalf("design.md \"4A\" constraint 6 - a never_backend-tagged message must stay out of the backend-bound request: surface=%s stage=%s",
						got.label, stage.name)
				}
			}
			if got.order.reassert >= got.order.ptb || got.order.ptb >= got.order.open {
				t.Fatalf("requirements.md 5.2/5.4 - the final reassertion must complete before the per-turn buffer, which must complete before Backend.Open: surface=%s %s", got.label, order)
			}
		})
	}
}

// mustAnchor locates the anchored message or fails with a content-free message.
func mustAnchor(t *testing.T, call aipapi.Call, loc driftLocator, label, stage string) aipapi.Message {
	t.Helper()
	msg, ok := driftFindAnchor(call, loc)
	if !ok {
		t.Fatalf("fixture: the anchored message must be present at %s: surface=%s stage=%s", stage, label, stage)
	}
	return msg
}

// ---------------------------------------------------------------------------
// Ambiguous lineage: fail closed, never relocate by ordinal.
// ---------------------------------------------------------------------------

// TestConversationView_AmbiguousLineageKeepsTheOverlayFailClosedAndUnrelocated is
// requirements.md 5.10 sentence 2 and requirement 8.5 at the runtime boundary, expressed
// as a composition claim: once the trajectory became structurally ambiguous, the refusal
// happens at the canonical reassertion stage, before the per-turn buffer, before
// Backend.Open, and therefore before any protocol-family adapter could observe or
// relocate the overlay at all.
//
// Two perturbations are used, both from the delivered Task 5.3 oracle set, and both
// applied on the request-part plane AFTER the real early pass has already virtualized the
// anchored message:
//
//   - equal_cardinality_stable_tool_call_id_change. Every frozen ordinal slot stays
//     occupied by the same kind of message, so an implementation that relocated the
//     overlay by position would find a slot, succeed, and reach the backend. This is the
//     anti-ordinal oracle: the fixture asserts the perturbation changed the trajectory
//     length by zero before asserting the denial.
//   - trajectory_cardinality_grew_by_insertion. The contrast case, where the trajectory
//     itself no longer has the frozen ordinal at all.
//
// The denial assertions are the triple the executor actually publishes, plus the two
// absence proofs that make "no relocation" observable rather than assumed: no
// conversation-view failure was recorded at any stage other than the final one, and
// neither backend-bound surface was ever produced, so there is no relocated overlay to
// have been produced.
func TestConversationView_AmbiguousLineageKeepsTheOverlayFailClosedAndUnrelocated(t *testing.T) {
	t.Parallel()

	locator := driftCallLocator()
	for _, perturbationCase := range []struct {
		label string
		apply func(call *aipapi.Call)
		// equalCardinality marks the anti-ordinal oracle cases: every frozen ordinal
		// slot stays occupied, so a positional relocation would also find a slot.
		equalCardinality bool
	}{
		{label: "equal_cardinality_stable_tool_call_id_change", apply: hookRegToolCallIDChange, equalCardinality: true},
		{label: "trajectory_cardinality_grew_by_insertion", apply: hookRegInsertMessage},
	} {
		t.Run(perturbationCase.label, func(t *testing.T) {
			t.Parallel()

			anchor := driftCallAnchorMessage()
			perturb := hookRegPerturbationFor(t, perturbationCase.label, anchor, locator, perturbationCase.apply)
			got := hookRegRun(t, hookRegDriftScenario(t, perturbationCase.label, anchor, nil, perturb))
			order := hookRegOrderOf(got)

			// Scaffolding: the perturbation ran on the request-part plane, it saw the
			// anchored message, that message's identity had ALREADY drifted, and the
			// cardinality claim holds. Without these a passing denial could be a
			// fixture artifact rather than a lineage refusal.
			locatorFound, identityDrift, applied, delta := perturb.observation()
			if !locatorFound || !identityDrift || !applied {
				t.Fatalf("fixture: the perturbation must find the anchored message and run after its identity already drifted: scenario=%s locator_found=%t identity_drift=%t applied=%t message_delta=%d",
					got.label, locatorFound, identityDrift, applied, delta)
			}
			if perturbationCase.equalCardinality && delta != 0 {
				t.Fatalf("fixture: an equal-cardinality perturbation must keep every frozen ordinal slot occupied so a positional relocation would also succeed: scenario=%s message_delta=%d",
					got.label, delta)
			}
			if !perturbationCase.equalCardinality && delta == 0 {
				t.Fatalf("fixture: a trajectory-length perturbation must actually change the trajectory length: scenario=%s message_delta=%d", got.label, delta)
			}
			if got.order.part == 0 || got.order.reassert == 0 || got.order.reassert <= got.order.part {
				t.Fatalf("fixture: the perturbation runs on the request-part plane, so the final reassertion stage must come after it: scenario=%s %s", got.label, order)
			}

			// The denial.
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
				t.Fatalf("design.md \"4A\" constraint 1 - the fail-closed policy must never be downgraded to a stable-prefix fallback, which is the one outcome that would silently relocate the overlay: scenario=%s anchor_fallbacks=%d",
					got.label, got.fallbacks)
			}

			// No relocation is observable because neither backend-bound surface was ever
			// produced. A family adapter only ever sees a call the reassertion released,
			// so a refusal here also means no adapter could have rewritten, moved, or
			// re-encoded the overlay: requirement 5.8's neutrality is trivially
			// preserved for a turn that never reached one.
			if got.order.ptb != 0 {
				t.Fatalf("requirements.md 5.10 - the refusal must happen before the per-turn buffer, which is the first backend-bound surface: scenario=%s reached_ptb=%d",
					got.label, got.order.ptb)
			}
			if got.order.open != 0 {
				t.Fatalf("requirements.md 5.10 - no candidate may reach Backend.Open, so no protocol-family adapter may observe the trajectory, when the lineage proof fails: scenario=%s backend_open_stage=%d",
					got.label, got.order.open)
			}
			if len(got.ptbBody) != 0 || len(got.backendOpenCall.Messages) != 0 || len(got.backendOpenCall.Items) != 0 {
				t.Fatalf("requirements.md 5.10 - a refused trajectory must produce no backend-bound call at all, so no overlay copy can exist anywhere downstream: scenario=%s ptb_body_bytes=%d backend_open_messages=%d backend_open_items=%d",
					got.label, len(got.ptbBody), len(got.backendOpenCall.Messages), len(got.backendOpenCall.Items))
			}
			if got.snapshotReads != 1 {
				t.Fatalf("design.md \"4A\" constraint 3 - the refusal must reuse the frozen snapshot without a second store read: scenario=%s snapshot_reads=%d",
					got.label, got.snapshotReads)
			}
		})
	}
}

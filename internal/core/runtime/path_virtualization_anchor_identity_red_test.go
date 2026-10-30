package runtime_test

// Spec: b-leg-path-virtualization Task 1.3, anchor-identity half. Requirements 5.2,
// 5.3, 5.4, 5.9, 5.10.
//
// Design sections consulted: "Existing Architecture and Placement" (steps 2, 3, 4, 6,
// 9, 10 and their consequences), "4A. Transform-Stable Conversation-View
// Reassertion", "Canonical Outbound Rewriter", "Testing Strategy /
// Runtime-integration", and "Boundary Commitments / Out of Boundary".
//
// WHAT THIS FILE IS
//
// The second half of Task 1.3. The first half - the backend-bound two-pass ordering -
// is a permanent GREEN guard in path_virtualization_two_pass_ordering_characterization_test.go
// and is untouched by this file.
//
// The conflict this file characterizes is a brownfield composition hazard the original
// spec missed and requirements.md 5.9/5.10 plus design.md "4A" now own:
//
//   - early conversation-view projection resolves a persisted after-message steering
//     anchor from A-leg/client truth, BEFORE any candidate attempt transform runs;
//   - conversationprojection.MessageIdentityOf is HashAtom(AtomOfMessage(msg)), a CONTENT
//     hash that includes the legacy PartJSON and PartToolResult payloads;
//   - so virtualizing the selected path payload inside that same complete message changes
//     the identity the frozen anchor names;
//   - the final conversation-view reassertion removes its projection-owned overlay and
//     then calls Project(cleaned, snap), which re-resolves the frozen anchor against
//     post-virtualization content and returns ErrAnchorMissing;
//   - the executor converts that into AnchorFailClosed plus CommandPreBackendDenial, so a
//     perfectly ordinary A-leg turn is denied before Backend.Open.
//
// Two tests document it, at the two seams the requirement is about:
//
//	TestConversationViewReassert_PathRewriteDriftsFrozenAfterMessageAnchor
//	    the pure seam. Reconstructs the exact Reassert inputs from exported
//	    conversationprojection API, runs the feature's real rewriter, and asserts the
//	    final reassertion must carry the resolved placement across the rewrite - the RED
//	    assertion requirements.md 5.9 asks for. The premise it rests on (the identity
//	    really does drift, and the drift really is confined to the one selected path
//	    payload) is asserted as a durable GREEN subtest.
//
//	TestOutboundAttempt_PathVirtualizationAnchorDriftDeniesTurnPreBackend
//	    the runtime consequence. Drives the real executor with the feature's real two
//	    outbound passes and a steering overlay anchored on the path-bearing message, and
//	    asserts the turn must reach the backend - the RED assertion requirements.md
//	    5.9/5.10 ask for. Its failing output carries the complete measured denial:
//	    ErrAnchorMissing from Reassert, one AnchorFailClosed publication at the final
//	    stage, no stable-prefix fallback, no per-turn buffer, and Backend.Open never
//	    reached.
//
// THREE SURFACES, AND WHICH ONES ACTUALLY DRIFT
//
//  1. legacy PartJSON tool call, argument member /file_path claimed by the SHIPPED
//     built-in profile. Reachable in a stock deployment; identity drifts.
//  2. legacy PartToolResult, opaque Text line, claimed by an OPERATOR profile that
//     declares OpaqueResultModePathLines. No shipped built-in enables any opaque mode,
//     so this needs operator configuration; identity drifts because
//     AtomOfMessage maps PartToolResult to {Kind, Text}.
//  3. legacy PartToolResult, structured Content member /file_path, claimed by an
//     OPERATOR profile that declares ResultJSONPointers. This is the finding: the
//     rewrite DOES change model-visible bytes, but MessageIdentityOf does NOT drift,
//     because AtomOfMessage projects PartToolResult from its Text field only and drops
//     Content. So today the identity-based reassertion happens to still succeed. That
//     asymmetry is why design.md "4A" forbids using identity equality as the lineage
//     proof, and it is asserted here rather than hidden.
//
// NOTHING HERE IS WEAKENED
//
// design.md "4A" forbids changing MessageIdentityOf, the stored
// MessageAnchor{Identity, Occurrence}, conversation-view persistence, overlay
// lifecycle, or the anchor-missing/fallback policy. The subtest
// fixture_requires_no_conversation_view_semantics_change asserts each of those against
// the fixture instead of accommodating it, including that the anchor-missing policy is
// still the fail-closed one and was never downgraded to a stable-prefix fallback.
//
// REUSED HARNESS
//
// The stage-ordinal recorder, the frozen conversation-view reader, the shared workspace
// resolver, the eligibility observer, the traffic observer, the recording backend, the
// real-pass markers, and the pass-report collector all come from the delivered Task 1.3
// and Task 5.2 files. This file adds no second end-to-end PTB/Backend.Open harness; it
// only adds the locator, the profile selection, and the bounded measurements the anchor
// conflict needs.
//
// OBSERVABILITY
//
// No real path, alias, workspace tag, message identity, overlay id, or anchor string
// ever reaches a failure message. Every observation is a boolean, a count, a byte total,
// or a stage ordinal, and the identity is only ever compared. This matches the existing
// two path-virtualization characterizations in this package.
//
// DETERMINISM
//
// Every stage ordinal comes from the shared monotonic recorder, every scan walks slices
// rather than maps, the executor RNG and clock are pinned, and the pure seam involves no
// concurrency at all, so -count=5 is stable.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// The two wrappers this file adds satisfy exactly the extension points the runtime chains
// the real outbound passes onto; the assertions below then measure what the REAL passes
// published through them.
var (
	_ request.AttemptTransform = (*driftAttemptMarker)(nil)
	_ sdkhooks.RequestPartHook = (*hookRegPartMarker)(nil)
)

const (
	// driftFirstUserText is a leading user message that carries no path, so its
	// identity is stable across every outbound pass. It is the negative control: if
	// identity drifted for it too, the drift assertion would prove nothing about the
	// selected path payload.
	driftFirstUserText = "anchor-drift-first-user"

	// driftSteerText is the model-visible steering payload. It carries no path, so the
	// overlay's own identity is stable too.
	driftSteerText = "anchor-drift-steering"

	// driftOverlayID and driftReasonCode are bounded, content-free labels for the one
	// overlay and the one never_backend tag this fixture installs.
	driftOverlayID  = "ov-anchor-drift"
	driftReasonCode = "anchor_drift_fixture"

	// driftCallID and driftResultID are the two stable tool-call identities used to
	// locate the anchored message in a call. Locating by ID rather than by ordinal is
	// what keeps the assertion independent of how many messages precede it.
	driftCallID   = "anchor-drift-call"
	driftResultID = "anchor-drift-result"

	// driftCallSuffix and driftResultSuffix are the path-bearing suffixes on the two
	// surfaces. They differ so a rewrite of one surface can never be mistaken for a
	// rewrite of the other, and each is identical in real and virtualized form so any
	// difference at a later stage is attributable to the root prefix alone.
	driftCallSuffix   = "src/anchor_drift_call.go"
	driftResultSuffix = "src/anchor_drift_result.go"

	// driftPathField is the argument and structured-result member the shipped built-in
	// profile claims for the fixture's exact tool name. Spelling it out keeps the
	// fixture independent of the optional schema-inference step.
	driftPathField = "file_path"

	// driftResultField is the non-path sibling of the structured result document.
	driftResultField = "bytes"

	// driftCallLimit and driftResultBytes are the non-path siblings of the two
	// structured documents. They differ so requirement 2.8's byte-for-byte preservation
	// of a non-path value is a real check on each surface rather than a repeated one.
	driftCallLimit   = 10
	driftResultBytes = 42
)

// The three surface labels. They are stable failure-message tokens and nothing else.
const (
	driftSurfaceCallArgument     = "legacy_part_json_argument"
	driftSurfaceResultOpaqueText = "legacy_part_tool_result_opaque_text"
	driftSurfaceResultStructured = "legacy_part_tool_result_structured_content"
)

// driftSelectedContent and driftSelectedText name which payload of a legacy part carries
// the selected location. The two are genuinely different surfaces: the structured member
// is read by a JSON Pointer, the opaque line by the bounded line/token recognizers.
const (
	driftSelectedContent = "content"
	driftSelectedText    = "text"
)

// driftLocator identifies one anchored message inside a call and the selected payload
// inside its part.
//
// It is a locator rather than a positional index on purpose: design.md "4A" requires a
// structural, identity-independent mapping, so the fixture must be able to find the same
// logical message in the ingress call and in the backend-shaped call without either one
// having to agree on an ordinal.
type driftLocator struct {
	kind  aipapi.PartKind
	id    string
	field string
}

func driftCallLocator() driftLocator {
	return driftLocator{kind: aipapi.PartJSON, id: driftCallID, field: driftSelectedContent}
}

func driftResultTextLocator() driftLocator {
	return driftLocator{kind: aipapi.PartToolResult, id: driftResultID, field: driftSelectedText}
}

func driftResultContentLocator() driftLocator {
	return driftLocator{kind: aipapi.PartToolResult, id: driftResultID, field: driftSelectedContent}
}

// driftExpectedNonPath is the sum of the non-selected numeric siblings one surface must
// still carry after the rewrite, i.e. what requirement 2.8 looks like numerically.
func (l driftLocator) expectedNonPath() int {
	switch l.field {
	case driftSelectedContent:
		if l.kind == aipapi.PartJSON {
			return driftCallLimit
		}
		return driftResultBytes
	default:
		return 0
	}
}

// driftArgumentDocument is the complete PartJSON argument document in real-root form: the
// selected path member plus one non-path sibling.
func driftArgumentDocument() []byte {
	return []byte(`{"` + driftPathField + `":"` + twoPassRealRoot + `/` + driftCallSuffix +
		`","limit":` + strconv.Itoa(driftCallLimit) + `}`)
}

// driftStructuredResultDocument is the complete structured tool-result Content document in
// real-root form: the selected path member plus one non-path sibling.
func driftStructuredResultDocument() []byte {
	return []byte(`{"` + driftPathField + `":"` + twoPassRealRoot + `/` + driftResultSuffix +
		`","` + driftResultField + `":` + strconv.Itoa(driftResultBytes) + `}`)
}

// driftCallAnchorMessage is surface 1: the historical path-bearing tool call the client
// replays, in legacy message authority, carrying the member the SHIPPED built-in profile
// claims. It is the message the steering overlay is anchored on.
func driftCallAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: driftCallID,
		ToolName:   twoPassToolName,
		Content:    driftArgumentDocument(),
	}}}
}

// driftResultTextAnchorMessage is surface 2: the historical path-bearing tool result whose
// selected location is its opaque text line. The payload is exactly one line holding
// exactly one location, which is the shape the line-mode recognizer accepts; a line
// carrying anything else is refused whole (requirement 2.6).
func driftResultTextAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: driftResultID,
		ToolName:   twoPassToolName,
		Text:       twoPassRealRoot + "/" + driftResultSuffix,
	}}}
}

// driftResultContentAnchorMessage is surface 3: the historical path-bearing tool result
// whose selected location is a member of its structured Content.
func driftResultContentAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: driftResultID,
		ToolName:   twoPassToolName,
		Content:    driftStructuredResultDocument(),
	}}}
}

// driftIngressCall builds the ingress call around one anchored message.
//
// The shape is Task 1.3's fixture with the anchor moved off the first user message and
// onto the path-bearing message itself, which is the whole point of this characterization.
// The never_backend note and the terminal forwardable user message are kept so the final
// conversation-view reassertion is a real stage rather than a no-op.
func driftIngressCall(anchor aipapi.Message) *aipapi.Call {
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
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(driftFirstUserText)}},
			anchor,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassTailText)}},
		},
	}
}

// driftSnapshot is the frozen per-turn conversation view: one never_backend tag plus one
// ACTIVE after-message steering overlay anchored, under the strict fail-closed anchor
// policy, on the pre-virtualization identity of the path-bearing message.
//
// The anchor is derived from the A-leg/client truth on purpose: that is the authority early
// projection must resolve against, and it is the identity the persisted overlay carries.
// Task 5.3's repair must carry the resolved placement forward; it must not change how the
// anchor is derived.
func driftSnapshot(t *testing.T, anchor aipapi.Message) conversationprojection.Snapshot {
	t.Helper()
	local := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(twoPassLocalText)}}
	localID, err := conversationprojection.MessageIdentityOf(local)
	if err != nil {
		t.Fatalf("fixture: local message identity: %v", err)
	}
	anchorID, err := conversationprojection.MessageIdentityOf(anchor)
	if err != nil {
		t.Fatalf("fixture: anchor message identity: %v", err)
	}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: driftReasonCode}},
		Steering: []conversationprojection.Overlay{{
			OverlayID:   driftOverlayID,
			Revision:    1,
			SlotOrdinal: 1,
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: driftSteerText},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		}},
	}
}

// driftStoredAnchor returns the anchor the fixture persists for one anchored message.
func driftStoredAnchor(t *testing.T, anchor aipapi.Message) conversationprojection.MessageAnchor {
	t.Helper()
	id, err := conversationprojection.MessageIdentityOf(anchor)
	if err != nil {
		t.Fatalf("fixture: stored anchor identity: %v", err)
	}
	return conversationprojection.MessageAnchor{Identity: id, Occurrence: 1}
}

// driftResolver builds one compiled policy for the fixture.
//
// Passing no operator profile yields exactly the conservative policy a deployment gets
// with no operator configuration: the shipped built-in layer alone. Passing an operator
// profile adds one exact-name profile, which REPLACES the built-in layer for the name it
// claims (requirements.md 3.7), so such a profile must declare the argument selector it
// wants as well as the result selector or opaque mode it adds.
func driftResolver(t *testing.T, operator ...pathvirtualization.ToolProfile) *pathvirtualization.Resolver {
	t.Helper()
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles(builtin) reject = %q, want none", reject)
	}
	var compiled []pathvirtualization.CompiledProfile
	if len(operator) > 0 {
		compiled, reject = pathvirtualization.CompileToolProfiles(operator)
		if reject != pathvirtualization.SelectorRejectNone {
			t.Fatalf("CompileToolProfiles(operator) reject = %q, want none", reject)
		}
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	return resolver
}

// driftOpaqueResultResolver selects the legacy PartToolResult opaque Text surface.
func driftOpaqueResultResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	return driftResolver(t, pathvirtualization.ToolProfile{
		Names:            []string{twoPassToolName},
		ArgPointers:      []string{"/" + driftPathField},
		OpaqueResultMode: pathvirtualization.OpaqueResultModePathLines,
	})
}

// driftStructuredResultResolver selects the legacy PartToolResult structured Content
// surface.
func driftStructuredResultResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	return driftResolver(t, pathvirtualization.ToolProfile{
		Names:              []string{twoPassToolName},
		ArgPointers:        []string{"/" + driftPathField},
		ResultJSONPointers: []string{"/" + driftPathField},
	})
}

// driftRewriter binds the feature's real pure outbound rewriter to the fixture's
// authoritative project root, in rewrite mode.
func driftRewriter(t *testing.T, resolver *pathvirtualization.Resolver) *rewrite.Rewriter {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(twoPassRealRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the fixture project root must derive an active mapping; the refusal reason is a bounded, content-free code")
	}
	return rewrite.New(mapping, resolver)
}

// driftAliasOf returns the alias the feature publishes for the fixture project root. The
// value is compared, never formatted: requirement 7.7 keeps the workspace identity tag out
// of every observable dimension. It is derived here so this file does not depend on
// another characterization's helper for its own fixtures.
func driftAliasOf(t *testing.T) string {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(twoPassRealRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the fixture project root must derive an active mapping; the refusal reason is a bounded, content-free code")
	}
	return mapping.VirtualRoot
}

// driftFindAnchor locates the anchored message in a call by part kind and stable tool-call
// ID, across both canonical authorities.
func driftFindAnchor(call aipapi.Call, loc driftLocator) (aipapi.Message, bool) {
	for _, msgs := range [][]aipapi.Message{call.Instructions, call.Messages} {
		for _, msg := range msgs {
			for _, part := range msg.Parts {
				if part.Kind == loc.kind && part.ToolCallID == loc.id {
					return msg, true
				}
			}
		}
	}
	return aipapi.Message{}, false
}

// driftFindTextMessage locates a path-free control message by its single text part. The
// anchored message is located by tool-call ID because it has one; a plain text message has
// none, so the control is located by its content instead.
func driftFindTextMessage(call aipapi.Call, text string) (aipapi.Message, bool) {
	for _, msgs := range [][]aipapi.Message{call.Instructions, call.Messages} {
		for _, msg := range msgs {
			for _, part := range msg.Parts {
				if part.Kind == aipapi.PartText && part.Text == text {
					return msg, true
				}
			}
		}
	}
	return aipapi.Message{}, false
}

// driftSteeringPlacement counts the projection-owned steering copies in a call and reports
// whether one sits IMMEDIATELY after the anchored message in the combined
// instruction+message trajectory.
//
// Immediately-after is the frozen placement itself, not "somewhere later": the overlay is an
// after_message placement, and a repair that merely relocated it to the end of the request
// would satisfy a weaker assertion while breaking the placement.
func driftSteeringPlacement(call aipapi.Call, loc driftLocator) (copies int, immediatelyAfter bool) {
	combined := make([]aipapi.Message, 0, len(call.Instructions)+len(call.Messages))
	combined = append(combined, call.Instructions...)
	combined = append(combined, call.Messages...)
	anchorAt := -1
	steerAt := -1
	for i, msg := range combined {
		for _, part := range msg.Parts {
			switch {
			case part.Kind == aipapi.PartText && part.Text == driftSteerText:
				if steerAt < 0 {
					steerAt = i
				}
				copies++
			case part.Kind == loc.kind && part.ToolCallID == loc.id && anchorAt < 0:
				anchorAt = i
			}
		}
	}
	return copies, anchorAt >= 0 && steerAt == anchorAt+1
}

// driftPartState is the content-free measurement of one anchored message's part.
//
// selectedCarriesAlias and selectedCarriesRealRoot are the two states that distinguish
// "the selected path payload was rewritten" from "it was not"; nonPathSum and
// nonPathCount are what requirement 2.8's byte-for-byte preservation of every non-selected
// member looks like numerically; partCount is the ordered part cardinality design.md "4A"
// requires a lineage proof to keep compatible.
type driftPartState struct {
	found                   bool
	selectedCarriesAlias    bool
	selectedCarriesRealRoot bool
	nonPathSum              int
	nonPathCount            int
	partCount               int
}

// driftInspectPart measures one part of the anchored message. It never returns the selected
// value itself, so no path can reach a failure message through it.
func driftInspectPart(msg aipapi.Message, loc driftLocator, alias string) driftPartState {
	out := driftPartState{partCount: len(msg.Parts)}
	for _, part := range msg.Parts {
		if part.Kind != loc.kind || part.ToolCallID != loc.id {
			continue
		}
		out.found = true
		if loc.field == driftSelectedText {
			out.selectedCarriesAlias = strings.Contains(part.Text, alias)
			out.selectedCarriesRealRoot = strings.Contains(part.Text, twoPassRealRoot)
			return out
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(part.Content, &members); err != nil {
			return out
		}
		var selected string
		if err := json.Unmarshal(members[driftPathField], &selected); err != nil {
			return out
		}
		out.selectedCarriesAlias = strings.Contains(selected, alias)
		out.selectedCarriesRealRoot = strings.Contains(selected, twoPassRealRoot)
		for name, raw := range members {
			if name == driftPathField {
				continue
			}
			var number int
			if err := json.Unmarshal(raw, &number); err != nil {
				continue
			}
			out.nonPathSum += number
			out.nonPathCount++
		}
		return out
	}
	return out
}

// driftSeam is the complete content-free record of one pure-seam scenario: the exact inputs
// Reassert receives, what the real rewriter did to the anchored message, and what the final
// reassertion then decided.
type driftSeam struct {
	surface string
	loc     driftLocator

	// Early projection, i.e. the A-leg/client-truth authority.
	storedAnchor    conversationprojection.MessageAnchor
	storedPolicy    conversationprojection.AnchorMissingPolicy
	provenanceCount int
	resolvedKind    conversationprojection.PlacementKind
	resolvedAnchor  conversationprojection.MessageAnchor
	overlayIdentity conversationprojection.MessageIdentity
	overlayCopies   int
	overlayAfter    bool
	filteredCount   int

	// The B-leg rewrite of that same projected call.
	rewritten           bool
	statsEligible       int
	statsRewritten      int
	bytesSaved          int
	identityBefore      conversationprojection.MessageIdentity
	identityAfter       conversationprojection.MessageIdentity
	identityLen         int
	identityDrift       bool
	part                driftPartState
	stableIdentityDrift bool

	// The final reassertion of the backend-shaped call.
	reassertErr          error
	anchorMissing        bool
	copiesAfterReassert  int
	overlayAfterReassert bool
}

// driftSeamRun reconstructs the exact inputs the executor hands
// conversationprojection.Reassert and runs them.
//
// The reconstruction is the runtime's own sequence, in order:
//
//	executor_prepare_secure.go: early projection   = Project(ingress, snap)  -> provenance
//	executor_prepare_secure.go: filtered baseline   = FilterNeverBackend(ingress, snap)
//	executor_open_attempt.go:  outbound pass       = the feature's real rewriter
//	executor_open_attempt.go:  final reassertion   = Reassert(rewritten, snap, provenance, filtered)
//
// Nothing here is inferred about internal state: every input is either exported by
// conversationprojection or produced by the feature's own public rewriter, so the only thing
// this test asserts is how those two compose.
func driftSeamRun(t *testing.T, surface string, anchor aipapi.Message, loc driftLocator, resolver *pathvirtualization.Resolver) driftSeam {
	t.Helper()
	ingress := driftIngressCall(anchor)
	snap := driftSnapshot(t, anchor)
	alias := driftAliasOf(t)

	// The two derivations are separate on purpose. The early projection runs over the
	// UNFILTERED ingress call, which is what executor_prepare_secure.go does and what
	// makes it resolve the anchor against A-leg/client truth; the frozen baseline is the
	// never_backend-filtered call derived separately, which is what it hands Reassert.
	// Projecting the already-filtered call instead would resolve against a trajectory the
	// client never sent.
	projected, evidence, err := conversationprojection.Project(*ingress, snap)
	if err != nil {
		t.Fatalf("seam %s: early projection must resolve the after-message anchor from A-leg truth: %v", surface, err)
	}
	filtered, err := conversationprojection.FilterNeverBackend(*ingress, snap)
	if err != nil {
		t.Fatalf("seam %s: FilterNeverBackend: %v", surface, err)
	}

	out := driftSeam{
		surface:       surface,
		loc:           loc,
		storedAnchor:  driftStoredAnchor(t, anchor),
		storedPolicy:  snap.Steering[0].AnchorMissingPolicy,
		filteredCount: evidence.FilteredCount,
	}
	for _, prov := range evidence.Provenance {
		out.provenanceCount++
		out.resolvedKind = prov.ResolvedKind
		if prov.ResolvedAnchor != nil {
			out.resolvedAnchor = *prov.ResolvedAnchor
		}
		out.overlayIdentity = prov.InjectedIdentity
	}
	out.overlayCopies, out.overlayAfter = driftSteeringPlacement(projected, loc)

	published, stats, err := driftRewriter(t, resolver).RewriteCall(&projected)
	if err != nil {
		t.Fatalf("seam %s: outbound rewrite: %v", surface, err)
	}
	out.rewritten = published != &projected
	out.statsEligible = stats.Eligible
	out.statsRewritten = stats.Rewritten
	out.bytesSaved = stats.BytesSaved()

	before, ok := driftFindAnchor(projected, loc)
	if !ok {
		t.Fatalf("seam %s: the anchored message must exist on the early projection", surface)
	}
	after, ok := driftFindAnchor(*published, loc)
	if !ok {
		t.Fatalf("seam %s: the rewrite must not remove the anchored message", surface)
	}
	identityBefore, err := conversationprojection.MessageIdentityOf(before)
	if err != nil {
		t.Fatalf("seam %s: identity before rewrite: %v", surface, err)
	}
	identityAfter, err := conversationprojection.MessageIdentityOf(after)
	if err != nil {
		t.Fatalf("seam %s: identity after rewrite: %v", surface, err)
	}
	out.identityBefore = identityBefore
	out.identityAfter = identityAfter
	out.identityLen = len(identityAfter)
	out.identityDrift = identityBefore != identityAfter
	out.part = driftInspectPart(after, loc, alias)

	controlBefore, ok := driftFindTextMessage(projected, driftFirstUserText)
	if !ok {
		t.Fatalf("seam %s: the path-free control message must exist", surface)
	}
	controlAfter, ok := driftFindTextMessage(*published, driftFirstUserText)
	if !ok {
		t.Fatalf("seam %s: the rewrite must not remove the path-free control message", surface)
	}
	idControlBefore, err := conversationprojection.MessageIdentityOf(controlBefore)
	if err != nil {
		t.Fatalf("seam %s: control identity before: %v", surface, err)
	}
	idControlAfter, err := conversationprojection.MessageIdentityOf(controlAfter)
	if err != nil {
		t.Fatalf("seam %s: control identity after: %v", surface, err)
	}
	out.stableIdentityDrift = idControlBefore != idControlAfter

	reasserted, _, rerr := conversationprojection.Reassert(*published, snap, evidence.Provenance, filtered)
	out.reassertErr = rerr
	out.anchorMissing = errors.Is(rerr, conversationprojection.ErrAnchorMissing)
	if rerr == nil {
		out.copiesAfterReassert, out.overlayAfterReassert = driftSteeringPlacement(reasserted, loc)
	}
	return out
}

// driftScaffold guards everything a RED assertion below depends on. Each surface must reach
// the final reassertion with the anchor already resolved, the selected payload rewritten, and
// every non-path sibling intact; otherwise the failure that follows would be a fixture
// failure rather than the brownfield conflict.
func driftScaffold(t *testing.T, got driftSeam) {
	t.Helper()
	if got.provenanceCount != 1 {
		t.Fatalf("fixture: surface %s: early projection must record exactly one overlay provenance entry: %s",
			got.surface, driftSeamState(got))
	}
	if got.resolvedKind != conversationprojection.PlacementAfterMessage {
		t.Fatalf("fixture: surface %s: the overlay must resolve as after_message during early projection: %s",
			got.surface, driftSeamState(got))
	}
	if got.resolvedAnchor != got.storedAnchor {
		t.Fatalf("fixture: surface %s: early projection must resolve the anchor that was stored, unchanged: %s",
			got.surface, driftSeamState(got))
	}
	if got.overlayCopies != 1 || !got.overlayAfter {
		t.Fatalf("fixture: surface %s: early projection must inject the overlay exactly once at the after-message placement: %s",
			got.surface, driftSeamState(got))
	}
	if got.filteredCount != 1 {
		t.Fatalf("fixture: surface %s: the never_backend tag must filter exactly the tagged message: %s",
			got.surface, driftSeamState(got))
	}
	if !got.rewritten || got.statsRewritten != 1 || got.statsEligible < 1 || got.bytesSaved <= 0 {
		t.Fatalf("fixture: surface %s: the real outbound rewriter must virtualize exactly the one selected path payload: %s",
			got.surface, driftSeamState(got))
	}
	if !got.part.found || !got.part.selectedCarriesAlias || got.part.selectedCarriesRealRoot {
		t.Fatalf("fixture: surface %s: the selected payload must carry the derived alias and no real-root occurrence: %s",
			got.surface, driftSeamState(got))
	}
	if got.part.nonPathSum != got.loc.expectedNonPath() || got.part.nonPathCount != boolToInt(got.loc.expectedNonPath() != 0) {
		t.Fatalf("requirements.md 2.8 - every non-selected member of the anchored payload must survive the rewrite byte-for-byte: %s expected_non_path_sum=%d",
			got.surface, got.loc.expectedNonPath())
	}
	if got.stableIdentityDrift {
		t.Fatalf("fixture: surface %s: the path-free control message must keep its identity across the rewrite, so the drift below is attributable to the selected path payload alone: %s",
			got.surface, driftSeamState(got))
	}
	if got.identityLen != len(got.identityBefore) {
		t.Fatalf("fixture: surface %s: identity drift must be a content change, not a shape change: %s",
			got.surface, driftSeamState(got))
	}
}

// driftSeamState renders one scenario as a failure-message fragment.
//
// Booleans, counts, byte totals, and the shared stage-ordinal renderer only. The message
// identity, the anchor, the alias, the real root, and the workspace tag are compared, not
// printed, so this characterization never emits a path or a path hash.
func driftSeamState(got driftSeam) string {
	var sb strings.Builder
	writeKV := func(key string, value string) {
		sb.WriteString(" ")
		sb.WriteString(key)
		sb.WriteString("=")
		sb.WriteString(value)
	}
	writeKV("surface", got.surface)
	writeKV("provenance_count", strconv.Itoa(got.provenanceCount))
	writeKV("resolved_kind", string(got.resolvedKind))
	writeKV("resolved_anchor_matches_stored", strconv.FormatBool(got.resolvedAnchor == got.storedAnchor))
	writeKV("early_overlay_copies", strconv.Itoa(got.overlayCopies))
	writeKV("early_overlay_after_anchor", strconv.FormatBool(got.overlayAfter))
	writeKV("filtered_count", strconv.Itoa(got.filteredCount))
	writeKV("eligible", strconv.Itoa(got.statsEligible))
	writeKV("rewritten", strconv.Itoa(got.statsRewritten))
	writeKV("bytes_saved", strconv.Itoa(got.bytesSaved))
	writeKV("identity_drift", strconv.FormatBool(got.identityDrift))
	writeKV("identity_length", strconv.Itoa(got.identityLen))
	writeKV("selected_carries_alias", strconv.FormatBool(got.part.selectedCarriesAlias))
	writeKV("selected_carries_real_root", strconv.FormatBool(got.part.selectedCarriesRealRoot))
	writeKV("non_path_sum", strconv.Itoa(got.part.nonPathSum))
	writeKV("non_path_count", strconv.Itoa(got.part.nonPathCount))
	writeKV("part_count", strconv.Itoa(got.part.partCount))
	writeKV("reassert_anchor_missing", strconv.FormatBool(got.anchorMissing))
	writeKV("reassert_overlay_copies", strconv.Itoa(got.copiesAfterReassert))
	writeKV("reassert_overlay_after_anchor", strconv.FormatBool(got.overlayAfterReassert))
	return strings.TrimPrefix(sb.String(), " ")
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// TestConversationViewReassert_PathRewriteDriftsFrozenAfterMessageAnchor is the pure seam
// half of Task 1.3 and the RED that defines what Task 5.3 has to repair.
//
// For three legacy surfaces it reconstructs the exact inputs the executor hands
// conversationprojection.Reassert, runs the feature's REAL pure rewriter over the
// early-projected call, and then separates two things that are currently conflated:
//
//   - what happens today. MessageIdentityOf drifts, the frozen after-message anchor no
//     longer resolves against the backend-shaped content, and Reassert returns
//     ErrAnchorMissing. Every one of those facts is measured and printed by the failing
//     RED assertion, which is the brownfield conflict this task has to document.
//   - what requirements.md 5.9 requires. The already-resolved placement must survive a
//     provably one-to-one, structure-preserving B-leg rewrite, so Reassert must succeed and
//     the overlay must still sit IMMEDIATELY after the same logical message. That
//     assertion is RED until Task 5.3 lands.
//
// The third surface is the control that shows the conflict is specifically an IDENTITY
// problem and not a "bytes changed" problem: its model-visible bytes change while its
// identity does not, and the exact identity-based reassertion therefore still succeeds
// today. design.md "4A" forbids treating identity equality as the lineage proof for exactly
// this reason.
func TestConversationViewReassert_PathRewriteDriftsFrozenAfterMessageAnchor(t *testing.T) {
	t.Parallel()

	callSurface := driftSeamRun(t, driftSurfaceCallArgument,
		driftCallAnchorMessage(), driftCallLocator(), driftResolver(t))
	// No shipped built-in declares a result selector or an opaque mode, so the legacy
	// tool-result surfaces need an operator profile. Requirements.md 2.2/3.2/3.7 make
	// exactly those two the supported ways to make a result surface path-bearing.
	opaqueSurface := driftSeamRun(t, driftSurfaceResultOpaqueText,
		driftResultTextAnchorMessage(), driftResultTextLocator(), driftOpaqueResultResolver(t))
	structuredSurface := driftSeamRun(t, driftSurfaceResultStructured,
		driftResultContentAnchorMessage(), driftResultContentLocator(), driftStructuredResultResolver(t))

	drifting := []driftSeam{callSurface, opaqueSurface}
	for _, got := range drifting {
		driftScaffold(t, got)
	}
	driftScaffold(t, structuredSurface)

	// -----------------------------------------------------------------------
	// THE PREMISE, AS A DURABLE ASSERTION.
	//
	// These facts do not change when requirements.md 5.9 is satisfied, so they are
	// asserted GREEN. They are what makes the RED below a real requirement rather than a
	// restatement of the fixture: the identity really does drift, the drift really is
	// confined to the one selected path payload, and it really is invisible to the overlay's
	// own identity.
	// -----------------------------------------------------------------------
	t.Run("identity_drift_is_real_and_localized_to_the_selected_path_payload", func(t *testing.T) {
		for _, got := range drifting {
			if !got.identityDrift {
				t.Fatalf("fixture: surface %s: MessageIdentityOf must change when the selected path payload is virtualized, because it is HashAtom(AtomOfMessage(msg)) over normalized semantic content that includes this payload: %s",
					got.surface, driftSeamState(got))
			}
			if got.identityLen != len(got.identityBefore) {
				t.Fatalf("fixture: surface %s: the drift must be a content change, not an identity-shape change: %s",
					got.surface, driftSeamState(got))
			}
			if got.overlayIdentity == got.identityAfter {
				t.Fatalf("fixture: surface %s: the overlay identity must be independent of the anchored message identity, otherwise the drift proves nothing", got.surface)
			}
		}
	})

	// -----------------------------------------------------------------------
	// WHAT requirements.md 5.9 REQUIRES (RED until Task 5.3).
	// -----------------------------------------------------------------------
	t.Run("requirements_5_9_resolved_placement_survives_a_proven_one_to_one_rewrite", func(t *testing.T) {
		// Errorf rather than Fatalf, one surface per iteration, so BOTH drifting surfaces
		// are reported: the legacy PartJSON argument and the legacy PartToolResult opaque
		// text must both reach this requirement.
		for _, got := range drifting {
			if got.reassertErr != nil {
				t.Errorf("requirements.md 5.9 and design.md \"4A. Transform-Stable Conversation-View Reassertion\" - virtualizing selected payload bytes inside the very complete message the anchor names must not by itself invalidate the placement the early projection already resolved: surface=%s reassert_failed=%t reassert_anchor_missing=%t identity_drift=%t provenance_count=%d resolved_kind=%s resolved_anchor_matches_stored=%t stored_policy_is_fail_closed=%t early_overlay_after_anchor=%t filtered_count=%d eligible=%d rewritten=%d bytes_saved=%d non_path_sum=%d non_path_count=%d part_count=%d",
					got.surface, got.reassertErr != nil, got.anchorMissing, got.identityDrift, got.provenanceCount, got.resolvedKind,
					got.resolvedAnchor == got.storedAnchor, got.storedPolicy == conversationprojection.AnchorFailClosed,
					got.overlayAfter, got.filteredCount,
					got.statsEligible, got.statsRewritten, got.bytesSaved,
					got.part.nonPathSum, got.part.nonPathCount, got.part.partCount)
				continue
			}
			if got.copiesAfterReassert != 1 {
				t.Errorf("design.md \"4A\" and the boundary context's adjacent expectations - overlay lifecycle must be unchanged: the projection-owned overlay must be present exactly once after final reassertion: surface=%s copies=%d overlay_immediately_after_anchor=%t",
					got.surface, got.copiesAfterReassert, got.overlayAfterReassert)
				continue
			}
			if !got.overlayAfterReassert {
				t.Errorf("requirements.md 5.9 and design.md \"4A\" - the frozen after-message placement must be carried through the rewrite, not relocated: surface=%s overlay_immediately_after_anchor=%t copies=%d",
					got.surface, got.overlayAfterReassert, got.copiesAfterReassert)
			}
		}
	})

	// -----------------------------------------------------------------------
	// THE CONTROL THAT MAKES THE RED A REAL RED.
	//
	// A rewrite that changes model-visible bytes WITHOUT changing MessageIdentityOf
	// keeps working today. So the failure above is caused by the identity drift, not by
	// the mutation, and no repair may be accepted that fixes it by weakening
	// MessageIdentityOf instead of proving lineage.
	// -----------------------------------------------------------------------
	t.Run("structured_result_content_rewrite_drifts_bytes_without_drifting_identity", func(t *testing.T) {
		got := structuredSurface
		if !got.rewritten || !got.part.selectedCarriesAlias || got.part.selectedCarriesRealRoot {
			t.Fatalf("fixture: the structured tool-result member must be rewritten by the declared result selector: %s",
				driftSeamState(got))
		}
		if got.identityDrift {
			t.Fatalf("fixture: conversationprojection.AtomOfMessage projects a legacy PartToolResult from its Text field only, so a rewrite of its structured Content must leave MessageIdentityOf unchanged - this characterization exists to pin that asymmetry: %s",
				driftSeamState(got))
		}
		if got.reassertErr != nil {
			t.Fatalf("fixture: with an unchanged identity the existing exact-resolution path must still succeed, which is the oracle proving the RED above is caused by the identity drift alone: surface=%s anchor_missing=%t",
				got.surface, got.anchorMissing)
		}
		if got.copiesAfterReassert != 1 || !got.overlayAfterReassert {
			t.Fatalf("fixture: the control must also restore the overlay exactly once at the frozen placement: surface=%s copies=%d overlay_immediately_after_anchor=%t",
				got.surface, got.copiesAfterReassert, got.overlayAfterReassert)
		}
	})

	// -----------------------------------------------------------------------
	// NO WEAKENING. design.md "4A" forbids the repair from changing identity derivation,
	// the stored anchor, persistence, overlay lifecycle, or the anchor-missing policy.
	// These assertions hold the fixture to the current semantics instead of accommodating
	// a change to them.
	// -----------------------------------------------------------------------
	t.Run("fixture_requires_no_conversation_view_semantics_change", func(t *testing.T) {
		scenarios := []struct {
			anchor aipapi.Message
			got    driftSeam
		}{
			{driftCallAnchorMessage(), callSurface},
			{driftResultTextAnchorMessage(), opaqueSurface},
			{driftResultContentAnchorMessage(), structuredSurface},
		}
		for _, scenario := range scenarios {
			anchor, got := scenario.anchor, scenario.got

			// (1) MessageIdentityOf's definition is untouched: it is still exactly
			// HashAtom(AtomOfMessage(msg)) over the normalized semantic content, and it
			// is still a validated versioned digest rather than anything looser.
			atom, err := conversationprojection.AtomOfMessage(anchor)
			if err != nil {
				t.Fatalf("fixture: surface %s: AtomOfMessage: %v", got.surface, err)
			}
			hashed, err := conversationprojection.HashAtom(atom)
			if err != nil {
				t.Fatalf("fixture: surface %s: HashAtom: %v", got.surface, err)
			}
			derived, err := conversationprojection.MessageIdentityOf(anchor)
			if err != nil {
				t.Fatalf("fixture: surface %s: MessageIdentityOf: %v", got.surface, err)
			}
			if derived != hashed {
				t.Fatalf("design.md \"4A\" constraint 1 - this fixture must not need a different identity definition; MessageIdentityOf must remain HashAtom(AtomOfMessage(msg)): surface=%s identity_is_content_hash=%t",
					got.surface, derived == hashed)
			}
			if err := derived.Validate(); err != nil {
				t.Fatalf("design.md \"4A\" constraint 1 - the identity must remain a validated versioned digest: surface=%s identity_valid=%t",
					got.surface, false)
			}

			// (2) The stored anchor is exactly the pre-virtualization identity with
			// occurrence 1, and the early projection reuses that very value.
			if got.storedAnchor.Identity != derived || got.storedAnchor.Occurrence != 1 {
				t.Fatalf("design.md \"4A\" constraint 1 - this fixture must not need the stored MessageAnchor{Identity, Occurrence} to change: surface=%s anchor_identity_is_pre_rewrite_identity=%t anchor_occurrence=%d",
					got.surface, got.storedAnchor.Identity == derived, got.storedAnchor.Occurrence)
			}
			if got.resolvedAnchor != got.storedAnchor {
				t.Fatalf("design.md \"4A\" constraint 1 - early projection must carry the stored anchor forward verbatim, not a re-derived one: surface=%s resolved_anchor_matches_stored=%t",
					got.surface, got.resolvedAnchor == got.storedAnchor)
			}
			// On a surface whose identity does drift, the stored anchor is provably NOT
			// the identity of the backend-shaped message. That is exactly why the frozen
			// anchor stops resolving today, and exactly what requirements.md 5.10 forbids
			// a repair from papering over by re-deriving the anchor from post-rewrite
			// content.
			if got.identityDrift && got.storedAnchor.Identity == got.identityAfter {
				t.Fatalf("design.md \"4A\" constraint 1 - the stored anchor must remain the PRE-virtualization identity; a repair may not re-derive it from post-rewrite content: surface=%s stored_anchor_equals_published_identity=%t",
					got.surface, true)
			}

			// (3) Overlay lifecycle is untouched: one active overlay, injected exactly
			// once, with an identity that depends only on the overlay's own payload and
			// is independent of the anchored message's identity.
			overlay := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(driftSteerText)}}
			overlayID, err := conversationprojection.MessageIdentityOf(overlay)
			if err != nil {
				t.Fatalf("fixture: surface %s: overlay identity: %v", got.surface, err)
			}
			if got.overlayIdentity != overlayID {
				t.Fatalf("design.md \"4A\" constraint 1 - overlay lifecycle must be unchanged; the injected copy's identity must still be the hash of the overlay payload alone: surface=%s overlay_identity_is_payload_hash=%t",
					got.surface, got.overlayIdentity == overlayID)
			}
			if got.overlayCopies != 1 {
				t.Fatalf("design.md \"4A\" constraint 1 - exactly one projection-owned overlay copy must exist on the early projection: surface=%s copies=%d",
					got.surface, got.overlayCopies)
			}
			if got.overlayIdentity == got.identityAfter {
				t.Fatalf("fixture: surface %s: the overlay identity must be independent of the anchored message identity, otherwise the drift assertions prove nothing", got.surface)
			}

			// (4) never_backend filtering is untouched: the tag still removes exactly the
			// tagged message from the projected trajectory.
			if got.filteredCount != 1 {
				t.Fatalf("design.md \"4A\" constraint 6 - never_backend filtering must remain authoritative and unchanged: exactly one tagged message must be filtered: surface=%s filtered_count=%d",
					got.surface, got.filteredCount)
			}
			if twoPassHasText(driftProjected(t, anchor), twoPassLocalText) {
				t.Fatalf("design.md \"4A\" constraint 6 - a never_backend-tagged message must stay out of the projected trajectory: surface=%s", got.surface)
			}

			// (5) The anchor-missing policy is untouched: it is still the strict fail-closed
			// policy this fixture stored, never the stable-prefix fallback. Whether the
			// policy then fires at all is the RED above's business; that it is still the
			// strict one, and that the repair may not swap it, is a fixture invariant.
			if got.storedPolicy != conversationprojection.AnchorFailClosed {
				t.Fatalf("design.md \"4A\" constraint 1 - this fixture must not need the anchor-missing policy to change: surface=%s stored_policy_is_fail_closed=%t",
					got.surface, got.storedPolicy == conversationprojection.AnchorFailClosed)
			}
		}
	})
}

// driftProjected rebuilds the early projection of one scenario's ingress call, which is the
// trajectory the never_backend assertion inspects.
func driftProjected(t *testing.T, anchor aipapi.Message) aipapi.Call {
	t.Helper()
	ingress := driftIngressCall(anchor)
	snap := driftSnapshot(t, anchor)
	projected, _, err := conversationprojection.Project(*ingress, snap)
	if err != nil {
		t.Fatalf("fixture: Project: %v", err)
	}
	return projected
}

// driftViewObserver records the bounded conversation-view diagnostics the executor publishes
// for one run. It delegates the stage ordinal to the shared recorder so the denial can be
// located in the same monotonic sequence as every other checkpoint.
type driftViewObserver struct {
	rec  *twoPassRecorder
	view *twoPassViewObserver

	mu             sync.Mutex
	stages         []string
	injected       []int
	afterMessage   []int
	filtered       []int
	failures       []string
	anchorFailures []conversationprojection.AnchorMissingPolicy
	fallbacks      []conversationprojection.AnchorMissingPolicy
}

func (o *driftViewObserver) OnProjection(stage string, summary conversationprojection.ProjectionSummary) {
	o.view.OnProjection(stage, summary)
	o.mu.Lock()
	o.stages = append(o.stages, stage)
	o.injected = append(o.injected, summary.InjectedCount)
	o.afterMessage = append(o.afterMessage, summary.AfterMessageCount)
	o.filtered = append(o.filtered, summary.FilteredCount)
	o.mu.Unlock()
}

func (o *driftViewObserver) OnProjectionFailure(stage string) {
	o.mu.Lock()
	o.failures = append(o.failures, stage)
	o.mu.Unlock()
}

func (o *driftViewObserver) OnAnchorFallback(_ string, policy conversationprojection.AnchorMissingPolicy) {
	o.mu.Lock()
	o.fallbacks = append(o.fallbacks, policy)
	o.mu.Unlock()
}

func (o *driftViewObserver) OnAnchorFailure(policy conversationprojection.AnchorMissingPolicy) {
	o.mu.Lock()
	o.anchorFailures = append(o.anchorFailures, policy)
	o.mu.Unlock()
}

func (o *driftViewObserver) early() (injected int, afterMessage int, filtered int, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, stage := range o.stages {
		if stage == conversationprojection.StageEarly {
			return o.injected[i], o.afterMessage[i], o.filtered[i], true
		}
	}
	return 0, 0, 0, false
}

func (o *driftViewObserver) failureStages() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.failures...)
}

// finalStageReached reports that the runtime reached the FINAL conversation-view stage at
// all, whether it completed or failed there. A failed reassertion never publishes a
// successful StageFinal projection summary, so a successful-projection-only check would
// report the denial as "the stage was never reached", which is the opposite of what
// happened.
func (o *driftViewObserver) finalStageReached() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if slices.Contains(o.stages, conversationprojection.StageFinal) {
		return true
	}
	return slices.Contains(o.failures, conversationprojection.StageFinal)
}

func (o *driftViewObserver) anchorFailurePolicies() []conversationprojection.AnchorMissingPolicy {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]conversationprojection.AnchorMissingPolicy(nil), o.anchorFailures...)
}

func (o *driftViewObserver) fallbackPolicies() []conversationprojection.AnchorMissingPolicy {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]conversationprojection.AnchorMissingPolicy(nil), o.fallbacks...)
}

// driftAttemptMarker records the candidate-attempt stage ordinal, delegates every decision to
// the REAL outbound.AttemptTransform, and then measures what the real pass did to the
// anchored message.
//
// The measurement is what turns the runtime half of this characterization into a proof rather
// than a guess: the state it reads back is the state the final reassertion is about to
// re-resolve the frozen anchor against.
type driftAttemptMarker struct {
	rec   *twoPassRecorder
	real  *outbound.AttemptTransform
	loc   driftLocator
	alias string

	mu       sync.Mutex
	part     driftPartState
	overlay  int
	afterAnc bool
}

func (m *driftAttemptMarker) ID() string                        { return m.real.ID() }
func (m *driftAttemptMarker) Order() int                        { return m.real.Order() }
func (m *driftAttemptMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *driftAttemptMarker) HandleAttempt(
	ctx context.Context,
	call *aipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (request.AttemptDecision, error) {
	m.rec.mark(twoPassCheckpointAttemptTransform)
	decision, err := m.real.HandleAttempt(ctx, call, meta, svc)
	if call != nil {
		part := driftPartState{}
		if anchor, ok := driftFindAnchor(*call, m.loc); ok {
			part = driftInspectPart(anchor, m.loc, m.alias)
		}
		overlay, after := driftSteeringPlacement(*call, m.loc)
		m.mu.Lock()
		m.part = part
		m.overlay = overlay
		m.afterAnc = after
		m.mu.Unlock()
	}
	return decision, err
}

func (m *driftAttemptMarker) observation() (driftPartState, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.part, m.overlay, m.afterAnc
}

// driftRun is the content-free record of one end-to-end executor run over one anchored
// surface.
type driftRun struct {
	surface string
	order   twoPassOrder

	turnCompleted bool
	anchorMissing bool
	execFailed    bool

	reachedReassert bool
	reachedPTB      bool
	reachedOpen     bool
	snapshotReads   int

	earlyInjected     int
	earlyAfterMessage int
	earlyFiltered     int
	earlySeen         bool
	failureStages     []string
	anchorFailures    []conversationprojection.AnchorMissingPolicy
	fallbacks         []conversationprojection.AnchorMissingPolicy

	part             driftPartState
	overlayAtAttempt int
	overlayAfter     bool

	attemptEligible  int
	attemptRewritten int
	lateRewritten    int
}

// driftState renders one run as a failure-message fragment: stage ordinals, counts, and
// booleans only.
func (r driftRun) driftState() string {
	var sb strings.Builder
	writeKV := func(key string, value string) {
		sb.WriteString(" ")
		sb.WriteString(key)
		sb.WriteString("=")
		sb.WriteString(value)
	}
	writeKV("surface", r.surface)
	writeKV("turn_completed", strconv.FormatBool(r.turnCompleted))
	writeKV("exec_failed", strconv.FormatBool(r.execFailed))
	writeKV("reassert_anchor_missing", strconv.FormatBool(r.anchorMissing))
	writeKV("reached_reassertion", strconv.FormatBool(r.reachedReassert))
	writeKV("reached_ptb", strconv.FormatBool(r.reachedPTB))
	writeKV("reached_backend_open", strconv.FormatBool(r.reachedOpen))
	writeKV("snapshot_reads", strconv.Itoa(r.snapshotReads))
	writeKV("early_injected", strconv.Itoa(r.earlyInjected))
	writeKV("early_after_message", strconv.Itoa(r.earlyAfterMessage))
	writeKV("early_filtered", strconv.Itoa(r.earlyFiltered))
	writeKV("projection_failure_stages", strconv.Itoa(len(r.failureStages)))
	writeKV("anchor_failures", strconv.Itoa(len(r.anchorFailures)))
	writeKV("anchor_fallbacks", strconv.Itoa(len(r.fallbacks)))
	writeKV("selected_carries_alias", strconv.FormatBool(r.part.selectedCarriesAlias))
	writeKV("selected_carries_real_root", strconv.FormatBool(r.part.selectedCarriesRealRoot))
	writeKV("non_path_sum", strconv.Itoa(r.part.nonPathSum))
	writeKV("part_count", strconv.Itoa(r.part.partCount))
	writeKV("overlay_copies_at_attempt", strconv.Itoa(r.overlayAtAttempt))
	writeKV("overlay_after_anchor_at_attempt", strconv.FormatBool(r.overlayAfter))
	writeKV("attempt_eligible", strconv.Itoa(r.attemptEligible))
	writeKV("attempt_rewritten", strconv.Itoa(r.attemptRewritten))
	writeKV("late_rewritten", strconv.Itoa(r.lateRewritten))
	sb.WriteString(" ")
	sb.WriteString(r.order.String())
	return strings.TrimPrefix(sb.String(), " ")
}

// driftExecute runs one end-to-end attempt with the feature's real two outbound passes and a
// steering overlay anchored on the path-bearing message.
//
// The runtime is wired exactly like the delivered two-pass characterization: the same frozen
// conversation-view reader, the same pinned workspace view both real passes read (the early
// one from its attempt metadata, the late one from the public SDK context projection), the
// same eligibility observer, and the same recording backend. Only the anchored message and
// the selected profile differ.
func driftExecute(t *testing.T, surface string, anchor aipapi.Message, loc driftLocator, resolver *pathvirtualization.Resolver) driftRun {
	t.Helper()

	rec := &twoPassRecorder{}
	reports := &hookRegReports{}
	observer := &driftViewObserver{rec: rec, view: &twoPassViewObserver{rec: rec}}
	marker := &driftAttemptMarker{
		rec:   rec,
		loc:   loc,
		alias: driftAliasOf(t),
		real: outbound.NewAttemptTransform(
			rewrite.ModeRewrite,
			resolver,
			outbound.WithReporter(reports.onAttempt),
		),
	}
	authority := twoPassWorkspaceResolver{root: twoPassRealRoot}
	late := outbound.NewRequestPartHook(
		rewrite.ModeRewrite,
		resolver,
		outbound.WithHookReporter(reports.onPart),
	)
	reader := &twoPassReader{snap: driftSnapshot(t, anchor)}

	bus := hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{&hookRegPartMarker{rec: rec, real: late}}})
	ex := runtime.TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua store: %v", err)
	}
	ex.Store = store
	ex.Bus = bus
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       authority,
		TrafficObserver: &twoPassTrafficObserver{rec: rec},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{marker},
		}),
	})
	ex.ConversationViewReader = reader
	ex.ConversationViewObserver = observer
	ex.EligibilityResolver = &twoPassEligibilityObserver{rec: rec}
	ex.Backends = map[string]execbackend.Backend{"two-pass": twoPassBackend(rec)}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	// The ordinary ingress path is required, not the detached auxiliary one: the detached
	// path pins an EMPTY workspace view, so the early real pass would have no authoritative
	// project root to derive a mapping from. On this path the runtime resolves the workspace
	// through the same snapshot resolver the late pass is handed and pins the result onto the
	// attempt metadata the early pass reads, which is what makes one shared resolver the right
	// thing to wire for both.
	stream, execErr := ex.Execute(context.Background(), driftIngressCall(anchor))
	if execErr == nil {
		_, execErr = aipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}

	raw := rec.observation(execErr == nil, reader.readCount())
	out := driftRun{
		surface:         surface,
		order:           twoPassOrderOf(raw),
		turnCompleted:   execErr == nil,
		execFailed:      execErr != nil,
		anchorMissing:   errors.Is(execErr, conversationprojection.ErrAnchorMissing),
		reachedReassert: raw.order.reassert != 0 || observer.finalStageReached(),
		reachedPTB:      raw.order.ptb != 0,
		reachedOpen:     raw.order.open != 0,
		snapshotReads:   reader.readCount(),
		failureStages:   observer.failureStages(),
		anchorFailures:  observer.anchorFailurePolicies(),
		fallbacks:       observer.fallbackPolicies(),
	}
	out.earlyInjected, out.earlyAfterMessage, out.earlyFiltered, out.earlySeen = observer.early()
	out.part, out.overlayAtAttempt, out.overlayAfter = marker.observation()
	attempt := reports.oneAttempt(t)
	out.attemptEligible = attempt.Stats.Eligible
	out.attemptRewritten = attempt.Stats.Rewritten
	out.lateRewritten = reports.onePart(t).Stats.Rewritten
	return out
}

// driftRuntimeScaffold guards everything the executor-level RED assertion depends on. Each
// surface must reach the final reassertion with the anchor already resolved, the selected
// payload virtualized, and every non-path sibling intact; otherwise the denial below would
// prove nothing.
func driftRuntimeScaffold(t *testing.T, got driftRun, loc driftLocator) {
	t.Helper()
	if !got.earlySeen || got.earlyInjected != 1 || got.earlyAfterMessage != 1 {
		t.Fatalf("fixture: the after-message steering overlay must resolve during early projection from A-leg/client truth, before any outbound pass runs: %s", got.driftState())
	}
	if got.earlyFiltered != 1 {
		t.Fatalf("design.md \"4A\" constraint 6 - never_backend filtering must remain authoritative and unchanged; exactly one tagged message must be filtered by the early projection: %s", got.driftState())
	}
	if !got.reachedReassert {
		t.Fatalf("fixture: the final conversation-view reassertion stage must be reached: %s", got.driftState())
	}
	if got.attemptEligible < 1 || got.attemptRewritten != 1 {
		t.Fatalf("fixture: the real early outbound pass must virtualize exactly the one selected path payload: %s", got.driftState())
	}
	if got.lateRewritten != 0 {
		t.Fatalf("requirements.md 2.9 - the idempotent late pass must contribute nothing once the early pass published the alias: %s", got.driftState())
	}
	if !got.part.found || !got.part.selectedCarriesAlias || got.part.selectedCarriesRealRoot {
		t.Fatalf("fixture: the selected payload must carry the derived alias and no real-root occurrence by the time the request reaches the reassertion: %s", got.driftState())
	}
	if got.part.nonPathSum != loc.expectedNonPath() {
		t.Fatalf("requirements.md 2.8 - every non-selected argument member must survive the rewrite byte-for-byte: %s expected_non_path_sum=%d",
			got.driftState(), loc.expectedNonPath())
	}
	if got.part.partCount != 1 {
		t.Fatalf("design.md \"4A\" lineage requires a stable ordered part cardinality; the fixture message must stay a single-part message: %s", got.driftState())
	}
	if got.overlayAtAttempt != 1 || !got.overlayAfter {
		t.Fatalf("fixture: the projection-owned overlay must be present exactly once at its after-message placement when the reassertion runs, so the denial is about the anchor and not about a missing overlay: %s", got.driftState())
	}
	if got.snapshotReads != 1 {
		t.Fatalf("design.md \"4A\" constraint 3 - the final reassertion must reuse the FROZEN request-local snapshot and must not read the conversation-view store again; snapshot reads = %d: %s",
			got.snapshotReads, got.driftState())
	}
}

// TestOutboundAttempt_PathVirtualizationAnchorDriftDeniesTurnPreBackend is the runtime half of
// Task 1.3: the same conflict as the pure seam, observed through the real executor with the
// feature's real two outbound passes.
//
// It separates the same two things:
//
//   - what requirements.md 5.9/5.10 require (RED): the turn must reach the backend with the
//     steering overlay still exactly once at its frozen after-message placement. The failing
//     output carries the complete measured denial it has to beat: ErrAnchorMissing from the
//     final Reassert, one AnchorFailClosed publication at the final stage, no stable-prefix
//     fallback, no per-turn buffer, and Backend.Open never reached.
//   - the durable premise (GREEN): the frozen projection evidence is resolved once from
//     A-leg truth, replayed once with no second store read, the stored anchor-missing
//     policy is still the strict fail-closed one and was never downgraded, and the overlay
//     is still exactly one copy immediately after the anchored message when the final
//     reassertion runs.
//
// It also proves the conflict needs no second harness and no new runtime seam: the anchor
// resolves during early projection from the A-leg/client truth, the selected path payload is
// virtualized afterwards by the real passes, and the frozen conversation view is read exactly
// once, so the final reassertion really is re-resolving against post-virtualization content
// rather than against a stale snapshot.
func TestOutboundAttempt_PathVirtualizationAnchorDriftDeniesTurnPreBackend(t *testing.T) {
	t.Parallel()

	callLocator := driftCallLocator()
	callRun := driftExecute(t, driftSurfaceCallArgument, driftCallAnchorMessage(), callLocator, driftResolver(t))
	opaqueLocator := driftResultTextLocator()
	opaqueRun := driftExecute(t, driftSurfaceResultOpaqueText, driftResultTextAnchorMessage(), opaqueLocator, driftOpaqueResultResolver(t))

	runs := []struct {
		loc driftLocator
		got driftRun
	}{
		{callLocator, callRun},
		{opaqueLocator, opaqueRun},
	}
	for _, run := range runs {
		driftRuntimeScaffold(t, run.got, run.loc)
	}

	// -----------------------------------------------------------------------
	// THE PREMISE, AS A DURABLE ASSERTION.
	//
	// None of this changes when requirements.md 5.9 is satisfied: the frozen projection
	// evidence is still resolved once from A-leg truth, replayed once without a store
	// read, the fail-closed policy is still the stored one and is still never downgraded
	// to a stable-prefix fallback, and the overlay is still exactly one copy at its
	// after-message placement when the reassertion runs.
	// -----------------------------------------------------------------------
	t.Run("frozen_projection_evidence_is_reused_without_a_second_store_read", func(t *testing.T) {
		for _, run := range runs {
			got := run.got
			if !got.earlySeen || got.earlyInjected != 1 || got.earlyAfterMessage != 1 {
				t.Fatalf("fixture: the after-message steering overlay must resolve during early projection from A-leg/client truth, before any outbound pass runs: %s", got.driftState())
			}
			if got.overlayAtAttempt != 1 || !got.overlayAfter {
				t.Fatalf("design.md \"4A\" and the boundary context's adjacent expectations - overlay lifecycle must be unchanged: the projection-owned overlay must still be exactly one copy immediately after the anchored message when the final reassertion runs: %s",
					got.driftState())
			}
			if got.snapshotReads != 1 {
				t.Fatalf("design.md \"4A\" constraint 3 - the final reassertion must reuse the FROZEN request-local snapshot and must not read the conversation-view store again; snapshot reads = %d: %s",
					got.snapshotReads, got.driftState())
			}
			if len(got.fallbacks) != 0 {
				t.Fatalf("design.md \"4A\" constraint 1 - the fallback policy must be unchanged; the stored fail-closed policy must never be downgraded to a stable-prefix fallback: %s anchor_fallbacks=%d",
					got.driftState(), len(got.fallbacks))
			}
			for _, policy := range got.anchorFailures {
				if policy != conversationprojection.AnchorFailClosed {
					t.Fatalf("design.md \"4A\" constraint 1 - the only anchor-missing policy this fixture stores is fail_closed: %s", got.driftState())
				}
			}
		}
	})

	// -----------------------------------------------------------------------
	// WHAT requirements.md 5.9/5.10 REQUIRE (RED until Task 5.3).
	// -----------------------------------------------------------------------
	t.Run("requirements_5_9_turn_survives_a_proven_one_to_one_backend_only_rewrite", func(t *testing.T) {
		// Errorf rather than Fatalf so both anchored surfaces are reported.
		for _, run := range runs {
			got := run.got
			if got.execFailed {
				t.Errorf("requirements.md 5.9 and design.md \"4A. Transform-Stable Conversation-View Reassertion\" - once the early projection has resolved an after-message anchor from A-leg/client truth, virtualizing selected payload bytes inside that same complete message must not by itself invalidate the frozen placement at final backend-bound reassertion; today the anchor can no longer resolve, the fail-closed policy fires, and the turn is denied before Backend.Open: surface=%s reassert_anchor_missing=%t reached_final_reassertion=%t projection_failure_stages=%d anchor_failures=%d anchor_fallbacks=%d reached_ptb=%t backend_open_stage=%d snapshot_reads=%d early_injected=%d early_after_message=%d early_filtered=%d attempt_rewritten=%d",
					got.surface, got.anchorMissing, got.reachedReassert, len(got.failureStages),
					len(got.anchorFailures), len(got.fallbacks), got.reachedPTB, got.order.Open,
					got.snapshotReads, got.earlyInjected, got.earlyAfterMessage, got.earlyFiltered,
					got.attemptRewritten)
			}
		}
	})
}

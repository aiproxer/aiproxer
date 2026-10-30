package pathvirtualization_test

// Spec: b-leg-path-virtualization Task 10.2, protocol-family half. Requirements 2.8,
// 5.8, 8.6, 8.7.
//
// Design sections consulted: "Testing Strategy / Protocol-certification", "Ownership",
// "Existing Architecture and Placement", "Canonical Outbound Rewriter", and "4A.
// Transform-Stable Conversation-View Reassertion".
//
// WHY THIS FILE IS IN THE FEATURE TREE AND NOT IN CORE
//
// The property under test is that a PROTOCOL-FAMILY ADAPTER carries the canonical mutation
// unchanged, which means driving the real adapter. Core must not import protocol wire
// codecs - internal/archtest enforces that for every package under internal/core - so a
// core test could not drive the OpenResponses codec without moving an ownership boundary
// this specification explicitly does not own. The dependency direction is also the one
// requirement 5.8 states: an adapter must remain unaware of path virtualization, so the
// measurement belongs on the feature side, importing the adapters, and never the reverse.
//
// NO CARTESIAN COVERAGE, DELIBERATELY
//
// Requirement 8.6 and design.md "Testing Strategy / Protocol-certification" ask for
// canonical/family evidence rather than a frontend-by-backend product. This file holds
// exactly ONE REPRESENTATIVE PER FAMILY:
//
//   - openresponses            the item-authoritative family, through its own request
//                              encoder and its own decoder;
//   - openresponses via the canonical authority bridge
//                              the SAME family reached from a legacy message-authority
//                              history through aipapi.ProjectLegacyToOrderedItems, which is
//                              the bridge an item-authoritative backend applies to an A-leg
//                              that speaks a message-authority dialect. This is a bridge,
//                              not a pairing: no frontend and no backend is crossed with
//                              any other;
//   - anthropic-messages       one additional family sentinel, chosen because it is the
//                              most structurally DIFFERENT legacy-chat adapter available: it
//                              wraps tool calls in typed content blocks rather than raw
//                              argument JSON and folds a tool result into a user message.
//
// Two families with the same canonical authority form are NOT both instantiated. That is
// the point: the mutation is a canonical one, so one representative per authority form plus
// one structurally-different sentinel certifies adapter neutrality for all of them.
//
// WHAT IS ASSERTED, AND WHY IT IS NOT VACUOUS
//
// For every representative: the selected path-bearing member must carry the derived alias
// and no real-root occurrence, every unselected member must survive byte-for-byte, the
// stable tool-call identity and canonical tool name must survive, and no byte of the
// adapter-produced body may carry the real root.
//
// Every representative is measured TWICE: once with the feature's REAL rewrite applied and
// once with the rewrite withheld by an empty compiled policy. The withheld run must still
// carry the real root on every selected member and no alias at all, which is what makes the
// positive measurement attributable to the rewrite rather than to the fixture.
//
// FAMILY REPRESENTATION IS NOT CANONICAL IDENTITY
//
// A family adapter is free to choose its own wire representation, and these two do. The
// OpenResponses codec puts a tool call's argument document in a JSON STRING on the wire, so
// its decoder returns the document still string-wrapped; and it normalizes a structured
// result content part into a text part whose text is the document. Neither is a change to
// the payload. The measurement therefore compares the DOCUMENT the adapter carried, after
// unwrapping the family's own envelope, and separately asserts the stable identities the
// canonical contract does own. For the encoder-only sentinel the measurement is made on the
// serialized body directly, because that adapter publishes no decode entry point and
// requirement 5.2 is about what reaches the wire.
//
// OBSERVABILITY
//
// No path, alias, workspace tag, tool-call identity, or argument byte reaches a failure
// message. Selected and unselected members are compared; the derived alias is only ever used
// with a containment test.
//
// DETERMINISM
//
// Pure mapping: no concurrency, no I/O, no clock, no RNG. -count=5 is stable.

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	anmessages "github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	orproto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// famRoot is the authoritative client-visible project root this file publishes an
	// alias for. It is a fixed fixture value, long enough that the derived alias is
	// strictly shorter (requirement 1.4), and it is never formatted.
	famRoot = "/home/dev/workspaces/aip-path-virtualization-worktree/packages/agent-runtime"

	// famToolName is the exact canonical tool name the compiled policy claims. Exact name
	// matching is what requirement 3.6 demands, so an adapter that renamed the tool on the
	// wire would be caught rather than tolerated.
	famToolName = "read_file"

	// famSelectedMember is the argument and structured-result member the policy claims.
	famSelectedMember = "file_path"

	// famLimitMember, famLimitValue, famNonPathMember, and famNonPathValue are the
	// unselected siblings. The tool-call document carries two of them and the tool-result
	// document one, so no surface's preservation check can pass by comparing a single
	// member twice, and the two documents do not share a member set.
	famLimitMember   = "max_bytes"
	famLimitValue    = 7
	famNonPathMember = "bytes_read"
	famNonPathValue  = 4242

	// famCallID is the stable tool-call identity both canonical fixtures use. The same
	// identity in the legacy and the item authority is what lets one surface definition
	// address the same logical payload in either form, including across the canonical
	// authority bridge, which preserves it. It is compared, never formatted.
	famCallID = "fam-call"

	// famMessageItemID, famToolCallItemID, famToolResultItemID, and famTailItemID are the
	// stable item identities of the item-authoritative fixture, in trajectory order.
	famMessageItemID    = "fam-msg"
	famToolCallItemID   = "fam-tc"
	famToolResultItemID = "fam-tr"
	famTailItemID       = "fam-msg-tail"

	// famCallSuffix and famResultSuffix are the two path-bearing suffixes. They differ so a
	// rewrite of one surface can never be mistaken for a rewrite of the other, and each is
	// identical in real and virtualized form so any difference at a later stage is
	// attributable to the root prefix alone.
	famCallSuffix   = "src/family_call.go"
	famResultSuffix = "src/family_result.go"

	// famModel is the wire model the additional family sentinel resolves. It is a bounded
	// fixture string with no path content.
	famModel = "family-sentinel-model"

	// maxFamilyScanDepth bounds the recursive body scan so a malformed or unexpectedly deep
	// adapter body cannot turn this test into an unbounded walk.
	maxFamilyScanDepth = 64
)

// famCallDocument is the complete path-bearing tool-call argument document in real-root
// form: the selected member plus two unselected siblings.
func famCallDocument() string {
	return `{"` + famSelectedMember + `":"` + famRoot + `/` + famCallSuffix + `",` +
		`"` + famLimitMember + `":` + strconv.Itoa(famLimitValue) + `,` +
		`"` + famNonPathMember + `":` + strconv.Itoa(famNonPathValue) + `}`
}

// famResultDocument is the complete path-bearing tool-result document in real-root form:
// the selected member plus one unselected sibling.
func famResultDocument() string {
	return `{"` + famSelectedMember + `":"` + famRoot + `/` + famResultSuffix + `",` +
		`"` + famNonPathMember + `":` + strconv.Itoa(famNonPathValue) + `}`
}

// famTools is the declared tool contract. Both path-bearing documents spell their members
// explicitly, so no measurement here depends on the optional schema-inference step and every
// assertion isolates adapter pass-through.
func famTools() []aipapi.ToolDef {
	return []aipapi.ToolDef{{
		Name: famToolName,
		Parameters: []byte(`{"type":"object","properties":{"` + famSelectedMember +
			`":{"type":"string"},"` + famLimitMember + `":{"type":"integer"},"` +
			famNonPathMember + `":{"type":"integer"}}}`),
	}}
}

// famResolver compiles the operator policy this file drives: an exact-name profile claiming
// the selected argument member and the selected structured-result member for this one tool
// name.
func famResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{{
		Names:              []string{famToolName},
		ArgPointers:        []string{"/" + famSelectedMember},
		ResultJSONPointers: []string{"/" + famSelectedMember},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: the operator profile must compile: reject is a bounded, content-free code")
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: the resolver must be constructed: reject is a bounded, content-free code")
	}
	return resolver
}

// famMapping derives this fixture's mapping. It is called per use rather than cached so two
// derivations in one test cannot disagree.
func famMapping(t *testing.T) pathvirtualization.Mapping {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(famRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the fixture root must derive an active mapping; the refusal reason is a bounded, content-free code")
	}
	if len(mapping.VirtualRoot) >= len(famRoot) {
		t.Fatalf("fixture: requirement 1.4 - the derived alias must be shorter than the real root, otherwise outbound virtualization is inactive")
	}
	return mapping
}

// famAlias returns the alias the feature derives for famRoot. It is compared, never
// formatted: requirement 7.7 keeps the workspace identity tag out of every observable
// dimension.
func famAlias(t *testing.T) string {
	t.Helper()
	return famMapping(t).VirtualRoot
}

// famRewrite applies the feature's REAL pure outbound rewriter to one canonical call.
func famRewrite(t *testing.T, resolver *pathvirtualization.Resolver, call *aipapi.Call) (*aipapi.Call, rewrite.Stats) {
	t.Helper()
	out, stats, err := rewrite.New(famMapping(t), resolver).RewriteCall(call)
	if err != nil {
		t.Fatalf("fixture: the real outbound rewrite: %v", err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the rewritten call must stay canonically valid: %v", err)
	}
	return out, stats
}

// famWithheld returns the same call with the rewrite withheld by a compiled policy that
// selects nothing.
//
// This is the same withholding mechanism the runtime-level regression uses, so both halves
// of this task measure one variable: whether the feature's rewrite ran.
func famWithheld(t *testing.T, call *aipapi.Call) *aipapi.Call {
	t.Helper()
	empty, reject := pathvirtualization.NewResolver(nil, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: an empty compiled policy must compile: reject is a bounded, content-free code")
	}
	out, stats := famRewrite(t, empty, call)
	if stats.Rewritten != 0 {
		t.Fatalf("fixture: the withheld policy must select nothing: rewritten=%d", stats.Rewritten)
	}
	return out
}

// ---------------------------------------------------------------------------
// Canonical fixtures, one per canonical authority form.
// ---------------------------------------------------------------------------

// famItemCall is the item-authoritative (OpenResponses) history: two path-free message items
// bracketing a path-bearing tool-call item and a path-bearing tool-result item.
//
// The tool-result item carries its selected location as a structured JSON content part, which
// is the item-authority structured surface requirement 2.2 addresses.
func famItemCall() *aipapi.Call {
	return &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "family:m"},
		Tools:      famTools(),
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      famMessageItemID,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "family-item-open"}},
			},
			{
				Kind: aipapi.ItemKindToolCall,
				ID:   famToolCallItemID,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    famCallID,
					Name:      famToolName,
					Arguments: json.RawMessage(famCallDocument()),
				},
			},
			{
				Kind: aipapi.ItemKindToolResult,
				ID:   famToolResultItemID,
				ToolResult: &aipapi.ToolResultItem{
					CallID: famCallID,
					Name:   famToolName,
					Parts: []aipapi.ContentPart{{
						Kind: aipapi.ContentPartJSON,
						Text: famResultDocument(),
					}},
				},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      famTailItemID,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "family-item-close"}},
			},
		},
	}
}

// famLegacyCall is the legacy chat (message authority) history: a path-free user message, a
// path-bearing historical tool call, a path-bearing historical tool result, and a terminal
// user message.
//
// The tool result's selected location is its opaque Text line rather than a structured
// document, for one reason: this fixture is also the input to the canonical authority bridge,
// and that bridge carries a legacy tool result's Text as the item-authority Output while it
// has no carrier for a legacy structured tool-result document. Both path-bearing surfaces
// still coexist in ONE history, so a rewriter that only understood one of them would fail
// here.
func famLegacyCall() *aipapi.Call {
	return &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "family:m"},
		Tools:      famTools(),
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("family-legacy-open")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: famCallID,
				ToolName:   famToolName,
				Content:    json.RawMessage(famCallDocument()),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: famCallID,
				ToolName:   famToolName,
				Text:       famRoot + "/" + famResultSuffix,
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("family-legacy-close")}},
		},
	}
}

// famOpaqueResolver compiles the operator policy this file needs to make an opaque tool
// result path-bearing. No shipped built-in declares an opaque mode, so requirements.md 2.6
// and 3.7 make an operator profile declaring one the supported way.
func famOpaqueResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{{
		Names:              []string{famToolName},
		ArgPointers:        []string{"/" + famSelectedMember},
		ResultJSONPointers: []string{"/" + famSelectedMember},
		OpaqueResultMode:   pathvirtualization.OpaqueResultModePathLines,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: the opaque-mode operator profile must compile: reject is a bounded, content-free code")
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("fixture: the resolver must be constructed: reject is a bounded, content-free code")
	}
	return resolver
}

// ---------------------------------------------------------------------------
// Content-free probes.
// ---------------------------------------------------------------------------

// famSurface is one path-bearing surface of one canonical authority form.
//
// document returns the selected-location DOCUMENT the surface carries, addressed by the
// stable tool-call identity rather than by an ordinal, with the family's own envelope
// removed: a document the adapter wrapped in a JSON string, or re-homed into a different
// content-part kind, is unwrapped here. A missing surface is reported through ok.
type famSurface struct {
	label    string
	document func(call aipapi.Call) ([]byte, bool)
}

// famLegacySurfaces are the two path-bearing surfaces of the legacy chat fixture.
var famLegacySurfaces = []famSurface{
	{
		label: "legacy_tool_call_argument",
		document: func(call aipapi.Call) ([]byte, bool) {
			for _, msg := range call.Messages {
				for _, part := range msg.Parts {
					if part.Kind == aipapi.PartJSON && part.ToolCallID == famCallID {
						return part.Content, len(part.Content) > 0
					}
				}
			}
			return nil, false
		},
	},
	{
		label: "legacy_tool_result_opaque_text",
		document: func(call aipapi.Call) ([]byte, bool) {
			for _, msg := range call.Messages {
				for _, part := range msg.Parts {
					if part.Kind == aipapi.PartToolResult && part.ToolCallID == famCallID {
						return []byte(part.Text), part.Text != ""
					}
				}
			}
			return nil, false
		},
	},
}

// famItemSurfaces are the two path-bearing surfaces of an item-authoritative trajectory,
// whether it started as items or arrived through the canonical authority bridge.
//
// Both are addressed by the stable tool-call identity. Each accepts the envelopes its own
// path can arrive in, because an envelope is the FAMILY's representation choice and not a
// canonical property: the OpenResponses wire form spells a function call's arguments as a
// JSON STRING and normalizes a structured result content part into a TEXT part, while the
// canonical authority bridge carries a legacy tool result into an opaque Output. All of those
// carry the same payload, so they are accepted as the same surface rather than counted as
// separate ones.
var famItemSurfaces = []famSurface{
	{
		label: "item_tool_call_arguments",
		document: func(call aipapi.Call) ([]byte, bool) {
			for _, item := range call.Items {
				if item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil &&
					item.ToolCall.CallID == famCallID {
					return famUnwrapDocument(item.ToolCall.Arguments)
				}
			}
			return nil, false
		},
	},
	{
		label: "item_tool_result_payload",
		document: func(call aipapi.Call) ([]byte, bool) {
			for _, item := range call.Items {
				if item.Kind != aipapi.ItemKindToolResult || item.ToolResult == nil ||
					item.ToolResult.CallID != famCallID {
					continue
				}
				if item.ToolResult.Output != "" {
					return []byte(item.ToolResult.Output), true
				}
				for _, part := range item.ToolResult.Parts {
					switch part.Kind {
					case aipapi.ContentPartJSON, aipapi.ContentPartText:
						return famUnwrapDocument([]byte(part.Text))
					default:
					}
				}
			}
			return nil, false
		},
	},
}

// famUnwrapDocument removes a family's own envelope from a carried document: a document the
// wire form spelled as a JSON STRING is unescaped back to the document it wraps. A document
// that is already an object is returned unchanged.
func famUnwrapDocument(raw []byte) ([]byte, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return raw, json.Valid(raw)
	}
	return famUnwrapDocument([]byte(asString))
}

// famMeasure is the content-free comparison of one selected-location value against the
// ingress form of the same surface.
type famMeasure struct {
	valid            bool
	selectedAlias    bool
	selectedRealRoot bool
	selectedChanged  bool
	unselectedCount  int
	unselectedEqual  int
}

// famCompareStructured compares the ingress and a later form of one structured document.
func famCompareStructured(ingress, later []byte, alias string) famMeasure {
	out := famMeasure{}
	var before, after map[string]json.RawMessage
	if json.Unmarshal(ingress, &before) != nil {
		return out
	}
	if json.Unmarshal(later, &after) != nil {
		return out
	}
	var ingressSelected, laterSelected string
	if json.Unmarshal(before[famSelectedMember], &ingressSelected) != nil {
		return out
	}
	if json.Unmarshal(after[famSelectedMember], &laterSelected) != nil {
		return out
	}
	out.valid = true
	out.selectedAlias = strings.Contains(laterSelected, alias)
	out.selectedRealRoot = strings.Contains(laterSelected, famRoot)
	out.selectedChanged = ingressSelected != laterSelected
	for name, raw := range before {
		if name == famSelectedMember {
			continue
		}
		out.unselectedCount++
		if bytes.Equal(raw, after[name]) {
			out.unselectedEqual++
		}
	}
	return out
}

// famCompareOpaque compares the ingress and a later form of one opaque selected value.
//
// The whole value must have been mutated, it must carry the alias and no real root, and it
// must equal the alias-rooted composition of the ingress value's own suffix. That last check
// is the strongest available form of requirement 2.8's byte-for-byte preservation for a value
// whose bytes the feature is allowed to change at exactly one prefix: everything after the
// root must be identical.
func famCompareOpaque(ingress, later []byte, alias string) famMeasure {
	out := famMeasure{valid: len(ingress) > 0 && len(later) > 0}
	if !out.valid {
		return out
	}
	before, after := string(ingress), string(later)
	expected, rooted := famAliasFormOfValue(before, alias)
	out.selectedAlias = strings.Contains(after, alias)
	out.selectedRealRoot = strings.Contains(after, famRoot)
	out.selectedChanged = before != after
	out.unselectedCount = 1
	if rooted && after == expected {
		out.unselectedEqual = 1
	}
	return out
}

// famCompare measures one surface against its ingress form.
//
// The kind is DETECTED from the ingress value rather than declared per surface, because the
// same logical surface legitimately arrives in two forms: a structured document on the
// argument and structured-result paths, and a bare real-rooted string on an opaque result
// path. A fixture that declared the kind could therefore disagree with the payload it
// carries, and the mismatch would read as a failed comparison rather than a broken fixture.
func famCompare(surface famSurface, ingress, later []byte, alias string) famMeasure {
	if famIsStructuredDocument(ingress) {
		return famCompareStructured(ingress, later, alias)
	}
	return famCompareOpaque(ingress, later, alias)
}

// famIsStructuredDocument reports whether a carried value is a JSON object spelling the
// selected member as a string.
func famIsStructuredDocument(value []byte) bool {
	var document map[string]json.RawMessage
	if json.Unmarshal(value, &document) != nil {
		return false
	}
	var selected string
	return json.Unmarshal(document[famSelectedMember], &selected) == nil
}

// famPickFirstSurface returns the first surface of the set the given call actually carries,
// together with that surface's ingress and later documents.
//
// A surface set may list the same logical payload under two family representations (an
// item tool call's arguments, once as an object and once string-wrapped). Exactly one
// representation is present in any given call, so the set is scanned in order and the first
// present surface is measured; a call carrying NONE of them fails loudly.
func famPickFirstSurface(t *testing.T, label string, ingress, later aipapi.Call, surfaces []famSurface, alias string) (famSurface, famMeasure) {
	t.Helper()
	for _, surface := range surfaces {
		ingressDoc, okIngress := surface.document(ingress)
		laterDoc, okLater := surface.document(later)
		if !okIngress || !okLater {
			continue
		}
		return surface, famCompare(surface, ingressDoc, laterDoc, alias)
	}
	t.Fatalf("fixture: no listed surface was present on both the ingress and the adapter-decoded call: family=%s surfaces=%d", label, len(surfaces))
	return famSurface{}, famMeasure{}
}

// famAssertSurface is the shared assertion body for every representative: the selected member
// must carry the alias and no real root, must actually have changed, and every unselected
// member must survive byte-for-byte.
func famAssertSurface(t *testing.T, label string, surface famSurface, m famMeasure, alias string) {
	t.Helper()
	if !m.valid {
		t.Errorf("fixture: both forms of the %s payload must be readable: family=%s surface=%s", surface.label, label, surface.label)
		return
	}
	if !m.selectedAlias || m.selectedRealRoot {
		t.Errorf("requirements.md 5.8 - the adapter must carry the canonical mutation unchanged, so the %s selected member must still carry the alias and no real root: family=%s surface=%s selected_carries_alias=%t selected_carries_real_root=%t",
			surface.label, label, surface.label, m.selectedAlias, m.selectedRealRoot)
		return
	}
	if !m.selectedChanged {
		t.Errorf("requirements.md 5.8 - the %s selected member must have been mutated before the adapter ran; an unchanged member would make the pass-through claim vacuous: family=%s surface=%s",
			surface.label, label, surface.label)
		return
	}
	if m.unselectedEqual != m.unselectedCount || m.unselectedCount == 0 {
		t.Errorf("requirements.md 2.8 - every non-selected member of the %s payload must survive the adapter byte-for-byte: family=%s surface=%s unselected_equal=%d unselected_count=%d",
			surface.label, label, surface.label, m.unselectedEqual, m.unselectedCount)
	}
}

// famAssertWithheld is the non-vacuity oracle: the SAME fixture, encoded and decoded with the
// rewrite withheld, must still carry the real root on its selected member and no alias at all.
func famAssertWithheld(t *testing.T, label string, surface famSurface, m famMeasure, alias string) {
	t.Helper()
	if !m.valid {
		t.Fatalf("fixture: the withheld measurement must be readable: family=%s surface=%s", label, surface.label)
	}
	if !m.selectedRealRoot || m.selectedAlias || m.selectedChanged {
		t.Fatalf("fixture: with the rewrite withheld the %s member must be untouched, otherwise the pass-through assertion above is vacuous: family=%s surface=%s selected_real_root=%t selected_carries_alias=%t selected_changed=%t",
			surface.label, label, surface.label, m.selectedRealRoot, m.selectedAlias, m.selectedChanged)
	}
}

// famScalars is every string and every number an adapter-produced body carries, including the
// contents of any string that itself spells a JSON document.
//
// This is what makes the encoder-only sentinel measurable without a decoder: the payload the
// adapter carried is recovered from its own serialized form, wherever the adapter chose to put
// it. Numbers are collected in their decoded textual form so a non-path numeric sibling can be
// matched without depending on the adapter's JSON formatting.
func famScalars(body []byte) []string {
	var out []string
	famCollectScalars(body, 0, &out)
	return out
}

func famCollectScalars(raw []byte, depth int, out *[]string) {
	if depth > maxFamilyScanDepth {
		return
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return
	}
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
		// A string that spells a document carries further payload; scanning it keeps the
		// measurement independent of how deeply the adapter nested its envelope.
		famCollectScalars([]byte(typed), depth+1, out)
	case json.Number:
		*out = append(*out, typed.String())
	case []any:
		for _, element := range typed {
			encoded, err := json.Marshal(element)
			if err != nil {
				continue
			}
			famCollectScalars(encoded, depth+1, out)
		}
	case map[string]any:
		for _, element := range typed {
			encoded, err := json.Marshal(element)
			if err != nil {
				continue
			}
			famCollectScalars(encoded, depth+1, out)
		}
	}
}

// famAssertBodyCarriesAlias is the encoder-only form of the surface assertion: the expected
// alias-rooted value must be present in the adapter's own serialized body, the real-rooted
// form must be absent from the whole body, and the unselected member must have reached the
// wire.
func famAssertBodyCarriesAlias(t *testing.T, label string, surface famSurface, ingress, later []byte, body []byte, alias string) {
	t.Helper()
	if !famIsStructuredDocument(ingress) {
		famAssertBodyCarriesOpaqueValue(t, label, surface, ingress, later, body, alias)
		return
	}
	expected, ok := famAliasForm(ingress, alias)
	if !ok {
		t.Fatalf("fixture: the ingress %s selected member must be a real-rooted string: family=%s surface=%s",
			surface.label, label, surface.label)
	}
	var ingressFields map[string]json.RawMessage
	if json.Unmarshal(ingress, &ingressFields) != nil {
		t.Fatalf("fixture: the ingress %s document must be one complete JSON value: family=%s surface=%s",
			surface.label, label, surface.label)
	}
	if !famCarriesScalar(body, expected) {
		t.Errorf("requirements.md 5.8 - the adapter must serialize the mutated %s selected member with its alias root prefix and original suffix intact: family=%s surface=%s",
			surface.label, label, surface.label)
		return
	}
	// Every unselected member must have reached the wire with its value intact. The
	// byte-for-byte claim itself is made by the canonical comparison above; this is the
	// independent statement that the ADAPTER did not drop or reshape one on the way out.
	for _, member := range []string{famNonPathMember, famLimitMember} {
		raw, present := ingressFields[member]
		if !present {
			continue
		}
		var scalar string
		if err := json.Unmarshal(raw, &scalar); err != nil {
			continue
		}
		if !famCarriesScalar(body, scalar) {
			t.Errorf("requirements.md 2.8 - the non-selected member of the %s payload must reach the wire with its value intact: family=%s surface=%s",
				surface.label, label, surface.label)
		}
	}
}

// famAssertBodyCarriesOpaqueValue is famAssertBodyCarriesAlias's opaque counterpart: the whole
// selected value, alias-rooted with its original suffix, must be present.
func famAssertBodyCarriesOpaqueValue(t *testing.T, label string, surface famSurface, ingress, later []byte, body []byte, alias string) {
	t.Helper()
	expected, ok := famAliasForm(ingress, alias)
	if !ok {
		t.Fatalf("fixture: the ingress %s selected value must be a real-rooted string: family=%s surface=%s",
			surface.label, label, surface.label)
	}
	if !famCarriesScalar(body, expected) {
		t.Errorf("requirements.md 5.8 - the adapter must serialize the mutated %s selected value with its alias root prefix and original suffix intact: family=%s surface=%s",
			surface.label, label, surface.label)
	}
}

// famAliasForm composes the expected alias-rooted form of a real-rooted fixture value: the
// alias root, the segment separator, and the value's own suffix.
//
// It accepts either a structured document (whose selected member is read) or a bare value,
// so the same composition serves both surface kinds. It is used only to build an expected
// string for a comparison; it never builds a failure message.
func famAliasForm(value []byte, alias string) (string, bool) {
	text := string(value)
	if json.Valid(value) {
		var document map[string]json.RawMessage
		if json.Unmarshal(value, &document) == nil {
			var selected string
			if json.Unmarshal(document[famSelectedMember], &selected) != nil {
				return "", false
			}
			text = selected
		}
	}
	return famAliasFormOfValue(text, alias)
}

// famAliasFormOfValue is famAliasForm's scalar half.
//
// The derived alias already ends in a segment separator and the fixture value's suffix
// begins with one, so exactly one separator is consumed here. Getting that wrong would
// compose an expectation that can never match, which is why this is the only place the
// composition happens.
func famAliasFormOfValue(value, alias string) (string, bool) {
	if !strings.HasPrefix(value, famRoot) {
		return "", false
	}
	suffix := value[len(famRoot):]
	suffix = strings.TrimPrefix(suffix, "/")
	return strings.TrimSuffix(alias, "/") + "/" + suffix, true
}

// famCarriesScalar reports whether an adapter body carries the given string or number
// anywhere, including inside a string that itself spells a document.
func famCarriesScalar(body []byte, value string) bool {
	if value == "" {
		return false
	}
	return slices.Contains(famScalars(body), value)
}

// famAssertToolIdentity proves the adapter preserved every exact canonical tool name and every
// stable tool-call identity requirement 2.8 names, across both authorities.
func famAssertToolIdentity(t *testing.T, label string, decoded aipapi.Call) {
	t.Helper()
	names := 0
	identities := 0
	for _, msg := range decoded.Messages {
		for _, part := range msg.Parts {
			if part.Kind == aipapi.PartJSON && part.ToolCallID != "" {
				identities++
				if part.ToolName == famToolName {
					names++
				}
			}
		}
	}
	for _, item := range decoded.Items {
		if item.ToolCall != nil {
			identities++
			if item.ToolCall.Name == famToolName {
				names++
			}
		}
		if item.ToolResult != nil {
			identities++
			if item.ToolResult.Name == famToolName {
				names++
			}
		}
	}
	if identities == 0 {
		t.Fatalf("fixture: the adapter's decoder must recover at least one stable tool identity: family=%s", label)
	}
	if names != identities {
		t.Errorf("requirements.md 2.8 - the adapter must preserve every canonical tool name: family=%s names_preserved=%d tool_identities=%d",
			label, names, identities)
	}
}

// famAssertRewroteBothSurfaces asserts that the real rewrite actually mutated every surface the
// representative claims, so the adapter assertions below are never measuring an unmutated
// payload.
func famAssertRewroteBothSurfaces(t *testing.T, label string, stats rewrite.Stats, want int) {
	t.Helper()
	if stats.Eligible != want || stats.Rewritten != want {
		t.Fatalf("fixture: the real rewrite must select and virtualize exactly one occurrence per claimed surface: family=%s surfaces=%d eligible=%d rewritten=%d bytes_saved=%d",
			label, want, stats.Eligible, stats.Rewritten, stats.BytesSaved())
	}
	if stats.BytesSaved() <= 0 {
		t.Fatalf("fixture: the real rewrite must report a positive byte saving: family=%s bytes_saved=%d", label, stats.BytesSaved())
	}
}

// famAssertBodyRealRootFree is the whole-body requirement 5.2 oracle for an encoder-only
// family: no byte of the adapter-produced body may carry the authoritative real root.
func famAssertBodyRealRootFree(t *testing.T, label string, body []byte) {
	t.Helper()
	if occurrences := bytes.Count(body, []byte(famRoot)); occurrences != 0 {
		t.Errorf("requirements.md 5.2 and 5.8 - no byte of the adapter-produced request may carry the authoritative real root: family=%s real_root_occurrences=%d",
			label, occurrences)
	}
}

// famAssertWithheldBody is the encoder-only non-vacuity oracle: the same canonical call with the
// rewrite withheld must serialize to a body that still carries the real root and no alias.
func famAssertWithheldBody(t *testing.T, label string, body []byte, alias string) {
	t.Helper()
	if bytes.Contains(body, []byte(alias)) {
		t.Fatalf("fixture: with the rewrite withheld no alias may appear in the %s body, otherwise the pass-through assertion is vacuous: family=%s", label, label)
	}
	if bytes.Count(body, []byte(famRoot)) == 0 {
		t.Fatalf("fixture: with the rewrite withheld the adapter body must still carry the real root, otherwise the whole-body assertion above is vacuous: family=%s", label)
	}
}

// ---------------------------------------------------------------------------
// The family representatives.
// ---------------------------------------------------------------------------

// famOpenResponsesRoundTrip runs the OpenResponses family's own encoder and decoder over one
// canonical call. The decoder's error is reported in full because a codec error here would be a
// fixture failure, not an observed value.
func famOpenResponsesRoundTrip(t *testing.T, call aipapi.Call) ([]byte, aipapi.Call) {
	t.Helper()
	body, err := orproto.EncodeRequest(call)
	if err != nil {
		t.Fatalf("fixture: the OpenResponses family encoder must accept the canonical call: %v", err)
	}
	_, decoded, err := orproto.DecodeRequest(body)
	if err != nil {
		t.Fatalf("requirements.md 5.8 - the OpenResponses family's own decoder must accept the body its encoder produced: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the decoded canonical call must pass validation: %v", err)
	}
	return body, decoded
}

// famOrderedItems applies the canonical legacy -> ordered-items bridge and returns the
// item-authoritative call an item-authoritative backend would encode.
//
// This is a canonical authority bridge, not a protocol pairing: it lives in pkg/aipapi and is
// the projection every item-authoritative backend applies to a message-authority A-leg.
func famOrderedItems(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	items, _, err := aipapi.ProjectLegacyToOrderedItems(call, aipapi.OrderedItemProjectionTarget{})
	if err != nil {
		t.Fatalf("fixture: the canonical legacy -> ordered-items bridge must accept the call: %v", err)
	}
	out := aipapi.CloneCall(call)
	out.Instructions = nil
	out.Messages = nil
	out.Items = items
	if err := out.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the bridged call must stay canonically valid: %v", err)
	}
	return out
}

// famAnthropicBody runs the additional family sentinel's canonical encoder and returns the wire
// body it produced.
//
// The adapter has no exported decode entry point, so the measurement is made on the encoder's
// own serialized output, which is the surface requirement 5.2 is about.
func famAnthropicBody(t *testing.T, call aipapi.Call) ([]byte, error) {
	t.Helper()
	params, err := anmessages.ParamsForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: famModel},
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(params)
}

// TestProtocolFamilies_CarryTheCanonicalMutationUnchanged is requirements.md 2.8, 5.8, 8.6, and
// 8.7.
//
// It drives ONE REPRESENTATIVE PER FAMILY with the feature's REAL outbound rewrite and the
// family's REAL adapter, and asserts the canonical mutation arrives unchanged:
//
//   - openresponses_item_authority: the item-authoritative family's own encoder and decoder
//     over the item-authoritative fixture.
//   - openresponses_via_the_authority_bridge: the SAME family reached from a legacy
//     message-authority history through the canonical legacy -> ordered-items bridge, which
//     is the composition an item-authoritative backend performs on an A-leg that speaks a
//     message-authority dialect. It proves the mutation is not lost at the authority
//     boundary.
//   - anthropic_messages: one additional family sentinel, chosen because it is the most
//     structurally different legacy-chat adapter available (typed tool_use blocks, a tool
//     result folded into a user message). It proves adapter neutrality is a property of the
//     canonical mutation rather than of one codec's shape.
//
// Every representative is measured with AND without the real rewrite, so the positive
// measurement is shown to be the rewrite's doing. Nothing is paired: no frontend is crossed
// with any backend, which is what requirement 8.6 asks for.
func TestProtocolFamilies_CarryTheCanonicalMutationUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("openresponses_item_authority", func(t *testing.T) {
		t.Parallel()

		const label = "openresponses_item_authority"
		ingress := famItemCall()
		rewritten, stats := famRewrite(t, famResolver(t), ingress)
		alias := famAlias(t)
		famAssertRewroteBothSurfaces(t, label, stats, 2)

		rewrittenBody, decoded := famOpenResponsesRoundTrip(t, *rewritten)
		surface, measured := famPickFirstSurface(t, label, *ingress, decoded, famItemSurfaces, alias)
		famAssertSurface(t, label, surface, measured, alias)
		famAssertToolIdentity(t, label, decoded)

		// The withheld measurement: the identical fixture through the identical family
		// adapter, with the rewrite withheld, must still carry the real root.
		_, withheldDecoded := famOpenResponsesRoundTrip(t, *famWithheld(t, ingress))
		_, withheld := famPickFirstSurface(t, label, *ingress, withheldDecoded, famItemSurfaces, alias)
		famAssertWithheld(t, label, surface, withheld, alias)

		// The same comparison against the encoder's own output, so the round trip is not the
		// only evidence: the alias-rooted value must already be in the bytes on the wire.
		if occurrences := bytes.Count(rewrittenBody, []byte(famRoot)); occurrences != 0 {
			t.Errorf("requirements.md 5.2 and 5.8 - no byte of the family-produced request may carry the authoritative real root: family=%s real_root_occurrences=%d",
				label, occurrences)
		}
	})

	t.Run("openresponses_via_the_authority_bridge", func(t *testing.T) {
		t.Parallel()

		const label = "openresponses_via_the_authority_bridge"
		ingress := famLegacyCall()
		// The rewrite runs under the call's OWN authority, before the bridge. That is the
		// runtime order requirements.md 5.2 fixes: virtualization precedes candidate
		// adaptation, and the bridge is part of adaptation.
		rewritten, stats := famRewrite(t, famOpaqueResolver(t), ingress)
		alias := famAlias(t)
		famAssertRewroteBothSurfaces(t, label, stats, 2)

		ingressProjected := famOrderedItems(t, *ingress)
		rewrittenProjected := famOrderedItems(t, *rewritten)
		_, decoded := famOpenResponsesRoundTrip(t, rewrittenProjected)
		surface, measured := famPickFirstSurface(t, label, ingressProjected, decoded, famItemSurfaces, alias)
		famAssertSurface(t, label, surface, measured, alias)
		famAssertToolIdentity(t, label, decoded)

		// The withdrawn-rewrite counterpart goes through the same bridge, so the only
		// variable is the rewrite.
		_, withheldDecoded := famOpenResponsesRoundTrip(t, famOrderedItems(t, *famWithheld(t, ingress)))
		_, withheld := famPickFirstSurface(t, label, ingressProjected, withheldDecoded, famItemSurfaces, alias)
		famAssertWithheld(t, label, surface, withheld, alias)
	})

	t.Run("anthropic_messages", func(t *testing.T) {
		t.Parallel()

		const label = "anthropic_messages"
		ingress := famLegacyCall()
		rewritten, stats := famRewrite(t, famOpaqueResolver(t), ingress)
		alias := famAlias(t)
		famAssertRewroteBothSurfaces(t, label, stats, 2)

		body, err := famAnthropicBody(t, *rewritten)
		if err != nil {
			t.Fatalf("requirements.md 5.8 - the additional family sentinel's encoder must accept the rewritten canonical call: %v", err)
		}
		controlBody, err := famAnthropicBody(t, *famWithheld(t, ingress))
		if err != nil {
			t.Fatalf("fixture: the family encoder must accept the unrewritten canonical call: %v", err)
		}
		famAssertWithheldBody(t, label, controlBody, alias)
		famAssertBodyRealRootFree(t, label, body)

		// Each surface is measured twice: once as the canonical comparison the feature's
		// rewrite produced (what the adapter was handed), and once on the adapter's own
		// serialized form (what actually reached the wire).
		//
		// The adapter puts the structured surface in a typed tool_use input object and the
		// opaque surface in a text block folded into a user message. That choice of envelope
		// is not a change to the payload, which is exactly why the wire measurement recovers
		// the payload from the serialized body rather than expecting a canonical shape there.
		for _, surface := range famLegacySurfaces {
			ingressDoc, ok := surface.document(*ingress)
			if !ok {
				t.Fatalf("fixture: the ingress call must carry the %s surface: family=%s surface=%s",
					surface.label, label, surface.label)
			}
			rewrittenDoc, ok := surface.document(*rewritten)
			if !ok {
				t.Fatalf("fixture: the rewritten call must carry the %s surface: family=%s surface=%s",
					surface.label, label, surface.label)
			}
			famAssertSurface(t, label, surface, famCompare(surface, ingressDoc, rewrittenDoc, alias), alias)
			famAssertBodyCarriesAlias(t, label, surface, ingressDoc, rewrittenDoc, body, alias)
		}
	})
}

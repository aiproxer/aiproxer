package outbound_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// This file is the adversarial sweep for the feature's first outbound pass. Each
// probe states a concrete hostile or degenerate input, states the invariant the
// requirements demand of it, and fails on any leak of a path, alias, workspace tag,
// tool name, or call ID into the observable outcome.
//
// The two invariants every probe shares are:
//
//   - NO PARTIAL REWRITE. Whatever the pass publishes is either the ingress value or
//     the fully published rewrite of it. A probe that produces a call holding both a
//     real path and a virtual alias on the same surface is a failure, and so is one
//     that produces a call that is no longer valid canonical JSON
//     (requirements.md 8.5).
//   - NO LEAK. The published call, the report, the decision, and any returned error
//     never carry content this feature is forbidden to expose: no path suffix, no
//     workspace tag, no tool name, no call ID (requirements.md 7.7).
//
// TestAdversarialOutboundInputs pins the sweep as a table so a reviewer can read the
// full input space in one place rather than reconstructing it from the individual
// probes below.

// adversarialCase is one hostile input plus the verdict it must produce.
type adversarialCase struct {
	name string
	// root is the authoritative project root the pass derives from.
	root string
	// resolver is the compiled policy the pass runs. An absent value means the
	// shipped built-in layer, which is the policy a deployment gets with no
	// operator configuration.
	resolver func(*testing.T) *pathvirtualization.Resolver
	// build produces the candidate the pass is handed.
	build func(t *testing.T) *aipapi.Call
	// wantOutcome is the bounded pass-level verdict.
	wantOutcome outbound.Outcome
	// wantRootReason, when set, is the mapper's bounded refusal code.
	wantRootReason pathvirtualization.SkipReason
	// wantSelected is the exact value the selected /file_path leaf must hold in the
	// published candidate, or the empty string when the probe does not constrain it.
	wantSelected string
	// wantAliasAfter reports whether the published candidate must carry the alias on
	// the selected field.
	wantAliasAfter bool
	// wantRealRootAfter reports whether the published candidate may still name the
	// real root anywhere on a path-bearing surface.
	wantRealRootAfter bool
	// wantSkip, when non-zero, is a rewriter skip reason the pass must have recorded.
	wantSkip rewrite.SkipReason
	// skipCanonicalValidation marks a probe whose ingress is NOT canonically valid.
	// Canonical validation runs before this pass in the real pipeline, so such a
	// probe exists only to reach a state the runtime would otherwise never produce.
	skipCanonicalValidation bool
	// wantArgumentsUnchanged requires the published argument document to be
	// byte-identical to the ingress, which is the strongest available statement for a
	// probe whose shape the rewriter must refuse outright.
	wantArgumentsUnchanged bool
	// wantLegacyAuthorityUntouched requires a call that ALSO carries legacy message
	// parts to leave them byte-identical, proving the pass never walks a second
	// authority over the same candidate.
	wantLegacyAuthorityUntouched bool
}

// adversarialCall builds a canonical candidate over the supplied argument document.
func adversarialCall(arguments string) func(*testing.T) *aipapi.Call {
	return func(t *testing.T) *aipapi.Call {
		t.Helper()
		call := attemptItemCall(arguments)
		if err := call.Validate(); err != nil {
			t.Fatalf("fixture call must be canonical: %v", err)
		}
		return call
	}
}

// adversarialOperatorResolver compiles an operator profile layer that claims one
// exact tool name with two argument pointers, so a payload that resolves only one of
// them produces the selector layer's own unresolved-location refusal.
func adversarialOperatorResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{{
		Names:       []string{attemptTool},
		ArgPointers: []string{"/file_path", "/notebook_path"},
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q", reject)
	}
	return resolver
}

// TestAdversarialOutboundInputs is the table form of the sweep. Every entry is a
// real input a client or a hostile payload can produce.
func TestAdversarialOutboundInputs(t *testing.T) {
	t.Parallel()

	const shortRoot = "/x"

	cases := []adversarialCase{
		{
			name:        "absent_project_root",
			root:        "",
			build:       adversarialCall(attemptPathArguments),
			wantOutcome: outbound.OutcomeProjectRootUnusable,
			// The mapper refuses an absent root, so nothing is examined at all and
			// the whole argument document survives byte for byte.
			wantRootReason:         pathvirtualization.SkipReasonEmptyRoot,
			wantArgumentsUnchanged: true,
		},
		{
			name:           "relative_project_root",
			root:           "workspace/subdir",
			build:          adversarialCall(attemptPathArguments),
			wantOutcome:    outbound.OutcomeProjectRootUnusable,
			wantRootReason: pathvirtualization.SkipReasonRelativeRoot,
			// Relative-path rewriting is explicitly out of scope, and a relative root
			// cannot be prefixed with a relative root either, so the real path stays.
			wantArgumentsUnchanged: true,
		},
		{
			name:           "windows_device_namespace_root",
			root:           `\\.\PIPE\aip`,
			build:          adversarialCall(attemptPathArguments),
			wantOutcome:    outbound.OutcomeProjectRootUnusable,
			wantRootReason: pathvirtualization.SkipReasonDeviceNamespace,
			// The device-namespace root does not prefix the POSIX real path, so no
			// replacement is even possible.
			wantArgumentsUnchanged: true,
		},
		{
			name:           "reserved_namespace_colliding_root",
			root:           `/.__aip_v1__/w_hr7xnivm5at2ku356qya/pkg`,
			build:          adversarialCall(attemptPathArguments),
			wantOutcome:    outbound.OutcomeProjectRootUnusable,
			wantRootReason: pathvirtualization.SkipReasonReservedNamespaceCollision,
			// The colliding root does not prefix the real path either.
			wantArgumentsUnchanged: true,
		},
		{
			name:                   "inactive_mapping_alias_not_shorter",
			root:                   shortRoot,
			build:                  adversarialCall(`{"file_path":"` + shortRoot + `/src/main.go"}`),
			wantOutcome:            outbound.OutcomeRewriterRan,
			wantArgumentsUnchanged: true,
			wantSkip:               rewrite.SkipReasonMappingInactive,
		},
		{
			name:  "already_fully_virtualized_candidate",
			root:  attemptRoot,
			build: adversarialCall(`{"file_path":"` + attemptVirtual + `","limit":10}`),
			// The rewriter publishes the input pointer itself, so the candidate is
			// untouched and measures zero (requirements.md 2.9).
			wantOutcome: outbound.OutcomeRewriterRan,
			// The already-virtual path is what was there; the real root is absent.
			wantAliasAfter: true,
		},
		{
			name: "candidate_with_both_authorities_is_refused_by_canonical_validation",
			root: attemptRoot,
			build: func(t *testing.T) *aipapi.Call {
				t.Helper()
				// The runtime never builds this, because Call.Validate rejects it, and
				// the pass must not become a way to smuggle one past validation.
				call := attemptItemCall(attemptPathArguments)
				call.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("both")}}}
				if err := call.Validate(); err == nil {
					t.Fatal("fixture: a call carrying both authorities must be rejected by canonical validation")
				}
				return call
			},
			wantOutcome:    outbound.OutcomeRewriterRan,
			wantAliasAfter: true,
			// Canonical validation refuses this shape before the pass ever sees one, so
			// the probe exists to prove the pass cannot be used to smuggle one through:
			// the rewriter walks the ITEM authority exactly as its documented
			// discrimination requires, and the legacy message part the call also carries
			// is never inspected. The item argument document therefore IS rewritten,
			// while the extra legacy message authority is left byte-for-byte alone.
			skipCanonicalValidation:      true,
			wantLegacyAuthorityUntouched: true,
		},
		{
			name: "legacy_tool_result_carrying_opaque_text",
			root: attemptRoot,
			build: func(t *testing.T) *aipapi.Call {
				t.Helper()
				call := attemptLegacyCall(attemptPathArguments)
				if err := call.Validate(); err != nil {
					t.Fatalf("fixture call must be canonical: %v", err)
				}
				return call
			},
			wantOutcome: outbound.OutcomeRewriterRan,
			// The opaque result text names the real path and no shipped profile marks
			// that tool path-oriented, so it survives by requirement 2.5 while the
			// selected argument field is aliased.
			wantAliasAfter:    true,
			wantRealRootAfter: true,
		},
		{
			name: "tool_with_no_profile",
			root: attemptRoot,
			build: func(t *testing.T) *aipapi.Call {
				t.Helper()
				// A tool no exact profile claims, carrying the same declared schema, so
				// the built-in inference step is not what refuses it.
				call := attemptItemCall(`{"file_path":"` + attemptTarget + `","limit":10}`)
				call.Tools = []aipapi.ToolDef{{Name: "grep", Parameters: []byte(`{"type":"object"}`)}}
				call.Items[1].ToolCall.Name = "grep"
				if err := call.Validate(); err != nil {
					t.Fatalf("fixture call must be canonical: %v", err)
				}
				return call
			},
			wantOutcome: outbound.OutcomeRewriterRan,
			// Requirement 3.5: nothing is selected, nothing is rewritten.
			wantSelected:      attemptTarget,
			wantRealRootAfter: true,
			wantSkip:          rewrite.SkipReasonNoSelectors,
		},
		{
			name: "invalid_json_argument_payload",
			root: attemptRoot,
			build: func(t *testing.T) *aipapi.Call {
				t.Helper()
				// Canonical validation rejects an invalid payload, so the fixture
				// bypasses it to reach the rewriter's own refusal path.
				return &aipapi.Call{
					Items: []aipapi.Item{
						{
							Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
							ToolCall: &aipapi.ToolCallItem{
								CallID: attemptCallID, Name: attemptTool,
								Arguments: json.RawMessage(`{"file_path": "unterminated`),
							},
						},
					},
				}
			},
			wantOutcome: outbound.OutcomeRewriterRan,
			// The rewriter refuses a payload it cannot read whole and never reads one
			// partially, so the real path survives inside the unread bytes.
			wantRealRootAfter:       true,
			wantSkip:                rewrite.SkipReasonPayloadInvalid,
			skipCanonicalValidation: true,
			wantArgumentsUnchanged:  true,
		},
		{
			name:              "nil_call",
			root:              attemptRoot,
			build:             func(*testing.T) *aipapi.Call { return nil },
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantRealRootAfter: false,
		},
		{
			// An operator profile claims a second pointer the payload does not carry.
			// The selector layer refuses that location and the resolvable one is still
			// rewritten, which is the mixed outcome requirement 3.5 asks for.
			name:              "selected_pointer_names_no_location",
			root:              attemptRoot,
			resolver:          adversarialOperatorResolver,
			build:             adversarialCall(`{"file_path":"` + attemptTarget + `"}`),
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantAliasAfter:    true,
			wantRealRootAfter: true,
			wantSkip:          rewrite.SkipReasonSelectorUnresolved,
		},
		{
			// The whole document is an array. A JSON Pointer can only name a member of
			// an object, so there is no selected location at all.
			name:              "argument_document_root_is_an_array",
			root:              attemptRoot,
			build:             adversarialCall(`["` + attemptTarget + `"]`),
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantRealRootAfter: true,
			wantSkip:          rewrite.SkipReasonPayloadNotObject,
		},
		{
			// The selected leaf is an object. Its nested string is not selected, because
			// descending into arbitrary structure is the recursive rewrite requirement
			// 2.3 forbids.
			name:              "selected_value_is_an_object",
			root:              attemptRoot,
			build:             adversarialCall(`{"file_path":{"nested":"` + attemptTarget + `"}}`),
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantRealRootAfter: true,
			wantSkip:          rewrite.SkipReasonSelectorObject,
		},
		{
			// The selected array holds a non-string, so the WHOLE array is refused
			// rather than partially selected.
			name:              "selected_array_holds_a_non_string",
			root:              attemptRoot,
			build:             adversarialCall(`{"file_path":["` + attemptTarget + `",7]}`),
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantRealRootAfter: true,
			wantSkip:          rewrite.SkipReasonSelectorNotStringArray,
		},
		{
			// A payload-concept sibling names the real root while the selected field
			// already carries the alias. Nothing new may be rewritten, and the sibling
			// keeps its bytes.
			name:              "real_root_appears_only_in_an_unselected_sibling",
			root:              attemptRoot,
			build:             adversarialCall(`{"file_path":"` + attemptVirtual + `","content":"` + attemptTarget + `"}`),
			wantOutcome:       outbound.OutcomeRewriterRan,
			wantAliasAfter:    true,
			wantRealRootAfter: true,
			wantSelected:      attemptVirtual,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bindResolver := attemptResolver
			if tc.resolver != nil {
				bindResolver = tc.resolver
			}
			rec := &attemptRecorder{}
			transform := outbound.NewAttemptTransform(rewrite.ModeRewrite, bindResolver(t), outbound.WithReporter(rec.record))
			call := tc.build(t)
			meta := attemptWorkspace()
			meta.Workspace.ProjectRoot = tc.root
			// The ingress is captured as the raw selected leaf plus the raw argument
			// document, because a deliberately malformed fixture cannot be marshalled
			// and that is exactly the state the byte-identity check needs to compare.
			var ingressArguments []byte
			if call != nil {
				ingressArguments = attemptToolCallDocument(call)
			}

			decision, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{})
			if err != nil {
				t.Fatalf("the pass must never return an error to the stage: %v", err)
			}
			attemptContinue(t, decision)

			report := rec.only(t)
			if report.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %v", report.Outcome, tc.wantOutcome)
			}
			if report.RootReason != tc.wantRootReason {
				t.Fatalf("root reason = %q, want %q", report.RootReason, tc.wantRootReason)
			}
			if tc.wantSkip != rewrite.SkipReasonNone && attemptSkipCount(report.Stats, tc.wantSkip) == 0 {
				t.Fatalf("stats must record the bounded %q reason: %+v", tc.wantSkip, report.Stats)
			}
			// A pass that refused a root before the rewriter ran reports no statistics,
			// because none were earned.
			if tc.wantOutcome == outbound.OutcomeProjectRootUnusable && !reflect.DeepEqual(report.Stats, rewrite.Stats{}) {
				t.Fatalf("a refused root must report no statistics: %+v", report.Stats)
			}
			attemptAdversarialNoLeak(t, report, decision)
			if call == nil {
				if tc.name != "nil_call" {
					t.Fatalf("only the nil-call probe may leave the pass with no call")
				}
				return
			}

			attemptAdversarialNoPartialRewrite(t, call, ingressArguments, tc)
		})
	}
}

// attemptAdversarialNoPartialRewrite proves the published candidate is either the
// ingress value or a fully published rewrite of it, never a mixture, and that no
// surface beyond the selected leaf acquired a replacement it did not already hold.
func attemptAdversarialNoPartialRewrite(t *testing.T, call *aipapi.Call, ingressArguments []byte, tc adversarialCase) {
	t.Helper()

	// A probe whose ingress is deliberately not canonically valid is judged by byte
	// identity of the shape it was refused on, not by validity.
	if tc.wantArgumentsUnchanged {
		if got := attemptToolCallDocument(call); string(got) != string(ingressArguments) {
			t.Fatalf("requirements.md 8.5 - a payload the rewriter cannot read must reach the backend byte-for-byte unchanged")
		}
		return
	}
	if tc.wantLegacyAuthorityUntouched {
		// The dual-authority probe is deliberately not canonically valid, so it
		// cannot be validated as a whole; the item surface must still carry the
		// published alias while the legacy surface stays untouched.
		if tc.wantAliasAfter {
			if selected := attemptSelectedPath(t, call); !strings.Contains(selected, ".__aip_v1__") {
				t.Fatalf("the item authority must be rewritten while the legacy authority is left alone")
			}
		}
		if len(call.Messages) != 1 || len(call.Messages[0].Parts) != 1 {
			t.Fatalf("the pass must not add or remove a legacy message authority")
		}
		if call.Messages[0].Parts[0].Text != "both" {
			t.Fatalf("the pass must not rewrite a legacy message authority on a call that carries the item authority")
		}
		return
	}
	if tc.skipCanonicalValidation {
		t.Fatalf("probe %q declares no published-value expectation", tc.name)
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("requirements.md 8.5 - the published candidate must stay canonically valid: %v", err)
	}

	selected := attemptSelectedPath(t, call)
	carriesAlias := strings.Contains(selected, ".__aip_v1__")
	carriesReal := strings.Contains(selected, attemptRoot)
	if tc.wantAliasAfter && !carriesAlias {
		t.Fatalf("the selected field must carry the reserved V1 alias; got a value that does not")
	}
	if carriesAlias && carriesReal {
		t.Fatalf("requirements.md 8.5 - the selected field must never carry both the real root and an alias")
	}
	if tc.wantSelected != "" && selected != tc.wantSelected {
		t.Fatalf("selected value shape changed unexpectedly; got a value this build does not emit for this input")
	}

	// Every alias in the published candidate must sit inside the selected leaf, and
	// the count must match what that leaf holds: a replacement escaping to an
	// unselected surface would be the recursive rewrite requirement 2.3 forbids.
	encoded := attemptMarshal(t, *call)
	leafAliases := strings.Count(selected, ".__aip_v1__")
	if got := strings.Count(encoded, ".__aip_v1__"); got != leafAliases {
		t.Fatalf("requirements.md 2.3 - %d alias occurrences are published but only %d belong to the selected leaf",
			got, leafAliases)
	}
	if tc.wantRealRootAfter && !strings.Contains(encoded, attemptRoot) {
		t.Fatalf("this probe requires the published candidate to keep its real-root surface")
	}
}

// attemptAdversarialNoLeak proves the observable outcome carries nothing the feature
// is forbidden to expose.
func attemptAdversarialNoLeak(t *testing.T, report outbound.Report, decision request.AttemptDecision) {
	t.Helper()

	encoded, err := json.Marshal(struct {
		Report   outbound.Report `json:"report"`
		Decision any             `json:"decision"`
	}{Report: report, Decision: decision})
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		attemptRoot, attemptAlias, attemptTarget, attemptVirtual, attemptSuffix,
		attemptTool, attemptCallID, "hr7xnivm5at2ku356qya",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("requirements.md 7.7 - the pass leaked content-bearing text into its observable outcome")
		}
	}
}

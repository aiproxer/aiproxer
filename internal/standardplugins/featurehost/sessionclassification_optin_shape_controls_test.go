package featurehost

// Task 10.3 non-vacuity controls for the structural half of requirement 1.6.
//
// The guards in sessionclassification_optin_decision_shape_test.go are allowlists
// over a source file. An allowlist can be satisfied by a checker that accepts
// everything, and it can be weakened one name at a time, so each control below
// feeds the SAME checks a synthetic consumer that commits exactly one violation
// and requires that violation to be reported.
//
//   - greedy_from_request: decides by re-deriving the fact from the request's
//     User-Agent and transcript instead of the snapshot. It must fail on the
//     decision signature, on the decision selectors, and on the handler
//     selectors.
//   - greedy_from_call_sitesite: keeps a snapshot-only decision but calls it with
//     a local variable instead of the projection, so a handler could compute that
//     local from the request. It must fail the call-argument check while passing
//     everything else, which is what proves that check is not vacuous.
//   - greedy_importing_contract: keeps a snapshot-only decision and an identical
//     handler, but imports the SDK classifier contract. It must fail only the
//     import check, which proves the import allowlist is doing work.
//   - unrelated_same_named_field: a consumer of a struct whose own member is also
//     called Classification. It must NOT be rejected by the import check, which
//     proves the import rule is fragment-scoped rather than a blanket ban.
//
// Each control is checked against the real allowlists, not against a simplified
// restatement of them.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// optInGreedyFromRequestSource is the first mutation the structural guard must
// reject: the same consumer, deciding by re-deriving the fact from the request.
const optInGreedyFromRequestSource = `package featurehost

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func greedyCodingAgentDecision(call *aipapi.Call, snapshot session.Classification) bool {
	if snapshot.IsCodingAgent() {
		return true
	}
	return call.Invocation.ClientUserAgent != "" || len(call.Messages) > 0
}

type greedyFromRequestGate struct{}

func (greedyFromRequestGate) ID() string                        { return "greedy-request" }
func (greedyFromRequestGate) Order() int                        { return 0 }
func (greedyFromRequestGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (greedyFromRequestGate) Handle(_ context.Context, call *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	snapshot := meta.Session.Classification
	gated := greedyCodingAgentDecision(call, snapshot)
	if gated {
		return prerequest.Deny("greedy"), nil
	}
	return prerequest.Allow(), nil
}
`

// optInGreedyCallSiteSource keeps the decision snapshot-only but hands it a local
// instead of the projection, so a handler could compute that local from anything.
const optInGreedyCallSiteSource = `package featurehost

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func callSiteDecision(snapshot session.Classification) bool {
	return snapshot.IsCodingAgent()
}

type callSiteGate struct{}

func (callSiteGate) ID() string                        { return "call-site" }
func (callSiteGate) Order() int                        { return 0 }
func (callSiteGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (callSiteGate) Handle(_ context.Context, _ *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	local := meta.Session.Classification
	if callSiteDecision(local) {
		return prerequest.Deny("call-site"), nil
	}
	return prerequest.Allow(), nil
}
`

// optInContractImportingSource keeps the decision snapshot-only and the handler
// selector set identical to the real consumer, but imports the SDK classifier
// contract so it could obtain a second decision for the same turn.
const optInContractImportingSource = `package featurehost

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

func contractDecision(snapshot session.Classification) bool {
	return snapshot.IsCodingAgent()
}

type contractGate struct{}

func (contractGate) ID() string                        { return "contract" }
func (contractGate) Order() int                        { return 0 }
func (contractGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (contractGate) Handle(_ context.Context, _ *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	var input sdkclassification.Input
	_ = input
	snapshot := meta.Session.Classification
	if contractDecision(snapshot) {
		return prerequest.Deny("contract"), nil
	}
	return prerequest.Allow(), nil
}
`

// optInUnrelatedFieldSource is a consumer of a struct whose own member is also
// called Classification. The import rule must not fire on it, which proves the
// rule is fragment-scoped.
const optInUnrelatedFieldSource = `package featurehost

import "github.com/aiproxer/aiproxer/pkg/aipapi"

type unrelatedCandidate struct {
	Name           string
	Classification int
}

func unrelatedToolConsumer(c unrelatedCandidate) int {
	return c.Classification
}
`

// TestOptInStructuralGuardRejectsConsumersThatReDeriveTheDecision is the
// non-vacuity control for the structural half of requirement 1.6.
func TestOptInStructuralGuardRejectsConsumersThatReDeriveTheDecision(t *testing.T) {
	t.Parallel()

	t.Run("re-deriving the decision from the request is rejected", func(t *testing.T) {
		t.Parallel()
		file := optInParseControl(t, optInGreedyFromRequestSource)

		params, _, ok := optInSignatureOf(file, "greedyCodingAgentDecision")
		if !ok {
			t.Fatal("the greedy control declares no decision function")
		}
		if len(params) == 1 {
			t.Errorf("greedy decision parameters = %v; the multi-input signature was not rejected", params)
		}
		selectors := optInSelectorNamesIn(t, file, "greedyCodingAgentDecision")
		if slices.Equal(selectors, optInDecisionAllowedSelectors) {
			t.Errorf("greedy decision selectors = %v, which equals the allowlist; the selector check is "+
				"vacuously satisfied", selectors)
		}
		for _, forbidden := range []string{"ClientUserAgent", "Messages"} {
			if !slices.Contains(selectors, forbidden) {
				t.Errorf("greedy decision selectors %v do not include %q, so this control no longer models "+
					"a consumer that re-derives the fact from the request", selectors, forbidden)
			}
		}
		handler := optInSelectorNamesIn(t, file, optInHandleMethod)
		if slices.Equal(handler, optInHandlerAllowedSelectors) {
			t.Errorf("greedy handler selectors = %v, which equals the allowlist; the handler check is "+
				"vacuously satisfied", handler)
		}
		if rejected := optInRejectedImports(file); len(rejected) != 0 {
			t.Errorf("the greedy control unexpectedly violates the import rule with %v; it is meant to "+
				"isolate the selector and signature checks", rejected)
		}
		t.Logf("rejected: decision params=%v selectors=%v handler selectors=%v", params, selectors, handler)
	})

	t.Run("calling the decision with a local instead of the projection is rejected", func(t *testing.T) {
		t.Parallel()
		file := optInParseControl(t, optInGreedyCallSiteSource)

		params, _, ok := optInSignatureOf(file, "callSiteDecision")
		if !ok {
			t.Fatal("the call-site control declares no decision function")
		}
		if want := []string{"Classification"}; !slices.Equal(params, want) {
			t.Fatalf("call-site decision parameters = %v, want %v; this control must isolate the call-argument "+
				"violation", params, want)
		}
		shape, ok := optInDecisionArgumentShape(file, "callSiteDecision")
		if !ok {
			t.Fatal("the call-site control never calls its decision function")
		}
		if shape == "meta.Session.Classification" {
			t.Errorf("the call-argument check accepted %q; a handler could have computed that local from "+
				"the request instead of handing the decision the projection", shape)
		}
		t.Logf("rejected: call argument %q instead of meta.Session.Classification", shape)
	})

	t.Run("importing the classifier contract is rejected", func(t *testing.T) {
		t.Parallel()
		file := optInParseControl(t, optInContractImportingSource)

		// The control must be otherwise clean, so the rejection is attributable to
		// the import rule alone.
		params, _, _ := optInSignatureOf(file, "contractDecision")
		if want := []string{"Classification"}; !slices.Equal(params, want) {
			t.Fatalf("contract decision parameters = %v, want %v; this control must isolate the import "+
				"violation", params, want)
		}
		if selectors := optInSelectorNamesIn(t, file, "contractDecision"); !slices.Equal(
			selectors, optInDecisionAllowedSelectors) {
			t.Fatalf("contract decision selectors = %v, want %v; this control must isolate the import "+
				"violation", selectors, optInDecisionAllowedSelectors)
		}
		rejected := optInRejectedImports(file)
		if len(rejected) == 0 {
			t.Errorf("imports %v were accepted; a consumer that imports the classifier contract could obtain "+
				"a second, independent decision for the same turn (requirement 1.6)", optInImportsOf(file))
		}
		t.Logf("rejected: contract imports %v", rejected)
	})

	t.Run("an unrelated same-named field is not an import violation", func(t *testing.T) {
		t.Parallel()
		file := optInParseControl(t, optInUnrelatedFieldSource)
		if rejected := optInRejectedImports(file); len(rejected) != 0 {
			t.Errorf("imports %v were rejected for the unrelated-field control; the import rule must be "+
				"fragment-scoped, not a blanket ban", rejected)
		}
		t.Logf("unrelated-field consumer accepted: imports %v", optInImportsOf(file))
	})
}

func optInParseControl(t *testing.T, source string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "control.go", source, 0)
	if err != nil {
		t.Fatalf("parse the structural control source: %v", err)
	}
	return file
}

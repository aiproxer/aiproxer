package expansion_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 8.1, design.md "Testing Strategy"
// ("Fuzz parser/mutator for panic freedom, valid JSON, and no unmatched
// reserved-alias expansion") applied to the inbound direction.
//
// The invariant stated here is the whole safety property of the pass, expressed over
// arbitrary model output rather than over hand-picked fixtures: whatever bytes arrive,
// this pass NEVER hands the client an argument document that still carries a
// well-formed alias of the CURRENT workspace at any position. Either the alias was
// replaced, or the call was refused, or the alias belongs to a different workspace and
// is therefore a refusal too.
//
// Two invariants are deliberately NOT asserted, because the pass's own documentation
// makes them false and a test that hid them would be a test that lied:
//
//   - a document that reads no alias of the current workspace is released unchanged,
//     including one whose content field QUOTES a reserved alias (requirements.md 4.7);
//   - a tool no layer claims is not inspected at all, so an alias in its arguments is
//     released unchanged (requirements.md 3.5, 4.7).
//
// The seed corpus carries the shapes this feature is actually about, so the interesting
// regions are exercised without waiting for the fuzzer to invent them.
func FuzzExpansionFinalizeNeverReleasesACurrentWorkspaceAlias(f *testing.F) {
	// The fixture helpers take a testing.TB, so the fuzz target derives from the SAME
	// roots the assertions elsewhere in this package use rather than duplicating the
	// derivation and drifting from it.
	fixture := newExpansionFixture(f)
	resolver := expansionResolver(f, expansionToolName, "/path", "/paths", "/file_path")
	fin, err := expansion.NewFinalizer(resolver, rewrite.ModeRewrite, expansion.Policy{})
	if err != nil {
		f.Fatalf("NewFinalizer: %v", err)
	}
	tool := aipapi.ToolDef{Name: expansionToolName}
	ctx := f.Context()

	// currentAlias is the one token that must never survive in a published document.
	currentAlias := fixture.virtualRoot

	for _, seed := range []string{
		`{"path":"` + fixture.aliasPath + `"}`,
		`{"path":"` + fixture.aliasPath + `","content":"kept"}`,
		`{"paths":["` + fixture.aliasPath + `",null,"relative"]}`,
		`{"path":"/.__aip_v1__/w_abcdefghijklmnopqrst/src/main.go"}`,
		`{"path":"/.__aip_v1__/w_short/src/main.go"}`,
		`{"file_path":"` + currentAlias + `"}`,
		`{"path":"` + fixture.aliasPath,
		`{"path":"/home/dev/other/src/main.go"}`,
		`{}`,
		`[]`,
		`null`,
		``,
		`{"path":1e400,"paths":[` + currentAlias + `]}`,
		`{"path":"` + currentAlias + strings.Repeat("/a", 64) + `"}`,
		`{"dup":"` + currentAlias + `","dup":"/home/dev/x"}`,
		`{"path":"` + currentAlias[:len(currentAlias)-1] + `"}`,
		`{"path":"` + currentAlias + `\..\..\etc\passwd"}`,
	} {
		f.Add(seed, expansionProjectRoot)
	}

	f.Fuzz(func(t *testing.T, args, projectRoot string) {
		res, ferr := fin.Finalize(ctx, expansionCall(expansionToolName, args), tool, nil,
			expansionMeta(projectRoot))
		if ferr != nil {
			// The pass reports decisions, never Go errors: a Go error makes the
			// assembler replay the ORIGINAL fragments, which is precisely the
			// requirements.md 4.4 release this pass exists to prevent.
			t.Fatalf("Finalize surfaced a Go error, which replays the originals: %v", ferr)
		}
		if _, ok := expansion.ParseReason(res.ReasonCode); !ok {
			t.Fatalf("reason code %q is outside the closed vocabulary", res.ReasonCode)
		}
		switch res.Action {
		case toolcall.ActionPass:
			if res.ArgsJSON != nil {
				t.Fatalf("a pass must not carry a mutated document: %q", res.ArgsJSON)
			}
			return
		case toolcall.ActionReject:
			if res.ArgsJSON != nil {
				t.Fatalf("a refusal must not carry a document: %q", res.ArgsJSON)
			}
			return
		case toolcall.ActionRewrite:
		default:
			t.Fatalf("action %d is outside the three the assembler can use", int(res.Action))
		}

		// requirements.md 8.5 and design.md section 7 step 9: whatever is published
		// must still be exactly one complete JSON value.
		if !json.Valid(res.ArgsJSON) {
			t.Fatalf("published arguments are not valid JSON: %q", res.ArgsJSON)
		}
		if res.ToolName != expansionToolName {
			t.Fatalf("published tool name %q changed the call's own name", res.ToolName)
		}
		// THE invariant. The current workspace's own alias must be gone from every
		// byte of the published document, including from an unselected member: a
		// rewrite that expanded the selected leaf but left a second copy elsewhere
		// would still hand the client a live alias.
		if strings.Contains(string(res.ArgsJSON), currentAlias) {
			t.Fatalf("the current workspace alias survived a published expansion: %q", res.ArgsJSON)
		}
		// And the reserved namespace must be gone entirely for this mapping: an
		// alias carrying this mapping's own tag cannot survive anywhere, and any
		// OTHER tag would have been refused rather than published.
		if pathvirtualization.ScanReservedAlias(res.ArgsJSON) != pathvirtualization.ReservedAliasAbsent {
			t.Fatalf("a reserved alias survived a published expansion: %q", res.ArgsJSON)
		}
	})
}

// TestExpansionFinalizerExpandsADocumentPastItsOwnDeclaredBound proves that the
// declared bound is a DECLARATION the assembler enforces, not a rule this pass
// enforces itself: a document the assembler could still hand over is expanded, and
// requirement 4.5's refusal belongs to the assembler's chokepoint rather than to
// this pass's own arithmetic. This pass's own contribution to the overflow case is
// therefore exactly nothing, which is what the test asserts by observing that no
// bound is enforced here.
func TestExpansionFinalizerExpandsADocumentPastItsOwnDeclaredBound(t *testing.T) {
	t.Parallel()

	fixture := newExpansionFixture(t)
	fin, err := expansion.NewFinalizer(expansionResolver(t, expansionToolName, "/path"),
		rewrite.ModeRewrite, expansion.Policy{MandatoryMaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	args := `{"path":"` + fixture.aliasPath + `","content":"` +
		strings.Repeat("x", toolcall.MinMandatoryMaxArgsBytes+1) + `"}`
	if len(args) <= toolcall.MinMandatoryMaxArgsBytes {
		t.Fatalf("fixture must exceed the declared bound: %d", len(args))
	}

	// Handed such a document directly, the pass expands it: the bound is a DECLARATION
	// the assembler enforces, and inventing a second, stricter check here would make
	// requirement 4.5's failure mode depend on which chokepoint ran first.
	res, ferr := fin.Finalize(t.Context(), expansionCall(expansionToolName, args),
		aipapi.ToolDef{Name: expansionToolName}, nil, expansionMeta(expansionProjectRoot))
	if ferr != nil {
		t.Fatalf("Finalize surfaced a Go error: %v", ferr)
	}
	if res.Action != toolcall.ActionRewrite {
		t.Fatalf("action=%v want rewrite (reason %q)", res.Action, res.ReasonCode)
	}
	if strings.Contains(string(res.ArgsJSON), ".__aip_v1__") {
		t.Fatalf("the reserved alias survived: %d bytes", len(res.ArgsJSON))
	}
}

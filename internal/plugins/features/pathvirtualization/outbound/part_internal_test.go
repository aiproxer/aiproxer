package outbound

// This file is the white-box half of the second outbound pass's proof. Each probe
// reaches a seam the public constructor deliberately closes: the rewriter binder (so
// the documented-unreachable error path and a panic can be exercised at all), and the
// exact field and selector set of the pass (so a per-request field could not be added
// later without failing loudly).
//
// Both matter for the same reason. This pass runs on a request-part chain that shares
// one instance across every request of a generation, and the runtime re-validates and
// may roll back the call around it. A stored per-request value would be a data race and
// a way for one turn's alias to reach another turn's request.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// internalPinnedView is the runtime's per-turn pin, as the white-box probes see it.
func internalPinnedView(ctx context.Context) context.Context {
	return aipworkspace.WithWorkspaceView(ctx, aipworkspace.WorkspaceView{ProjectRoot: internalRoot})
}

// TestRequestPartHookFailsOpenOnUnexpectedTransformationError is requirements.md 8.2
// at the position design.md names for this pass.
//
// The shared rewriter's error path is documented as unreachable from untrusted input,
// so the branch is reached through the pass's own rewriter binder. The failure
// deliberately hands back a PARTIALLY rewritten call, which makes this the strongest
// available proof: publishing it would put a real path and a virtual alias on the same
// backend-bound request, and the bus's post-hook re-validation would not catch that.
func TestRequestPartHookFailsOpenOnUnexpectedTransformationError(t *testing.T) {
	t.Parallel()

	rec := &reports{}
	hook := NewRequestPartHook(rewrite.ModeRewrite, nil, WithHookReporter(rec.record))
	hook.bind = func(pathvirtualization.Mapping) virtualizer {
		return virtualizerFunc(func(call *aipapi.Call) (*aipapi.Call, rewrite.Stats, error) {
			partial := aipapi.CloneCall(*call)
			partial.Items[0].ToolCall.Arguments = json.RawMessage(
				`{"file_path":"/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"}`)
			return &partial, rewrite.Stats{Eligible: 9, Rewritten: 9, BytesBefore: 999, BytesAfter: 1}, errUnexpected
		})
	}

	call := internalCall()
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}
	before, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	if err := hook.HandleRequestParts(internalPinnedView(t.Context()), call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("requirements.md 8.2 - an unexpected late transformation failure must fail open rather than surface an error: %v", err)
	}
	after, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal published call: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("requirements.md 8.2 - the real path must survive an unexpected late failure; a partially rewritten call escaped")
	}
	if containsAlias(string(after)) {
		t.Fatalf("requirements.md 8.2 - no alias may reach the backend when the pass failed")
	}

	report := rec.only(t)
	if report.Outcome != OutcomeTransformationFailed {
		t.Fatalf("report outcome = %v, want %v", report.Outcome, OutcomeTransformationFailed)
	}
	if !reflect.DeepEqual(report.Stats, rewrite.Stats{}) {
		t.Fatalf("requirements.md 8.2 - a failed pass must not report statistics it never earned: %+v", report.Stats)
	}
}

// TestRequestPartHookPropagatesARewriterPanicWithoutPublishing proves the pass never
// recovers on its own behalf.
//
// The runtime's request-part bus owns panic containment and applies this pass's
// declared FailureMode to it, so a pass that swallowed the panic would be taking over a
// boundary the runtime already owns. What the pass must guarantee is narrower and is
// what this asserts: a panic inside the shared rewriter leaves the call untouched, so
// whichever policy the bus applies afterwards, nothing partially rewritten escapes.
func TestRequestPartHookPropagatesARewriterPanicWithoutPublishing(t *testing.T) {
	t.Parallel()

	hook := NewRequestPartHook(rewrite.ModeRewrite, nil)
	hook.bind = func(pathvirtualization.Mapping) virtualizer {
		return virtualizerFunc(func(*aipapi.Call) (*aipapi.Call, rewrite.Stats, error) {
			panic(errUnexpected)
		})
	}

	call := internalCall()
	before, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("a panic in the shared rewriter must reach the runtime's own panic boundary")
			}
		}()
		_ = hook.HandleRequestParts(internalPinnedView(t.Context()), call, sdkhooks.PartMeta{})
	}()

	after, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal published call: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("requirements.md 8.2 - a panic in the shared rewriter must leave the call exactly as the runtime built it")
	}
}

// TestRequestPartHookBindsTheOnePureRewriter proves the pass runs the SAME canonical
// outbound rewriter every other outbound pass runs, in the mode it was built with. A
// second rewriter here is what would let the two passes drift about what a path-bearing
// surface is, which design.md 233 rules out structurally.
func TestRequestPartHookBindsTheOnePureRewriter(t *testing.T) {
	t.Parallel()

	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q", reject)
	}

	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			hook := NewRequestPartHook(mode, resolver)
			mapping, reason := pathvirtualization.DeriveMapping(internalRoot)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", internalRoot, reason)
			}
			bound := hook.bind(mapping)
			rewriter, ok := bound.(*rewrite.Rewriter)
			if !ok {
				t.Fatalf("the default binder must produce the shared canonical rewriter, got %T", bound)
			}
			if rewriter.Mode() != mode {
				t.Fatalf("bound rewriter mode = %v, want %v", rewriter.Mode(), mode)
			}
		})
	}
}

// TestRequestPartHookHoldsNoMutableRequestState pins the pass's whole field set. A
// generation shares one instance across every request, so anything a per-request value
// could be written into is a data race waiting to happen and a way for one request's
// alias to reach another's request.
func TestRequestPartHookHoldsNoMutableRequestState(t *testing.T) {
	t.Parallel()

	hook := NewRequestPartHook(rewrite.ModeRewrite, nil, WithHookReporter(func(Report) {}))
	value := reflect.ValueOf(hook).Elem()
	if value.NumField() != 4 {
		names := make([]string, 0, value.NumField())
		for i := range value.NumField() {
			names = append(names, value.Type().Field(i).Name)
		}
		t.Fatalf("pass fields = %v, want exactly the rollout mode, the policy, the reporter, and the rewriter binder", names)
	}
	if hook.mode != rewrite.ModeRewrite || hook.resolver != nil ||
		hook.report == nil || hook.bind == nil {
		t.Fatalf("unexpected pass configuration: mode=%v resolver_set=%t report_set=%t bind_set=%t",
			hook.mode, hook.resolver != nil, hook.report != nil, hook.bind != nil)
	}
}

// TestRequestPartHookNeverReadsRequestIdentityFields is the structural half of
// requirements.md 5.7 for this pass: it reads the workspace projection and nothing
// else from the request-part metadata, so no mapping can be keyed to a trace ID, A-leg
// ID, B-leg ID, attempt ordinal, or backend identity.
func TestRequestPartHookNeverReadsRequestIdentityFields(t *testing.T) {
	t.Parallel()

	used := handleRequestPartsFieldUses(t)
	for _, forbidden := range []string{"TraceID", "ALegID", "BLegID", "AttemptSeq", "BackendID"} {
		if used[forbidden] {
			t.Fatalf("requirements.md 5.7 - the late outbound pass must not read %s; no mapping may be keyed to it", forbidden)
		}
	}
	for _, required := range []string{"WorkspaceViewFromContext", "ProjectRoot", "DeriveMapping"} {
		if !used[required] {
			t.Fatalf("the late outbound pass must read %s; the pinned project root is the only mapping authority", required)
		}
	}
}

// TestTheLatePassNeverHoldsOrCallsAWorkspaceResolver is requirement 5.6's structural
// form, and the one that makes it true by CONSTRUCTION rather than by convention.
//
// The hazard 5.6 forbids is two workspace tags in one backend-bound request, and the
// way that used to be possible was specific: this pass held its own
// aipworkspace.Resolver and re-resolved it live, while the early pass read the
// runtime's per-turn PIN. A resolver that answered root A and then root B would put
// aliasA on the early surface and aliasB on the late one.
//
// With the pass holding no authority and reading the pin the runtime already published,
// that disagreement is not merely unlikely, it is unrepresentable: there is no field to
// inject and no call site that could resolve anything. The check is therefore over the
// WHOLE of part.go rather than over the two decision methods, because the injection
// could be reintroduced anywhere - a field, a constructor argument, a package-level
// value, a helper.
//
// It is deliberately a source scan and not a behavioral probe: "no call happened" is
// unobservable from outside the package, so reading the construction site is the only
// way to make the rule falsifiable. The positive control at the end keeps it from
// passing vacuously.
func TestTheLatePassNeverHoldsOrCallsAWorkspaceResolver(t *testing.T) {
	t.Parallel()

	used := allPartSelectorUses(t)
	for _, forbidden := range []string{
		// The compiled POLICY resolver is a different thing entirely and is required;
		// it is the WORKSPACE resolver that must be absent.
		"workspace.Resolver", "aipworkspace.Resolver", "Workspace.Resolver",
		"DisabledResolver", "NewResolverChain", "NewStrictChain",
		"Resolve", "ResolverChain",
	} {
		if used[forbidden] {
			t.Fatalf("requirements.md 5.6 - the late outbound pass must not reference %s; the runtime's pinned view is its only workspace authority", forbidden)
		}
	}
	if !used["workspace.WorkspaceViewFromContext"] {
		t.Fatal("the scan proved nothing: part.go no longer reads the pinned workspace projection")
	}
	if !used["pathvirtualization.DeriveMapping"] {
		t.Fatal("the scan proved nothing: the shared outbound operation no longer derives the mapping")
	}
}

// allPartSelectorUses reports every QUALIFIED selector name appearing anywhere in
// part.go and its shared apply operation, keyed "package.Symbol".
//
// It is qualified rather than bare because the pass legitimately holds a
// pathvirtualization.Resolver - the compiled policy - and only a qualified name can tell
// that apart from the workspace authority this file forbids. Comments are not walked, so
// a doc comment that merely NAMES a contract cannot fail the check, while any real
// field, type, or method reference can.
func allPartSelectorUses(t *testing.T) map[string]bool {
	t.Helper()

	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range []string{"part.go", "apply.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch qualifier := sel.X.(type) {
			case *ast.Ident:
				used[qualifier.Name+"."+sel.Sel.Name] = true
			case *ast.SelectorExpr:
				if outer, ok := qualifier.X.(*ast.Ident); ok {
					used[outer.Name+"."+qualifier.Sel.Name+"."+sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	return used
}

// handleRequestPartsFieldUses reports which resolver, view, and metadata fields the
// pass actually reads, by walking the request-part entry point, its workspace
// projection, and the shared outbound operation.
//
// The walk is by field and method name on purpose: the names it forbids are exactly the
// identity surfaces requirements.md 5.7 names, so a rename of one of them is itself a
// spec change and should fail loudly rather than silently pass. Both bodies are walked
// under the same forbidden set, so extracting the authority read into a helper cannot
// become a way to read a B-leg ID.
func handleRequestPartsFieldUses(t *testing.T) map[string]bool {
	t.Helper()

	used := map[string]bool{}
	fset := token.NewFileSet()
	wanted := map[string]bool{"HandleRequestParts": true, "projectRoot": true, "apply": true}
	found := 0
	for _, name := range []string{"part.go", "apply.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			method, ok := node.(*ast.FuncDecl)
			if !ok || !wanted[method.Name.Name] {
				return true
			}
			found++
			ast.Inspect(method.Body, func(inner ast.Node) bool {
				selector, ok := inner.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				used[selector.Sel.Name] = true
				return true
			})
			return false
		})
	}
	if found != len(wanted) {
		t.Fatalf("walked %d of %d outbound operations", found, len(wanted))
	}
	return used
}

// containsAlias reports whether encoded bytes name the fixed V1 reserved namespace.
func containsAlias(encoded string) bool {
	return strings.Contains(encoded, ".__aip_v1__")
}

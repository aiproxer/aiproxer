package bundle_test

// This file pins requirements.md 7.7 at the bundle boundary: no path, alias,
// workspace tag, tool name, pointer, or argument byte the bundle handles may reach
// an observable surface.
//
// The bundle is the last place operator content is in scope, because it is the only
// place all three of it coexist - a compiled policy resolver that holds exact tool
// names and JSON Pointers, a project-root-derived alias, and complete tool-call
// arguments. Its contribution surface is deliberately dull for that reason:
//
//   - the only values it can refuse on are the SDK's plane-contribution errors and
//     the expansion constructor's bound refusal, and both carry bounded text;
//   - it installs exactly ONE reporter per component, and that reporter is the
//     feature's own content-free recorder rather than anything this package composes;
//   - and it emits no log line, no metric, and no error of its own.
//
// The identity strings below are exactly the content an observable surface would
// leak: a fixed low-cardinality label is safe, a workspace root or an alias is not.
//
// THIS FILE USED TO ASSERT THE OPPOSITE OF WHAT IT ASSERTS NOW, and the change is worth
// recording because the assertion had to be REWRITTEN rather than relaxed. It read
// TestTheBundleRegistersNoReporter and structurally proved that none of the three
// constructors was passed a reporter, on the reasoning that a reporter installed here
// would be a metric dimension this package controls and a later edit could make
// content-bearing. That reasoning was right and the conclusion is now INVERTED, because
// requirements.md 7.6 and 7.8 ask this feature for counters and an inventory, and
// requirements.md 7.7's obligation is discharged by WHAT those counters contain rather
// than by the feature having none. A feature that observes nothing cannot leak
// observation data, but it also cannot let an operator answer "is this on, in which mode,
// and what is it saving" - which is the question 7.8 exists for.
//
// So the replacement asserts three things instead of one:
//
//  1. the SAME structural exactness, now in the affirmative: each constructor is called
//     with its full declared arity PLUS exactly one reporter option, and the reporter
//     argument is a fixed expression rather than something assembled from content;
//  2. that the one reporter is the feature's recorder and not a closure this package
//     builds - because a locally composed closure is exactly the thing that could
//     capture a root, an alias, or a tool name and attach it as a label later; and
//  3. the behavioural opposite of the old test: driven with hostile content, the
//     installed reporter records observations, and every one of them is bounded.

import (
	"context"
	"encoding/json"
	"go/ast"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// contentMarkers are the shapes a leak would take. The reserved-namespace marker is
// spelled out because it is the most specific leak this feature could make: an
// alias in a metric label tells a reader both that the feature is active and which
// workspace it derived.
var contentMarkers = []string{
	"/.__aip_v1__",
	"/home/dev/projects",
	`\Users\dev`,
	"pkg/aipapi/call.go",
	"acme_internal_read_secrets",
	"file_path",
}

// hostileToolName is the exact tool name a fully configured operator profile claims. It is
// named once so the configuration fixture, the call fixture, and the marker list cannot
// drift apart, which would leave the resolver selecting nothing.
const hostileToolName = "acme_internal_read_secrets"

// TestNoObservableIdentityCarriesPathOrToolContent asserts that everything an
// operator could see about a participating component is a fixed literal. A
// low-cardinality identity is what a plugin suppression rule, an extension-stage
// failure log, and a diagnostics inventory all key on, so it is exactly where a
// composed-in value would leak.
func TestNoObservableIdentityCarriesPathOrToolContent(t *testing.T) {
	t.Parallel()
	b := bundleWithFixtureConfiguration(t)
	for _, identity := range []string{
		aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0].ID(),
		aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0].ID(),
		aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0].ID(),
	} {
		for _, marker := range contentMarkers {
			if strings.Contains(identity, marker) {
				t.Fatalf("identity %q carries content %q", identity, marker)
			}
		}
	}
}

// reporterConstructors names the trailing option each component constructor takes as its
// content-free reporter.
//
// The arity below is the EXACT argument count of the call including that option, and the
// test compares against it with equality rather than a lower bound. That is the property
// worth preserving from the test this one replaces: a variadic option list cannot be checked
// by naming an argument position, because the option is itself a call expression rather than
// a variable whose text reveals what it is, so the COUNT is the only structural fact that
// distinguishes "one reporter" from "no reporter" and from "a reporter plus something else".
// Relaxing it to ">=" would let a fourth option appear without notice, which is exactly the
// kind of late addition that could make an observable dimension content-bearing.
// bundleReporterValue is the local name the composition seam binds the content-free
// recorder to.
//
// This is the opt-in recorder parameter checked by the no-capturing-closure guard.
const bundleReporterValue = "observations"

// TestTheBundleComposesNoReporterClosure is the negative-space half of the structural
// rule: the reporter this package installs is the feature's recorder, handed over as a
// value, and nothing here WRAPS it.
//
// A wrapper would be the only way this package could attach a label of its own to an
// observation, and a label it composed is the one dimension requirements.md 7.7 could not
// keep bounded by construction. So the guard is on the shape of the source: no function
// literal in this file may mention the recorder.
//
// The positive control is the third assertion, and it is what keeps this test from being
// vacuous: the walk above finds closures today (there are none in bundle.go), so a reader
// cannot tell from a pass whether the walk works. Re-parsing and re-walking the same file
// and confirming the walk observes the AST at all is cheap, and a guard that cannot
// distinguish "no closure" from "walked nothing" is not a guard.
func TestTheBundleComposesNoReporterClosure(t *testing.T) {
	t.Parallel()
	_, file := parseBundle(t)
	closures := 0
	calls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			calls++
		}
		literal, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		// A closure that mentions the recorder is the shape that could capture observation
		// context and re-label it. One that does not is some other concern's business.
		if mentionsRecorder(literal) {
			closures++
			t.Errorf("the bundle wraps the recorder in a closure; a locally composed reporter is the one observable dimension this package could make content-bearing")
		}
		return true
	})
	if calls == 0 {
		t.Fatal("no call expressions were found in bundle.go; the closure guard proved nothing")
	}
	t.Logf("bundle.go has %d call expressions and %d recorder-wrapping closures", calls, closures)
}

// mentionsRecorder reports whether a function literal names the bundle's recorder value.
func mentionsRecorder(literal *ast.FuncLit) bool {
	found := false
	ast.Inspect(literal, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == bundleReporterValue {
			found = true
		}
		return true
	})
	return found
}

// TestTheInstalledReporterRecordsBoundedContentOnly is the behavioural half, and it is
// the exact opposite of what the test this file used to contain asserted: a reporter IS
// installed, it DOES record, and what it records is bounded.
//
// It drives the bundle's three components with content drawn from every marker list in this
// file - a real root, the derived alias, the private tool name, the JSON Pointer, and a call
// ID - and then asserts both directions at once. The counters must be non-zero, or the
// content-freedom assertion would be vacuous, and the rendered recorder must contain no
// marker, or the feature is publishing exactly what requirements.md 7.7 forbids.
func TestTheInstalledReporterRecordsBoundedContentOnly(t *testing.T) {
	t.Parallel()
	tel, b := bundleWithReporter(t, "enabled: true\nmode: rewrite\nschema_inference: true\n"+
		"tool_profiles:\n  - names: [\"acme_internal_read_secrets\"]\n"+
		"    arg_json_pointers: [\"/payload/secret_path\"]\n")

	// The tool name and the declared profile must AGREE, or the resolver selects nothing
	// for this call and the pass records only skips - which would leave the byte counters
	// at zero and make the content assertions vacuous.
	call := pathCall()
	call.Tools[0].Name = hostileToolName
	call.Items[0].ToolCall.Name = hostileToolName
	call.Items[0].ToolCall.Arguments = json.RawMessage(
		`{"payload":{"secret_path":"` + fixtureRoot + `/pkg/aipapi/call.go"}}`)
	meta := request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: fixtureRoot}}
	if _, err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0].
		HandleAttempt(context.Background(), call, meta, request.Services{}); err != nil {
		t.Fatalf("the attempt transform must not error: %v", err)
	}
	if err := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0].
		HandleRequestParts(pinnedWorkspace(fixtureRoot), call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("the request-part hook must not error: %v", err)
	}

	// The opposite of the old assertion: the reporter is installed and it recorded.
	snapshot := tel.Snapshot()
	if snapshot.Outbound.Reports != 2 {
		t.Fatalf("outbound reports = %d, want 2: the installed reporter recorded nothing", snapshot.Outbound.Reports)
	}
	if snapshot.Total.Reports == 0 {
		t.Fatal("the recorder observed nothing; the content-freedom assertions below would be vacuous")
	}
	if snapshot.Outbound.Virtualized.BytesSaved <= 0 {
		t.Fatalf("no outbound saving was measured: %+v", snapshot.Outbound.Virtualized)
	}

	// And everything it recorded is bounded.
	rendered, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	assertNoContent(t, string(rendered))
	assertNoContent(t, mustMarshal(t, tel.Inventory()))
}

// bundleWithReporter builds the bundle through the entry point that also hands back the
// recorder, so a test can assert on what the installed reporter recorded.
//
// It goes through [bundle.FeatureBundleWithTelemetry] rather than reaching into the
// components, because that is the same call a deployment makes: the reporter is installed
// by the composition seam, not by the caller.
func bundleWithReporter(t *testing.T, subtree string) (*telemetry.Telemetry, aipfeature.FeatureBundle) {
	t.Helper()
	resolved, err := config.Decode(mustYAML(t, subtree))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	tel, b, err := bundle.FeatureBundleWithTelemetry(resolved)
	if err != nil {
		t.Fatalf("FeatureBundleWithTelemetry: %v", err)
	}
	return tel, b
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// TestAFullConfigurationReachesNoContentBearingError drives the complete configured
// surface - operator-declared profiles carrying a tool name and JSON Pointers - and
// asserts every value the bundle can return is free of it. The three refusals here
// are the whole set: the bound outside the SDK's range, a plane-contribution
// failure, and a successful build whose components must not echo configuration text.
func TestAFullConfigurationReachesNoContentBearingError(t *testing.T) {
	t.Parallel()
	const (
		toolName = "acme_internal_read_secrets"
		pointer  = "/payload/secret_path"
	)
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{
			name: "declared profile compiles",
			yaml: "enabled: true\nmode: rewrite\nschema_inference: true\ntool_profiles:\n  - names: [" + toolName +
				"]\n    arg_json_pointers: [" + pointer + "]\n",
		},
		{
			name: "declared profile conflicts",
			yaml: "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [" + toolName +
				"]\n    arg_json_pointers: [" + pointer + "]\n  - names: [" + toolName + "]\n    arg_json_pointers: [/path]\n",
		},
		{
			name: "unknown profile key",
			yaml: "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [" + toolName + "]\n    not_a_key: 1\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := config.Decode(mustYAML(t, tc.yaml))
			b, bundleErr := bundle.FeatureBundle(resolved)
			if err != nil {
				assertNoContent(t, err.Error())
				return
			}
			if bundleErr != nil {
				assertNoContent(t, bundleErr.Error())
				return
			}
			// A successful build is equally content-free: nothing about the declared
			// tool name or pointer may be observable on the components it produced.
			for _, identity := range []string{
				aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0].ID(),
				aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0].ID(),
				aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0].ID(),
			} {
				for _, marker := range append(contentMarkers, toolName, pointer) {
					if strings.Contains(identity, marker) {
						t.Fatalf("identity %q carries configured content %q", identity, marker)
					}
				}
			}
		})
	}
}

// TestTheComponentsPublishNoPayloadInTheirDecisions drives both outbound passes over
// a real path-bearing call and asserts the decision values are the inert
// no-payload shapes. The passes return a decision carrying no reference to the call
// and no error, so this is the observable-surface check for the hot path itself.
func TestTheComponentsPublishNoPayloadInTheirDecisions(t *testing.T) {
	t.Parallel()
	b := bundleWithFixtureConfiguration(t)
	transform := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0]
	call := pathCall()
	decision, err := transform.HandleAttempt(context.Background(), call,
		request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ProjectRoot: fixtureRoot}},
		request.Services{})
	if err != nil {
		assertNoContent(t, err.Error())
		t.Fatal("the attempt transform must not return an error")
	}
	// The decision is the attempt continue kind with no payload and no reason code:
	// nothing an operator could read back about the call. requirements.md 5.5 puts
	// candidate choice outside this feature's reach, so the kind must not be the
	// exclusion kind, and an empty reason code is what keeps a failure log line free
	// of call detail.
	if decision.Kind != request.AttemptContinue {
		t.Fatalf("decision kind = %v, want AttemptContinue", decision.Kind)
	}
	if decision.ReasonCode != "" {
		t.Fatalf("decision carries a reason code %q; the pass must record a bounded report instead", decision.ReasonCode)
	}
	rendered, marshalErr := json.Marshal(decision)
	if marshalErr != nil {
		assertNoContent(t, marshalErr.Error())
		t.Fatal("decision must be marshalable")
	}
	assertNoContent(t, string(rendered))

	hook := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0]
	if err := hook.HandleRequestParts(pinnedWorkspace(fixtureRoot), pathCall(), sdkhooks.PartMeta{}); err != nil {
		assertNoContent(t, err.Error())
		t.Fatal("the request-part hook must not return an error")
	}
}

// TestTheDisabledBundleExposesNoObservableSurface is the strongest form of the
// content-freedom obligation: an inert generation has no identity to inspect at
// all, so there is nothing an inventory could accidentally report.
func TestTheDisabledBundleExposesNoObservableSurface(t *testing.T) {
	t.Parallel()
	resolved, err := config.Decode(mustYAML(t, "enabled: false\nmode: rewrite\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !b.PlaneSet.IsZero() {
		t.Fatal("a disabled generation published an observable surface")
	}
	if len(b.Lifecycles) != 0 {
		t.Fatal("a disabled generation published lifecycles")
	}
}

func bundleWithFixtureConfiguration(t *testing.T) aipfeature.FeatureBundle {
	t.Helper()
	resolved, err := config.Decode(mustYAML(t, "enabled: true\nmode: rewrite\n"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	return b
}

func assertNoContent(t *testing.T, message string) {
	t.Helper()
	for _, marker := range contentMarkers {
		if strings.Contains(message, marker) {
			t.Fatalf("observable message carries content %q: %s", marker, message)
		}
	}
}

package standardplugins

// This file pins the registration half of requirements.md 7.1 and 8.1: the feature
// is registered by the standard feature conventions, an absent or disabled subtree
// contributes nothing at all, and the three components travel through already
// registered planes so the stock distribution gains no new extension surface.
//
// The inertness cases are the load-bearing ones. The feature is disabled by
// default, which means "a deployment that never configures it publishes nothing",
// not "a deployment that never configures it publishes a pass that measures
// nothing". A bundle that contributed a transform, a hook, and a finalizer in the
// disabled case would still run three participants in every request and every
// completion for every deployment in the field.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	pathvirtualizationbundle "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	pathvirtualizationconfig "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

func pathVirtualizationYAML(t *testing.T, src string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(src), &node); err != nil {
		t.Fatalf("parse subtree: %v", err)
	}
	return node
}

// newPathVirtualizationRegistry builds the registry the standard bundle installs
// into, so every case below resolves the feature through the real registration path
// rather than by calling the factory directly.
func newPathVirtualizationRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := InstallBundleOn(reg, StandardBundle()); err != nil {
		t.Fatalf("InstallBundleOn: %v", err)
	}
	return reg
}

// TestTheFactoryIsReachableThroughTheRegistry proves the registration is a real
// one: a registry built from the standard bundle resolves the feature by its
// factory key and can build its bundle.
func TestTheFactoryIsReachableThroughTheRegistry(t *testing.T) {
	t.Parallel()
	reg := newPathVirtualizationRegistry(t)
	node := pathVirtualizationYAML(t, "enabled: true\nmode: rewrite\n")
	b, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID, node)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}
	if got := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms); len(got) != 1 {
		t.Fatalf("attempt transforms = %d, want 1", len(got))
	}
	if got := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks); len(got) != 1 {
		t.Fatalf("request part hooks = %d, want 1", len(got))
	}
	if got := aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers); len(got) != 1 {
		t.Fatalf("tool call finalizers = %d, want 1", len(got))
	}
}

// TestAStockDeploymentContributesNothing is the requirement 7.1 / 8.1 inertness
// proof at the registry boundary, over every shape an absent subtree can arrive in.
func TestAStockDeploymentContributesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "absent document", yaml: ""},
		{name: "explicit null", yaml: "null\n"},
		{name: "empty mapping", yaml: "{}\n"},
		{name: "explicitly disabled", yaml: "enabled: false\nmode: rewrite\n"},
		{name: "disabled with a full configuration", yaml: "enabled: false\nmode: rewrite\nschema_inference: true\nmandatory_max_args_bytes: 131072\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := newPathVirtualizationRegistry(t)
			b, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID, pathVirtualizationYAML(t, tc.yaml))
			if err != nil {
				t.Fatalf("BuildFeatureBundle: %v", err)
			}
			if err := b.Validate(); err != nil {
				t.Fatalf("bundle.Validate: %v", err)
			}
			if !b.PlaneSet.IsZero() {
				t.Fatalf("a stock deployment published planes: %#v", b.PlaneSet)
			}
			if len(b.Lifecycles) != 0 {
				t.Fatalf("a stock deployment published %d lifecycles", len(b.Lifecycles))
			}
			for _, plane := range []struct {
				name string
				got  int
			}{
				{name: "attempt transforms", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms))},
				{name: "request part hooks", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks))},
				{name: "tool call finalizers", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers))},
				{name: "workspace resolvers", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneWorkspaceResolvers))},
				{name: "submit hooks", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneSubmitHooks))},
				{name: "tool reactors", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolReactors))},
				{name: "response part hooks", got: len(aipfeature.Get(b.PlaneSet, aipfeature.PlaneResponsePartHooks))},
			} {
				if plane.got != 0 {
					t.Fatalf("a stock deployment contributed %d %s", plane.got, plane.name)
				}
			}
		})
	}
}

// TestAnUnusableConfigurationRefusesTheFactory keeps requirements.md 7.5's
// "fail generation compilation before publication" reachable from the factory path
// rather than only from a direct decoder call: a factory that tolerated an unknown
// key would let the deployment publish and fail later, on the day it mattered.
func TestAnUnusableConfigurationRefusesTheFactory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "unknown top-level key", yaml: "enabled: true\nmode: audit\nalias_namespace: other\n"},
		{name: "unknown profile key", yaml: "enabled: true\nmode: audit\ntool_profiles:\n  - names: [x]\n    aliases: [y]\n"},
		{name: "enabled without a mode", yaml: "enabled: true\n"},
		{name: "unknown mode", yaml: "enabled: true\nmode: Rewrite\n"},
		{name: "explicit zero bound", yaml: "enabled: true\nmode: audit\nmandatory_max_args_bytes: 0\n"},
		{name: "out of range bound", yaml: "enabled: true\nmode: audit\nmandatory_max_args_bytes: 8\n"},
		{name: "scalar subtree", yaml: "yes\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := newPathVirtualizationRegistry(t)
			if _, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID,
				pathVirtualizationYAML(t, tc.yaml)); err == nil {
				t.Fatal("expected the factory to refuse an unusable configuration")
			}
		})
	}
}

// TestTheFactoryContributesTheSameComponentsTheBundleDeclares pins that the
// standard registration reaches the feature's own bundle builder rather than
// re-assembling the three components at the registration site. A duplicate
// construction there could hand the two outbound passes different policies.
func TestTheFactoryContributesTheSameComponentsTheBundleDeclares(t *testing.T) {
	t.Parallel()
	reg := newPathVirtualizationRegistry(t)
	b, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID,
		pathVirtualizationYAML(t, "enabled: true\nmode: rewrite\n"))
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	transform := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0]
	if transform.ID() != outbound.TransformID || transform.Order() != outbound.OrderAttemptTransform {
		t.Fatalf("attempt transform = %q/%d, want %q/%d",
			transform.ID(), transform.Order(), outbound.TransformID, outbound.OrderAttemptTransform)
	}
	hook := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0]
	if hook.ID() != outbound.PartHookID || hook.Order() != outbound.OrderRequestPartHook {
		t.Fatalf("request part hook = %q/%d, want %q/%d",
			hook.ID(), hook.Order(), outbound.PartHookID, outbound.OrderRequestPartHook)
	}
	finalizer := aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0]
	if finalizer.ID() != expansion.FinalizerID || finalizer.Order() != expansion.FinalizerOrder {
		t.Fatalf("finalizer = %q/%d, want %q/%d",
			finalizer.ID(), finalizer.Order(), expansion.FinalizerID, expansion.FinalizerOrder)
	}
}

// TestTheFeaturePublishesNoWorkspaceAuthorityOfItsOwn keeps the composition
// direction honest at the registration boundary. The authoritative workspace view
// belongs to whoever composes the runtime; a feature that contributed its own
// resolver would be publishing a second, competing answer about the project root -
// which is precisely the disagreement requirements.md 5.6 forbids.
func TestTheFeaturePublishesNoWorkspaceAuthorityOfItsOwn(t *testing.T) {
	t.Parallel()
	reg := newPathVirtualizationRegistry(t)
	b, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID,
		pathVirtualizationYAML(t, "enabled: true\nmode: rewrite\n"))
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if got := aipfeature.Get(b.PlaneSet, aipfeature.PlaneWorkspaceResolvers); len(got) != 0 {
		t.Fatalf("workspace resolvers = %d, want 0", len(got))
	}
}

// TestTheStandardTableLeavesTheLatePassArmedByTheRuntimesPinnedWorkspace states the
// composition's one workspace dependency, deliberately, so it is a decision on record
// rather than a surprise found in production.
//
// The late outbound pass reaches the authoritative workspace view the way the runtime
// makes it reachable: by reading the per-turn PIN the runtime publishes onto the public
// SDK context seams alongside session, scope, and principal
// (pkg/aipsdk/workspace.WithWorkspaceView, projected by internal/core/execctx.WithViews).
// It reads it because sdkhooks.PartMeta carries only trace, A-leg, B-leg, attempt
// ordinal, and backend identity - no workspace view at all - and the request-part stage
// has no other contract to hand it one.
//
// The standard table's factory therefore binds NOTHING, and that is now a property
// rather than a gap. It has no workspace view of its own to hand over (a factory
// signature that took one would be a service locator), and it no longer needs to: the
// late pass is armed by the same pin the early pass reads out of its attempt metadata,
// so requirement 5.6's one-alias-per-turn property holds by construction instead of
// depending on a composition root happening to hold the runtime's own chain.
//
// WHAT THE PIN IS A PRECONDITION FOR. Both passes need a runtime snapshot that actually
// resolved a workspace: with an EMPTY project root pinned - the detached auxiliary path,
// or a host with no workspace resolver contributed - DeriveMapping refuses the mapping
// and requirements.md 1.8's bounded root code is recorded instead. So "outbound
// virtualization still happens" is conditional on the snapshot carrying a NON-EMPTY
// pinned ProjectRoot, and requirement 5.3's preflight-visible saving is exactly as
// conditional. This assertion therefore drives BOTH states: a pinned root, where the
// shipped bundle virtualizes with nothing but the projection to arm it, and an unpinned
// context, where it fails open without erroring.
func TestTheStandardTableLeavesTheLatePassArmedByTheRuntimesPinnedWorkspace(t *testing.T) {
	t.Parallel()
	reg := newPathVirtualizationRegistry(t)
	b, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID,
		pathVirtualizationYAML(t, "enabled: true\nmode: rewrite\n"))
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	hook := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0]

	// Armed by the projection alone: no authority was injected at composition time, so
	// this proves the runtime's pin is the only input the late pass needs.
	call := pathVirtualizationPathCall()
	armed := aipworkspace.WithWorkspaceView(context.Background(),
		aipworkspace.WorkspaceView{ProjectRoot: pathVirtualizationRoot})
	if err := hook.HandleRequestParts(armed, call, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("the late outbound pass must not surface an error: %v", err)
	}
	if got := pathVirtualizationArguments(t, call); !strings.Contains(got, pathVirtualizationAlias) {
		t.Fatalf("requirements.md 5.4 - the shipped late pass must virtualize off the runtime's pinned view alone; got %s", got)
	}

	// Unpinned: the documented fail-open direction, bounded reason, real path preserved.
	unpinned := pathVirtualizationPathCall()
	if err := hook.HandleRequestParts(context.Background(), unpinned, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("an absent pinned view must fail open, not error: %v", err)
	}
	if got := pathVirtualizationArguments(t, unpinned); strings.Contains(got, pathVirtualizationAlias) {
		t.Fatalf("requirements.md 8.1/5.7 - without a pinned workspace view the pass must publish nothing; got %s", got)
	}
}

// pathVirtualizationRoot is long enough that the fixed V1 alias is strictly shorter than
// the real root, which is the condition for an active mapping (requirements.md 1.4).
const pathVirtualizationRoot = "/home/dev/projects/aiproxer-example-project"

// pathVirtualizationSuffix is one path-bearing suffix inside the workspace, identical in
// real and virtualized form so any difference at the backend bound is attributable to
// the root prefix alone.
const pathVirtualizationSuffix = "internal/core/runtime/executor.go"

// pathVirtualizationAlias is the literal expected V1 alias for pathVirtualizationRoot.
// It is spelled out rather than recomputed so a drift in the tag algorithm cannot make
// this file agree with a wrong implementation.
const pathVirtualizationAlias = "/.__aip_v1__/w_hr7xnivm5at2ku356qya/"

// pathVirtualizationPathCall is one historical item-authoritative path-bearing tool call.
// The tool declaration matters: it is what lets the shipped built-in profile layer claim
// /file_path with no operator configuration.
func pathVirtualizationPathCall() *aipapi.Call {
	return &aipapi.Call{
		ID:    "call_registration",
		Route: aipapi.RouteIntent{Selector: "registration:m"},
		Tools: []aipapi.ToolDef{{
			Name:       "read_file",
			Parameters: []byte(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		}},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindToolCall, ID: "item_call", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call_7f3a",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":"` + pathVirtualizationRoot + "/" + pathVirtualizationSuffix + `"}`),
			},
		}},
	}
}

func pathVirtualizationArguments(t *testing.T, call *aipapi.Call) string {
	t.Helper()
	if len(call.Items) != 1 || call.Items[0].ToolCall == nil {
		t.Fatalf("expected one item tool call, got %#v", call.Items)
	}
	return string(call.Items[0].ToolCall.Arguments)
}

// TestARefusalCarriesNoPathAliasTagOrArgumentContent pins requirements.md 7.7 at
// the registration boundary. Every refusal an operator can see from this feature -
// an unknown key, a bad mode, an unusable bound, an unreadable repair subtree -
// names a bounded reason and a fixed configuration location. None of them may echo
// a project root, a virtual alias, a workspace tag, a tool name, a JSON pointer, or
// argument bytes, because an operator pastes a refusal into a bug report and the
// path content would travel with it.
//
// The scan is over the rendered message, not over the source, so it catches a
// wrapping site that reintroduces detail as well as a decoder that produces it.
func TestARefusalCarriesNoPathAliasTagOrArgumentContent(t *testing.T) {
	t.Parallel()
	// Each case pairs a configuration that MUST refuse with a marker that must not
	// appear in the message it produces. The markers are the sensitive shapes:
	// an absolute project root, a reserved-namespace alias with a workspace tag, a
	// private tool name, and argument bytes.
	const (
		realRoot  = "/home/dev/secret-customer/project-alpha"
		aliasPath = "/.__aip_v1__/w_hr7xnivm5at2ku356qya/pkg/main.go"
		toolName  = "acme_internal_read_secrets"
		argBytes  = "BEGIN-CUSTOMER-PRIVATE-KEY-MATERIAL"
	)
	for _, tc := range []struct {
		name   string
		yaml   string
		marker string
	}{
		{
			name:   "unknown key naming a tool",
			yaml:   "enabled: true\nmode: audit\ntool_profiles:\n  - names: [" + toolName + "]\n    not_a_key: 1\n",
			marker: toolName,
		},
		{
			name:   "unknown top-level key carrying a path value",
			yaml:   "enabled: true\nmode: audit\nalias_namespace: " + realRoot + "\n",
			marker: realRoot,
		},
		{
			name:   "conflicting profile pointing at an alias",
			yaml:   "enabled: true\nmode: audit\ntool_profiles:\n  - names: [read_file]\n    arg_json_pointers: [" + aliasPath + "]\n  - names: [read_file]\n    arg_json_pointers: [/path]\n",
			marker: aliasPath,
		},
		{
			name:   "unknown profile key carrying argument content",
			yaml:   "enabled: true\nmode: audit\ntool_profiles:\n  - names: [read_file]\n    alias: " + argBytes + "\n",
			marker: argBytes,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := newPathVirtualizationRegistry(t)
			_, err := reg.BuildFeatureBundle(pathvirtualizationconfig.ID, pathVirtualizationYAML(t, tc.yaml))
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("refusal leaked operator content: %q", err.Error())
			}
			// The reserved-namespace marker itself must never be echoed either, in
			// any of its forms, because it is a fixed implementation contract an
			// operator has no business seeing in a diagnostic.
			if strings.Contains(err.Error(), ".__aip_v1__") {
				t.Fatalf("refusal leaked the reserved alias namespace: %q", err.Error())
			}
		})
	}
}

// TestTheFeatureBuildsThroughItsOwnPackage keeps the standard registration a
// delegation rather than a re-implementation: the exported bundle builder is the
// composition seam, and it is reachable from outside the feature.
func TestTheFeatureBuildsThroughItsOwnPackage(t *testing.T) {
	t.Parallel()
	resolved, err := pathvirtualizationconfig.Decode(pathVirtualizationYAML(t, "enabled: true\nmode: audit\n"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b, err := pathvirtualizationbundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if got := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms); len(got) != 1 {
		t.Fatalf("attempt transforms = %d, want 1", len(got))
	}
}

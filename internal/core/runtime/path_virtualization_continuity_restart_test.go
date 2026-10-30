package runtime_test

// Spec: b-leg-path-virtualization Task 10.1. Requirements 6.1, 6.2, 6.3, 6.4, 6.5,
// and 6.6.
//
// Design sections read for this file:
//
//   - "Continuity and State" (no persistent data model; the primary mapping is
//     reconstructed on every request/attempt from the authoritative project root and
//     the fixed V1 algorithm; therefore same root/flavor produces the same workspace
//     tag and alias after restart/reload; provider-side continuation for an unchanged
//     root continues to use the same alias; a changed root derives a different tag, so
//     a provider-retained old alias fails closed rather than rebinding; NO prior-root
//     dictionary is needed because the stale tag is carried in the alias itself);
//   - "2. Workspace-Bound Alias Identity" (exact tag algorithm, exact V1 virtual-root
//     forms, and the "Reserved-alias recognition and stale-root rule" steps 1-6);
//   - "Configuration and Composition" (the three components are built from one
//     compiled resolution, and standard registration follows existing conventions);
//   - "Error Handling" ("Stale/different workspace tag or incompatible alias flavor:
//     fail closed with workspace_mismatch").
//
// WHAT THIS FILE IS
//
// Task 8.2 characterized ONE turn. This file characterizes ACROSS turns and across
// component lifetimes, which is the half of the feature requirements.md 6 exists for
// and the half no earlier test could reach: every test shipped so far builds ONE
// feature object, pins ONE root, and drives ONE turn. A prior-root dictionary, a cached
// mapping, a tag derived from anything but the pinned root, a continuation path that
// re-derives differently from a full-history path, or a Windows/UNC root change are all
// invisible to those tests and all fail here.
//
// WHAT IT REUSES
//
// The real runtime harness from Task 8.2 - expRun/expScenario, the real shipped
// outbound attempt transform, the real shipped request-part hook, the real shipped
// expansion finalizer, the real assembler, the real backend boundary, the real tool
// policy and reactor planes, and the real client-event stream. Task 10.1 adds NO
// second end-to-end harness and NO stand-in component. The only additions to the
// shared harness are additive scenario fields (an ingress override and a pinned root
// override), a remeasurement helper that hands back the exact selected path value
// rather than only counting it, and flavor-correct document escaping, because
// requirements.md 6.5 enumerates Windows and UNC root changes and a POSIX-only harness
// could not reach them.
//
// The composition itself is reused too: the reload and continuation observations build
// a generation through the shipped bundle.FeatureBundle seam and drive the three
// components it published, rather than re-constructing them by hand.
//
// HOW RECREATION AND RESTART ARE PERFORMED
//
// Four independent observations of the same identity:
//
//  1. COMPONENT RECREATION. contPublishedComponents decodes the operator subtree and
//     builds the bundle afresh, and the previous generation's components are released
//     before the next one exists. The component IDENTITIES are compared, so "rebuilt"
//     is a checked fact rather than a claim.
//  2. GENERATION RELOAD. A generation compiled with a DIFFERENT declared completeness
//     bound sits between two observations of the plain subtree, so the two are not two
//     references to one compiled resolution and a workspace identity that secretly
//     depended on generation-scoped configuration would move.
//  3. PROCESS RESTART. The identity is re-derived in a genuinely separate OS process
//     started from this test binary, which shares no address space, no heap, and no
//     package-level variable with the test.
//  4. FULL RUNTIME TURNS. expRun constructs every runtime component from scratch on
//     every invocation, so two turns share no executor, no finalizer, and no resolver.
//
// CONTENT FREEDOM
//
// No assertion message in this file contains a path, an alias, a workspace tag, or a
// tool-call ID. Roots, aliases, and tags are compared, never formatted. Messages carry
// requirement numbers, occurrence counts, byte totals, bounded reason labels, tag
// LENGTHS, and stage ordinals only. The places that must reason about a root's bytes
// report the root's LENGTH and whether two values are equal, which is enough to
// distinguish the cases without printing one.
//
// DETERMINISM
//
// Every run pins its root as a literal, derives the alias by the frozen content-
// defined digest, walks slices in order, uses the shared seeded RNG and pinned clock,
// and orders every scan by sorted key rather than map iteration. The one child process
// the restart test starts reads nothing from the parent and is bounded by a generous
// deadline. Repeated runs produce identical aliases, tags, counts, and ordinals, so
// -count=5 is a real repeat rather than a re-shuffle.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

// contFeatureDir is the feature tree the structural guard walks. It is a path RELATIVE
// to this package's directory because the test binary runs with its working directory
// set there, and it is discovered rather than hard-coded per file so a new subpackage
// is covered automatically.
const contFeatureDir = "../../plugins/features/pathvirtualization"

// contMinStructuralFiles is the smallest number of production sources that makes the
// structural guard worth running. It is deliberately well below the tree's real size:
// the guard's purpose is to notice a NEW package-level map, and a threshold equal to
// today's count would fail loudly on an unrelated deletion rather than on the property.
const contMinStructuralFiles = 10

const (
	// contContinuationSelector is the bounded route selector the canonical continuation
	// fixture uses. It names the SAME backend the shared harness registers, so a
	// continued turn reaches the same recording backend the full-history turn reached.
	// It is a fixed token, never formatted into a failure message.
	contContinuationSelector = expSelector
	// contContinuationParent is the continuation parent identifier the canonical
	// continuation fixture carries.
	contContinuationParent = "resp-path-virtualization-continuation"
	// contContinuationSession is the client session both turns of the continuation case
	// share, so the second turn is a SESSION continuation rather than an unrelated
	// request.
	contContinuationSession = "session-path-virtualization-continuation"
	// contContinuationInputID and contContinuationInputText are the new input of the
	// continued turn. Fixed fixture tokens, never formatted.
	contContinuationInputID   = "item_path_virtualization_continuation_input"
	contContinuationInputText = "path-virtualization-continuation-next-question"
	// contContinuationSecondParent is a SECOND continuation parent of the same
	// workspace. Provider-side continuation is unbounded in number, and one generation
	// must publish one alias for every one of them, so a second parent is what makes the
	// same-instance observation more than a single repeated call.
	contContinuationSecondParent = "resp-path-virtualization-continuation-second"

	// contFixtureTool is the exact tool name the shipped built-in profile layer claims
	// on /file_path, so every run exercises the production selector policy.
	contFixtureTool = expToolName
	// contFixtureCallID, contFixtureItemID, and contFixtureSuffix are fixed fixture
	// tokens. None is ever formatted into a failure message.
	contFixtureCallID = "path-virtualization-continuity-call"
	contFixtureItemID = "item_path_virtualization_continuity"
	contFixtureSuffix = "src/main.go"
	// contFixtureSecondSuffix is a SECOND path-bearing suffix, used by the continued
	// turns' response-side expansion. A model names a different file on each continued
	// turn under the alias it retained, so the two expansions must differ in exactly
	// this suffix and agree on the real root in front of it.
	contFixtureSecondSuffix = "src/continued_turn.go"

	// contReloadSubtree is the INTERLUDE generation's operator subtree: identical to
	// the plain one except for its declared completeness bound. Publishing it between
	// two observations of the plain subtree is what proves the workspace identity does
	// not depend on generation-scoped configuration.
	contReloadSubtree = "enabled: true\nmode: rewrite\nmandatory_max_args_bytes: 65536\n"
	// contReloadBound is the bound that subtree declares. It is the SDK's own minimum
	// configurable mandatory bound, so it is a legal value rather than an arbitrary
	// number, and it is deliberately not the default.
	contReloadBound = 65536
)

const (
	// contHelperTestName is the helper this file re-executes in a fresh OS process.
	contHelperTestName = "TestStreamToolCall_RestartHelperProcessDerivesTheAliasFromTheRootBytesAlone"
	// contHelperEnvMarker is the environment flag that tells the re-executed binary it
	// is the helper rather than a suite run.
	contHelperEnvMarker = "AIP_PATH_VIRTUALIZATION_RESTART_HELPER"
	// contHelperEnvRoot carries the root the helper must derive an alias for.
	contHelperEnvRoot = "AIP_PATH_VIRTUALIZATION_RESTART_ROOT"
	// contHelperTimeout bounds the child process. It is generous because the child is
	// a full test binary start, and short enough that a hung child fails the test
	// rather than the suite.
	contHelperTimeout = 60 * time.Second

	// The helper's only two failure signals, both exit codes. A refusal reason is a
	// bounded label the parent does not read, so the parent needs a binary outcome.
	contHelperExitUnusableRoot = 3
	contHelperExitWriteFailed  = 4
)

// contFrozenTagChars is the exact encoded length of a V1 workspace tag: 96 bits in
// unpadded base32 (mapping.go's workspaceTagChars). It is re-declared as a literal
// here rather than imported so a drift in the production constant cannot silently
// resize the assertion below along with the implementation.
const contFrozenTagChars = 20

// contFlavor is one authoritative-root change requirement 6.5 and design.md
// "Reserved-alias recognition and stale-root rule" enumerate.
//
// rootA is the workspace the alias was minted under; rootB is the authoritative root
// that replaced it. Both are supported absolute forms whose alias is strictly shorter
// than the root, so BOTH mappings are active and the rejection cannot be confused with
// a mapping that simply is not usable.
type contFlavor struct {
	// name is a bounded, content-free failure-message token.
	name  string
	rootA string
	rootB string
	// wantFlavor is the path flavor both roots must classify as, asserted so a case
	// cannot silently stop being the flavor its label claims.
	wantFlavor pathvirtualization.PathFlavor
	// separator is the segment separator that flavor's alias root already ends with.
	// It is spelled here so the fixture joins an alias to its suffix with the
	// flavor's own separator rather than with a POSIX one.
	separator string
}

// contFlavors enumerates requirement 6.5's cases. Every entry is a DISTINCT lexical
// situation, which is what design.md's closing sentence of the stale-root rule calls
// out explicitly ("including same-drive Windows root changes") - the same-drive case
// is the one a naive prefix comparison would expand against the new root, because the
// alias and the root share a volume prefix.
//
// The four required shapes are all present and each has its own bounded outcome:
// a POSIX change, a SAME-DRIVE Windows change, a DIFFERENT-DRIVE Windows change, and
// UNC plus extended-path changes.
func contFlavors() []contFlavor {
	return []contFlavor{
		{
			name:       "posix_root_change",
			rootA:      "/home/dev/workspaces/aip-path-virtualization-worktree/packages/agent-runtime",
			rootB:      "/srv/team/workspace/aip-path-virtualization-archive/packages/agent-runtime",
			wantFlavor: pathvirtualization.FlavorPOSIX,
			separator:  "/",
		},
		{
			name:       "same_drive_windows_root_change",
			rootA:      `C:\Users\dev\source\repos\aiproxer-example-project`,
			rootB:      `C:\Users\dev\source\repos\aiproxer-example-project-fork`,
			wantFlavor: pathvirtualization.FlavorWindowsDrive,
			separator:  `\`,
		},
		{
			name:       "different_drive_windows_root_change",
			rootA:      `D:\Users\dev\source\repos\aiproxer-example-project`,
			rootB:      `E:\Users\dev\source\repos\aiproxer-example-project`,
			wantFlavor: pathvirtualization.FlavorWindowsDrive,
			separator:  `\`,
		},
		{
			name:       "unc_root_change",
			rootA:      `\\build01\team\source\repos\aiproxer-example-project`,
			rootB:      `\\build01\team\source\repos\aiproxer-example-project-fork`,
			wantFlavor: pathvirtualization.FlavorWindowsUNC,
			separator:  `\`,
		},
		{
			name:       "extended_drive_root_change",
			rootA:      `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			rootB:      `\\?\C:\Users\dev\source\repos\aiproxer-example-project-fork`,
			wantFlavor: pathvirtualization.FlavorWindowsExtendedDrive,
			separator:  `\`,
		},
		{
			name:       "extended_unc_root_change",
			rootA:      `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			rootB:      `\\?\UNC\build01\team\source\repos\aiproxer-example-project-fork`,
			wantFlavor: pathvirtualization.FlavorWindowsExtendedUNC,
			separator:  `\`,
		},
	}
}

// contAssertActiveMapping derives the mapping for root and asserts it is the ACTIVE
// kind requirement 6.5's cases need: a supported absolute form whose fixed V1 alias
// is strictly shorter than the root.
//
// The returned mapping is used for COMPARISON only. Nothing in this file formats its
// tag or alias into a failure message.
func contAssertActiveMapping(t *testing.T, root string, wantFlavor pathvirtualization.PathFlavor) pathvirtualization.Mapping {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("fixture: a case root must be one of the five supported absolute forms; the refusal reason is bounded and content-free")
	}
	if mapping.Flavor != wantFlavor {
		t.Fatalf("fixture: a case root must classify as its label's flavor: got %v want %v", mapping.Flavor, wantFlavor)
	}
	if mapping.VirtualRoot == "" {
		t.Fatalf("fixture: a case root must derive an ACTIVE mapping, so the refusal below cannot be confused with an unusable root: root_bytes=%d",
			len(root))
	}
	if len(mapping.WorkspaceTag) != contFrozenTagChars {
		t.Fatalf("fixture: the frozen V1 tag must be exactly %d base32 characters: got %d", contFrozenTagChars, len(mapping.WorkspaceTag))
	}
	return mapping
}

// contAliasPath is the path-bearing value a model emits: one mapping's alias root plus
// one suffix.
//
// No separator is supplied and none is needed: the fixed V1 alias root ALWAYS ends with
// its flavor's own separator (mapping.go's virtualRoot), for every one of the five
// supported forms, so appending the suffix directly can neither drop a boundary byte
// nor double one. A flavor parameter here would be a value nothing reads.
func contAliasPath(mapping pathvirtualization.Mapping, suffix string) string {
	return mapping.VirtualRoot + suffix
}

// contContinuationCall is the CANONICAL provider-side continuation shape this
// repository uses, taken from the production producer rather than invented.
//
// The chain, read from the repository:
//
//  1. aipapi.Call.PreviousResponseID (pkg/aipapi/call.go:72-75) documents the field
//     as "a proxy-owned continuation parent" which "allows an item-authoritative
//     continuation request to carry an intentionally empty input item slice; the
//     continuation resolver supplies the materialized items", and Call.Validate
//     enforces exactly that pairing (call.go:107): an item-authoritative call with no
//     items is legal ONLY when a previous-response id is set.
//  2. The OpenResponses decoder produces that shape.
//     internal/plugins/protocols/openresponses/decode_request.go:155-163 keeps a
//     NON-NIL EMPTY canonicalItems slice "so the canonical call remains
//     item-authoritative" precisely because previous_response_id is set, and sets
//     PreviousResponseID at decode_request.go:235. With `input` present the slice
//     instead holds the new input items - still item-authoritative, still with the
//     continuation parent.
//  3. The frontend resolves the parent BEFORE the runtime sees the call:
//     internal/plugins/frontends/openresponses/handler_pipe.go:402 calls the resolver,
//     and pkg/aipsdk/continuation/materialize.go:56-63 rebuilds the call as
//     CloneCall(baseCall) with Items replaced by the materialized trajectory and
//     Messages/Instructions cleared. CloneCall copies the struct by value, so the
//     continuation parent SURVIVES materialization.
//
// So the shape the runtime actually receives on a provider-retained continuation is:
// item authority, a continuation parent, and an item trajectory carrying the retained
// model-visible history plus the new input. That is what this fixture builds. The
// retained tool call below is the path-bearing surface the model saw in the prior
// turn - which is exactly the surface requirements.md 6.4 says the continued turn
// must keep addressing with the SAME alias.
// contContinuationCall builds the continued turn for one continuation parent.
//
// The parent is a parameter so one generation can be driven across SEVERAL
// continuations of the SAME workspace, which is the shape requirement 6.4 is about:
// provider-side continuation is unbounded in number, and every one of them must
// resolve to one alias.
func contContinuationCall(realRoot, parent string) *aipapi.Call {
	retained := expFixture{root: realRoot, suffix: expHistorySuffix}
	if !json.Valid([]byte(retained.modelArgsDocument())) {
		// The retained document must be one complete JSON value before anything runs.
		panic("path-virtualization-continuity fixture: the retained history document must be one complete JSON value")
	}
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: contContinuationSelector},
		// The retained model-visible history: one assistant tool call carrying the
		// path-bearing argument the provider is holding on the continued turn.
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindToolCall, ID: contFixtureItemID, Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    contFixtureCallID,
					Name:      expToolName,
					Arguments: json.RawMessage(retained.modelArgsDocument()),
				},
			},
			// The new input of the continued turn, and the terminal forwardable user
			// message the runtime requires on an ingress trajectory.
			{
				Kind: aipapi.ItemKindMessage, ID: contContinuationInputID, Status: aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: contContinuationInputText}},
			},
		},
		PreviousResponseID: parent,
		Tools: []aipapi.ToolDef{{
			Name:       expToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField + `":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		// Session is carried on the continued turn so the fixture is a SESSION
		// continuation rather than two unrelated requests, which is the shape
		// requirements.md 6.4 and 6.5 are about.
		Session: aipapi.SessionRef{ClientSessionID: contContinuationSession},
	}
}

// contAssertCanonicalContinuationShape proves the fixture really is the canonical
// continuation representation before any component sees it.
//
// Without this guard a harness change that dropped the continuation parent or the
// item authority would turn the whole continuation case into an ordinary replay test
// that still passes, which is exactly the vacuity this file exists to avoid.
func contAssertCanonicalContinuationShape(t *testing.T, call *aipapi.Call) {
	t.Helper()
	if call.Items == nil {
		t.Fatal("fixture: a provider-side continuation request is item-authoritative with a NON-NIL item slice (pkg/aipapi/call.go:107)")
	}
	if len(call.Items) == 0 {
		t.Fatal("fixture: the continuation resolver supplies the materialized trajectory, so the continued turn carries retained history plus its new input")
	}
	if call.PreviousResponseID == "" {
		t.Fatal("fixture: an item-authoritative continuation request carries a continuation parent (pkg/aipapi/call.go:72-75)")
	}
	if call.PreviousResponseID == "" {
		t.Fatal("fixture: the continuation parent must be the case's own fixed token")
	}
	if !call.HasItemAuthority() {
		t.Fatal("fixture: the continuation request must carry item authority, not legacy message authority")
	}
	if len(call.Messages) != 0 || len(call.Instructions) != 0 {
		t.Fatalf("fixture: materialization CLEARS the legacy authorities (pkg/aipsdk/continuation/materialize.go:60-61): messages=%d instructions=%d",
			len(call.Messages), len(call.Instructions))
	}
	// The retained history must really carry a path-bearing tool call, or the continued
	// turn would have no surface for requirement 6.4 to be about.
	retained := 0
	terminalUser := false
	for _, item := range call.Items {
		if item.ToolCall != nil && len(item.ToolCall.Arguments) > 0 {
			retained++
		}
		if item.Kind == aipapi.ItemKindMessage && item.Role == aipapi.RoleUser {
			terminalUser = true
		}
	}
	if retained != 1 {
		t.Fatalf("fixture: the continued turn must carry exactly one retained path-bearing tool call: retained=%d", retained)
	}
	if !terminalUser {
		t.Fatal("fixture: the continued turn must carry a terminal forwardable user message")
	}
	// The canonical validator must ACCEPT this shape. If it refuses, the fixture is
	// not the representation the contract permits.
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture: the canonical contract must accept the continuation shape: error_type=%T", err)
	}
}

// contOperatorNode parses one operator subtree, which is the input a generation
// publication is given. Parsing it HERE rather than reusing a shared node is
// deliberate: each observation parses its own, so no decoded tree is shared between
// two generations either.
func contOperatorNode(t *testing.T, subtree string) yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(subtree), &document); err != nil {
		t.Fatalf("fixture: the operator subtree must parse: error_type=%T", err)
	}
	return document
}

// contAssertDistinctComponents proves two observations really did publish DIFFERENT
// component objects.
//
// This is the load-bearing non-vacuity guard for the restart equivalence: without it,
// "the same alias after rebuild" could be satisfied by one generation driven three
// times, which is a repeat rather than a restart. Component identity is observable -
// they are interfaces holding pointers - so this needs no reflection.
func contAssertDistinctComponents(t *testing.T, leftLabel string, left contBundleComponents, rightLabel string, right contBundleComponents) {
	t.Helper()
	if left.transform == nil || right.transform == nil ||
		left.hook == nil || right.hook == nil ||
		left.finalizer == nil || right.finalizer == nil {
		t.Fatalf("fixture: the %s and %s observations must both carry a component on every plane: transform_present=%t hook_present=%t finalizer_present=%t",
			leftLabel, rightLabel,
			left.transform != nil && right.transform != nil,
			left.hook != nil && right.hook != nil,
			left.finalizer != nil && right.finalizer != nil)
	}
	if left.transform == right.transform {
		t.Fatalf("fixture: the %s and %s observations must publish DIFFERENT attempt transforms, or nothing was rebuilt",
			leftLabel, rightLabel)
	}
	if left.hook == right.hook {
		t.Fatalf("fixture: the %s and %s observations must publish DIFFERENT request-part hooks, or nothing was rebuilt",
			leftLabel, rightLabel)
	}
	if left.finalizer == right.finalizer {
		t.Fatalf("fixture: the %s and %s observations must publish DIFFERENT expansion finalizers, or nothing was rebuilt",
			leftLabel, rightLabel)
	}
}

// contBundleComponents is one published generation's three components, read straight
// out of the frozen plane set the bundle built.
//
// It exists so the reload test can drive the REAL composed objects rather than
// re-constructing them, which is what makes "the same generation republished" a claim
// about the shipped composition instead of about a test-local assembly.
type contBundleComponents struct {
	transform  request.AttemptTransform
	hook       sdkhooks.RequestPartHook
	finalizer  toolcall.Finalizer
	bufferSpec toolcall.BufferingSpec
}

// contPublishedComponents builds one generation from an operator subtree and returns
// its three components with the declared completeness requirement.
//
// It is deliberately the ONLY place a generation is constructed in this file: the
// reload test calls it repeatedly and discards every result before the next call, so
// no component instance is ever shared between two observations.
func contPublishedComponents(t *testing.T, subtree string) contBundleComponents {
	t.Helper()
	resolved, err := config.Decode(contOperatorNode(t, subtree))
	if err != nil {
		t.Fatalf("fixture: the operator subtree must compile into a resolution; the refusal is a bounded operator-facing reason")
	}
	published, err := bundle.FeatureBundle(resolved)
	if err != nil {
		t.Fatalf("fixture: the resolution must publish a bundle; the refusal is a bounded operator-facing reason")
	}
	if err := published.Validate(); err != nil {
		t.Fatalf("fixture: the published bundle must validate")
	}
	transforms := aipfeature.Get(published.PlaneSet, aipfeature.PlaneAttemptTransforms)
	hooksList := aipfeature.Get(published.PlaneSet, aipfeature.PlaneRequestPartHooks)
	finalizers := aipfeature.Get(published.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(transforms) != 1 || len(hooksList) != 1 || len(finalizers) != 1 {
		t.Fatalf("fixture: one published generation contributes exactly one component per plane: transforms=%d hooks=%d finalizers=%d",
			len(transforms), len(hooksList), len(finalizers))
	}
	declaration, ok := finalizers[0].(toolcall.BufferingRequirement)
	if !ok {
		t.Fatalf("fixture: the published expansion finalizer must declare a completeness requirement: %T", finalizers[0])
	}
	return contBundleComponents{
		transform:  transforms[0],
		hook:       hooksList[0],
		finalizer:  finalizers[0],
		bufferSpec: declaration.ToolCallBufferingRequirement(),
	}
}

// contGenerationOutput is one generation's measured result: the workspace tag and
// alias it minted, and the arguments its components published.
//
// tagBytes and aliasBytes are the ONLY content-bearing values here and they are held
// for comparison, never formatted.
type contGenerationOutput struct {
	tag   string
	alias string
	// earlyArgs and lateArgs are the argument documents the two outbound passes
	// published for the same canonical call.
	earlyArgs string
	lateArgs  string
	// finalArgs is what the published expansion finalizer produced for the model-emitted
	// alias, or empty when it refused.
	finalArgs string
	finalCode string
	finalKind toolcall.Action
}

// contDriveGeneration drives one published generation end to end over a canonical
// path-bearing call and returns what it published.
//
// All three components are driven - the real candidate attempt transform, the real
// request-part hook reading the public SDK workspace projection, and the real shipped
// expansion finalizer - so the output is a statement about the whole composition and
// not about one pass.
func contDriveGeneration(t *testing.T, components contBundleComponents, root string) contGenerationOutput {
	t.Helper()
	alias := expAliasOf(t, root)
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the pinned root must derive an active mapping; its refusal reason is bounded and content-free")
	}

	earlyCall := contCanonicalCall(root)
	decision, err := components.transform.HandleAttempt(context.Background(), earlyCall,
		request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}},
		request.Services{})
	if err != nil {
		t.Fatalf("fixture: the real early pass must not fail the attempt: %T", err)
	}
	if decision.Kind != request.AttemptContinue {
		t.Fatalf("fixture: this feature must never exclude a candidate: kind=%q", decision.Kind)
	}

	lateCall := contCanonicalCall(root)
	if err := components.hook.HandleRequestParts(
		aipworkspace.WithWorkspaceView(context.Background(),
			aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}),
		lateCall, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("fixture: the real late pass must not fail the request: %T", err)
	}

	finalizer := components.finalizer
	finalCall := toolcall.CompletedCall{
		ToolCallID: contFixtureCallID,
		ToolName:   contFixtureTool,
		ArgsJSON: []byte(`{"` + expPathField + `":"` + expEscaped(contAliasPath(mapping, contFixtureSuffix)) +
			`","` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`),
	}
	result, err := finalizer.Finalize(context.Background(), finalCall,
		aipapi.ToolDef{
			Name:       contFixtureTool,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField + `":{"type":"string"}}}`),
		},
		nil,
		toolcall.Meta{Workspace: aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}})
	if err != nil {
		t.Fatalf("fixture: the shipped expansion pass must report a decision, never an error: %T", err)
	}

	return contGenerationOutput{
		tag:       mapping.WorkspaceTag,
		alias:     alias,
		earlyArgs: contSelectedPath(t, earlyCall),
		lateArgs:  contSelectedPath(t, lateCall),
		finalArgs: string(result.ArgsJSON),
		finalCode: result.ReasonCode,
		finalKind: result.Action,
	}
}

// contDriveOneCall drives ONE published generation's three components over ONE ingress
// call and returns what the request side published.
//
// It is the same composition contDriveGeneration uses, factored out so a scenario can
// drive ONE generation instance across SEVERAL calls. That sharing is the point: a
// per-instance cached mapping would be invisible across separate generations, because
// every generation would legitimately hold its own, but it is exactly what a
// requirements.md 6.4 violation looks like in a deployment, where one generation
// serves a whole session including every continuation of it.
//
// The returned value is the selected path field the components published. It is
// compared, never formatted.
func contDriveOneCall(t *testing.T, components contBundleComponents, call *aipapi.Call, root string) (early, late string) {
	t.Helper()

	earlyCall := aipapi.CloneCall(*call)
	decision, err := components.transform.HandleAttempt(context.Background(), &earlyCall,
		request.AttemptMeta{Workspace: aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}},
		request.Services{})
	if err != nil {
		t.Fatalf("fixture: the real early pass must not fail the attempt: %T", err)
	}
	if decision.Kind != request.AttemptContinue {
		t.Fatalf("fixture: this feature must never exclude a candidate: kind=%q", decision.Kind)
	}

	lateCall := aipapi.CloneCall(*call)
	if err := components.hook.HandleRequestParts(
		aipworkspace.WithWorkspaceView(context.Background(),
			aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}),
		&lateCall, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("fixture: the real late pass must not fail the request: %T", err)
	}
	return contSelectedPath(t, &earlyCall), contSelectedPath(t, &lateCall)
}

// contExpandOneModelCall drives ONE published generation's expansion finalizer over one
// model-emitted alias and returns the published result.
//
// It exists beside contDriveOneCall for the same reason: the response side must be
// driven per call against the SAME generation instance, so a cached mapping on the
// finalizer would be observable.
func contExpandOneModelCall(t *testing.T, components contBundleComponents, aliasPath, root string) toolcall.Result {
	t.Helper()
	result, err := components.finalizer.Finalize(context.Background(),
		toolcall.CompletedCall{
			ToolCallID: contFixtureCallID,
			ToolName:   contFixtureTool,
			ArgsJSON: []byte(`{"` + expPathField + `":"` + expEscaped(aliasPath) +
				`","` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`),
		},
		aipapi.ToolDef{
			Name:       contFixtureTool,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField + `":{"type":"string"}}}`),
		},
		nil,
		toolcall.Meta{Workspace: aipworkspace.WorkspaceView{ID: "ws_fixture", ProjectRoot: root}})
	if err != nil {
		t.Fatalf("fixture: the shipped expansion pass must report a decision, never an error: %T", err)
	}
	return result
}

// contReleasedSelectedPath reads the one selected path field off a RELEASED client-facing
// argument document.
//
// It is a read, not a search: the fixture's payload-concept sibling deliberately spells
// the reserved alias and requirements.md 4.9 forbids expansion from touching it, so a
// whole-document substring search would report an expected occurrence as a defect. The
// value is returned for comparison only and is never formatted.
func contReleasedSelectedPath(t *testing.T, released string) string {
	t.Helper()
	if !json.Valid([]byte(released)) {
		t.Fatalf("fixture: the released argument document must be one complete JSON value: released=%d bytes", len(released))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(released), &fields); err != nil {
		t.Fatalf("fixture: the released argument document must be a JSON object: error_type=%T", err)
	}
	var selected string
	if json.Unmarshal(fields[expPathField], &selected) != nil {
		t.Fatalf("fixture: the released argument document must carry a readable selected path field: members=%d", len(fields))
	}
	return selected
}

// contCanonicalCall is one item-authoritative path-bearing tool call spelled against
// root, which is the shape a client replays and the shape the outbound passes rewrite.
func contCanonicalCall(root string) *aipapi.Call {
	history := expFixture{root: root, suffix: contFixtureSuffix}
	return &aipapi.Call{
		ID:    contFixtureCallID,
		Route: aipapi.RouteIntent{Selector: contContinuationSelector},
		Tools: []aipapi.ToolDef{{
			Name:       contFixtureTool,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField + `":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindToolCall, ID: contFixtureItemID, Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    contFixtureCallID,
				Name:      contFixtureTool,
				Arguments: json.RawMessage(history.modelArgsDocument()),
			},
		}},
	}
}

// contSelectedPath reads the ONE selected path field off a canonical call and fails if
// it is unreadable, so a component that published nothing cannot pass a later
// comparison against a component that published a different alias.
//
// Exactly one path-bearing item is required rather than "at least one": the whole point
// of comparing one alias across calls is that a component published exactly one value,
// and a call that happened to carry two would make the comparison ambiguous.
func contSelectedPath(t *testing.T, call *aipapi.Call) string {
	t.Helper()
	selected, found := contSelectedPaths(t, call)
	if !found {
		t.Fatal("fixture: the published argument document must carry a readable selected path field")
	}
	return selected
}

// contSelectedPaths counts the selected path fields a canonical call carries and returns
// the first one.
func contSelectedPaths(t *testing.T, call *aipapi.Call) (string, bool) {
	t.Helper()
	documents := 0
	for _, item := range call.Items {
		if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
			continue
		}
		documents++
		if documents > 1 {
			t.Fatalf("fixture: a canonical call must carry exactly ONE path-bearing tool call, or the alias comparison is ambiguous: documents=%d", documents)
		}
		if !json.Valid(item.ToolCall.Arguments) {
			t.Fatalf("fixture: the published argument document must be one complete JSON value: bytes=%d", len(item.ToolCall.Arguments))
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item.ToolCall.Arguments, &fields); err != nil {
			t.Fatalf("fixture: the published argument document must be a JSON object: error_type=%T", err)
		}
		var selected string
		if json.Unmarshal(fields[expPathField], &selected) != nil {
			t.Fatalf("fixture: the published argument document must carry a readable selected path field")
		}
		return selected, true
	}
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != aipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			documents++
			if documents > 1 {
				t.Fatalf("fixture: a canonical call must carry exactly ONE path-bearing tool call, or the alias comparison is ambiguous: documents=%d", documents)
			}
			if !json.Valid(part.Content) {
				t.Fatalf("fixture: the published argument document must be one complete JSON value: bytes=%d", len(part.Content))
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(part.Content, &fields); err != nil {
				t.Fatalf("fixture: the published argument document must be a JSON object: error_type=%T", err)
			}
			var selected string
			if json.Unmarshal(fields[expPathField], &selected) != nil {
				t.Fatalf("fixture: the published argument document must carry a readable selected path field")
			}
			return selected, true
		}
	}
	return "", false
}

// contRunTurn drives one full runtime turn for a scenario and returns the run's
// content-free record.
func contRunTurn(t *testing.T, scenario expScenario) expRunResult {
	t.Helper()
	if scenario.label == "" {
		t.Fatal("fixture: a scenario must carry a bounded, content-free label")
	}
	return expRun(t, scenario)
}

// contStructuralFiles is every PRODUCTION source of the feature tree, discovered by
// walking the feature directory rather than by a hand-maintained list.
//
// The discovery is what makes the structural guard non-vacuous in the second sense: a
// hand-maintained list would silently stop covering a new subpackage, and a
// subpackage is exactly where a per-root cache would be added.
func contStructuralFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(contFeatureDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the feature tree: %v", err)
	}
	sort.Strings(files)
	return files
}

// contAllowedStore is the ONE package-level map the feature legitimately owns.
//
// It is allowlisted by exact file and exact name rather than by pattern, and every
// structural property that makes it harmless is asserted rather than assumed:
//
//   - it is a SET (`map[string]struct{}`), not a dictionary of mappings, so it cannot
//     hold a derived workspace identity;
//   - its keys are JSON Schema keyword names, a fixed vocabulary with no root or tag in
//     it;
//   - it is READ ONLY: the guard proves no assignment to it exists anywhere in the
//     feature tree, so no request could ever add an entry to it.
//
// It is the schema-inference step's suppression vocabulary
// (schemainfer/schema.go:85-111): "this declared keyword means the node's shape is not
// provable". It has nothing to do with workspace identity, and the guard would be
// noise rather than protection if it fired on it - which is exactly why the
// allowlisting is narrow, exact, and property-checked instead of broad.
type contAllowedStore struct {
	file string
	name string
}

// contAllowedStores is the complete allowlist. A new package-level map is NOT added
// here; it fails the guard, and adding one requires saying why it cannot hold workspace
// identity.
var contAllowedStores = []contAllowedStore{
	{file: "schemainfer/schema.go", name: "ambiguousKeywords"},
	{file: "schemainfer/schema.go", name: "unevenArrayKeywords"},
}

// contDetection is the outcome of one scan: the names of the offending variables and
// the files they were found in.
type contDetection struct {
	files []string
	names []string
}

func (d contDetection) found() int { return len(d.names) }

// contForbidPriorRootStore is the DETECTOR both the negative and the positive
// assertion run, so the two cannot disagree about what counts as a store.
//
// It refuses two shapes and nothing else:
//
//  1. a package-level `var` whose declared type is a map. A prior-root dictionary has
//     to outlive a single call to be a dictionary at all, and the only place in Go
//     where state can outlive a call without being reachable from the caller is
//     package scope. That makes this check sound rather than a heuristic: a cache held
//     on a component would be a per-generation immutable field, which the field-type
//     assertion above already covers.
//
//  2. a package-level `var` of `sync.Map`, which is the only concurrency-safe map in
//     the standard library and the obvious way to smuggle one past a type check.
//
// It deliberately does NOT inspect function-local maps: every map in the feature tree
// is a per-call scratch structure built from a decoded document or a compiled profile
// list, none of which is keyed by a root or a tag, and flagging those would make the
// guard noise rather than informative.
func contForbidPriorRootStore(t *testing.T, sources map[string]string) contDetection {
	t.Helper()
	var out contDetection
	fset := token.NewFileSet()
	for _, name := range sortedKeys(sources) {
		file, err := parser.ParseFile(fset, name, sources[name], parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		allowedHere := map[string]bool{}
		slashed := filepath.ToSlash(name)
		for _, allowed := range contAllowedStores {
			// The allowlist names a path RELATIVE to the feature tree, so a scanned path
			// matches when it ends with that relative path. The exact spelling is
			// accepted too, so the synthetic controls below can name a file the same way
			// the real walk does without the matcher having two behaviours.
			if slashed == allowed.file || strings.HasSuffix(slashed, "/"+allowed.file) {
				allowedHere[allowed.name] = true
			}
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if !contIsSyncMap(value.Type) && !contDeclaresMap(value) {
					continue
				}
				for _, declared := range declaredNames(value) {
					if allowedHere[declared] {
						continue
					}
					out.files = append(out.files, name)
					out.names = append(out.names, declared)
				}
			}
		}
	}
	return out
}

// contAssertAllowedStoresAreInert proves every allowlisted package-level map really is
// the shape the allowlist claims it is, so the allowlist cannot rot into a loophole:
//
//   - it is declared as a SET (`map[string]struct{}`), so it cannot hold a derived
//     workspace identity;
//   - NO assignment to it exists anywhere in the scanned tree, so no request could add
//     an entry to it.
//
// A declaration that stops being a set, or that gains a write, fails here even though
// the structural guard would still skip it.
func contAssertAllowedStoresAreInert(t *testing.T, sources map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, allowed := range contAllowedStores {
		spelling := path.Base(allowed.file)
		found := 0
		for _, name := range sortedKeys(sources) {
			if path.Base(name) != spelling {
				continue
			}
			file, err := parser.ParseFile(fset, name, sources[name], parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok || len(value.Names) != 1 || value.Names[0].Name != allowed.name {
						continue
					}
					found++
					if allowed.name == "injectedPriorRoots" {
						continue
					}
					if !contIsStringKeyedSet(value) {
						t.Fatalf("requirements.md 6.1/6.6 - allowlisted package-level map %q must stay a STRING-KEYED SET, so it cannot hold a workspace identity",
							allowed.name)
					}
				}
			}
		}
		if found != 1 {
			t.Fatalf("requirements.md 6.1/6.6 - allowlisted package-level map %q must be declared exactly once in %s, so the allowlist cannot drift onto something else: declarations=%d",
				allowed.name, allowed.file, found)
		}
	}
	// No write to an allowlisted name exists anywhere in the tree. The scan is over
	// every production source, and it looks at ASSIGNMENTS only, so an ordinary
	// membership test (`_, ok := m[k]`) is not mistaken for a mutation.
	for _, name := range sortedKeys(sources) {
		file, err := parser.ParseFile(fset, name, sources[name], parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				for _, allowed := range contAllowedStores {
					if ident.Name == allowed.name {
						t.Errorf("requirements.md 6.1/6.6 - allowlisted package-level map %q is assigned in %s; a read-only vocabulary set cannot grow at request time",
							allowed.name, name)
					}
				}
			}
			return true
		})
	}
}

// contIsStringKeyedSet reports whether a value spec's EFFECTIVE type is exactly
// `map[string]T` for some T, which is the shape a membership vocabulary has. The VALUE
// type is deliberately not constrained: the point is that the map cannot hold a
// mapping.
//
// The effective type is resolved from the initializer when the spec infers it, so
// `var x = map[string]struct{}{...}` - which is how the allowlisted tables are
// actually written - is checked rather than skipped.
func contIsStringKeyedSet(value *ast.ValueSpec) bool {
	if value.Type != nil {
		return contIsMapKeyedByString(value.Type)
	}
	for _, expr := range value.Values {
		switch typed := expr.(type) {
		case *ast.CompositeLit:
			if contIsMapKeyedByString(typed.Type) {
				return true
			}
		case *ast.UnaryExpr:
			if typed.Op == token.AND && contIsMapKeyedByString(typed.X) {
				return true
			}
		default:
		}
	}
	return false
}

// contIsMapKeyedByString reports whether a type expression is a map whose key is the
// predeclared string type.
func contIsMapKeyedByString(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.MapType:
		ident, ok := typed.Key.(*ast.Ident)
		return ok && ident.Name == "string"
	case *ast.StarExpr:
		return contIsMapKeyedByString(typed.X)
	case *ast.ParenExpr:
		return contIsMapKeyedByString(typed.X)
	default:
		return false
	}
}

// contIsMapType reports whether a declared type is a map type, looking through the
// single pointer indirection a package-level cache variable would plausibly use.
func contIsMapType(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.MapType:
		return true
	case *ast.StarExpr:
		return contIsMapType(typed.X)
	case *ast.ParenExpr:
		return contIsMapType(typed.X)
	default:
		return false
	}
}

// contDeclaresMap reports whether a package-level value spec establishes a map.
//
// Two spellings exist and both are checked, because the second is what anyone writes
// naturally:
//
//	explicit type   var priorRoots map[string]string
//	inferred type   var priorRoots = map[string]string{}
//	address of one  var priorRoots = &map[string]string{}
//
// A spec that declares an explicit non-map type is NOT followed into its initializer:
// `var x []string = ...` is a slice, not a map, and reading past the declared type
// would report shapes that are not maps at all.
func contDeclaresMap(value *ast.ValueSpec) bool {
	if value.Type != nil {
		return contIsMapType(value.Type)
	}
	for _, expr := range value.Values {
		if contIsMapType(expr) {
			return true
		}
		switch typed := expr.(type) {
		case *ast.CompositeLit:
			if contIsMapType(typed.Type) {
				return true
			}
		case *ast.UnaryExpr:
			if typed.Op == token.AND && contIsMapType(typed.X) {
				return true
			}
		default:
		}
	}
	return false
}

// contIsSyncMap reports whether a declared type spells the sync.Map type, by name and
// by selector. The import is not resolved: a production source that reached for
// sync.Map would name it, and requiring the import to be present as well would only
// hide the finding behind a second condition.
func contIsSyncMap(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name == "Map" && typed.Obj == nil
	case *ast.SelectorExpr:
		ident, ok := typed.X.(*ast.Ident)
		return ok && ident.Name == "sync" && typed.Sel.Name == "Map"
	case *ast.StarExpr:
		return contIsSyncMap(typed.X)
	case *ast.ParenExpr:
		return contIsSyncMap(typed.X)
	default:
		return false
	}
}

// declaredNames renders the names a value spec declares.
func declaredNames(value *ast.ValueSpec) []string {
	if len(value.Names) == 0 {
		return []string{"<anonymous>"}
	}
	names := make([]string, 0, len(value.Names))
	for _, name := range value.Names {
		names = append(names, name.Name)
	}
	return names
}

// sortedKeys returns a map's keys in a stable order, so a scan's findings do not
// depend on Go's randomized map iteration.
func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestStreamToolCall_OneRootMintsOneAliasAcrossRecreationReloadAndRestart is
// requirements.md 6.1, 6.2, and 6.6 at the strongest level the shipped composition
// allows: the SAME published generation, rebuilt from scratch.
//
// Three independent observations of the identity, each made with no component from a
// previous observation reachable:
//
//   - COMPONENT RECREATION. contPublishedComponents decodes the operator subtree and
//     builds the bundle afresh, and every previous generation's components are
//     dropped before the next call. The tag and alias must be byte-identical.
//
//   - GENERATION RELOAD WITH A DIFFERENT SUBTREE IN BETWEEN. A generation compiled
//     with a different declared completeness bound sits between the two observations,
//     so the equality cannot be explained by two identical objects. The bound is a
//     generation-scoped choice the alias must NOT depend on.
//
//   - PROCESS RESTART. The identity is re-derived in a genuinely separate OS process
//     started from this test binary, which shares no address space, no heap, and no
//     package-level variable with the test. Its output must equal the in-process one.
//
// The positive control matters: the same components must EXPAND under the pinned root
// on every observation, so an equality of two empty or refused outputs could not pass.
func TestStreamToolCall_OneRootMintsOneAliasAcrossRecreationReloadAndRestart(t *testing.T) {
	t.Parallel()

	const root = twoPassRealRoot
	const subtree = "enabled: true\nmode: rewrite\n"

	// OBSERVATION 1. Every component is built here and released below, before the
	// next generation exists. Only the MEASURED OUTCOME and the component IDENTITIES
	// are retained, because those are what the comparisons below read; no component
	// instance is.
	first := contPublishedComponents(t, subtree)
	firstOut := contDriveGeneration(t, first, root)
	firstBound := first.bufferSpec.MaxArgsBytes
	// The identities are retained as interface VALUES holding the same pointers, which
	// is enough to compare address identity later while keeping the components
	// themselves unreachable for any behavioural call. They are a local, not a
	// package-level store: this file forbids exactly that shape in the feature.
	firstIdentity := first

	// OBSERVATION 2, an INTERLUDE. A generation compiled with a DIFFERENT declared
	// completeness bound is published and driven in between, so the two observations
	// of the same subtree cannot be two references to one compiled resolution, and so
	// a workspace identity that secretly depended on generation-scoped configuration
	// would move.
	interlude := contPublishedComponents(t, contReloadSubtree)
	interludeOut := contDriveGeneration(t, interlude, root)
	if interlude.bufferSpec.MaxArgsBytes != contReloadBound {
		t.Fatalf("fixture: the interlude generation must carry the operator's declared bound: max_args_bytes=%d want=%d",
			interlude.bufferSpec.MaxArgsBytes, contReloadBound)
	}
	if interlude.bufferSpec.MaxArgsBytes == firstBound {
		t.Fatal("fixture: the interlude generation must declare a DIFFERENT bound than the first observation's, or it is the same generation")
	}

	// The interlude really is a DIFFERENT set of objects, not the same three
	// republished. Comparing the component identities is the direct proof that
	// "recreation" happened at all; without it, three identical equality assertions
	// could be satisfied by one generation driven three times.
	contAssertDistinctComponents(t, "first", first, "interlude", interlude)

	// Every component of observations 1 and 2 is released here. Nothing reachable from
	// either survives into observation 3, so the equality below is a restart
	// equivalence rather than a repeat of one object.
	first, interlude = contBundleComponents{}, contBundleComponents{}

	second := contPublishedComponents(t, subtree)
	secondOut := contDriveGeneration(t, second, root)
	if second.bufferSpec.MaxArgsBytes == contReloadBound {
		t.Fatalf("fixture: the reloaded generation must NOT carry the interlude's declared bound, or the interlude was not a different generation: max_args_bytes=%d",
			second.bufferSpec.MaxArgsBytes)
	}
	// The first generation's components were released above, so only their RETAINED
	// identity is available to compare against.
	contAssertDistinctComponents(t, "reloaded", second, "first", firstIdentity)

	// requirements.md 6.2 / design.md "Continuity and State": same root and flavor
	// produces the same workspace tag and virtual root after reload. Lengths and
	// equality only; neither value is ever formatted.
	if firstOut.tag != secondOut.tag {
		t.Fatalf("requirements.md 6.2 - one root must mint one workspace tag across component recreation: first_tag_bytes=%d second_tag_bytes=%d tags_equal=%t",
			len(firstOut.tag), len(secondOut.tag), firstOut.tag == secondOut.tag)
	}
	if firstOut.alias != secondOut.alias {
		t.Fatalf("requirements.md 6.2 - one root must mint one alias across component recreation: first_alias_bytes=%d second_alias_bytes=%d aliases_equal=%t",
			len(firstOut.alias), len(secondOut.alias), firstOut.alias == secondOut.alias)
	}
	if firstOut.earlyArgs != secondOut.earlyArgs || firstOut.lateArgs != secondOut.lateArgs {
		t.Fatalf("requirements.md 6.2/5.6 - both outbound passes must publish the identical alias after reload: early_bytes=%d late_bytes=%d early_equal=%t late_equal=%t",
			len(firstOut.earlyArgs), len(secondOut.lateArgs),
			firstOut.earlyArgs == secondOut.earlyArgs, firstOut.lateArgs == secondOut.lateArgs)
	}
	// The third observation: a different declared bound changed the finalizer's
	// completeness requirement and MUST NOT change the alias.
	if interludeOut.tag != firstOut.tag || interludeOut.alias != firstOut.alias {
		t.Fatalf("requirements.md 6.2/6.6 - the workspace identity is derived from the project root alone, never from generation-scoped configuration: interlude_tag_bytes=%d interlude_alias_bytes=%d",
			len(interludeOut.tag), len(interludeOut.alias))
	}

	// Non-vacuity: every observation must have EXPANDED the model-emitted alias against
	// the pinned root. Two identical refused or empty outputs would otherwise satisfy
	// every equality above.
	for label, out := range map[string]contGenerationOutput{
		"first": firstOut, "interlude": interludeOut, "second": secondOut,
	} {
		if out.finalKind != toolcall.ActionRewrite {
			t.Fatalf("fixture: the %s observation must expand the alias: action=%d reason=%q", label, int(out.finalKind), out.finalCode)
		}
		if out.finalArgs == "" {
			t.Fatalf("fixture: the %s observation must publish an expanded document", label)
		}
		if strings.Contains(out.finalArgs, expReservedMarker) {
			t.Fatalf("fixture: the %s observation must publish an EXPANDED document, not an alias: published=%d bytes",
				label, len(out.finalArgs))
		}
		if !strings.Contains(out.finalArgs, root) {
			t.Fatalf("fixture: the %s observation must publish the pinned root's own bytes: published=%d bytes",
				label, len(out.finalArgs))
		}
	}
}

// TestStreamToolCall_RestartedProcessMintsTheSameAliasWithNoCarriedState is
// requirements.md 6.2's process-restart clause.
//
// The helper runs in a FRESH OS PROCESS started from this test binary. It shares no
// address space, no heap, and no initialized package-level variable with the test, so
// the identity it mints is derived from the root bytes alone. Any mapping that had to
// be carried across a restart - a per-session dictionary, a package-level cache, a
// memoized tag - would be absent in that process and the tag would differ.
func TestStreamToolCall_RestartedProcessMintsTheSameAliasWithNoCarriedState(t *testing.T) {
	t.Parallel()

	inProcess := expAliasOf(t, twoPassRealRoot)
	inProcessMapping, reason := pathvirtualization.DeriveMapping(twoPassRealRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("fixture: the pinned root must derive an active mapping; its refusal reason is bounded and content-free")
	}

	// Two independent processes. Two rather than one because the property being proved
	// is that the identity is STABLE, and one child could only agree with this process
	// by accident.
	children := make([]string, 2)
	for i := range children {
		children[i] = contRunRestartHelper(t)
	}

	for i, child := range children {
		if child != inProcess {
			t.Fatalf("requirements.md 6.2 - a restarted process must mint the identical alias: child=%d alias_bytes=%d equal=%t",
				i, len(child), child == inProcess)
		}
		if children[0] != children[1] {
			t.Fatalf("requirements.md 6.2 - two restarted processes must mint the identical alias: first_bytes=%d second_bytes=%d",
				len(children[0]), len(children[1]))
		}
	}
	// The frozen tag length is checked in-process as well, so a drift that changed
	// both sides identically would still be caught here.
	if len(inProcessMapping.WorkspaceTag) != contFrozenTagChars {
		t.Fatalf("fixture: the frozen V1 tag must be exactly %d base32 characters: got %d",
			contFrozenTagChars, len(inProcessMapping.WorkspaceTag))
	}
}

// contRunRestartHelper runs the restart helper in a separate OS process and returns the
// alias it printed.
//
// The helper's own test function re-executes THIS BINARY with an environment marker,
// which is the established idiom in this repository for a genuine process boundary
// (internal/infra/runtimebundle/process_state_test.go:45-74). Nothing about the
// feature's state can survive that boundary, so the alias it reports is derived from
// the root bytes alone.
func contRunRestartHelper(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contHelperTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^"+contHelperTestName+"$", "-test.v=false")
	command.Env = append(os.Environ(),
		contHelperEnvMarker+"=1",
		contHelperEnvRoot+"="+twoPassRealRoot,
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("the restart helper process must succeed: %T", err)
	}
	alias := strings.TrimSpace(string(output))
	if alias == "" {
		t.Fatal("the restart helper process printed no alias")
	}
	return alias
}

// TestStreamToolCall_RestartHelperProcessDerivesTheAliasFromTheRootBytesAlone is the
// helper TestStreamToolCall_RestartedProcessMintsTheSameAliasWithNoCarriedState starts
// in a fresh OS process.
//
// It is a no-op in a normal run: the environment marker is absent, so it returns
// immediately and contributes nothing to the suite's cost.
func TestStreamToolCall_RestartHelperProcessDerivesTheAliasFromTheRootBytesAlone(t *testing.T) {
	if os.Getenv(contHelperEnvMarker) != "1" {
		return
	}
	root := os.Getenv(contHelperEnvRoot)
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		// The exit code is the only signal that reaches the parent, and it is bounded.
		os.Exit(contHelperExitUnusableRoot)
	}
	if _, err := os.Stdout.WriteString(mapping.VirtualRoot); err != nil {
		os.Exit(contHelperExitWriteFailed)
	}
	os.Exit(0)
}

// TestStreamToolCall_AProviderRetainedAliasExpandsToTheSameWorkspaceOnAContinuedTurn
// is requirements.md 6.4: where provider-side continuation retains prior model-visible
// history, a continued turn's tool call must expand against the SAME workspace-bound
// alias.
//
// The continuation shape is the canonical one, asserted by
// contAssertCanonicalContinuationShape before anything runs: an item-authoritative
// call whose item slice is non-nil and intentionally EMPTY, carrying a continuation
// parent (pkg/aipapi/call.go:72-75 and :107; produced at
// internal/plugins/protocols/openresponses/decode_request.go:155-163 and :235).
//
// Two turns of the SAME session and the SAME authoritative root are driven through
// the real runtime:
//
//   - a FULL-HISTORY turn, where the client replays the path-bearing tool call;
//   - a CONTINUATION turn, where the ingress carries no path history at all because
//     the provider retains it, and the model nevertheless emits a tool call carrying
//     the alias it saw in the retained turn.
//
// The property is that the alias the continued turn expands is byte-identical to the
// one the full-history turn minted, and both reach the client as the same real path.
// A continuation path that re-derived differently from the replay path - the exact
// asymmetry this file exists to catch - would produce a different document and fail
// the byte comparison.
func TestStreamToolCall_AProviderRetainedAliasExpandsToTheSameWorkspaceOnAContinuedTurn(t *testing.T) {
	t.Parallel()

	realRoot := twoPassRealRoot
	alias := expAliasOf(t, realRoot)

	// The fixture must really be the canonical continuation shape.
	contAssertCanonicalContinuationShape(t, contContinuationCall(realRoot, contContinuationParent))

	// TURN 1: a full-history replay. The real outbound passes mint the alias and the
	// backend receives it; the model emits it back and the client receives the real
	// path.
	first := contRunTurn(t, expScenario{
		label:             "full_history_turn",
		aliasRoot:         alias,
		expansionDecides:  true,
		observersExpected: true,
	})
	if first.openMeasure.aliasHits != 1 || first.openMeasure.realHits != 0 {
		t.Fatalf("fixture: the full-history turn must virtualize its replayed history before Backend.Open: %s alias_hits=%d real_hits=%d",
			first.stages, first.openMeasure.aliasHits, first.openMeasure.realHits)
	}
	if first.previousResponseID != "" {
		t.Fatalf("fixture: the full-history turn must carry NO continuation parent: parent_bytes=%d", len(first.previousResponseID))
	}

	// TURN 2: the continued turn. The ingress carries the MATERIALIZED trajectory the
	// continuation resolver produced - the retained model-visible history plus this
	// turn's new input - with the continuation parent still attached, which is the
	// shape the runtime actually receives (pkg/aipsdk/continuation/materialize.go:56-63).
	second := contRunTurn(t, expScenario{
		label:             "provider_continued_turn",
		aliasRoot:         alias,
		expansionDecides:  true,
		observersExpected: true,
		ingress: func(*testing.T) *aipapi.Call {
			return contContinuationCall(realRoot, contContinuationParent)
		},
	})
	if second.previousResponseID != contContinuationParent {
		t.Fatalf("requirements.md 6.4 - the continuation parent must reach the provider unchanged: parent_bytes=%d matches=%t",
			len(second.previousResponseID), second.previousResponseID == contContinuationParent)
	}
	// The continued turn's RETAINED history must reach the provider virtualized, under
	// the SAME alias the full-history turn used. This is the request-side half of
	// requirement 6.4: the provider is holding one alias for this workspace, not two.
	if second.openMeasure.aliasHits != 1 || second.openMeasure.realHits != 0 {
		t.Fatalf("requirements.md 6.4/5.2 - the continued turn must virtualize its retained history before Backend.Open: %s alias_hits=%d real_hits=%d",
			second.stages, second.openMeasure.aliasHits, second.openMeasure.realHits)
	}
	if second.openMeasure.documents != 1 || second.openMeasure.selectedFields != 1 {
		t.Fatalf("fixture: the continued turn must carry exactly one retained path-bearing tool call: %s documents=%d selected_fields=%d",
			second.stages, second.openMeasure.documents, second.openMeasure.selectedFields)
	}

	// The continuity property. Both turns expanded the same alias against the same
	// authoritative root, so both released the same complete expanded document, byte
	// for byte.
	if first.released != second.released {
		t.Fatalf("requirements.md 6.4 - a continued turn must expand to the same workspace-bound alias: released_bytes=%d continued_bytes=%d equal=%t valid_json=%t alias_released=%t",
			len(first.released), len(second.released), first.released == second.released,
			json.Valid([]byte(second.released)), strings.Contains(second.released, alias))
	}
	if second.released == "" {
		t.Fatal("fixture: the continued turn must release an expanded document; two empty releases would satisfy the equality above")
	}

	// The property at the level where it is sharpest: ONE published generation, driven
	// across a full replay and TWO DIFFERENT continuations of the same workspace. In a
	// deployment one generation serves a whole session, so a mapping cached on the
	// component would be invisible across separate generations - every generation would
	// legitimately hold its own - and would show up HERE as one alias for the replay and
	// another for the continuations. All three request-side publications and both
	// response-side expansions must be byte-identical.
	generation := contPublishedComponents(t, "enabled: true\nmode: rewrite\n")
	contReplayAlias, replayLate := contDriveOneCall(t, generation, contCanonicalCall(realRoot), realRoot)
	contFirstAlias, firstLate := contDriveOneCall(t, generation,
		contContinuationCall(realRoot, contContinuationParent), realRoot)
	contSecondAlias, secondLate := contDriveOneCall(t, generation,
		contContinuationCall(realRoot, contContinuationSecondParent), realRoot)
	// The published value is the alias ROOT plus the fixture's path-bearing suffix, so
	// the expected bytes are computed rather than the bare alias.
	wantPublished := alias + contFixtureSuffix
	for label, published := range map[string][2]string{
		"replay":              {contReplayAlias, replayLate},
		"first_continuation":  {contFirstAlias, firstLate},
		"second_continuation": {contSecondAlias, secondLate},
	} {
		if published[0] != wantPublished || published[1] != wantPublished {
			t.Fatalf("requirements.md 6.4 - one generation must publish ONE alias across a replay and every continuation of the same workspace: %s early_bytes=%d late_bytes=%d early_equal=%t late_equal=%t",
				label, len(published[0]), len(published[1]),
				published[0] == wantPublished, published[1] == wantPublished)
		}
	}
	// And the response side, on the SAME generation instance, once per continuation. The
	// model emits a DIFFERENT suffix on each continued turn - it is naming a new file
	// under the alias it retained - so the two expansions must differ in exactly that
	// suffix and agree on everything before it. Asserting the two published documents
	// are byte-identical would therefore be wrong; what must hold is that both resolve
	// the SAME alias root to the SAME real root.
	mapping := contAssertActiveMapping(t, realRoot, pathvirtualization.FlavorPOSIX)
	for _, suffix := range []string{contFixtureSuffix, contFixtureSecondSuffix} {
		result := contExpandOneModelCall(t, generation, contAliasPath(mapping, suffix), realRoot)
		if result.Action != toolcall.ActionRewrite {
			t.Fatalf("requirements.md 6.4 - the retained alias must expand on every continuation: action=%d reason=%q",
				int(result.Action), result.ReasonCode)
		}
		expanded := contReleasedSelectedPath(t, string(result.ArgsJSON))
		// The EXPECTED expansion is computed from the mapping, not searched for. It
		// spells the real root and the fixture's own separator, so a byte comparison is
		// the whole claim: the same alias root resolved to the same real root, with the
		// suffix bytes requirement 1.6 preserves intact.
		wantExpanded := expFixture{root: mapping.RealRoot, suffix: suffix}.joined()
		if expanded != wantExpanded {
			t.Fatalf("requirements.md 4.1/6.4 - the retained alias must resolve to the same real root on every continuation: published_bytes=%d expected_bytes=%d equal=%t carries_alias=%t",
				len(expanded), len(wantExpanded), expanded == wantExpanded,
				strings.Contains(expanded, expReservedMarker))
		}
	}
	// The SELECTED path field must be the expanded value. It is read off the released
	// document rather than searched for, because the fixture's payload-concept sibling
	// deliberately spells the alias and requirements.md 4.9 forbids expansion from
	// touching it - so an alias occurrence elsewhere in the release is expected and a
	// whole-document search would flag it.
	selected := contReleasedSelectedPath(t, second.released)
	if selected == "" || !strings.Contains(selected, realRoot) || strings.Contains(selected, alias) {
		t.Fatalf("requirements.md 4.1/6.4 - the continued turn must release the expanded selected path field: released=%d bytes selected_bytes=%d carries_root=%t carries_alias=%t",
			len(second.released), len(selected),
			strings.Contains(selected, realRoot), strings.Contains(selected, alias))
	}
	// The model-emitted alias the continued turn resolved is the SAME one the
	// full-history turn minted: it is the alias derived from the pinned root, and it is
	// what both turns' shipped passes were shown.
	if !strings.Contains(string(first.expSeen), alias) || !strings.Contains(string(second.expSeen), alias) {
		t.Fatalf("requirements.md 6.4 - both turns' shipped pass must have been shown the retained alias: first_seen=%d bytes second_seen=%d bytes alias_in_first=%t alias_in_second=%t",
			len(first.expSeen), len(second.expSeen),
			strings.Contains(string(first.expSeen), alias), strings.Contains(string(second.expSeen), alias))
	}
	// The request side of the same property: BOTH turns handed the provider the BYTE-IDENTICAL
	// alias for the retained history. This is a byte comparison, not a count: a
	// continuation path that re-derived a DIFFERENT alias from the replay path - the exact
	// asymmetry this file exists to catch - would still register one alias hit on each
	// turn, so only the bytes separate them.
	if first.openSelected != second.openSelected {
		t.Fatalf("requirements.md 6.4 - both turns must hand the provider ONE byte-identical alias for this workspace: first_bytes=%d continued_bytes=%d equal=%t",
			len(first.openSelected), len(second.openSelected), first.openSelected == second.openSelected)
	}
	if first.openSelected == "" {
		t.Fatal("fixture: both turns must hand the provider a selected path value; two empty values would satisfy the equality above")
	}
	for label, run := range map[string]expRunResult{"first": first, "continued": second} {
		if run.expCalls != 1 || run.expResult.Action != toolcall.ActionRewrite {
			t.Fatalf("fixture: the %s turn's shipped pass must have expanded exactly one call: invocations=%d action=%d",
				label, run.expCalls, int(run.expResult.Action))
		}
		// The SELECTED field is read, not searched: the payload-concept sibling
		// legitimately keeps its alias bytes (requirement 4.9), so a whole-document
		// search would be the wrong oracle.
		if !strings.Contains(contReleasedSelectedPath(t, run.released), realRoot) {
			t.Fatalf("fixture: the %s turn must release the pinned root's own bytes in the selected field: released=%d bytes",
				label, len(run.released))
		}
	}
}

// TestStreamToolCall_APriorRootAliasIsRefusedAndNeverReboundAfterTheRootChanges is
// requirements.md 6.5 and design.md "Reserved-alias recognition and stale-root rule"
// steps 4-6, end to end through the real runtime.
//
// The fixture per flavor: alias A is derived under root A, the authoritative root then
// becomes root B, and the model's tool call carries alias A. Three things must hold,
// and they are proved by THREE DIFFERENT oracles because any one of them alone is
// satisfiable by a wrong implementation:
//
//   - the SHIPPED PASS refused with the bounded workspace_mismatch reason, so the
//     rejection is the feature's own decision rather than an incidental failure;
//   - NOTHING reached the client: no tool lifecycle, no argument bytes, no document,
//     which is requirements.md 4.4/8.3's release rule;
//   - the REBINDING oracle is clean: no released selected path field carries root B's
//     bytes, and none carries root A's tag. This is the assertion a
//     stale-alias-that-was-expanded-instead-of-refused would fail while every other
//     zero still held.
//
// And root B must derive a DIFFERENT tag in the same fixture, asserted before the run,
// so the refusal is not accidental: with an equal tag the alias would name the current
// mapping and there would be nothing stale about it.
//
// The ingress of every run spells its replayed history against root B, the CURRENT
// authoritative root, so root B's mapping is active, resolvable, and exercised in the
// very same turn. That makes the refusal ALIAS-SPECIFIC: the same component, the same
// turn, and the same policy expand root B's alias while refusing root A's.
func TestStreamToolCall_APriorRootAliasIsRefusedAndNeverReboundAfterTheRootChanges(t *testing.T) {
	t.Parallel()

	for _, flavor := range contFlavors() {
		t.Run(flavor.name, func(t *testing.T) {
			t.Parallel()

			prior := contAssertActiveMapping(t, flavor.rootA, flavor.wantFlavor)
			current := contAssertActiveMapping(t, flavor.rootB, flavor.wantFlavor)

			// requirements.md 6.5, first clause: the new root derives a different tag.
			// Without this the alias below would name the CURRENT mapping and there
			// would be no stale workspace to reject, so the whole case would be
			// vacuous. Only lengths and the equality verdict are reported.
			if prior.WorkspaceTag == current.WorkspaceTag {
				t.Fatalf("fixture: the changed root must derive a DIFFERENT workspace tag, or there is no stale alias: prior_tag_bytes=%d current_tag_bytes=%d",
					len(prior.WorkspaceTag), len(current.WorkspaceTag))
			}
			if prior.VirtualRoot == current.VirtualRoot {
				t.Fatalf("fixture: the changed root must derive a DIFFERENT alias root, or nothing is stale")
			}
			// The derived alias root must end with ITS OWN flavor's separator. That is
			// what makes the case a distinct lexical situation rather than the same one
			// re-spelled: a POSIX alias ends with `/` and every Windows flavor with `\`,
			// so each row of this matrix exercises a different alias grammar downstream.
			if !strings.HasSuffix(prior.VirtualRoot, flavor.separator) ||
				!strings.HasSuffix(current.VirtualRoot, flavor.separator) {
				t.Fatalf("fixture: each flavor's alias root must end with that flavor's own separator: prior_suffix_ok=%t current_suffix_ok=%t",
					strings.HasSuffix(prior.VirtualRoot, flavor.separator),
					strings.HasSuffix(current.VirtualRoot, flavor.separator))
			}

			// The stale alias is minted by the production mapper under root A, never
			// hand-written, so it is a real V1 alias and not a lookalike.
			staleAlias := contAliasPath(prior, expModelSuffix)
			if !strings.Contains(staleAlias, expReservedMarker) {
				t.Fatal("fixture: the stale value must be a well-formed V1 reserved alias, or the case would test the malformed branch instead")
			}

			// The lexical core, before anything else runs. This is design.md's
			// "Reserved-alias recognition and stale-root rule" steps 3 and 6 in their
			// lowest form: the NEW mapping refuses the OLD alias, returns NO path value
			// at all, and the OLD mapping still resolves its own alias. A stale-root
			// dictionary that had recorded root A would make the second half fail,
			// because the new mapping would then answer with root A's alias.
			rebound, coreResult := current.ExpandPath(staleAlias)
			if coreResult != pathvirtualization.ExpandResultWorkspaceMismatch || rebound != "" {
				t.Fatalf("requirements.md 6.5 - the new mapping must refuse the prior alias and return no value: result=%v returned_bytes=%d",
					coreResult, len(rebound))
			}
			if strings.Contains(rebound, flavor.rootB) || strings.Contains(rebound, expReservedMarker) {
				t.Fatalf("requirements.md 6.5 - the refused alias must not be rebound or handed back: returned_bytes=%d", len(rebound))
			}
			ownStillWorks, ownResult := prior.ExpandPath(staleAlias)
			if ownResult != pathvirtualization.ExpandResultExpanded || ownStillWorks == "" {
				t.Fatalf("fixture: the prior root's own mapping must still expand its own alias: result=%v returned_bytes=%d",
					ownResult, len(ownStillWorks))
			}

			// POSITIVE CONTROL: root B's OWN alias expands in this exact harness. It is
			// what makes the refusal below alias-specific rather than a blanket rule
			// over every alias.
			control := contRunTurn(t, expScenario{
				label:             "current_root_alias_expands",
				aliasRoot:         current.VirtualRoot,
				expansionDecides:  true,
				observersExpected: true,
				workspaceRoot:     flavor.rootB,
			})
			if control.expResult.Action != toolcall.ActionRewrite {
				t.Fatalf("fixture: the CURRENT root's own alias must expand in this harness: action=%d reason=%q",
					int(control.expResult.Action), control.expResult.ReasonCode)
			}
			// The SELECTED field is read rather than searched for. A Windows root spells
			// backslashes, which the released JSON document escapes, so a substring search
			// over the raw release would never find the root's own bytes even when the
			// expansion is exactly right.
			controlSelected := contReleasedSelectedPath(t, control.released)
			if !strings.Contains(controlSelected, flavor.rootB) {
				t.Fatalf("fixture: the positive control must release the current root's bytes in the selected field: released=%d bytes selected_bytes=%d carries_current_root=%t",
					len(control.released), len(controlSelected), strings.Contains(controlSelected, flavor.rootB))
			}

			// THE CASE. The authoritative root is root B; the model emits alias A.
			refusal := expRefusalCase{
				label:               "prior_root_alias_after_the_root_changed",
				aliasRoot:           staleAlias,
				workspaceRoot:       flavor.rootB,
				newRoot:             flavor.rootB,
				staleTag:            prior.WorkspaceTag,
				wantPassInvocations: 1,
				wantPassReason:      expansion.ReasonWorkspaceMismatch,
				wantRejectError:     true,
			}
			result, scan := expRunRefusal(t, refusal)
			if result.realRoot != flavor.rootB {
				t.Fatalf("fixture: the run must pin the CHANGED root: pinned_bytes=%d changed_root_bytes=%d equal=%t",
					len(result.realRoot), len(flavor.rootB), result.realRoot == flavor.rootB)
			}
			if result.alias != current.VirtualRoot {
				t.Fatalf("fixture: the run must mint the CHANGED root's alias: minted_bytes=%d changed_alias_bytes=%d equal=%t",
					len(result.alias), len(current.VirtualRoot), result.alias == current.VirtualRoot)
			}
			// The reason the shipped pass gave. design.md step 8 and requirements.md
			// 6.5 name exactly one bounded code for this shape, and it is distinct
			// from the malformed-alias code, so the two refusals cannot be one rule.
			if len(result.expReports) != 1 || result.expReports[0].Outcome != expansion.OutcomeRejected {
				t.Fatalf("requirements.md 6.5 - the shipped pass must report the content-free rejected outcome: reports=%d",
					len(result.expReports))
			}
			if result.expReports[0].Reason != expansion.ReasonWorkspaceMismatch {
				t.Fatalf("requirements.md 6.5 - the shipped pass must report the stale-workspace reason: got=%q want=%q",
					result.expReports[0].Reason, expansion.ReasonWorkspaceMismatch)
			}
			// The whole-stream and rebinding oracles, both inside the shared assertion.
			assertNoAliasReachedTheClient(t, refusal, result, scan,
				expFixture{root: staleAlias, suffix: expModelSuffix})
		})
	}
}

// TestStreamToolCall_NoPriorRootDictionaryExists is requirements.md 6.1 and 6.6, and
// design.md "Continuity and State"'s closing sentence: "no prior-root dictionary is
// needed to identify a stale alias because the stale tag is carried in the alias
// itself."
//
// THREE independent oracles, because the property is an absence and an absence proved
// one way is weak:
//
//  1. BEHAVIORAL, ORDER INDEPENDENT. Deriving root A, then root B, then root A again,
//     yields the identical tag each time. A store that remembered the FIRST mapping
//     would return it for the third call.
//
//  2. BEHAVIORAL, THE MAPPING VALUE CARRIES NO STORE. pathvirtualization.Mapping is
//     reflected over field by field: exactly the four declared scalar/identifier
//     fields, no map, slice, pointer, channel, or function. A mapping that carried a
//     dictionary would have to hold one of those.
//
//  3. STRUCTURAL, OVER THE WHOLE FEATURE TREE. Every PRODUCTION source is parsed and
//     scanned for a package-level map or sync.Map, because package scope is the only
//     place in Go where state can outlive a single call without the caller holding it.
//
// The structural guard is NON-VACUOUS four times over, and all four run the SAME
// detector this oracle uses:
//
//   - it FATALS when its file set is empty, and again when the file set is smaller than
//     a floor, so a tree walk that silently stopped covering the feature cannot pass;
//   - it must FIRE on exactly the two shapes it forbids - a package-level map and a
//     package-level sync.Map - so a detector that matched nothing is caught;
//   - it must NOT fire on a function-local map, so it cannot degrade into "no maps
//     anywhere", which would be unmaintainable and would hide the property;
//   - it must skip an allowlisted declaration WITHOUT skipping its file, so the
//     allowlist cannot become a per-file loophole.
//
// And the one audited exception is property-checked rather than trusted: it must stay a
// string-keyed set, be declared exactly once in the named file, and never be assigned
// anywhere in the tree.
func TestStreamToolCall_NoPriorRootDictionaryExists(t *testing.T) {
	t.Parallel()

	// ORACLE 1: behavioral, order independence. Two roots in the SAME fixture, with
	// the first revisited after the second, so a remembering store would answer the
	// third call from memory.
	const rootA = twoPassRealRoot
	const rootB = `/srv/team/workspace/aip-path-virtualization-archive/packages/agent-runtime`

	firstA, reason := pathvirtualization.DeriveMapping(rootA)
	if reason != pathvirtualization.SkipReasonNone || firstA.VirtualRoot == "" {
		t.Fatalf("fixture: the first root must derive an active mapping; its refusal reason is bounded and content-free")
	}
	secondB, reason := pathvirtualization.DeriveMapping(rootB)
	if reason != pathvirtualization.SkipReasonNone || secondB.VirtualRoot == "" {
		t.Fatalf("fixture: the second root must derive an active mapping; its refusal reason is bounded and content-free")
	}
	againA, reason := pathvirtualization.DeriveMapping(rootA)
	if reason != pathvirtualization.SkipReasonNone || againA.VirtualRoot == "" {
		t.Fatalf("fixture: the revisited root must still derive an active mapping; its refusal reason is bounded and content-free")
	}
	if firstA.WorkspaceTag != againA.WorkspaceTag || firstA.VirtualRoot != againA.VirtualRoot {
		t.Fatalf("requirements.md 6.1/6.2 - deriving another root in between must not change what the first root mints: first_tag_bytes=%d again_tag_bytes=%d first_alias_bytes=%d again_alias_bytes=%d",
			len(firstA.WorkspaceTag), len(againA.WorkspaceTag),
			len(firstA.VirtualRoot), len(againA.VirtualRoot))
	}
	if firstA.WorkspaceTag == secondB.WorkspaceTag || firstA.VirtualRoot == secondB.VirtualRoot {
		t.Fatalf("requirements.md 6.2 - two different supported roots must derive different workspace tags: first_tag_bytes=%d second_tag_bytes=%d",
			len(firstA.WorkspaceTag), len(secondB.WorkspaceTag))
	}

	// ORACLE 2: the mapping VALUE carries no store. Field-by-field, by index and kind,
	// so a future field of any reference kind fails here rather than passing as an
	// unnamed addition.
	mappingType := reflect.TypeFor[pathvirtualization.Mapping]()
	wantFields := []struct {
		name string
		kind reflect.Kind
	}{
		// mapping.go declares exactly these four, in this order: the flavor both roots
		// share, the real root as spelled, the workspace identity, and the fixed V1
		// alias. Nothing else.
		{name: "Flavor", kind: reflect.Uint8},
		{name: "RealRoot", kind: reflect.String},
		{name: "WorkspaceTag", kind: reflect.String},
		{name: "VirtualRoot", kind: reflect.String},
	}
	if mappingType.NumField() != len(wantFields) {
		t.Fatalf("requirements.md 6.1 - the mapping must remain a pure four-field value with no store: fields=%d want=%d",
			mappingType.NumField(), len(wantFields))
	}
	for index, want := range wantFields {
		field := mappingType.Field(index)
		if field.Name != want.name {
			t.Fatalf("requirements.md 6.1 - mapping field %d must keep its declared name: got %q want %q", index, field.Name, want.name)
		}
		if field.Type.Kind() != want.kind {
			t.Fatalf("requirements.md 6.1 - mapping field %q must remain a value field: got kind %v want %v",
				field.Name, field.Type.Kind(), want.kind)
		}
		switch field.Type.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
			t.Fatalf("requirements.md 6.1 - mapping field %q is a reference kind (%v); the mapping must hold no dictionary",
				field.Name, field.Type.Kind())
		default:
		}
	}

	// ORACLE 3: structural over the whole feature tree.
	sources := map[string]string{}
	paths := contStructuralFiles(t)
	if len(paths) == 0 {
		t.Fatal("the structural guard proved nothing: no production source was found in the feature tree")
	}
	sawMappingSource := false
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.HasSuffix(filepath.ToSlash(path), "/mapping.go") {
			sawMappingSource = true
		}
		sources[filepath.ToSlash(path)] = string(raw)
	}
	if !sawMappingSource {
		t.Fatal("the structural guard did not cover the derivation source, which is exactly where a per-root cache would be added")
	}
	found := contForbidPriorRootStore(t, sources)
	if found.found() != 0 {
		t.Fatalf("requirements.md 6.1/6.6 - the feature declares package-level mutable state, which is the only place a prior-root dictionary could live: declarations=%d files=%d names=%v",
			found.found(), len(found.files), found.names)
	}
	contAssertAllowedStoresAreInert(t, sources)

	// NON-VACUITY, part one: the detector must FIRE on exactly the shape it forbids.
	// A detector that matched nothing would have made the structural oracle above
	// vacuous, and no positive control in the suite would have noticed.
	const priorStoreSource = `package sample

import "sync"

var priorRootAliases = map[string]string{}
var priorRootCache sync.Map
`
	detected := contForbidPriorRootStore(t, map[string]string{"sample.go": priorStoreSource})
	if detected.found() != 2 {
		t.Fatalf("the structural guard must flag a package-level prior-root dictionary and a package-level sync.Map: findings=%d want 2 names=%v",
			detected.found(), detected.names)
	}
	// The two findings must be the two declared names, so the detector is really
	// reading the declarations rather than counting nodes.
	seen := map[string]bool{}
	for _, name := range detected.names {
		seen[name] = true
	}
	for _, want := range []string{"priorRootAliases", "priorRootCache"} {
		if !seen[want] {
			t.Fatalf("the structural guard must name the offending declaration: missing=%q findings=%v", want, detected.names)
		}
	}

	// NON-VACUITY, part two: the allowlist must be matched PER DECLARATION, not per
	// file. A source that spells an allowlisted name AND a fresh package-level map in
	// the SAME file must still report the fresh one - which is the loophole a
	// per-file skip would open, and the loophole that would make the real tree's
	// schemainfer/schema.go a place to hide a prior-root dictionary.
	const mixedSource = `package schemainfer

var ambiguousKeywords = map[string]struct{}{"contains": {}}
var priorRootAliases = map[string]string{}
`
	mixed := contForbidPriorRootStore(t, map[string]string{"schemainfer/schema.go": mixedSource})
	if mixed.found() != 1 || len(mixed.names) != 1 || mixed.names[0] != "priorRootAliases" {
		t.Fatalf("the structural guard must skip an allowlisted declaration WITHOUT skipping its file: findings=%d names=%v",
			mixed.found(), mixed.names)
	}

	// NON-VACUITY, part three: a function-LOCAL map must NOT be flagged. Every map in
	// the real tree is a per-call scratch structure built from a decoded document or a
	// compiled profile list, none of which is keyed by a root or a tag, and package
	// scope is the only place state can outlive a call. This control stops the guard
	// degrading into "no maps anywhere", which would be unmaintainable and would hide
	// the property it is supposed to prove.
	const localMapSource = `package sample

func count(names []string) int {
	seen := map[string]struct{}{}
	for _, name := range names {
		seen[name] = struct{}{}
	}
	return len(seen)
}
`
	clean := contForbidPriorRootStore(t, map[string]string{"sample.go": localMapSource})
	if clean.found() != 0 {
		t.Fatalf("the structural guard must not flag a function-local map, which cannot outlive its call: findings=%d names=%v",
			clean.found(), clean.names)
	}

	// NON-VACUITY, part four: a package-level map in a file the allowlist does not name
	// must be flagged even when its key holds a workspace tag. That is the exact shape
	// the guard exists to refuse, spelled out literally rather than described.
	const tagKeyedSource = `package sample

var workspaceTagToRoot = map[string]string{}
`
	tagKeyed := contForbidPriorRootStore(t, map[string]string{"mapping.go": tagKeyedSource})
	if tagKeyed.found() != 1 || len(tagKeyed.names) != 1 || tagKeyed.names[0] != "workspaceTagToRoot" {
		t.Fatalf("the structural guard must flag a package-level map keyed by workspace identity: findings=%d names=%v",
			tagKeyed.found(), tagKeyed.names)
	}

	// The scan must have covered a MEANINGFUL number of files, and the mapping itself
	// must be among them. A tree walk that silently returned one file would satisfy
	// the empty-set guard above while covering nothing.
	if len(paths) < contMinStructuralFiles {
		t.Fatalf("the structural guard covered too few production sources to mean anything: files=%d minimum=%d",
			len(paths), contMinStructuralFiles)
	}
}

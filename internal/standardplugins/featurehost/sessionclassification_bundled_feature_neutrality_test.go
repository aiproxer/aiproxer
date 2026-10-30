package featurehost

// Task 10.3 bundled-feature behavioral characterization (requirements 1.8, 1.7).
//
// The architecture sweep in internal/archtest proves no bundled coding-oriented
// feature can even SEE the classification. That is the structural half of
// requirement 1.8; this file is the behavioral half, and it is deliberately a
// DIFFERENTIAL rather than a set of per-feature assertions.
//
// The differential: one process, one durable topology, ONE set of bundled feature
// contributions, and the same client turn - differing only in whether the
// session-classification plane is published. If the observable outcome is
// identical in both, the plane changed nothing a bundled feature or a client can
// see. Any difference IS the requirement 1.8 violation, and the comparator names
// it by field rather than answering a boolean.
//
// The observable outcome set is the coarse, externally visible one the task asks
// for - same backend opens, same event kinds, same tool catalog - plus the request
// the backend actually received, because that is where a tool-catalog filter and a
// request-part hook show up. Internal call counts are deliberately NOT compared: a
// feature may run its own logic as often as it likes as long as the result is
// unchanged.
//
// Three further properties keep the characterization honest:
//
//   - every bundled feature contributed to the plane set is CENSUSED as present in
//     the compiled generation, so "the bundled features behaved identically" cannot
//     be satisfied by an empty feature set;
//   - the served outcome is proven to be a real one - the classified arm is
//     positively classified and committed, and the blocked tool really was removed
//     from the catalog by the bundled policy - so "identical" is a comparison of
//     two meaningful results; and
//   - the comparator is pinned by controls that run it over deliberately different
//     outcomes, so "identical" cannot be the answer a broken comparator always
//     gives.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	ssbunstore "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftool"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	sdktoolpolicy "github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
)

// bundledNeutralityBackendID is the backend the composed turn routes to. It is the
// Codex backend id on purpose: codexclientcompat is one of the characterized
// features and it only acts on that backend, so any other id would characterize a
// feature that was never engaged.
const bundledNeutralityBackendID = "openai-codex"

// bundledNeutralityKeptTool is a tool name no bundled feature blocks, so its
// catalog entry must survive.
const bundledNeutralityKeptTool = "Read"

// bundledNeutralityBlockedTool is the exact name reftoolpolicy's DEFAULT
// configuration blocks, so the catalog filter engages on it without any
// test-supplied configuration. It is deliberately kept OUT of the response stream:
// the blocked tool's CATALOG effect is what this characterization observes, and
// emitting a blocked tool call would fail the turn closed before the stream is
// observable at all.
const bundledNeutralityBlockedTool = "ref_blocked_by_name"

// bundledNeutralityPrompt is the delivered user prompt. Both arms receive it
// byte-identically.
const bundledNeutralityPrompt = "Read the failing test and make the smallest edit."

// bundledFeaturePlaneSet is the closed set of bundled coding-oriented features
// this characterization composes. Every entry uses the feature's OWN default
// configuration, decoded through its production DecodeConfig, so the features
// behave exactly as they do in the standard distribution with no operator YAML:
//
//   - reftoolpolicy     drops blocked tools from the catalog and denies their
//     stream events;
//   - reftool           prefixes tool argument deltas on the stream;
//   - codexclientcompat rewrites request parts for the Codex backend; and
//   - toolcallrepair    finalizes tool calls against the declared schema.
//
// The returned names are the feature ids whose plane values must be present in the
// compiled generation; they are the census keys for the non-vacuity check.
func bundledFeaturePlaneSet(tb testing.TB) (names []string, planes aipfeature.FrozenPlaneSet) {
	tb.Helper()
	policyCfg, err := reftoolpolicy.DecodeConfig(yaml.Node{})
	if err != nil {
		tb.Fatalf("decode the reftoolpolicy default config: %v", err)
	}
	refCfg, err := reftool.DecodeConfig(yaml.Node{})
	if err != nil {
		tb.Fatalf("decode the reftool default config: %v", err)
	}
	compatCfg, err := codexclientcompat.DecodeConfig(yaml.Node{})
	if err != nil {
		tb.Fatalf("decode the codexclientcompat default config: %v", err)
	}
	repairCfg, err := toolcallrepair.DecodeConfig(yaml.Node{})
	if err != nil {
		tb.Fatalf("decode the toolcallrepair default config: %v", err)
	}
	repairBundle, err := toolcallrepair.FeatureBundle(repairCfg)
	if err != nil {
		tb.Fatalf("build the toolcallrepair feature bundle: %v", err)
	}

	set := aipfeature.NewContributionSet()
	steps := []struct {
		plane    string
		pluginID string
		apply    func() error
	}{
		{
			plane: aipfeature.PlaneToolCatalogFilters.ID, pluginID: reftoolpolicy.ID,
			apply: func() error {
				value := []toolcatalog.Filter{reftoolpolicy.NewToolCatalogFilter(policyCfg)}
				return aipfeature.Contribute(set, aipfeature.PlaneToolCatalogFilters, reftoolpolicy.ID, value)
			},
		},
		{
			plane: aipfeature.PlaneToolCallPolicies.ID, pluginID: reftoolpolicy.ID,
			apply: func() error {
				value := []sdktoolpolicy.Policy{reftoolpolicy.NewToolCallPolicy(policyCfg)}
				return aipfeature.Contribute(set, aipfeature.PlaneToolCallPolicies, reftoolpolicy.ID, value)
			},
		},
		{
			plane: aipfeature.PlaneToolReactors.ID, pluginID: reftoolpolicy.ID,
			apply: func() error {
				value := []sdkhooks.ToolReactor{reftoolpolicy.NewToolReactor(policyCfg)}
				return aipfeature.Contribute(set, aipfeature.PlaneToolReactors, reftoolpolicy.ID, value)
			},
		},
		{
			plane: aipfeature.PlaneToolReactors.ID, pluginID: reftool.ID,
			apply: func() error {
				value := []sdkhooks.ToolReactor{reftool.NewToolReactor(refCfg)}
				return aipfeature.Contribute(set, aipfeature.PlaneToolReactors, reftool.ID, value)
			},
		},
		{
			plane: aipfeature.PlaneRequestPartHooks.ID, pluginID: codexclientcompat.ID,
			apply: func() error {
				value := []sdkhooks.RequestPartHook{codexclientcompat.NewRequestPartHook(compatCfg)}
				return aipfeature.Contribute(set, aipfeature.PlaneRequestPartHooks, codexclientcompat.ID, value)
			},
		},
	}
	for _, step := range steps {
		if err := step.apply(); err != nil {
			tb.Fatalf("contribute %q to plane %s: %v", step.pluginID, step.plane, err)
		}
	}
	if err := set.ContributeCandidate(repairBundle.PlaneSet); err != nil {
		tb.Fatalf("merge the toolcallrepair planes: %v", err)
	}
	return []string{reftoolpolicy.ID, reftool.ID, codexclientcompat.ID, toolcallrepair.ID}, set.Freeze()
}

// bundledCallCapture is what the backend actually received. It is the observation
// point for both the tool-catalog filter and the request-part hooks.
type bundledCallCapture struct {
	ToolNames []string
	UserText  []string
	InstrText []string
	// ExtensionKeys are the non-internal call extension keys the request-part hooks
	// left on the working call. codexclientcompat adds one for every Codex-backend
	// turn, so this is where its effect is observable.
	ExtensionKeys []string
}

// bundledBackendRecorder is the single counting backend for one arm. It captures
// the working call the executor handed the backend and returns a fixed stream that
// exercises the tool reactors.
type bundledBackendRecorder struct {
	mu       sync.Mutex
	captures []bundledCallCapture
	opens    atomic.Int32
}

func (b *bundledBackendRecorder) record(capture bundledCallCapture) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.captures = append(b.captures, capture)
}

func (b *bundledBackendRecorder) observed() []bundledCallCapture {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bundledCallCapture(nil), b.captures...)
}

// bundledRig is one compiled generation serving one differential arm. The bundled
// feature contributions are identical across arms; only the presence of the
// classification plane differs.
type bundledRig struct {
	executor      *runtime.Executor
	backend       *bundledBackendRecorder
	classified    bool
	catalogFilter []string
	reactors      []string
	partHooks     []string
}

func bundledContributedIDs[T any](values []T, id func(T) string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, id(value))
	}
	sort.Strings(out)
	return out
}

// newBundledRig compiles one generation whose plane set is the bundled feature
// contributions plus, when enabled, the session-classification plane published by
// the real featurehost generation binder.
func newBundledRig(tb testing.TB, rt *Runtime, database *bun.DB, bundled aipfeature.FrozenPlaneSet, enabled bool) *bundledRig {
	tb.Helper()
	out, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Planes:        bundled,
		Registrations: []aipsdk.Registration{certificationRegistration(tb, enabled, "")},
	})
	if err != nil {
		tb.Fatalf("CompileGeneration(classification enabled=%t): %v", enabled, err)
	}
	published := aipfeature.Get[sessionclassification.Classifier](out.Planes, aipfeature.PlaneSessionClassifier)
	if enabled && published == nil {
		tb.Fatal("enabled generation published no classifier plane")
	}
	if !enabled && published != nil {
		tb.Fatalf("disabled generation published classifier %v; the differential requires an absent plane", published)
	}
	for _, lifecycle := range out.Lifecycles {
		if err := lifecycle.Start(context.Background()); err != nil {
			tb.Fatalf("generation lifecycle Start: %v", err)
		}
	}
	recorder := &bundledBackendRecorder{}
	return &bundledRig{
		executor:      bundledExecutor(tb, database, out.Planes, recorder),
		backend:       recorder,
		classified:    enabled,
		catalogFilter: bundledContributedIDs(aipfeature.Get(out.Planes, aipfeature.PlaneToolCatalogFilters), toolcatalog.Filter.ID),
		reactors: bundledContributedIDs(aipfeature.Get(out.Planes, aipfeature.PlaneToolReactors),
			sdkhooks.ToolReactor.ID),
		partHooks: bundledContributedIDs(aipfeature.Get(out.Planes, aipfeature.PlaneRequestPartHooks),
			sdkhooks.RequestPartHook.ID),
	}
}

// bundledExecutor builds a generic-core executor over the real durable
// secure-session and continuity topology, with the recording counting backend. It
// mirrors certificationExecutorWithWorkspace and differs only in the backend id and
// the capture, which is what makes the tool-catalog filter and the request-part
// hooks observable at all.
func bundledExecutor(
	tb testing.TB,
	database *bun.DB,
	planes aipfeature.FrozenPlaneSet,
	recorder *bundledBackendRecorder,
) *runtime.Executor {
	tb.Helper()
	ctx := context.Background()
	sessionStore, err := ssbunstore.NewWithContext(ctx, database)
	if err != nil {
		tb.Fatalf("durable secure-session store: %v", err)
	}
	continuityStore, err := continuitybunstore.NewWithContext(ctx, database)
	if err != nil {
		tb.Fatalf("durable continuity store: %v", err)
	}
	fingerprintKey := certificationFingerprintKey()
	manager, err := app.NewManager(
		sessionStore,
		app.NewRandGenerator(fingerprintKey),
		b2bualineage.New(continuityStore),
		app.ManagerConfig{FingerprintKey: fingerprintKey, StoreDurable: true},
	)
	if err != nil {
		tb.Fatalf("secure session manager: %v", err)
	}
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = continuityStore
	ex.SecureSession = manager
	ex.SyntheticLocalPrincipal = true
	ex.Bus = hooks.New(hooks.Config{
		SubmitHooks:       aipfeature.Get(planes, aipfeature.PlaneSubmitHooks),
		RequestPartHooks:  aipfeature.Get(planes, aipfeature.PlaneRequestPartHooks),
		ResponsePartHooks: aipfeature.Get(planes, aipfeature.PlaneResponsePartHooks),
		ToolReactors:      aipfeature.Get(planes, aipfeature.PlaneToolReactors),
	})
	// The tool-call finalizer and its assembly budget are installed the way the
	// generic bundle installs them, so toolcallrepair really runs on the completed
	// call instead of being present in the plane set only.
	ex.SetToolCallFinalizers(
		aipfeature.Get(planes, aipfeature.PlaneToolCallFinalizers),
		aipfeature.Get(planes, aipfeature.PlaneToolCallFinalizationMaxArgsBytes),
	)
	ex.Rand = routing.NewSeededRng(3)
	ex.LargeBodyGenerationID = "bundled-neutrality-generation-1"
	ex.LargeBodyCandidateDomainGeneration = "bundled-neutrality-domain-1"
	ex.DefaultBackend = bundledNeutralityBackendID
	ex.Backends = map[string]execbackend.Backend{
		bundledNeutralityBackendID: {
			// Opaque extensions are declared so codexclientcompat's request-part hook
			// can act: a backend that does not satisfy the call's extension types fails
			// the turn closed before any bundled behavior is observable.
			Caps: aipapi.NewBackendCaps(
				aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityOpaqueExtensions),
			DialectSupport: aipapi.DialectSupport{
				ExtensionTypes: []aipapi.ExtensionRequirement{
					{Namespace: "call", Type: "openai_codex.ignore_unsupported_gen_params"},
				},
			},
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				recorder.opens.Add(1)
				recorder.record(bundledCallCapture{
					ToolNames:     bundledToolNames(call),
					UserText:      bundledUserText(call),
					InstrText:     bundledInstructionText(call),
					ExtensionKeys: bundledExtensionKeys(call),
				})
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{
						Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1",
						ToolName: bundledNeutralityKeptTool,
					},
					{
						Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call-1",
						ToolName: bundledNeutralityKeptTool, Delta: `{"path":"main.go"}`,
					},
					{
						Kind: aipapi.EventToolCallFinished, ToolCallID: "call-1",
						ToolName: bundledNeutralityKeptTool,
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{certificationWorkspaceResolver{}}),
		FeaturePlanes: planes,
	})
	return ex
}

func bundledExtensionKeys(call aipapi.Call) []string {
	out := make([]string, 0, len(call.Extensions))
	for key := range call.Extensions {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func bundledToolNames(call aipapi.Call) []string {
	out := make([]string, 0, len(call.Tools))
	for _, tool := range call.Tools {
		out = append(out, tool.Name)
	}
	sort.Strings(out)
	return out
}

func bundledUserText(call aipapi.Call) []string {
	var out []string
	for _, message := range call.Messages {
		if message.Role != aipapi.RoleUser {
			continue
		}
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText {
				out = append(out, part.Text)
			}
		}
	}
	return out
}

func bundledInstructionText(call aipapi.Call) []string {
	var out []string
	for _, instruction := range call.Instructions {
		for _, part := range instruction.Parts {
			if part.Kind == aipapi.PartText {
				out = append(out, part.Text)
			}
		}
	}
	return out
}

// bundledOutcome is one served turn's complete observable outcome. Only externally
// visible facts are recorded: no internal call counts, so a bundled feature is free
// to run its own logic as often as it likes.
type bundledOutcome struct {
	SessionID    string
	Opens        int
	EventKinds   []string
	ToolArgs     map[string]string
	BackendTools []string
	BackendUser  []string
	BackendInstr []string
	BackendExt   []string
	ServeErr     string
}

// bundledOutcomeDiff names every observable field that differs between two arms. An
// empty result IS the requirement 1.8 result, and naming the field is what makes a
// failure actionable.
func bundledOutcomeDiff(withPlane, withoutPlane bundledOutcome) []string {
	var diffs []string
	compare := func(field string, left, right any) {
		if fmt.Sprint(left) != fmt.Sprint(right) {
			diffs = append(diffs, fmt.Sprintf("%s: plane-present=%v plane-absent=%v", field, left, right))
		}
	}
	compare("backend opens", withPlane.Opens, withoutPlane.Opens)
	compare("client event kinds", withPlane.EventKinds, withoutPlane.EventKinds)
	compare("client tool arguments", withPlane.ToolArgs, withoutPlane.ToolArgs)
	compare("backend-received tool catalog", withPlane.BackendTools, withoutPlane.BackendTools)
	compare("backend-received user text", withPlane.BackendUser, withoutPlane.BackendUser)
	compare("backend-received instructions", withPlane.BackendInstr, withoutPlane.BackendInstr)
	compare("backend-received call extensions", withPlane.BackendExt, withoutPlane.BackendExt)
	compare("serve error", withPlane.ServeErr, withoutPlane.ServeErr)
	return diffs
}

// serveBundledTurn serves one canonical turn for an arm and records its complete
// observable outcome.
func serveBundledTurn(t *testing.T, rig *bundledRig, clientSessionID string) bundledOutcome {
	t.Helper()
	sessionID, resumeToken := certificationResumableSession(t, rig.executor, clientSessionID)
	outcome := bundledOutcome{SessionID: sessionID}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: bundledNeutralityBackendID + ":model"},
		Session: aipapi.SessionRef{
			ClientSessionID: clientSessionID,
			ResumeToken:     resumeToken,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(bundledNeutralityPrompt)},
		}},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: certificationPositiveUserAgent,
		},
		Tools: []aipapi.ToolDef{
			{Name: bundledNeutralityKeptTool},
			{Name: bundledNeutralityBlockedTool},
		},
	}

	before := rig.backend.opens.Load()
	stream, err := rig.executor.Execute(context.Background(), call)
	outcome.Opens = int(rig.backend.opens.Load() - before)
	if err != nil {
		outcome.ServeErr = err.Error()
		return outcome
	}
	recorder := &falsePositiveStreamRecorder{inner: stream}
	collected, err := aipapi.Collect(context.Background(), recorder)
	if err != nil {
		outcome.ServeErr = err.Error()
	}
	for _, kind := range recorder.kinds {
		outcome.EventKinds = append(outcome.EventKinds, string(kind))
	}
	outcome.ToolArgs = map[string]string{}
	for _, id := range collected.ToolCallOrder {
		if args := collected.ToolArgs[id]; args != nil {
			outcome.ToolArgs[id] = args.String()
		}
	}
	captures := rig.backend.observed()
	if len(captures) != 1 {
		t.Fatalf("arm %q: the backend received %d calls, want exactly 1", clientSessionID, len(captures))
	}
	outcome.BackendTools = captures[0].ToolNames
	outcome.BackendUser = captures[0].UserText
	outcome.BackendInstr = captures[0].InstrText
	outcome.BackendExt = captures[0].ExtensionKeys
	return outcome
}

// TestBundledFeaturesBehaveIdenticallyWithAndWithoutTheClassificationPlane is the
// behavioral half of requirement 1.8.
//
// Both arms serve the same client turn on the same process with the same bundled
// feature contributions. The only difference is whether the session-classification
// plane is published, and the classified arm is positively classified on its first
// turn, so the comparison is between "coding_agent + bundled features" and
// "unknown + bundled features".
func TestBundledFeaturesBehaveIdenticallyWithAndWithoutTheClassificationPlane(t *testing.T) {
	t.Parallel()

	rt, database, _ := hotPathProcess(t, "bundled-neutrality.db")
	featureNames, bundled := bundledFeaturePlaneSet(t)

	classified := newBundledRig(t, rt, database, bundled, true)
	plain := newBundledRig(t, rt, database, bundled, false)

	// Census: the bundled features are present in BOTH compiled generations, so the
	// comparison is over a real feature set rather than an empty one.
	for _, rig := range []*bundledRig{classified, plain} {
		if len(rig.catalogFilter) == 0 || len(rig.reactors) == 0 || len(rig.partHooks) == 0 {
			t.Fatalf("arm (classified=%t) compiled catalog filters %v, tool reactors %v, request-part hooks %v; "+
				"the bundled contributions were dropped, so an identical outcome would certify nothing",
				rig.classified, rig.catalogFilter, rig.reactors, rig.partHooks)
		}
		for _, want := range []string{reftool.ID, reftoolpolicy.ID + "-reactor"} {
			if !slices.Contains(rig.reactors, want) {
				t.Errorf("arm (classified=%t) tool reactors %v do not include %q", rig.classified, rig.reactors, want)
			}
		}
		if !slices.Contains(rig.catalogFilter, reftoolpolicy.ID+"-filter") {
			t.Errorf("arm (classified=%t) catalog filters %v do not include the reftoolpolicy filter",
				rig.classified, rig.catalogFilter)
		}
		if !slices.Contains(rig.partHooks, codexclientcompat.ID) {
			t.Errorf("arm (classified=%t) request-part hooks %v do not include the codex compat hook",
				rig.classified, rig.partHooks)
		}
		t.Logf("arm classified=%t: catalog filters=%v tool reactors=%v request-part hooks=%v",
			rig.classified, rig.catalogFilter, rig.reactors, rig.partHooks)
	}
	t.Logf("characterized bundled features: %v", featureNames)

	withPlane := serveBundledTurn(t, classified, "bundled-classified")
	withoutPlane := serveBundledTurn(t, plain, "bundled-plain")

	if withPlane.ServeErr != "" {
		t.Fatalf("the classified arm failed to serve: %s", withPlane.ServeErr)
	}
	if withoutPlane.ServeErr != "" {
		t.Fatalf("the plane-absent arm failed to serve: %s", withoutPlane.ServeErr)
	}
	if withPlane.Opens != 1 || withoutPlane.Opens != 1 {
		t.Fatalf("backend opens: classified=%d plain=%d, want exactly 1 each", withPlane.Opens, withoutPlane.Opens)
	}

	// The classified arm really is positively classified and committed, so the
	// comparison is not accidentally unknown-versus-unknown.
	if !certificationRowIsPositive(t, database, withPlane.SessionID) {
		t.Fatalf("the classified arm's session %q owns no durable positive; the differential would be "+
			"comparing two unknown turns", withPlane.SessionID)
	}
	if certificationRowIsPositive(t, database, withoutPlane.SessionID) {
		t.Fatalf("the plane-absent arm's session %q owns a durable positive; the absent plane published none",
			withoutPlane.SessionID)
	}

	// The bundled catalog policy really removed the blocked tool, so the tool catalog
	// is a meaningful comparison field rather than an identical-by-default one.
	if slices.Contains(withPlane.BackendTools, bundledNeutralityBlockedTool) {
		t.Fatalf("the backend received the blocked tool %v; the reftoolpolicy catalog filter never ran, so "+
			"the tool-catalog comparison would certify nothing", withPlane.BackendTools)
	}
	if !slices.Contains(withPlane.BackendTools, bundledNeutralityKeptTool) {
		t.Fatalf("the backend received tools %v, want the kept tool %q to survive",
			withPlane.BackendTools, bundledNeutralityKeptTool)
	}
	// reftool really rewrote the streamed argument delta, so the client-tool-args
	// comparison is over a transformed value rather than a pass-through.
	if !strings.HasPrefix(withPlane.ToolArgs["call-1"], ">>") {
		t.Fatalf("the streamed tool arguments are %v, want the reftool rewrite prefix; the reactor never ran, "+
			"so the stream comparison would certify nothing", withPlane.ToolArgs)
	}
	// codexclientcompat really marked the call for the Codex backend, so the
	// extension comparison is over a mutated request rather than an empty one.
	if len(withPlane.BackendExt) == 0 {
		t.Fatal("the backend received no call extensions; the codex compat request-part hook never ran, so " +
			"the request comparison would certify nothing")
	}

	diffs := bundledOutcomeDiff(withPlane, withoutPlane)
	if len(diffs) != 0 {
		t.Fatalf("publishing the session-classification plane changed the observable outcome of a bundled "+
			"feature turn; requirement 1.8 keeps existing features behavior-neutral until they opt in:\n  %s",
			strings.Join(diffs, "\n  "))
	}
	t.Logf("identical outcome across both arms: backend tools=%v call extensions=%v client events=%v "+
		"tool args=%v user text=%v",
		withPlane.BackendTools, withPlane.BackendExt, withPlane.EventKinds, withPlane.ToolArgs,
		withPlane.BackendUser)
}

// TestBundledFeatureComparisonDetectsAnObservableGate is the non-vacuity control
// for the differential. It feeds the SAME comparator three deliberately different
// outcome pairs and requires it to name the offending field in each, so a
// comparator that answered "identical" unconditionally would fail here even though
// it would pass the characterization above.
func TestBundledFeatureComparisonDetectsAnObservableGate(t *testing.T) {
	t.Parallel()

	base := bundledOutcome{
		Opens:        1,
		EventKinds:   []string{"response_started", "response_finished"},
		ToolArgs:     map[string]string{},
		BackendTools: []string{bundledNeutralityKeptTool},
	}

	t.Run("a changed tool catalog is reported", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.BackendTools = []string{bundledNeutralityBlockedTool}
		diffs := bundledOutcomeDiff(base, mutated)
		if !slices.ContainsFunc(diffs, func(d string) bool {
			return strings.Contains(d, "backend-received tool catalog")
		}) {
			t.Fatalf("diffs = %v, want the tool-catalog field named", diffs)
		}
	})

	t.Run("a changed request is reported", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.BackendInstr = []string{"compatibility mode"}
		mutated.BackendExt = []string{"openai_codex.ignore_unsupported_gen_params"}
		diffs := bundledOutcomeDiff(base, mutated)
		for _, want := range []string{"backend-received instructions", "backend-received call extensions"} {
			if !slices.ContainsFunc(diffs, func(d string) bool { return strings.Contains(d, want) }) {
				t.Fatalf("diffs = %v, want the %q field named", diffs, want)
			}
		}
	})

	t.Run("a changed client stream is reported", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.EventKinds = []string{"response_started"}
		mutated.ToolArgs = map[string]string{"call-1": ">>{}"}
		diffs := bundledOutcomeDiff(base, mutated)
		for _, want := range []string{"client event kinds", "client tool arguments"} {
			if !slices.ContainsFunc(diffs, func(d string) bool { return strings.Contains(d, want) }) {
				t.Fatalf("diffs = %v, want the %q field named", diffs, want)
			}
		}
	})

	t.Run("a refused turn is reported", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Opens = 0
		mutated.ServeErr = "coding-agent turn gated by the opt-in consumer"
		diffs := bundledOutcomeDiff(base, mutated)
		for _, want := range []string{"backend opens", "serve error"} {
			if !slices.ContainsFunc(diffs, func(d string) bool { return strings.Contains(d, want) }) {
				t.Fatalf("diffs = %v, want the %q field named", diffs, want)
			}
		}
	})

	t.Run("an identical pair reports nothing", func(t *testing.T) {
		t.Parallel()
		if diffs := bundledOutcomeDiff(base, base); len(diffs) != 0 {
			t.Fatalf("diffs = %v, want none for two identical outcomes", diffs)
		}
	})
}

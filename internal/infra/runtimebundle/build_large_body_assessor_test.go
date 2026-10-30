package runtimebundle_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildLargeBodyAssessor_CompositionGenID_NotDerivedFromPublication(t *testing.T) {
	t.Parallel()

	gen1 := runtimebundle.NewCompositionGenIDForTest()
	gen2 := runtimebundle.NewCompositionGenIDForTest()

	require.NotEmpty(t, gen1)
	require.NotEmpty(t, gen2)
	require.NotEqual(t, gen1, gen2, "multiple constructions must have distinct incarnation identities")
	for _, gen := range []string{gen1, gen2} {
		require.True(t, strings.HasPrefix(gen, "composition-"), "identity must carry composition scope, got %q", gen)
		require.LessOrEqual(t, len(gen), 64, "identity must stay bounded, got %q", gen)
		require.NotContains(t, gen, "gen-", "identity must not alias economic publication numbering, got %q", gen)
	}
}

// compositionStubSource is a minimal immutable replay capture sized to the
// assessed proof. It records root Close so terminal failure ownership is
// observable without a spool directory.
type compositionStubSource struct {
	size   int64
	body   []byte
	closed atomic.Int32
}

func newCompositionStubSource(size int64) *compositionStubSource {
	return &compositionStubSource{size: size, body: bytes.Repeat([]byte("x"), int(size))}
}

func (s *compositionStubSource) Size() int64 { return s.size }

func (s *compositionStubSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func (s *compositionStubSource) Close() error {
	s.closed.Add(1)
	return nil
}

// compositionWireOpens counts OpenWire invocations for the test-local
// wire-capable backend. Each test uses its own counter via closure so parallel
// tests do not share counts.
type compositionWireCounter struct {
	opens atomic.Int32
}

// compositionWireFactory is a SMALL TEST-LOCAL BackendFactory wrapping the
// existing eligible-backend setup: it returns the exact wire resolvers from
// charTestWireBackend (compatible true, AnyAcceptedModel true) and the exact
// test wire stream from the prior manual executor (FixedEventStream carrying
// EventResponseFinished). It registers via existing RegistrySetup/registry
// interfaces; no production backend, API, generic framework, or new test
// scaffolding package is introduced.
func compositionWireFactory(counter *compositionWireCounter) pluginreg.BackendFactory {
	return func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		return execbackend.Backend{
			Caps: caps,
			ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
				return caps
			},
			BackendPrefixes: []string{"composition-wire"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{
					CanonicalID: "composition-wire:gpt-4o",
					NativeID:    "gpt-4o",
					DisplayName: "Composition Wire Probe",
				}},
			},
			EnforcesMaxOutputTokens: true,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, context.DeadlineExceeded
			},
			ResolveWireRequest: func(context.Context, largebody.WireRequestFacts, routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible: true,
					Reason:     largebody.WireSupportReasonNone,
				}
			},
			ResolveWireDomain: func(context.Context, largebody.WireDomainFacts) largebody.WireDomainSupport {
				return largebody.WireDomainSupport{
					Compatible:       true,
					AnyAcceptedModel: true,
					Reason:           largebody.WireSupportReasonNone,
				}
			},
			OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				counter.opens.Add(1)
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		}, nil
	}
}

// compositionRegistry builds the standard registry plus the test-local
// wire-capable backend kind. No char-bundle feature is registered, so the
// compiled census stays clean (no authority blockers) via existing options.
func compositionRegistry(t *testing.T, counter *compositionWireCounter) *pluginreg.Registry {
	t.Helper()
	reg := stdFactoryCatalog(t)
	require.NoError(t, reg.RegisterBackendWithProfiles("composition-wire-kind", compositionWireFactory(counter),
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone, AccessScope: pluginreg.BackendAccessAny},
		pluginreg.BackendExecutionProfile{Class: aipsdk.BackendExecutionInference}))
	return reg
}

// compositionCandidateConfig registers only the needed existing frontend
// (openai-legacy for openai_chat_v1) and the test-local wire backend with wire
// resolvers. No optional standard features are enabled, via existing config
// options.
func compositionCandidateConfig(t *testing.T, backendID, defaultRoute string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: defaultRoute},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-legacy", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind:    "composition-wire-kind",
				ID:      backendID,
				Enabled: true,
				Config:  genYAMLNode(t, "{}\n"),
			}},
		},
	}
	require.NoError(t, config.Validate(cfg))
	return cfg
}

// newCompositionProcess builds same-process ProcessServices for genuine
// compiled generations. The registry carries the test-local wire backend;
// the candidate configs carry only the needed frontend/backend.
func newCompositionProcess(t *testing.T, counter *compositionWireCounter) *runtimebundle.ProcessServices {
	t.Helper()
	cfg := processBaseConfig()
	require.NoError(t, config.Validate(cfg))
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: compositionRegistry(t, counter)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// compileCompositionGeneration compiles one genuine generation through the
// production candidate compile (feature surface, feature host, assembly).
func compileCompositionGeneration(t *testing.T, ps *runtimebundle.ProcessServices, backendID, defaultRoute string) runtimebundle.GenerationRuntime {
	t.Helper()
	candCfg := compositionCandidateConfig(t, backendID, defaultRoute)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	return bundle
}

// compositionExecutorFrom retrieves the genuine compiled executor from a
// published generation's request plane via the existing ExecutorProvider
// interface. No executor fields are overwritten.
func compositionExecutorFrom(t *testing.T, gen *runtimehost.Generation) *coreruntime.Executor {
	t.Helper()
	rt, ok := gen.RequestPlane().(runtimebundle.GenerationRuntime)
	require.True(t, ok, "published generation must expose GenerationRuntime")
	ep, ok := rt.(runtimehost.ExecutorProvider)
	require.True(t, ok, "generation runtime must implement ExecutorProvider")
	ex, ok := ep.ExecutorView().(*coreruntime.Executor)
	require.True(t, ok, "executor view must be *coreruntime.Executor")
	require.NotNil(t, ex)
	return ex
}

// compositionExecContext carries the request principal and wire identity a
// real execution requires. Each execution takes a distinct request ID so
// request authority admission observes independent requests.
func compositionExecContext(reqID, traceID string) context.Context {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "composition-probe-user"})
	return largebody.WithWireIdentity(ctx, reqID, traceID)
}

// requireCompositionStreamFinished proves the result stream is a genuine
// provider stream carrying the backend event, not a canonical fallback.
func requireCompositionStreamFinished(ctx context.Context, t *testing.T, stream aipapi.EventStream) {
	t.Helper()
	require.NotNil(t, stream, "successful execution must return a provider stream")
	ev, err := stream.Recv(ctx)
	require.NoError(t, err)
	require.Equal(t, aipapi.EventResponseFinished, ev.Kind)
	require.NoError(t, stream.Close())
}

func compositionTestProof() largebody.Proof {
	proof := validCharTestProof("openai_chat_v1", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	// Stock-host eligibility requires what the production frontend would
	// produce for a fresh large request: a fresh A-leg session and a complete
	// compaction fact set. Core wire facts (profile, operation, delivery,
	// route, model, max tokens, protocol facts, mode, rewrite, identity,
	// source, body bytes) stay exact via validCharTestProof; only the
	// stock-required session/compaction completeness is supplied here.
	proof.Session = largebody.SessionInput{NewSessionRequested: true}
	proof.CompactionComplete = true
	return proof
}

func requireCompositionAccepted(t *testing.T, assessor *coreruntime.ProductionLargeBodyAssessor, proof largebody.Proof) largebody.Assessment {
	t.Helper()
	accepted, err := assessor.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, accepted.Decision, "composition must accept valid proof (reason: %v)", accepted.Reason)
	require.False(t, accepted.Stamp.IsZero())
	return accepted
}

func requireCompositionAcceptedViaExecutor(t *testing.T, ex *coreruntime.Executor, proof largebody.Proof) largebody.Assessment {
	t.Helper()
	pa, ok := ex.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok, "expected ProductionLargeBodyAssessor, got %T", ex.LargeBodyAssessor)
	accepted, err := ex.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, accepted.Decision, "compiled composition must accept valid proof (reason: %v)", accepted.Reason)
	require.False(t, accepted.Stamp.IsZero())
	require.Equal(t, pa.GenerationID, accepted.Stamp.GenerationID())
	return accepted
}

// TestCompositionIdentity_SamePublisherBuildsDistinct proves requirement 2.1:
// independently built compositions receive distinct bounded acceptance
// identities even when their economic publication is unchanged or absent.
func TestCompositionIdentity_SamePublisherBuildsDistinct(t *testing.T) {
	t.Parallel()

	pub := snapshotgen.NewPublisher()
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{})

	assessorA, genA, err := runtimebundle.BuildCompositionAssessorForTest(pub)
	require.NoError(t, err)
	assessorB, genB, err := runtimebundle.BuildCompositionAssessorForTest(pub)
	require.NoError(t, err)

	require.NotEmpty(t, genA)
	require.NotEmpty(t, genB)
	require.NotEqual(t, genA, genB, "two compositions sharing one populated publisher must disagree")

	for _, gen := range []string{genA, genB} {
		require.True(t, strings.HasPrefix(gen, "composition-"), "identity must carry composition scope, got %q", gen)
		require.LessOrEqual(t, len(gen), 64, "identity must stay bounded, got %q", gen)
		require.NotContains(t, gen, "gen-", "identity must not alias economic publication numbering, got %q", gen)
	}

	// Absent publication also yields a distinct incarnation.
	_, genN, err := runtimebundle.BuildCompositionAssessorForTest(nil)
	require.NoError(t, err)
	require.NotEqual(t, genA, genN)
	require.NotEqual(t, genB, genN)

	// One identity is reused across the existing acceptance/domain projections.
	require.Equal(t, genA, assessorA.GenerationID)
	require.Equal(t, genA, assessorA.CandidateDomainGeneration)
	require.Equal(t, genB, assessorB.GenerationID)
	require.Equal(t, genB, assessorB.CandidateDomainGeneration)
}

// TestCompositionIsolation_ForeignAcceptedProofTerminal proves requirement 2.2
// (generation condition) through genuine compiled generations: composition A's
// accepted proof executed against composition B's compiled executor fails as a
// terminal invariant failure before any provider open, with zero canonical
// fallback. The rejected source is closed through existing failure ownership.
func TestCompositionIsolation_ForeignAcceptedProofTerminal(t *testing.T) {
	t.Parallel()

	counter := &compositionWireCounter{}
	ps := newCompositionProcess(t, counter)

	bundleA := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")
	t.Cleanup(func() { _ = bundleA.Close() })
	bundleB := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")
	t.Cleanup(func() { _ = bundleB.Close() })

	mgr := runtimehost.NewManager(8, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})
	genA := publishTestGeneration(t, mgr, bundleA, "composition-foreign-A")
	genB := publishTestGeneration(t, mgr, bundleB, "composition-foreign-B")
	require.Equal(t, int64(1), genA.ID())
	require.Equal(t, int64(2), genB.ID())

	execA := compositionExecutorFrom(t, genA)
	execB := compositionExecutorFrom(t, genB)
	paA, ok := execA.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok, "expected ProductionLargeBodyAssessor, got %T", execA.LargeBodyAssessor)
	paB, ok := execB.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok, "expected ProductionLargeBodyAssessor, got %T", execB.LargeBodyAssessor)
	require.NotEqual(t, paA.GenerationID, paB.GenerationID, "two compiled compositions must disagree")

	proof := compositionTestProof()
	acceptedA := requireCompositionAcceptedViaExecutor(t, execA, proof)
	require.Equal(t, paA.GenerationID, acceptedA.Stamp.GenerationID())

	before := counter.opens.Load()
	src := newCompositionStubSource(proof.BodyBytes)
	result, err := execB.ExecuteLargeBody(compositionExecContext("req-foreign", "trace-foreign"), acceptedA, src)
	require.Error(t, err)
	require.True(t, errors.Is(err, largebody.ErrStampDisagreement), "foreign proof must fail terminal, got: %v", err)
	require.Nil(t, result.Stream, "terminal rejection must not fall back to canonical execution")
	require.Equal(t, before, counter.opens.Load(), "foreign proof must fail before any provider open")
	require.Equal(t, int32(1), src.closed.Load(), "rejected source must close through failure ownership")
}

// TestCompositionIsolation_CandidateDomainOnlyDisagreementRejects proves
// requirement 2.2 (candidate-domain condition): disagreeing only the
// candidate-domain identity while the composition identity matches still
// rejects identically, so both acceptance conditions are exercised.
//
// This is a targeted invariant check of the second acceptance condition in
// isolation. It uses the existing BuildCompositionAssessorForTest production
// assessor path with a drifted candidate-domain executor identity; the
// compiled pin flow (2.3) and refresh/rollback flow (2.4) use genuine compiled
// generations below.
func TestCompositionIsolation_CandidateDomainOnlyDisagreementRejects(t *testing.T) {
	t.Parallel()

	assessorA, genA, err := runtimebundle.BuildCompositionAssessorForTest(nil)
	require.NoError(t, err)

	proof := compositionTestProof()
	acceptedA := requireCompositionAccepted(t, assessorA, proof)

	live := largebody.LiveExecutionFacts{
		GenerationID:              genA,
		ProfileID:                 proof.ProfileID,
		Source:                    acceptedA.Stamp.SourceDigest(),
		BodyBytes:                 proof.BodyBytes,
		Mode:                      acceptedA.Stamp.BodyMode(),
		Rewrite:                   acceptedA.Stamp.Rewrite(),
		CandidateDomainGeneration: "composition-domain-only-differs",
	}
	err = largebody.ValidateExecuteLargeBody(acceptedA, newCompositionStubSource(proof.BodyBytes), live)
	require.Error(t, err)
	require.True(t, errors.Is(err, largebody.ErrStampDisagreement), "candidate-domain disagreement must fail terminal, got: %v", err)
	var invariantErr *largebody.StampInvariantError
	require.True(t, errors.As(err, &invariantErr), "rejection must detail the invariant, got: %v", err)
	require.Contains(t, strings.ToLower(invariantErr.Field), "candidate/domain")
}

// TestCompositionIsolation_PinnedAcceptsAfterReplacement proves requirement
// 2.3 through genuine compiled generations: a request pinned to its admitted
// composition keeps that composition's identity and execution domain despite
// publication of a replacement, while the replacement rejects the pinned
// proof without canonical execution.
//
// Flow: compile A -> publish A -> pin A -> retrieve pinned executor -> SAME
// accepted proof executes before replacement -> publish B (replacement) ->
// pin still bound to old -> SAME proof still executes via pinned executor ->
// replacement executor rejects SAME proof. A targeted mutant (B's proof
// rejected by A) proves the success is identity-bound rather than vacuous.
func TestCompositionIsolation_PinnedAcceptsAfterReplacement(t *testing.T) {
	t.Parallel()

	counter := &compositionWireCounter{}
	ps := newCompositionProcess(t, counter)

	bundleA := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")
	bundleB := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")

	mgr := runtimehost.NewManager(8, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})

	genA := publishTestGeneration(t, mgr, bundleA, "composition-A")
	require.Equal(t, int64(1), genA.ID())
	require.Equal(t, int64(1), mgr.Active().ID())

	leaseA, ok := mgr.Acquire()
	require.True(t, ok, "must acquire admitted lease on serving composition")
	pinA, ok := leaseA.TransferPin(runtimehost.PinAsync)
	require.True(t, ok, "must retain admitted pin across replacement")
	defer pinA.Release()

	execA := compositionExecutorFrom(t, pinA.Generation())
	paA, ok := execA.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	genAComp := paA.GenerationID

	proof := compositionTestProof()
	acceptedA := requireCompositionAcceptedViaExecutor(t, execA, proof)

	// Baseline: A's accepted proof executes through its own pinned retrieved
	// executor.
	ctxBase := compositionExecContext("req-pinned-base", "trace-pinned-base")
	before := counter.opens.Load()
	resBase, err := execA.ExecuteLargeBody(ctxBase, acceptedA, newCompositionStubSource(proof.BodyBytes))
	require.NoError(t, err, "pinned composition must execute its own accepted proof")
	requireCompositionStreamFinished(ctxBase, t, resBase.Stream)
	require.Equal(t, before+1, counter.opens.Load(), "pinned execution must reach its original provider")

	// Economic refresh publishes a replacement generation number without
	// reusing numbers; composition identity must not derive from it.
	pub := snapshotgen.NewPublisher()
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{})
	publishedBefore := pub.Current().ID
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{})
	require.NotEqual(t, publishedBefore, pub.Current().ID, "refresh must publish without reusing numbers")

	// Replacement publication on the production manager.
	genB := publishTestGeneration(t, mgr, bundleB, "composition-B")
	require.Equal(t, int64(2), genB.ID(), "replacement must become active")
	require.Equal(t, int64(2), mgr.Active().ID())
	require.Equal(t, int64(1), pinA.Generation().ID(), "admitted pin must stay bound to the old composition")

	execB := compositionExecutorFrom(t, genB)
	paB, ok := execB.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.NotEqual(t, genAComp, paB.GenerationID)

	// Pinned composition retains identity and still accepts its own proof.
	require.Equal(t, genAComp, paA.GenerationID)
	require.Equal(t, genAComp, paA.CandidateDomainGeneration)
	acceptedAgain, err := execA.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, acceptedAgain.Decision)
	require.Equal(t, genAComp, acceptedAgain.Stamp.GenerationID())
	require.Equal(t, genAComp, acceptedAgain.Stamp.CandidateDomainGeneration())

	// SAME pinned proof still executes through its own retrieved executor
	// after replacement publication.
	ctxPinned := compositionExecContext("req-pinned-after", "trace-pinned-after")
	beforePinned := counter.opens.Load()
	resPinned, err := execA.ExecuteLargeBody(ctxPinned, acceptedA, newCompositionStubSource(proof.BodyBytes))
	require.NoError(t, err, "pinned composition must still execute its proof after replacement publication")
	requireCompositionStreamFinished(ctxPinned, t, resPinned.Stream)
	require.Equal(t, beforePinned+1, counter.opens.Load(), "pinned execution must still reach its original provider")

	// Replacement composition rejects the SAME pinned proof without execution.
	beforeB := counter.opens.Load()
	srcB := newCompositionStubSource(proof.BodyBytes)
	result, err := execB.ExecuteLargeBody(compositionExecContext("req-pinned-foreign", "trace-pinned-foreign"), acceptedA, srcB)
	require.Error(t, err)
	require.True(t, errors.Is(err, largebody.ErrStampDisagreement), "replacement must reject pinned foreign proof, got: %v", err)
	require.Nil(t, result.Stream, "terminal rejection must not fall back to canonical execution")
	require.Equal(t, beforeB, counter.opens.Load(), "replacement must reject before any provider open")
	require.Equal(t, int32(1), srcB.closed.Load(), "rejected source must close through failure ownership")

	// Targeted mutant control in the reverse direction: B's own accepted proof
	// executes via B but is rejected by pinned A, proving the pinned success
	// is identity-bound rather than vacuous.
	acceptedB := requireCompositionAcceptedViaExecutor(t, execB, proof)
	require.Equal(t, paB.GenerationID, acceptedB.Stamp.GenerationID())
	ctxMutBase := compositionExecContext("req-mutant-base", "trace-mutant-base")
	resMutBase, err := execB.ExecuteLargeBody(ctxMutBase, acceptedB, newCompositionStubSource(proof.BodyBytes))
	require.NoError(t, err)
	requireCompositionStreamFinished(ctxMutBase, t, resMutBase.Stream)
	beforeMut := counter.opens.Load()
	_, err = execA.ExecuteLargeBody(compositionExecContext("req-mutant-foreign", "trace-mutant-foreign"), acceptedB, newCompositionStubSource(proof.BodyBytes))
	require.Error(t, err)
	require.True(t, errors.Is(err, largebody.ErrStampDisagreement), "pinned A must reject B's proof, got: %v", err)
	require.Equal(t, beforeMut, counter.opens.Load())
}

// TestCompositionIsolation_RefreshAndRollbackPreserveActive proves requirement
// 2.4 through genuine same-process compilation: economic refresh on the actual
// process publisher, a real failed candidate compilation aborted by the
// production fault hook after the handler stage, and a real discarded compiled
// candidate leave the active composition identity, the economic publication,
// and pinned request behavior unchanged.
//
// The failed candidate is a separately compiled generation aborted by the
// production fault hook after "handler" (its executor is already compiled, so
// rollback exercises the post-executor path); the discarded candidate is a
// genuinely compiled generation discarded through production manager
// ownership. Both run their own cleanup only, while the pinned active compiled
// composition keeps executing its SAME accepted proof against its original
// domain.
func TestCompositionIsolation_RefreshAndRollbackPreserveActive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	counter := &compositionWireCounter{}
	ps := newCompositionProcess(t, counter)

	bundleA := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")

	mgr := runtimehost.NewManager(8, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})
	genActive := publishTestGeneration(t, mgr, bundleA, "composition-active")
	require.Equal(t, int64(1), genActive.ID())

	// Real async pin on the serving composition, retained across refresh and
	// rollback. The executor is retrieved through the pin, never the manager.
	lease, ok := mgr.Acquire()
	require.True(t, ok, "must acquire admitted lease on serving composition")
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	require.True(t, ok, "must retain admitted async pin across refresh and rollback")
	defer pin.Release()
	pinGen := pin.Generation()
	require.Same(t, genActive, pinGen, "async pin must stay bound to the active composition")

	execA := compositionExecutorFrom(t, pinGen)
	paA, ok := execA.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok, "expected ProductionLargeBodyAssessor, got %T", execA.LargeBodyAssessor)
	genA := paA.GenerationID
	planeBefore := pinGen.RequestPlane()
	require.NotNil(t, planeBefore, "pinned generation must expose its request plane")

	// The active executor must share the actual process snapshot publisher
	// instance. NewProcessServices wires it before terminal workers, so no
	// unrelated publisher is introduced here.
	require.NotNil(t, ps.SnapshotGeneration, "process must own an economic snapshot publisher")
	pub := ps.SnapshotGeneration
	require.Same(t, pub, execA.SnapshotGeneration, "active executor must share the process snapshot publisher")

	proof := compositionTestProof()
	acceptedBefore := requireCompositionAcceptedViaExecutor(t, execA, proof)

	// Baseline: the pinned active composition executes its accepted proof.
	ctxBase := compositionExecContext("req-active-base", "trace-active-base")
	before := counter.opens.Load()
	resBase, err := execA.ExecuteLargeBody(ctxBase, acceptedBefore, newCompositionStubSource(proof.BodyBytes))
	require.NoError(t, err, "active composition must execute its own accepted proof")
	requireCompositionStreamFinished(ctxBase, t, resBase.Stream)
	require.Equal(t, before+1, counter.opens.Load())

	// Economic refresh publishes replacement generation numbers on the ACTUAL
	// process publisher.
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{})
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{})
	refreshed := pub.Current()
	require.NotNil(t, refreshed)

	// Real failed candidate: a separately compiled generation aborted by the
	// production fault hook after the handler stage, i.e. after its executor
	// was compiled. Compilation fails closed and process services survive for
	// the active composition.
	candCfg := compositionCandidateConfig(t, "composition-wire", "composition-wire:gpt-4o")
	_, err = runtimebundle.CompileGeneration(ctx, runtimebundle.GenerationCompileInput{
		Process:     ps,
		Bus:         hooks.New(hooks.Config{}),
		Candidate:   candCfg,
		Compose:     stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{After: "handler"},
	})
	require.Error(t, err, "injected candidate fault must fail compilation")
	require.Contains(t, err.Error(), "after handler")
	require.True(t, errors.Is(err, runtimebundle.ErrCandidateFaultInjected), "want fault-injected failure, got: %v", err)
	require.False(t, ps.Closed(), "process services must survive candidate rollback")

	// Real discarded candidate: a genuinely compiled generation discarded
	// through production manager ownership without publication.
	bundleCand := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")
	discardGen := mgr.PrepareRequestPlane("composition-candidate", bundleCand)
	execCand := compositionExecutorFrom(t, discardGen)
	require.Same(t, pub, execCand.SnapshotGeneration, "discarded candidate must share the same process snapshot publisher")
	require.NoError(t, discardGen.Discard(), "discard must run candidate-owned cleanup")

	// Active composition identity, economic publication, and pinned execution
	// are unchanged by the failed and discarded candidates.
	require.Equal(t, genA, paA.GenerationID)
	require.Equal(t, genA, paA.CandidateDomainGeneration)
	require.Same(t, refreshed, pub.Current(), "failed candidate and discard must not disturb economic publication")
	require.Equal(t, int64(1), mgr.Active().ID(), "manager must retain active composition")
	require.Same(t, genActive, mgr.Active(), "manager must retain the same active generation pointer")
	require.Same(t, genActive, pin.Generation(), "async pin must stay bound to the active composition")
	require.Equal(t, int64(1), pin.Generation().ID())
	require.Same(t, planeBefore, pin.Generation().RequestPlane(), "pinned request plane pointer must be retained")
	execAfter := compositionExecutorFrom(t, pin.Generation())
	require.Same(t, execA, execAfter, "pinned executor pointer must be retained")
	require.Equal(t, genA, paA.GenerationID, "pinned executor must retain its composition generation identity")
	require.Same(t, pub, execA.SnapshotGeneration, "process publisher instance must be retained")

	// The SAME accepted proof still executes through the pinned retrieved
	// executor after failure and discard, reaching its original provider.
	ctxAfter := compositionExecContext("req-active-after", "trace-active-after")
	beforeAfter := counter.opens.Load()
	resAfter, err := execA.ExecuteLargeBody(ctxAfter, acceptedBefore, newCompositionStubSource(proof.BodyBytes))
	require.NoError(t, err, "pinned execution must survive candidate failure and discard")
	requireCompositionStreamFinished(ctxAfter, t, resAfter.Stream)
	require.Equal(t, beforeAfter+1, counter.opens.Load(), "pinned execution must still reach its original provider")
}

// --- Task 4 eligibility-consolidation production-occupancy sentinel ---
//
// The standard serving builder (buildLargeBodyAssessor) seeds its census from
// declaration-derived metadata (NewStandardDependencyCensus) and projects
// occupancy from frozen contributions, hook-bus lengths, typed core ports,
// routing/accounting/billing and custom callbacks. These sentinels drive that
// real production path with bounded frozen inputs and prove early rejection,
// admitted isolation and unchanged defaults. They reuse existing stubs,
// helpers and seams; no new harness is introduced.

// eligibilitySentinelResponseHook is a minimal response-part hook occupant.
type eligibilitySentinelResponseHook struct{ id string }

func (h eligibilitySentinelResponseHook) ID() string { return h.id }

func (eligibilitySentinelResponseHook) Order() int { return 0 }

func (eligibilitySentinelResponseHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (eligibilitySentinelResponseHook) HandleEvent(context.Context, *aipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

// eligibilitySentinelTrafficObserver is a minimal traffic observer occupant.
type eligibilitySentinelTrafficObserver struct{}

func (eligibilitySentinelTrafficObserver) OnObservation(context.Context, traffic.Observation) error {
	return nil
}

// eligibilitySentinelAdmission is a minimal paid exposure-admission port. Its
// presence (non-nil) is the admission fact; behavior is never exercised here.
type eligibilitySentinelAdmission struct{}

func (eligibilitySentinelAdmission) Admit(context.Context, coreruntime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{}, nil
}

// eligibilitySentinelFrozen contributes one bounded occupant through the
// existing feature-bundle freeze seam and returns the frozen plane set the
// production builder consumes via ToContributions.
func eligibilitySentinelFrozen(t *testing.T, contribute func(*aipfeature.ContributionSet) error) aipfeature.FrozenPlaneSet {
	t.Helper()
	bundle := testkit.FeatureBundle(t, "eligibility-sentinel", contribute, nil)
	return testkit.FreezeBundle(bundle)
}

func eligibilitySentinelResponseOnlyFrozen(t *testing.T) aipfeature.FrozenPlaneSet {
	t.Helper()
	return eligibilitySentinelFrozen(t, func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "eligibility-sentinel",
			[]sdkhooks.ResponsePartHook{eligibilitySentinelResponseHook{id: "sentinel-resp"}})
	})
}

func eligibilitySentinelMetadataOnlyFrozen(t *testing.T) aipfeature.FrozenPlaneSet {
	t.Helper()
	return eligibilitySentinelFrozen(t, func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "eligibility-sentinel",
			[]session.Opener{charBundleSessionOpener{id: "sentinel-opener"}})
	})
}

func eligibilitySentinelTrafficFrozen(t *testing.T) aipfeature.FrozenPlaneSet {
	t.Helper()
	return eligibilitySentinelFrozen(t, func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "eligibility-sentinel",
			[]traffic.Observer{eligibilitySentinelTrafficObserver{}})
	})
}

// requireSentinelNoBlockingExtraPorts proves the built census carries no
// spurious blocking ports: every extra port is classified and nonblocking.
// The always-registered security.session_recorder exemption (wire-safe) is
// covered here rather than forbidden.
func requireSentinelNoBlockingExtraPorts(t *testing.T, assessor *coreruntime.ProductionLargeBodyAssessor) {
	t.Helper()
	for _, ep := range assessor.AuthorityGate.Census.ExtraPorts {
		require.False(t, ep.Class.IsUnknown(), "extra port %q must be classified", ep.Name)
		require.False(t, ep.Class.IsBlocker() && ep.Occupied, "extra port %q must not block", ep.Name)
	}
}

// requireSentinelDeclined proves early rejection: same-permit decline, bounded
// authority-blocker reason, and a zero stamp so no wire execution can open.
func requireSentinelDeclined(t *testing.T, assessor *coreruntime.ProductionLargeBodyAssessor, proof largebody.Proof) largebody.Assessment {
	t.Helper()
	declined, err := assessor.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionDecline, declined.Decision)
	require.Equal(t, largebody.DeclineReasonAuthorityBlocker, declined.Reason)
	require.True(t, declined.Stamp.IsZero(), "decline must carry no stamp, otherwise wire execution could open")
	return declined
}

// TestEligibilityConsolidation_ProductionOccupancy_ResponseOnlyBlocks proves
// requirements 2.1/2.2 through the real production builder: an occupied
// response-only plane (frozen contribution) and occupied response-part hook
// occupancy (hook bus) each block with a recorded static blocker.
func TestEligibilityConsolidation_ProductionOccupancy_ResponseOnlyBlocks(t *testing.T) {
	t.Parallel()

	frozen := eligibilitySentinelResponseOnlyFrozen(t)
	assessor, genID, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{Frozen: frozen})
	require.NoError(t, err)
	require.NotEmpty(t, genID)
	require.True(t, assessor.AuthorityGate.Summary.HasStaticBlocker(), "occupied response-only plane must record a static blocker")
	require.NotZero(t, assessor.AuthorityGate.Summary.PlaneBlockers(), "occupied response-only plane must set a plane blocker bit")
	requireSentinelDeclined(t, assessor, compositionTestProof())

	bus := hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{
		eligibilitySentinelResponseHook{id: "sentinel-bus-resp"},
	}})
	hookAssessor, _, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{Bus: bus})
	require.NoError(t, err)
	require.True(t, hookAssessor.AuthorityGate.Census.Hooks.ResponsePartOccupied, "bus response-part length must project hook occupancy")
	require.NotZero(t, hookAssessor.AuthorityGate.Summary.HookBlockers(), "occupied response-part chain must set a hook blocker bit")
	requireSentinelDeclined(t, hookAssessor, compositionTestProof())
}

// TestEligibilityConsolidation_ProductionOccupancy_MetadataOnlyPositive proves
// requirement 2.4 through the real production builder: an occupied
// metadata-only plane stays nonblocking and the composition remains available
// for assessment with no spurious extra ports.
func TestEligibilityConsolidation_ProductionOccupancy_MetadataOnlyPositive(t *testing.T) {
	t.Parallel()

	frozen := eligibilitySentinelMetadataOnlyFrozen(t)
	assessor, genID, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{Frozen: frozen})
	require.NoError(t, err)
	require.NotEmpty(t, genID)
	require.False(t, assessor.AuthorityGate.Summary.HasStaticBlocker(), "occupied metadata-only plane must not block")
	requireSentinelNoBlockingExtraPorts(t, assessor)
	accepted, err := assessor.AssessLargeBody(context.Background(), compositionTestProof())
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, accepted.Decision)
	require.False(t, accepted.Stamp.IsZero())
	require.Equal(t, genID, accepted.Stamp.GenerationID())
}

// TestEligibilityConsolidation_ProductionOccupancy_BlockersPreserved proves
// requirements 2.4/4.5 through the real production builder: paid admission,
// custom-billing callbacks, production traffic observers and frozen traffic
// planes each retain their existing wire blocker. It also pins the typed
// core-port unknown-field fail-closed behavior on the built census.
func TestEligibilityConsolidation_ProductionOccupancy_BlockersPreserved(t *testing.T) {
	t.Parallel()

	trafficFrozen := eligibilitySentinelTrafficFrozen(t)
	cases := []struct {
		name  string
		input runtimebundle.OccupancyAssessorTestInput
	}{
		{"paid exposure admission", runtimebundle.OccupancyAssessorTestInput{
			Prod: runtimebundle.ProductionOptions{BillingExposureAdmission: eligibilitySentinelAdmission{}},
		}},
		{"custom billing callbacks", runtimebundle.OccupancyAssessorTestInput{
			Prod: runtimebundle.ProductionOptions{BillingIdentity: coreruntime.BillingIdentity{
				AccountID: func(context.Context, aipapi.Call) string { return "sentinel-acct" },
			}},
		}},
		{"production traffic observers", runtimebundle.OccupancyAssessorTestInput{
			Prod: runtimebundle.ProductionOptions{TrafficObservers: []traffic.Observer{eligibilitySentinelTrafficObserver{}}},
		}},
		{"frozen traffic planes", runtimebundle.OccupancyAssessorTestInput{Frozen: trafficFrozen}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assessor, _, err := runtimebundle.BuildOccupancyAssessorForTest(tc.input)
			require.NoError(t, err)
			require.True(t, assessor.AuthorityGate.Summary.HasStaticBlocker(), "%s must record a static blocker", tc.name)
			requireSentinelDeclined(t, assessor, compositionTestProof())
		})
	}

	clean, genID, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{})
	require.NoError(t, err)
	requireSentinelNoBlockingExtraPorts(t, clean)
	unknown := clean.AuthorityGate.Census
	unknown.AddPort("core.SentinelFuturePort", true)
	decision, reason := largebody.NewAuthorityAssessmentGate(clean.AuthorityGate.Summary, unknown, genID).Evaluate()
	require.Equal(t, largebody.AssessmentDecisionDecline, decision, "unfamiliar occupied core field must fail closed")
	require.Equal(t, largebody.DeclineReasonAuthorityBlocker, reason)
}

// TestEligibilityConsolidation_ProductionOccupancy_AdmittedIsolation proves
// requirement 4.3 through the real production builder: later public
// descriptor/list mutation and rejected candidate compilation do not change
// admitted eligibility metadata or decisions.
//
// NOTE: this test mutates the aipfeature.StandardPlanes global and therefore
// must not run in parallel. No test in this package reads that global, and
// the mutation is restored by defer before return.
func TestEligibilityConsolidation_ProductionOccupancy_AdmittedIsolation(t *testing.T) {
	frozen := eligibilitySentinelMetadataOnlyFrozen(t)
	admitted, genID, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{Frozen: frozen})
	require.NoError(t, err)
	baseline := requireCompositionAccepted(t, admitted, compositionTestProof())

	counter := &compositionWireCounter{}
	ps := newCompositionProcess(t, counter)
	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:     ps,
		Bus:         hooks.New(hooks.Config{}),
		Candidate:   compositionCandidateConfig(t, "composition-wire", "composition-wire:gpt-4o"),
		Compose:     stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{After: "handler"},
	})
	require.Error(t, err, "injected candidate fault must fail compilation")
	require.ErrorIs(t, err, runtimebundle.ErrCandidateFaultInjected)
	require.False(t, ps.Closed(), "process services must survive candidate rollback")
	reaudit, err := admitted.AssessLargeBody(context.Background(), compositionTestProof())
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, reaudit.Decision, "rejected candidate must not change admitted decisions")
	require.Equal(t, baseline.Stamp.GenerationID(), reaudit.Stamp.GenerationID())

	origPlanes := aipfeature.StandardPlanes
	defer func() { aipfeature.StandardPlanes = origPlanes }()
	mutated := append([]aipfeature.PlaneDeclaration(nil), origPlanes...)
	mutated[0] = nil
	mutated = append(mutated, aipfeature.PlaneSubmitHooks)
	aipfeature.StandardPlanes = mutated
	require.NotEqual(t, len(origPlanes), len(aipfeature.StandardPlanes), "test mutation must actually disturb the public list")

	still, err := admitted.AssessLargeBody(context.Background(), compositionTestProof())
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, still.Decision, "descriptor mutation must not change admitted decisions")
	require.Equal(t, genID, still.Stamp.GenerationID())

	fresh, freshGen, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{Frozen: frozen})
	require.NoError(t, err)
	require.Equal(t, largebody.WireEligibilityPlaneCount, len(fresh.AuthorityGate.Census.Planes),
		"fresh census must come from derived metadata, not the mutated public list")
	require.Equal(t, admitted.AuthorityGate.Census.Planes, fresh.AuthorityGate.Census.Planes,
		"fresh census must match admitted metadata despite descriptor mutation")
	freshAccepted, err := fresh.AssessLargeBody(context.Background(), compositionTestProof())
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionAccept, freshAccepted.Decision)
	require.Equal(t, freshGen, freshAccepted.Stamp.GenerationID())
}

// TestEligibilityConsolidation_ProductionOccupancy_EarlyRejectionDefaults proves
// requirements 2.5/4.1/4.2 through the real production builder: pre-capture
// static precedence is preserved, body-admission limits are unchanged, and an
// unsupported composition declines to canonical without opening a wire backend.
func TestEligibilityConsolidation_ProductionOccupancy_EarlyRejectionDefaults(t *testing.T) {
	t.Parallel()

	admitted, genID, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{
		Frozen: eligibilitySentinelMetadataOnlyFrozen(t),
	})
	require.NoError(t, err)
	summary := admitted.AuthorityGate.Summary

	disabled, disabledReason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
		FeatureEnabled: false, HasKnownLength: true, ContentLength: 1 << 30,
	})
	require.True(t, disabled.IsDefinitelyCanonical())
	require.Equal(t, largebody.StaticWireReasonFeatureDisabled, disabledReason)

	blocked, _, err := runtimebundle.BuildOccupancyAssessorForTest(runtimebundle.OccupancyAssessorTestInput{
		Frozen: eligibilitySentinelResponseOnlyFrozen(t),
	})
	require.NoError(t, err)
	precedence, precedenceReason := largebody.StaticDisposition(blocked.AuthorityGate.Summary, largebody.StaticDispositionInput{
		FeatureEnabled: true, GzipCompressed: true, HasKnownLength: true, ContentLength: 1,
	})
	require.True(t, precedence.IsDefinitelyCanonical())
	require.Equal(t, largebody.StaticWireReasonStaticBlocker, precedenceReason,
		"static blocker outranks gzip and size-threshold reasons")

	require.Equal(t, int64(1<<20), largebody.DefaultThresholdBytes, "body-admission default limit must stay unchanged")
	below, belowReason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
		FeatureEnabled: true, GenerationID: genID, HasKnownLength: true, ContentLength: largebody.DefaultThresholdBytes - 1,
	})
	require.True(t, below.IsDefinitelyCanonical())
	require.Equal(t, largebody.StaticWireReasonBelowThreshold, belowReason)
	atLimit, atLimitReason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
		FeatureEnabled: true, GenerationID: genID, HasKnownLength: true, ContentLength: largebody.DefaultThresholdBytes,
	})
	require.True(t, atLimit.IsNeedsRequestAssessment())
	require.Equal(t, largebody.StaticWireReasonNone, atLimitReason)

	counter := &compositionWireCounter{}
	ps := newCompositionProcess(t, counter)
	bundle := compileCompositionGeneration(t, ps, "composition-wire", "composition-wire:gpt-4o")
	t.Cleanup(func() { _ = bundle.Close() })
	mgr := runtimehost.NewManager(8, nil)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.ShutdownDetached(shutdownCtx)
	})
	gen := publishTestGeneration(t, mgr, bundle, "composition-unsupported")
	exec := compositionExecutorFrom(t, gen)

	proof := compositionTestProof()
	proof.Delivery = aipapi.DeliveryModeNonStreaming
	declined, err := exec.AssessLargeBody(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, largebody.AssessmentDecisionDecline, declined.Decision, "unsupported delivery must decline to canonical")
	require.True(t, declined.Stamp.IsZero(), "decline must carry no stamp, otherwise wire execution could open")
	before := counter.opens.Load()
	src := newCompositionStubSource(proof.BodyBytes)
	result, err := exec.ExecuteLargeBody(compositionExecContext("req-unsupported", "trace-unsupported"), declined, src)
	require.Error(t, err, "execution without an accepted stamp must fail")
	require.Nil(t, result.Stream, "unsupported composition must not fall back to canonical execution")
	require.Equal(t, before, counter.opens.Load(), "unsupported composition must never open a wire backend")
	require.Equal(t, int32(1), src.closed.Load(), "rejected source must close through failure ownership")
}

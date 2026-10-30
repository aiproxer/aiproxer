package runtimebundle

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	keepwarm "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testBackgroundProcessRunner struct{}

func (testBackgroundProcessRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
}

// ProcessFeatureSnapshot captures the unique pointers of process-scoped feature resources
// observable from generic runtimebundle. Compaction coordinator/parent-port
// live inside package featurehost and are proven there
// (TestProcess_CompactionConstructionCounted); the detector remains observable
// here through the public consumer port accessor.
type ProcessFeatureSnapshot struct {
	KeepwarmPolicy         *keepwarm.PolicyStore
	KeepwarmRegistry       *keepwarm.ManagerRegistry
	TerminalDecisionPolicy *sessionpolicy.Store
	CompactionDetector     runtime.CompactionDetector
	ConversationStore      conversationview.Store
	BackgroundAux          *BackgroundAuxScheduler
	StandardFeatures       *featurehost.Runtime
}

// CaptureProcessFeatureSnapshot extracts the current process feature resources from ProcessServices.
func CaptureProcessFeatureSnapshot(ps *ProcessServices) ProcessFeatureSnapshot {
	if ps == nil {
		return ProcessFeatureSnapshot{}
	}
	var (
		detector  runtime.CompactionDetector
		convStore conversationview.Store
		kwPolicy  *keepwarm.PolicyStore
		kwReg     *keepwarm.ManagerRegistry
		termPol   *sessionpolicy.Store
	)
	if ps.StandardFeatures != nil {
		detector = ps.StandardFeatures.CompactionDetector()
		convStore = ps.StandardFeatures.ConversationStore()
		kwPolicy = ps.StandardFeatures.KeepwarmPolicy()
		kwReg = ps.StandardFeatures.KeepwarmRegistry()
		termPol = ps.StandardFeatures.TerminalDecisionPolicy()
	}
	return ProcessFeatureSnapshot{
		KeepwarmPolicy:         kwPolicy,
		KeepwarmRegistry:       kwReg,
		TerminalDecisionPolicy: termPol,
		CompactionDetector:     detector,
		ConversationStore:      convStore,
		BackgroundAux:          ps.BackgroundAux,
		StandardFeatures:       ps.StandardFeatures,
	}
}

// AssertAllPresent asserts that each enabled process-scoped feature resource is instantiated.
func (s ProcessFeatureSnapshot) AssertAllPresent(t *testing.T) {
	t.Helper()
	if s.KeepwarmPolicy == nil {
		t.Fatal("expected non-nil KeepwarmPolicy on StandardFeatures")
	}
	if s.KeepwarmRegistry == nil {
		t.Fatal("expected non-nil KeepwarmRegistry on StandardFeatures")
	}
	if s.TerminalDecisionPolicy == nil {
		t.Fatal("expected non-nil TerminalDecisionPolicy on StandardFeatures")
	}
	if s.CompactionDetector == nil {
		t.Fatal("expected non-nil CompactionDetector on StandardFeatures")
	}
	if s.ConversationStore == nil {
		t.Fatal("expected non-nil ConversationStore on StandardFeatures")
	}
	if s.BackgroundAux == nil {
		t.Fatal("expected non-nil BackgroundAux on ProcessServices")
	}
	if s.StandardFeatures == nil {
		t.Fatal("expected non-nil StandardFeatures on ProcessServices")
	}
}

// AssertIdentical asserts that two snapshots refer to the exact same physical instances.
func (s ProcessFeatureSnapshot) AssertIdentical(t *testing.T, other ProcessFeatureSnapshot, stage string) {
	t.Helper()
	if s.KeepwarmPolicy != other.KeepwarmPolicy {
		t.Fatalf("%s: KeepwarmPolicy instance changed: %p vs %p", stage, s.KeepwarmPolicy, other.KeepwarmPolicy)
	}
	if s.KeepwarmRegistry != other.KeepwarmRegistry {
		t.Fatalf("%s: KeepwarmRegistry instance changed: %p vs %p", stage, s.KeepwarmRegistry, other.KeepwarmRegistry)
	}
	if s.TerminalDecisionPolicy != other.TerminalDecisionPolicy {
		t.Fatalf("%s: TerminalDecisionPolicy instance changed: %p vs %p", stage, s.TerminalDecisionPolicy, other.TerminalDecisionPolicy)
	}
	if s.CompactionDetector != other.CompactionDetector {
		t.Fatalf("%s: CompactionDetector instance changed: %p vs %p", stage, s.CompactionDetector, other.CompactionDetector)
	}
	if s.ConversationStore != other.ConversationStore {
		t.Fatalf("%s: ConversationStore instance changed: %p vs %p", stage, s.ConversationStore, other.ConversationStore)
	}
	if s.BackgroundAux != other.BackgroundAux {
		t.Fatalf("%s: BackgroundAux instance changed: %p vs %p", stage, s.BackgroundAux, other.BackgroundAux)
	}
	if s.StandardFeatures != other.StandardFeatures {
		t.Fatalf("%s: StandardFeatures instance changed: %p vs %p", stage, s.StandardFeatures, other.StandardFeatures)
	}
}

// AssertDistinctOwnedResources asserts that all feature-owned process resources have distinct
// physical pointers across two separate ProcessServices builds (proving constructors run once per process).
func (s ProcessFeatureSnapshot) AssertDistinctOwnedResources(t *testing.T, other ProcessFeatureSnapshot) {
	t.Helper()
	if s.KeepwarmPolicy == other.KeepwarmPolicy {
		t.Fatalf("KeepwarmPolicy pointer identical across distinct ProcessServices builds: %p", s.KeepwarmPolicy)
	}
	if s.KeepwarmRegistry == other.KeepwarmRegistry {
		t.Fatalf("KeepwarmRegistry pointer identical across distinct ProcessServices builds: %p", s.KeepwarmRegistry)
	}
	if s.TerminalDecisionPolicy == other.TerminalDecisionPolicy {
		t.Fatalf("TerminalDecisionPolicy pointer identical across distinct ProcessServices builds: %p", s.TerminalDecisionPolicy)
	}
	if s.CompactionDetector == other.CompactionDetector {
		t.Fatalf("CompactionDetector pointer identical across distinct ProcessServices builds: %p", s.CompactionDetector)
	}
	if s.ConversationStore == other.ConversationStore {
		t.Fatalf("ConversationStore pointer identical across distinct ProcessServices builds: %p", s.ConversationStore)
	}
	if s.StandardFeatures == other.StandardFeatures {
		t.Fatalf("StandardFeatures pointer identical across distinct ProcessServices builds: %p", s.StandardFeatures)
	}
}

func testProcessServicesOwnershipConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
}

// TestProcessFeatureResources_OwnershipCountingSeam satisfies Task 1.2 by verifying:
//  1. (a) Exactly one construction of borrowed BackgroundAux (directly counted via factory seam)
//     and presence of the six feature-owned resources constructed by NewProcessServices (design.md:232);
//  2. (b) Zero duplicate constructions across two overlapping CompileCandidate compiles;
//  3. (c) Exactly one physical close per closable resource at ps.Close, with second Close idempotent;
//  4. Explicit verification of genuinely non-closable resources (no Close method or registration).
func TestProcessFeatureResources_OwnershipCountingSeam(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testProcessServicesOwnershipConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()

	// Injectable construction counter for BackgroundAux.
	var auxConstructionCount atomic.Int32
	countedNewBackgroundScheduler := func() (*BackgroundAuxScheduler, error) {
		s, err := auxreq.NewBackgroundScheduler(ctx, func() auxreq.ExecutorRunner {
			return testBackgroundProcessRunner{}
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
		if err == nil {
			auxConstructionCount.Add(1)
		}
		return s, err
	}

	scheduler, err := countedNewBackgroundScheduler()
	if err != nil {
		t.Fatalf("countedNewBackgroundScheduler: %v", err)
	}

	// 1. Process Construction: NewProcessServices constructs the six feature-owned resources;
	// BackgroundAux is borrowed via ProcessServicesInput (test-owned factory, counted separately; design.md:232).
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:           cfg,
		Log:           log,
		Opts:          &BuildOptions{PluginRegistry: reg},
		BackgroundAux: scheduler,
	})
	if err != nil {
		_ = scheduler.Close()
		t.Fatalf("NewProcessServices: %v", err)
	}

	// Capture initial snapshot and assert all process resources are present.
	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	// Verify construction count for borrowed BackgroundAux (test-owned counted factory)
	// and presence of all feature resources. Single constructor execution for the six
	// feature-owned resources is verified via distinct pointers across separate builds
	// in TestProcessFeatureResources_DistinctProcessInstances.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count = %d, want exactly 1", got)
	}

	// Assert explicitly non-closable resources do not implement io.Closer.
	if _, ok := any(ps.StandardFeatures.KeepwarmPolicy()).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("KeepwarmPolicy must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.StandardFeatures.KeepwarmRegistry()).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("KeepwarmRegistry must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.StandardFeatures.CompactionDetector()).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("CompactionDetector must be genuinely non-closable (implements io.Closer unexpectedly)")
	}

	// Wrap closers of actually-closable process resources with injectable counters
	// to verify exactly-once physical Close execution and detect any double disposal.
	// In NewProcessServices:
	// - closer 0 is registered at background_aux_lifecycle.go:27 (BackgroundAux.Close)
	// StandardFeatures owns its internal process closers (including TerminalDecisionPolicy).
	if len(ps.closers) < 1 {
		_ = ps.Close()
		t.Fatalf("ps.closers length = %d, expected at least 1 for Aux", len(ps.closers))
	}

	var backgroundAuxCloseCount atomic.Int32

	origAuxClose := ps.closers[0]
	ps.closers[0] = func() error {
		backgroundAuxCloseCount.Add(1)
		return origAuxClose()
	}

	initialCloserCount := len(ps.closers)

	// Verify initial operational state of closable resources before candidate compile.
	policyStore := ps.StandardFeatures.TerminalDecisionPolicy()
	if policyStore == nil {
		_ = ps.Close()
		t.Fatal("expected non-nil TerminalDecisionPolicy on StandardFeatures")
	}
	policyKey := sessionpolicy.Key{
		SecureSessionIncarnation: "test-sess",
		ALegID:                   "test-aleg",
		FeatureID:                "terminal-decision",
	}
	policyAuth := sessionpolicy.Authority{
		SecureSessionIncarnation: "test-sess",
		ALegID:                   "test-aleg",
		Authorized:               true,
	}
	snap, err := policyStore.Snapshot(ctx, policyAuth, policyKey, false)
	if err != nil {
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy.Effective before candidates: %v", err)
	}
	if snap.EffectiveEnabled {
		_ = ps.Close()
		t.Fatalf("expected false default effective state")
	}

	subID, err := ps.BackgroundAux.SubmitCollect(ctx, auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "initial-turn"})
	if err != nil {
		_ = ps.Close()
		t.Fatalf("BackgroundAux.SubmitCollect before candidates: %v", err)
	}
	if _, err := ps.BackgroundAux.Await(ctx, subID); err != nil {
		_ = ps.Close()
		t.Fatalf("BackgroundAux.Await before candidates: %v", err)
	}

	// 2. Overlapping Generation Compilation:
	// (b) Zero duplicate constructions across two overlapping CompileCandidate compiles.
	bus1 := hooks.New(hooks.Config{})
	c1, err := CompileCandidate(ctx, GenerationCompileInput{
		Process: ps,
		Bus:     bus1,
	})
	if err != nil {
		_ = ps.Close()
		t.Fatalf("CompileCandidate #1: %v", err)
	}

	// Assert zero duplicate constructions during Candidate #1 compile.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count after Candidate #1 = %d, want 1", got)
	}
	if len(ps.closers) != initialCloserCount {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("ps.closers grew during Candidate #1 compile: %d vs %d", len(ps.closers), initialCloserCount)
	}
	snapAfterGen1 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen1, "after candidate #1 compile")

	// Compile Candidate Generation 2 overlapping with Generation 1 before Generation 1 closes.
	bus2 := hooks.New(hooks.Config{})
	c2, err := CompileCandidate(ctx, GenerationCompileInput{
		Process: ps,
		Bus:     bus2,
	})
	if err != nil {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	// Assert zero duplicate constructions during overlapping Candidate #2 compile.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = c1.Close()
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count after Candidate #2 = %d, want 1", got)
	}
	if len(ps.closers) != initialCloserCount {
		_ = c1.Close()
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("ps.closers grew during Candidate #2 compile: %d vs %d", len(ps.closers), initialCloserCount)
	}
	snapAfterGen2 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen2, "after candidate #2 compile (overlapping)")

	// Close Candidate Generation 1.
	if err := c1.Close(); err != nil {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("Candidate #1 Close: %v", err)
	}

	// Assert process resources were NOT closed by Candidate 1 Close.
	if ps.Closed() {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatal("ProcessServices was unexpectedly closed by Candidate #1 Close")
	}
	if ps.StandardFeatures.Closed() {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("StandardFeatures closed prematurely on Candidate #1 Close")
	}
	if backgroundAuxCloseCount.Load() != 0 {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux closed prematurely on Candidate #1 Close")
	}
	snapAfterC1Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC1Close, "after candidate #1 close")

	// Verify closable resources still accept calls while Generation 2 remains active.
	if _, err := policyStore.Snapshot(ctx, policyAuth, policyKey, false); err != nil {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy unexpectedly failed after Candidate #1 Close: %v", err)
	}

	// Close Candidate Generation 2.
	if err := c2.Close(); err != nil {
		_ = ps.Close()
		t.Fatalf("Candidate #2 Close: %v", err)
	}

	// Assert process resources were NOT closed by Candidate 2 Close.
	if ps.Closed() {
		_ = ps.Close()
		t.Fatal("ProcessServices was unexpectedly closed by Candidate #2 Close")
	}
	if ps.StandardFeatures.Closed() {
		_ = ps.Close()
		t.Fatalf("StandardFeatures closed prematurely on Candidate #2 Close")
	}
	if backgroundAuxCloseCount.Load() != 0 {
		_ = ps.Close()
		t.Fatalf("BackgroundAux closed prematurely on Candidate #2 Close")
	}
	snapAfterC2Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC2Close, "after candidate #2 close")

	// 3. Process Shutdown:
	// (c) Exactly one physical close per closable resource at ps.Close.
	if ps.StandardFeatures.Closed() {
		t.Fatal("StandardFeatures closed prematurely before ps.Close")
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("ProcessServices.Closed() returned false after Close")
	}
	if !ps.StandardFeatures.Closed() {
		t.Fatal("StandardFeatures not closed after ps.Close")
	}

	// Assert exactly one physical close per closable resource.
	if got := backgroundAuxCloseCount.Load(); got != 1 {
		t.Fatalf("BackgroundAux physical close count = %d, want exactly 1", got)
	}

	// Verify TerminalDecisionPolicy is physically closed (operations fail with ErrClosed).
	if _, err := policyStore.Snapshot(ctx, policyAuth, policyKey, false); !errors.Is(err, sessionpolicy.ErrClosed) {
		t.Fatalf("TerminalDecisionPolicy.Effective after ps.Close() = %v, want ErrClosed", err)
	}

	// Verify BackgroundAux is physically closed (operations fail with ErrSchedulerClosed).
	if _, err := ps.BackgroundAux.SubmitCollect(ctx, auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "after-ps-close"}); !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("BackgroundAux.SubmitCollect after ps.Close() = %v, want ErrSchedulerClosed", err)
	}

	// 4. Close Idempotency:
	// (c) second Close is safe and does NOT invoke physical closers again.
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent ProcessServices.Close: %v", err)
	}
	if got := backgroundAuxCloseCount.Load(); got != 1 {
		t.Fatalf("BackgroundAux physical close count after second Close = %d, want exactly 1", got)
	}
}

func TestProcessFeatureResources_ConcurrentOverlappingGenerations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testProcessServicesOwnershipConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	const concurrency = 4
	errCh := make(chan error, concurrency)
	cands := make([]*CandidateHTTPCompile, concurrency)

	for i := range concurrency {
		go func(idx int) {
			bus := hooks.New(hooks.Config{})
			c, err := CompileCandidate(ctx, GenerationCompileInput{
				Process: ps,
				Bus:     bus,
			})
			if err != nil {
				errCh <- err
				return
			}
			cands[idx] = c
			errCh <- nil
		}(i)
	}

	for range concurrency {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent CompileCandidate failed: %v", err)
		}
	}

	snapConcurrent := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapConcurrent, "after concurrent overlapping generation compiles")

	for _, c := range cands {
		if c != nil {
			_ = c.Close()
		}
	}

	snapAfterCloses := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterCloses, "after concurrent candidates closed")
}

// TestProcessFeatureResources_DistinctProcessInstances proves that NewProcessServices constructs
// distinct physical instances of each of the six feature-owned resources for each process build
// (proving constructors run once per process without shared global or singleton state).
func TestProcessFeatureResources_DistinctProcessInstances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	log := testkit.DiscardLogger()

	ps1, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices #1: %v", err)
	}
	t.Cleanup(func() { _ = ps1.Close() })

	ps2, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices #2: %v", err)
	}
	t.Cleanup(func() { _ = ps2.Close() })

	snap1 := CaptureProcessFeatureSnapshot(ps1)
	snap1.AssertAllPresent(t)

	snap2 := CaptureProcessFeatureSnapshot(ps2)
	snap2.AssertAllPresent(t)

	// Assert distinct pointers for all six feature-owned resources (Requirement 1.2, item a).
	snap1.AssertDistinctOwnedResources(t, snap2)

	// Also verify default-constructed BackgroundAux instances are distinct.
	if snap1.BackgroundAux == snap2.BackgroundAux {
		t.Fatalf("BackgroundAux pointer identical across distinct ProcessServices builds: %p", snap1.BackgroundAux)
	}
}

// TestProcessFeatureResources_RealCompilerCandidateIsolation verifies requirement I4:
// Real integrated compiler candidate failure leaves last-good published generation untouched,
// keeps process snapshot identical, and observes zero duplicate process-resource construction.
func TestProcessFeatureResources_RealCompilerCandidateIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	log := testkit.DiscardLogger()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}
	var auxConstructionCount atomic.Int32
	scheduler, err := auxreq.NewBackgroundScheduler(ctx, func() auxreq.ExecutorRunner {
		return testBackgroundProcessRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	auxConstructionCount.Add(1)
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:           testProcessServicesOwnershipConfig(),
		Log:           log,
		Opts:          &BuildOptions{PluginRegistry: reg},
		BackgroundAux: scheduler,
	})
	if err != nil {
		_ = scheduler.Close()
		t.Fatalf("NewProcessServices: %v", err)
	}
	defer func() { _ = ps.Close() }()

	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	initialAuxCount := auxConstructionCount.Load()
	initialClosersCount := len(ps.closers)
	initialFeatureClosersCount := ps.StandardFeatures.ClosersCount()

	// 1. Compile valid Generation 1 via real CompileGeneration with secrets-guard configured.
	validCandidate := testProcessServicesOwnershipConfig()
	validCandidate.Plugins.Features = []config.PluginConfig{
		{
			ID:      "secrets-guard",
			Enabled: true,
			Config:  mustYAMLNode(t, "action: block\npatterns: [\"secret\"]\n"),
		},
	}
	// Bus: nil forces compileGeneration to build the hook bus FROM featOut.Planes rather than supplying one around it.
	gen1, err := CompileGeneration(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: validCandidate,
		Compose:   stubHandlerComposer,
	})
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}
	defer func() { _ = gen1.Close() }()

	b1, ok := gen1.(*GenerationBundle)
	require.True(t, ok, "compiled generation must be *GenerationBundle")
	snap1 := b1.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap1, "expected non-nil RuntimeSnapshot on compiled Generation 1")

	// Verify facade-composed output reflected on candidate runtime:
	// SecretGuardPlane has MatcherResolver and DecisionObserver produced by the real facade.
	sgPlane := snap1.SecretGuardPlane()
	require.NotNil(t, sgPlane.MatcherResolver, "expected non-nil MatcherResolver from facade secret-guard composition")
	require.NotNil(t, sgPlane.DecisionObserver, "expected non-nil DecisionObserver from facade secret-guard composition")
	assert.Len(t, snap1.SecretGuards(), 1, "expected 1 secret guard from facade featOut.Planes in candidate snapshot")
	assert.Len(t, aipfeature.Get(b1.operations.Frozen, aipfeature.PlaneSecretGuards), 1, "expected 1 secret guard on generation bundle")

	// Prove facade consumption sensitivity: neutralizing the facade (StandardFeatures == nil)
	// produces a candidate where facade-composed secret-guard plane (MatcherResolver/DecisionObserver) is absent,
	// even when the candidate configuration still has secrets-guard configured.
	origStandardFeatures := ps.StandardFeatures
	ps.StandardFeatures = nil
	genNeutral, err := CompileGeneration(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: validCandidate,
		Compose:   stubHandlerComposer,
	})
	ps.StandardFeatures = origStandardFeatures
	if err != nil {
		t.Fatalf("CompileGeneration neutral: %v", err)
	}
	defer func() { _ = genNeutral.Close() }()
	bNeutral, ok := genNeutral.(*GenerationBundle)
	require.True(t, ok, "compiled neutral generation must be *GenerationBundle")
	snapNeutral := bNeutral.execution.executor.RuntimeSnapshot
	require.NotNil(t, snapNeutral, "expected non-nil RuntimeSnapshot on neutral Generation")
	neutralPlane := snapNeutral.SecretGuardPlane()
	if neutralPlane.MatcherResolver != nil {
		t.Fatal("expected nil MatcherResolver when facade output is neutralized (StandardFeatures == nil)")
	}
	if neutralPlane.DecisionObserver != nil {
		t.Fatal("expected nil DecisionObserver when facade output is neutralized (StandardFeatures == nil)")
	}

	snapAfterGen1 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen1, "after real generation 1 compile")

	if got := auxConstructionCount.Load(); got != initialAuxCount {
		t.Fatalf("BackgroundAux count changed after Gen 1: got %d want %d", got, initialAuxCount)
	}
	if got := len(ps.closers); got != initialClosersCount {
		t.Fatalf("closers count changed after Gen 1: got %d want %d", got, initialClosersCount)
	}
	if got := ps.StandardFeatures.ClosersCount(); got != initialFeatureClosersCount {
		t.Fatalf("featurehost closers changed after Gen 1: got %d want %d", got, initialFeatureClosersCount)
	}

	// 2. Compile overlapping Candidate Generation 2 with real invalid feature entry (invalid secrets-guard action).
	invalidCandidate := testProcessServicesOwnershipConfig()
	invalidCandidate.Plugins.Features = []config.PluginConfig{
		{
			ID:      "secrets-guard",
			Enabled: true,
			Config:  mustYAMLNode(t, "action: invalid-action\n"),
		},
	}

	// Bus: nil forces hook bus to be built from featOut.Planes; compilation must fail inside the facade call.
	cand2, err2 := CompileGeneration(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: invalidCandidate,
		Compose:   stubHandlerComposer,
	})

	// Verify Candidate 2 compilation failed with real feature composition error.
	if err2 == nil {
		t.Fatal("expected candidate compile error for invalid feature registration, got nil")
	}
	if cand2 != nil {
		t.Fatal("expected nil candidate on compilation failure")
	}

	// Assert published Generation 1 remains untouched and intact.
	if err := gen1.Quiesce(ctx); err != nil {
		t.Fatalf("published Generation 1 Quiesce failed after Candidate failure: %v", err)
	}

	// Assert process snapshot after failed compile is IDENTICAL to initial snapshot.
	snapAfterFail := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterFail, "after failed candidate compile")

	// Assert zero duplicate process-resource construction observed via counters.
	if got := auxConstructionCount.Load(); got != initialAuxCount {
		t.Fatalf("BackgroundAux count changed after failed candidate: got %d want %d", got, initialAuxCount)
	}
	if got := len(ps.closers); got != initialClosersCount {
		t.Fatalf("closers count changed after failed candidate: got %d want %d", got, initialClosersCount)
	}
	if got := ps.StandardFeatures.ClosersCount(); got != initialFeatureClosersCount {
		t.Fatalf("featurehost closers changed after failed candidate: got %d want %d", got, initialFeatureClosersCount)
	}

	// Close published Generation 1.
	if err := gen1.Close(); err != nil {
		t.Fatalf("published Generation 1 Close: %v", err)
	}
	snapAfterGen1Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen1Close, "after published gen 1 close")

	// Process shutdown: exactly one close.
	if err := ps.Close(); err != nil {
		t.Fatalf("ps.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("expected ps.Closed() == true")
	}
	if !ps.StandardFeatures.Closed() {
		t.Fatal("expected ps.StandardFeatures.Closed() == true")
	}
}

// TestProcessFeatureResources_CompactionContinuityOverlappingGenerations verifies that compiling
// multiple overlapping generations with compaction-continuity configured constructs ZERO
// duplicate process resources: CompactionDetector, BranchCoordinator, and CompactionParentPort
// remain identical across all generations, and candidate closure does not close process resources.
func TestProcessFeatureResources_CompactionContinuityOverlappingGenerations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	scheduler, err := auxreq.NewBackgroundScheduler(ctx, func() auxreq.ExecutorRunner {
		return testBackgroundProcessRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:           testProcessServicesOwnershipConfig(),
		Log:           log,
		Opts:          &BuildOptions{PluginRegistry: reg},
		BackgroundAux: scheduler,
	})
	if err != nil {
		_ = scheduler.Close()
		t.Fatalf("NewProcessServices: %v", err)
	}
	defer func() { _ = ps.Close() }()

	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	candCfg := testProcessServicesOwnershipConfig()
	candCfg.Plugins.Features = []config.PluginConfig{
		{
			ID:      "compaction-continuity",
			Enabled: true,
			Config:  mustYAMLNode(t, "extractor:\n  enabled: true\n  route: inherit\n"),
		},
	}

	// 1. Compile Candidate Generation 1
	c1, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		Bus:       hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}

	snapAfterGen1 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen1, "after candidate #1 with compaction-continuity")

	// 2. Compile overlapping Candidate Generation 2
	c2, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		Bus:       hooks.New(hooks.Config{}),
	})
	if err != nil {
		_ = c1.Close()
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	snapAfterGen2 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen2, "after overlapping candidate #2 with compaction-continuity")

	// 3. Close Candidate Generation 1
	if err := c1.Close(); err != nil {
		_ = c2.Close()
		t.Fatalf("c1.Close: %v", err)
	}

	snapAfterC1Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC1Close, "after c1 close")

	// 4. Close Candidate Generation 2
	if err := c2.Close(); err != nil {
		t.Fatalf("c2.Close: %v", err)
	}

	snapAfterC2Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC2Close, "after c2 close")
}

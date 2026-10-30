package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

const (
	backendResourceHighCardinalityN = 100
	backendResourceChangedK         = 3
	backendResourceSyntheticKind    = "synthetic-per-instance-reload"
)

// backendResourceCounters are deliberately operation-shaped rather than time
// shaped. Physical counters come from the injected discovered DialSession and
// the tracking session; lease evidence is asserted from the private pool state.
type backendResourceCounters struct {
	factoryInvocations atomic.Int64
	physicalBuilds     atomic.Int64
	configures         atomic.Int64
	physicalCleanups   atomic.Int64
}

type backendResourceOperationCounts struct {
	FactoryInvocations int64
	PhysicalBuilds     int64
	Activations        int64
	Configures         int64
	PhysicalCleanups   int64
	CurrentEntries     int
	OwnedEntries       int
	Claims             int
}

func (c *backendResourceCounters) snapshot(activations int64) backendResourceOperationCounts {
	return backendResourceOperationCounts{
		FactoryInvocations: c.factoryInvocations.Load(),
		PhysicalBuilds:     c.physicalBuilds.Load(),
		Activations:        activations,
		Configures:         c.configures.Load(),
		PhysicalCleanups:   c.physicalCleanups.Load(),
	}
}

type backendResourceTrackingSession struct {
	backendplugin.ConfiguredInstance
	counters *backendResourceCounters
}

func (s *backendResourceTrackingSession) Close(ctx context.Context) error {
	s.counters.physicalCleanups.Add(1)
	return s.ConfiguredInstance.Close(ctx)
}

type backendResourceFixture struct {
	process  *ProcessServices
	host     *processhost.Host
	launcher *processhost.TestLauncher
	counters *backendResourceCounters
	pool     *backendResourcePool
}

func newBackendResourceFixture(tb testing.TB) *backendResourceFixture {
	tb.Helper()
	return newBackendResourceFixtureWithMode(tb, bpkit.ModeValid)
}

func newBackendResourceFixtureWithMode(tb testing.TB, mode bpkit.Mode) *backendResourceFixture {
	tb.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		tb.Fatalf("install standard bundle: %v", err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		tb.Fatalf("register local stub: %v", err)
	}
	launcher := &processhost.TestLauncher{PID: 9821}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	counters := &backendResourceCounters{}
	fake := &bpkit.FakeService{Mode: mode}
	dial := func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		// InstallDiscoveredExports has no public factory probe. The generic
		// discovered DialSession seam is the physical factory/build choke point:
		// one call means one configured session and one adapter build.
		counters.factoryInvocations.Add(1)
		counters.physicalBuilds.Add(1)
		counters.configures.Add(1)
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems, backendplugin.FeatureExactOpenResponsesFields},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return &backendResourceTrackingSession{ConfiguredInstance: inst, counters: counters}, profile, nil
	}
	resourcePool := newBackendResourcePool()
	if err := installDiscoveredExportsWithPool(reg, host, []ValidatedExport{{
		Kind:    backendResourceSyntheticKind,
		Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone, AccessScope: pluginreg.BackendAccessLocalOnly},
		Artifact: &trust.VerifiedArtifact{
			DigestHex: "synthetic-per-instance-reload-digest",
		},
		Model: processhost.ProcessModelPerInstance,
	}}, DiscoveredInstallOptions{DialSession: dial}, resourcePool); err != nil {
		tb.Fatalf("install synthetic discovered export: %v", err)
	}

	processCfg := backendResourceProcessBaseConfig()
	process, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:                processCfg,
		Log:                testkit.DiscardLogger(),
		PluginHost:         host,
		PluginResourcePool: resourcePool,
		Opts:               &BuildOptions{PluginRegistry: reg},
		Tracing: ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		_ = host.Close()
		tb.Fatalf("NewProcessServices: %v", err)
	}
	fixture := &backendResourceFixture{process: process, host: host, launcher: launcher, counters: counters, pool: resourcePool}
	tb.Cleanup(func() { _ = process.Close() })
	return fixture
}

func backendResourceProcessBaseConfig() *config.Config {
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_ = config.Validate(cfg)
	return cfg
}

func (f *backendResourceFixture) counts() backendResourceOperationCounts {
	counts := f.counters.snapshot(f.launcher.Launches.Load())
	f.pool.mu.Lock()
	counts.CurrentEntries = len(f.pool.current)
	counts.OwnedEntries = len(f.pool.owned)
	for _, entry := range f.pool.current {
		counts.Claims += entry.claims
	}
	f.pool.mu.Unlock()
	return counts
}

func (f *backendResourceFixture) compile(tb testing.TB, candidate *config.Config, live bool) GenerationRuntime {
	tb.Helper()
	var liveKinds map[string]int
	if live {
		liveKinds = map[string]int{backendResourceSyntheticKind: backendResourceHighCardinalityN}
	}
	gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:          f.process,
		Candidate:        candidate,
		Compose:          stubHandlerComposer,
		LiveFactoryKinds: liveKinds,
	})
	if err != nil {
		tb.Fatalf("CompileGeneration: %v", err)
	}
	return gen
}

func backendResourceCandidate(tb testing.TB, materialVersion int, changed map[int]struct{}) *config.Config {
	tb.Helper()
	backends := make([]config.PluginConfig, 0, backendResourceHighCardinalityN)
	for i := range backendResourceHighCardinalityN {
		value := fmt.Sprintf("stable-%03d", i)
		if _, ok := changed[i]; ok {
			value = fmt.Sprintf("changed-v%d-%03d", materialVersion, i)
		}
		backends = append(backends, config.PluginConfig{
			Kind:    backendResourceSyntheticKind,
			ID:      fmt.Sprintf("synthetic-%03d", i),
			Enabled: true,
			Config:  backendResourceConfigNode(tb, value),
		})
	}
	maxAttempts := 3
	if materialVersion != 0 {
		// This is unrelated material: no connector-defining field changes in the
		// unchanged matrix, while the old generation remains retained.
		maxAttempts = 4
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  maxAttempts,
			DefaultRoute: "synthetic-000:fake-model",
		},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  backends,
		},
	}
	if err := config.Validate(cfg); err != nil {
		tb.Fatalf("validate synthetic candidate: %v", err)
	}
	return cfg
}

func backendResourceConfigNode(tb testing.TB, value string) yaml.Node {
	tb.Helper()
	var node yaml.Node
	raw := fmt.Sprintf("value: %q\n", value)
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		tb.Fatalf("yaml: %v", err)
	}
	for node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 || node.Content[0] == nil {
			tb.Fatal("empty synthetic backend config")
		}
		node = *node.Content[0]
	}
	return node
}

func logBackendResourceCounts(tb testing.TB, label string, counts backendResourceOperationCounts) {
	tb.Helper()
	tb.Logf("%s: factory_invocations=%d physical_builds=%d activations=%d configures=%d physical_cleanups=%d current_entries=%d owned_entries=%d claims=%d", label, counts.FactoryInvocations, counts.PhysicalBuilds, counts.Activations, counts.Configures, counts.PhysicalCleanups, counts.CurrentEntries, counts.OwnedEntries, counts.Claims)
}

// TestBackendResourceHighCardinalityBaselineCurrentReconstruction preserves
// the pre-reuse O(N) expectation as historical evidence while asserting that
// the implementation now observes zero physical churn for the same reload.
func TestBackendResourceHighCardinalityBaselineCurrentReconstruction(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	initial := fixture.counts()
	first := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	afterFirst := fixture.counts()
	second := fixture.compile(t, backendResourceCandidate(t, 1, nil), true)
	afterSecond := fixture.counts()

	if got := afterFirst.PhysicalBuilds - initial.PhysicalBuilds; got != backendResourceHighCardinalityN {
		t.Fatalf("initial physical builds=%d want %d", got, backendResourceHighCardinalityN)
	}
	historicalBaselineDelta := int64(backendResourceHighCardinalityN)
	t.Logf("historical pre-reuse baseline: unrelated reload would rebuild %d physical resources", historicalBaselineDelta)
	for label, got := range map[string]int64{
		"factory invocations": afterSecond.FactoryInvocations - afterFirst.FactoryInvocations,
		"physical builds":     afterSecond.PhysicalBuilds - afterFirst.PhysicalBuilds,
		"activations":         afterSecond.Activations - afterFirst.Activations,
		"configures":          afterSecond.Configures - afterFirst.Configures,
	} {
		if got != 0 {
			t.Fatalf("post-reuse unrelated reload %s=%d want 0", label, got)
		}
	}
	logBackendResourceCounts(t, "baseline after overlapping generations", afterSecond)

	if err := second.Close(); err != nil {
		t.Fatalf("close second generation: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first generation: %v", err)
	}
	final := fixture.counts()
	logBackendResourceCounts(t, "baseline after generation cleanup", final)
	if got := final.PhysicalCleanups - initial.PhysicalCleanups; got != backendResourceHighCardinalityN {
		t.Fatalf("post-reuse physical cleanups=%d want %d", got, backendResourceHighCardinalityN)
	}
	if final.CurrentEntries != 0 || final.OwnedEntries != 0 || final.Claims != 0 {
		t.Fatalf("post-reuse pool after cleanup current=%d owned=%d claims=%d, want 0/0/0", final.CurrentEntries, final.OwnedEntries, final.Claims)
	}
}

// TestBackendResourceHighCardinalityReloadUnchanged proves that N unchanged
// connector rows do not trigger any new physical build, launch, or Configure
// operation across an overlapping generation reload.
func TestBackendResourceHighCardinalityReloadUnchanged(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	first := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	beforeReload := fixture.counts()
	second := fixture.compile(t, backendResourceCandidate(t, 1, nil), true)
	afterReload := fixture.counts()
	if beforeReload.CurrentEntries != backendResourceHighCardinalityN || beforeReload.OwnedEntries != backendResourceHighCardinalityN || beforeReload.Claims != backendResourceHighCardinalityN {
		t.Fatalf("initial pool observation current=%d owned=%d claims=%d, want %d/%d/%d", beforeReload.CurrentEntries, beforeReload.OwnedEntries, beforeReload.Claims, backendResourceHighCardinalityN, backendResourceHighCardinalityN, backendResourceHighCardinalityN)
	}

	for label, got := range map[string]int64{
		"factory invocations": afterReload.FactoryInvocations - beforeReload.FactoryInvocations,
		"physical builds":     afterReload.PhysicalBuilds - beforeReload.PhysicalBuilds,
		"activations":         afterReload.Activations - beforeReload.Activations,
		"configures":          afterReload.Configures - beforeReload.Configures,
	} {
		if got != 0 {
			t.Errorf("unchanged reload %s=%d want 0", label, got)
		}
	}
	if afterReload.CurrentEntries != backendResourceHighCardinalityN || afterReload.OwnedEntries != backendResourceHighCardinalityN || afterReload.Claims != 2*backendResourceHighCardinalityN {
		t.Fatalf("unchanged reload pool observation current=%d owned=%d claims=%d, want %d/%d/%d", afterReload.CurrentEntries, afterReload.OwnedEntries, afterReload.Claims, backendResourceHighCardinalityN, backendResourceHighCardinalityN, 2*backendResourceHighCardinalityN)
	}
	logBackendResourceCounts(t, "unchanged reload", afterReload)
	if err := second.Close(); err != nil {
		t.Fatalf("close unchanged second generation: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close unchanged first generation: %v", err)
	}
	final := fixture.counts()
	logBackendResourceCounts(t, "unchanged reload after generation cleanup", final)
	if got := final.PhysicalCleanups - beforeReload.PhysicalCleanups; got != backendResourceHighCardinalityN {
		t.Fatalf("unchanged reload physical cleanups=%d want %d", got, backendResourceHighCardinalityN)
	}
	if final.CurrentEntries != 0 || final.OwnedEntries != 0 || final.Claims != 0 {
		t.Fatalf("unchanged reload pool after cleanup current=%d owned=%d claims=%d, want 0/0/0", final.CurrentEntries, final.OwnedEntries, final.Claims)
	}
}

// TestBackendResourceHighCardinalityReloadChanged proves that only K changed
// identities are physically replaced while the remaining N-K rows are reused.
func TestBackendResourceHighCardinalityReloadChanged(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	first := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	beforeReload := fixture.counts()
	changed := map[int]struct{}{7: {}, 41: {}, 89: {}}
	second := fixture.compile(t, backendResourceCandidate(t, 2, changed), true)
	afterReload := fixture.counts()
	if beforeReload.CurrentEntries != backendResourceHighCardinalityN || beforeReload.OwnedEntries != backendResourceHighCardinalityN || beforeReload.Claims != backendResourceHighCardinalityN {
		t.Fatalf("initial pool observation current=%d owned=%d claims=%d, want %d/%d/%d", beforeReload.CurrentEntries, beforeReload.OwnedEntries, beforeReload.Claims, backendResourceHighCardinalityN, backendResourceHighCardinalityN, backendResourceHighCardinalityN)
	}

	for label, got := range map[string]int64{
		"factory invocations": afterReload.FactoryInvocations - beforeReload.FactoryInvocations,
		"physical builds":     afterReload.PhysicalBuilds - beforeReload.PhysicalBuilds,
		"activations":         afterReload.Activations - beforeReload.Activations,
		"configures":          afterReload.Configures - beforeReload.Configures,
	} {
		if got != backendResourceChangedK {
			t.Errorf("K-changed reload %s=%d want %d", label, got, backendResourceChangedK)
		}
	}
	if afterReload.CurrentEntries != backendResourceHighCardinalityN+backendResourceChangedK || afterReload.OwnedEntries != backendResourceHighCardinalityN+backendResourceChangedK || afterReload.Claims != 2*backendResourceHighCardinalityN {
		t.Fatalf("K-changed reload pool observation current=%d owned=%d claims=%d, want %d/%d/%d", afterReload.CurrentEntries, afterReload.OwnedEntries, afterReload.Claims, backendResourceHighCardinalityN+backendResourceChangedK, backendResourceHighCardinalityN+backendResourceChangedK, 2*backendResourceHighCardinalityN)
	}
	logBackendResourceCounts(t, "K-changed reload", afterReload)
	if err := second.Close(); err != nil {
		t.Fatalf("close changed second generation: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close changed first generation: %v", err)
	}
	final := fixture.counts()
	logBackendResourceCounts(t, "K-changed reload after generation cleanup", final)
	if got := final.PhysicalCleanups - beforeReload.PhysicalCleanups; got != backendResourceHighCardinalityN+backendResourceChangedK {
		t.Fatalf("K-changed reload physical cleanups=%d want %d", got, backendResourceHighCardinalityN+backendResourceChangedK)
	}
	if final.CurrentEntries != 0 || final.OwnedEntries != 0 || final.Claims != 0 {
		t.Fatalf("K-changed reload pool after cleanup current=%d owned=%d claims=%d, want 0/0/0", final.CurrentEntries, final.OwnedEntries, final.Claims)
	}
}

// TestBackendResourceDiscoveredInvalidationDetachesExactIncarnation is the
// Task 3.2 integration characterization. A stream failure invalidates the
// old adapter; the exact pooled incarnation must detach while its generation
// lease remains alive, allowing one fresh same-config incarnation. A delayed
// callback from the old adapter must not detach that replacement.
func TestBackendResourceDiscoveredInvalidationDetachesExactIncarnation(t *testing.T) {
	fixture := newBackendResourceFixtureWithMode(t, bpkit.ModeProcessExit)
	const instanceID = "synthetic-invalidation"
	configNode := backendResourceConfigNode(t, "stable-invalidation")
	first, err := fixture.process.FactoryCatalog.BuildBackendWithLifecycle(
		backendResourceSyntheticKind, instanceID, configNode, nil, pluginreg.BackendFactoryDeps{},
	)
	if err != nil {
		t.Fatalf("first discovered build: %v", err)
	}
	oldEntry, oldClaims, oldOwned := backendResourceSingleEntrySnapshot(t, fixture.pool)
	if oldEntry == nil || oldClaims != 1 || oldOwned != 1 {
		t.Fatalf("old pool observation entry=%v claims=%d owned=%d, want entry/1/1", oldEntry != nil, oldClaims, oldOwned)
	}

	call := aipapi.Call{
		ID:         "invalidation-call",
		Session:    aipapi.SessionRef{ALegID: "invalidation-aleg"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trigger")}}},
	}
	stream, err := first.Backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: instanceID, Model: "fake-model"},
		Key:     instanceID + ":fake-model",
	})
	if err != nil {
		t.Fatalf("old Open: %v", err)
	}
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("old stream error=%v, want process-exit failure", err)
	}
	_ = stream.Close()

	current, claims, owned := backendResourceSingleEntrySnapshot(t, fixture.pool)
	oldClaimsNow := backendResourceEntryClaims(t, fixture.pool, oldEntry)
	if current != nil || claims != 0 || oldClaimsNow != 1 || owned != 1 {
		t.Fatalf("after old invalidation current=%v current_claims=%d old_claims=%d owned=%d, want nil/0/1/1", current != nil, claims, oldClaimsNow, owned)
	}

	fresh, err := fixture.process.FactoryCatalog.BuildBackendWithLifecycle(
		backendResourceSyntheticKind, instanceID, configNode, nil, pluginreg.BackendFactoryDeps{},
	)
	if err != nil {
		t.Fatalf("fresh discovered build: %v", err)
	}
	freshEntry, freshClaims, freshOwned := backendResourceSingleEntrySnapshot(t, fixture.pool)
	if freshEntry == nil {
		t.Fatal("fresh pool entry missing")
	}
	if freshEntry == oldEntry || freshEntry.incarnation == oldEntry.incarnation {
		t.Fatalf("fresh pool entry=%p incarnation=%d, old=%p incarnation=%d", freshEntry, freshEntry.incarnation, oldEntry, oldEntry.incarnation)
	}
	if freshClaims != 1 || freshOwned != 2 {
		t.Fatalf("after replacement claims=%d owned=%d, want 1/2", freshClaims, freshOwned)
	}
	if got := fixture.counters.factoryInvocations.Load(); got != 2 {
		t.Fatalf("physical factory invocations=%d, want 2 after one fresh incarnation", got)
	}
	if got := fixture.launcher.Launches.Load(); got != 2 {
		t.Fatalf("host activations=%d, want 2 after one fresh incarnation", got)
	}

	// Replaying the old adapter failure is a delayed stale callback. It must
	// leave the newer current incarnation acquirable.
	staleStream, err := first.Backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: instanceID, Model: "fake-model"},
		Key:     instanceID + ":fake-model",
	})
	if err != nil {
		t.Fatalf("stale old Open: %v", err)
	}
	_, staleErr := staleStream.Recv(context.Background())
	_ = staleStream.Close()
	if staleErr == nil || errors.Is(staleErr, io.EOF) {
		t.Fatalf("stale old stream error=%v, want process-exit failure", staleErr)
	}
	current, claims, owned = backendResourceSingleEntrySnapshot(t, fixture.pool)
	if current != freshEntry || claims != 1 || owned != 2 {
		t.Fatalf("after stale invalidation current=%p claims=%d owned=%d, want fresh/%d/2", current, claims, owned, freshClaims)
	}

	if err := first.Cleanup(); err != nil {
		t.Fatalf("old lease cleanup: %v", err)
	}
	if err := fresh.Cleanup(); err != nil {
		t.Fatalf("fresh lease cleanup: %v", err)
	}
	finalCurrent, finalClaims, finalOwned := backendResourceSingleEntrySnapshot(t, fixture.pool)
	if finalCurrent != nil || finalClaims != 0 || finalOwned != 0 {
		t.Fatalf("after lease cleanup current=%v claims=%d owned=%d, want nil/0/0", finalCurrent != nil, finalClaims, finalOwned)
	}
	if got := fixture.counters.physicalCleanups.Load(); got != 2 {
		t.Fatalf("physical cleanups=%d, want exactly 2 incarnations", got)
	}
}

func backendResourceSingleEntrySnapshot(t *testing.T, pool *backendResourcePool) (*backendResourceEntry, int, int) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	var current *backendResourceEntry
	claims := 0
	for _, entry := range pool.current {
		if current != nil {
			t.Fatalf("expected one current test entry, found multiple")
		}
		current = entry
		claims = entry.claims
	}
	return current, claims, len(pool.owned)
}

func backendResourceEntryClaims(t *testing.T, pool *backendResourcePool, want *backendResourceEntry) int {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if want == nil {
		t.Fatal("nil entry in claim snapshot")
	}
	return want.claims
}

// BenchmarkBackendResourceCandidateCompilation records candidate compile
// allocations and operation counts for the high-cardinality fixture. It has no
// wall-clock correctness threshold; deterministic operation counters are the
// acceptance evidence for reconciliation.
func BenchmarkBackendResourceCandidateCompilation(b *testing.B) {
	fixture := newBackendResourceFixture(b)
	candidate := backendResourceCandidate(b, 0, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generation, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:          fixture.process,
			Candidate:        candidate,
			Compose:          stubHandlerComposer,
			LiveFactoryKinds: map[string]int{backendResourceSyntheticKind: backendResourceHighCardinalityN},
		})
		if err != nil {
			b.Fatalf("CompileGeneration: %v", err)
		}
		b.StopTimer()
		if err := generation.Close(); err != nil {
			b.Fatalf("close generation: %v", err)
		}
		b.StartTimer()
	}
	b.StopTimer()
	logBackendResourceCounts(b, "benchmark totals", fixture.counts())
}

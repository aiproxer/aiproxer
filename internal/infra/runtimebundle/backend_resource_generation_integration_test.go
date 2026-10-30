package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestBackendResourceGeneration_CandidateRollbackReuseFailure proves the
// candidate/ledger path, rather than backendResourcePool.Acquire directly:
// an unchanged candidate that reaches HTTP composition and then fails releases
// only its reuse leases. The active generation's stream and query plane remain
// usable, and no active physical resource is cleaned up or invalidated.
func TestBackendResourceGeneration_CandidateRollbackReuseFailure(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	active := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	registerBackendResourceGenerationCleanup(t, active)
	activeBundle := backendResourceGenerationBundle(t, active)
	activeBackend := generationResourceBackend(t, activeBundle, "synthetic-000")

	stream, err := activeBackend.Open(context.Background(), backendResourceCall("candidate-rollback-stream"), routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "synthetic-000", Model: "fake-model"},
		Key:     "synthetic-000:fake-model",
	})
	if err != nil {
		t.Fatalf("open retained active stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	before := fixture.counts()
	_, err = CompileGeneration(context.Background(), GenerationCompileInput{
		Process:          fixture.process,
		Candidate:        backendResourceCandidate(t, 1, nil),
		Compose:          backendResourceFailingComposer,
		LiveFactoryKinds: map[string]int{backendResourceSyntheticKind: backendResourceHighCardinalityN},
	})
	if err == nil {
		t.Fatal("expected post-backend compose failure")
	}
	if !containsText(err, "compose request plane") {
		t.Fatalf("compose failure=%v, want compose request-plane failure", err)
	}

	after := fixture.counts()
	assertNoPhysicalReuseMutation(t, before, after)
	assertLiveClaims(t, fixture.pool, backendResourceHighCardinalityN)

	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatalf("retained active stream after candidate rollback: %v", err)
	}
	assertGenerationBackendQuery(t, activeBackend)
}

// TestBackendResourceGeneration_CandidateRollbackChangedFailure proves that a
// failed candidate with changed same-ID configuration cleans only its newly
// acquired physical identities. The old generation's identities and claims
// remain current and usable after the candidate ledger rolls back.
func TestBackendResourceGeneration_CandidateRollbackChangedFailure(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	active := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	registerBackendResourceGenerationCleanup(t, active)
	activeBundle := backendResourceGenerationBundle(t, active)
	activeBackend := generationResourceBackend(t, activeBundle, "synthetic-007")

	changed := map[int]struct{}{7: {}, 41: {}, 89: {}}
	before := fixture.counts()
	_, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:          fixture.process,
		Candidate:        backendResourceCandidate(t, 2, changed),
		Compose:          backendResourceFailingComposer,
		LiveFactoryKinds: map[string]int{backendResourceSyntheticKind: backendResourceHighCardinalityN},
	})
	if err == nil {
		t.Fatal("expected post-backend compose failure")
	}
	if !containsText(err, "compose request plane") {
		t.Fatalf("compose failure=%v, want compose request-plane failure", err)
	}

	after := fixture.counts()
	if got := after.PhysicalBuilds - before.PhysicalBuilds; got != backendResourceChangedK {
		t.Fatalf("candidate changed physical builds=%d, want %d", got, backendResourceChangedK)
	}
	if got := after.Activations - before.Activations; got != backendResourceChangedK {
		t.Fatalf("candidate changed activations=%d, want %d", got, backendResourceChangedK)
	}
	if got := after.Configures - before.Configures; got != backendResourceChangedK {
		t.Fatalf("candidate changed configures=%d, want %d", got, backendResourceChangedK)
	}
	if got := after.PhysicalCleanups - before.PhysicalCleanups; got != backendResourceChangedK {
		t.Fatalf("candidate-only physical cleanups=%d, want %d", got, backendResourceChangedK)
	}
	assertLiveClaims(t, fixture.pool, backendResourceHighCardinalityN)

	assertGenerationBackendQuery(t, activeBackend)
}

// TestBackendResourceGeneration_RetainedOldClaimsOnRemoveDisableAndChange
// proves successful overlapping recomposition does not retire old resources
// early. A candidate removes one row, disables another, and changes one
// same-ID config while the old generation remains retained.
func TestBackendResourceGeneration_RetainedOldClaimsOnRemoveDisableAndChange(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	active := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	registerBackendResourceGenerationCleanup(t, active)
	before := fixture.counts()

	candidateCfg := backendResourceCandidateWithRemovedDisabledChanged(t)
	candidate := fixture.compile(t, candidateCfg, true)
	registerBackendResourceGenerationCleanup(t, candidate)
	overlap := fixture.counts()
	if overlap.PhysicalCleanups != before.PhysicalCleanups {
		t.Fatalf("physical cleanup during overlapping successful generations=%d, want %d", overlap.PhysicalCleanups-before.PhysicalCleanups, 0)
	}
	if overlap.CurrentEntries != backendResourceHighCardinalityN+1 || overlap.OwnedEntries != backendResourceHighCardinalityN+1 {
		t.Fatalf(
			"overlap pool current=%d owned=%d, want %d/%d",
			overlap.CurrentEntries, overlap.OwnedEntries,
			backendResourceHighCardinalityN+1, backendResourceHighCardinalityN+1,
		)
	}

	// Retiring the candidate may clean its candidate-only changed incarnation,
	// but every old-generation entry must remain owned until active retires.
	if err := candidate.Close(); err != nil {
		t.Fatalf("close candidate generation: %v", err)
	}
	afterCandidate := fixture.counts()
	if got := afterCandidate.PhysicalCleanups - overlap.PhysicalCleanups; got != 1 {
		t.Fatalf("candidate-only cleanup=%d, want 1", got)
	}
	if afterCandidate.CurrentEntries != backendResourceHighCardinalityN || afterCandidate.OwnedEntries != backendResourceHighCardinalityN {
		t.Fatalf(
			"after candidate retirement current=%d owned=%d, want %d/%d",
			afterCandidate.CurrentEntries, afterCandidate.OwnedEntries,
			backendResourceHighCardinalityN, backendResourceHighCardinalityN,
		)
	}

	assertGenerationBackendQuery(t, generationResourceBackend(t, backendResourceGenerationBundle(t, active), "synthetic-099"))
	if err := active.Close(); err != nil {
		t.Fatalf("close active generation: %v", err)
	}
	afterActive := fixture.counts()
	if got := afterActive.PhysicalCleanups - afterCandidate.PhysicalCleanups; got != backendResourceHighCardinalityN {
		t.Fatalf("old-generation cleanup=%d, want %d", got, backendResourceHighCardinalityN)
	}
	if afterActive.CurrentEntries != 0 || afterActive.OwnedEntries != 0 || afterActive.Claims != 0 {
		t.Fatalf("after active retirement current=%d owned=%d claims=%d, want 0/0/0", afterActive.CurrentEntries, afterActive.OwnedEntries, afterActive.Claims)
	}
}

// TestBackendResourceGeneration_LocalProjectionsRemainDistinct proves that
// reuse shares only the physical backend functions. Handler, executor, model
// registry/catalog, routing projection, lifecycle context, and ledger remain
// generation-local across actual CompileGeneration calls.
func TestBackendResourceGeneration_LocalProjectionsRemainDistinct(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	old := compileBackendResourceProjectionGeneration(t, fixture, backendResourceCandidateWithRoute(t, "synthetic-000:fake-model"))
	registerBackendResourceGenerationCleanup(t, old)
	newGen := compileBackendResourceProjectionGeneration(t, fixture, backendResourceCandidateWithRoute(t, "synthetic-001:fake-model"))
	registerBackendResourceGenerationCleanup(t, newGen)

	oldBundle := backendResourceGenerationBundle(t, old)
	newBundle := backendResourceGenerationBundle(t, newGen)
	if oldBundle.execution.executor == newBundle.execution.executor {
		t.Fatal("generations must own distinct executors")
	}
	if oldBundle.models.models == newBundle.models.models {
		t.Fatal("generations must own distinct model registry runtimes")
	}
	if oldBundle.models.catalog != nil && oldBundle.models.catalog == newBundle.models.catalog {
		t.Fatal("generations must own distinct model catalog runtimes")
	}
	if oldBundle.ledger == newBundle.ledger {
		t.Fatal("generations must own distinct ResourceLedgers")
	}
	oldHandler, okOld := old.Handler().(*backendResourceGenerationProjectionHandler)
	if !okOld {
		t.Fatal("expected old handler to be *backendResourceGenerationProjectionHandler")
	}
	newHandler, okNew := newGen.Handler().(*backendResourceGenerationProjectionHandler)
	if !okNew {
		t.Fatal("expected new handler to be *backendResourceGenerationProjectionHandler")
	}
	if oldHandler == newHandler {
		t.Fatal("generations must own distinct handlers")
	}
	if oldHandler.input.Core.Executor == newHandler.input.Core.Executor ||
		oldHandler.input.Models.ModelRegistryRuntime == newHandler.input.Models.ModelRegistryRuntime ||
		(oldHandler.input.Models.CatalogRuntime != nil && oldHandler.input.Models.CatalogRuntime == newHandler.input.Models.CatalogRuntime) {
		t.Fatal("handler projection must retain generation-local executor/model views")
	}
	if oldBundle.Routing().DefaultRoute == newBundle.Routing().DefaultRoute {
		t.Fatalf("routing projections unexpectedly shared default route %q", oldBundle.Routing().DefaultRoute)
	}
	if oldBundle.publication.handler == newBundle.publication.handler {
		t.Fatal("publication handlers must remain generation-local")
	}

	oldBackend := generationResourceBackend(t, oldBundle, "synthetic-000")
	newBackend := generationResourceBackend(t, newBundle, "synthetic-000")
	if oldBackend.backend.Open == nil || newBackend.backend.Open == nil {
		t.Fatal("expected generation-local executable backend projections")
	}
}

// TestBackendResourceGeneration_ReuseHitIsQueryOnly documents the observable
// preparation contract over CompileGeneration: all physical construction and
// lifecycle mutation counters stay flat while generation-local model inventory
// queries may run, and pooled backend values expose no lifecycle bypass.
func TestBackendResourceGeneration_ReuseHitIsQueryOnly(t *testing.T) {
	fixture := newBackendResourceFixture(t)
	active := fixture.compile(t, backendResourceCandidate(t, 0, nil), false)
	registerBackendResourceGenerationCleanup(t, active)
	before := fixture.counts()
	candidate := fixture.compile(t, backendResourceCandidate(t, 1, nil), true)
	registerBackendResourceGenerationCleanup(t, candidate)
	after := fixture.counts()

	for label, got := range map[string]int64{
		"factory invocations": after.FactoryInvocations - before.FactoryInvocations,
		"physical builds":     after.PhysicalBuilds - before.PhysicalBuilds,
		"activations":         after.Activations - before.Activations,
		"configures":          after.Configures - before.Configures,
		"physical cleanups":   after.PhysicalCleanups - before.PhysicalCleanups,
	} {
		if got != 0 {
			t.Errorf("reuse-hit %s=%d, want 0", label, got)
		}
	}
	for _, generation := range []GenerationRuntime{active, candidate} {
		bundle := backendResourceGenerationBundle(t, generation)
		for id, backend := range bundle.execution.executor.Backends {
			if backend.Close != nil || backend.Start != nil || backend.Stop != nil || backend.CleanupIdleTransports != nil {
				t.Fatalf("pooled backend %q exposes generation lifecycle bypass", id)
			}
		}
	}
	assertGenerationBackendQuery(t, generationResourceBackend(t, backendResourceGenerationBundle(t, candidate), "synthetic-000"))
}

type backendResourceGenerationProjectionHandler struct {
	input httpcontract.StandardHTTPInput
}

func (*backendResourceGenerationProjectionHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func backendResourceFailingComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return nil, errors.New("backend-resource compose failure")
}

func backendResourceGenerationProjectionComposer(
	_ context.Context,
	_ *config.Config,
	_ *slog.Logger,
	input httpcontract.StandardHTTPInput,
) (http.Handler, error) {
	return &backendResourceGenerationProjectionHandler{input: input}, nil
}

func compileBackendResourceProjectionGeneration(t *testing.T, fixture *backendResourceFixture, candidate *config.Config) GenerationRuntime {
	t.Helper()
	generation, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:          fixture.process,
		Candidate:        candidate,
		Compose:          backendResourceGenerationProjectionComposer,
		LiveFactoryKinds: map[string]int{backendResourceSyntheticKind: backendResourceHighCardinalityN},
	})
	if err != nil {
		t.Fatalf("CompileGeneration projection: %v", err)
	}
	return generation
}

func backendResourceGenerationBundle(t *testing.T, generation GenerationRuntime) *GenerationBundle {
	t.Helper()
	bundle, ok := generation.(*GenerationBundle)
	if !ok || bundle == nil {
		t.Fatalf("generation type=%T, want *GenerationBundle", generation)
	}
	return bundle
}

func registerBackendResourceGenerationCleanup(tb testing.TB, generation GenerationRuntime) {
	tb.Helper()
	tb.Cleanup(func() { _ = generation.Close() })
}

func generationResourceBackend(t *testing.T, bundle *GenerationBundle, id string) execBackendForGeneration {
	t.Helper()
	if bundle == nil || bundle.execution.executor == nil {
		t.Fatal("generation has no executor")
	}
	backend, ok := bundle.execution.executor.Backends[id]
	if !ok {
		t.Fatalf("generation missing backend %q", id)
	}
	return execBackendForGeneration{backend: backend}
}

// execBackendForGeneration keeps test helpers from leaking the core backend
// implementation type through each test assertion.
type execBackendForGeneration struct {
	backend execbackend.Backend
}

func (b execBackendForGeneration) Open(ctx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	if b.backend.Open == nil {
		return nil, fmt.Errorf("backend Open is nil")
	}
	return b.backend.Open(ctx, call, candidate)
}

func (b execBackendForGeneration) Query(ctx context.Context) error {
	if b.backend.ModelInventory == nil {
		return fmt.Errorf("backend model inventory is nil")
	}
	_, err := b.backend.ModelInventory.LoadModels(ctx)
	return err
}

func assertGenerationBackendQuery(t *testing.T, backend execBackendForGeneration) {
	t.Helper()
	if err := backend.Query(context.Background()); err != nil {
		t.Fatalf("generation backend query: %v", err)
	}
}

func backendResourceCall(id string) aipapi.Call {
	return aipapi.Call{
		ID:         id,
		Session:    aipapi.SessionRef{ALegID: id + "-aleg"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("retained")}}},
	}
}

func backendResourceCandidateWithRoute(t *testing.T, route string) *config.Config {
	t.Helper()
	cfg := backendResourceCandidate(t, 3, nil)
	cfg.Routing.DefaultRoute = route
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate projection candidate: %v", err)
	}
	return cfg
}

func backendResourceCandidateWithRemovedDisabledChanged(t *testing.T) *config.Config {
	t.Helper()
	cfg := backendResourceCandidate(t, 4, nil)
	backends := make([]config.PluginConfig, 0, len(cfg.Plugins.Backends))
	for _, backend := range cfg.Plugins.Backends {
		switch backend.ID {
		case "synthetic-099":
			continue
		case "synthetic-098":
			backend.Enabled = false
		case "synthetic-007":
			backend.Config = backendResourceConfigNode(t, "changed-retained-007")
		}
		backends = append(backends, backend)
	}
	cfg.Plugins.Backends = backends
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate remove/disable candidate: %v", err)
	}
	return cfg
}

func assertNoPhysicalReuseMutation(t *testing.T, before, after backendResourceOperationCounts) {
	t.Helper()
	for label, got := range map[string]int64{
		"factory invocations": after.FactoryInvocations - before.FactoryInvocations,
		"physical builds":     after.PhysicalBuilds - before.PhysicalBuilds,
		"activations":         after.Activations - before.Activations,
		"configures":          after.Configures - before.Configures,
		"physical cleanups":   after.PhysicalCleanups - before.PhysicalCleanups,
	} {
		if got != 0 {
			t.Errorf("failed unchanged candidate %s=%d, want 0", label, got)
		}
	}
}

func assertLiveClaims(t *testing.T, pool *backendResourcePool, want int) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.current) != want || len(pool.owned) != want {
		t.Fatalf("pool current=%d owned=%d, want %d/%d", len(pool.current), len(pool.owned), want, want)
	}
	for identity, entry := range pool.current {
		if entry.state != backendResourceLive || entry.claims != 1 {
			t.Fatalf("entry %x state=%d claims=%d, want live/1", identity.digest[:4], entry.state, entry.claims)
		}
	}
}

func containsText(err error, text string) bool {
	return err != nil && text != "" && strings.Contains(err.Error(), text)
}

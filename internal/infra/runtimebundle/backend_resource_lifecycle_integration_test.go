package runtimebundle

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// Task 1.5 runtimebundle contracts intentionally sit above the private pool
// state-machine tests. They describe the ownership boundary that generation
// compilation must preserve when a configured external backend is reused.

// TestBackendResourceRuntimeBundle_CandidateRollbackLeavesActiveResourceUsable
// proves that an unchanged active resource and a candidate share one physical
// build, while candidate rollback releases only the candidate claim. Query
// preparation remains allowed; generation-local lifecycle callbacks are not
// part of a pool-compatible backend.
func TestBackendResourceRuntimeBundle_CandidateRollbackLeavesActiveResourceUsable(t *testing.T) {
	t.Parallel()

	id := backendResourcePoolTestIdentity(t, "runtimebundle-candidate-isolation")
	pool := newBackendResourcePool()
	t.Cleanup(func() { _ = pool.Close() })
	var builds, physicalCleanups, executions atomic.Int32
	queries := &backendResourceLifecycleQueryProbe{}

	build := func(context.Context, uint64) (execbackend.Backend, func() error, error) {
		builds.Add(1)
		backend := backendResourceLifecycleBackend(queries, &executions)
		return backend, func() error {
			physicalCleanups.Add(1)
			return nil
		}, nil
	}

	active, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("active Acquire: %v", err)
	}
	candidate, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("candidate reuse Acquire: %v", err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("physical builds=%d, want 1 for active plus candidate", got)
	}

	// A pooled generation-facing backend may retain query-shaped operations,
	// but it must not expose physical lifecycle ownership to either generation.
	for label, present := range map[string]bool{
		"Close": active.Backend.Close != nil,
		"Start": active.Backend.Start != nil,
		"Stop":  active.Backend.Stop != nil,
	} {
		if present {
			t.Fatalf("active pooled backend exposes generation-owned physical %s bypass", label)
		}
	}
	for label, present := range map[string]bool{
		"Close": candidate.Backend.Close != nil,
		"Start": candidate.Backend.Start != nil,
		"Stop":  candidate.Backend.Stop != nil,
	} {
		if present {
			t.Fatalf("candidate pooled backend exposes generation-owned physical %s bypass", label)
		}
	}
	if active.Backend.ModelInventory == nil || candidate.Backend.ModelInventory == nil {
		t.Fatal("pooled backend must retain query-shaped model inventory operations")
	}

	if _, err := candidate.Backend.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("candidate query preparation: %v", err)
	}
	if got := queries.loads.Load(); got != 1 {
		t.Fatalf("query preparation calls=%d, want 1", got)
	}
	if err := candidate.Cleanup(); err != nil {
		t.Fatalf("candidate rollback: %v", err)
	}
	if got := physicalCleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after candidate rollback, want 0", got)
	}

	// The active generation must still be able to execute and refresh its
	// query-shaped view after the candidate has been rejected.
	stream, err := active.Backend.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatalf("active execution after candidate rollback: %v", err)
	}
	if stream == nil {
		t.Fatal("active execution returned nil stream")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("active stream close: %v", err)
	}
	if _, err := active.Backend.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("active query after candidate rollback: %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("active executions=%d, want 1", got)
	}
	if got := queries.loads.Load(); got != 2 {
		t.Fatalf("active query calls=%d, want 2 total", got)
	}

	if err := active.Cleanup(); err != nil {
		t.Fatalf("active lease release: %v", err)
	}
	if got := physicalCleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after final active release, want 1", got)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("pool close after candidate rollback: %v", err)
	}
}

// TestBackendResourceRuntimeBundle_CompositeCleanupOwnedExactlyOnce proves
// that adapter/session cleanup and ActivateResult.Cleanup are one entry-owned
// physical composite. Pool fail-safe cleanup, late lease release, and a later
// processhost.Host.Close-style callback all converge without a second physical
// teardown.
func TestBackendResourceRuntimeBundle_CompositeCleanupOwnedExactlyOnce(t *testing.T) {
	t.Parallel()

	id := backendResourcePoolTestIdentity(t, "runtimebundle-composite-cleanup")
	pool := newBackendResourcePool()
	t.Cleanup(func() { _ = pool.Close() })
	var entryCleanup, adapterCleanup, hostInstanceCleanup, hostClose atomic.Int32
	var activationOnce sync.Once
	activationCleanup := func() {
		activationOnce.Do(func() { hostInstanceCleanup.Add(1) })
	}
	compositeCleanup := func() error {
		entryCleanup.Add(1)
		adapterCleanup.Add(1)
		activationCleanup()
		return nil
	}

	build := func(context.Context, uint64) (execbackend.Backend, func() error, error) {
		return execbackend.Backend{}, compositeCleanup, nil
	}
	active, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("active Acquire: %v", err)
	}
	candidate, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("candidate Acquire: %v", err)
	}
	if err := candidate.Cleanup(); err != nil {
		t.Fatalf("candidate rollback: %v", err)
	}
	if got := entryCleanup.Load(); got != 0 {
		t.Fatalf("entry cleanup calls=%d after candidate rollback, want 0", got)
	}

	// Keep the active claim outstanding so Pool.Close must exercise residual
	// entry ownership. This is the competing path that must share cleanupOnce.
	if err := pool.Close(); err != nil {
		t.Fatalf("pool fail-safe close: %v", err)
	}
	if err := active.Cleanup(); err != nil {
		t.Fatalf("late active lease release: %v", err)
	}

	// processhost.Host remains the supervisor. Its later fail-safe close may
	// revisit the same instance, but must observe the idempotent activation
	// cleanup rather than tear the physical resource down twice.
	hostClose.Add(1)
	activationCleanup()
	if err := pool.Close(); err != nil {
		t.Fatalf("idempotent pool close: %v", err)
	}

	if got := entryCleanup.Load(); got != 1 {
		t.Fatalf("entry cleanup calls=%d, want exactly 1", got)
	}
	if got := adapterCleanup.Load(); got != 1 {
		t.Fatalf("adapter/session cleanup calls=%d, want exactly 1", got)
	}
	if got := hostInstanceCleanup.Load(); got != 1 {
		t.Fatalf("ActivateResult/host-instance cleanup calls=%d, want exactly 1", got)
	}
	if got := hostClose.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d, want 1", got)
	}
}

// TestBackendResourceRuntimeBundle_NewProcessServicesTransfersPoolOwnership
// exercises the successful constructor transfer. Pool cleanup must run while
// processhost.Host is still open, then Host.Close must run before staging is
// removed. The private input field is deliberate: the connector-specific pool
// is not a public runtime resource API.
func TestBackendResourceRuntimeBundle_NewProcessServicesTransfersPoolOwnership(t *testing.T) {
	t.Parallel()

	fixture := newBackendResourceLifecycleProcessFixture(t)
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:                backendResourceLifecycleProcessConfig(),
		Log:                testkit.DiscardLogger(),
		Opts:               &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing:            ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		PluginHost:         fixture.host,
		PluginResourcePool: fixture.pool,
		PluginArtifacts:    fixture.artifacts,
		PluginStagingDir:   fixture.staging,
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	if ps == nil {
		t.Fatal("NewProcessServices returned nil ProcessServices")
	}
	t.Cleanup(func() { _ = ps.Close() })

	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !fixture.hostOpenDuringPoolCleanup.Load() {
		t.Fatal("pool physical cleanup could not use the live processhost.Host")
	}
	if !fixture.stagingPresentDuringPoolCleanup.Load() {
		t.Fatal("staging disappeared before pool cleanup; pool must close first")
	}
	assertBackendResourceLifecycleProcessClosed(t, fixture)

	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent ProcessServices.Close: %v", err)
	}
}

// TestBackendResourceRuntimeBundle_NewProcessServicesBootstrapFailureClosesPoolFirst
// freezes the pre-transfer/error path. Resources supplied to a failing
// NewProcessServices call must be released in pool -> host -> artifacts ->
// staging order even though ProcessServices is never returned.
func TestBackendResourceRuntimeBundle_NewProcessServicesBootstrapFailureClosesPoolFirst(t *testing.T) {
	t.Parallel()

	fixture := newBackendResourceLifecycleProcessFixture(t)
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:                nil,
		Log:                testkit.DiscardLogger(),
		Opts:               nil,
		PluginHost:         fixture.host,
		PluginResourcePool: fixture.pool,
		PluginArtifacts:    fixture.artifacts,
		PluginStagingDir:   fixture.staging,
	})
	if ps != nil {
		_ = ps.Close()
		t.Fatal("bootstrap failure must not return ProcessServices")
	}
	if err == nil {
		t.Fatal("expected bootstrap failure")
	}
	if !fixture.hostOpenDuringPoolCleanup.Load() {
		t.Fatal("bootstrap cleanup invoked pool cleanup after Host.Close")
	}
	if !fixture.stagingPresentDuringPoolCleanup.Load() {
		t.Fatal("bootstrap cleanup removed staging before pool cleanup")
	}
	assertBackendResourceLifecycleProcessClosed(t, fixture)
}

type backendResourceLifecycleProcessFixture struct {
	pool                            *backendResourcePool
	host                            *processhost.Host
	staging                         string
	artifacts                       []*trust.VerifiedArtifact
	hostOpenDuringPoolCleanup       atomic.Bool
	stagingPresentDuringPoolCleanup atomic.Bool
}

func newBackendResourceLifecycleProcessFixture(t *testing.T) *backendResourceLifecycleProcessFixture {
	t.Helper()
	fixture := &backendResourceLifecycleProcessFixture{
		pool: newBackendResourcePool(),
		host: processhost.NewHost(processhost.Config{
			Launcher: &processhost.TestLauncher{PID: 8711},
			Channel:  &processhost.TestChannel{},
		}),
	}
	parent := t.TempDir()
	fixture.staging = filepath.Join(parent, "aip-plugin-serve-lifecycle")
	if err := os.Mkdir(fixture.staging, 0o700); err != nil {
		t.Fatalf("create staging: %v", err)
	}
	fixture.artifacts = []*trust.VerifiedArtifact{{DigestHex: "lifecycle-artifact"}}

	resource, err := backendResourceLifecycleActivate(t, fixture.host, "pool-owned-resource")
	if err != nil {
		t.Fatalf("activate pool-owned resource: %v", err)
	}
	id := backendResourcePoolTestIdentity(t, "runtimebundle-process-owner")
	_, err = fixture.pool.Acquire(context.Background(), id, func(context.Context, uint64) (execbackend.Backend, func() error, error) {
		return execbackend.Backend{}, func() error {
			_, probeErr := backendResourceLifecycleActivate(t, fixture.host, "pool-cleanup-host-probe")
			if probeErr != nil {
				return probeErr
			}
			fixture.hostOpenDuringPoolCleanup.Store(true)
			fixture.stagingPresentDuringPoolCleanup.Store(pathExists(fixture.staging))
			// Leave the probe instance to Host.Close. The resource activation
			// itself is released by the pool-owned composite cleanup.
			if resource.Cleanup != nil {
				return resource.Cleanup()
			}
			return nil
		}, nil
	})
	if err != nil {
		t.Fatalf("acquire process-owned pool resource: %v", err)
	}
	t.Cleanup(func() {
		_ = fixture.pool.Close()
		_ = fixture.host.Close()
		_ = os.RemoveAll(fixture.staging)
	})
	return fixture
}

func assertBackendResourceLifecycleProcessClosed(t *testing.T, fixture *backendResourceLifecycleProcessFixture) {
	t.Helper()
	if pathExists(fixture.staging) {
		t.Fatalf("staging %q still exists after process cleanup", fixture.staging)
	}
	if _, err := backendResourceLifecycleActivate(t, fixture.host, "after-process-close"); err != processhost.ReasonShuttingDown {
		t.Fatalf("activation after ProcessServices cleanup err=%v, want %q", err, processhost.ReasonShuttingDown)
	}
}

func backendResourceLifecycleActivate(t *testing.T, host *processhost.Host, instanceID string) (processhost.ActivateResult, error) {
	t.Helper()
	return host.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID:  instanceID,
		Artifact:    &trust.VerifiedArtifact{DigestHex: "lifecycle-test-artifact-" + instanceID},
		Model:       processhost.ProcessModelPerInstance,
		FactoryKind: "lifecycle-test-factory",
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
}

func backendResourceLifecycleProcessConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			ID: "disabled-backend", Kind: "openai-responses", Enabled: false,
		}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type backendResourceLifecycleQueryProbe struct {
	loads atomic.Int32
}

func (p *backendResourceLifecycleQueryProbe) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	p.loads.Add(1)
	return modelinventory.Snapshot{Models: []modelinventory.Model{{CanonicalID: "query-model"}}}, nil
}

func backendResourceLifecycleBackend(queries modelinventory.Provider, executions *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		BackendPrefixes: []string{"runtimebundle-lifecycle"},
		ModelInventory:  queries,
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			executions.Add(1)
			return aipapi.NewFixedEventStream(nil), nil
		},
	}
}

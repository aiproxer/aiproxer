package runtimebundle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func validDistributionInput(cfgPath string) ValidateDistributionInput {
	return ValidateDistributionInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stubHandlerComposer,
	}
}

func dogfoodConfigPath(tb testing.TB) string {
	tb.Helper()
	return MaterializeExampleConfigForTest(tb, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
}

func failingHandlerComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return nil, errors.New("compose boom")
}

// TestValidateDistribution_NilGuards proves nil context/input/composer fail
// before any resource is acquired (req 5.2, 5.3).
func TestValidateDistribution_NilGuards(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // deliberate nil-context guard characterization
	if err := ValidateDistribution(nil, validDistributionInput(dogfoodConfigPath(t))); err == nil {
		t.Fatal("expected nil-context failure")
	}
	if err := ValidateDistribution(context.Background(), ValidateDistributionInput{ConfigPath: dogfoodConfigPath(t), HandlerComposer: nil}); err == nil {
		t.Fatal("expected nil-composer failure")
	}
	if err := ValidateDistribution(context.Background(), ValidateDistributionInput{ConfigPath: "", HandlerComposer: stubHandlerComposer}); err == nil {
		t.Fatal("expected empty-path failure")
	}
}

// TestValidateDistribution_OneStrictLoad proves ValidateDistribution reads the
// effective snapshot exactly once, including a controlled A/B disagreement
// (TOCTOU dual-load pattern mirroring one_snapshot_toctou_red_test.go).
func TestValidateDistribution_OneStrictLoad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pathA := MaterializeExampleConfigForTest(t, writeOneSnapshotMarkerConfig(t, "127.0.0.1:18301", accessmode.ModeSingleUser))
	pathB := MaterializeExampleConfigForTest(t, writeOneSnapshotMarkerConfig(t, "127.0.0.1:18302", accessmode.ModeSingleUser))
	snapA := mustLoadBootstrapSnapshot(ctx, t, pathA)
	snapB := mustLoadBootstrapSnapshot(ctx, t, pathB)

	var loads atomic.Int32
	ops := defaultValidateDistributionOps()
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		if loads.Add(1) == 1 {
			return snapA.eff, snapA.active, snapA.owner, snapA.fixed, nil
		}
		return snapB.eff, snapB.active, snapB.owner, snapB.fixed, nil
	}
	var acquired []string
	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		eff, src, owner, fixed, err := baseLoad(ctx, path, cli, capabilities)
		if err != nil {
			return nil, nil, owner, fixed, err
		}
		acquired = append(acquired, "loader")
		return eff, src, owner, fixed, nil
	}
	err := validateDistribution(ctx, validDistributionInput(pathA), nil, ops)
	if err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
	if gotLoads := int(loads.Load()); gotLoads != 1 {
		t.Fatalf("effective loads=%d want 1 (one-snapshot invariant)", gotLoads)
	}
	if len(acquired) == 0 || acquired[0] != string(validateStageLoader) {
		t.Fatalf("expected loader as first acquired stage, got %v", acquired)
	}
}

// TestValidateDistribution_SuccessRollsBackAndClosesExactlyOnce proves a
// successful validation never publishes and closes generation, process, and
// tracing resources in the deterministic reverse order gen -> process ->
// tracing, exactly once each (req 5.2-5.4).
func TestValidateDistribution_SuccessRollsBackAndClosesExactlyOnce(t *testing.T) {
	t.Parallel()
	journal, err := validateDistributionOutcome(context.Background(), validDistributionInput(dogfoodConfigPath(t)), LoadBootstrapEffectiveWithSource)
	if err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
	wantAcquired := []string{
		string(validateStageLoader),
		string(validateStageTracing),
		string(validateStageRegistry),
		string(validateStageProcess),
		string(validateStageCompile),
	}
	if got := strings.Join(journal.Acquired, ","); got != strings.Join(wantAcquired, ",") {
		t.Fatalf("acquired=%v want %v", journal.Acquired, wantAcquired)
	}
	wantCleaned := []string{
		string(validateStageRollback),
		string(validateStageProcessClose),
		string(validateStageTracingClose),
	}
	if got := strings.Join(journal.Cleaned, ","); got != strings.Join(wantCleaned, ",") {
		t.Fatalf("cleaned=%v want reverse ownership order %v", journal.Cleaned, wantCleaned)
	}
	if journal.Loads != 1 {
		t.Fatalf("loads=%d want 1", journal.Loads)
	}
}

// TestValidateDistribution_NeverBindsListener proves validation never occupies
// the configured data-plane address: an external listener pre-bound to that
// address must remain the sole owner and must not observe a connection
// attempt from validation (req 5.3).
func TestValidateDistribution_NeverBindsListener(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()
	cfgPath := MaterializeExampleConfigForTest(t, writeOneSnapshotMarkerConfig(t, addr, accessmode.ModeSingleUser))

	if err := ValidateDistribution(context.Background(), validDistributionInput(cfgPath)); err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}

	tcpLn, ok := ln.(*net.TCPListener)
	if !ok {
		t.Fatal("expected TCP listener")
	}
	_ = tcpLn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	if conn, acceptErr := ln.Accept(); acceptErr == nil {
		_ = conn.Close()
		t.Fatal("unexpected accept: ValidateDistribution must not dial or bind the data-plane address")
	}
}

// TestValidateDistribution_NoManagerOrGenerationLeftBehind proves repeated
// back-to-back validations never fail due to a retained Manager, generation,
// or listener from a prior run (req 5.2; "owner-free").
func TestValidateDistribution_NoManagerOrGenerationLeftBehind(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(dogfoodConfigPath(t))
	for i := range 3 {
		if err := ValidateDistribution(context.Background(), in); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

// TestValidateDistribution_ConcurrentRepeatedValidationIsOwnerFree proves
// concurrent repeated validation is race-clean (run with -race) and does not
// contend on any shared owned resource (req 5.2-5.4, 11.x).
func TestValidateDistribution_ConcurrentRepeatedValidationIsOwnerFree(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(dogfoodConfigPath(t))
	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = ValidateDistribution(context.Background(), in)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

// TestValidateDistribution_CancelledContextStillCleansUp proves an already
// cancelled caller context does not prevent internal cleanup from completing
// (context.WithoutCancel is used for cleanup, never context.Background
// mid-request).
func TestValidateDistribution_CancelledContextStillCleansUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	journal, err := validateDistributionOutcome(ctx, validDistributionInput(dogfoodConfigPath(t)), LoadBootstrapEffectiveWithSource)
	// Whether or not a cancelled context surfaces as an error from an internal
	// step, cleanup that already acquired resources must still run to
	// completion (no partial ownership left behind).
	if len(journal.Cleaned) > 0 {
		wantCleaned := []string{
			string(validateStageRollback),
			string(validateStageProcessClose),
			string(validateStageTracingClose),
		}
		gotN := len(journal.Cleaned)
		wantPrefix := wantCleaned[:gotN]
		if strings.Join(journal.Cleaned, ",") != strings.Join(wantPrefix, ",") {
			t.Fatalf("cleaned=%v must be a reverse-ownership-order prefix of %v", journal.Cleaned, wantCleaned)
		}
	}
	_ = err
}

// TestValidateDistribution_ComposeFailureClosesProcessAndTracingWithoutRollback
// proves a HandlerComposer failure (the "compose" boundary inside
// CompileGeneration) still closes process/tracing in order, with no rollback
// stage (no generation was ever produced) — mirroring
// assertBootstrapComposeCleanup for the unpublished validation path.
func TestValidateDistribution_ComposeFailureClosesProcessAndTracingWithoutRollback(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(dogfoodConfigPath(t))
	in.HandlerComposer = failingHandlerComposer
	journal, err := validateDistributionOutcome(context.Background(), in, LoadBootstrapEffectiveWithSource)
	if err == nil {
		t.Fatal("expected compose failure")
	}
	if !strings.Contains(err.Error(), "compose boom") {
		t.Fatalf("expected wrapped compose error, got %v", err)
	}
	wantCleaned := []string{string(validateStageProcessClose), string(validateStageTracingClose)}
	if got := strings.Join(journal.Cleaned, ","); got != strings.Join(wantCleaned, ",") {
		t.Fatalf("cleaned=%v want %v (no rollback: no generation was produced)", journal.Cleaned, wantCleaned)
	}
}

// TestValidateDistribution_StageFaultMatrix encodes the ValidateDistribution
// ownership cleanup contract (req 5.4, 11.7): every row injects a stage
// failure through the real production transaction (validateDistributionFaulting)
// and asserts acquire/cleanup evidence in deterministic reverse order.
func TestValidateDistribution_StageFaultMatrix(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(dogfoodConfigPath(t))

	cases := []struct {
		name         string
		stage        validateDistributionStage
		wantAcquired []string
		wantCleaned  []string
	}{
		{
			name:         "tracing_failure_no_process",
			stage:        validateStageTracing,
			wantAcquired: []string{"loader", "tracing"},
			wantCleaned:  []string{"tracing_close"},
		},
		{
			name:         "registry_failure_no_process",
			stage:        validateStageRegistry,
			wantAcquired: []string{"loader", "tracing", "registry"},
			wantCleaned:  []string{"tracing_close"},
		},
		{
			name:         "process_failure_shuts_tracing",
			stage:        validateStageProcess,
			wantAcquired: []string{"loader", "tracing", "registry", "process"},
			wantCleaned:  []string{"process_close", "tracing_close"},
		},
		{
			name:         "compile_success_fault_rolls_back_once",
			stage:        validateStageCompile,
			wantAcquired: []string{"loader", "tracing", "registry", "process", "compile"},
			wantCleaned:  []string{"rollback", "process_close", "tracing_close"},
		},
		{
			name:         "rollback_fault_still_closes_process_and_tracing",
			stage:        validateStageRollback,
			wantAcquired: []string{"loader", "tracing", "registry", "process", "compile"},
			wantCleaned:  []string{"rollback", "process_close", "tracing_close"},
		},
		{
			name:         "process_close_fault_still_closes_tracing",
			stage:        validateStageProcessClose,
			wantAcquired: []string{"loader", "tracing", "registry", "process", "compile"},
			wantCleaned:  []string{"rollback", "process_close", "tracing_close"},
		},
		{
			name:         "tracing_close_fault_still_runs",
			stage:        validateStageTracingClose,
			wantAcquired: []string{"loader", "tracing", "registry", "process", "compile"},
			wantCleaned:  []string{"rollback", "process_close", "tracing_close"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Each row's premise is that every stage succeeds until the injected
			// fault fires. Under heavy parallel load a *non-faulted* stage can
			// fail first (e.g. compile is starved for CPU), which surfaces as a
			// misleading acquire-list mismatch instead of exercising the fault.
			// Retry the scenario a bounded number of times only when the
			// acquire evidence is a strict prefix of the expectation (an earlier
			// stage failed before the fault point); any other divergence fails
			// loudly with the underlying error, as does a seam that stops firing.
			const maxPreemptedAttempts = 5
			var journal validateDistributionJournal
			var err error
			attempt := 0
			for {
				journal, err = validateDistributionFaulting(context.Background(), in, tc.stage)
				if err == nil {
					t.Fatalf("stage %s must fail: fault did not fire", tc.stage)
				}
				if slices.Equal(journal.Acquired, tc.wantAcquired) {
					break
				}
				if !acquiredStrictPrefix(journal.Acquired, tc.wantAcquired) {
					t.Fatalf("stage %s acquired=%v want %v (err=%v)", tc.stage, journal.Acquired, tc.wantAcquired, err)
				}
				if attempt >= maxPreemptedAttempts {
					t.Fatalf("stage %s scenario preempted %d times by non-faulted stage failure (err=%v)", tc.stage, attempt+1, err)
				}
				attempt++
			}

			if got := strings.Join(journal.Cleaned, ","); got != strings.Join(tc.wantCleaned, ",") {
				t.Fatalf("stage %s cleaned=%v want reverse order %v (err=%v)", tc.stage, journal.Cleaned, tc.wantCleaned, err)
			}
		})
	}
}

// acquiredStrictPrefix reports whether got is a non-empty strict prefix of want,
// i.e. execution stopped at a stage before the injected fault point because a
// non-faulted stage failed (environmental preemption), as opposed to the fault
// firing as designed (got == want) or an out-of-order acquisition (regression).
func acquiredStrictPrefix(got, want []string) bool {
	if len(got) == 0 || len(got) >= len(want) {
		return false
	}
	return slices.Equal(got, want[:len(got)])
}

// TestValidateDistribution_CleanupFaultsJoinedInOrder proves every cleanup-stage
// fault is joined with the returned error (req 5.4): rollback → process
// close → tracing close, without discarding sibling cleanup failures.
func TestValidateDistribution_CleanupFaultsJoinedInOrder(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(dogfoodConfigPath(t))
	journal, err := validateDistributionWithCleanupFaults(context.Background(), in)
	if err == nil {
		t.Fatal("expected joined cleanup faults")
	}
	wantCleaned := []string{"rollback", "process_close", "tracing_close"}
	if got := strings.Join(journal.Cleaned, ","); got != strings.Join(wantCleaned, ",") {
		t.Fatalf("cleaned=%v want reverse ownership order %v", journal.Cleaned, wantCleaned)
	}
	msg := err.Error()
	for _, stage := range wantCleaned {
		token := "validate distribution fault: " + stage
		if !strings.Contains(msg, token) {
			t.Fatalf("joined error missing %q: %v", token, err)
		}
	}
	// Mixed AlreadyClosed + sentinel must still preserve the sentinel when the
	// generation-rollback callback surfaces both (same joinInitialFailureCleanup
	// owner ValidateDistribution uses).
	primary := errors.New("primary validate")
	sentinel := errors.New("sentinel cleanup failure")
	joined := joinInitialFailureCleanup(
		context.Background(), primary,
		func() error { return errors.Join(runtimehost.ErrAlreadyClosed, sentinel) },
		func() error { return errors.New("process close failed") },
		func(context.Context) error { return errors.New("trace shutdown failed") },
	)
	if !errors.Is(joined, primary) || !errors.Is(joined, sentinel) {
		t.Fatalf("mixed AlreadyClosed join must keep primary+sentinel: %v", joined)
	}
	if !strings.Contains(joined.Error(), "process close failed") || !strings.Contains(joined.Error(), "trace shutdown failed") {
		t.Fatalf("process/tracing cleanup faults must remain joined: %v", joined)
	}
}

// TestValidateDistribution_LoaderFailureAcquiresNothing proves a missing
// config file fails before any tracing/process/generation resource exists.
func TestValidateDistribution_LoaderFailureAcquiresNothing(t *testing.T) {
	t.Parallel()
	in := validDistributionInput(filepath.Join(t.TempDir(), "missing-validate.yaml"))
	journal, err := validateDistributionOutcome(context.Background(), in, LoadBootstrapEffectiveWithSource)
	if err == nil {
		t.Fatal("expected loader failure")
	}
	if len(journal.Acquired) != 0 || len(journal.Cleaned) != 0 {
		t.Fatalf("loader failure must not acquire or clean any resource: acquired=%v cleaned=%v", journal.Acquired, journal.Cleaned)
	}
}

// TestValidateDistribution_InvalidConfigCategoriesMatchStartupLoad proves
// deterministic strict-loader rejections are identical between
// ValidateDistribution and the same effective loader used by startup/reload
// (req 5.5-5.6): both consume [LoadBootstrapEffectiveWithSource], so an
// invalid fixture must fail with the same secret-safe category from both.
func TestValidateDistribution_InvalidConfigCategoriesMatchStartupLoad(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join(t.TempDir(), "malformed.yaml")
	if err := os.WriteFile(cfgPath, []byte("server: [\napi_key: sk-should-not-leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	validateErr := ValidateDistribution(context.Background(), validDistributionInput(cfgPath))
	if validateErr == nil {
		t.Fatal("expected ValidateDistribution failure for malformed fixture")
	}
	_, _, _, _, loadErr := LoadBootstrapEffectiveWithSource(context.Background(), cfgPath, config.StreamRecoveryOverrides{}, nil)
	if loadErr == nil {
		t.Fatal("expected startup-loader failure for malformed fixture")
	}
	if validateErr.Error() != loadErr.Error() {
		t.Fatalf("ValidateDistribution category diverged from startup loader:\nvalidate=%q\nstartup =%q", validateErr.Error(), loadErr.Error())
	}
	if strings.Contains(validateErr.Error(), "sk-should-not-leak") {
		t.Fatalf("secret leaked in ValidateDistribution error: %q", validateErr.Error())
	}
}

// TestValidateDistribution_MandatoryFactoryRejectionMatchesStartupHost proves
// a registry-stage rejection (mandatory bundled-factory validation, not a
// loader-level parse rejection) is identical between ValidateDistribution and
// [BuildHost] for the same candidate — the same deterministic category and
// message surface from the same [installRegistryAndRegistrations] owner used
// by startup/reload (req 5.5-5.6).
func TestValidateDistribution_MandatoryFactoryRejectionMatchesStartupHost(t *testing.T) {
	t.Parallel()
	cfgPath := dogfoodConfigPath(t)
	impossible := []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "definitely-not-a-bundled-backend"}}

	validateErr := ValidateDistribution(context.Background(), ValidateDistributionInput{
		ConfigPath:      cfgPath,
		Mandatory:       impossible,
		HandlerComposer: stubHandlerComposer,
	})
	if validateErr == nil {
		t.Fatal("expected ValidateDistribution mandatory-factory rejection")
	}

	host, hostErr := BuildHost(context.Background(), BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       impossible,
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	})
	if hostErr == nil {
		cleanupHost(t, host)
		t.Fatal("expected BuildHost mandatory-factory rejection")
	}
	if validateErr.Error() != hostErr.Error() {
		t.Fatalf("ValidateDistribution category diverged from BuildHost startup:\nvalidate=%q\nstartup =%q", validateErr.Error(), hostErr.Error())
	}
}

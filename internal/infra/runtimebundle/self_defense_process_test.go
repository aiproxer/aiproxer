package runtimebundle_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func selfDefenseProbeAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{203, 0, 113, byte(i%254 + 1)}).WithZone("")
}

func selfDefenseProbeAddrs(n int) []netip.Addr {
	out := make([]netip.Addr, 0, n)
	for i := range n {
		out = append(out, netip.AddrFrom4([4]byte{198, 18, byte(i / 254), byte(i%254 + 1)}))
	}
	return out
}

func durationStr(d time.Duration) string { return d.String() }

func selfDefenseConfigWith(mutate func(*config.SelfDefenseConfig)) *config.Config {
	cfg := processServicesTestConfig()
	if mutate != nil {
		mutate(&cfg.Access.SelfDefense)
	}
	return cfg
}

func newSelfDefenseProcessServices(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// TestProcessServices_OwnsOneAdaptiveStateSizedByStartupLimits proves the
// process owns exactly one bounded adaptive state, constructed from the
// effective startup-fixed max_entries/state_ttl, and that those limits are the
// ones it actually enforces.
func TestProcessServices_OwnsOneAdaptiveStateSizedByStartupLimits(t *testing.T) {
	t.Parallel()

	const maxEntries = 1024
	ps := newSelfDefenseProcessServices(t, selfDefenseConfigWith(func(sd *config.SelfDefenseConfig) {
		sd.Adaptive.MaxEntries = new(maxEntries)
		sd.Adaptive.StateTTL = durationStr(time.Minute)
	}))
	if ps.IngressDefense == nil {
		t.Fatal("ProcessServices must own the process-lifetime ingress self-defense state")
	}
	if got := ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("fresh process state entries = %d, want 0", got)
	}

	policy := ingressdefense.Policy{
		Enabled: true, AuthFailures: 2, FailureWindow: time.Minute,
		InitialQuarantine: time.Second, MaxQuarantine: time.Minute,
	}
	start := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	addr := selfDefenseProbeAddr(1)
	ps.IngressDefense.RecordProbe(addr, start, policy)
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries after one probe = %d, want 1", got)
	}

	// The configured inactivity TTL, not a compiled default, expires the entry.
	if ps.IngressDefense.IsQuarantined(addr, start.Add(2*time.Minute)) {
		t.Fatal("the entry must expire after the configured state_ttl of 1m")
	}
	if got := ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("entries after configured TTL expiry = %d, want 0", got)
	}

	// The configured capacity, not a compiled default, bounds unique churn.
	for _, churn := range selfDefenseProbeAddrs(maxEntries + 96) {
		ps.IngressDefense.RecordProbe(churn, start, policy)
	}
	if got := ps.IngressDefense.Len(); got != maxEntries {
		t.Fatalf("entries under unique churn = %d, want the configured max_entries %d", got, maxEntries)
	}
}

// TestProcessServices_DisabledStartStillOwnsALightweightState pins the reload
// posture: enabled/impossible_paths are generation-reloadable, so a process
// that starts with self-defense disabled must still own the bounded state it
// would need if a later generation enables the feature. Ownership is
// lightweight: one empty table, no goroutine, file, database or network
// resource, and no request ever consults it while the generation omits it.
func TestProcessServices_DisabledStartStillOwnsALightweightState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(*config.SelfDefenseConfig)
	}{
		{name: "omitted self_defense config", mutate: nil},
		{name: "explicitly disabled", mutate: func(sd *config.SelfDefenseConfig) { sd.Enabled = new(false) }},
		{name: "impossible paths only disabled", mutate: func(sd *config.SelfDefenseConfig) { sd.ImpossiblePaths = new(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps := newSelfDefenseProcessServices(t, selfDefenseConfigWith(tc.mutate))
			if ps.IngressDefense == nil {
				t.Fatal("the process state must exist so a later generation can enable the feature without a restart")
			}
			if got := ps.IngressDefense.Len(); got != 0 {
				t.Fatalf("entries = %d, want an empty table before any request", got)
			}
			// Lightweight ownership: the process state is reachable only through the
			// process handle, and an explicitly disabled generation projects
			// nothing at all, so no request can consult or grow it.
			if ps.IngressDefense.IsQuarantined(selfDefenseProbeAddr(1), time.Now()) {
				t.Fatal("an unused process state must hold no quarantine")
			}
		})
	}
}

// TestProcessServices_OneStateInstanceIsReusedAcrossGenerationReloads proves
// requirement 7.6: adaptive state survives policy-only generation reloads, both
// generations borrow the identical process instance, and retiring a generation
// neither closes nor clears the state it borrowed.
func TestProcessServices_OneStateInstanceIsReusedAcrossGenerationReloads(t *testing.T) {
	t.Parallel()

	ps := newSelfDefenseProcessServices(t, selfDefenseConfigWith(func(sd *config.SelfDefenseConfig) {
		sd.Adaptive.AuthFailures = new(9)
	}))
	first := selfDefensePolicyConfig(3)
	second := selfDefensePolicyConfig(4)

	cand1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: first, Compose: selfDefenseStubComposer,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}
	in1 := cand1.StandardHTTPInput(first, nil, "")
	cand2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: second, Compose: selfDefenseStubComposer,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #2: %v", err)
	}
	in2 := cand2.StandardHTTPInput(second, nil, "")

	if in1.Security.SelfDefense.State != ps.IngressDefense || in2.Security.SelfDefense.State != ps.IngressDefense {
		t.Fatal("every generation must borrow the one process-owned state instance")
	}
	if in1.Security.SelfDefense.Policy == nil || in2.Security.SelfDefense.Policy == nil {
		t.Fatal("both enabled generations must project their immutable policy")
	}
	if in1.Security.SelfDefense.Policy.AuthFailures == in2.Security.SelfDefense.Policy.AuthFailures {
		t.Fatal("the test must actually reload a different policy")
	}

	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	addr := selfDefenseProbeAddr(7)
	ps.IngressDefense.RecordProbe(addr, now, *in1.Security.SelfDefense.Policy)
	if !ps.IngressDefense.IsQuarantined(addr, now) {
		t.Fatal("a probe offense must quarantine through generation one")
	}
	if err := cand1.Close(); err != nil {
		t.Fatalf("retire generation one: %v", err)
	}
	if !ps.IngressDefense.IsQuarantined(addr, now) {
		t.Fatal("retiring a generation must not dispose or resize the process-owned state")
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries after generation retire = %d, want the surviving entry", got)
	}
	if in2.Security.SelfDefense.State != ps.IngressDefense {
		t.Fatal("the reloaded generation must reuse the surviving process state")
	}
	_ = cand2.Close()
}

// TestProcessServices_AdaptiveStateIsPerProcessAndDisposedWithItsOwner proves
// the state belongs to one ProcessServices lifetime: two processes own distinct
// instances, process close is idempotent and clean, and a closed process admits
// no further generation that could consult the disposed state.
func TestProcessServices_AdaptiveStateIsPerProcessAndDisposedWithItsOwner(t *testing.T) {
	t.Parallel()

	cfg := selfDefenseConfigWith(nil)
	first := newSelfDefenseProcessServices(t, cfg)
	second := newSelfDefenseProcessServices(t, cfg)
	if first.IngressDefense == second.IngressDefense {
		t.Fatal("two processes must own distinct adaptive state; it is process state, not a global")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close must be idempotent: %v", err)
	}
	if !first.Closed() {
		t.Fatal("Closed must report the completed process close")
	}
	if _, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: first}); err == nil {
		t.Fatal("a closed process must admit no further generation that could reach disposed adaptive state")
	}
	if second.Closed() {
		t.Fatal("closing one process must not report another process closed")
	}
}

// TestProcessServices_InvalidSelfDefenseConfigRollsBackProcessStartup proves
// the partial-startup path: an invalid self-defense configuration fails process
// construction and every resource already acquired in this process is disposed,
// while the adaptive state itself contributes nothing to dispose because it owns
// no external resource.
func TestProcessServices_InvalidSelfDefenseConfigRollsBackProcessStartup(t *testing.T) {
	t.Parallel()

	sched, err := auxreq.NewBackgroundScheduler(context.Background(), nil, auxreq.SchedulerConfig{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:           selfDefenseConfigWith(func(sd *config.SelfDefenseConfig) { sd.Adaptive.AuthFailures = new(1) }),
		Log:           testkit.DiscardLogger(),
		Opts:          &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		BackgroundAux: sched,
		Tracing:       runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err == nil {
		_ = ps.Close()
		t.Fatal("an out-of-range self-defense auth_failures must fail process construction")
	}
	if ps != nil {
		t.Fatal("a failed process construction must return no process services")
	}
	if !strings.Contains(err.Error(), "access.self_defense.adaptive.auth_failures") {
		t.Fatalf("error = %v, want the self-defense validation failure", err)
	}
	_, submitErr := sched.SubmitCollect(context.Background(),
		auxiliary.Request{Call: &aipapi.Call{Route: aipapi.RouteIntent{Selector: "local:test"}}},
		auxiliary.SubmitOptions{CoalesceKey: "self-defense-rollback"})
	if !errors.Is(submitErr, auxreq.ErrSchedulerClosed) {
		t.Fatalf("adopted scheduler submit error = %v, want the rollback to have closed it", submitErr)
	}
	if err := sched.Close(); err != nil {
		t.Fatalf("rollback Close must be idempotent: %v", err)
	}
}

// selfDefensePolicyConfig returns a reloadable-policy-only variant of the base
// process config: it changes only a generation-reloadable self-defense field, so
// candidate compilation accepts it and the process state must survive.
func selfDefensePolicyConfig(authFailures int) *config.Config {
	cfg := processServicesTestConfig()
	cfg.Access.SelfDefense.Adaptive.AuthFailures = new(authFailures)
	return cfg
}

func selfDefenseStubComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.NotFoundHandler(), nil
}

package runtimebundle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// faultUsageLimitPort records the borrowed-attachment contract for the forced
// post-attachment build failure: attach calls, authority operations, and any
// runtime close of the caller-owned port.
type faultUsageLimitPort struct {
	mu                  sync.Mutex
	stage, evidence     int
	admit, settle       int
	release, applyUsage int
	closeCalls          int
	sink                authority.EvidenceSink
}

var (
	_ usagelimit.Authority          = (*faultUsageLimitPort)(nil)
	_ usagelimit.StageMetricsSetter = (*faultUsageLimitPort)(nil)
	_ usagelimit.EvidenceSinkSetter = (*faultUsageLimitPort)(nil)
)

func (p *faultUsageLimitPort) SetStageMetrics(usagelimit.StageMetrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage++
}

func (p *faultUsageLimitPort) SetEvidenceSink(sink authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evidence++
	p.sink = sink
}

// Close exists to prove the runtime never closes the borrowed port.
func (p *faultUsageLimitPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCalls++
	return nil
}

func (p *faultUsageLimitPort) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.admit++
	return usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}, nil
}

func (p *faultUsageLimitPort) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settle++
	return usagelimit.SettleResult{}, nil
}

func (p *faultUsageLimitPort) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release++
	return usagelimit.ReleaseResult{}, nil
}

func (p *faultUsageLimitPort) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyUsage++
	return usagelimit.ApplyUsageResult{}, nil
}

func (p *faultUsageLimitPort) snapshot() (stage, evidence, admit, settle, release, apply, closes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stage, p.evidence, p.admit, p.settle, p.release, p.applyUsage, p.closeCalls
}

// faultEvidenceSink records evidence writes for the retained-sink assertion.
type faultEvidenceSink struct {
	mu    sync.Mutex
	wrote int
}

func (s *faultEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *faultEvidenceSink) RecordAccountingAuthority(context.Context, cp.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *faultEvidenceSink) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

// TestUsageLimitFailedBuildBorrowedContract is the R2/H1 forced
// post-attachment failure control: rollback closes process resources exactly
// once, the borrowed port and caller sink stay untouched (no authority
// operation, no evidence write, no runtime close), and a fresh port instance
// builds and attaches.
func TestUsageLimitFailedBuildBorrowedContract(t *testing.T) {
	t.Parallel()
	cfgPath := MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	ctx := context.Background()
	port := &faultUsageLimitPort{}
	sink := &faultEvidenceSink{}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(t.TempDir(), "usage-limit-fault.yaml")
	withMetrics := strings.Replace(string(raw), "plugins:\n", "observability:\n  metrics:\n    enabled: true\nplugins:\n", 1)
	if withMetrics == string(raw) {
		t.Fatal("dogfood config lost the plugins anchor")
	}
	if writeErr := os.WriteFile(cfgPath, []byte(withMetrics), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	in := hostBuildInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
		Production:      ProductionOptions{UsageLimits: port, EvidenceSink: sink},
		EnterpriseCapabilities: config.CapabilitySet{
			config.CapabilityUsageLimits: true,
		},
	}

	out, buildErr := productionHostBuilder{}.BuildFaulting(ctx, in, hostBuildStageCompile)
	err = buildErr
	if err == nil {
		t.Fatal("forced compile failure must fail the build")
	}
	if out.Host != nil {
		t.Fatalf("failed build must not return a Host")
	}
	if got := strings.Join(out.Journal.Cleaned, ","); !strings.Contains(got, "compile") || !strings.Contains(got, "process") || !strings.Contains(got, "tracing") {
		t.Fatalf("rollback must dispose compile/process/tracing, cleaned=%v", out.Journal.Cleaned)
	}
	if count := func(stage string) int {
		n := 0
		for _, name := range out.Journal.Cleaned {
			if name == stage {
				n++
			}
		}
		return n
	}; count("process") != 1 || count("tracing") != 1 {
		t.Fatalf("rollback must close process resources exactly once, cleaned=%v", out.Journal.Cleaned)
	}

	stageAttach, evidenceAttach, admit, settle, release, apply, closes := port.snapshot()
	if stageAttach != 1 || evidenceAttach != 1 {
		t.Fatalf("startup attachments stage=%d evidence=%d, want once each before the compile fault", stageAttach, evidenceAttach)
	}
	if admit+settle+release+apply != 0 {
		t.Fatalf("failed build must not run authority operations: admit=%d settle=%d release=%d apply=%d", admit, settle, release, apply)
	}
	if closes != 0 {
		t.Fatal("runtime closed the borrowed port")
	}
	if writes := sink.writes(); writes != 0 {
		t.Fatalf("failed build wrote %d evidence records to the retained caller sink", writes)
	}

	// A fresh port instance with a valid build attaches and serves.
	fresh := &faultUsageLimitPort{}
	in.Production = ProductionOptions{UsageLimits: fresh, EvidenceSink: sink}
	out2, err := productionHostBuilder{}.Build(ctx, in)
	if err != nil {
		t.Fatalf("fresh port with a valid config: %v", err)
	}
	if out2.Host == nil {
		t.Fatal("fresh build must return the Host")
	}
	t.Cleanup(func() { cleanupHost(t, out2.Host) })
	stageAttach, evidenceAttach, _, _, _, _, closes = fresh.snapshot()
	if stageAttach != 1 || evidenceAttach != 1 {
		t.Fatalf("fresh port attachments stage=%d evidence=%d, want once each", stageAttach, evidenceAttach)
	}
	if closes != 0 {
		t.Fatal("runtime closed the fresh borrowed port")
	}
}

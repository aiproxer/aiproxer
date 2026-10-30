package aipruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// abiUsageLimits is a setter-capable public port fake used from an external
// module: it proves the failed-build borrowed contract of the public Options.
type abiUsageLimits struct {
	mu                  sync.Mutex
	stage, evidence     int
	admit, settle       int
	release, applyUsage int
	closeCalls          int
}

var _ aipruntime.UsageLimits = (*abiUsageLimits)(nil)

func (p *abiUsageLimits) Admit(context.Context, aipruntime.UsageAdmissionInput) (aipruntime.UsageAdmissionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.admit++
	return aipruntime.UsageAdmissionResult{Allowed: true, Outcome: "allow"}, nil
}

func (p *abiUsageLimits) Settle(context.Context, aipruntime.UsageSettleInput) (aipruntime.UsageSettleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settle++
	return aipruntime.UsageSettleResult{}, nil
}

func (p *abiUsageLimits) Release(context.Context, aipruntime.UsageReleaseInput) (aipruntime.UsageReleaseResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release++
	return aipruntime.UsageReleaseResult{}, nil
}

func (p *abiUsageLimits) ApplyUsage(context.Context, aipruntime.UsageApplyUsageCommand) (aipruntime.UsageApplyUsageResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyUsage++
	return aipruntime.UsageApplyUsageResult{}, nil
}

func (p *abiUsageLimits) SetStageMetrics(aipruntime.UsageStageMetrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage++
}

func (p *abiUsageLimits) SetEvidenceSink(authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evidence++
}

// Close is the caller's own cleanup; the runtime must never call it.
func (p *abiUsageLimits) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCalls++
	return nil
}

func (p *abiUsageLimits) snapshot() (stage, evidence, admit, settle, release, apply, closes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stage, p.evidence, p.admit, p.settle, p.release, p.applyUsage, p.closeCalls
}

// abiEvidenceSink is the caller-owned evidence sink with an idempotent Close.
type abiEvidenceSink struct {
	mu     sync.Mutex
	wrote  int
	closed int
}

func (s *abiEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *abiEvidenceSink) RecordAccountingAuthority(context.Context, cp.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *abiEvidenceSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func (s *abiEvidenceSink) snapshot() (wrote, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote, s.closed
}

// TestBuildFailureLeavesBorrowedUsageLimitPortUntouched is the R2/H2 external
// ABI control: a real public build failure after the port was attached leaves
// the caller's port and sink unwritten and open, caller cleanup is safe and
// idempotent, and a fresh port with a valid config builds.
func TestBuildFailureLeavesBorrowedUsageLimitPortUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	validPath := bpkit.WriteDogfoodLocalStubConfig(t)
	raw, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	// A metering journal sqlite path that is a directory fails the process
	// build after the usage-limit port was captured and attached.
	brokenDir := t.TempDir()
	brokenPath := filepath.Join(brokenDir, "broken-metering.yaml")
	broken := string(raw) + "\nmetering:\n  enabled: true\n  journal:\n    store: sqlite\n    sqlite_path: " + brokenDir + "\n"
	if err := os.WriteFile(brokenPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	port := &abiUsageLimits{}
	sink := &abiEvidenceSink{}
	if _, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: brokenPath, UsageLimits: port, EvidenceSink: sink}); err == nil {
		t.Fatal("broken metering journal must fail the public build")
	} else if !strings.Contains(err.Error(), "metering") {
		t.Fatalf("unexpected build failure: %v", err)
	}
	stage, evidence, admit, settle, release, apply, closes := port.snapshot()
	if evidence != 1 {
		t.Fatalf("failed build must have attached the evidence surface once before the fault (stage=%d evidence=%d)", stage, evidence)
	}
	if admit+settle+release+apply != 0 {
		t.Fatalf("failed build ran authority operations: admit=%d settle=%d release=%d apply=%d", admit, settle, release, apply)
	}
	if closes != 0 {
		t.Fatal("runtime closed the caller-owned port")
	}
	if wrote, sinkCloses := sink.snapshot(); wrote != 0 || sinkCloses != 0 {
		t.Fatalf("caller sink wrote=%d closes=%d, want untouched", wrote, sinkCloses)
	}

	// Caller cleanup is safe and idempotent (the runtime retained no ownership).
	if err := sink.Close(); err != nil {
		t.Fatalf("caller sink Close: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("caller sink Close must be idempotent: %v", err)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("caller port Close: %v", err)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("caller port Close must be idempotent: %v", err)
	}
	if closes != 0 {
		t.Fatal("runtime called the port Close")
	}

	// A fresh port (and sink) with a valid configuration succeeds.
	freshPort := &abiUsageLimits{}
	freshSink := &abiEvidenceSink{}
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: validPath, UsageLimits: freshPort, EvidenceSink: freshSink})
	if err != nil {
		t.Fatalf("fresh port with a valid config: %v", err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("fresh runtime close: %v", err)
	}
	_, freshEvidence, freshAdmit, _, _, _, freshCloses := freshPort.snapshot()
	if freshEvidence != 1 {
		t.Fatalf("fresh port evidence attachments=%d, want 1", freshEvidence)
	}
	if freshCloses != 0 {
		t.Fatal("runtime closed the fresh borrowed port")
	}
	_ = freshAdmit
	if wrote, _ := freshSink.snapshot(); wrote != 0 {
		t.Fatalf("fresh build wrote %d records before any request", wrote)
	}
}

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// enterpriseUsageLimits proves the public usage-limit port is implementable
// from a separate module with public names only: every method input and result
// is a aipruntime alias, no internal package is imported, and no open-source
// no-op is assigned. It records host attachment and admission so the fixture
// can assert the runtime consumed the injected instance (task 5.3).
type enterpriseUsageLimits struct {
	mu       sync.Mutex
	attached int
	admitted int
	sink     authority.EvidenceSink
}

var (
	_ aipruntime.UsageLimits = (*enterpriseUsageLimits)(nil)
	_ authority.EvidenceSink = (*enterpriseEvidence)(nil)
)

func (p *enterpriseUsageLimits) Admit(context.Context, aipruntime.UsageAdmissionInput) (aipruntime.UsageAdmissionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.admitted++
	return aipruntime.UsageAdmissionResult{Allowed: true, Outcome: "allow"}, nil
}

func (p *enterpriseUsageLimits) Settle(context.Context, aipruntime.UsageSettleInput) (aipruntime.UsageSettleResult, error) {
	return aipruntime.UsageSettleResult{}, nil
}

func (p *enterpriseUsageLimits) Release(context.Context, aipruntime.UsageReleaseInput) (aipruntime.UsageReleaseResult, error) {
	return aipruntime.UsageReleaseResult{}, nil
}

func (p *enterpriseUsageLimits) ApplyUsage(context.Context, aipruntime.UsageApplyUsageCommand) (aipruntime.UsageApplyUsageResult, error) {
	return aipruntime.UsageApplyUsageResult{}, nil
}

// SetStageMetrics is the optional public post-construction metrics surface.
func (p *enterpriseUsageLimits) SetStageMetrics(aipruntime.UsageStageMetrics) {}

// SetEvidenceSink is the optional public post-construction evidence surface.
func (p *enterpriseUsageLimits) SetEvidenceSink(sink authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attached++
	p.sink = sink
}

// Status, Limits, and Decisions exercise the optional public query surface.
func (p *enterpriseUsageLimits) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	return controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}, nil
}

func (p *enterpriseUsageLimits) Limits(context.Context, controlplane.AccountingLimitStatusQuery) (aipruntime.UsageLimitStatusResult, error) {
	return aipruntime.UsageLimitStatusResult{State: aipruntime.UsageQueryStateReady}, nil
}

func (p *enterpriseUsageLimits) Decisions(context.Context, controlplane.AccountingDecisionQuery) (aipruntime.UsageDecisionHistoryResult, error) {
	return aipruntime.UsageDecisionHistoryResult{State: aipruntime.UsageQueryStateReady}, nil
}

func (p *enterpriseUsageLimits) snapshot() (attached, admitted int, sink authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attached, p.admitted, p.sink
}

// verifyPublicUsageLimits runs the custom implementation through every method
// so the proof executes, not only compiles.
func verifyPublicUsageLimits(ctx context.Context, limits *enterpriseUsageLimits) error {
	admission, err := limits.Admit(ctx, aipruntime.UsageAdmissionInput{})
	if err != nil || !admission.Allowed || admission.Outcome != "allow" {
		return fmt.Errorf("public UsageLimits.Admit = %+v, err=%v", admission, err)
	}
	if _, err := limits.Settle(ctx, aipruntime.UsageSettleInput{}); err != nil {
		return fmt.Errorf("public UsageLimits.Settle: %w", err)
	}
	if _, err := limits.Release(ctx, aipruntime.UsageReleaseInput{}); err != nil {
		return fmt.Errorf("public UsageLimits.Release: %w", err)
	}
	if _, err := limits.ApplyUsage(ctx, aipruntime.UsageApplyUsageCommand{}); err != nil {
		return fmt.Errorf("public UsageLimits.ApplyUsage: %w", err)
	}
	if _, err := limits.Status(ctx); err != nil {
		return fmt.Errorf("public UsageLimits.Status: %w", err)
	}
	if _, err := limits.Limits(ctx, controlplane.AccountingLimitStatusQuery{}); err != nil {
		return fmt.Errorf("public UsageLimits.Limits: %w", err)
	}
	if _, err := limits.Decisions(ctx, controlplane.AccountingDecisionQuery{}); err != nil {
		return fmt.Errorf("public UsageLimits.Decisions: %w", err)
	}
	return nil
}

// verifyPublicUsageLimitsConsumed asserts the runtime consumed the injected
// instance: the host attached the evidence sink and every served request
// passed through the port's Admit.
func verifyPublicUsageLimitsConsumed(limits *enterpriseUsageLimits, wantAdmitted int) error {
	attached, admitted, sink := limits.snapshot()
	if attached != 1 {
		return fmt.Errorf("host attached the evidence sink %d times, want exactly once", attached)
	}
	if sink == nil {
		return fmt.Errorf("host attached a nil evidence sink")
	}
	if admitted < wantAdmitted {
		return fmt.Errorf("injected port admitted %d requests, want >= %d", admitted, wantAdmitted)
	}
	return nil
}

// These compile-only adapters prove that an external module can implement the
// neutral economics seams without importing internal packages or provider SDKs.
// They are compile-only adapters, not installed runtime raters/importers and
// do not perform rating, persistence, or provider calls.
type enterpriseV2Rater struct{}

func (enterpriseV2Rater) Rate(context.Context, economics.RatingInput) (economics.Valuation, error) {
	return economics.Valuation{}, nil
}

type enterpriseV2Quoter struct{}

func (enterpriseV2Quoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

// enterpriseV2SnapshotContext is compile-only coverage for the additive V2
// replay fields. Historical VersionRef wire members remain owned by the
// economics package; external raters and quoters consume the content refs on
// their input/output envelopes instead.
func enterpriseV2SnapshotContext(in economics.RatingInput, quote economics.QuoteInput, valuation economics.Valuation, exposure economics.ExposureQuote) {
	_ = in.RaterContent
	_ = in.TariffContent
	_ = in.PolicyContent
	_ = in.QualifierSnapshotRef
	_ = quote.TariffContent
	_ = quote.PolicyContent
	_ = quote.QualifierSnapshotRef
	_ = valuation.RaterContent
	_ = valuation.TariffContent
	_ = valuation.PolicyContent
	_ = valuation.QualifierSnapshotRef
	_ = valuation.Payer
	_ = exposure.TariffContent
	_ = exposure.PolicyContent
	_ = exposure.QualifierSnapshotRef
}

var (
	_ metering.ObservationSink       = enterpriseV2ObservationSink{}
	_ economics.Rater                = enterpriseV2Rater{}
	_ economics.Quoter               = enterpriseV2Quoter{}
	_ economics.StatementImporter    = enterpriseV2StatementImporter{}
	_ economics.ReconciliationReader = enterpriseV2ReconciliationReader{}
)

type enterpriseV2ObservationSink struct{}

func (enterpriseV2ObservationSink) Append(context.Context, metering.Observation) error { return nil }

type enterpriseV2StatementImporter struct{}

func (enterpriseV2StatementImporter) Import(context.Context, economics.StatementBatch) (economics.ImportResult, error) {
	return economics.ImportResult{}, nil
}

type enterpriseV2ReconciliationReader struct{}

func (enterpriseV2ReconciliationReader) Query(context.Context, economics.ReconciliationQuery) (economics.ReconciliationPage, error) {
	return economics.ReconciliationPage{}, nil
}

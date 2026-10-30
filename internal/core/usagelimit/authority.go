package usagelimit

import "context"

// Authority is the admission and completion protocol consumed by the runtime.
type Authority interface {
	Admit(context.Context, AdmissionInput) (AdmissionResult, error)
	Settle(context.Context, SettleInput) (SettleResult, error)
	Release(context.Context, ReleaseInput) (ReleaseResult, error)
	ApplyUsage(context.Context, ApplyUsageCommand) (ApplyUsageResult, error)
}

// NoOp admits every request without reservations or enforcement. Completion
// methods return empty results, even when the context is already canceled.
// Its zero value is ready for use.
type NoOp struct{}

var _ Authority = NoOp{}

func (NoOp) Admit(context.Context, AdmissionInput) (AdmissionResult, error) {
	return AdmissionResult{Allowed: true, Outcome: DecisionOutcomeAllow}, nil
}

func (NoOp) Settle(context.Context, SettleInput) (SettleResult, error) {
	return SettleResult{}, nil
}

func (NoOp) Release(context.Context, ReleaseInput) (ReleaseResult, error) {
	return ReleaseResult{}, nil
}

func (NoOp) ApplyUsage(context.Context, ApplyUsageCommand) (ApplyUsageResult, error) {
	return ApplyUsageResult{}, nil
}

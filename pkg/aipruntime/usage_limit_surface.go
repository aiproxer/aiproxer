// The public usage-limit port surface. Closed modules implement the
// four-method [UsageLimits] port with public names only; these aliases make its
// payloads, optional host surfaces, and bounded query vocabulary publicly
// nameable too. The borrow/lifetime contract lives on [Options.UsageLimits].
package aipruntime

import (
	"context"
	"reflect"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
)

// UsageLimits is the runtime's public usage-limit port (admission, settlement,
// release, usage application). Its inputs and results alias the carved
// open-source contract, so an external module can implement it with public
// names only; the optional host surfaces and borrow contract are named in
// [Options.UsageLimits].
type UsageLimits interface {
	Admit(context.Context, UsageAdmissionInput) (UsageAdmissionResult, error)
	Settle(context.Context, UsageSettleInput) (UsageSettleResult, error)
	Release(context.Context, UsageReleaseInput) (UsageReleaseResult, error)
	ApplyUsage(context.Context, UsageApplyUsageCommand) (UsageApplyUsageResult, error)
}

// Aliases of the port payloads: exact contract types, publicly nameable.
type (
	UsageAdmissionInput    = usagelimit.AdmissionInput
	UsageAdmissionResult   = usagelimit.AdmissionResult
	UsageSettleInput       = usagelimit.SettleInput
	UsageSettleResult      = usagelimit.SettleResult
	UsageReleaseInput      = usagelimit.ReleaseInput
	UsageReleaseResult     = usagelimit.ReleaseResult
	UsageApplyUsageCommand = usagelimit.ApplyUsageCommand
	UsageApplyUsageResult  = usagelimit.ApplyUsageResult
)

// The public port stays assignable to and from the carved contract, so the two
// cannot drift.
var (
	_ usagelimit.Authority = UsageLimits(nil)
	_ UsageLimits          = usagelimit.NoOp{}
)

// UsageStageMetrics names the stage-metrics contract publicly.
type UsageStageMetrics = usagelimit.StageMetrics

// Bounded authority query vocabulary, publicly nameable by closed modules.
type UsageQueryState = usagelimit.QueryState

const (
	UsageQueryStateDisabled     = usagelimit.QueryStateDisabled
	UsageQueryStateEmpty        = usagelimit.QueryStateEmpty
	UsageQueryStateUnsupported  = usagelimit.QueryStateUnsupported
	UsageQueryStateTooBroad     = usagelimit.QueryStateTooBroad
	UsageQueryStateAdvisoryOnly = usagelimit.QueryStateAdvisoryOnly
	UsageQueryStateDegraded     = usagelimit.QueryStateDegraded
	UsageQueryStateUnavailable  = usagelimit.QueryStateUnavailable
	UsageQueryStateReady        = usagelimit.QueryStateReady
)

type UsageLimitStatusResult = usagelimit.LimitStatusResult

type UsageDecisionHistoryResult = usagelimit.DecisionHistoryResult

// isNilValue reports untyped nil and typed-nil chan/func/interface/map/pointer/
// slice values so boxed nil ports fail validation instead of panicking later.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

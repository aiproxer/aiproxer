package aipruntime

import (
	"context"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// reloadQuery is the narrow reload/status seam satisfied by hostAPI.
// It is unexported so public callers never see internal packages.
// Signatures use the canonical SDK contract types directly.
type reloadQuery interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
}

// ReloadControl is the importable, thread-safe public reload and status facade.
// It delegates to the host seam and never duplicates reload logic.
type ReloadControl struct {
	q reloadQuery
}

func newReloadControl(q reloadQuery) *ReloadControl {
	if q == nil {
		return nil
	}
	return &ReloadControl{q: q}
}

// Reload runs one explicit reload attempt through the bound coordinator seam.
func (c *ReloadControl) Reload(ctx context.Context, trigger ReloadTrigger) ReloadResult {
	if c == nil || c.q == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "reload-unavailable"}
	}
	if ctx == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "nil-context"}
	}
	if !sdkreload.IsKnownTriggerKind(trigger.Kind) {
		return ReloadResult{Category: ResultInvalid, ReasonCategory: "trigger"}
	}
	res := c.q.Reload(ctx, trigger).Clone()
	res.Category = sdkreload.NormalizeResultCategory(res.Category)
	return res
}

// Status returns a defensive copy of the safe reload status snapshot.
func (c *ReloadControl) Status() ReloadStatus {
	if c == nil || c.q == nil {
		return ReloadStatus{}
	}
	return normalizeStatus(c.q.Status().Clone())
}

// Reload runs one explicit reload attempt when a host is bound to this runtime.
func (r *Runtime) Reload(ctx context.Context, trigger ReloadTrigger) ReloadResult {
	if r == nil || r.host == nil {
		return ReloadResult{Category: ResultInternalFailed, ReasonCategory: "reload-unavailable"}
	}
	return newReloadControl(r.host).Reload(ctx, trigger)
}

// ReloadStatus returns the safe reload status snapshot for this runtime.
func (r *Runtime) ReloadStatus() ReloadStatus {
	if r == nil || r.host == nil {
		return ReloadStatus{}
	}
	return newReloadControl(r.host).Status()
}

// ReloadControl returns the bound reload facade, or nil when reload is unavailable.
func (r *Runtime) ReloadControl() *ReloadControl {
	if r == nil || r.host == nil {
		return nil
	}
	return newReloadControl(r.host)
}

func normalizeStatus(st ReloadStatus) ReloadStatus {
	st.LastResult.Category = sdkreload.NormalizeResultCategory(st.LastResult.Category)
	st.LastSuccess.Category = sdkreload.NormalizeResultCategory(st.LastSuccess.Category)
	st.LastFailure.Category = sdkreload.NormalizeResultCategory(st.LastFailure.Category)
	if st.CurrentAttempt != nil {
		st.CurrentAttempt.Category = sdkreload.NormalizeResultCategory(st.CurrentAttempt.Category)
	}
	for i := range st.History {
		st.History[i].Category = sdkreload.NormalizeResultCategory(st.History[i].Category)
	}
	return st
}

package secretguard

import (
	"context"
	"time"
)

// DecisionEvent is one secret-safe structured audit record for a guard decision.
// It must never carry secret values, prompt excerpts, bearer/resume tokens, or reversible fingerprints.
type DecisionEvent struct {
	Timestamp time.Time
	EventID   string

	TraceID   string
	SessionID string
	ALegID    string
	TurnID    string

	PrincipalID string
	TenantID    string
	OrgID       string
	WorkspaceID string

	PeerIP string
	Source string

	FrontendID          string
	Operation           string
	AgentIdentityDigest string

	RequestedRoute string
	RequestedModel string

	Findings []Finding

	Action  string
	Outcome Outcome

	AccessMode    string
	ConfigVersion string

	QuarantineResult  string
	BackendDispatched bool

	GuardID      string
	ScanLimitHit bool
}

// Observer receives secret-decision audit events.
type Observer interface {
	OnSecretDecision(ctx context.Context, ev DecisionEvent) error
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(ctx context.Context, ev DecisionEvent) error

func (f ObserverFunc) OnSecretDecision(ctx context.Context, ev DecisionEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, ev)
}

// AuditFailurePolicy controls observer-chain delivery failures.
type AuditFailurePolicy string

const (
	AuditFailClosed AuditFailurePolicy = "fail_closed"
	AuditBestEffort AuditFailurePolicy = "best_effort"
)

// QuarantineResult values for DecisionEvent.QuarantineResult.
const (
	QuarantineResultCommitted = "committed"
	QuarantineResultFailed    = "failed"
	QuarantineResultSkipped   = "skipped"
	QuarantineResultNA        = "n/a"
)

// ChainObservers returns an Observer that invokes observers in order.
// fail_closed stops and returns the first error; best_effort continues after errors.
func ChainObservers(policy AuditFailurePolicy, observers ...Observer) Observer {
	filtered := make([]Observer, 0, len(observers))
	for _, o := range observers {
		if !IsNilObserver(o) {
			filtered = append(filtered, o)
		}
	}
	if len(filtered) == 0 {
		return ObserverFunc(func(context.Context, DecisionEvent) error { return nil })
	}
	return chainObserver{policy: policy, observers: filtered}
}

type chainObserver struct {
	policy    AuditFailurePolicy
	observers []Observer
}

func (c chainObserver) OnSecretDecision(ctx context.Context, ev DecisionEvent) error {
	for _, o := range c.observers {
		if err := o.OnSecretDecision(ctx, ev); err != nil {
			if c.policy != AuditBestEffort {
				return err
			}
		}
	}
	return nil
}

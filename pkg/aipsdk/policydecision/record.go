package policydecision

import (
	"maps"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Record is the in-memory decision evidence DTO for one provider decision or one
// projected extension outcome (requirements 1.1, 1.6, 7.1). Mutable maps and the
// embedded scope view are deep-cloned at observer boundaries; callers and observers
// must not mutate a shared record in place.
type Record struct {
	TraceID            string                   `json:"trace_id"`
	ALegID             string                   `json:"a_leg_id"`
	BLegID             string                   `json:"b_leg_id"`
	AttemptSeq         int                      `json:"attempt_seq"`
	Stage              string                   `json:"stage"`
	Provider           ProviderRef              `json:"provider"`
	Outcome            Outcome                  `json:"outcome"`
	Effect             Effect                   `json:"effect"`
	ReasonCode         string                   `json:"reason_code"`
	ClientCategory     string                   `json:"client_category"`
	ClientMessage      string                   `json:"client_message"`
	FailureBehavior    FailureBehavior          `json:"failure_behavior"`
	Visibility         EvidenceVisibility       `json:"visibility"`
	Scope              scope.PrincipalScopeView `json:"scope"`
	Annotations        map[string]string        `json:"annotations,omitempty"`
	OutputCommitted    bool                     `json:"output_committed"`
	BackendAttempted   bool                     `json:"backend_attempted"`
	EvaluationTimeout  time.Duration            `json:"evaluation_timeout,omitempty"`
	EvaluationDeadline time.Time                `json:"evaluation_deadline,omitzero"`
}

// Clone returns a deep copy of the record so callers and observers cannot mutate
// shared record state through maps or the embedded scope view (requirements 1.1, 7.7).
// Nil maps are preserved as nil.
func (r Record) Clone() Record {
	out := r
	out.Scope = r.Scope.Clone()
	out.Annotations = maps.Clone(r.Annotations)
	return out
}

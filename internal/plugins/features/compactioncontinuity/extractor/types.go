package extractor

import "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"

// Limits bounds the untrusted child result before it can become capsule state.
// Zero values use DefaultLimits.
type Limits struct {
	MaxBytes          int
	MaxDepth          int
	MaxItems          int
	MaxStringBytes    int
	MaxSourceRefBytes int
	MaxConflictBytes  int
	MaxSupersedes     int
}

func DefaultLimits() Limits {
	return Limits{MaxBytes: 256 << 10, MaxDepth: 8, MaxItems: 128, MaxStringBytes: 4 << 10, MaxSourceRefBytes: 512, MaxConflictBytes: 128, MaxSupersedes: 32}
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.MaxBytes > 0 {
		d.MaxBytes = l.MaxBytes
	}
	if l.MaxDepth > 0 {
		d.MaxDepth = l.MaxDepth
	}
	if l.MaxItems > 0 {
		d.MaxItems = l.MaxItems
	}
	if l.MaxStringBytes > 0 {
		d.MaxStringBytes = l.MaxStringBytes
	}
	if l.MaxSourceRefBytes > 0 {
		d.MaxSourceRefBytes = l.MaxSourceRefBytes
	}
	if l.MaxConflictBytes > 0 {
		d.MaxConflictBytes = l.MaxConflictBytes
	}
	if l.MaxSupersedes > 0 {
		d.MaxSupersedes = l.MaxSupersedes
	}
	return d
}

// ParseOptions supplies the locally validated parent capsule and source
// authority. The result schema has no branch field: cross-branch output is
// rejected by checking the parent binding and supersede references here.
type ParseOptions struct {
	Previous            capsule.Envelope
	ExpectedBranch      string
	AllowedSourceRefs   []string
	SourceHighWatermark string
	Limits              Limits
}

type Result struct {
	SchemaVersion uint8
	BaseRevision  uint64
	Facts         []FactUpdate
	PlanUpdates   []PlanUpdate
	Decisions     []DecisionUpdate
	Removals      []Removal
}

type PlanUpdate struct {
	ID        string
	Text      string
	Status    capsule.StepStatus
	SourceRef string
}

type FactUpdate struct {
	Kind      FactKind
	ID        string
	Statement string
	Status    capsule.FactStatus
	Rationale string
	SourceRef string
}

type FactKind string

const (
	FactConstraint          FactKind = "constraint"
	FactRejectedAlternative FactKind = "rejected_alternative"
	FactOpenQuestion        FactKind = "open_question"
)

type DecisionUpdate struct {
	ID          string
	ConflictKey string
	Supersedes  []string
	Statement   string
	Status      capsule.DecisionStatus
	Rationale   string
	SourceRef   string
	Authority   capsule.Authority
	Source      capsule.Source
}

type Removal struct {
	ID        string
	Status    capsule.DecisionStatus
	SourceRef string
}

type wireResult struct {
	SchemaVersion     uint8          `json:"schema_version"`
	BaseRevision      uint64         `json:"base_revision"`
	Facts             []wireFact     `json:"facts"`
	PlanUpdates       []wirePlan     `json:"plan_updates"`
	DecisionUpdates   []wireDecision `json:"decision_updates"`
	RemoveOrSupersede []wireRemoval  `json:"remove_or_supersede"`
}

type wirePlan struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	SourceRef string `json:"source_ref"`
}

type wireFact struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	Rationale string `json:"rationale"`
	SourceRef string `json:"source_ref"`
}

type wireDecision struct {
	ID          string   `json:"id"`
	ConflictKey string   `json:"conflict_key"`
	Supersedes  []string `json:"supersedes"`
	Statement   string   `json:"statement"`
	Status      string   `json:"status"`
	Rationale   string   `json:"rationale"`
	SourceRef   string   `json:"source_ref"`
}

type wireRemoval struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	SourceRef string `json:"source_ref"`
}

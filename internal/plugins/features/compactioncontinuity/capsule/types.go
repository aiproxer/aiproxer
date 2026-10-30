// Package capsule contains the provider-neutral, bounded continuity capsule.
//
// The package deliberately has no runtime, provider, or wire-protocol
// dependencies. It is the semantic value object used by the feature's
// adapters and workers.
package capsule

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	SchemaVersion uint8 = 1

	SourceStructured     Source = "structured"
	SourceUserExplicit   Source = "user_explicit"
	SourceUserAcceptance Source = "user_acceptance"
	SourceSemantic       Source = "semantic"

	PlanDraft     PlanStatus = "draft"
	PlanAccepted  PlanStatus = "accepted"
	PlanCompleted PlanStatus = "completed"

	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepCompleted  StepStatus = "completed"
	StepCancelled  StepStatus = "cancelled"

	FactActive     FactStatus = "active"
	FactSuperseded FactStatus = "superseded"
	FactRejected   FactStatus = "rejected"

	DecisionActive     DecisionStatus = "active"
	DecisionSuperseded DecisionStatus = "superseded"
	DecisionRejected   DecisionStatus = "rejected"

	AuthorityUserExplicit   Authority = "user_explicit"
	AuthorityUserAcceptance Authority = "user_acceptance"
	AuthorityStructured     Authority = "structured"
	AuthoritySemantic       Authority = "semantic"
)

type (
	Source         string
	PlanStatus     string
	StepStatus     string
	FactStatus     string
	DecisionStatus string
	Authority      string
)

// BranchKey is the authoritative parent identity used to bind capsule state.
// The auxiliary child A-leg must never be substituted for ALegID.
type BranchKey struct {
	AuthoritativeSessionID string `json:"authoritative_session_id"`
	ALegID                 string `json:"a_leg_id"`
	PrincipalPartition     string `json:"principal_partition"`
}

// BranchBinding returns the content-free binding for an authoritative parent
// branch. At least one authoritative identity must be present.
func BranchBinding(key BranchKey) (string, error) {
	if strings.TrimSpace(key.AuthoritativeSessionID) == "" && strings.TrimSpace(key.ALegID) == "" && strings.TrimSpace(key.PrincipalPartition) == "" {
		return "", fmt.Errorf("%w: branch key is empty", ErrInvalidBranch)
	}
	b, err := marshalCanonical(key)
	if err != nil {
		return "", fmt.Errorf("%w: branch key: %v", ErrInvalidBranch, err)
	}
	h := sha256.Sum256(append([]byte("aip.compaction-continuity.branch.v1\x00"), b...))
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// NewBranchBinding is a convenience for the usual parent identity fields.
func NewBranchBinding(sessionID, aLegID, principalPartition string) (string, error) {
	return BranchBinding(BranchKey{AuthoritativeSessionID: sessionID, ALegID: aLegID, PrincipalPartition: principalPartition})
}

type Envelope struct {
	SchemaVersion        uint8      `json:"schema_version"`
	Revision             uint64     `json:"revision"`
	SourceHighWatermark  string     `json:"source_high_watermark"`
	BranchBinding        string     `json:"branch_binding"`
	ContentDigest        string     `json:"content_digest,omitempty"`
	Plan                 Plan       `json:"plan"`
	Decisions            []Decision `json:"decisions"`
	Constraints          []Fact     `json:"constraints"`
	RejectedAlternatives []Fact     `json:"rejected_alternatives"`
	OpenQuestions        []Fact     `json:"open_questions"`
}

type Plan struct {
	Status PlanStatus `json:"status"`
	Source Source     `json:"source"`
	Steps  []PlanStep `json:"steps"`
}

type PlanStep struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Status    StepStatus `json:"status"`
	SourceRef string     `json:"source_ref"`
}

type Decision struct {
	ID              string         `json:"id"`
	ConflictKey     string         `json:"conflict_key"`
	Supersedes      []string       `json:"supersedes"`
	Statement       string         `json:"statement"`
	Status          DecisionStatus `json:"status"`
	Authority       Authority      `json:"authority"`
	Rationale       string         `json:"rationale"`
	SourceRef       string         `json:"source_ref"`
	StatusSourceRef string         `json:"status_source_ref,omitempty"`
}

// DecisionTransition changes only the terminal status of an existing active
// semantic decision. It is intentionally narrower than Decision so semantic
// extraction cannot rewrite or remove user/structured authority.
type DecisionTransition struct {
	ID        string         `json:"id"`
	Status    DecisionStatus `json:"status"`
	Authority Authority      `json:"authority"`
	SourceRef string         `json:"source_ref"`
}

type Fact struct {
	ID        string     `json:"id"`
	Statement string     `json:"statement"`
	Status    FactStatus `json:"status"`
	Authority Authority  `json:"authority"`
	Rationale string     `json:"rationale"`
	SourceRef string     `json:"source_ref"`
}

// Delta is the validated input to a compare-and-merge operation. A zero
// SchemaVersion is accepted as a convenience for local callers and normalized
// to v1; non-zero unknown versions are rejected.
type Delta struct {
	SchemaVersion        uint8
	BaseRevision         uint64
	BranchBinding        string
	SourceHighWatermark  string
	Plan                 *Plan
	Decisions            []Decision
	DecisionTransitions  []DecisionTransition
	Constraints          []Fact
	RejectedAlternatives []Fact
	OpenQuestions        []Fact
}

// New returns a sealed v1 capsule at revision one for branchBinding.
func New(branchBinding string) (Envelope, error) {
	e := Envelope{
		SchemaVersion: SchemaVersion,
		Revision:      1,
		BranchBinding: branchBinding,
		Plan:          Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{}},
		Decisions:     []Decision{}, Constraints: []Fact{}, RejectedAlternatives: []Fact{}, OpenQuestions: []Fact{},
	}
	if err := e.Seal(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func StableID(kind string, parts ...string) string {
	var buf strings.Builder
	buf.WriteString("aip.compaction-continuity.id.v1\x00")
	buf.WriteString(strings.TrimSpace(kind))
	for _, part := range parts {
		buf.WriteByte(0)
		buf.WriteString(strings.TrimSpace(part))
	}
	h := sha256.Sum256([]byte(buf.String()))
	return "id_" + hex.EncodeToString(h[:16])
}

func StableStepID(text string) string { return StableID("plan-step", normalizeText(text)) }
func StableFactID(kind, statement, sourceRef string) string {
	return StableID(kind, normalizeText(statement), strings.TrimSpace(sourceRef))
}

func normalizeText(s string) string { return strings.Join(strings.Fields(strings.TrimSpace(s)), " ") }

func (e Envelope) Clone() Envelope {
	out := e
	out.Plan.Steps = append([]PlanStep{}, e.Plan.Steps...)
	out.Decisions = append([]Decision{}, e.Decisions...)
	for i := range out.Decisions {
		out.Decisions[i].Supersedes = append([]string{}, e.Decisions[i].Supersedes...)
	}
	out.Constraints = append([]Fact{}, e.Constraints...)
	out.RejectedAlternatives = append([]Fact{}, e.RejectedAlternatives...)
	out.OpenQuestions = append([]Fact{}, e.OpenQuestions...)
	return out
}

// validateString is intentionally shared by all fact validators to keep the
// bounds stable across carrier and semantic extraction paths.
func validateString(name, value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidCapsule, name)
	}
	if len([]byte(value)) > max {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidCapsule, name, max)
	}
	return nil
}

package capsule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const digestDomain = "aip.compaction-continuity.capsule.v1\x00"

var sha256BindingPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Seal normalizes collection fields, validates semantic values, and computes
// the canonical content digest. Only the digest field is excluded from the
// digest input.
func (e *Envelope) Seal() error {
	if e == nil {
		return fmt.Errorf("%w: nil capsule", ErrInvalidCapsule)
	}
	normalizeEnvelope(e)
	if err := e.validate(false); err != nil {
		return err
	}
	digest, err := e.ComputeDigest()
	if err != nil {
		return err
	}
	e.ContentDigest = digest
	return nil
}

func (e Envelope) ComputeDigest() (string, error) {
	if err := e.validate(false); err != nil {
		return "", err
	}
	clone := e
	clone.ContentDigest = ""
	b, err := marshalCanonical(clone)
	if err != nil {
		return "", fmt.Errorf("%w: canonical encoding: %v", ErrInvalidCapsule, err)
	}
	h := sha256.Sum256(append([]byte(digestDomain), b...))
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

func (e Envelope) Verify() error { return e.VerifyBranch("") }

// Validate checks semantic and envelope invariants without requiring a
// caller-supplied branch. Verify additionally checks the self-digest.
func (e Envelope) Validate() error { return e.validate(true) }

func (e Envelope) VerifyBranch(expectedBranch string) error {
	if err := e.validate(true); err != nil {
		return err
	}
	if expectedBranch != "" && e.BranchBinding != expectedBranch {
		return ErrBranchMismatch
	}
	want, err := e.ComputeDigest()
	if err != nil {
		return err
	}
	if e.ContentDigest != want {
		return fmt.Errorf("%w: got %q want %q", ErrDigestMismatch, e.ContentDigest, want)
	}
	return nil
}

func CanonicalBytes(e Envelope) ([]byte, error) {
	if err := e.validate(false); err != nil {
		return nil, err
	}
	e.ContentDigest = ""
	return marshalCanonical(e)
}

func Parse(data []byte) (Envelope, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Envelope{}, fmt.Errorf("%w: empty JSON", ErrInvalidCapsule)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var e Envelope
	if err := dec.Decode(&e); err != nil {
		return Envelope{}, fmt.Errorf("%w: malformed JSON: %v", ErrInvalidCapsule, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Envelope{}, fmt.Errorf("%w: trailing JSON", ErrInvalidCapsule)
	}
	if err := e.Verify(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func normalizeEnvelope(e *Envelope) {
	if e.Plan.Steps == nil {
		e.Plan.Steps = []PlanStep{}
	}
	if e.Decisions == nil {
		e.Decisions = []Decision{}
	}
	if e.Constraints == nil {
		e.Constraints = []Fact{}
	}
	if e.RejectedAlternatives == nil {
		e.RejectedAlternatives = []Fact{}
	}
	if e.OpenQuestions == nil {
		e.OpenQuestions = []Fact{}
	}
	for i := range e.Decisions {
		e.Decisions[i].Supersedes = uniqueSorted(e.Decisions[i].Supersedes)
	}
}

func (e Envelope) validate(withDigest bool) error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema_version %d", ErrInvalidCapsule, e.SchemaVersion)
	}
	if e.Revision == 0 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidCapsule)
	}
	if !sha256BindingPattern.MatchString(e.BranchBinding) {
		return fmt.Errorf("%w: branch_binding", ErrInvalidBranch)
	}
	if len([]byte(e.SourceHighWatermark)) > 512 {
		return fmt.Errorf("%w: source_high_watermark exceeds 512 bytes", ErrInvalidCapsule)
	}
	if e.Plan.Steps == nil || e.Decisions == nil || e.Constraints == nil || e.RejectedAlternatives == nil || e.OpenQuestions == nil {
		return fmt.Errorf("%w: collection fields must be arrays", ErrInvalidCapsule)
	}
	if err := validatePlan(e.Plan); err != nil {
		return err
	}
	decisionIDs := make(map[string]struct{}, len(e.Decisions))
	activeKeys := make(map[string]string)
	for i, d := range e.Decisions {
		if err := validateDecision(d, i); err != nil {
			return err
		}
		if _, exists := decisionIDs[d.ID]; exists {
			return fmt.Errorf("%w: duplicate decision id %q", ErrInvalidCapsule, d.ID)
		}
		decisionIDs[d.ID] = struct{}{}
		if d.Status == DecisionActive {
			if prior, exists := activeKeys[d.ConflictKey]; exists && prior != d.ID {
				return fmt.Errorf("%w: multiple active decisions for conflict_key %q", ErrInvalidCapsule, d.ConflictKey)
			}
			activeKeys[d.ConflictKey] = d.ID
		}
	}
	for _, d := range e.Decisions {
		for _, id := range d.Supersedes {
			if _, exists := decisionIDs[id]; !exists {
				return fmt.Errorf("%w: %q", ErrUnknownSupersede, id)
			}
			if id == d.ID {
				return fmt.Errorf("%w: self-reference %q", ErrInvalidCapsule, id)
			}
		}
	}
	if err := validateFacts("constraints", e.Constraints); err != nil {
		return err
	}
	if err := validateFacts("rejected_alternatives", e.RejectedAlternatives); err != nil {
		return err
	}
	if err := validateFacts("open_questions", e.OpenQuestions); err != nil {
		return err
	}
	if withDigest && !sha256BindingPattern.MatchString(e.ContentDigest) {
		return fmt.Errorf("%w: content_digest", ErrDigestMismatch)
	}
	if len(e.ContentDigest) > 0 && !sha256BindingPattern.MatchString(e.ContentDigest) {
		return fmt.Errorf("%w: content_digest", ErrDigestMismatch)
	}
	return nil
}

func validatePlan(p Plan) error {
	switch p.Status {
	case PlanDraft, PlanAccepted, PlanCompleted:
	default:
		return fmt.Errorf("%w: unknown plan status %q", ErrInvalidCapsule, p.Status)
	}
	if !validSource(p.Source) {
		return fmt.Errorf("%w: unknown plan source %q", ErrInvalidCapsule, p.Source)
	}
	if len(p.Steps) > 100 {
		return fmt.Errorf("%w: too many plan steps", ErrInvalidCapsule)
	}
	ids := map[string]struct{}{}
	inProgress := 0
	for i, s := range p.Steps {
		if err := validateString(fmt.Sprintf("plan.steps[%d].id", i), s.ID, 256, true); err != nil {
			return err
		}
		if err := validateString(fmt.Sprintf("plan.steps[%d].text", i), s.Text, 4096, true); err != nil {
			return err
		}
		if err := validateString(fmt.Sprintf("plan.steps[%d].source_ref", i), s.SourceRef, 512, false); err != nil {
			return err
		}
		if _, exists := ids[s.ID]; exists {
			return fmt.Errorf("%w: duplicate plan step id %q", ErrInvalidCapsule, s.ID)
		}
		ids[s.ID] = struct{}{}
		switch s.Status {
		case StepPending, StepInProgress, StepCompleted, StepCancelled:
		default:
			return fmt.Errorf("%w: unknown plan step status %q", ErrInvalidCapsule, s.Status)
		}
		if s.Status == StepInProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("%w: more than one in-progress step", ErrInvalidCapsule)
	}
	return nil
}

func validateDecision(d Decision, i int) error {
	if err := validateString(fmt.Sprintf("decisions[%d].id", i), d.ID, 256, true); err != nil {
		return err
	}
	if err := validateString(fmt.Sprintf("decisions[%d].conflict_key", i), d.ConflictKey, 512, true); err != nil {
		return err
	}
	if err := validateString(fmt.Sprintf("decisions[%d].statement", i), d.Statement, 4096, true); err != nil {
		return err
	}
	if err := validateString(fmt.Sprintf("decisions[%d].rationale", i), d.Rationale, 4096, false); err != nil {
		return err
	}
	if err := validateString(fmt.Sprintf("decisions[%d].source_ref", i), d.SourceRef, 512, false); err != nil {
		return err
	}
	if err := validateString(fmt.Sprintf("decisions[%d].status_source_ref", i), d.StatusSourceRef, 512, false); err != nil {
		return err
	}
	if !validAuthority(d.Authority) {
		return fmt.Errorf("%w: unknown decision authority %q", ErrInvalidCapsule, d.Authority)
	}
	switch d.Status {
	case DecisionActive, DecisionSuperseded, DecisionRejected:
	default:
		return fmt.Errorf("%w: unknown decision status %q", ErrInvalidCapsule, d.Status)
	}
	if len(d.Supersedes) > 32 {
		return fmt.Errorf("%w: too many supersedes references", ErrInvalidCapsule)
	}
	if len(uniqueSorted(d.Supersedes)) != len(d.Supersedes) {
		return fmt.Errorf("%w: duplicate supersedes references", ErrInvalidCapsule)
	}
	for _, id := range d.Supersedes {
		if err := validateString("supersedes", id, 256, true); err != nil {
			return err
		}
	}
	return nil
}

func validateFacts(name string, facts []Fact) error {
	if len(facts) > 100 {
		return fmt.Errorf("%w: too many %s", ErrInvalidCapsule, name)
	}
	ids := map[string]struct{}{}
	for i, f := range facts {
		if err := validateString(fmt.Sprintf("%s[%d].id", name, i), f.ID, 256, true); err != nil {
			return err
		}
		if err := validateString(fmt.Sprintf("%s[%d].statement", name, i), f.Statement, 4096, true); err != nil {
			return err
		}
		if err := validateString(fmt.Sprintf("%s[%d].rationale", name, i), f.Rationale, 4096, false); err != nil {
			return err
		}
		if err := validateString(fmt.Sprintf("%s[%d].source_ref", name, i), f.SourceRef, 512, false); err != nil {
			return err
		}
		if _, exists := ids[f.ID]; exists {
			return fmt.Errorf("%w: duplicate %s id %q", ErrInvalidCapsule, name, f.ID)
		}
		ids[f.ID] = struct{}{}
		if !validAuthority(f.Authority) {
			return fmt.Errorf("%w: unknown fact authority %q", ErrInvalidCapsule, f.Authority)
		}
		switch f.Status {
		case FactActive, FactSuperseded, FactRejected:
		default:
			return fmt.Errorf("%w: unknown fact status %q", ErrInvalidCapsule, f.Status)
		}
	}
	return nil
}

func validSource(s Source) bool {
	return s == SourceStructured || s == SourceUserExplicit || s == SourceUserAcceptance || s == SourceSemantic
}

func validAuthority(a Authority) bool {
	return a == AuthorityUserExplicit || a == AuthorityUserAcceptance || a == AuthorityStructured || a == AuthoritySemantic
}

func marshalCanonical(value any) ([]byte, error) { return json.Marshal(value) }

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[strings.TrimSpace(value)] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

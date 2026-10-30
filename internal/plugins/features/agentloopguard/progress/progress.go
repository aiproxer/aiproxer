// Package progress contains the pure ALG progress breaker and bounded
// recovery-intent policy. It owns no runtime, terminal, backend, or transcript
// authority; callers retain the returned State for one logical request.
package progress

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

const (
	DefaultNoProgressLimit = 2
	MaxNoProgressLimit     = 64

	MaxRecoveryReasonBytes    = 512
	MaxRecoveryObjectiveBytes = 512
)

// Verdict is the bounded semantic classification consumed by the progress
// policy. NeedsUser is a conservative policy outcome; the current verifier may
// map user-dependent evidence to UNCERTAIN instead.
type Verdict string

const (
	VerdictComplete         Verdict = "COMPLETE"
	VerdictIncomplete       Verdict = "INCOMPLETE"
	VerdictNeedsUser        Verdict = "NEEDS_USER"
	VerdictUncertain        Verdict = "UNCERTAIN"
	ReasonComplete                  = "complete"
	ReasonNeedsUser                 = "user_input_required"
	ReasonUncertain                 = "insufficient_evidence"
	ReasonUnfinished                = "unfinished_objective"
	ReasonNoProgress                = "no_progress"
	ReasonBudgetExhausted           = "budget_exhausted"
	ReasonInvalidInput              = "invalid_input"
	ReasonOutputUncommitted         = "output_not_committed"
	ReasonMissingEvidence           = "insufficient_evidence"
	ReasonMissingSafePoint          = "missing_safe_point"
)

// Action identifies the provider-level result without granting platform
// authority.
type Action string

const (
	ActionContinue  Action = "continue"
	ActionAllowStop Action = "allow_stop"
)

// Config contains only the local no-progress bound. The semantic maximum is
// read from the immutable terminaldecision.Input policy snapshot.
type Config struct {
	NoProgressLimit int
}

// State is a request-scoped value. It stores only a digest and bounded counters
// so a Provider can own it without globals, goroutines, maps, or raw evidence.
type State struct {
	LastFingerprint       string
	HasBaseline           bool
	TotalAttempts         int
	ConsecutiveNoProgress int
	NoProgressTripped     bool
	BudgetExhausted       bool
	Terminal              bool
}

// Evaluation contains the pure policy result and next request-scoped state.
type Evaluation struct {
	State                 State
	Fingerprint           string
	NewProgress           bool
	NoProgressTripped     bool
	BudgetExhausted       bool
	ConsecutiveNoProgress int
	TotalAttempts         int
	SemanticAttempt       int
	MaxSemanticAttempts   int
	Action                Action
	Decision              terminaldecision.Decision
}

var ErrInvalidVerdict = errors.New("agent-loop-guard progress: invalid verdict")

func (v Verdict) known() bool {
	switch v {
	case VerdictComplete, VerdictIncomplete, VerdictNeedsUser, VerdictUncertain:
		return true
	default:
		return false
	}
}

// Fingerprint hashes stable canonical evidence. Request/leg/candidate/action
// identifiers, policy revisions, deadlines, and attempt numbers are excluded
// because they are volatile identity or budget metadata rather than progress.
// Raw evidence never leaves this function.
func Fingerprint(in terminaldecision.Input, verdict Verdict) string {
	h := sha256.New()
	writeField := func(name, value string) {
		_, _ = fmt.Fprintf(h, "%s:%d:%s\n", name, len(value), value)
	}
	writeField("cause", string(in.Candidate.Cause))
	writeField("committed", strconv.FormatBool(in.Candidate.OutputCommitted))
	writeField("objective", normalize(in.Evidence.Objective))
	writeField("recent", normalize(in.Evidence.RecentText))
	writeField("candidate", normalize(in.Evidence.CandidateText))
	writeField("explicit", strconv.FormatBool(in.Evidence.ExplicitCompletion))
	writeField("verdict", string(verdict))

	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	writeField("action_count", strconv.Itoa(count))
	for i := range count {
		action := in.Evidence.Actions[i]
		writeField("action_kind", string(action.Kind))
		writeField("action_status", string(action.Status))
		writeField("action_name", normalize(action.Name))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Evaluate records one candidate observation and returns the next pure policy
// result. Every stop path is conservative and carries no continuation intent.
func Evaluate(in terminaldecision.Input, verdict Verdict, prior State, cfg Config) (Evaluation, error) {
	if err := in.Validate(); err != nil {
		return stopped(prior, ReasonInvalidInput), err
	}
	if !verdict.known() {
		return stopped(prior, ReasonUncertain), ErrInvalidVerdict
	}
	if prior.Terminal {
		result := stopped(prior, terminalReason(prior))
		result.NoProgressTripped = prior.NoProgressTripped
		result.BudgetExhausted = prior.BudgetExhausted
		result.ConsecutiveNoProgress = prior.ConsecutiveNoProgress
		result.TotalAttempts = prior.TotalAttempts
		return result, nil
	}

	next := prior
	if next.TotalAttempts < 0 {
		next.TotalAttempts = 0
	}
	next.TotalAttempts++
	fingerprint := Fingerprint(in, verdict)
	newProgress := !next.HasBaseline || next.LastFingerprint != fingerprint
	if newProgress {
		next.HasBaseline = true
		next.LastFingerprint = fingerprint
		next.ConsecutiveNoProgress = 0
	} else {
		next.ConsecutiveNoProgress++
	}

	semanticAttempt := int(in.Continuation.Attempt)
	if semanticAttempt <= 0 {
		semanticAttempt = next.TotalAttempts
	}
	maxAttempts := int(in.Policy.MaxContinuationAttempts)
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	next.BudgetExhausted = next.BudgetExhausted || semanticAttempt >= maxAttempts || next.TotalAttempts >= maxAttempts
	limit := normalizedNoProgressLimit(cfg.NoProgressLimit)
	next.NoProgressTripped = next.NoProgressTripped || next.ConsecutiveNoProgress >= limit

	result := Evaluation{
		State:                 next,
		Fingerprint:           fingerprint,
		NewProgress:           newProgress,
		NoProgressTripped:     next.NoProgressTripped,
		BudgetExhausted:       next.BudgetExhausted,
		ConsecutiveNoProgress: next.ConsecutiveNoProgress,
		TotalAttempts:         next.TotalAttempts,
		SemanticAttempt:       semanticAttempt,
		MaxSemanticAttempts:   maxAttempts,
	}

	stop := func(reason string) (Evaluation, error) {
		result.State.Terminal = true
		result.Action = ActionAllowStop
		result.Decision = allowStop(reason)
		return result, nil
	}
	if in.Candidate.Cause.Authoritative() {
		return stop("authoritative_candidate")
	}
	if in.Evidence.ExplicitCompletion || verdict == VerdictComplete {
		return stop(ReasonComplete)
	}
	switch verdict {
	case VerdictNeedsUser:
		return stop(ReasonNeedsUser)
	case VerdictUncertain:
		return stop(ReasonUncertain)
	}
	if next.BudgetExhausted {
		return stop(ReasonBudgetExhausted)
	}
	if next.NoProgressTripped {
		return stop(ReasonNoProgress)
	}
	if !in.Candidate.OutputCommitted {
		return stop(ReasonOutputUncommitted)
	}
	if strings.TrimSpace(in.Evidence.Objective) == "" || strings.TrimSpace(in.Evidence.CandidateText) == "" {
		return stop(ReasonMissingEvidence)
	}
	intent, ok := BuildIntent(in, verdict, ReasonUnfinished)
	if !ok {
		return stop(ReasonMissingSafePoint)
	}
	next.Terminal = false
	result.State = next
	result.Action = ActionContinue
	result.Decision = terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: ReasonUnfinished,
		Continue:   &intent,
	}
	return result, nil
}

// BuildIntent returns a bounded internal continuation intent only for concrete
// existing work with a canonical trajectory. Complete, user-dependent,
// incomplete-evidence, and authoritative candidates return no intent.
func BuildIntent(in terminaldecision.Input, verdict Verdict, reason string) (terminaldecision.ContinuationIntent, bool) {
	if verdict != VerdictIncomplete || in.Candidate.Cause.Authoritative() || in.Evidence.ExplicitCompletion || !in.Candidate.OutputCommitted {
		return terminaldecision.ContinuationIntent{}, false
	}
	objective := boundText(in.Evidence.Objective, MaxRecoveryObjectiveBytes)
	if objective == "" || strings.TrimSpace(in.Evidence.CandidateText) == "" {
		return terminaldecision.ContinuationIntent{}, false
	}
	trajectory := strings.TrimSpace(in.Evidence.Lineage.TrajectoryRef)
	if trajectory == "" {
		trajectory = strings.TrimSpace(in.Continuation.TrajectoryRef)
	}
	if trajectory == "" {
		return terminaldecision.ContinuationIntent{}, false
	}
	attempt := int(in.Continuation.Attempt)
	if attempt <= 0 {
		attempt = 1
	}
	maxAttempts := int(in.Policy.MaxContinuationAttempts)
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	reason = boundText(reason, MaxRecoveryReasonBytes)
	instruction := buildInstruction(reason, objective, attempt, maxAttempts)
	intent := terminaldecision.ContinuationIntent{
		TrajectoryRef: trajectory,
		ControlRef:    strings.TrimSpace(in.Evidence.Lineage.ProgressRef),
		Instruction:   instruction,
		Provenance:    "internal-control",
		ReasonCode:    ReasonUnfinished,
	}
	if intent.Validate() != nil {
		return terminaldecision.ContinuationIntent{}, false
	}
	return intent, true
}

func normalizedNoProgressLimit(limit int) int {
	if limit <= 0 {
		return DefaultNoProgressLimit
	}
	if limit > MaxNoProgressLimit {
		return MaxNoProgressLimit
	}
	return limit
}

func stopped(prior State, reason string) Evaluation {
	prior.Terminal = true
	return Evaluation{
		State:                 prior,
		Action:                ActionAllowStop,
		Decision:              allowStop(reason),
		NoProgressTripped:     prior.NoProgressTripped,
		BudgetExhausted:       prior.BudgetExhausted,
		ConsecutiveNoProgress: prior.ConsecutiveNoProgress,
		TotalAttempts:         prior.TotalAttempts,
	}
}

func terminalReason(state State) string {
	if state.BudgetExhausted {
		return ReasonBudgetExhausted
	}
	if state.NoProgressTripped {
		return ReasonNoProgress
	}
	return ReasonComplete
}

func allowStop(reason string) terminaldecision.Decision {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: reason}
}

func buildInstruction(reason, objective string, attempt, maxAttempts int) string {
	var b strings.Builder
	b.Grow(2048 + len(reason) + len(objective))
	b.WriteString("<automated-recovery>\n")
	b.WriteString("This is an internal recovery instruction, not a new user request, approval, permission, or expansion of scope. The user has not sent a new message.\n\n")
	b.WriteString("Re-read the existing user request and the work already completed.\n")
	b.WriteString("If concrete unfinished work from that request can be performed without new user input, resume exactly that work from the last safe point.\n\n")
	b.WriteString("If the requested work is complete, DO NOT invent, repeat, broaden, optimize, or discover additional work. End normally.\n\n")
	b.WriteString("If the next step requires user input, permission, approval, credentials, clarification, or a choice, do not assume it; end normally so the user can respond.\n\n")
	fmt.Fprintf(&b, "Recovery reason: %s\nRemaining objective: %s\nAttempt %d/%d\n", reason, objective, attempt, maxAttempts)
	b.WriteString("</automated-recovery>")
	return b.String()
}

func normalize(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func boundText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	cut := value[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

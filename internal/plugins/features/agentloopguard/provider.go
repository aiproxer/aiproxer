package agentloopguard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/causepolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/verifier"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

const providerID = "agent-loop-guard"

const (
	reasonCanceled          = "context_canceled"
	reasonDeadline          = "deadline_exceeded"
	reasonInvalidInput      = "invalid_input"
	reasonExplicitComplete  = "explicit_completion"
	reasonOutputUncommitted = "output_not_committed"
	reasonInsufficient      = "insufficient_evidence"
	reasonUnfinished        = "unfinished_objective"
	reasonBudgetExhausted   = "budget_exhausted"
	reasonInvalidProgress   = "invalid_progress_state"
)

// provider is the first concrete feature policy behind the generic platform
// seam. It has no runtime or lifecycle authorities; all state arrives in the
// immutable terminaldecision.Input value.
type provider struct{ cfg Config }

var _ terminaldecision.Provider = provider{}

// NewProvider constructs the conservative, stateless ALG provider with
// default provider settings. Feature composition validates the raw config
// before calling this constructor.
//
// An explicitly selected strategy or an explicit protocol-reprompt bound is new
// input: it is delegated to NewConfiguredProvider so strategy selection and
// mutual exclusion are enforced strictly. This signature has no error result, so
// an invalid explicit configuration returns a nil interface rather than the
// legacy default provider. Callers that select or configure a strategy must use
// NewConfiguredProvider to receive the error and must not interpret this
// compatibility nil as a disabled or generic no-provider outcome.
func NewProvider(config ...Config) terminaldecision.Provider {
	if len(config) > 0 {
		requested := config[0]
		if requested.Strategy != "" || requested.MaxProtocolReprompts != 0 {
			provider, err := NewConfiguredProvider(requested)
			if err != nil {
				return nil
			}
			return provider
		}
	}
	cfg, _ := (Config{
		Enabled:                  true,
		VerifierRole:             DefaultVerifierRole,
		VerifierTimeoutSeconds:   DefaultVerifierTimeoutSeconds,
		MaxSemanticContinuations: DefaultMaxSemanticContinuations,
		NoProgressLimit:          DefaultNoProgressLimit,
	}).Normalize()
	if len(config) > 0 {
		requested := config[0]
		if requested.VerifierRole == "" {
			requested.VerifierRole = DefaultVerifierRole
		}
		if requested.VerifierTimeoutSeconds == 0 {
			requested.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
		}
		if requested.MaxSemanticContinuations == 0 {
			requested.MaxSemanticContinuations = DefaultMaxSemanticContinuations
		}
		if requested.NoProgressLimit == 0 {
			requested.NoProgressLimit = DefaultNoProgressLimit
		}
		if normalized, err := requested.Normalize(); err == nil {
			cfg = normalized
		}
	}
	return provider{cfg: cfg}
}

// NewConfiguredProvider is the single error-returning terminal-policy
// constructor for a selected ALG strategy.
//
// It normalizes the raw programmatic configuration strictly once and switches
// on the resolved strategy: the semantic verifier keeps the existing legacy
// provider value, and the explicit-completion strategy gets the distinct
// preferred receiver that owns only the protocol numeric limits. It never calls
// NewProvider, and an unknown or incompatible configuration is refused instead
// of falling back to another strategy.
//
// It has no enable/disable authority: Enabled stays the composition gate.
func NewConfiguredProvider(cfg Config) (terminaldecision.Provider, error) {
	normalized, err := NormalizeProgrammatic(cfg)
	if err != nil {
		return nil, err
	}
	switch normalized.Strategy {
	case StrategySemanticVerifier:
		return provider{cfg: normalized}, nil
	case StrategyAttemptCompletion:
		return preferredProvider{
			maxProtocolReprompts: normalized.MaxProtocolReprompts,
			noProgressLimit:      normalized.NoProgressLimit,
		}, nil
	default:
		// Unreachable: NormalizeProgrammatic resolves every other selector.
		return nil, fmt.Errorf("%s: strategy must be %s or %s", ID, StrategyAttemptCompletion, StrategySemanticVerifier)
	}
}

func (provider) ID() string { return providerID }

func (p provider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return allowStop(reasonDeadline), nil
		}
		return allowStop(reasonCanceled), nil
	}
	if err := in.Validate(); err != nil {
		return allowStop(reasonInvalidInput), nil
	}
	causeInput := in
	// Verification is the configured authority for explicit completion; let the
	// shared safety/cause gate classify the remaining evidence first.
	if p.cfg.ExplicitCompletionPolicy == ExplicitCompletionPolicyVerify {
		causeInput.Evidence.ExplicitCompletion = false
	}
	causeResult := causepolicy.Evaluate(causeInput)
	if causeResult.Eligibility != causepolicy.EligibilityVerifier {
		return allowStop(string(causeResult.Reason)), nil
	}
	prior, ok := decodeProgressState(in)
	if !ok {
		return allowStop(reasonInvalidProgress), nil
	}

	projected := in
	projected.Policy.MaxContinuationAttempts = effectiveSemanticCap(p.cfg.MaxSemanticContinuations, in.Policy.MaxContinuationAttempts)
	verdict := progress.VerdictIncomplete
	if p.cfg.ExplicitCompletionPolicy != ExplicitCompletionPolicyTrust || !in.Evidence.ExplicitCompletion {
		semantic, err := verifier.New(in.Auxiliary, verifier.Config{
			Role:    p.cfg.VerifierRole,
			Timeout: p.cfg.VerifierTimeout,
		}).Verify(ctx, in)
		if err != nil || semantic.Kind == verifier.VerdictUncertain {
			return allowStop(progress.ReasonUncertain), nil
		}
		switch semantic.Kind {
		case verifier.VerdictComplete:
			if in.Evidence.ExplicitCompletion {
				return allowStop(reasonExplicitComplete), nil
			}
			return allowStop(progress.ReasonComplete), nil
		case verifier.VerdictIncomplete:
			if in.Evidence.ExplicitCompletion {
				projected.Evidence.ExplicitCompletion = false
			}
		default:
			return allowStop(progress.ReasonUncertain), nil
		}
	}
	if !hasConcreteUnfinishedWork(projected) {
		return allowStop(progress.ReasonMissingSafePoint), nil
	}
	evaluation, err := progress.Evaluate(projected, verdict, prior, progress.Config{
		NoProgressLimit: p.cfg.NoProgressLimit,
	})
	if err != nil {
		return allowStop(progress.ReasonUncertain), nil
	}
	if evaluation.Action != progress.ActionContinue || evaluation.Decision.Continue == nil {
		return evaluation.Decision, nil
	}
	token, err := progress.EncodeState(evaluation.State)
	if err != nil {
		return allowStop(reasonInvalidProgress), nil
	}
	intent := *evaluation.Decision.Continue
	intent.ControlRef = token
	if err := intent.Validate(); err != nil {
		return allowStop(reasonInvalidProgress), nil
	}
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: progress.ReasonUnfinished,
		Continue:   &intent,
	}, nil
}

// hasConcreteUnfinishedWork is the final provider-local safe-point check. The
// shared cause policy owns candidate and tool safety; this only ensures the
// continuation describes an actually unfinished canonical message.
func hasConcreteUnfinishedWork(in terminaldecision.Input) bool {
	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	for i := range count {
		action := in.Evidence.Actions[i]
		if action.Kind == aipapi.ItemKindMessage && action.Status == aipapi.ItemStatusInProgress {
			return true
		}
	}
	return false
}

func effectiveSemanticCap(configCap int, platformCap uint8) uint8 {
	if configCap <= 0 {
		configCap = DefaultMaxSemanticContinuations
	}
	if configCap > 255 {
		configCap = 255
	}
	cap := uint8(configCap)
	if platformCap > 0 && platformCap < cap {
		return platformCap
	}
	return cap
}

func decodeProgressState(in terminaldecision.Input) (progress.State, bool) {
	ref := strings.TrimSpace(in.Evidence.Lineage.ProgressRef)
	attempt := max(in.Continuation.Attempt, in.Evidence.Lineage.Attempt)
	if ref == "" {
		return progress.State{}, attempt <= 1
	}
	// Existing first-candidate lineage references predate the opaque state
	// token. They are not prior progress state until a continuation is emitted.
	if attempt <= 1 && !strings.HasPrefix(ref, "alg-state-v1.") {
		return progress.State{}, true
	}
	state, err := progress.DecodeState(ref)
	if err != nil {
		return progress.State{}, false
	}
	return state, true
}

func allowStop(reason string) terminaldecision.Decision {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: reason}
}

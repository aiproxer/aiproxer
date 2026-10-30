package sessionclassification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

var (
	// ErrInvalidClassifier reports a classifier value this feature cannot serve.
	ErrInvalidClassifier = errors.New("session classification: invalid classifier")
	// ErrStateUnavailable reports that process-owned classification state is
	// not usable for the current turn. It fails open: the caller preserves the
	// request and the conservative classification.
	ErrStateUnavailable = errors.New("session classification: process state unavailable")
)

// StateAuthority resolves the process-owned classification state used by one
// generation's classifier. Resolution is lazy by design: composing a generation
// must not initialize feature state, so the first request after candidate
// preparation resolves the already-initialized shared coordinator. A
// standard-distribution process holder satisfies this contract.
type StateAuthority interface {
	// ClassificationState returns the initialized process-owned state
	// authority, or an error when no enabled generation has prepared it.
	ClassificationState() (Store, error)
}

// ClassifierDeps binds one generation's immutable policy to the process-owned
// state authority. There is no per-request or per-generation state here: the
// authoritative store is shared across generations so positive classifications
// survive reload.
type ClassifierDeps struct {
	// State resolves the shared process coordinator. Required.
	State StateAuthority
	// Remote is the generation's remote decider. It is required by, and only
	// accepted in, the modes whose promotion depends on a remote decision, so a
	// heuristic generation cannot hold a network-capable dependency at all
	// (requirements 6.1, 6.2, 6.10).
	Remote RemoteDecider
	// Observer receives bounded classification observations. Nil disables
	// observation entirely, which is what keeps requirement 9.6 true for a
	// generation that never publishes a classifier.
	Observer Observer
	// Now supplies promotion, lease, and attempt timestamps. Nil selects
	// time.Now.
	Now func() time.Time
}

// Classifier is the concrete standard-feature classifier bound to one immutable
// generation. It holds only bounded policy, the shared state authority, the
// generation's remote decider, and a bounded observation sink; it never retains
// request content, raw identities, or per-session workers.
type Classifier struct {
	cfg           Config
	state         StateAuthority
	remoteDecider RemoteDecider
	remote        RemoteConfig
	observer      Observer
	now           func() time.Time
}

var _ sdkclassification.Classifier = (*Classifier)(nil)

// NewClassifier validates one generation's policy and binds it to the
// process-owned state authority. An unservable policy is rejected here so a
// candidate generation fails before publication instead of serving a partially
// configured mode.
func NewClassifier(cfg Config, deps ClassifierDeps) (*Classifier, error) {
	if deps.State == nil {
		return nil, fmt.Errorf("%w: state authority is required", ErrInvalidClassifier)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClassifier, err)
	}
	policy := cfg
	if policy.Mode == "" {
		policy.Mode = ModeHeuristic
	}
	remote, err := bindRemoteDecider(policy, deps.Remote)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClassifier, err)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Classifier{
		cfg:           policy,
		state:         deps.State,
		remoteDecider: deps.Remote,
		remote:        remote,
		observer:      deps.Observer,
		now:           now,
	}, nil
}

// bindRemoteDecider checks the decider dependency against the generation's mode
// and returns the copied remote settings the classifier will enforce.
//
// The copy matters: a decoded configuration is owned by the caller, so without it
// an in-place edit after construction could change the timeout, threshold, or
// attempt budget an already-admitted generation enforces (requirement 8.5).
//
// The mode check is the structural half of requirements 6.1, 6.2, and 6.10. A
// heuristic generation is refused a decider, so it cannot construct, hold, or
// call one; a jev or hybrid generation is refused without one, so a partially
// configured remote mode fails the candidate before publication rather than
// silently degrading to local-only classification.
func bindRemoteDecider(cfg Config, decider RemoteDecider) (RemoteConfig, error) {
	switch cfg.Mode {
	case ModeJev, ModeHybrid:
		if decider == nil {
			return RemoteConfig{}, fmt.Errorf("%w: mode %q requires a remote decider", ErrRemoteNotConfigured, cfg.Mode)
		}
		return *cfg.Remote, nil
	default:
		if decider != nil {
			return RemoteConfig{}, fmt.Errorf("%w: mode %q never constructs or calls a remote decider", ErrRemoteNotConfigured, cfg.Mode)
		}
		return RemoteConfig{}, nil
	}
}

// ID returns the stable identity of the standard session-classification feature.
func (c *Classifier) ID() string {
	if c == nil {
		return ""
	}
	return ID
}

// Classify evaluates one bounded metadata snapshot against this generation's
// policy and the shared monotonic state. It never mutates request content,
// never scans a transcript, and never downgrades an established positive
// classification. State failures return an error with the conservative snapshot
// so the generic stage runner can fail open without rejecting the request.
func (c *Classifier) Classify(ctx context.Context, in sdkclassification.Input) (session.Classification, error) {
	if c == nil || c.state == nil {
		return session.Classification{}, ErrInvalidClassifier
	}
	current := in.Session.Classification
	// Requirement 1.4/6.5: an accepted positive classification is immutable for
	// the whole logical session, so no state work is required for later turns.
	if current.IsCodingAgent() {
		c.observeEvaluation(EvaluationPreserved)
		return current, nil
	}
	if err := ValidateStoreContext(ctx); err != nil {
		// A nil or already-canceled context is a caller-side rejection, not a
		// classification decision. It is still reported through the bounded
		// fail-open outcome so no evaluation is silently unobserved.
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, err
	}
	key, keyErr := ResolveKey(in.Session)
	if keyErr != nil {
		// Without proxy-owned authority there is nothing to scope state to, and
		// a client-controlled hint is never state authority (requirements 2.2/2.3).
		c.observeEvaluation(EvaluationNoAuthority)
		return current, nil
	}
	store, err := c.state.ClassificationState()
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if store == nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, ErrStateUnavailable
	}

	record, found, err := store.Load(ctx, key)
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if found && record.Classification.IsCodingAgent() {
		c.observeEvaluation(EvaluationRestored)
		return record.Classification, nil
	}

	decision := EvaluateLocal(c.cfg, in)
	if !decision.Promotes {
		// Unknown stays unknown unless a mode whose promotion depends on a remote
		// decision performs one. Absence of evidence is not a negative
		// classification and creates no durable state (requirements 6.2, 6.3,
		// 6.4, 6.9).
		return c.classifyUnknown(ctx, key, store, in)
	}
	proposal := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     decision.Source,
		Confidence: session.ConfidenceHigh,
		Evidence:   decision.EvidenceCode,
		Revision:   1,
	}
	if err := ValidatePositiveProposal(proposal); err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return session.Classification{}, err
	}
	promoted, didPromote, err := store.Promote(ctx, key, proposal, c.now())
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return session.Classification{}, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if !promoted.Classification.IsCodingAgent() {
		// A store that neither accepted the proposal nor holds a positive leaves
		// the session unknown; absence of evidence is not a negative
		// classification.
		c.observeEvaluation(EvaluationRestored)
		return session.Classification{}, nil
	}
	// Requirement 9.1/2.5: the transition observation belongs to the caller that
	// actually established the first positive. A store coalesces concurrent
	// attempts for one authority key and returns the winner's positive record
	// with promoted=false to a coalesced-flight waiter or a cached-positive
	// short-circuit, so record positivity alone cannot identify the owner.
	if !didPromote {
		// A concurrent turn owns this session's first positive. Requirement 1.4
		// still requires this turn to project the accepted positive, so only the
		// observation is suppressed here.
		c.observeEvaluation(EvaluationRestored)
		return promoted.Classification, nil
	}
	c.observeTransition(promoted.Classification)
	c.observeEvaluation(EvaluationPromoted)
	return promoted.Classification, nil
}

// classifyUnknown resolves a turn that local evaluation did not promote.
//
// It is the single decision point for the three modes. A heuristic turn ends as
// unknown. A jev or hybrid turn that is still remote-eligible runs the bounded
// remote-decision phase, which is the only path in those modes that can promote.
// A turn whose prospective local match was excluded ends as unknown without any
// remote call, because an excluded identity is not sent to an external service
// (requirements 3.7, 6.2, 6.3, 6.4).
func (c *Classifier) classifyUnknown(ctx context.Context, key Key, store Store, in sdkclassification.Input) (session.Classification, error) {
	// Requirement 3.7: an exclusion prevented a prospective local match. This is
	// the most specific bounded fact available, so it is reported ahead of any
	// remote diagnostic and stays visible in every mode. It is reported without
	// echoing the excluded value.
	if ExcludedIdentity(c.cfg, in.Evidence.ClientUserAgent) {
		c.observeEvaluation(EvaluationExcluded)
		return session.Classification{}, nil
	}
	if !c.remoteEligible() {
		c.observeEvaluation(EvaluationUnknown)
		return session.Classification{}, nil
	}
	result, outcome, err := c.runRemote(ctx, key, store, in)
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return session.Classification{}, err
	}
	if result.classification.IsCodingAgent() {
		// Requirement 9.1: the transition belongs to the turn that established
		// the first positive. A refused claim or a stale completion observed
		// another turn's value, so only this turn's own accepted completion records
		// the transition; every observer of an established positive still projects
		// it (requirement 1.4).
		if result.promoted {
			c.observeTransition(result.classification)
			c.observeEvaluation(EvaluationPromoted)
		} else {
			c.observeEvaluation(EvaluationRestored)
		}
		return result.classification, nil
	}
	c.observeEvaluation(c.remoteNonPromotionOutcome(outcome))
	return session.Classification{}, nil
}

// remoteNonPromotionOutcome maps a remote phase that produced no positive to the
// closed diagnostic vocabulary of requirement 9.2. A remote failure is reported
// through its bounded remote outcome rather than as a bare unknown, and no
// outcome ever reports a negative classification (requirements 6.9, 9.2).
func (c *Classifier) remoteNonPromotionOutcome(outcome RemoteOutcome) EvaluationOutcome {
	switch outcome {
	case RemoteTimeout:
		return EvaluationRemoteTimeout
	case RemoteNetworkError, RemoteServerError, RemoteRateLimited, RemoteMalformed:
		return EvaluationRemoteError
	default:
		// A refused claim, a spent budget, a below-threshold result, and refused
		// input all leave the session unknown with no remote decision taken.
		return EvaluationUnknown
	}
}

// remoteEligible reports whether this generation's mode leaves a still-unknown
// turn eligible for a configured remote decision.
//
// The answer is a property of the mode alone, and that is deliberate rather than
// a simplification. Requirement 6.3 makes a jev-mode promotion depend entirely on
// the configured remote decision, so every still-unknown jev turn is
// remote-eligible. Requirement 6.4 makes a hybrid-mode turn remote-eligible
// exactly when local evaluation found nothing decisive, and that condition is
// already guaranteed here: decisionForEvidence sets Promotes for every hybrid
// evidence code, so a hybrid turn reaches classifyUnknown only with an empty
// decision. Testing the evidence code again would be unreachable in one mode and
// misleading in the other.
//
// A heuristic generation holds no decider at all - bindRemoteDecider refuses one -
// so no mode reaches the remote phase without a decider to call (requirement 6.2).
func (c *Classifier) remoteEligible() bool {
	switch c.cfg.Mode {
	case ModeJev, ModeHybrid:
		return true
	default:
		return false
	}
}

// observeEvaluation records one bounded outcome. The observation sink is
// optional, so a deployment that never configures one performs no work beyond a
// nil check (requirements 9.6, 10.8).
func (c *Classifier) observeEvaluation(outcome EvaluationOutcome) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveEvaluation(EvaluationObservation{Mode: c.cfg.Mode, Outcome: outcome})
}

// observeTransition records the single first accepted positive transition for a
// logical session, projecting only the bounded snapshot the store assigned
// (requirements 9.1, 9.5).
func (c *Classifier) observeTransition(classification session.Classification) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveTransition(TransitionObservation{
		Source:     classification.Source,
		Confidence: classification.Confidence,
		Evidence:   classification.Evidence,
		Revision:   classification.Revision,
	})
}

// observeRemote records one bounded remote attempt outcome with its measured
// latency. Both fields are closed-vocabulary values the observer validates
// before export, and neither carries the request or response body
// (requirements 9.3, 9.4).
func (c *Classifier) observeRemote(outcome RemoteOutcome, latency time.Duration) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveRemote(RemoteObservation{Outcome: outcome, Latency: latency})
}

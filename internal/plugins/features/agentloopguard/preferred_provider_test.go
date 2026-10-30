package agentloopguard

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// forbiddenPreferredCollector fails every auxiliary call and counts them. The
// preferred strategy must never construct a verifier or issue an auxiliary
// request, so any call here is a genuine failure rather than a tolerated one.
type forbiddenPreferredCollector struct {
	calls int
}

func (c *forbiddenPreferredCollector) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	c.calls++
	return aipapi.Collected{}, errors.New("preferred strategy must not collect")
}

func (c *forbiddenPreferredCollector) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	c.calls++
	return nil, errors.New("preferred strategy must not stream")
}

func preferredProviderFromConfig(t *testing.T, cfg Config) terminaldecision.Provider {
	t.Helper()
	p, err := NewConfiguredProvider(cfg)
	if err != nil {
		t.Fatalf("NewConfiguredProvider(%+v): %v", cfg, err)
	}
	if p == nil {
		t.Fatalf("NewConfiguredProvider(%+v) returned a nil provider", cfg)
	}
	return p
}

func TestNewConfiguredProviderPreferredCarriesOnlyProtocolLimits(t *testing.T) {
	t.Parallel()

	p := preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion})
	preferred, ok := p.(preferredProvider)
	if !ok {
		t.Fatalf("provider type=%T, want preferredProvider", p)
	}
	if preferred.maxProtocolReprompts != DefaultMaxProtocolReprompts {
		t.Fatalf("max protocol reprompts=%d, want %d", preferred.maxProtocolReprompts, DefaultMaxProtocolReprompts)
	}
	if preferred.noProgressLimit != DefaultNoProgressLimit {
		t.Fatalf("no-progress limit=%d, want %d", preferred.noProgressLimit, DefaultNoProgressLimit)
	}

	// The preferred receiver owns no legacy verifier field, no legacy default,
	// and no derived duration, and it is a different implementation than the
	// legacy provider value.
	typ := reflect.TypeFor[preferredProvider]()
	if typ.NumField() != 2 {
		t.Fatalf("preferredProvider fields=%d, want exactly the two protocol numeric limits", typ.NumField())
	}
	for field := range typ.Fields() {
		if field.Type.Kind() != reflect.Int {
			t.Fatalf("preferredProvider field %q kind=%s, want an int protocol limit", field.Name, field.Type.Kind())
		}
	}
	if _, ok := p.(provider); ok {
		t.Fatal("preferred dispatch must not reuse the legacy verifier provider value")
	}
	if p.ID() != providerID {
		t.Fatalf("preferred ID=%q, want %q", p.ID(), providerID)
	}
}

func TestNewConfiguredProviderRejectsInvalidPreferredConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "mixed verifier role", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, VerifierRole: "loop_guard"}},
		{name: "mixed verifier timeout seconds", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, VerifierTimeoutSeconds: 4}},
		{name: "mixed verifier duration", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, VerifierTimeout: 4 * time.Second}},
		{name: "mixed semantic continuations", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxSemanticContinuations: 2}},
		{name: "mixed completion policy", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify}},
		{name: "unknown strategy", cfg: Config{Enabled: true, Strategy: Strategy("sometimes")}},
		{name: "blank strategy", cfg: Config{Enabled: true, Strategy: Strategy("   ")}},
		{name: "negative protocol cap", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: -1}},
		{name: "protocol cap above bound", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: MaxMaxProtocolReprompts + 1}},
		{name: "no-progress above bound", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, NoProgressLimit: MaxNoProgressLimit + 1}},
		{name: "disabled but invalid", cfg: Config{Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: MaxMaxProtocolReprompts + 1}},
		{name: "disabled unknown strategy", cfg: Config{Strategy: Strategy("sometimes")}},
		{name: "disabled mixed", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierRole: "loop_guard"}},
		{name: "protocol cap on legacy strategy", cfg: Config{Enabled: true, Strategy: StrategySemanticVerifier, MaxProtocolReprompts: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewConfiguredProvider(tc.cfg)
			if err == nil {
				t.Fatal("expected a construction error for an invalid preferred configuration")
			}
			if p != nil {
				t.Fatalf("provider=%T(%v), want a nil provider on construction error", p, p)
			}
		})
	}
}

func TestNewConfiguredProviderExplicitSemanticVerifierKeepsLegacyProvider(t *testing.T) {
	t.Parallel()

	p, err := NewConfiguredProvider(Config{Enabled: true, Strategy: StrategySemanticVerifier, VerifierRole: "custom_verifier"})
	if err != nil {
		t.Fatalf("NewConfiguredProvider: %v", err)
	}
	legacy, ok := p.(provider)
	if !ok {
		t.Fatalf("provider type=%T, want the existing legacy provider value", p)
	}
	if legacy.cfg.VerifierRole != "custom_verifier" || legacy.cfg.Strategy != StrategySemanticVerifier {
		t.Fatalf("legacy config=%+v, want the normalized explicit semantic verifier config", legacy.cfg)
	}
}

func TestNewProviderKeepsLegacyOmittedStrategyBehavior(t *testing.T) {
	t.Parallel()

	if _, ok := NewProvider().(provider); !ok {
		t.Fatalf("no-argument provider type=%T, want the legacy provider value", NewProvider())
	}
	partial := NewProvider(Config{Enabled: true})
	legacy, ok := partial.(provider)
	if !ok || legacy.cfg.VerifierRole != DefaultVerifierRole ||
		legacy.cfg.VerifierTimeoutSeconds != DefaultVerifierTimeoutSeconds ||
		legacy.cfg.VerifierTimeout != time.Duration(DefaultVerifierTimeoutSeconds)*time.Second ||
		legacy.cfg.MaxSemanticContinuations != DefaultMaxSemanticContinuations ||
		legacy.cfg.NoProgressLimit != DefaultNoProgressLimit {
		t.Fatalf("partial legacy provider=%+v, want unchanged historical defaults", legacy)
	}
	// Historical invalid legacy configuration still falls back to defaults.
	invalid := NewProvider(Config{Enabled: true, VerifierTimeoutSeconds: MaxVerifierTimeoutSeconds + 1})
	fallback, ok := invalid.(provider)
	if !ok || fallback.cfg.VerifierTimeoutSeconds != DefaultVerifierTimeoutSeconds {
		t.Fatalf("invalid legacy provider=%+v, want the unchanged default fallback", fallback)
	}
}

func TestNewProviderDispatchesExplicitStrategyWithoutPublishingInvalidConfig(t *testing.T) {
	t.Parallel()

	preferred := NewProvider(Config{Enabled: true, Strategy: StrategyAttemptCompletion})
	concrete, ok := preferred.(preferredProvider)
	if !ok {
		t.Fatalf("provider type=%T, want preferredProvider", preferred)
	}
	if concrete.maxProtocolReprompts != DefaultMaxProtocolReprompts {
		t.Fatalf("max protocol reprompts=%d, want %d", concrete.maxProtocolReprompts, DefaultMaxProtocolReprompts)
	}
	legacy := NewProvider(Config{Enabled: true, Strategy: StrategySemanticVerifier, VerifierRole: "custom_verifier"})
	if _, ok := legacy.(provider); !ok {
		t.Fatalf("explicit legacy provider type=%T, want the legacy provider value", legacy)
	}

	for name, cfg := range map[string]Config{
		"mixed":                    {Enabled: true, Strategy: StrategyAttemptCompletion, VerifierRole: "loop_guard"},
		"unknown":                  {Enabled: true, Strategy: Strategy("sometimes")},
		"protocol cap on legacy":   {Enabled: true, Strategy: StrategySemanticVerifier, MaxProtocolReprompts: 1},
		"protocol cap above bound": {Enabled: true, Strategy: Strategy(" Attempt_Completion "), MaxProtocolReprompts: MaxMaxProtocolReprompts + 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NewProvider(cfg); got != nil {
				t.Fatalf("provider=%T(%v), want a nil interface for an invalid explicit configuration", got, got)
			}
		})
	}

	// Case and surrounding whitespace normalize only inside the strict constructor.
	padded := NewProvider(Config{Enabled: true, Strategy: Strategy(" Attempt_Completion ")})
	if _, ok := padded.(preferredProvider); !ok {
		t.Fatalf("padded selector provider type=%T, want preferredProvider", padded)
	}
}

// preferredInput is an eligible missing-completion-signal candidate: an active
// proxy control protocol, a normal cause, safe ordinary tool state, a retained
// trajectory, and the initial live B-leg reference.
func preferredInput() terminaldecision.Input {
	in := algInput(terminaldecision.CandidateCauseNormal)
	in.Candidate.OutputCommitted = true
	in.Evidence.ExplicitCompletionExpected = true
	in.Evidence.ExplicitCompletion = false
	in.Evidence.Lineage.ProgressRef = in.Request.BLegID
	in.Policy.MaxContinuationAttempts = 3
	in.Continuation.Attempt = 1
	in.Evidence.Lineage.Attempt = 1
	return in
}

func TestPreferredProviderAcceptanceMatrix(t *testing.T) {
	t.Parallel()

	// A prior state with one consumed reprompt keeps the platform-cap and
	// exhaustion cases reachable.
	spent, err := protocolstate.Encode(protocolstate.State{Reprompts: 1, LastFingerprint: protocolstate.Fingerprint(preferredInput())})
	if err != nil {
		t.Fatalf("protocolstate.Encode: %v", err)
	}
	terminal, err := protocolstate.Encode(protocolstate.State{
		Reprompts:       1,
		LastFingerprint: protocolstate.Fingerprint(preferredInput()),
		Terminal:        true,
	})
	if err != nil {
		t.Fatalf("protocolstate.Encode: %v", err)
	}
	noProgress, err := protocolstate.Encode(protocolstate.State{
		Reprompts:             1,
		LastFingerprint:       protocolstate.Fingerprint(preferredInput()),
		ConsecutiveNoProgress: 2,
	})
	if err != nil {
		t.Fatalf("protocolstate.Encode: %v", err)
	}

	cases := []struct {
		name       string
		mutate     func(in *terminaldecision.Input)
		kind       terminaldecision.DecisionKind
		reason     string
		wantContue bool
	}{
		{
			name:   "eligible missing signal continues",
			mutate: func(in *terminaldecision.Input) {},
			kind:   terminaldecision.DecisionContinue,
			reason: protocolpolicy.ReasonMissingSignal, wantContue: true,
		},
		{
			name:   "trusted native completion allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.ExplicitCompletion = true },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonExplicitCompletion,
		},
		{
			name: "trusted proxy completion allows stop",
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.ExplicitCompletion = true
				in.Evidence.CandidateText = ""
			},
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonExplicitCompletion,
		},
		{
			name:   "authoritative refusal allows stop",
			mutate: func(in *terminaldecision.Input) { in.Candidate.Cause = terminaldecision.CandidateCauseRefusal },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonAuthoritative,
		},
		{
			name:   "inactive protocol allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.ExplicitCompletionExpected = false },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonProtocolInactive,
		},
		{
			name: "unsafe ordinary tool state allows stop",
			mutate: func(in *terminaldecision.Input) {
				in.Evidence.Actions[0].Status = aipapi.ItemStatusInProgress
			},
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonUnsafeAction,
		},
		{
			name: "pre-output transport failure keeps recovery ownership",
			mutate: func(in *terminaldecision.Input) {
				in.Candidate.Cause = terminaldecision.CandidateCauseTransport
				in.Candidate.OutputCommitted = false
			},
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonPreOutputFailure,
		},
		{
			name:   "missing objective allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.Objective = "   " },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonMissingObjective,
		},
		{
			name: "missing trajectory allows stop",
			mutate: func(in *terminaldecision.Input) {
				in.Continuation.TrajectoryRef = ""
				in.Evidence.Lineage.TrajectoryRef = ""
			},
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonMissingTrajectory,
		},
		{
			name:       "no output committed still continues on canonical facts",
			mutate:     func(in *terminaldecision.Input) { in.Candidate.OutputCommitted = false; in.Evidence.CandidateText = "" },
			kind:       terminaldecision.DecisionContinue,
			reason:     protocolpolicy.ReasonMissingSignal,
			wantContue: true,
		},
		{
			name:   "post-output committed continues",
			mutate: func(in *terminaldecision.Input) { in.Candidate.OutputCommitted = true },
			kind:   terminaldecision.DecisionContinue,
			reason: protocolpolicy.ReasonMissingSignal, wantContue: true,
		},
		{
			name:   "corrupt protocol state allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.Lineage.ProgressRef = "alg-proto-v1.corrupt" },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonInvalidState,
		},
		{
			name:   "legacy state reference allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.Lineage.ProgressRef = "alg-state-v1.legacy" },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonInvalidState,
		},
		{
			name: "retry progress reference with spent budget",
			mutate: func(in *terminaldecision.Input) {
				in.Continuation.Attempt = 2
				in.Evidence.Lineage.Attempt = 2
				in.Evidence.Lineage.ProgressRef = spent
				in.Evidence.CandidateText = "different canonical candidate text"
			},
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonBudgetExhausted,
		},
		{
			name:   "terminal state allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.Lineage.ProgressRef = terminal },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonProtocolTerminal,
		},
		{
			name:   "no-progress breaker allows stop",
			mutate: func(in *terminaldecision.Input) { in.Evidence.Lineage.ProgressRef = noProgress },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonNoProgress,
		},
		{
			name:   "platform cap exhausted allows stop",
			mutate: func(in *terminaldecision.Input) { in.Policy.MaxContinuationAttempts = 1; in.Continuation.Attempt = 1 },
			kind:   terminaldecision.DecisionAllowStop,
			reason: protocolpolicy.ReasonBudgetExhausted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := preferredInput()
			tc.mutate(&in)
			collector := &forbiddenPreferredCollector{}
			in.Auxiliary = collector
			decision, err := preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion}).Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if decision.Kind != tc.kind || decision.ReasonCode != tc.reason {
				t.Fatalf("decision=%+v, want kind=%q reason=%q", decision, tc.kind, tc.reason)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("decision must be SDK-valid: %v", err)
			}
			if tc.wantContue {
				if decision.Continue == nil {
					t.Fatal("continue decision carries no intent")
				}
				if _, err := protocolstate.Decode(decision.Continue.ControlRef); err != nil {
					t.Fatalf("control ref is not preferred protocol state: %v", err)
				}
				if collector.calls != 0 {
					t.Fatalf("auxiliary calls=%d, want zero verifier work", collector.calls)
				}
			} else if decision.Continue != nil {
				t.Fatalf("stop decision carries an intent: %+v", decision.Continue)
			}
			if collector.calls != 0 {
				t.Fatalf("auxiliary calls=%d, want zero auxiliary work in every preferred path", collector.calls)
			}
		})
	}
}

func TestPreferredProviderSecondRepromptBudgetStops(t *testing.T) {
	t.Parallel()

	first := preferredInput()
	first.Auxiliary = &forbiddenPreferredCollector{}
	provider := preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: 2})
	decision, err := provider.Decide(context.Background(), first)
	if err != nil || decision.Kind != terminaldecision.DecisionContinue || decision.Continue == nil {
		t.Fatalf("first decision=%+v err=%v, want one bounded continuation", decision, err)
	}

	second := first
	second.Continuation.Attempt = 2
	second.Evidence.Lineage.Attempt = 2
	second.Evidence.Lineage.ProgressRef = decision.Continue.ControlRef
	second.Evidence.CandidateText = "another canonical candidate text"
	collector := &forbiddenPreferredCollector{}
	second.Auxiliary = collector
	decision, err = provider.Decide(context.Background(), second)
	if err != nil {
		t.Fatalf("second Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionContinue || decision.Continue == nil {
		t.Fatalf("second decision=%+v, want the configured second reprompt", decision)
	}
	if _, err := protocolstate.Decode(decision.Continue.ControlRef); err != nil {
		t.Fatalf("second control ref is not preferred protocol state: %v", err)
	}

	third := second
	third.Continuation.Attempt = 3
	third.Evidence.Lineage.Attempt = 3
	third.Evidence.Lineage.ProgressRef = decision.Continue.ControlRef
	decision, err = provider.Decide(context.Background(), third)
	if err != nil {
		t.Fatalf("third Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != protocolpolicy.ReasonBudgetExhausted {
		t.Fatalf("third decision=%+v, want budget exhaustion", decision)
	}
	if collector.calls != 0 {
		t.Fatalf("auxiliary calls=%d, want zero auxiliary work", collector.calls)
	}
}

func TestPreferredProviderStopsConservativelyOnContextAndMalformedInput(t *testing.T) {
	t.Parallel()

	provider := preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion})
	for name, ctx := range map[string]context.Context{
		"canceled": func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(),
		"deadline": func() context.Context {
			c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			return c
		}(),
		"nil context": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := preferredInput()
			collector := &forbiddenPreferredCollector{}
			in.Auxiliary = collector
			decision, err := provider.Decide(ctx, in)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if ctx == nil {
				if decision.Kind != terminaldecision.DecisionContinue {
					t.Fatalf("nil context decision=%+v, want a normal eligible decision", decision)
				}
			} else if decision.Kind != terminaldecision.DecisionAllowStop ||
				(decision.ReasonCode != reasonCanceled && decision.ReasonCode != reasonDeadline) {
				t.Fatalf("decision=%+v, want a bounded conservative allow-stop", decision)
			}
			if collector.calls != 0 {
				t.Fatalf("auxiliary calls=%d, want zero auxiliary work", collector.calls)
			}
		})
	}

	malformed := preferredInput()
	malformed.Evidence.ActionCount = terminaldecision.MaxEvidenceActions + 1
	decision, err := provider.Decide(context.Background(), malformed)
	if err != nil {
		t.Fatalf("malformed Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != protocolpolicy.ReasonInvalidInput {
		t.Fatalf("malformed decision=%+v, want invalid-input allow-stop", decision)
	}
}

func TestPreferredProviderStateLoadErrorDoesNotResetReference(t *testing.T) {
	t.Parallel()

	in := preferredInput()
	in.Continuation.Attempt = 2
	in.Evidence.Lineage.Attempt = 2
	in.Evidence.Lineage.ProgressRef = "unrelated-foreign-reference"
	decision, err := preferredProviderFromConfig(t, Config{Enabled: true, Strategy: StrategyAttemptCompletion}).Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != protocolpolicy.ReasonInvalidState {
		t.Fatalf("decision=%+v, want a bounded state allow-stop", decision)
	}
}

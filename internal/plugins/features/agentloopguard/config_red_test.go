package agentloopguard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_DefaultsToDisabledAndProviderBounds(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(yaml.Node{})
	if err != nil {
		t.Fatalf("DecodeConfig(empty): %v", err)
	}
	if cfg.Enabled {
		t.Fatal("empty ALG config must be disabled")
	}
	if cfg.MaxSemanticContinuations != DefaultMaxSemanticContinuations {
		t.Fatalf("max semantic continuations=%d, want %d", cfg.MaxSemanticContinuations, DefaultMaxSemanticContinuations)
	}
	if cfg.VerifierRole != DefaultVerifierRole {
		t.Fatalf("verifier role=%q, want %q", cfg.VerifierRole, DefaultVerifierRole)
	}
	if cfg.VerifierTimeoutSeconds != DefaultVerifierTimeoutSeconds {
		t.Fatalf("verifier timeout seconds=%d, want %d", cfg.VerifierTimeoutSeconds, DefaultVerifierTimeoutSeconds)
	}
	if cfg.VerifierTimeout != time.Duration(DefaultVerifierTimeoutSeconds)*time.Second {
		t.Fatalf("verifier timeout=%s, want %s", cfg.VerifierTimeout, time.Duration(DefaultVerifierTimeoutSeconds)*time.Second)
	}
	if cfg.NoProgressLimit != DefaultNoProgressLimit {
		t.Fatalf("no-progress limit=%d, want %d", cfg.NoProgressLimit, DefaultNoProgressLimit)
	}
	if cfg.ExplicitCompletionPolicy != ExplicitCompletionPolicyTrust {
		t.Fatalf("explicit completion policy=%q, want %q", cfg.ExplicitCompletionPolicy, ExplicitCompletionPolicyTrust)
	}
}

func TestDecodeConfig_EnabledProviderSettings(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(`
enabled: true
verifier_role: custom_verifier
verifier_timeout_seconds: 8
max_semantic_continuations: 5
no_progress_limit: 4
explicit_completion_policy: VERIFY
`), &node); err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(node)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if !cfg.Enabled || cfg.VerifierRole != "custom_verifier" || cfg.VerifierTimeoutSeconds != 8 || cfg.VerifierTimeout != 8*time.Second || cfg.MaxSemanticContinuations != 5 || cfg.NoProgressLimit != 4 || cfg.ExplicitCompletionPolicy != ExplicitCompletionPolicyVerify {
		t.Fatalf("decoded config=%+v", cfg)
	}
}

func TestDecodeConfig_EnabledRejectsInvalidProviderSettings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "zero semantic cap", raw: "enabled: true\nmax_semantic_continuations: 0\n", want: "max_semantic_continuations"},
		{name: "negative semantic cap", raw: "enabled: true\nmax_semantic_continuations: -1\n", want: "max_semantic_continuations"},
		{name: "empty verifier role", raw: "enabled: true\nverifier_role: '   '\n", want: "verifier_role"},
		{name: "zero verifier timeout", raw: "enabled: true\nverifier_timeout_seconds: 0\n", want: "verifier_timeout_seconds"},
		{name: "negative verifier timeout", raw: "enabled: true\nverifier_timeout_seconds: -1\n", want: "verifier_timeout_seconds"},
		{name: "zero no-progress limit", raw: "enabled: true\nno_progress_limit: 0\n", want: "no_progress_limit"},
		{name: "negative no-progress limit", raw: "enabled: true\nno_progress_limit: -1\n", want: "no_progress_limit"},
		{name: "unknown completion policy", raw: "explicit_completion_policy: always_continue\n", want: "explicit_completion_policy"},
		{name: "unknown field", raw: "unexpected: true\n", want: "unknown field"},
		{name: "timeout exceeds bound", raw: "verifier_timeout_seconds: 301\n", want: "verifier_timeout_seconds"},
		{name: "no-progress exceeds bound", raw: "no_progress_limit: 65\n", want: "no_progress_limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
				t.Fatal(err)
			}
			_, err := DecodeConfig(node)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DecodeConfig error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfiguredProviderRetainsVerifierAndProgressSettings(t *testing.T) {
	t.Parallel()

	configured := Config{
		Enabled:                  true,
		VerifierRole:             "custom_verifier",
		VerifierTimeoutSeconds:   8,
		MaxSemanticContinuations: 5,
		NoProgressLimit:          4,
		ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify,
	}
	got, ok := NewProvider(configured).(provider)
	if !ok {
		t.Fatalf("unexpected provider type %T", NewProvider(configured))
	}
	if got.cfg.VerifierRole != configured.VerifierRole || got.cfg.VerifierTimeoutSeconds != configured.VerifierTimeoutSeconds || got.cfg.VerifierTimeout != 8*time.Second || got.cfg.MaxSemanticContinuations != configured.MaxSemanticContinuations || got.cfg.NoProgressLimit != configured.NoProgressLimit || got.cfg.ExplicitCompletionPolicy != configured.ExplicitCompletionPolicy {
		t.Fatalf("provider config=%+v, want %+v", got.cfg, configured)
	}
}

func TestConfiguredProviderConsumesSemanticContinuationCap(t *testing.T) {
	t.Parallel()

	provider := NewProvider(Config{Enabled: true, MaxSemanticContinuations: 2})
	in := terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "candidate",
			OutputCommitted: true,
		},
		Request:      terminaldecision.RequestIdentity{RequestID: "request", TraceID: "trace", ALegID: "a-leg", BLegID: "b-leg"},
		Policy:       terminaldecision.PolicySnapshot{Revision: "policy"},
		Continuation: terminaldecision.ContinuationEvidence{Attempt: 2},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{{
				ItemID: "item",
				Kind:   aipapi.ItemKindMessage,
				Status: aipapi.ItemStatusInProgress,
			}},
			ActionCount: 1,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory",
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
	in.Evidence.Lineage.ProgressRef, _ = progress.EncodeState(progress.State{})
	in.Auxiliary = &providerSemanticCollector{responses: []string{`{"kind":"INCOMPLETE","objective":"resume tests"}`}}
	decision, err := provider.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != reasonBudgetExhausted {
		t.Fatalf("decision=%+v, want budget exhaustion allow-stop", decision)
	}
}

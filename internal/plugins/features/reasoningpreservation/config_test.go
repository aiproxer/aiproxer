package reasoningpreservation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
)

const validObserveYAML = `
action: observe
use_builtin_catalog: true
rules:
  - id: openrouter-kimi
    backend: openrouter-prod
    model_keywords: ["kimi", "moonshot"]
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 24h
  max_turns_per_session: 16
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`

const validRestoreYAML = `
action: restore
use_builtin_catalog: false
rules:
  - id: backend-only
    backend: anthropic-prod
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: log_skip
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 32768
`

func TestDecodeConfig_validObserve(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validObserveYAML)
	if cfg.Action != reasoningpreservation.ActionObserve {
		t.Fatalf("action=%q want %q", cfg.Action, reasoningpreservation.ActionObserve)
	}
	if !cfg.UseBuiltinCatalog {
		t.Fatal("use_builtin_catalog must be true")
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "openrouter-kimi" {
		t.Fatalf("rules=%+v", cfg.Rules)
	}
	if cfg.Rules[0].Enabled == nil || *cfg.Rules[0].Enabled != true {
		t.Fatalf("enabled=%v want true", cfg.Rules[0].Enabled)
	}
	if cfg.OnAmbiguous != reasoningpreservation.PolicyLogSkip {
		t.Fatalf("on_ambiguous=%q", cfg.OnAmbiguous)
	}
	if cfg.OnUnrepresentable != reasoningpreservation.PolicyReject {
		t.Fatalf("on_unrepresentable=%q", cfg.OnUnrepresentable)
	}
	if cfg.OnStateError != reasoningpreservation.PolicyLogSkip {
		t.Fatalf("on_state_error=%q", cfg.OnStateError)
	}
	if cfg.State.TTL != 24*time.Hour {
		t.Fatalf("ttl=%v", cfg.State.TTL)
	}
}

func TestDecodeConfig_validRestore(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validRestoreYAML)
	if cfg.Action != reasoningpreservation.ActionRestore {
		t.Fatalf("action=%q want %q", cfg.Action, reasoningpreservation.ActionRestore)
	}
	if cfg.UseBuiltinCatalog {
		t.Fatal("use_builtin_catalog must be false")
	}
	if cfg.OnUnrepresentable != reasoningpreservation.PolicyLogSkip {
		t.Fatalf("on_unrepresentable=%q", cfg.OnUnrepresentable)
	}
	if cfg.OnStateError != reasoningpreservation.PolicyReject {
		t.Fatalf("on_state_error=%q", cfg.OnStateError)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Enabled == nil || *cfg.Rules[0].Enabled != true {
		t.Fatalf("rules[0].enabled=%v want true", cfg.Rules[0].Enabled)
	}
}

func TestDecodeConfig_ruleEnabledOmittedRejected(t *testing.T) {
	t.Parallel()
	raw := `
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: r1
    backend: b1
    model_keywords: ["kimi"]
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`
	if err := decodeConfigExpectError(t, raw); err == nil {
		t.Fatal("expected rejection when rule enabled is omitted")
	}
}

func TestDecodeConfig_ruleEnabledExplicitFalse(t *testing.T) {
	t.Parallel()
	raw := `
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: r1
    backend: b1
    model_keywords: ["kimi"]
    enabled: false
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`
	cfg := decodeValidConfig(t, raw)
	if cfg.Rules[0].Enabled == nil {
		t.Fatal("enabled must be present when explicitly set")
	}
	if *cfg.Rules[0].Enabled {
		t.Fatal("enabled must decode as false")
	}
}

func TestDecodeConfig_onAmbiguousMustBeLogSkip(t *testing.T) {
	t.Parallel()
	cases := []string{
		"action: observe\non_ambiguous: reject\non_unrepresentable: reject\non_state_error: log_skip\nstate:\n  ttl: 1h\n  max_turns_per_session: 1\n  max_reasoning_bytes_per_turn: 1\n  max_session_bytes: 1\n",
		"action: observe\non_ambiguous: skip\non_unrepresentable: reject\non_state_error: log_skip\nstate:\n  ttl: 1h\n  max_turns_per_session: 1\n  max_reasoning_bytes_per_turn: 1\n  max_session_bytes: 1\n",
	}
	for _, raw := range cases {
		if err := decodeConfigExpectError(t, raw); err == nil {
			t.Fatalf("expected on_ambiguous rejection for %q", raw)
		}
	}
}

func TestDecodeConfig_onUnrepresentablePolicies(t *testing.T) {
	t.Parallel()
	base := "action: observe\non_ambiguous: log_skip\non_state_error: log_skip\nstate:\n  ttl: 1h\n  max_turns_per_session: 1\n  max_reasoning_bytes_per_turn: 1\n  max_session_bytes: 1\n"
	for _, policy := range []string{"reject", "log_skip"} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			raw := base + "on_unrepresentable: " + policy + "\n"
			cfg := decodeValidConfig(t, raw)
			if cfg.OnUnrepresentable != policy {
				t.Fatalf("on_unrepresentable=%q want %q", cfg.OnUnrepresentable, policy)
			}
		})
	}
	t.Run("reject_invalid", func(t *testing.T) {
		t.Parallel()
		if err := decodeConfigExpectError(t, base+"on_unrepresentable: continue\n"); err == nil {
			t.Fatal("expected rejection")
		}
	})
}

func TestDecodeConfig_onStateErrorPolicies(t *testing.T) {
	t.Parallel()
	base := "action: observe\non_ambiguous: log_skip\non_unrepresentable: reject\nstate:\n  ttl: 1h\n  max_turns_per_session: 1\n  max_reasoning_bytes_per_turn: 1\n  max_session_bytes: 1\n"
	for _, policy := range []string{"log_skip", "reject"} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			raw := base + "on_state_error: " + policy + "\n"
			cfg := decodeValidConfig(t, raw)
			if cfg.OnStateError != policy {
				t.Fatalf("on_state_error=%q want %q", cfg.OnStateError, policy)
			}
		})
	}
	t.Run("reject_invalid", func(t *testing.T) {
		t.Parallel()
		if err := decodeConfigExpectError(t, base+"on_state_error: ignore\n"); err == nil {
			t.Fatal("expected rejection")
		}
	})
}

func TestDecodeConfig_rejectsUnknownFields(t *testing.T) {
	t.Parallel()
	raw := validObserveYAML + "\nextra_field: true\n"
	if err := decodeConfigExpectError(t, raw); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestDecodeConfig_rejectsUnknownOrOmittedAction(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"omitted": `
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`,
		"unknown": `
action: capture
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := decodeConfigExpectError(t, raw); err == nil {
				t.Fatal("expected action validation error")
			}
		})
	}
}

func TestDecodeConfig_rejectsDuplicateRuleIDs(t *testing.T) {
	t.Parallel()
	raw := `
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: dup
    backend: b1
    model_keywords: ["kimi"]
    enabled: true
  - id: dup
    backend: b2
    model_keywords: ["moonshot"]
    enabled: false
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`
	if err := decodeConfigExpectError(t, raw); err == nil {
		t.Fatal("expected duplicate rule id rejection")
	}
}

func TestDecodeConfig_rejectsEmptyBackend(t *testing.T) {
	t.Parallel()
	raw := `
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: r1
    backend: "   "
    model_keywords: ["kimi"]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`
	if err := decodeConfigExpectError(t, raw); err == nil {
		t.Fatal("expected empty backend rejection")
	}
}

func TestDecodeConfig_rejectsEmptyKeyword(t *testing.T) {
	t.Parallel()
	cases := []string{
		`
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: r1
    backend: b1
    model_keywords: [""]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`,
		`
action: observe
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: r1
    backend: b1
    model_keywords: ["   "]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 1
  max_reasoning_bytes_per_turn: 1
  max_session_bytes: 1
`,
	}
	for i, raw := range cases {
		if err := decodeConfigExpectError(t, raw); err == nil {
			t.Fatalf("case %d: expected empty keyword rejection", i)
		}
	}
}

func TestDecodeConfig_rejectsInvalidDuration(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validObserveYAML, "ttl: 24h", "ttl: not-a-duration", 1)
	if err := decodeConfigExpectError(t, raw); err == nil {
		t.Fatal("expected invalid duration rejection")
	}
}

func TestDecodeConfig_rejectsUnsafeBounds(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ttl_zero": strings.Replace(validObserveYAML, "ttl: 24h", "ttl: 0", 1),
		"max_turns_zero": strings.Replace(validObserveYAML,
			"max_turns_per_session: 16", "max_turns_per_session: 0", 1),
		"per_turn_bytes_zero": strings.Replace(validObserveYAML,
			"max_reasoning_bytes_per_turn: 65536", "max_reasoning_bytes_per_turn: 0", 1),
		"session_bytes_zero": strings.Replace(validObserveYAML,
			"max_session_bytes: 262144", "max_session_bytes: 0", 1),
		"ttl_negative": strings.Replace(validObserveYAML, "ttl: 24h", "ttl: -1h", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := decodeConfigExpectError(t, raw); err == nil {
				t.Fatal("expected unsafe bounds rejection")
			}
		})
	}
}

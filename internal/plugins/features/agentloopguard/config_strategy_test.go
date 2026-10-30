package agentloopguard

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func decodeYAMLConfig(t *testing.T, raw string) (Config, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatalf("yaml.Unmarshal(%q): %v", raw, err)
	}
	return DecodeConfig(node)
}

func assertBoundedConfigError(t *testing.T, err error, want []string, raw string) {
	t.Helper()
	if err == nil {
		t.Fatalf("DecodeConfig(%q) succeeded, want error", raw)
	}
	msg := err.Error()
	for _, fragment := range want {
		if !strings.Contains(msg, fragment) {
			t.Fatalf("error %q does not identify %q", msg, fragment)
		}
	}
	// Validation errors must stay bounded: a fixed static prefix, the field or
	// strategy, and a static bound. No raw YAML document or configured value may
	// be echoed, and no other document line may leak in.
	if len(msg) > maxBoundedConfigErrorLen {
		t.Fatalf("validation error is unbounded (%d bytes): %q", len(msg), msg)
	}
	if strings.Contains(msg, "line ") {
		t.Fatalf("validation error leaked a YAML document position: %q", msg)
	}
}

const maxBoundedConfigErrorLen = 160

func TestDecodeConfig_OmittedSelectorStaysLegacy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty node", raw: ""},
		{name: "null document", raw: "null\n"},
		{name: "empty mapping", raw: "{}\n"},
		{name: "disabled omitted", raw: "enabled: false\n"},
		{name: "enabled omitted", raw: "enabled: true\n"},
		{name: "explicit legacy", raw: "enabled: true\nstrategy: semantic_verifier\n"},
		{name: "explicit legacy padded", raw: "enabled: true\nstrategy: '  SEMANTIC_VERIFIER  '\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeYAMLConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig(%q): %v", tc.raw, err)
			}
			if cfg.Strategy != StrategySemanticVerifier {
				t.Fatalf("strategy=%q, want %q", cfg.Strategy, StrategySemanticVerifier)
			}
			if cfg.MaxProtocolReprompts != 0 {
				t.Fatalf("legacy max protocol reprompts=%d, want 0", cfg.MaxProtocolReprompts)
			}
			if cfg.VerifierRole != DefaultVerifierRole {
				t.Fatalf("verifier role=%q, want %q", cfg.VerifierRole, DefaultVerifierRole)
			}
			if cfg.VerifierTimeoutSeconds != DefaultVerifierTimeoutSeconds {
				t.Fatalf("verifier timeout seconds=%d, want %d", cfg.VerifierTimeoutSeconds, DefaultVerifierTimeoutSeconds)
			}
			if cfg.VerifierTimeout != time.Duration(DefaultVerifierTimeoutSeconds)*time.Second {
				t.Fatalf("verifier timeout=%s, want %ds", cfg.VerifierTimeout, DefaultVerifierTimeoutSeconds)
			}
			if cfg.MaxSemanticContinuations != DefaultMaxSemanticContinuations {
				t.Fatalf("max semantic continuations=%d, want %d", cfg.MaxSemanticContinuations, DefaultMaxSemanticContinuations)
			}
			if cfg.NoProgressLimit != DefaultNoProgressLimit {
				t.Fatalf("no-progress limit=%d, want %d", cfg.NoProgressLimit, DefaultNoProgressLimit)
			}
			if cfg.ExplicitCompletionPolicy != ExplicitCompletionPolicyTrust {
				t.Fatalf("policy=%q, want %q", cfg.ExplicitCompletionPolicy, ExplicitCompletionPolicyTrust)
			}
		})
	}
}

func TestDecodeConfig_PreferredMinimalDefaults(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"enabled: true\nstrategy: attempt_completion\n",
		"enabled: false\nstrategy: attempt_completion\n",
		"enabled: true\nstrategy: ATTEMPT_COMPLETION\n",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeYAMLConfig(t, raw)
			if err != nil {
				t.Fatalf("DecodeConfig(%q): %v", raw, err)
			}
			if cfg.Strategy != StrategyAttemptCompletion {
				t.Fatalf("strategy=%q, want %q", cfg.Strategy, StrategyAttemptCompletion)
			}
			if cfg.MaxProtocolReprompts != DefaultMaxProtocolReprompts {
				t.Fatalf("max protocol reprompts=%d, want %d", cfg.MaxProtocolReprompts, DefaultMaxProtocolReprompts)
			}
			if cfg.NoProgressLimit != DefaultNoProgressLimit {
				t.Fatalf("no-progress limit=%d, want %d", cfg.NoProgressLimit, DefaultNoProgressLimit)
			}
			if cfg.VerifierRole != "" || cfg.VerifierTimeoutSeconds != 0 || cfg.VerifierTimeout != 0 {
				t.Fatalf("preferred verifier fields must stay empty, got role=%q secs=%d dur=%s", cfg.VerifierRole, cfg.VerifierTimeoutSeconds, cfg.VerifierTimeout)
			}
			if cfg.MaxSemanticContinuations != 0 {
				t.Fatalf("max semantic continuations=%d, want 0", cfg.MaxSemanticContinuations)
			}
			if cfg.ExplicitCompletionPolicy != "" {
				t.Fatalf("policy=%q, want empty", cfg.ExplicitCompletionPolicy)
			}
		})
	}
}

func TestDecodeConfig_PreferredProtocolReprompts(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name string
		raw  string
		want int
	}{
		{name: "default", raw: "enabled: true\nstrategy: attempt_completion\n", want: 1},
		{name: "explicit one", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: 1\n", want: 1},
		{name: "explicit max", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: 3\n", want: 3},
		{name: "two", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: 2\nno_progress_limit: 2\n", want: 2},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeYAMLConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig(%q): %v", tc.raw, err)
			}
			if cfg.MaxProtocolReprompts != tc.want {
				t.Fatalf("max protocol reprompts=%d, want %d", cfg.MaxProtocolReprompts, tc.want)
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "zero enabled", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: 0\n"},
		{name: "zero disabled", raw: "strategy: attempt_completion\nmax_protocol_reprompts: 0\n"},
		{name: "null", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: null\n"},
		{name: "negative", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: -1\n"},
		{name: "above max", raw: "enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: 4\n"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeYAMLConfig(t, tc.raw)
			assertBoundedConfigError(t, err, []string{"max_protocol_reprompts"}, tc.raw)
		})
	}
}

func TestDecodeConfig_RejectsInactiveStrategyFields(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"zero":       "0",
		"empty":      "''",
		"whitespace": "'   '",
		"null":       "null",
		"nonzero":    "7",
	}
	preferredKeys := []string{"verifier_role", "verifier_timeout_seconds", "max_semantic_continuations", "explicit_completion_policy"}
	for _, key := range preferredKeys {
		for name, value := range values {
			for _, enabled := range []bool{true, false} {
				raw := "enabled: " + boolLiteral(enabled) + "\nstrategy: attempt_completion\n" + key + ": " + value + "\n"
				t.Run("preferred/"+key+"/"+name+"/"+boolLiteral(enabled), func(t *testing.T) {
					t.Parallel()
					_, err := decodeYAMLConfig(t, raw)
					assertBoundedConfigError(t, err, []string{key, string(StrategyAttemptCompletion)}, raw)
				})
			}
		}
	}
	for name, value := range values {
		for _, enabled := range []bool{true, false} {
			raw := "enabled: " + boolLiteral(enabled) + "\nstrategy: semantic_verifier\nmax_protocol_reprompts: " + value + "\n"
			t.Run("legacy/max_protocol_reprompts/"+name+"/"+boolLiteral(enabled), func(t *testing.T) {
				t.Parallel()
				_, err := decodeYAMLConfig(t, raw)
				assertBoundedConfigError(t, err, []string{"max_protocol_reprompts", string(StrategySemanticVerifier)}, raw)
			})
		}
	}
}

func TestDecodeConfig_RejectsInvalidSelectorAndUnknownKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "unknown enabled", raw: "enabled: true\nstrategy: auto\n", want: []string{"strategy"}},
		{name: "unknown disabled", raw: "strategy: hybrid\n", want: []string{"strategy"}},
		{name: "blank enabled", raw: "enabled: true\nstrategy: ''\n", want: []string{"strategy"}},
		{name: "blank disabled", raw: "strategy: '   '\n", want: []string{"strategy"}},
		{name: "null selector", raw: "enabled: true\nstrategy: null\n", want: []string{"strategy"}},
		{name: "unknown key disabled", raw: "max_protocol_reprompt: 1\n", want: []string{"unknown field"}},
		{name: "unknown key enabled", raw: "enabled: true\nverifier_timeout: 5\n", want: []string{"unknown field"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeYAMLConfig(t, tc.raw)
			assertBoundedConfigError(t, err, tc.want, tc.raw)
		})
	}
}

func TestDecodeConfig_SharedNoProgressLimit(t *testing.T) {
	t.Parallel()

	valid := []struct {
		raw  string
		want int
	}{
		{raw: "enabled: true\n", want: DefaultNoProgressLimit},
		{raw: "enabled: true\nno_progress_limit: 1\n", want: 1},
		{raw: "enabled: true\nno_progress_limit: 64\n", want: 64},
		{raw: "enabled: true\nstrategy: attempt_completion\nno_progress_limit: 3\n", want: 3},
		{raw: "enabled: false\nno_progress_limit: 0\n", want: DefaultNoProgressLimit},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.raw, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeYAMLConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig(%q): %v", tc.raw, err)
			}
			if cfg.NoProgressLimit != tc.want {
				t.Fatalf("no-progress limit=%d, want %d", cfg.NoProgressLimit, tc.want)
			}
		})
	}

	invalid := []struct {
		raw  string
		want string
	}{
		{raw: "enabled: true\nno_progress_limit: 0\n", want: "no_progress_limit"},
		{raw: "enabled: true\nno_progress_limit: -1\n", want: "no_progress_limit"},
		{raw: "enabled: true\nno_progress_limit: 65\n", want: "no_progress_limit"},
		{raw: "enabled: true\nstrategy: attempt_completion\nno_progress_limit: 0\n", want: "no_progress_limit"},
		{raw: "enabled: true\nstrategy: attempt_completion\nno_progress_limit: 65\n", want: "no_progress_limit"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.raw, func(t *testing.T) {
			t.Parallel()
			_, err := decodeYAMLConfig(t, tc.raw)
			assertBoundedConfigError(t, err, []string{tc.want}, tc.raw)
		})
	}
}

// TestDecodeConfig_PreferredProtocolRepromptsRejectsNonIntegerValues pins the
// integer shape of the new bounded cap. yaml.v3 silently truncates YAML floats
// when decoding into an int, so a fractional or malformed value must be rejected
// by explicit scalar-kind validation rather than coerced into a valid bound.
func TestDecodeConfig_PreferredProtocolRepromptsRejectsNonIntegerValues(t *testing.T) {
	t.Parallel()

	// The configured payload must never reach a validation error.
	const privatePayload = "PRIVATE_CONFIG_PAYLOAD_123456789"

	for _, enabled := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "fractional below range", raw: "1.5"},
			{name: "fractional at upper bound", raw: "3.9"},
			{name: "fractional inside range", raw: "2.5"},
			{name: "malformed string", raw: "'" + privatePayload + "'"},
			{name: "boolean", raw: "true"},
			{name: "sequence", raw: "[1]"},
			{name: "mapping", raw: "{value: 1}"},
			{name: "overflowing integer", raw: "99999999999999999999"},
		} {
			raw := "enabled: " + boolLiteral(enabled) + "\nstrategy: attempt_completion\nmax_protocol_reprompts: " + tc.raw + "\n"
			t.Run(boolLiteral(enabled)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				_, err := decodeYAMLConfig(t, raw)
				assertBoundedConfigError(t, err, []string{"max_protocol_reprompts", "integer"}, raw)
				assertNoConfiguredValueLeak(t, err, privatePayload)
			})
		}
	}
}

// TestDecodeConfig_PreferredProtocolRepromptsIntegerBounds proves valid integer
// values are preserved exactly and out-of-range integers are rejected.
func TestDecodeConfig_PreferredProtocolRepromptsIntegerBounds(t *testing.T) {
	t.Parallel()

	for _, enabled := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			raw  string
			want int
		}{
			{name: "minimum", raw: "1", want: 1},
			{name: "middle", raw: "2", want: 2},
			{name: "maximum", raw: "3", want: 3},
		} {
			raw := "enabled: " + boolLiteral(enabled) + "\nstrategy: attempt_completion\nmax_protocol_reprompts: " + tc.raw + "\n"
			t.Run(boolLiteral(enabled)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				cfg, err := decodeYAMLConfig(t, raw)
				if err != nil {
					t.Fatalf("DecodeConfig(%q): %v", raw, err)
				}
				if cfg.MaxProtocolReprompts != tc.want {
					t.Fatalf("max protocol reprompts=%d, want %d", cfg.MaxProtocolReprompts, tc.want)
				}
			})
		}
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{name: "zero", raw: "0"},
			{name: "negative", raw: "-1"},
			{name: "above maximum", raw: "4"},
			{name: "null", raw: "null"},
			{name: "empty", raw: "''"},
		} {
			raw := "enabled: " + boolLiteral(enabled) + "\nstrategy: attempt_completion\nmax_protocol_reprompts: " + tc.raw + "\n"
			t.Run(boolLiteral(enabled)+"/invalid/"+tc.name, func(t *testing.T) {
				t.Parallel()
				_, err := decodeYAMLConfig(t, raw)
				assertBoundedConfigError(t, err, []string{"max_protocol_reprompts"}, raw)
			})
		}
	}
}

func assertNoConfiguredValueLeak(t *testing.T, err error, value string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error to inspect for a configured-value leak")
	}
	if strings.Contains(err.Error(), value) {
		t.Fatalf("validation error disclosed the configured value: %q", err.Error())
	}
}

func TestNormalizeProgrammatic_FillsSelectedActiveDefaults(t *testing.T) {
	t.Parallel()

	preferred, err := NormalizeProgrammatic(Config{Enabled: true, Strategy: StrategyAttemptCompletion})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(preferred): %v", err)
	}
	if preferred.MaxProtocolReprompts != DefaultMaxProtocolReprompts {
		t.Fatalf("max protocol reprompts=%d, want %d", preferred.MaxProtocolReprompts, DefaultMaxProtocolReprompts)
	}
	if preferred.NoProgressLimit != DefaultNoProgressLimit {
		t.Fatalf("no-progress limit=%d, want %d", preferred.NoProgressLimit, DefaultNoProgressLimit)
	}
	if preferred.VerifierRole != "" || preferred.VerifierTimeout != 0 || preferred.MaxSemanticContinuations != 0 || preferred.ExplicitCompletionPolicy != "" {
		t.Fatalf("preferred verifier fields must stay empty, got %+v", preferred)
	}

	legacy, err := NormalizeProgrammatic(Config{Enabled: true})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(legacy): %v", err)
	}
	if legacy.Strategy != StrategySemanticVerifier {
		t.Fatalf("strategy=%q, want %q", legacy.Strategy, StrategySemanticVerifier)
	}
	if legacy.VerifierRole != DefaultVerifierRole || legacy.VerifierTimeoutSeconds != DefaultVerifierTimeoutSeconds ||
		legacy.VerifierTimeout != time.Duration(DefaultVerifierTimeoutSeconds)*time.Second ||
		legacy.MaxSemanticContinuations != DefaultMaxSemanticContinuations || legacy.NoProgressLimit != DefaultNoProgressLimit ||
		legacy.ExplicitCompletionPolicy != ExplicitCompletionPolicyTrust {
		t.Fatalf("legacy defaults=%+v", legacy)
	}
	if legacy.MaxProtocolReprompts != 0 {
		t.Fatalf("legacy max protocol reprompts=%d, want 0", legacy.MaxProtocolReprompts)
	}
}

func TestNormalizeProgrammatic_ProtocolRepromptBounds(t *testing.T) {
	t.Parallel()

	if got, err := NormalizeProgrammatic(Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: MaxMaxProtocolReprompts}); err != nil {
		t.Fatalf("NormalizeProgrammatic(max): %v", err)
	} else if got.MaxProtocolReprompts != MaxMaxProtocolReprompts {
		t.Fatalf("max protocol reprompts=%d, want %d", got.MaxProtocolReprompts, MaxMaxProtocolReprompts)
	}
	for _, bad := range []int{-1, MaxMaxProtocolReprompts + 1} {
		cfg := Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: bad}
		if _, err := NormalizeProgrammatic(cfg); err == nil || !strings.Contains(err.Error(), "max_protocol_reprompts") {
			t.Fatalf("NormalizeProgrammatic(%d) error=%v, want max_protocol_reprompts rejection", bad, err)
		}
	}
}

func TestNormalizeProgrammatic_RejectsInactiveFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "preferred role", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierRole: "loop_guard"}, want: []string{"verifier_role", "attempt_completion"}},
		{name: "preferred role whitespace", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierRole: "   "}, want: []string{"verifier_role", "attempt_completion"}},
		{name: "preferred timeout seconds", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierTimeoutSeconds: 4}, want: []string{"verifier_timeout_seconds", "attempt_completion"}},
		{name: "preferred timeout seconds negative", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierTimeoutSeconds: -4}, want: []string{"verifier_timeout_seconds", "attempt_completion"}},
		{name: "preferred timeout duration", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierTimeout: 4 * time.Second}, want: []string{"verifier_timeout", "attempt_completion"}},
		{name: "preferred timeout duration negative", cfg: Config{Strategy: StrategyAttemptCompletion, VerifierTimeout: -time.Second}, want: []string{"verifier_timeout", "attempt_completion"}},
		{name: "preferred semantic cap", cfg: Config{Strategy: StrategyAttemptCompletion, MaxSemanticContinuations: 3}, want: []string{"max_semantic_continuations", "attempt_completion"}},
		{name: "preferred policy", cfg: Config{Strategy: StrategyAttemptCompletion, ExplicitCompletionPolicy: ExplicitCompletionPolicyTrust}, want: []string{"explicit_completion_policy", "attempt_completion"}},
		{name: "preferred policy whitespace", cfg: Config{Strategy: StrategyAttemptCompletion, ExplicitCompletionPolicy: " verify "}, want: []string{"explicit_completion_policy", "attempt_completion"}},
		{name: "preferred duration only", cfg: Config{Enabled: true, Strategy: StrategyAttemptCompletion, NoProgressLimit: 2, VerifierTimeout: time.Second}, want: []string{"verifier_timeout", "attempt_completion"}},
		{name: "legacy cap positive", cfg: Config{Enabled: true, VerifierRole: DefaultVerifierRole, MaxProtocolReprompts: 1}, want: []string{"max_protocol_reprompts", "semantic_verifier"}},
		{name: "legacy cap negative", cfg: Config{Enabled: true, VerifierRole: DefaultVerifierRole, MaxProtocolReprompts: -2}, want: []string{"max_protocol_reprompts", "semantic_verifier"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NormalizeProgrammatic(tc.cfg)
			if err == nil {
				t.Fatalf("NormalizeProgrammatic(%+v) succeeded, want mixed-field rejection", tc.cfg)
			}
			msg := err.Error()
			for _, fragment := range tc.want {
				if !strings.Contains(msg, fragment) {
					t.Fatalf("error %q does not identify %q", msg, fragment)
				}
			}
		})
	}
}

func TestNormalizeProgrammatic_ZeroMeansOmitted(t *testing.T) {
	t.Parallel()

	// A value struct cannot distinguish an explicit zero from an absent value, so
	// a zero protocol cap under the legacy strategy is omission, not a mix.
	legacy, err := NormalizeProgrammatic(Config{Enabled: true, MaxProtocolReprompts: 0})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(legacy zero cap): %v", err)
	}
	if legacy.Strategy != StrategySemanticVerifier || legacy.MaxProtocolReprompts != 0 {
		t.Fatalf("legacy config=%+v", legacy)
	}
	preferred, err := NormalizeProgrammatic(Config{Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: 0})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(preferred zero cap): %v", err)
	}
	if preferred.MaxProtocolReprompts != DefaultMaxProtocolReprompts {
		t.Fatalf("preferred cap=%d, want %d", preferred.MaxProtocolReprompts, DefaultMaxProtocolReprompts)
	}
}

func TestNormalizeProgrammatic_RejectsUnknownSelector(t *testing.T) {
	t.Parallel()

	for _, selector := range []Strategy{"auto", "hybrid", "attempt completion", " "} {
		cfg := Config{Enabled: true, Strategy: selector}
		if _, err := NormalizeProgrammatic(cfg); err == nil || !strings.Contains(err.Error(), "strategy") {
			t.Fatalf("NormalizeProgrammatic(selector=%q) error=%v, want strategy rejection", selector, err)
		}
	}
}

func TestNormalize_IsIdempotentPerStrategy(t *testing.T) {
	t.Parallel()

	seeds := map[string]Config{
		"preferred enabled":    {Enabled: true, Strategy: StrategyAttemptCompletion, MaxProtocolReprompts: 2, NoProgressLimit: 3},
		"preferred disabled":   {Strategy: StrategyAttemptCompletion},
		"legacy enabled":       {Enabled: true, VerifierRole: "custom", VerifierTimeoutSeconds: 8, MaxSemanticContinuations: 5, NoProgressLimit: 4, ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify},
		"legacy disabled zero": {},
	}
	for name, seed := range seeds {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			once, err := NormalizeProgrammatic(seed)
			if err != nil {
				t.Fatalf("NormalizeProgrammatic(%+v): %v", seed, err)
			}
			twice, err := once.Normalize()
			if err != nil {
				t.Fatalf("second Normalize(%+v): %v", once, err)
			}
			if once != twice {
				t.Fatalf("Normalize not idempotent:\nonce=%+v\ntwice=%+v", once, twice)
			}
			if err := twice.Validate(); err != nil {
				t.Fatalf("Validate(normalized): %v", err)
			}
		})
	}
}

func TestValidate_RejectsMixedProgrammaticConfig(t *testing.T) {
	t.Parallel()

	mixed := []Config{
		{Strategy: StrategyAttemptCompletion, VerifierRole: DefaultVerifierRole},
		{Strategy: StrategyAttemptCompletion, VerifierTimeout: time.Second},
		{Strategy: StrategyAttemptCompletion, MaxSemanticContinuations: 1},
		{Strategy: StrategyAttemptCompletion, ExplicitCompletionPolicy: ExplicitCompletionPolicyVerify},
		{Enabled: true, VerifierRole: DefaultVerifierRole, MaxProtocolReprompts: 1},
	}
	for _, cfg := range mixed {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded, want mixed-field rejection", cfg)
		}
	}

	normalizedPreferred, err := NormalizeProgrammatic(Config{Enabled: true, Strategy: StrategyAttemptCompletion})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(preferred): %v", err)
	}
	if err := normalizedPreferred.Validate(); err != nil {
		t.Fatalf("Validate(preferred): %v", err)
	}
	normalizedLegacy, err := NormalizeProgrammatic(Config{Enabled: true})
	if err != nil {
		t.Fatalf("NormalizeProgrammatic(legacy): %v", err)
	}
	if err := normalizedLegacy.Validate(); err != nil {
		t.Fatalf("Validate(legacy): %v", err)
	}
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

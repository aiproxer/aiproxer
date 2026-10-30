package sessionclassification_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfigDefaultsAndModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		mode sessionclassification.Mode
	}{
		{name: "empty mapping defaults to heuristic", raw: "{}\n", mode: sessionclassification.ModeHeuristic},
		{name: "null defaults to heuristic", raw: "null\n", mode: sessionclassification.ModeHeuristic},
		{name: "heuristic mode", raw: "mode: heuristic\n", mode: sessionclassification.ModeHeuristic},
		{name: "jev mode", raw: validRemoteConfig(sessionclassification.ModeJev), mode: sessionclassification.ModeJev},
		{name: "hybrid mode", raw: validRemoteConfig(sessionclassification.ModeHybrid), mode: sessionclassification.ModeHybrid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := decodeConfig(t, tc.raw)
			if cfg.Mode != tc.mode {
				t.Fatalf("mode = %q, want %q", cfg.Mode, tc.mode)
			}
			if tc.mode == sessionclassification.ModeHeuristic && cfg.Remote != nil {
				t.Fatalf("heuristic mode unexpectedly has remote config: %+v", cfg.Remote)
			}
		})
	}
}

func TestDecodeConfigRemoteRequiresExplicitBoundedValues(t *testing.T) {
	t.Parallel()

	valid := validRemoteConfig(sessionclassification.ModeJev)
	validCases := []struct {
		name string
		raw  string
	}{
		{name: "complete jev config", raw: valid},
		{name: "explicit zero retry backoff", raw: valid},
		{name: "complete hybrid config", raw: validRemoteConfig(sessionclassification.ModeHybrid)},
	}
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := decodeConfig(t, tc.raw)
			if cfg.Remote == nil {
				t.Fatal("remote config is nil")
			}
			if cfg.Remote.Provider != "jev" || cfg.Remote.APIKeyEnv != "TYPESAFE_API_KEY" {
				t.Fatalf("remote provider or credential reference not preserved: %+v", cfg.Remote)
			}
			if cfg.Remote.Timeout != 750*time.Millisecond || cfg.Remote.LeaseTTL != 2*time.Second {
				t.Fatalf("remote durations = timeout %s, lease %s", cfg.Remote.Timeout, cfg.Remote.LeaseTTL)
			}
			if cfg.Remote.MaxAttemptsPerSession != 1 || cfg.Remote.RetryBackoff != 0 {
				t.Fatalf("remote retry bounds = attempts %d, backoff %s", cfg.Remote.MaxAttemptsPerSession, cfg.Remote.RetryBackoff)
			}
			if cfg.Remote.PositiveThreshold != 0.90 {
				t.Fatalf("positive threshold = %v, want 0.90", cfg.Remote.PositiveThreshold)
			}
		})
	}
}

func TestHeuristicConfigDoesNotRequireRemoteCredentialEnvironment(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	cfg := decodeConfig(t, "{}\n")
	if cfg.Mode != sessionclassification.ModeHeuristic || cfg.Remote != nil {
		t.Fatalf("heuristic config requires remote setup: %+v", cfg)
	}
}

func TestDecodeConfigNormalizesBoundedLiteralExclusions(t *testing.T) {
	t.Parallel()

	cfg := decodeConfig(t, "heuristic:\n  ignored_user_agent_prefixes:\n    - '  CODEX_CLI_RS/  '\n    - 'Anthropic/JS'\n")
	want := []string{"codex_cli_rs/", "anthropic/js"}
	if len(cfg.Heuristic.IgnoredUserAgentPrefixes) != len(want) {
		t.Fatalf("ignored prefixes = %#v, want %#v", cfg.Heuristic.IgnoredUserAgentPrefixes, want)
	}
	for i := range want {
		if cfg.Heuristic.IgnoredUserAgentPrefixes[i] != want[i] {
			t.Fatalf("ignored prefixes = %#v, want %#v", cfg.Heuristic.IgnoredUserAgentPrefixes, want)
		}
	}
}

func TestDecodeConfigRejectsInvalidAndContradictorySettings(t *testing.T) {
	t.Parallel()

	valid := validRemoteConfig(sessionclassification.ModeJev)
	cases := []struct {
		name string
		raw  string
	}{
		{name: "unknown mode", raw: "mode: automatic\n"},
		{name: "outer enabled flag is not feature config", raw: "enabled: false\n"},
		{name: "heuristic cannot configure remote", raw: strings.Replace(valid, "mode: jev", "mode: heuristic", 1)},
		{name: "jev requires remote config", raw: "mode: jev\n"},
		{name: "hybrid requires remote config", raw: "mode: hybrid\n"},
		{name: "missing provider", raw: strings.Replace(valid, "  provider: jev\n", "", 1)},
		{name: "wrong provider", raw: strings.Replace(valid, "provider: jev", "provider: other", 1)},
		{name: "missing api key environment reference", raw: strings.Replace(valid, "  api_key_env: TYPESAFE_API_KEY\n", "", 1)},
		{name: "api key value is not an environment name", raw: strings.Replace(valid, "api_key_env: TYPESAFE_API_KEY", "api_key_env: 'Bearer private-token-value'", 1)},
		{name: "missing timeout", raw: strings.Replace(valid, "  timeout: 750ms\n", "", 1)},
		{name: "non-positive timeout", raw: strings.Replace(valid, "timeout: 750ms", "timeout: 0s", 1)},
		{name: "invalid timeout", raw: strings.Replace(valid, "timeout: 750ms", "timeout: not-a-duration", 1)},
		{name: "timeout exceeds finite maximum", raw: strings.Replace(valid, "timeout: 750ms", "timeout: 31s", 1)},
		{name: "missing attempt budget", raw: strings.Replace(valid, "  max_attempts_per_session: 1\n", "", 1)},
		{name: "zero attempt budget", raw: strings.Replace(valid, "max_attempts_per_session: 1", "max_attempts_per_session: 0", 1)},
		{name: "attempt budget exceeds maximum", raw: strings.Replace(valid, "max_attempts_per_session: 1", "max_attempts_per_session: 6", 1)},
		{name: "missing lease ttl", raw: strings.Replace(valid, "  lease_ttl: 2s\n", "", 1)},
		{name: "lease ttl lacks safety margin", raw: strings.Replace(valid, "lease_ttl: 2s", "lease_ttl: 800ms", 1)},
		{name: "lease ttl exceeds finite maximum", raw: strings.Replace(valid, "lease_ttl: 2s", "lease_ttl: 121s", 1)},
		{name: "missing retry backoff", raw: strings.Replace(valid, "  retry_backoff: 0s\n", "", 1)},
		{name: "negative retry backoff", raw: strings.Replace(valid, "retry_backoff: 0s", "retry_backoff: -1s", 1)},
		{name: "retry backoff exceeds finite maximum", raw: strings.Replace(valid, "retry_backoff: 0s", "retry_backoff: 31s", 1)},
		{name: "missing positive threshold", raw: strings.Replace(valid, "  positive_threshold: 0.90\n", "", 1)},
		{name: "zero positive threshold", raw: strings.Replace(valid, "positive_threshold: 0.90", "positive_threshold: 0", 1)},
		{name: "threshold above one", raw: strings.Replace(valid, "positive_threshold: 0.90", "positive_threshold: 1.01", 1)},
		{name: "unknown remote key", raw: strings.Replace(valid, "  provider: jev\n", "  provider: jev\n  api_key: private-token-value\n", 1)},
		{name: "unknown heuristic key", raw: "heuristic:\n  scan_transcript: true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}
			_, err := sessionclassification.DecodeConfig(node)
			if err == nil {
				t.Fatal("DecodeConfig succeeded; want invalid config error")
			}
			if strings.Contains(err.Error(), "private-token-value") {
				t.Fatalf("config error exposed a secret-like value: %v", err)
			}
		})
	}
}

func TestDecodeConfigRejectsUnboundedExclusionMatchers(t *testing.T) {
	t.Parallel()

	tooMany := strings.Builder{}
	tooMany.WriteString("heuristic:\n  ignored_user_agent_prefixes:\n")
	for i := range sessionclassification.MaxIgnoredUserAgentPrefixes + 1 {
		fmt.Fprintf(&tooMany, "    - prefix-%d\n", i)
	}
	tooLong := fmt.Sprintf("heuristic:\n  ignored_user_agent_prefixes:\n    - %q\n", strings.Repeat("x", sessionclassification.MaxIgnoredUserAgentPrefixBytes+1))
	normalizedTooLong := fmt.Sprintf("heuristic:\n  ignored_user_agent_prefixes:\n    - %q\n", strings.Repeat("\u023a", sessionclassification.MaxIgnoredUserAgentPrefixBytes/2))
	cases := []struct {
		name string
		raw  string
	}{
		{name: "too many prefixes", raw: tooMany.String()},
		{name: "prefix too long", raw: tooLong},
		{name: "prefix exceeds byte cap after case normalization", raw: normalizedTooLong},
		{name: "empty prefix", raw: "heuristic:\n  ignored_user_agent_prefixes: ['']\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}
			if _, err := sessionclassification.DecodeConfig(node); err == nil {
				t.Fatal("DecodeConfig succeeded; want bounded-input error")
			}
		})
	}
}

func decodeConfig(t *testing.T, raw string) sessionclassification.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	cfg, err := sessionclassification.DecodeConfig(node)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}

func validRemoteConfig(mode sessionclassification.Mode) string {
	return fmt.Sprintf("mode: %s\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n", mode)
}

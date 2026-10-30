package reasoningpreservation_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
)

func validCompressionBlock() string {
	return `
compression:
  enabled: true
  mode: shadow
  route: "openai-responses:compressor"
  timeout: 8s
  max_input_tokens: 12000
  max_input_bytes: 1048576
  max_output_tokens: 1500
  max_output_bytes: 262144
  max_surrogate_bytes: 131072
  min_source_bytes: 4096
  min_saved_bytes: 1024
  min_savings_ratio: 0.3
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 524288
  max_pending_total: 256
  max_surrogate_bytes_total: 16777216
  egress_policy_ref: "test-allow"
`
}

func validCompressionYAML() string {
	return validObserveYAML + validCompressionBlock()
}

func TestCompressionConfig_DisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validObserveYAML)
	if cfg.Compression.Enabled {
		t.Fatalf("expected disabled by default, got %+v", cfg.Compression)
	}
	limits := cfg.Compression.ToLimits()
	if limits.MaxPendingPerSession != 0 || limits.MaxPendingTotal != 0 || limits.MaxSurrogateBytesPerTurn != 0 || limits.MaxSurrogateBytesPerSession != 0 || limits.MaxSurrogateBytesTotal != 0 {
		t.Fatalf("disabled must yield zero limits, got %+v", limits)
	}
	if cfg.Compression.Mode != "" {
		t.Fatalf("disabled mode must be empty, got %q", cfg.Compression.Mode)
	}
}

func TestCompressionConfig_DisabledIgnoresOtherFields(t *testing.T) {
	t.Parallel()
	raw := validObserveYAML + `
compression:
  enabled: false
  route: ""
  timeout: 0s
  max_input_tokens: 0
  max_output_bytes: 0
  egress_policy_ref: ""
`
	cfg := decodeValidConfig(t, raw)
	if cfg.Compression.Enabled {
		t.Fatal("expected disabled")
	}
	limits := cfg.Compression.ToLimits()
	if limits.MaxPendingPerSession != 0 || limits.MaxSurrogateBytesTotal != 0 {
		t.Fatalf("disabled must yield zero limits despite invalid other fields, got %+v", limits)
	}
}

func TestCompressionConfig_DisabledPreservesExistingBehavior(t *testing.T) {
	t.Parallel()
	without := decodeValidConfig(t, validObserveYAML)
	withDisabled := decodeValidConfig(t, validObserveYAML+`
compression:
  enabled: false
`)
	if without.Action != withDisabled.Action || without.State.TTL != withDisabled.State.TTL || len(without.Rules) != len(withDisabled.Rules) {
		t.Fatalf("disabled compression must not alter existing config: %+v vs %+v", without, withDisabled)
	}
	if withDisabled.Compression.Enabled {
		t.Fatal("expected disabled")
	}
}

func TestCompressionConfig_EnabledDefaultsShadow(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validCompressionYAML(), "  mode: shadow\n", "", 1)
	cfg := decodeValidConfig(t, raw)
	if !cfg.Compression.Enabled {
		t.Fatal("expected enabled")
	}
	if cfg.Compression.Mode != reasoningpreservation.CompressionShadow {
		t.Fatalf("expected default shadow, got %q", cfg.Compression.Mode)
	}
}

func TestCompressionConfig_ActiveMode(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validCompressionYAML(), "mode: shadow", "mode: active", 1)
	cfg := decodeValidConfig(t, raw)
	if cfg.Compression.Mode != reasoningpreservation.CompressionActive {
		t.Fatalf("expected active, got %q", cfg.Compression.Mode)
	}
}

func TestCompressionConfig_ToLimitsMapping(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, validCompressionYAML())
	limits := cfg.Compression.ToLimits()
	if limits.MaxPendingPerSession != 8 {
		t.Fatalf("MaxPendingPerSession=%d want 8", limits.MaxPendingPerSession)
	}
	if limits.MaxPendingTotal != 256 {
		t.Fatalf("MaxPendingTotal=%d want 256", limits.MaxPendingTotal)
	}
	if limits.MaxSurrogateBytesPerTurn != 131072 {
		t.Fatalf("MaxSurrogateBytesPerTurn=%d want 131072", limits.MaxSurrogateBytesPerTurn)
	}
	if limits.MaxSurrogateBytesPerSession != 524288 {
		t.Fatalf("MaxSurrogateBytesPerSession=%d want 524288", limits.MaxSurrogateBytesPerSession)
	}
	if limits.MaxSurrogateBytesTotal != 16777216 {
		t.Fatalf("MaxSurrogateBytesTotal=%d want 16777216", limits.MaxSurrogateBytesTotal)
	}
}

func TestCompressionConfig_ValidationTable(t *testing.T) {
	t.Parallel()
	base := validCompressionBlock()
	cases := []struct {
		name string
		mut  func(string) string
	}{
		{name: "missing_route", mut: func(s string) string { return strings.Replace(s, `  route: "openai-responses:compressor"`+"\n", "", 1) }},
		{name: "blank_route", mut: func(s string) string {
			return strings.Replace(s, `  route: "openai-responses:compressor"`, `  route: "   "`, 1)
		}},
		{name: "missing_egress", mut: func(s string) string { return strings.Replace(s, `  egress_policy_ref: "test-allow"`+"\n", "", 1) }},
		{name: "blank_egress", mut: func(s string) string {
			return strings.Replace(s, `  egress_policy_ref: "test-allow"`, `  egress_policy_ref: "   "`, 1)
		}},
		{name: "timeout_zero", mut: func(s string) string { return strings.Replace(s, "  timeout: 8s", "  timeout: 0s", 1) }},
		{name: "timeout_negative", mut: func(s string) string { return strings.Replace(s, "  timeout: 8s", "  timeout: -1s", 1) }},
		{name: "timeout_over_ceiling", mut: func(s string) string { return strings.Replace(s, "  timeout: 8s", "  timeout: 60s", 1) }},
		{name: "max_input_tokens_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_input_tokens: 12000", "  max_input_tokens: 0", 1)
		}},
		{name: "max_input_tokens_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_input_tokens: 12000", "  max_input_tokens: 9999999", 1)
		}},
		{name: "max_input_bytes_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_input_bytes: 1048576", "  max_input_bytes: 0", 1)
		}},
		{name: "max_input_bytes_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_input_bytes: 1048576", "  max_input_bytes: 99999999", 1)
		}},
		{name: "max_output_tokens_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_output_tokens: 1500", "  max_output_tokens: 0", 1)
		}},
		{name: "max_output_tokens_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_output_tokens: 1500", "  max_output_tokens: 999999", 1)
		}},
		{name: "max_output_bytes_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_output_bytes: 262144", "  max_output_bytes: 0", 1)
		}},
		{name: "max_output_bytes_over_hard", mut: func(s string) string {
			return strings.Replace(s, "  max_output_bytes: 262144", "  max_output_bytes: 9999999", 1)
		}},
		{name: "max_surrogate_bytes_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes: 131072", "  max_surrogate_bytes: 0", 1)
		}},
		{name: "max_surrogate_bytes_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes: 131072", "  max_surrogate_bytes: 9999999", 1)
		}},
		{name: "min_source_bytes_zero", mut: func(s string) string {
			return strings.Replace(s, "  min_source_bytes: 4096", "  min_source_bytes: 0", 1)
		}},
		{name: "min_saved_bytes_zero", mut: func(s string) string { return strings.Replace(s, "  min_saved_bytes: 1024", "  min_saved_bytes: 0", 1) }},
		{name: "min_saved_gt_min_source", mut: func(s string) string {
			s = strings.Replace(s, "  min_source_bytes: 4096", "  min_source_bytes: 100", 1)
			return strings.Replace(s, "  min_saved_bytes: 1024", "  min_saved_bytes: 200", 1)
		}},
		{name: "ratio_zero", mut: func(s string) string {
			return strings.Replace(s, "  min_savings_ratio: 0.3", "  min_savings_ratio: 0", 1)
		}},
		{name: "ratio_one", mut: func(s string) string {
			return strings.Replace(s, "  min_savings_ratio: 0.3", "  min_savings_ratio: 1", 1)
		}},
		{name: "ratio_negative", mut: func(s string) string {
			return strings.Replace(s, "  min_savings_ratio: 0.3", "  min_savings_ratio: -0.1", 1)
		}},
		{name: "ratio_gt_one", mut: func(s string) string {
			return strings.Replace(s, "  min_savings_ratio: 0.3", "  min_savings_ratio: 1.5", 1)
		}},
		{name: "mode_invalid", mut: func(s string) string { return strings.Replace(s, "  mode: shadow", "  mode: turbo", 1) }},
		{name: "max_pending_per_session_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_pending_per_session: 8", "  max_pending_per_session: 0", 1)
		}},
		{name: "max_pending_per_session_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_pending_per_session: 8", "  max_pending_per_session: 9999", 1)
		}},
		{name: "max_surrogate_per_session_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes_per_session: 524288", "  max_surrogate_bytes_per_session: 0", 1)
		}},
		{name: "max_surrogate_per_session_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes_per_session: 524288", "  max_surrogate_bytes_per_session: 99999999", 1)
		}},
		{name: "max_pending_total_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_pending_total: 256", "  max_pending_total: 0", 1)
		}},
		{name: "max_pending_total_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_pending_total: 256", "  max_pending_total: 999999", 1)
		}},
		{name: "max_surrogate_total_zero", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes_total: 16777216", "  max_surrogate_bytes_total: 0", 1)
		}},
		{name: "max_surrogate_total_over_ceiling", mut: func(s string) string {
			return strings.Replace(s, "  max_surrogate_bytes_total: 16777216", "  max_surrogate_bytes_total: 99999999", 1)
		}},
		{name: "output_lt_surrogate_plus_overhead", mut: func(s string) string {
			s = strings.Replace(s, "  max_output_bytes: 262144", "  max_output_bytes: 1024", 1)
			return s
		}},
		{name: "pending_total_lt_per_session", mut: func(s string) string {
			s = strings.Replace(s, "  max_pending_per_session: 8", "  max_pending_per_session: 10", 1)
			s = strings.Replace(s, "  max_pending_total: 256", "  max_pending_total: 5", 1)
			return s
		}},
		{name: "surrogate_total_lt_per_session", mut: func(s string) string {
			s = strings.Replace(s, "  max_surrogate_bytes_per_session: 524288", "  max_surrogate_bytes_per_session: 10000", 1)
			s = strings.Replace(s, "  max_surrogate_bytes_total: 16777216", "  max_surrogate_bytes_total: 5000", 1)
			return s
		}},
		{name: "surrogate_total_lt_per_artifact", mut: func(s string) string {
			s = strings.Replace(s, "  max_surrogate_bytes: 131072", "  max_surrogate_bytes: 10000", 1)
			s = strings.Replace(s, "  max_surrogate_bytes_total: 16777216", "  max_surrogate_bytes_total: 5000", 1)
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := validObserveYAML + tc.mut(base)
			if err := decodeConfigExpectError(t, raw); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestCompressionConfig_UnknownFieldRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		validObserveYAML + `
compression:
  enabled: true
  route: "r"
  egress_policy_ref: "ref"
  timeout: 1s
  max_input_tokens: 1
  max_input_bytes: 1
  max_output_tokens: 1
  max_output_bytes: 2048
  max_surrogate_bytes: 512
  min_source_bytes: 10
  min_saved_bytes: 1
  min_savings_ratio: 0.5
  max_pending_per_session: 1
  max_surrogate_bytes_per_session: 512
  max_pending_total: 1
  max_surrogate_bytes_total: 512
  unknown_field: true
`,
		validObserveYAML + `
compression:
  enabled: false
  unknown_field: true
`,
	}
	for i, raw := range cases {
		if err := decodeConfigExpectError(t, raw); err == nil {
			t.Fatalf("case %d: expected unknown field rejection", i)
		}
	}
}

func TestCompressionConfig_HardCeilings(t *testing.T) {
	t.Parallel()
	if reasoningpreservation.HardCompressionMaxOutputBytes != reasoningpreservation.HardRawOutputCeiling {
		t.Fatalf("HardCompressionMaxOutputBytes %d must equal HardRawOutputCeiling %d", reasoningpreservation.HardCompressionMaxOutputBytes, reasoningpreservation.HardRawOutputCeiling)
	}
	if reasoningpreservation.CompressionSurrogateOverhead <= 0 {
		t.Fatal("CompressionSurrogateOverhead must be >0")
	}
	if reasoningpreservation.HardCompressionTimeout <= 0 {
		t.Fatal("HardCompressionTimeout must be >0")
	}
}

func TestCompressionConfig_TimeoutBoundary(t *testing.T) {
	t.Parallel()
	rawAtCeiling := strings.Replace(validCompressionYAML(), "  timeout: 8s", "  timeout: 30s", 1)
	cfg := decodeValidConfig(t, rawAtCeiling)
	if cfg.Compression.Timeout != 30*time.Second {
		t.Fatalf("timeout at ceiling=%v", cfg.Compression.Timeout)
	}
	rawOver := strings.Replace(validCompressionYAML(), "  timeout: 8s", "  timeout: 31s", 1)
	if err := decodeConfigExpectError(t, rawOver); err == nil {
		t.Fatal("expected timeout over ceiling rejection")
	}
}

func TestCompressionConfig_RatioNaNInf(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		cc := reasoningpreservation.CompressionConfig{
			Enabled: true, Mode: reasoningpreservation.CompressionShadow, Route: "r", EgressPolicyRef: "ref", Timeout: time.Second,
			MaxInputTokens: 1, MaxInputBytes: 1, MaxOutputTokens: 1, MaxOutputBytes: 2048, MaxSurrogateBytes: 512,
			MinSourceBytes: 10, MinSavedBytes: 1, MinSavingsRatio: v,
			MaxPendingPerSession: 1, MaxSurrogateBytesPerSession: 512, MaxPendingTotal: 1, MaxSurrogateBytesTotal: 512,
		}
		if err := cc.Validate(); err == nil {
			t.Fatalf("expected rejection for ratio %v", v)
		}
	}
}

func TestCompressionConfig_RouteTrim(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(validCompressionYAML(), `route: "openai-responses:compressor"`, `route: "  openai-responses:compressor  "`, 1)
	cfg := decodeValidConfig(t, raw)
	if cfg.Compression.Route != "openai-responses:compressor" {
		t.Fatalf("route trim failed %q", cfg.Compression.Route)
	}
}

func FuzzCompressionConfig(f *testing.F) {
	f.Add([]byte(validCompressionYAML()))
	f.Add([]byte(validObserveYAML))
	f.Add([]byte(validObserveYAML + `
compression:
  enabled: true
  route: "r"
  egress_policy_ref: "ref"
  timeout: 1s
  max_input_tokens: 1
  max_input_bytes: 1
  max_output_tokens: 1
  max_output_bytes: 2048
  max_surrogate_bytes: 512
  min_source_bytes: 10
  min_saved_bytes: 1
  min_savings_ratio: 0.5
  max_pending_per_session: 1
  max_surrogate_bytes_per_session: 512
  max_pending_total: 1
  max_surrogate_bytes_total: 512
`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			return
		}
		var n yaml.Node
		if err := yaml.Unmarshal(raw, &n); err != nil {
			return
		}
		cfg, err := reasoningpreservation.DecodeConfig(n)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "\x00") {
				t.Fatalf("error must not contain NUL: %q", msg)
			}
			return
		}
		if cfg.Compression.Enabled {
			if strings.TrimSpace(cfg.Compression.Route) == "" {
				t.Fatalf("enabled compression must have non-blank route, got %q", cfg.Compression.Route)
			}
			if strings.TrimSpace(cfg.Compression.EgressPolicyRef) == "" {
				t.Fatalf("enabled compression must have non-blank egress ref")
			}
			if cfg.Compression.Mode != reasoningpreservation.CompressionShadow && cfg.Compression.Mode != reasoningpreservation.CompressionActive {
				t.Fatalf("invalid mode %q", cfg.Compression.Mode)
			}
			if cfg.Compression.MinSavingsRatio <= 0 || cfg.Compression.MinSavingsRatio >= 1 {
				t.Fatalf("ratio out of bounds %v", cfg.Compression.MinSavingsRatio)
			}
			if cfg.Compression.MaxOutputBytes < cfg.Compression.MaxSurrogateBytes+reasoningpreservation.CompressionSurrogateOverhead {
				t.Fatalf("output bytes %d < surrogate %d + overhead", cfg.Compression.MaxOutputBytes, cfg.Compression.MaxSurrogateBytes)
			}
			if cfg.Compression.MaxPendingTotal < cfg.Compression.MaxPendingPerSession {
				t.Fatalf("pending total %d < per session %d", cfg.Compression.MaxPendingTotal, cfg.Compression.MaxPendingPerSession)
			}
			if cfg.Compression.MaxSurrogateBytesTotal < cfg.Compression.MaxSurrogateBytesPerSession {
				t.Fatalf("surrogate total %d < per session %d", cfg.Compression.MaxSurrogateBytesTotal, cfg.Compression.MaxSurrogateBytesPerSession)
			}
			if cfg.Compression.MaxSurrogateBytesTotal < cfg.Compression.MaxSurrogateBytes {
				t.Fatalf("surrogate total %d < per artifact %d", cfg.Compression.MaxSurrogateBytesTotal, cfg.Compression.MaxSurrogateBytes)
			}
			if cfg.Compression.MinSavedBytes > cfg.Compression.MinSourceBytes {
				t.Fatalf("min_saved %d > min_source %d", cfg.Compression.MinSavedBytes, cfg.Compression.MinSourceBytes)
			}
			limits := cfg.Compression.ToLimits()
			if limits.MaxPendingPerSession != cfg.Compression.MaxPendingPerSession || limits.MaxSurrogateBytesTotal != cfg.Compression.MaxSurrogateBytesTotal {
				t.Fatalf("ToLimits mismatch")
			}
		} else {
			limits := cfg.Compression.ToLimits()
			if limits.MaxPendingPerSession != 0 || limits.MaxPendingTotal != 0 || limits.MaxSurrogateBytesTotal != 0 {
				t.Fatalf("disabled must yield zero limits")
			}
		}
	})
}

package config

import (
	"strings"
	"testing"
	"time"
)

func TestUpstreamCredentialsDefaults(t *testing.T) {
	t.Parallel()
	var c UpstreamCredentialsConfig
	if got, err := c.EffectiveMaxEnvIndex(); err != nil || got != DefaultUpstreamMaxEnvIndex {
		t.Fatalf("default max_env_index = %d, %v; want %d, nil", got, err, DefaultUpstreamMaxEnvIndex)
	}
	if DefaultUpstreamMaxEnvIndex != 20 {
		t.Fatalf("DefaultUpstreamMaxEnvIndex = %d, want 20", DefaultUpstreamMaxEnvIndex)
	}
	if got, err := c.EffectiveQuotaSwitchRetryAfter(); err != nil || got != time.Hour {
		t.Fatalf("default quota_switch_retry_after = %v, %v; want 1h, nil", got, err)
	}
}

func TestUpstreamCredentialsMaxEnvIndexBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		max  int
		want int
		fail bool
	}{
		{name: "omitted selects default", max: 0, want: 20},
		{name: "lower bound", max: 1, want: 1},
		{name: "upper bound", max: 256, want: 256},
		{name: "negative rejected", max: -1, fail: true},
		{name: "above upper bound rejected", max: 257, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := UpstreamCredentialsConfig{MaxEnvIndex: tc.max}
			got, err := c.EffectiveMaxEnvIndex()
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("EffectiveMaxEnvIndex: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestUpstreamCredentialsRetryAfterParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
		fail bool
	}{
		{name: "omitted selects hour", raw: "", want: time.Hour},
		{name: "explicit hour", raw: "1h", want: time.Hour},
		{name: "short threshold allowed", raw: "30s", want: 30 * time.Second},
		{name: "whole milliseconds allowed", raw: "2ms", want: 2 * time.Millisecond},
		{name: "millisecond floor allowed", raw: "1ms", want: time.Millisecond},
		{name: "sub-millisecond rejected", raw: "500us", fail: true},
		{name: "fractional millisecond rejected", raw: "1500us", fail: true},
		{name: "zero rejected", raw: "0s", fail: true},
		{name: "negative rejected", raw: "-1h", fail: true},
		{name: "malformed rejected", raw: "soon", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := UpstreamCredentialsConfig{QuotaSwitchRetryAfter: tc.raw}
			got, err := c.EffectiveQuotaSwitchRetryAfter()
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("EffectiveQuotaSwitchRetryAfter: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestValidateCredentialEnvRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		root string
		fail bool
	}{
		{name: "canonical root", root: "OPENAI_API_KEY"},
		{name: "digits allowed inside", root: "PROVIDER123_API_KEY"},
		{name: "empty rejected", root: "", fail: true},
		{name: "lowercase rejected", root: "openai_api_key", fail: true},
		{name: "numbered suffix rejected", root: "OPENAI_API_KEY_2", fail: true},
		{name: "bare _1 rejected", root: "OPENAI_API_KEY_1", fail: true},
		{name: "spaces rejected", root: "OPENAI API KEY", fail: true},
		{name: "interpolation rejected", root: "${OPENAI_API_KEY}", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCredentialEnvRoot(tc.root)
			if tc.fail && err == nil {
				t.Fatal("expected error")
			}
			if !tc.fail && err != nil {
				t.Fatalf("ValidateCredentialEnvRoot: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), tc.root) == false {
				t.Fatalf("error %q must name the rejected root", err.Error())
			}
		})
	}
}

func TestValidateRejectsUpstreamCredentialsPolicy(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	applyLoadDefaults(cfg)
	cfg.UpstreamCredentials.MaxEnvIndex = 999
	if err := Validate(cfg); err == nil {
		t.Fatal("expected max_env_index 999 rejection")
	} else if !strings.Contains(err.Error(), "upstream_credentials.max_env_index") {
		t.Fatalf("error %q must name upstream_credentials.max_env_index", err.Error())
	}
	cfg.UpstreamCredentials.MaxEnvIndex = 20
	cfg.UpstreamCredentials.QuotaSwitchRetryAfter = "bogus"
	if err := Validate(cfg); err == nil {
		t.Fatal("expected quota_switch_retry_after rejection")
	} else if !strings.Contains(err.Error(), "upstream_credentials.quota_switch_retry_after") {
		t.Fatalf("error %q must name upstream_credentials.quota_switch_retry_after", err.Error())
	}
}

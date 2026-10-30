package secretguard

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_requiresAction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
	}{
		{name: "empty_mapping", yaml: "{}"},
		{name: "action_empty", yaml: "action: \"\""},
		{name: "null_doc", yaml: "null"},
		{name: "empty", yaml: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeConfig(mustYAML(t, tc.yaml))
			if err == nil {
				t.Fatal("expected error for missing/empty action")
			}
			if !strings.Contains(err.Error(), "action") {
				t.Fatalf("error should mention action: %v", err)
			}
			assertNoSyntheticSecrets(t, err.Error())
		})
	}
}

func TestDecodeConfig_unknownAction(t *testing.T) {
	t.Parallel()
	_, err := DecodeConfig(mustYAML(t, "action: drop"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Fatalf("error should mention action: %v", err)
	}
}

func TestDecodeConfig_validActions(t *testing.T) {
	t.Parallel()
	for _, action := range []string{ActionBlock, ActionRedact, ActionLog} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeConfig(mustYAML(t, "action: "+action))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Action != action {
				t.Fatalf("action: got %q want %q", cfg.Action, action)
			}
			if cfg.ScanMaxBytes != DefaultScanMaxBytes {
				t.Fatalf("scan_max_bytes default: got %d want %d", cfg.ScanMaxBytes, DefaultScanMaxBytes)
			}
			if cfg.MinSecretBytes != DefaultMinSecretBytes {
				t.Fatalf("min_secret_bytes default: got %d want %d", cfg.MinSecretBytes, DefaultMinSecretBytes)
			}
			if cfg.AuditFailurePolicy != AuditFailClosed {
				t.Fatalf("audit_failure_policy default: got %q", cfg.AuditFailurePolicy)
			}
			if cfg.Redaction.MaskByte != DefaultMaskByte {
				t.Fatalf("mask_byte default: got %q want %q", cfg.Redaction.MaskByte, DefaultMaskByte)
			}
			if !cfg.Redaction.PreserveKnownPrefixes {
				t.Fatal("preserve_known_prefixes default want true")
			}
			if !cfg.SingleUser.IncludePopularEnv {
				t.Fatal("include_popular_env default want true")
			}
		})
	}
}

func TestDecodeConfig_maskByteValidation(t *testing.T) {
	t.Parallel()
	t.Run("ok_single_ascii", func(t *testing.T) {
		t.Parallel()
		cfg, err := DecodeConfig(mustYAML(t, "action: block\nredaction:\n  mask_byte: \"X\""))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Redaction.MaskByte != "X" {
			t.Fatalf("mask_byte: got %q", cfg.Redaction.MaskByte)
		}
	})
	t.Run("reject_multi", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeConfig(mustYAML(t, "action: block\nredaction:\n  mask_byte: \"**\""))
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("reject_non_ascii", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeConfig(mustYAML(t, "action: block\nredaction:\n  mask_byte: \"é\""))
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDecodeConfig_fullShape(t *testing.T) {
	t.Parallel()
	raw := `
action: redact
order: 10
audit_failure_policy: best_effort
min_secret_bytes: 12
scan_max_bytes: 1024
single_user:
  include_popular_env: false
  include_env: [FOO_KEY]
  exclude_env: [BAR_KEY]
redaction:
  mask_byte: "#"
  preserve_known_prefixes: false
`
	cfg, err := DecodeConfig(mustYAML(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Action != ActionRedact {
		t.Fatalf("action: %q", cfg.Action)
	}
	if cfg.Order == nil || *cfg.Order != 10 {
		t.Fatalf("order: %#v", cfg.Order)
	}
	if cfg.AuditFailurePolicy != AuditBestEffort {
		t.Fatalf("audit_failure_policy: %q", cfg.AuditFailurePolicy)
	}
	if cfg.MinSecretBytes != 12 || cfg.ScanMaxBytes != 1024 {
		t.Fatalf("bytes: min=%d scan=%d", cfg.MinSecretBytes, cfg.ScanMaxBytes)
	}
	if cfg.SingleUser.IncludePopularEnv {
		t.Fatal("include_popular_env want false")
	}
	if len(cfg.SingleUser.IncludeEnv) != 1 || cfg.SingleUser.IncludeEnv[0] != "FOO_KEY" {
		t.Fatalf("include_env: %#v", cfg.SingleUser.IncludeEnv)
	}
	if len(cfg.SingleUser.ExcludeEnv) != 1 || cfg.SingleUser.ExcludeEnv[0] != "BAR_KEY" {
		t.Fatalf("exclude_env: %#v", cfg.SingleUser.ExcludeEnv)
	}
	if cfg.Redaction.MaskByte != "#" || cfg.Redaction.PreserveKnownPrefixes {
		t.Fatalf("redaction: %#v", cfg.Redaction)
	}
}

func TestDecodeConfig_unknownAuditPolicy(t *testing.T) {
	t.Parallel()
	_, err := DecodeConfig(mustYAML(t, "action: log\naudit_failure_policy: explode"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeConfig_rejectsUnsafeScanMaxBytes(t *testing.T) {
	t.Parallel()
	_, err := DecodeConfig(mustYAML(t, fmt.Sprintf("action: block\nscan_max_bytes: %d", MaxScanMaxBytes+1)))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "scan_max_bytes") {
		t.Fatalf("error: %v", err)
	}
}

func TestHasSingleUserKey(t *testing.T) {
	t.Parallel()
	t.Run("true_when_present", func(t *testing.T) {
		t.Parallel()
		n := mustYAML(t, "action: block\nsingle_user:\n  include_popular_env: true")
		if !HasSingleUserKey(n) {
			t.Fatal("expected HasSingleUserKey true")
		}
	})
	t.Run("false_when_absent", func(t *testing.T) {
		t.Parallel()
		n := mustYAML(t, "action: block")
		if HasSingleUserKey(n) {
			t.Fatal("expected HasSingleUserKey false")
		}
	})
	t.Run("false_for_null", func(t *testing.T) {
		t.Parallel()
		n := mustYAML(t, "null")
		if HasSingleUserKey(n) {
			t.Fatal("expected HasSingleUserKey false for null")
		}
	})
}

func TestValidateAccessMode_rejectsSingleUserInMultiUser(t *testing.T) {
	t.Parallel()
	raw := mustYAML(t, "action: block\nsingle_user:\n  include_env: [FOO]")
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateAccessMode(cfg, true, raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "single_user is invalid in multi_user mode") {
		t.Fatalf("error: %v", err)
	}
	if err := ValidateAccessMode(cfg, false, raw); err != nil {
		t.Fatalf("single_user mode should accept: %v", err)
	}
}

func mustYAML(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

package config

import (
	"fmt"
	"strings"
	"time"
)

func validateAccounting(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	a := &cfg.Accounting
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = "provider_first"
		a.Mode = mode
	}
	switch mode {
	case "provider_first", "local_only", "provider_required", "advisory":
	default:
		return fmt.Errorf("accounting.mode: want provider_first, local_only, provider_required, or advisory, got %q", a.Mode)
	}
	if raw := strings.TrimSpace(a.CountTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("accounting.count_timeout: invalid duration %q", raw)
		}
		if d <= 0 {
			return fmt.Errorf("accounting.count_timeout: duration must be positive")
		}
	}
	if err := validateAccountingTokenizer(a); err != nil {
		return err
	}
	if mode == "provider_required" && hasLocalTokenizerFallback(a.Tokenizer) {
		return fmt.Errorf("accounting.mode: provider_required cannot configure local tokenizer fallback")
	}
	if err := validateAccountingPreflight(a); err != nil {
		return err
	}
	if err := validateAccountingLedger(a); err != nil {
		return err
	}
	if err := validateAccountingAdmin(a); err != nil {
		return err
	}
	if err := validateAccountingAuthority(cfg); err != nil {
		return err
	}
	if err := validateAccountingConcurrency(cfg); err != nil {
		return err
	}
	if err := validateAccountingBilling(a); err != nil {
		return err
	}
	// accounting.pricing is Enterprise configuration: the capability gate
	// rejects it without an Enterprise build, and the Enterprise composition
	// validates every price when it builds its catalog.
	return nil
}

func validateAccountingTokenizer(a *AccountingConfig) error {
	if strings.Contains(a.Tokenizer.DefaultEncoding, "\x00") {
		return fmt.Errorf("accounting.tokenizer.default_encoding: must not contain NUL")
	}
	for model, encoding := range a.Tokenizer.ModelMappings {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("accounting.tokenizer.model_mappings: model key must be non-empty")
		}
		if strings.TrimSpace(encoding) == "" {
			return fmt.Errorf("accounting.tokenizer.model_mappings[%q]: encoding must be non-empty", model)
		}
		if strings.Contains(model, "\x00") || strings.Contains(encoding, "\x00") {
			return fmt.Errorf("accounting.tokenizer.model_mappings[%q]: must not contain NUL", model)
		}
	}
	return nil
}

func hasLocalTokenizerFallback(t AccountingTokenizerConfig) bool {
	return strings.TrimSpace(t.DefaultEncoding) != "" || len(t.ModelMappings) > 0
}

func validateAccountingPreflight(a *AccountingConfig) error {
	mode := strings.ToLower(strings.TrimSpace(a.Preflight.Mode))
	if mode != "" {
		switch mode {
		case "required", "advisory":
		default:
			return fmt.Errorf("accounting.preflight.mode: want required or advisory, got %q", a.Preflight.Mode)
		}
	}
	for _, chk := range []struct {
		name  string
		value int64
	}{
		{"max_input_tokens", a.Preflight.MaxInputTokens},
		{"max_output_tokens", a.Preflight.MaxOutputTokens},
		{"max_context_tokens", a.Preflight.MaxContextTokens},
	} {
		if chk.value < 0 {
			return fmt.Errorf("accounting.preflight.%s: must be >= 0", chk.name)
		}
	}
	policy := strings.ToLower(strings.TrimSpace(a.Preflight.UnknownOutputPolicy))
	if policy != "" {
		switch policy {
		case "require_client_limit", "configured_default", "model_backend_maximum", "clamp", "deny":
		default:
			return fmt.Errorf("accounting.preflight.unknown_output_policy: want require_client_limit, configured_default, model_backend_maximum, clamp, or deny, got %q", a.Preflight.UnknownOutputPolicy)
		}
	}
	return nil
}

func validateAccountingLedger(a *AccountingConfig) error {
	// Leftover accounting.ledger.* YAML is accepted for compatibility. Composition
	// never opens this store, so sqlite/postgres paths are not required and defaults
	// are not written as if the ledger were live.
	store := strings.ToLower(strings.TrimSpace(a.Ledger.Store))
	if store != "" {
		switch store {
		case "memory", "sqlite", "postgres":
		default:
			return fmt.Errorf("accounting.ledger.store: want memory, sqlite, or postgres, got %q", a.Ledger.Store)
		}
	}
	policy := strings.ToLower(strings.TrimSpace(a.Ledger.WritePolicy))
	if policy != "" && policy != "required" && policy != "best_effort" {
		return fmt.Errorf("accounting.ledger.write_policy: want required or best_effort, got %q", a.Ledger.WritePolicy)
	}
	return nil
}

func validateAccountingAdmin(a *AccountingConfig) error {
	if !a.Admin.Enabled {
		return nil
	}
	path := strings.TrimSpace(a.Admin.Path)
	if path == "" {
		return fmt.Errorf("accounting.admin.path: required when admin is enabled")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("accounting.admin.path: must start with /")
	}
	if a.Admin.MaxBodyBytes < 0 {
		return fmt.Errorf("accounting.admin.max_body_bytes: must be >= 0")
	}
	return nil
}

func validateAccountingBilling(a *AccountingConfig) error {
	if a == nil {
		return nil
	}
	return nil
}

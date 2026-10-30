package identity

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Validate applies defaults, normalizes modes/values in place, and checks identity
// policy rules (modes, custom values, control characters, bounds, OpenRouter URL shape).
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("identity: nil config")
	}
	ApplyDefaults(cfg)
	if err := normalizeValidateField("identity.upstream.user_agent", &cfg.Upstream.UserAgent, MaxUserAgentBytes, false, true); err != nil {
		return err
	}
	if err := normalizeValidateField("identity.upstream.openrouter.app_url", &cfg.Upstream.OpenRouter.AppURL, MaxAppURLBytes, true, true); err != nil {
		return err
	}
	if err := normalizeValidateField("identity.upstream.openrouter.app_title", &cfg.Upstream.OpenRouter.AppTitle, MaxAppTitleBytes, false, true); err != nil {
		return err
	}
	if err := normalizeValidateField("identity.downstream.server", &cfg.Downstream.Server, MaxServerBytes, false, false); err != nil {
		return err
	}
	return nil
}

// ValidateBackendOverride validates every non-nil override field (including URL
// rules) and persists normalized mode/value into those fields. Nil override is OK.
func ValidateBackendOverride(ov *BackendOverride) error {
	if ov == nil {
		return nil
	}
	if ov.UserAgent != nil {
		if err := normalizeValidateField("identity.backend.user_agent", ov.UserAgent, MaxUserAgentBytes, false, true); err != nil {
			return err
		}
	}
	if ov.OpenRouter == nil {
		return nil
	}
	if ov.OpenRouter.AppURL != nil {
		if err := normalizeValidateField("identity.backend.openrouter.app_url", ov.OpenRouter.AppURL, MaxAppURLBytes, true, true); err != nil {
			return err
		}
	}
	if ov.OpenRouter.AppTitle != nil {
		if err := normalizeValidateField("identity.backend.openrouter.app_title", ov.OpenRouter.AppTitle, MaxAppTitleBytes, false, true); err != nil {
			return err
		}
	}
	return nil
}

// AcceptClientUserAgent returns a trimmed User-Agent suitable for canonical
// invocation metadata. ok is false when blank, over MaxUserAgentBytes, or when
// the value contains CR/LF/NUL/control characters.
func AcceptClientUserAgent(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > MaxUserAgentBytes || hasControlChars(v) {
		return "", false
	}
	return v, true
}

// AcceptClientAppURL returns a trimmed absolute http(s) URL suitable for
// OpenRouter HTTP-Referer passthrough. ok is false when blank, over
// MaxAppURLBytes, control-laden, or not a valid absolute http(s) URL without
// userinfo or fragment.
func AcceptClientAppURL(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > MaxAppURLBytes || hasControlChars(v) {
		return "", false
	}
	if err := validateAbsoluteHTTPURL("app_url", v); err != nil {
		return "", false
	}
	return v, true
}

// AcceptClientAppTitle returns a trimmed app title suitable for OpenRouter
// title passthrough. ok is false when blank, over MaxAppTitleBytes, or when
// the value contains control characters.
func AcceptClientAppTitle(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > MaxAppTitleBytes || hasControlChars(v) {
		return "", false
	}
	return v, true
}

func normalizeValidateField(path string, f *FieldPolicy, maxBytes int, isURL bool, allowPassthrough bool) error {
	mode := Mode(strings.ToLower(strings.TrimSpace(string(f.Mode))))
	switch mode {
	case ModeProxy, ModeCustom, ModeDrop:
	case ModePassthrough:
		if !allowPassthrough {
			return fmt.Errorf("%s.mode: passthrough is not allowed", path)
		}
	default:
		if allowPassthrough {
			return fmt.Errorf("%s.mode: want proxy, passthrough, custom, or drop, got %q", path, f.Mode)
		}
		return fmt.Errorf("%s.mode: want proxy, custom, or drop, got %q", path, f.Mode)
	}

	value := strings.TrimSpace(f.Value)
	switch mode {
	case ModeCustom:
		if value == "" {
			return fmt.Errorf("%s.value: required when mode is custom", path)
		}
		if err := rejectControlChars(path+".value", value); err != nil {
			return err
		}
		if len(value) > maxBytes {
			return fmt.Errorf("%s.value: exceeds %d bytes", path, maxBytes)
		}
		if isURL {
			if err := validateAbsoluteHTTPURL(path+".value", value); err != nil {
				return err
			}
		}
		f.Mode = mode
		f.Value = value
	default:
		if value != "" {
			return fmt.Errorf("%s.value: must be empty when mode is %s", path, mode)
		}
		f.Mode = mode
		f.Value = ""
	}
	return nil
}

func rejectControlChars(path, value string) error {
	if hasControlChars(value) {
		return fmt.Errorf("%s: contains disallowed control characters", path)
	}
	return nil
}

func hasControlChars(value string) bool {
	for _, r := range value {
		if r == '\r' || r == '\n' || r == 0 || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateAbsoluteHTTPURL(path, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL: %w", path, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: want absolute http(s) URL", path)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: want absolute http(s) URL with host", path)
	}
	if u.User != nil {
		return fmt.Errorf("%s: userinfo is not allowed", path)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s: fragment is not allowed", path)
	}
	return nil
}

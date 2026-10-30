package anthropicmessages

import (
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func validateCacheEnrollment(mode, ttl string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" || mode == "disabled" {
		if strings.TrimSpace(ttl) != "" {
			return fmt.Errorf("anthropic: cache ttl requires automatic enrollment")
		}
		return nil
	}
	if mode != "automatic" {
		return fmt.Errorf("anthropic: unsupported cache enrollment %q", mode)
	}
	if ttl != "5m" && ttl != "1h" {
		return fmt.Errorf("anthropic: cache ttl must be 5m or 1h")
	}
	return nil
}

func cacheEnrollmentOptions(cfg Config) []option.RequestOption {
	if strings.TrimSpace(cfg.CacheEnrollment) != "automatic" {
		return nil
	}
	// Kept as an explicit request option so enrollment is visible in provider
	// request-shape tests and cannot be toggled by the generic keep-warm switch.
	return []option.RequestOption{option.WithJSONSet("cache_control", map[string]string{
		"type": "ephemeral",
		"ttl":  cfg.CacheTTL,
	})}
}

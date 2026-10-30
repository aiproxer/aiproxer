package aipapi

import (
	"fmt"
	"strings"
)

// VerbosityLevel controls the amount of reasoning/detail requested from a
// provider that exposes the OpenAI-compatible verbosity contract.
type VerbosityLevel string

const (
	VerbosityLow    VerbosityLevel = "low"
	VerbosityMedium VerbosityLevel = "medium"
	VerbosityHigh   VerbosityLevel = "high"
)

// ParseVerbosityLevel trims and normalizes an external verbosity value. An
// empty value means unset and is returned without error.
func ParseVerbosityLevel(raw string) (VerbosityLevel, error) {
	normalized := VerbosityLevel(strings.ToLower(strings.TrimSpace(raw)))
	if normalized == "" {
		return "", nil
	}
	switch normalized {
	case VerbosityLow, VerbosityMedium, VerbosityHigh:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid verbosity %q: expected low, medium, or high", raw)
	}
}

func (v VerbosityLevel) valid() bool {
	switch v {
	case "", VerbosityLow, VerbosityMedium, VerbosityHigh:
		return true
	default:
		return false
	}
}

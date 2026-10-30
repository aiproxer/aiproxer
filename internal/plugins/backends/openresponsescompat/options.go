package openresponsescompat

import (
	"fmt"
	"strings"
	"unicode"
)

// CodecOptions is the narrow, explicit, immutable codec customization seam for
// future provider connectors that reuse the shared OpenResponses wire codec
// (Requirement 9.12). It carries only bounded provenance identity and the pinned
// profile; provider-specific attribution, routing, billing, catalog, and
// proprietary controls are never representable here and stay in the connector
// (Requirement 4.11).
//
// CodecOptions is immutable and safe for concurrent use: every field is
// validated at construction and read-only afterwards. It exposes no callbacks,
// no arbitrary option maps, and no provider-policy fields.
type CodecOptions struct {
	profile     string
	factoryKind string
	providerID  string
}

// DefaultCodecOptions returns the generic-mode codec options: the pinned
// OpenResponses profile and the generic factory kind, with no provider label.
func DefaultCodecOptions() CodecOptions {
	return CodecOptions{
		profile:     DefaultProfile,
		factoryKind: ID,
	}
}

// NewCodecOptions validates and returns the explicit codec options a future
// provider connector supplies. profile must be the pinned DefaultProfile (or
// empty to select it); factoryKind defaults to the generic [ID]; providerID is
// an optional bounded provenance label and must not carry provider policy,
// whitespace, control characters, or slashes.
func NewCodecOptions(profile, factoryKind, providerID string) (CodecOptions, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultProfile
	}
	if profile != DefaultProfile {
		return CodecOptions{}, fmt.Errorf("%s: unsupported profile %q (supported: %q)", ID, profile, DefaultProfile)
	}
	factoryKind = strings.TrimSpace(factoryKind)
	if factoryKind == "" {
		factoryKind = ID
	}
	if err := validateProviderID(providerID); err != nil {
		return CodecOptions{}, err
	}
	return CodecOptions{
		profile:     profile,
		factoryKind: factoryKind,
		providerID:  strings.TrimSpace(providerID),
	}, nil
}

// Profile returns the pinned OpenResponses profile (always DefaultProfile for
// the generic codec; empty only for a zero-value CodecOptions).
func (o CodecOptions) Profile() string { return o.profile }

// FactoryKind returns the constructing factory kind (generic [ID] unless a
// future provider connector overrides it for provenance/diagnostics only).
func (o CodecOptions) FactoryKind() string { return o.factoryKind }

// ProviderID returns the bounded provider provenance label; empty means the
// generic remote mode. It is never forwarded on the wire.
func (o CodecOptions) ProviderID() string { return o.providerID }

// validateProviderID bounds the optional provider provenance label. Empty is
// valid (generic remote mode). Otherwise the label must be a lowercase
// identifier of letters/digits/hyphens/dots, at most 32 bytes, and must not
// start or end with a separator. Provider-policy payloads (routes, billing,
// catalog, attribution controls) are structurally rejected here.
func validateProviderID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if len(id) > 32 {
		return fmt.Errorf("%s: provider id must be at most 32 bytes", ID)
	}
	validRune := func(r rune) bool {
		return r == '-' || r == '.' || unicode.IsLower(r) || unicode.IsDigit(r)
	}
	for i, r := range id {
		if !validRune(r) {
			return fmt.Errorf("%s: provider id %q contains invalid character %q", ID, id, r)
		}
		if (r == '-' || r == '.') && (i == 0 || i == len(id)-1) {
			return fmt.Errorf("%s: provider id %q must not start or end with a separator", ID, id)
		}
	}
	return nil
}

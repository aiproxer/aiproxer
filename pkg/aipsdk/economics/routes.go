package economics

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// MaxQuoteRoutes bounds the ordered planned-route occurrence list carried by
// one quote and the distinct route-tariff bindings returned with it. It
// matches the existing envelope-observation bound and stays far above any
// parsed selector.
const MaxQuoteRoutes = 1024

// RouteOutputBound is the finite output bound the runtime has actually
// permitted for one planned route leaf after its token preflight: the effective
// canonical request limit the backend is executed with (explicit client max,
// enforced clamp/default, or the model/backend maximum). It is never an
// advisory threshold, and Present=false means no defensible finite bound
// exists.
type RouteOutputBound struct {
	Tokens  int64 `json:"tokens,omitempty"`
	Present bool  `json:"present"`
}

// Validate enforces the presence semantics: a present bound is positive, an
// absent bound carries no token claim.
func (b RouteOutputBound) Validate() error {
	if b.Present && b.Tokens <= 0 {
		return fmt.Errorf("%w: present route output bound requires positive tokens", ErrInvalidQuote)
	}
	if !b.Present && b.Tokens != 0 {
		return fmt.Errorf("%w: absent route output bound cannot carry tokens", ErrInvalidQuote)
	}
	return nil
}

// Clone returns an independent value copy.
func (b RouteOutputBound) Clone() RouteOutputBound { return b }

// RouteCandidate is one planned route leaf occurrence carried by a quote
// request. Backend is empty for model-only selectors; Model is always
// required. The list is an ordered occurrence list: every planned occurrence
// appears once in the exact traversal order of the shared planned-leaf
// projection, duplicates are legal and load-bearing (two identical parallel
// branches cost twice), and the list is never read as a set or deduplicated.
type RouteCandidate struct {
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model"`
	// MaxOutputTokens is the runtime-permitted finite output bound for this
	// occurrence; nil keeps the version-1 request mapping.
	MaxOutputTokens *RouteOutputBound `json:"max_output_tokens,omitempty"`
}

// Validate checks the bounded, trimmed route identity. Backend is optional;
// Model is required.
func (c RouteCandidate) Validate() error {
	if c.Backend != "" {
		if err := validatePublicRef("route candidate backend", c.Backend); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
	}
	if err := validatePublicRef("route candidate model", c.Model); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	if c.MaxOutputTokens != nil {
		if err := c.MaxOutputTokens.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns an independent value copy.
func (c RouteCandidate) Clone() RouteCandidate {
	out := c
	if c.MaxOutputTokens != nil {
		bound := c.MaxOutputTokens.Clone()
		out.MaxOutputTokens = &bound
	}
	return out
}

// RouteTariffBinding freezes the exact customer tariff material used to quote
// one candidate route: the canonical route key plus the tariff identity and
// its canonical content hash. It mirrors the internal reference binding rules
// so quote, admission, and settlement derive the same identity.
type RouteTariffBinding struct {
	RouteID       string `json:"route_id"`
	TariffID      string `json:"tariff_id"`
	TariffVersion string `json:"tariff_version"`
	ContentHash   string `json:"content_hash"`
}

// Validate requires the bounded identity fields and a lowercase SHA-256
// content hash.
func (b RouteTariffBinding) Validate() error {
	for name, value := range map[string]string{
		"route tariff binding route":          b.RouteID,
		"route tariff binding tariff id":      b.TariffID,
		"route tariff binding tariff version": b.TariffVersion,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
	}
	if len(b.ContentHash) != 64 || strings.ToLower(b.ContentHash) != b.ContentHash {
		return fmt.Errorf("%w: route tariff binding content hash must be lowercase SHA-256 hex", ErrInvalidQuote)
	}
	if _, err := hex.DecodeString(b.ContentHash); err != nil {
		return fmt.Errorf("%w: route tariff binding content hash: %v", ErrInvalidQuote, err)
	}
	return nil
}

// Clone returns an independent value copy.
func (b RouteTariffBinding) Clone() RouteTariffBinding { return b }

// RouteTariffKey returns the canonical route identity binding one backend and
// model to its frozen customer tariff. It deliberately excludes routing
// display params, which never change tariff resolution. The internal reference
// billing.RouteTariffKey is the behavioral oracle; the two must stay equal.
func RouteTariffKey(backend, model string) string {
	return strings.TrimSpace(backend) + ":" + strings.TrimSpace(model)
}

// RouteTariffMode is the explicit quote-level declaration of how a quote
// carries per-route tariff material.
type RouteTariffMode string

const (
	// RouteTariffModeNone means the quote intentionally carries no per-route
	// tariff material: the legacy scalar path or a strict-ceiling fallback
	// bound. RouteTariffs must be empty.
	RouteTariffModeNone RouteTariffMode = "none"
	// RouteTariffModeFrozen means the quote carries one frozen binding per
	// distinct canonical route key of the planned leaves.
	RouteTariffModeFrozen RouteTariffMode = "frozen"
)

// IsKnown reports whether m is a documented route-tariff mode.
func (m RouteTariffMode) IsKnown() bool {
	return m == RouteTariffModeNone || m == RouteTariffModeFrozen
}

// RouteTariffCarrier is the optional quoter declaration for participation in
// route-fact carriage. A quoter must declare it to receive the ordered
// planned-route occurrence list and to have frozen bindings carried back; a
// non-declaring quoter keeps the version-1 mapping with no coverage
// requirement and no bindings stamped.
type RouteTariffCarrier interface {
	CarriesRouteTariffs()
}

// ReferenceFacts is the optional quoter refinement that resolves the frozen
// reference facts stamped into generated usage records. The zero ref means the
// fact is absent and is never substituted. Facts are frozen for the binding
// lifetime exactly like BindingScope and its snapshots; implementations stay
// wire-bounded and perform no per-request provider-data reads.
type ReferenceFacts interface {
	CustomerPricingRef(ctx context.Context) VersionRef
	ChargePolicyRef(ctx context.Context) VersionRef
	OperatorRateRef(ctx context.Context, backendID, modelID string) VersionRef
}

// validateRouteCandidates validates the bounded ordered occurrence list.
func validateRouteCandidates(routes []RouteCandidate) error {
	if len(routes) > MaxQuoteRoutes {
		return fmt.Errorf("%w: route candidate bound exceeded", ErrInvalidQuote)
	}
	for i, route := range routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("%w: route candidate %d: %v", ErrInvalidQuote, i, err)
		}
	}
	return nil
}

// validateRouteTariffCarriage validates the mode/binding consistency: version-1
// mode-less quotes stay valid with empty bindings, none requires empty
// bindings, frozen requires at least one binding, and bindings without a mode
// fail closed so an omitted mode can never be read as an intentional scalar
// quote. Distinctness is a producer/coverage rule enforced where the plan is
// known, not here.
func validateRouteTariffCarriage(mode RouteTariffMode, bindings []RouteTariffBinding) error {
	if len(bindings) > MaxQuoteRoutes {
		return fmt.Errorf("%w: route tariff binding bound exceeded", ErrInvalidQuote)
	}
	for i, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("%w: route tariff binding %d: %v", ErrInvalidQuote, i, err)
		}
	}
	switch mode {
	case "":
		if len(bindings) != 0 {
			return fmt.Errorf("%w: route tariff bindings require an explicit route tariff mode", ErrInvalidQuote)
		}
	case RouteTariffModeNone:
		if len(bindings) != 0 {
			return fmt.Errorf("%w: route tariff mode %q requires empty bindings", ErrInvalidQuote, mode)
		}
	case RouteTariffModeFrozen:
		if len(bindings) == 0 {
			return fmt.Errorf("%w: route tariff mode %q requires bindings", ErrInvalidQuote, mode)
		}
	default:
		return fmt.Errorf("%w: unknown route tariff mode %q", ErrInvalidQuote, mode)
	}
	return nil
}

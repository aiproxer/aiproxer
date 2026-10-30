package economics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// SnapshotState classifies versioned snapshot readiness (requirement 11.7).
type SnapshotState string

const (
	SnapshotReady       SnapshotState = "ready"
	SnapshotStale       SnapshotState = "stale"
	SnapshotDegraded    SnapshotState = "degraded"
	SnapshotUnavailable SnapshotState = "unavailable"
	SnapshotDisabled    SnapshotState = "disabled"
)

// IsKnown reports whether s is a documented snapshot state.
func (s SnapshotState) IsKnown() bool {
	switch s {
	case SnapshotReady, SnapshotStale, SnapshotDegraded, SnapshotUnavailable, SnapshotDisabled:
		return true
	default:
		return false
	}
}

// Snapshot is an immutable versioned envelope for policy or rating material
// (design: Versioned Snapshots / Dynamic Sources).
type Snapshot[T any] struct {
	ID          string        `json:"id"`
	Version     string        `json:"version"`
	EffectiveAt time.Time     `json:"effective_at,omitzero"`
	FetchedAt   time.Time     `json:"fetched_at,omitzero"`
	State       SnapshotState `json:"state"`
	Value       T             `json:"value"`
}

// Ref returns a PolicySnapshotRef for authority/concurrency binding.
func (s Snapshot[T]) PolicyRef(policyID string) PolicySnapshotRef {
	return PolicySnapshotRef{
		VersionRef: VersionRef{
			ID:          s.ID,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		PolicyID: policyID,
	}
}

// RatingRef returns a RatingSnapshotRef for rating binding.
func (s Snapshot[T]) RatingRef(raterID string) RatingSnapshotRef {
	return RatingSnapshotRef{
		VersionRef: VersionRef{
			ID:          s.ID,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		RaterID: raterID,
	}
}

// PolicyKind identifies which rule plane a policy snapshot represents.
type PolicyKind string

const (
	PolicyKindUsageAuthority PolicyKind = "usage_authority"
	PolicyKindConcurrency    PolicyKind = "concurrency"
)

// IsKnown reports whether k is a documented policy kind.
func (k PolicyKind) IsKnown() bool {
	switch k {
	case PolicyKindUsageAuthority, PolicyKindConcurrency:
		return true
	default:
		return false
	}
}

// PolicyRulesView is the public, opaque policy payload for injectable sources.
// Concrete rule decoding stays internal; enterprise modules may supply opaque bytes.
type PolicyRulesView struct {
	Kind    PolicyKind `json:"kind"`
	Payload []byte     `json:"payload,omitempty"`
}

// RatingCatalogView is the public rating/catalog snapshot payload. Prices are
// Enterprise material: Rules is an opaque JSON array that the public package
// carries and bounds but never decodes, following the PolicyRulesView
// precedent. The wire form is the rule array a view always carried, so
// published catalogs keep their bytes and content identities.
type RatingCatalogView struct {
	Currency               string               `json:"currency,omitempty"`
	CatalogVersion         string               `json:"catalog_version,omitempty"`
	Rules                  json.RawMessage      `json:"rules,omitempty"`
	EffectiveQualifiers    []metering.Dimension `json:"effective_qualifiers,omitempty"`
	LegacySemantics        string               `json:"legacy_semantics,omitempty"`
	SupportAdvisoryVersion string               `json:"support_advisory_version,omitempty"`
	// Schemas carries optional frozen component-relationship material published
	// by a rating source. It is the view-plane counterpart of
	// TariffSnapshot.Schemas: a view that omits or drops schemas reconstructs a
	// different content identity than the published tariff. Nil/empty schemas
	// reproduce the exact legacy contract.
	Schemas []metering.ComponentSchema `json:"schemas,omitempty"`
}

// RuleSnapshotSource provides immutable authority or concurrency rule snapshots
// (design: Dynamic Sources; requirements 11.1, 11.5, 11.6).
type RuleSnapshotSource interface {
	Snapshot(ctx context.Context) (Snapshot[PolicyRulesView], error)
}

// RatingSnapshotSource provides immutable rating/pricebook snapshots.
type RatingSnapshotSource interface {
	Snapshot(ctx context.Context) (Snapshot[RatingCatalogView], error)
}

// Clone returns a deep copy of the view.
func (v RatingCatalogView) Clone() RatingCatalogView {
	out := v
	out.Rules = append(json.RawMessage(nil), v.Rules...)
	if v.Rules == nil {
		out.Rules = nil
	}
	out.EffectiveQualifiers = append([]metering.Dimension(nil), v.EffectiveQualifiers...)
	out.Schemas = cloneComponentSchemas(v.Schemas)
	return out
}

// Validate validates the public catalog material without decoding prices: the
// currency, the rules payload's shape and bound, the legacy semantics marker,
// the qualifiers and the component schemas. Rule-level validation belongs to
// the rating owner that decodes the payload (DecodeRatingRules).
func (v RatingCatalogView) Validate() error {
	if err := validateSupportAdvisoryVersion(v.SupportAdvisoryVersion); err != nil {
		return err
	}
	if v.Currency != "" {
		if err := NormalizeCurrencyRequired(v.Currency); err != nil {
			return err
		}
	}
	if len(v.Rules) != 0 {
		var rules []json.RawMessage
		if err := json.Unmarshal(v.Rules, &rules); err != nil {
			return fmt.Errorf("%w: rules payload must be a JSON array: %v", ErrInvalidTariffSnapshot, err)
		}
		if len(rules) > MaxRatingRules {
			return fmt.Errorf("%w: rule bound exceeded", ErrInvalidTariffSnapshot)
		}
	}
	if v.LegacySemantics != "" {
		if err := validatePublicRef("legacy semantics", v.LegacySemantics); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
		}
	}
	if err := validateDimensions(v.EffectiveQualifiers, ErrInvalidTariffSnapshot); err != nil {
		return err
	}
	if err := metering.ValidateComponentSchemas(v.Schemas); err != nil {
		return fmt.Errorf("%w: schemas: %w", ErrInvalidTariffSnapshot, err)
	}
	return nil
}

func cloneComponentSchemas(schemas []metering.ComponentSchema) []metering.ComponentSchema {
	if schemas == nil {
		return nil
	}
	out := make([]metering.ComponentSchema, len(schemas))
	for i, schema := range schemas {
		out[i] = schema.Clone()
	}
	return out
}

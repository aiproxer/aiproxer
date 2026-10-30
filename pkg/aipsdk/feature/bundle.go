package feature

import (
	"fmt"

	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// SchemaVersionV1 is the initial FeatureBundle wire/compile contract. New optional
// fields may be added in backward-compatible ways; bump only when breaking stable fields.
const SchemaVersionV1 = 1

// FeatureBundle is the versioned unit a feature factory contributes: schema version
// metadata, an immutable [FrozenPlaneSet], and optional plugin lifecycles.
// Note: FeatureBundle contains no per-plane named fields or slices; all extension planes
// are stored in the immutable [FrozenPlaneSet].
type FeatureBundle struct {
	SchemaVersion int

	// PlaneSet is the immutable composed extension plane snapshot (V1).
	PlaneSet FrozenPlaneSet

	Lifecycles []aipplugin.Lifecycle
}

// BundleFromPlanes constructs a [FeatureBundle] from a [FrozenPlaneSet] and optional lifecycles.
// It sets SchemaVersion to [SchemaVersionV1], defensively clones the [FrozenPlaneSet] and lifecycles
// (preserving nil vs explicit empty slice semantics), and performs no validation side effects.
func BundleFromPlanes(planes FrozenPlaneSet, lifecycles []aipplugin.Lifecycle) FeatureBundle {
	return FeatureBundle{
		SchemaVersion: SchemaVersionV1,
		PlaneSet:      planes.Clone(),
		Lifecycles:    cloneSlice(lifecycles),
	}
}

func (b FeatureBundle) empty() bool {
	return b.PlaneSet.IsZero() && len(b.Lifecycles) == 0
}

// Validate checks schema metadata against bundle contents. An empty bundle may use
// SchemaVersion 0 (unset) or SchemaVersionV1; any non-empty bundle must declare SchemaVersionV1.
func (b FeatureBundle) Validate() error {
	if b.empty() {
		if b.SchemaVersion != 0 && b.SchemaVersion != SchemaVersionV1 {
			return fmt.Errorf("feature: FeatureBundle: invalid schema version %d for empty bundle", b.SchemaVersion)
		}
		return nil
	}
	if b.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("feature: FeatureBundle: schema version want %d got %d", SchemaVersionV1, b.SchemaVersion)
	}
	if !b.PlaneSet.IsZero() {
		if err := b.PlaneSet.Validate(); err != nil {
			return fmt.Errorf("feature: FeatureBundle: PlaneSet: %w", err)
		}
	}
	return nil
}

package terminalwork

import (
	"fmt"
	"strings"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// SourceKey is a stable versioned work identity key (requirement 8.2).
type SourceKey struct {
	IdentityVersion int
	Key             string
}

// Validate rejects non-positive versions and empty/whitespace keys.
func (k SourceKey) Validate() error {
	if k.IdentityVersion < 1 {
		return fmt.Errorf("%w: source key identity version", sdk.ErrInvalid)
	}
	if strings.TrimSpace(k.Key) == "" {
		return fmt.Errorf("%w: empty source key", sdk.ErrInvalid)
	}
	return nil
}

// String returns the stable versioned key encoding.
func (k SourceKey) String() string {
	return fmt.Sprintf("v%d:%s", k.IdentityVersion, k.Key)
}

// RuntimeIdentityClass classifies BoundVersions runtime identity material.
type RuntimeIdentityClass uint8

const (
	// RuntimeIdentityLegacy is both runtime instance and generation empty —
	// process-registry compatible rows from before task 3.6.
	RuntimeIdentityLegacy RuntimeIdentityClass = iota
	// RuntimeIdentityExact is both runtime instance and generation non-empty.
	RuntimeIdentityExact
	// RuntimeIdentityMalformed is only one of instance/generation populated.
	// It never falls back to legacy process-registry resolution.
	RuntimeIdentityMalformed
)

// BoundVersions records generation/provider/rating versions bound at admit.
//
// Identity rules (task 3.6):
//   - GenerationID is the executable-policy / snapshotgen generation identity.
//     The historical field name is retained for persisted rows, storage codecs,
//     and SameIntentReplay determinism. Prefer ExecutableGenerationID().
//   - RuntimeInstanceID is the opaque runtimehost.Manager process incarnation.
//   - RuntimeGenerationID is the runtimehost request-plane generation identity
//     (GenerationMeta.ID). Together with RuntimeInstanceID it forms durable
//     runtime identity; numeric IDs alone are not restart-safe.
//   - These identities must never be conflated with each other or with model
//     registry/catalog generations.
type BoundVersions struct {
	GenerationID        string // executable-policy generation (persisted generation_id)
	RuntimeInstanceID   string // opaque process/manager incarnation
	RuntimeGenerationID string // request-plane runtime configuration generation
	ProviderID          string
	RatingID            string
}

// ExecutableGenerationID returns the executable-policy generation identity.
func (v BoundVersions) ExecutableGenerationID() string {
	return v.GenerationID
}

// RuntimeIdentity returns the runtime identity classification for this row.
func (v BoundVersions) RuntimeIdentity() RuntimeIdentityClass {
	inst := strings.TrimSpace(v.RuntimeInstanceID)
	gen := strings.TrimSpace(v.RuntimeGenerationID)
	switch {
	case inst == "" && gen == "":
		return RuntimeIdentityLegacy
	case inst != "" && gen != "":
		return RuntimeIdentityExact
	default:
		return RuntimeIdentityMalformed
	}
}

// HasRuntimeGeneration reports whether an exact runtime configuration identity
// was bound (both instance and generation non-empty).
func (v BoundVersions) HasRuntimeGeneration() bool {
	return v.RuntimeIdentity() == RuntimeIdentityExact
}

// LifecycleCorrelation ties work to safe request/attempt identities.
type LifecycleCorrelation struct {
	RequestID string
	AttemptID string
	TraceID   string
}

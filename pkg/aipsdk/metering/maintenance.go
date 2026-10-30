package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidMaintenance is the canonical invalid-record sentinel for the
// provider-maintenance DTO and for the carved usage-evidence vocabulary
// (internal/core/usageevidence aliases it as ErrInvalidRecord). The text is the
// existing "billing: invalid usage record" contract; this is one definition,
// not an additional sentinel.
//
// The maintenance vocabulary lives in this neutral public leaf because it is
// provider-maintenance usage evidence, not a monetary binding contract. The
// public billing package keeps the same names as aliases, so every existing
// consumer keeps one type identity, one fingerprint and one error value.
var ErrInvalidMaintenance = errors.New("billing: invalid usage record")

// MaintenanceQuantity is one provider-reported token quantity with explicit
// presence: an absent quantity is the zero value, an explicit provider zero is
// Present with Value 0.
type MaintenanceQuantity struct {
	Value   int64
	Present bool
}

// MaintenanceMoney is provider-reported cost with explicit presence. A present
// cost carries the reported currency; absent cost stays the zero value.
type MaintenanceMoney struct {
	NanoUnits int64
	Currency  string
	Present   bool
}

// MaintenanceSource names where a maintenance quantity came from.
type MaintenanceSource string

const (
	MaintenanceSourceUnknown          MaintenanceSource = ""
	MaintenanceSourceProviderReported MaintenanceSource = "provider_reported"
	MaintenanceSourceProviderCountAPI MaintenanceSource = "provider_count_api"
	MaintenanceSourceLocalTokenizer   MaintenanceSource = "local_tokenizer"
	MaintenanceSourceLocalEstimator   MaintenanceSource = "local_estimator"
	MaintenanceSourceUnavailable      MaintenanceSource = "unavailable"
)

// MaintenanceAuthority names the trust level of a maintenance quantity.
type MaintenanceAuthority string

const (
	MaintenanceAuthorityUnknown       MaintenanceAuthority = ""
	MaintenanceAuthorityAuthoritative MaintenanceAuthority = "authoritative"
	MaintenanceAuthorityDelegated     MaintenanceAuthority = "delegated"
	MaintenanceAuthorityEstimated     MaintenanceAuthority = "estimated"
	MaintenanceAuthorityUnavailable   MaintenanceAuthority = "unavailable"
)

// MaintenanceEvidence is the complete scalar evidence vocabulary of one
// provider-maintenance observation: six token quantities, the cost amount with
// currency and presence, source, authority, and the dedupe key. Exported field
// names and order are load-bearing: the durable store marshals this value with
// field names (no JSON tags) and the persisted JSON must stay byte-identical.
type MaintenanceEvidence struct {
	InputTokens      MaintenanceQuantity
	OutputTokens     MaintenanceQuantity
	CacheReadTokens  MaintenanceQuantity
	CacheWriteTokens MaintenanceQuantity
	ReasoningTokens  MaintenanceQuantity
	TotalTokens      MaintenanceQuantity
	Cost             MaintenanceMoney
	Source           MaintenanceSource
	Authority        MaintenanceAuthority
	DedupeKey        string
}

func validateMaintenanceEvidence(e MaintenanceEvidence) error {
	for name, q := range map[string]MaintenanceQuantity{
		"input": e.InputTokens, "output": e.OutputTokens, "cache_read": e.CacheReadTokens,
		"cache_write": e.CacheWriteTokens, "reasoning": e.ReasoningTokens, "total": e.TotalTokens,
	} {
		if q.Value < 0 || (!q.Present && q.Value != 0) {
			return fmt.Errorf("%w: invalid %s quantity presence/value", ErrInvalidMaintenance, name)
		}
	}
	if e.Cost.NanoUnits < 0 || (!e.Cost.Present && e.Cost.NanoUnits != 0) {
		return fmt.Errorf("%w: invalid cost presence/value", ErrInvalidMaintenance)
	}
	if e.Cost.Present && strings.TrimSpace(e.Cost.Currency) == "" {
		return fmt.Errorf("%w: present cost requires currency", ErrInvalidMaintenance)
	}
	return nil
}

// MaintenanceUsage is provider-authoritative usage from a control-plane
// maintenance call. It is intentionally not a usage-evidence leg record:
// maintenance must retain its operation identity without masquerading as a
// foreground B-leg. The payload identity is OperationID; duplicates are
// idempotent and changed payloads conflict at the durable owner.
type MaintenanceUsage struct {
	OperationID string
	ALegID      string
	TargetID    string
	BackendID   string
	ModelID     string
	RecordedAt  time.Time
	Evidence    MaintenanceEvidence
}

// Validate enforces the existing maintenance contract: trimmed-non-empty
// identity fields, a non-zero timestamp, and evidence bounds. Every error wraps
// ErrInvalidMaintenance.
func (u MaintenanceUsage) Validate() error {
	if strings.TrimSpace(u.OperationID) == "" || strings.TrimSpace(u.ALegID) == "" ||
		strings.TrimSpace(u.TargetID) == "" || strings.TrimSpace(u.BackendID) == "" ||
		strings.TrimSpace(u.ModelID) == "" || u.RecordedAt.IsZero() {
		return fmt.Errorf("%w: maintenance identity and timestamp are required", ErrInvalidMaintenance)
	}
	return validateMaintenanceEvidence(u.Evidence)
}

// Fingerprint returns the durable identity of the maintenance payload. The
// preimage field order and the version prefix are frozen: RecordedAt is
// excluded, and the returned shape is "provider-maintenance:v1:<sha256 hex>".
func (u MaintenanceUsage) Fingerprint() (string, error) {
	if err := u.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Version     string
		OperationID string
		ALegID      string
		TargetID    string
		BackendID   string
		ModelID     string
		Evidence    MaintenanceEvidence
	}{
		Version: "provider-maintenance:v1", OperationID: u.OperationID, ALegID: u.ALegID,
		TargetID: u.TargetID, BackendID: u.BackendID, ModelID: u.ModelID, Evidence: u.Evidence,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "provider-maintenance:v1:" + hex.EncodeToString(digest[:]), nil
}

// MaintenanceObserver delivers immutable maintenance evidence to its durable
// owner. Errors stay visible to the calling lifecycle so delivery failures are
// not silently mistaken for successful accounting; implementations must honor
// the supplied context and must not retain request bodies.
type MaintenanceObserver interface {
	ObserveProviderMaintenance(context.Context, MaintenanceUsage) error
}

// MaintenanceObserverFunc adapts a function to MaintenanceObserver. A nil
// function observes nothing and returns nil.
type MaintenanceObserverFunc func(context.Context, MaintenanceUsage) error

// ObserveProviderMaintenance implements MaintenanceObserver.
func (f MaintenanceObserverFunc) ObserveProviderMaintenance(ctx context.Context, usage MaintenanceUsage) error {
	if f == nil {
		return nil
	}
	return f(ctx, usage)
}

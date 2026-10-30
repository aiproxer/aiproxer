package billingport

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
)

type ExposureStatus string

const (
	ExposureOpen   ExposureStatus = "open"
	ExposureClosed ExposureStatus = "closed"
)

type ExposureBasis struct {
	BalanceNano            int64
	CreditFloorNano        int64
	OpenExposureNano       int64
	SettledHeadroomNano    int64
	SafetyMarginBeforeNano int64
	SafetyMarginAfterNano  int64
}

// RouteTariffBinding carries only frozen identity data, not tariff material or
// validation. Its JSON shape is also used by persisted exposure snapshots.
type RouteTariffBinding struct {
	RouteID       string `json:"route_id"`
	TariffID      string `json:"tariff_id"`
	TariffVersion string `json:"tariff_version"`
	ContentHash   string `json:"content_hash"`
}

type CallExposure struct {
	AccountID string
	CallID    string
	// Max is an inert amount projection, with no monetary methods. The unnamed
	// struct preserves assignment compatibility with implementation-owned money.
	Max struct {
		Nano     int64
		Currency string
	}
	PricingRef      usageevidence.VersionRef
	ChargePolicyRef usageevidence.VersionRef
	// RouteTariffs carries the frozen per-route customer tariff bindings
	// admitted with this exposure. Empty on the legacy scalar path, which
	// stays bound through the base pricing/policy references alone.
	RouteTariffs []RouteTariffBinding
	Fingerprint  string
	CreatedAt    time.Time
	ClosedAt     time.Time
	Status       ExposureStatus
	Basis        ExposureBasis
}

func (e CallExposure) IsOpen() bool {
	return e.Status == ExposureOpen && e.ClosedAt.IsZero()
}

type AdmitExposureInput struct {
	AccountID string
	CallID    string
	Max       struct {
		Nano     int64
		Currency string
	}
	PricingRef      usageevidence.VersionRef
	ChargePolicyRef usageevidence.VersionRef
	// RouteTariffs carries the frozen per-route customer tariff bindings
	// quoted for this call. Empty on the legacy scalar path.
	RouteTariffs []RouteTariffBinding
	Now          time.Time
}

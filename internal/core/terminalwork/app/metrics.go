package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Metrics page/scan bounds (requirement 8.9, 12.5–12.8).
const (
	MetricsPageSize       = 256
	MaxMetricsScanRecords = 100_000
	maxMetricsCursorPages = 10_000
)

// MetricsConfig configures MetricsObserver clocks.
type MetricsConfig struct {
	Clock func() time.Time
}

// MetricsSnapshot is a bounded backlog view for readiness/metrics exporters.
type MetricsSnapshot struct {
	Backlog     int
	OldestAge   time.Duration
	Pending     int
	Retrying    int
	Quarantined int
	Completed   int
	Claimed     int
	Intent      int
}

// MetricsObserver computes backlog and oldest-age from a query store.
type MetricsObserver struct {
	store QueryStore
	clock func() time.Time
}

// NewMetricsObserver returns a metrics snapshotter.
func NewMetricsObserver(store QueryStore, cfg MetricsConfig) *MetricsObserver {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &MetricsObserver{store: store, clock: clock}
}

// Snapshot returns counts and oldest outstanding age.
// Query failures and cursor faults are returned as errors; callers must not
// treat zero counts as ready.
func (o *MetricsObserver) Snapshot(ctx context.Context) (MetricsSnapshot, error) {
	out := MetricsSnapshot{}
	if o == nil || o.store == nil {
		return out, fmt.Errorf("%w: nil metrics store", ErrNilIntentStore)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := o.clock().UTC()
	q := terminalwork.ListQuery{
		States: []sdk.WorkState{
			sdk.WorkStateIntent,
			sdk.WorkStatePending,
			sdk.WorkStateRetry,
			sdk.WorkStateClaimed,
			sdk.WorkStateQuarantined,
			sdk.WorkStateCompleted,
		},
		Limit: MetricsPageSize,
	}
	seen := map[string]struct{}{"": {}}
	prev := ""
	var oldest time.Time
	scanned := 0
	for range maxMetricsCursorPages {
		if err := ctx.Err(); err != nil {
			return MetricsSnapshot{}, err
		}
		page, err := o.store.List(ctx, q)
		if err != nil {
			return MetricsSnapshot{}, err
		}
		if len(page.Records) == 0 && strings.TrimSpace(page.Cursor) != "" {
			return MetricsSnapshot{}, fmt.Errorf("%w: empty page with next cursor", ErrMetricsCursorFault)
		}
		for _, rec := range page.Records {
			scanned++
			if scanned > MaxMetricsScanRecords {
				return MetricsSnapshot{}, fmt.Errorf("%w: scanned %d max %d", ErrMetricsBoundExceeded, scanned, MaxMetricsScanRecords)
			}
			switch rec.State {
			case sdk.WorkStatePending:
				out.Pending++
			case sdk.WorkStateRetry:
				out.Retrying++
			case sdk.WorkStateQuarantined:
				out.Quarantined++
			case sdk.WorkStateCompleted:
				out.Completed++
			case sdk.WorkStateClaimed:
				out.Claimed++
			case sdk.WorkStateIntent:
				out.Intent++
			}
			if isBacklogState(rec.State) {
				out.Backlog++
				ts := rec.UpdatedAt
				if ts.IsZero() {
					ts = rec.CreatedAt
				}
				if oldest.IsZero() || ts.Before(oldest) {
					oldest = ts
				}
			}
		}
		next := strings.TrimSpace(page.Cursor)
		if next == "" {
			break
		}
		if next == prev || next == strings.TrimSpace(q.Cursor) {
			return MetricsSnapshot{}, fmt.Errorf("%w: non-advancing cursor %q", ErrMetricsCursorFault, next)
		}
		if _, ok := seen[next]; ok {
			return MetricsSnapshot{}, fmt.Errorf("%w: cyclic cursor %q", ErrMetricsCursorFault, next)
		}
		if len(page.Records) == 0 {
			return MetricsSnapshot{}, fmt.Errorf("%w: no progress", ErrMetricsCursorFault)
		}
		seen[next] = struct{}{}
		prev = next
		q.Cursor = next
	}
	if !oldest.IsZero() && now.After(oldest) {
		out.OldestAge = now.Sub(oldest)
	}
	return out, nil
}

func isBacklogState(st sdk.WorkState) bool {
	switch st {
	case sdk.WorkStateIntent, sdk.WorkStatePending, sdk.WorkStateRetry, sdk.WorkStateClaimed:
		return true
	default:
		return false
	}
}

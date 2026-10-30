// Package reconcile exposes bounded journal reconciliation helpers over a
// metering Querier (requirements 13.6, 13.7, 14.5, 15.3). It does not touch
// live reservation/lease authority stores.
package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// UnresolvedItem is one journal fact or stream needing operator attention.
// Historical metering totals are distinct from live reservations/leases (14.5).
type UnresolvedItem struct {
	StreamID  string
	FactID    string
	Kind      metering.FactKind
	Reason    string
	RequestID string
}

// Report is the result of a bounded stream reconciliation.
type Report struct {
	StreamID   string
	Snapshot   aggregate.Snapshot
	Unresolved []UnresolvedItem
}

// Options bounds reconciliation work.
type Options struct {
	// Timeout is applied via a fresh child context (requirement 15.3).
	Timeout time.Duration
	// PageLimit caps each List page; default 100.
	PageLimit int
}

// Stream loads all facts for streamID (paged), aggregates without double-count,
// and lists unavailable / orphaned egress items.
func Stream(ctx context.Context, q metering.Querier, streamID string, opts Options) (Report, error) {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return Report{}, fmt.Errorf("metering/reconcile: stream_id required")
	}
	if q == nil {
		return Report{}, fmt.Errorf("metering/reconcile: nil querier")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	limit := opts.PageLimit
	if limit <= 0 {
		limit = 100
	}
	var facts []metering.Fact
	cursor := ""
	for {
		page, err := q.List(child, metering.Query{StreamID: streamID, Limit: limit, Cursor: cursor})
		if err != nil {
			return Report{}, fmt.Errorf("metering/reconcile: list: %w", err)
		}
		facts = append(facts, page.Facts...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	snap, err := aggregate.Apply(facts)
	if err != nil {
		return Report{}, err
	}
	rep := Report{StreamID: streamID, Snapshot: snap}
	seenIngress := false
	seenEgress := false
	for _, f := range facts {
		switch f.Boundary {
		case metering.BoundaryFrontendIngress, metering.BoundaryBackendIngress:
			seenIngress = true
		case metering.BoundaryBackendEgress, metering.BoundaryFrontendEgress:
			seenEgress = true
		}
		if f.Kind == metering.FactKindUnavailable {
			rep.Unresolved = append(rep.Unresolved, UnresolvedItem{
				StreamID:  streamID,
				FactID:    f.FactID,
				Kind:      f.Kind,
				Reason:    "unavailable_fact",
				RequestID: f.Correlation.RequestID,
			})
		}
	}
	if seenEgress && !seenIngress {
		rep.Unresolved = append(rep.Unresolved, UnresolvedItem{
			StreamID: streamID,
			Reason:   "orphan_egress_without_ingress",
		})
	}
	for _, id := range snap.Unavailable {
		found := false
		for _, u := range rep.Unresolved {
			if u.FactID == id {
				found = true
				break
			}
		}
		if !found {
			rep.Unresolved = append(rep.Unresolved, UnresolvedItem{
				StreamID: streamID,
				FactID:   id,
				Kind:     metering.FactKindUnavailable,
				Reason:   "unavailable_in_aggregate",
			})
		}
	}
	return rep, nil
}

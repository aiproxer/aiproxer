package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const DefaultMeteringDrainMaxFacts = 10_000

var (
	ErrMeteringCursorFault        = errors.New("controlplane: metering cursor fault")
	ErrMeteringDrainBoundExceeded = errors.New("controlplane: metering drain bound exceeded")
)

type MeteringDrainBound struct {
	MaxFacts int
}

func ListUsageRowsFromMetering(ctx context.Context, q metering.Querier, query metering.Query) ([]cp.UsageRow, metering.Page, error) {
	return ListUsageRowsFromMeteringBounded(ctx, q, query, MeteringDrainBound{})
}

func ListUsageRowsFromMeteringBounded(ctx context.Context, q metering.Querier, query metering.Query, bound MeteringDrainBound) ([]cp.UsageRow, metering.Page, error) {
	facts, page, err := drainMeteringFacts(ctx, q, query, bound)
	if err != nil {
		return nil, metering.Page{}, err
	}
	return UsageRowsFromMeteringFacts(facts), page, nil
}

func DualPlaneReportInputsFromMetering(ctx context.Context, q metering.Querier, query metering.Query) (cp.DualPlaneReportInputs, metering.Page, error) {
	return DualPlaneReportInputsFromMeteringBounded(ctx, q, query, MeteringDrainBound{})
}

func DualPlaneReportInputsFromMeteringBounded(ctx context.Context, q metering.Querier, query metering.Query, bound MeteringDrainBound) (cp.DualPlaneReportInputs, metering.Page, error) {
	facts, page, err := drainMeteringFacts(ctx, q, query, bound)
	if err != nil {
		return cp.DualPlaneReportInputs{}, metering.Page{}, err
	}
	in, err := cp.DualPlaneReportInputsFromFacts(facts)
	if err != nil {
		return cp.DualPlaneReportInputs{}, page, err
	}
	return in, page, nil
}

func drainMeteringFacts(ctx context.Context, q metering.Querier, query metering.Query, bound MeteringDrainBound) ([]metering.Fact, metering.Page, error) {
	if q == nil {
		return nil, metering.Page{}, fmt.Errorf("controlplane: nil metering querier")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxFacts := bound.MaxFacts
	if maxFacts <= 0 {
		maxFacts = DefaultMeteringDrainMaxFacts
	}
	pageQuery := query
	pageQuery.Cursor = ""
	if pageQuery.Limit <= 0 {
		pageQuery.Limit = 100
	}
	if pageQuery.Limit > metering.MaxQueryLimit {
		pageQuery.Limit = metering.MaxQueryLimit
	}

	var (
		all          []metering.Fact
		unsupported  []metering.UnsupportedFilter
		seenCursors  = map[string]struct{}{"": {}}
		prevCursor   string
		lastPageSize int
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, metering.Page{}, err
		}
		page, err := q.List(ctx, pageQuery)
		if err != nil {
			return nil, metering.Page{}, err
		}
		if unsupported == nil && len(page.Unsupported) > 0 {
			unsupported = append([]metering.UnsupportedFilter(nil), page.Unsupported...)
		}
		if len(page.Facts) == 0 && strings.TrimSpace(page.NextCursor) != "" {
			return nil, metering.Page{}, fmt.Errorf("%w: empty page with next cursor", ErrMeteringCursorFault)
		}
		all = append(all, page.Facts...)
		lastPageSize = len(page.Facts)
		if len(all) > maxFacts {
			return nil, metering.Page{}, fmt.Errorf("%w: collected %d facts max %d", ErrMeteringDrainBoundExceeded, len(all), maxFacts)
		}
		next := strings.TrimSpace(page.NextCursor)
		if next == "" {
			return all, metering.Page{Facts: all, Unsupported: unsupported}, nil
		}
		if next == prevCursor || next == strings.TrimSpace(pageQuery.Cursor) {
			return nil, metering.Page{}, fmt.Errorf("%w: non-advancing cursor %q", ErrMeteringCursorFault, next)
		}
		if _, ok := seenCursors[next]; ok {
			return nil, metering.Page{}, fmt.Errorf("%w: cyclic cursor %q", ErrMeteringCursorFault, next)
		}
		if lastPageSize == 0 {
			return nil, metering.Page{}, fmt.Errorf("%w: no progress", ErrMeteringCursorFault)
		}
		seenCursors[next] = struct{}{}
		prevCursor = next
		pageQuery.Cursor = next
	}
}

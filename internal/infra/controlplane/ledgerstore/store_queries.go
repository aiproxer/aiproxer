package ledgerstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/fields"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/uptrace/bun/dialect"
)

// Events returns bounded raw control-plane events (requirement 9.1).
func (s *DurableStore) Events(ctx context.Context, q cp.EventQuery) (cp.Page[cp.Event], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHash(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.Event]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedEventFilters(s.unsupportedFields, q)

	w := newWhereBuilder(s.dialect)
	if q.Category != "" && !isUnsupportedField(s.unsupportedFields, fields.EventCategory) {
		w.eq("category", string(q.Category))
	}
	w.applyCommon(q.Common, s.unsupportedFields)
	if cur.LastSeq > 0 {
		w.addRaw("id > " + w.placeholder())
		w.args = append(w.args, cur.LastSeq)
	}

	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC"
	if limit > 0 {
		sqlStr += " LIMIT " + w.placeholder()
		w.args = append(w.args, limit+1)
	}

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.Event]{}, fmt.Errorf("%w: query events: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]sequenced[cp.Event], 0, limit)
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.Event]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.Event]{}, decodeErr
		}
		items = append(items, sequenced[cp.Event]{row: applyQueryVisibility(ev, visibility), seq: r.id})
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.Event]{}, fmt.Errorf("%w: iterate events: %v", controlplaneErrStorage(err), err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return paginateWithHasMore(items, limit, hasMore, shapeHash(q), visibility, unsupported), nil
}

// Sessions returns bounded session summaries projected from recorded events
// (requirement 2.1). Grouping is performed in Go from filtered durable rows so
// the projection stays consistent with the memory adapter.
func (s *DurableStore) Sessions(ctx context.Context, q cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashSession(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.SessionSummary]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedSessionFilters(s.unsupportedFields, q)

	w := newWhereBuilder(s.dialect)
	w.applyCommon(q.Common, s.unsupportedFields)
	// Session grouping needs session_id; only fetch rows with a session id.
	w.addRaw("session_id <> ''")
	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC"

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.SessionSummary]{}, fmt.Errorf("%w: query sessions: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	groups := map[string]*sessionGroup{}
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.SessionSummary]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.SessionSummary]{}, decodeErr
		}
		g, ok := groups[ev.Correlation.SessionID]
		if !ok {
			g = &sessionGroup{sessionID: ev.Correlation.SessionID, state: cp.EvidenceRecorded}
			groups[ev.Correlation.SessionID] = g
		}
		if ev.RecordedAt.After(g.lastActivity) {
			g.lastActivity = ev.RecordedAt
		}
		if r.id > g.maxSeq {
			g.maxSeq = r.id
		}
		if ev.Session() != nil {
			if r.id > g.scopeSeq {
				g.scope = ev.Scope
				g.scopeSeq = r.id
			}
		} else if g.scopeSeq == 0 {
			g.scope = ev.Scope
			g.scopeSeq = r.id
		}
		if ev.Usage() != nil {
			g.usage.InputTokens += ev.Usage().InputTokens
			g.usage.OutputTokens += ev.Usage().OutputTokens
			g.usage.TotalTokens += ev.Usage().TotalTokens
			g.usage.CostNanoUnits += ev.Usage().CostNanoUnits
		}
		if ev.Attempt() != nil {
			g.attempts++
		}
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.SessionSummary]{}, fmt.Errorf("%w: iterate sessions: %v", controlplaneErrStorage(err), err)
	}

	rowsList := make([]sequenced[cp.SessionSummary], 0, len(groups))
	for _, g := range groups {
		rowsList = append(rowsList, g.toSummary())
	}
	sortSeq(rowsList)
	resumed := resumeSeq(rowsList, cur.LastSeq)
	return paginate(resumed, limit, shapeHashSession(q), visibility, unsupported), nil
}

// Attempts returns bounded backend attempt rows (requirement 2.2).
func (s *DurableStore) Attempts(ctx context.Context, q cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashAttempt(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.AttemptRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedAttemptFilters(s.unsupportedFields, q)

	w := newWhereBuilder(s.dialect)
	w.eq("category", string(cp.CategoryAttempt))
	if q.Surfaced != "" && !isUnsupportedField(s.unsupportedFields, fields.AttemptSurfaced) {
		w.eq("surfaced", q.Surfaced)
	}
	w.applyCommon(q.Common, s.unsupportedFields)
	if cur.LastSeq > 0 {
		w.addRaw("id > " + w.placeholder())
		w.args = append(w.args, cur.LastSeq)
	}
	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC LIMIT " + w.placeholder()
	w.args = append(w.args, limit+1)

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.AttemptRow]{}, fmt.Errorf("%w: query attempts: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]sequenced[cp.AttemptRow], 0, limit)
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.AttemptRow]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.AttemptRow]{}, decodeErr
		}
		if ev.Attempt() == nil {
			continue
		}
		items = append(items, sequenced[cp.AttemptRow]{row: attemptRowFromEvent(ev), seq: r.id})
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.AttemptRow]{}, fmt.Errorf("%w: iterate attempts: %v", controlplaneErrStorage(err), err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return paginateWithHasMore(items, limit, hasMore, shapeHashAttempt(q), visibility, unsupported), nil
}

// Usage returns bounded usage rows (requirement 2.3, 9.2).
func (s *DurableStore) Usage(ctx context.Context, q cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashUsage(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedUsageFilters(s.unsupportedFields, q)
	if strings.TrimSpace(q.RuleID) != "" {
		return paginateWithHasMore[cp.UsageRow](nil, limit, false, shapeHashUsage(q), visibility, unsupported), nil
	}

	w := newWhereBuilder(s.dialect)
	w.eq("category", string(cp.CategoryUsage))
	if q.Plane != "" && !isUnsupportedField(s.unsupportedFields, fields.UsagePlane) {
		w.eq("usage_plane", q.Plane)
	}
	if q.Availability != "" && !isUnsupportedField(s.unsupportedFields, fields.UsageAvailability) {
		w.eq("usage_availability", q.Availability)
	}
	w.applyCommon(q.Common, s.unsupportedFields)
	if cur.LastSeq > 0 {
		w.addRaw("id > " + w.placeholder())
		w.args = append(w.args, cur.LastSeq)
	}
	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC LIMIT " + w.placeholder()
	w.args = append(w.args, limit+1)

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.UsageRow]{}, fmt.Errorf("%w: query usage: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]sequenced[cp.UsageRow], 0, limit)
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.UsageRow]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.UsageRow]{}, decodeErr
		}
		if ev.Usage() == nil {
			continue
		}
		if !usageDetailMatchesQuery(ev.Usage(), q, s.unsupportedFields) {
			continue
		}
		items = append(items, sequenced[cp.UsageRow]{row: usageRowFromEvent(ev), seq: r.id})
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.UsageRow]{}, fmt.Errorf("%w: iterate usage: %v", controlplaneErrStorage(err), err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return paginateWithHasMore(items, limit, hasMore, shapeHashUsage(q), visibility, unsupported), nil
}

// UsageAggregate returns bounded usage aggregates (requirement 2.3, 6.4).
func (s *DurableStore) UsageAggregate(ctx context.Context, q cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashUsageAggregate(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageAggregate]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedUsageAggregateFilters(s.unsupportedFields, q)

	w := newWhereBuilder(s.dialect)
	w.eq("category", string(cp.CategoryUsage))
	w.applyCommon(q.Common, s.unsupportedFields)
	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC"

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.UsageAggregate]{}, fmt.Errorf("%w: query usage aggregate: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	aggMap := map[string]*cp.UsageAggregate{}
	var order []string
	seqFor := map[string]int64{}
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.UsageAggregate]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.UsageAggregate]{}, decodeErr
		}
		if ev.Usage() == nil {
			continue
		}
		key, a := aggregateRow(q.GroupBy, ev)
		if existing, ok := aggMap[key]; ok {
			mergeAggregate(existing, ev.Usage())
			if r.id > seqFor[key] {
				seqFor[key] = r.id
			}
			continue
		}
		aggMap[key] = a
		order = append(order, key)
		seqFor[key] = r.id
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.UsageAggregate]{}, fmt.Errorf("%w: iterate usage aggregate: %v", controlplaneErrStorage(err), err)
	}

	rowsList := make([]sequenced[cp.UsageAggregate], 0, len(order))
	for _, k := range order {
		rowsList = append(rowsList, sequenced[cp.UsageAggregate]{row: *aggMap[k], seq: seqFor[k]})
	}
	sortSeq(rowsList)
	resumed := resumeSeq(rowsList, cur.LastSeq)
	return paginate(resumed, limit, shapeHashUsageAggregate(q), visibility, unsupported), nil
}

// PolicyAudit returns bounded policy and audit rows (requirement 2.4, 9.3).
func (s *DurableStore) PolicyAudit(ctx context.Context, q cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashEvidence(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.PolicyAuditRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedEvidenceFilters(s.unsupportedFields, q)

	w := newWhereBuilder(s.dialect)
	if q.Category != "" && !isUnsupportedField(s.unsupportedFields, fields.EvidenceCategory) {
		w.eq("category", string(q.Category))
	} else {
		w.addRaw("(category = 'policy' OR category = 'audit')")
	}
	if q.Effect != "" && !isUnsupportedField(s.unsupportedFields, fields.EvidenceEffect) {
		w.eq("effect", q.Effect)
	}
	w.applyCommon(q.Common, s.unsupportedFields)
	if cur.LastSeq > 0 {
		w.addRaw("id > " + w.placeholder())
		w.args = append(w.args, cur.LastSeq)
	}
	sqlStr := "SELECT " + eventSelectColumns + " FROM control_plane_events" + w.clause() + " ORDER BY id ASC LIMIT " + w.placeholder()
	w.args = append(w.args, limit+1)

	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, w.args...)
	if err != nil {
		return cp.Page[cp.PolicyAuditRow]{}, fmt.Errorf("%w: query policy audit: %v", controlplaneErrStorage(err), err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]sequenced[cp.PolicyAuditRow], 0, limit)
	for rows.Next() {
		r, scanErr := scanEventRow(rows)
		if scanErr != nil {
			return cp.Page[cp.PolicyAuditRow]{}, scanErr
		}
		ev, decodeErr := eventFromRow(r)
		if decodeErr != nil {
			return cp.Page[cp.PolicyAuditRow]{}, decodeErr
		}
		if ev.Policy() == nil && ev.Audit() == nil {
			continue
		}
		items = append(items, sequenced[cp.PolicyAuditRow]{row: policyAuditRowFromEvent(ev), seq: r.id})
	}
	if err := rows.Err(); err != nil {
		return cp.Page[cp.PolicyAuditRow]{}, fmt.Errorf("%w: iterate policy audit: %v", controlplaneErrStorage(err), err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return paginateWithHasMore(items, limit, hasMore, shapeHashEvidence(q), visibility, unsupported), nil
}

// ApplyRetention marks or prunes records at or before the cutoff. Records
// remain in the table with explicit expired/redacted state and cleared
// detail_json so consumers can see the lifecycle state; repeated runs at the
// same cutoff affect no additional rows (requirement 6.1, 6.2, 6.6).
func (s *DurableStore) ApplyRetention(ctx context.Context, cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.RetentionResult{}, err
	}
	if !cmd.Profile.IsKnown() {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: unknown retention profile %q", controlplane.ErrInvalidQuery, cmd.Profile)
	}
	if cmd.Cutoff.IsZero() {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: retention cutoff is required", controlplane.ErrInvalidQuery)
	}

	newState := cp.EvidenceExpired
	newRedaction := cp.RedactionNone
	if cmd.Profile == controlplane.RetentionProfileStrict {
		newState = cp.EvidenceRedacted
		newRedaction = cp.RedactionRedacted
	}
	cutoffUnix := cmd.Cutoff.UnixNano()

	// A single UPDATE transitions the eligible rows and reports the count via
	// RowsAffected, avoiding a COUNT-then-UPDATE TOCTOU window (requirement 6.1).
	updateSQL := s.buildRetentionUpdate(newState, newRedaction)
	updateArgs := []any{string(newState), "{}", string(newRedaction), cutoffUnix}
	res, err := s.sqlDB.ExecContext(ctx, updateSQL, updateArgs...)
	if err != nil {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: apply retention: %v", controlplaneErrStorage(err), err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: apply retention rows affected: %v", controlplaneErrStorage(err), err)
	}
	return controlplane.RetentionResult{
		Marked: int(affected),
		Pruned: 0,
		Status: cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort},
	}, nil
}

// buildRetentionUpdate constructs the dialect-aware UPDATE statement for
// retention. It clears detail_json and summary, sets evidence_state and
// redaction_state, for rows at or before the cutoff that are not already
// expired/redacted (idempotence, requirement 6.1). Placeholder order matches
// the args slice built by ApplyRetention: newState, emptyDetail, newRedaction,
// cutoffUnix.
func (s *DurableStore) buildRetentionUpdate(newState cp.EvidenceState, newRedaction cp.RedactionState) string {
	notExpired := fmt.Sprintf("(evidence_state NOT IN ('%s','%s'))", cp.EvidenceExpired, cp.EvidenceRedacted)
	if s.dialect == dialect.PG {
		return fmt.Sprintf(
			"UPDATE control_plane_events SET evidence_state = $1, detail_json = $2, redaction_state = $3, summary = '' WHERE occurred_at_unix <= $4 AND %s",
			notExpired,
		)
	}
	return fmt.Sprintf(
		"UPDATE control_plane_events SET evidence_state = ?, detail_json = ?, redaction_state = ?, summary = '' WHERE occurred_at_unix <= ? AND %s",
		notExpired,
	)
}

// paginateWithHasMore builds a page when the caller has already fetched one
// extra row to determine whether a continuation cursor is needed. This avoids
// relying on count queries for has-more detection in durable stores.
func paginateWithHasMore[T any](items []sequenced[T], limit int, hasMore bool, shape uint64, visibility cp.Visibility, unsupported []cp.UnsupportedFilter) cp.Page[T] {
	out := make([]T, 0, len(items))
	for _, it := range items {
		out = append(out, it.row)
	}
	page := cp.Page[T]{
		Items:       out,
		Unsupported: unsupported,
		Visibility:  visibility,
	}
	if hasMore && len(out) > 0 {
		last := items[len(out)-1].seq
		page.Next = encodeCursor(cursorPayload{LastSeq: last, ShapeHash: shape, Visibility: string(visibility)})
	}
	return page
}

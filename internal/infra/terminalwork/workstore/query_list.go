package workstore

import (
	"strings"
)

func buildListQuery(storeID string, q Query, limit, offset int) (string, []any) {
	where := []string{"store_id = ?"}
	args := []any{storeID}

	if workID := strings.TrimSpace(q.WorkID); workID != "" {
		where = append(where, "work_id = ?")
		args = append(args, workID)
	}
	if err := q.SourceKey.Validate(); err == nil {
		where = append(where, "source_key = ?")
		args = append(args, q.SourceKey.String())
	}
	if q.State != "" {
		where = append(where, "state = ?")
		args = append(args, string(q.State))
	}
	if len(q.States) > 0 {
		placeholders := make([]string, 0, len(q.States))
		for _, st := range q.States {
			placeholders = append(placeholders, "?")
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	if provider := strings.TrimSpace(q.ProviderID); provider != "" {
		where = append(where, "provider_id = ?")
		args = append(args, provider)
	}
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(q.Kind))
	}
	if req := strings.TrimSpace(q.RequestID); req != "" {
		where = append(where, "request_id = ?")
		args = append(args, req)
	}
	if att := strings.TrimSpace(q.AttemptID); att != "" {
		where = append(where, "attempt_id = ?")
		args = append(args, att)
	}
	if !q.DueBefore.IsZero() {
		where = append(where, `(
			state = 'pending'
			OR (state = 'retry' AND next_retry_at_unix <= ?)
			OR (state = 'claimed' AND claim_expires_at_unix <= ?)
		)`)
		ns := q.DueBefore.UTC().UnixNano()
		args = append(args, ns, ns)
	}
	if !q.UpdatedAfter.IsZero() {
		where = append(where, "updated_at_unix >= ?")
		args = append(args, q.UpdatedAfter.UTC().UnixNano())
	}
	if !q.UpdatedBefore.IsZero() {
		where = append(where, "updated_at_unix <= ?")
		args = append(args, q.UpdatedBefore.UTC().UnixNano())
	}

	query := "SELECT * FROM economic_terminal_work WHERE " + strings.Join(where, " AND ") + " ORDER BY created_at_unix ASC, work_id ASC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	return query, args
}

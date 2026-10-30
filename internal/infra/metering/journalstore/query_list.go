package journalstore

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type factFilter struct {
	name   string
	value  string
	column string // non-empty means filter on metering_facts column directly (prefixed with f.)
}

func factFiltersForQuery(q metering.Query) []factFilter {
	out := make([]factFilter, 0, 20)
	add := func(name, value string) {
		if value != "" {
			out = append(out, factFilter{name: name, value: value})
		}
	}
	addCol := func(name, column, value string) {
		if value != "" {
			out = append(out, factFilter{name: name, value: value, column: column})
		}
	}
	addCol("stream_id", "stream_id", strings.TrimSpace(q.StreamID))
	addCol("request_id", "request_id", strings.TrimSpace(q.RequestID))
	addCol("frontend_id", "frontend_id", strings.TrimSpace(q.FrontendID))
	addCol("backend_id", "backend_id", strings.TrimSpace(q.BackendID))
	addCol("model", "model", strings.TrimSpace(q.Model))
	addCol("perspective", "perspective", string(q.Perspective))
	addCol("boundary", "boundary", string(q.Boundary))
	addCol("lifecycle", "lifecycle_scope", string(q.Lifecycle))
	add("source", string(q.Source))
	add("authority", string(q.Authority))
	if q.IdentityVersion != 0 {
		add("identity_version", strconv.Itoa(q.IdentityVersion))
	}
	add("a_leg_id", strings.TrimSpace(q.ALegID))
	add("b_leg_id", strings.TrimSpace(q.BLegID))
	add("attempt_id", strings.TrimSpace(q.AttemptID))
	add("trace_id", strings.TrimSpace(q.TraceID))
	add("session_id", strings.TrimSpace(q.SessionID))
	add("rule_id", strings.TrimSpace(q.RuleID))
	addScope := func(name string, v scope.Value) {
		if v.IsKnown() && strings.TrimSpace(v.Value) != "" {
			add(name, strings.TrimSpace(v.Value))
		}
	}
	addScope("principal_id", q.Scope.PrincipalID)
	addScope("credential_id", q.Scope.CredentialID)
	addScope("tenant_id", q.Scope.TenantID)
	addScope("organization_id", q.Scope.OrganizationID)
	addScope("workspace_id", q.Scope.WorkspaceID)
	addScope("project_id", q.Scope.ProjectID)
	addScope("department_id", q.Scope.DepartmentID)
	addScope("cost_center_id", q.Scope.CostCenterID)
	return out
}

func buildDurableListQuery(storeID string, q metering.Query, limit, offset int) (string, []any) {
	filters := factFiltersForQuery(q)
	where := []string{"f.store_id = ?", "COALESCE(f.payload_kind, 'fact') = 'fact'"}
	args := []any{storeID}

	for _, filter := range filters {
		if filter.column != "" {
			where = append(where, "f."+filter.column+" = ?")
			args = append(args, filter.value)
			continue
		}
		where = append(where, `EXISTS (SELECT 1 FROM metering_fact_filters ff WHERE ff.store_id = f.store_id AND ff.fact_id = f.fact_id AND ff.stream_id = f.stream_id AND ff.field_name = ? AND ff.field_value = ?)`)
		args = append(args, filter.name, filter.value)
	}
	if !q.TimeRange.From.IsZero() {
		where = append(where, "f.recorded_at_unix >= ?")
		args = append(args, q.TimeRange.From.UTC().UnixNano())
	}
	if !q.TimeRange.To.IsZero() {
		where = append(where, "f.recorded_at_unix <= ?")
		args = append(args, q.TimeRange.To.UTC().UnixNano())
	}

	query := fmt.Sprintf(`
SELECT f.payload_json FROM metering_facts f
WHERE %s
ORDER BY f.stream_id ASC, f.sequence ASC
LIMIT ? OFFSET ?
`, strings.Join(where, " AND "))
	args = append(args, limit+1, offset)
	return query, args
}

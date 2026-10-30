package ledgerstore

import (
	"sort"
	"strings"
	"time"

	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// sequenced pairs a projected row with the store sequence of its source event
// so query pagination can resume deterministically without adding SDK public
// surface for sequence fields (requirement 2.7).
type sequenced[T any] struct {
	row T
	seq int64
}

// resumeSeq returns the subset of items whose sequence is strictly greater than
// lastSeq. lastSeq == 0 means "from the start".
func resumeSeq[T any](items []sequenced[T], lastSeq int64) []sequenced[T] {
	if lastSeq == 0 {
		return items
	}
	for i, it := range items {
		if it.seq > lastSeq {
			return items[i:]
		}
	}
	return nil
}

// paginate builds a bounded page from sequenced rows, applying the limit and
// emitting a continuation cursor bound to the query shape hash when more rows
// remain. The last emitted sequence drives the cursor (requirement 2.6, 2.7).
func paginate[T any](items []sequenced[T], limit int, shape uint64, visibility cp.Visibility, unsupported []cp.UnsupportedFilter) cp.Page[T] {
	out := make([]T, 0, limit)
	for i, it := range items {
		if i >= limit {
			break
		}
		out = append(out, it.row)
	}
	page := cp.Page[T]{
		Items:       out,
		Unsupported: unsupported,
		Visibility:  visibility,
	}
	if len(items) > limit && len(out) > 0 {
		last := items[len(out)-1].seq
		page.Next = encodeCursor(cursorPayload{LastSeq: last, ShapeHash: shape, Visibility: string(visibility)})
	}
	return page
}

// ---- sessions ----

type sessionGroup struct {
	sessionID     string
	lastActivity  time.Time
	scope         cp.ScopeSnapshot
	scopeSeq      int64
	fallbackScope cp.ScopeSnapshot
	fallbackSeq   int64
	usage         cp.UsageTotals
	attempts      int
	state         cp.EvidenceState
	maxSeq        int64
}

// groupSessionsLocked walks recorded events under the read lock, applies common
// filters (skipping any field the store declares unsupported), and groups them
// by session id. Events without a session id are skipped. The session's scope
// is taken from the highest-sequence event that carries a session detail,
// falling back to the highest-sequence event in the group (requirement 2.1,
// 3.1, 2.5).
func (s *MemoryStore) groupSessionsLocked(common cp.CommonFilters, unsupported map[string]struct{}) map[string]*sessionGroup {
	groups := map[string]*sessionGroup{}
	for _, se := range s.events {
		ev := se.event
		if ev.Correlation.SessionID == "" {
			continue
		}
		if !commonFiltersMatch(common, ev, unsupported) {
			continue
		}
		g, ok := groups[ev.Correlation.SessionID]
		if !ok {
			g = &sessionGroup{sessionID: ev.Correlation.SessionID, state: cp.EvidenceRecorded}
			groups[ev.Correlation.SessionID] = g
		}
		if ev.RecordedAt.After(g.lastActivity) {
			g.lastActivity = ev.RecordedAt
		}
		if se.seq > g.maxSeq {
			g.maxSeq = se.seq
		}
		if ev.Session() != nil {
			if se.seq > g.scopeSeq {
				g.scope = ev.Scope
				g.scopeSeq = se.seq
			}
		} else if se.seq > g.fallbackSeq {
			g.fallbackScope = ev.Scope
			g.fallbackSeq = se.seq
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
	return groups
}

func (g *sessionGroup) toSummary() sequenced[cp.SessionSummary] {
	var totals *cp.UsageTotals
	if g.usage.TotalTokens != 0 || g.usage.InputTokens != 0 || g.usage.OutputTokens != 0 || g.usage.CostNanoUnits != 0 {
		totals = &cp.UsageTotals{
			InputTokens:   g.usage.InputTokens,
			OutputTokens:  g.usage.OutputTokens,
			TotalTokens:   g.usage.TotalTokens,
			CostNanoUnits: g.usage.CostNanoUnits,
		}
	}
	scope := g.scope
	if g.scopeSeq == 0 {
		scope = g.fallbackScope
	}
	return sequenced[cp.SessionSummary]{
		row: cp.SessionSummary{
			SessionID:     g.sessionID,
			LastActivity:  g.lastActivity,
			Scope:         scope,
			UsageTotals:   totals,
			AttemptCount:  g.attempts,
			EvidenceState: g.state,
		},
		seq: g.maxSeq,
	}
}

// ---- attempts ----

func attemptRowFromEvent(ev cp.Event) cp.AttemptRow {
	a := ev.Attempt()
	if a == nil {
		return cp.AttemptRow{Correlation: ev.Correlation, EvidenceState: ev.EvidenceState}
	}
	row := cp.AttemptRow{
		Correlation:   ev.Correlation,
		BackendID:     a.BackendID,
		Model:         a.Model,
		RouteOutcome:  a.RouteOutcome,
		Surfaced:      a.Surfaced,
		Outcome:       a.Outcome,
		ErrorClass:    a.ErrorClass,
		StartedAt:     a.StartedAt,
		FinishedAt:    a.FinishedAt,
		EvidenceState: ev.EvidenceState,
	}
	if row.EvidenceState == "" {
		row.EvidenceState = cp.EvidenceRecorded
	}
	return row
}

// ---- usage ----

func usageRowFromEvent(ev cp.Event) cp.UsageRow {
	return corecp.UsageRowFromEvent(ev)
}

// ---- usage aggregate ----

func aggregateRow(groupBy []string, ev cp.Event) (string, *cp.UsageAggregate) {
	u := ev.Usage()
	if u == nil {
		return "", &cp.UsageAggregate{EvidenceState: ev.EvidenceState}
	}
	parts := make([]string, 0, len(groupBy))
	agg := &cp.UsageAggregate{
		Plane:          u.Plane,
		Perspective:    u.Perspective,
		Boundary:       u.Boundary,
		LifecycleScope: u.LifecycleScope,
		InputTokens:    int64(u.InputTokens),
		OutputTokens:   int64(u.OutputTokens),
		TotalTokens:    int64(u.TotalTokens),
		CostNanoUnits:  u.CostNanoUnits,
		EvidenceState:  ev.EvidenceState,
	}
	if agg.EvidenceState == "" {
		agg.EvidenceState = cp.EvidenceRecorded
	}
	for _, g := range groupBy {
		switch g {
		case "backend":
			agg.BackendID = ev.Correlation.BackendID
			parts = append(parts, "backend="+ev.Correlation.BackendID)
		case "model":
			agg.Model = ev.Correlation.Model
			parts = append(parts, "model="+ev.Correlation.Model)
		case "plane":
			parts = append(parts, "plane="+string(u.Plane))
		case "principal":
			agg.Scope = ev.Scope
			parts = append(parts, "principal="+ev.Scope.PrincipalID.String())
		case "tenant":
			agg.Scope = ev.Scope
			parts = append(parts, "tenant="+ev.Scope.TenantID.String())
		case "workspace":
			agg.Scope = ev.Scope
			parts = append(parts, "workspace="+ev.Scope.WorkspaceID.String())
		case "project":
			agg.Scope = ev.Scope
			parts = append(parts, "project="+ev.Scope.ProjectID.String())
		case "organization":
			agg.Scope = ev.Scope
			parts = append(parts, "organization="+ev.Scope.OrganizationID.String())
		case "department":
			agg.Scope = ev.Scope
			parts = append(parts, "department="+ev.Scope.DepartmentID.String())
		case "cost_center":
			agg.Scope = ev.Scope
			parts = append(parts, "cost_center="+ev.Scope.CostCenterID.String())
		case "credential":
			agg.Scope = ev.Scope
			parts = append(parts, "credential="+ev.Scope.CredentialID.String())
		}
	}
	return strings.Join(parts, "|"), agg
}

func mergeAggregate(dst *cp.UsageAggregate, u *cp.UsageDetail) {
	dst.InputTokens += int64(u.InputTokens)
	dst.OutputTokens += int64(u.OutputTokens)
	dst.TotalTokens += int64(u.TotalTokens)
	dst.CostNanoUnits += u.CostNanoUnits
}

// ---- policy/audit ----

func policyAuditRowFromEvent(ev cp.Event) cp.PolicyAuditRow {
	row := cp.PolicyAuditRow{
		Correlation:    ev.Correlation,
		Category:       ev.Category,
		OccurredAt:     ev.OccurredAt,
		Visibility:     ev.Visibility,
		RedactionState: ev.RedactionState,
		EvidenceState:  ev.EvidenceState,
	}
	if ev.Policy() != nil {
		row.Stage = ev.Policy().Stage
		row.Outcome = ev.Policy().Outcome
		row.Effect = ev.Policy().Effect
		row.ReasonCode = ev.Policy().ReasonCode
	}
	if ev.Audit() != nil {
		row.Stage = "audit"
		row.Outcome = ev.Audit().Action
		row.ReasonCode = ev.Audit().ReasonCode
	}
	if row.EvidenceState == "" {
		row.EvidenceState = cp.EvidenceRecorded
	}
	if row.RedactionState == "" {
		row.RedactionState = cp.RedactionNone
	}
	return row
}

// sortSeq stabilises any sequenced slice by store sequence (requirement 2.7:
// deterministic ordering).
func sortSeq[T any](items []sequenced[T]) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].seq < items[j].seq })
}

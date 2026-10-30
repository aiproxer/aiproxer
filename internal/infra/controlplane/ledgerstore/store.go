package ledgerstore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/fields"
	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable control-plane stores
)

// DurableConfig configures a Bun-backed durable control-plane event store.
type DurableConfig struct {
	// StoreID is the stable store identifier embedded in assigned EventIDs.
	StoreID string
	// DefaultPageSize is applied when a query omits Limit. Defaults to 100.
	DefaultPageSize int
	// MaxPageSize is the upper bound for any query page. Defaults to 500.
	MaxPageSize int
	// UnsupportedFilters is the set of canonical filter field names this store
	// cannot apply (see internal/infra/controlplane/contract). Empty means all
	// documented filters are supported (requirement 2.5, 8.6, 9.4).
	UnsupportedFilters []string
	// MaxDetailBytes bounds detail_json before persistence. Defaults to 16384.
	MaxDetailBytes int
	// MaxScopeBytes bounds scope_json before persistence. Defaults to 16384.
	MaxScopeBytes int
	// MaxSummaryBytes bounds the summary string. Defaults to MaxSummaryBytes
	// from core validation (4096).
	MaxSummaryBytes int
}

// DurableStore persists control-plane events through Bun-supported SQL
// dialects (SQLite and Postgres). It implements the core-owned Store port
// without leaking SQL, Bun, DSN, driver, or infrastructure types into core or
// SDK contracts (requirement 9.5, 7.3).
type DurableStore struct {
	cfg               DurableConfig
	db                *bun.DB
	sqlDB             *sql.DB
	dialect           dialect.Name
	defaultPageSize   int
	maxPageSize       int
	unsupportedFields map[string]struct{}
	maxDetailBytes    int
	maxScopeBytes     int
	maxSummaryBytes   int
}

// NewDurableStore runs schema migrations against db and returns a durable
// store. The caller owns closing db; Close on the store closes the bun handle.
func NewDurableStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ledgerstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("ledgerstore: nil bun db")
	}
	if cfg.StoreID == "" {
		return nil, fmt.Errorf("ledgerstore: durable store id is required")
	}
	if err := runControlPlaneSchemaMigrate(ctx, db); err != nil {
		return nil, fmt.Errorf("ledgerstore: migrate: %w", err)
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if maxPageSize < def {
		return nil, fmt.Errorf("ledgerstore: max page size %d < default %d", maxPageSize, def)
	}
	maxDetail := cfg.MaxDetailBytes
	if maxDetail <= 0 {
		maxDetail = 16384
	}
	maxScope := cfg.MaxScopeBytes
	if maxScope <= 0 {
		maxScope = 16384
	}
	maxSummary := cfg.MaxSummaryBytes
	if maxSummary <= 0 {
		maxSummary = controlplane.MaxSummaryBytes
	}
	unsup := make(map[string]struct{}, len(cfg.UnsupportedFilters))
	for _, f := range cfg.UnsupportedFilters {
		unsup[f] = struct{}{}
	}
	return &DurableStore{
		cfg:               cfg,
		db:                db,
		sqlDB:             db.DB,
		dialect:           db.Dialect().Name(),
		defaultPageSize:   def,
		maxPageSize:       maxPageSize,
		unsupportedFields: unsup,
		maxDetailBytes:    maxDetail,
		maxScopeBytes:     maxScope,
		maxSummaryBytes:   maxSummary,
	}, nil
}

// Close closes the underlying bun handle.
func (s *DurableStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CheckReadiness pings the backing database and reports availability without
// leaking raw infrastructure errors (requirement 7.1, 7.3).
func (s *DurableStore) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.sqlDB == nil {
		return fmt.Errorf("%w: backing database unavailable", controlplane.ErrUnavailable)
	}
	if err := s.sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: backing database not reachable", controlplane.ErrUnavailable)
	}
	return nil
}

// ---- append ----

// insertColumns lists the columns written by Append in placeholder order.
const insertColumns = `store_id, source_event_key, category,
	occurred_at_unix, recorded_at_unix,
	trace_id, request_id, session_id, a_leg_id, b_leg_id, attempt_seq,
	frontend_id, backend_id, model, parent_trace_id,
	outcome, effect, reason_code, visibility,
	surfaced, usage_plane, usage_availability,
	evidence_state, redaction_state,
	source_name, source_version, summary, summary_json, scope_json, detail_json,
	principal_known, principal_value, credential_known, credential_value,
	tenant_known, tenant_value, organization_known, organization_value,
	workspace_known, workspace_value, project_known, project_value,
	department_known, department_value, cost_center_known, cost_center_value`

// Append persists one validated event atomically with its safe detail and scope
// snapshots, returning a stable record result for new and deduplicated source
// events (requirement 1.7, 1.8, 4.4, 4.5, 9.5).
func (s *DurableStore) Append(ctx context.Context, ev cp.Event) (cp.RecordResult, error) {
	if err := ctx.Err(); err != nil {
		return cp.RecordResult{}, err
	}
	if err := controlplane.ValidateEvent(ev); err != nil {
		return cp.RecordResult{}, fmt.Errorf("%w: %v", controlplane.ErrUnsafeEvidence, err)
	}
	if len(ev.Summary) > s.maxSummaryBytes {
		return cp.RecordResult{}, fmt.Errorf("%w: summary exceeds %d bytes", controlplane.ErrUnsafeEvidence, s.maxSummaryBytes)
	}

	scopeJSON, err := encodeScopeJSON(ev.Scope)
	if err != nil {
		return cp.RecordResult{}, fmt.Errorf("ledgerstore: marshal scope: %w", err)
	}
	if len(scopeJSON) > s.maxScopeBytes {
		return cp.RecordResult{}, fmt.Errorf("%w: scope_json exceeds %d bytes", controlplane.ErrUnsafeEvidence, s.maxScopeBytes)
	}
	detailJSON, err := encodeDetailJSON(ev)
	if err != nil {
		return cp.RecordResult{}, fmt.Errorf("ledgerstore: marshal detail: %w", err)
	}
	if len(detailJSON) > s.maxDetailBytes {
		return cp.RecordResult{}, fmt.Errorf("%w: detail_json exceeds %d bytes", controlplane.ErrUnsafeEvidence, s.maxDetailBytes)
	}

	if ev.SourceEventKey != "" {
		if existingID, ok, err := s.selectIDBySourceKey(ctx, ev.SourceEventKey); err != nil {
			return cp.RecordResult{}, err
		} else if ok {
			existing, err := s.selectEventByID(ctx, existingID)
			if err != nil {
				return cp.RecordResult{}, err
			}
			return cp.RecordResult{ID: existingID, Dedupe: cp.DedupeDuplicate, RecordedAt: existing.RecordedAt}, nil
		}
	}

	if ev.RecordedAt.IsZero() {
		ev.RecordedAt = ev.OccurredAt
	}
	if ev.RecordedAt.Before(ev.OccurredAt) {
		ev.RecordedAt = ev.OccurredAt
	}

	outcome, effect, reasonCode, surfaced, plane, availability := extractDetailColumns(ev)
	pkKnown, pkVal := scopeDimCols(ev.Scope.PrincipalID)
	ckKnown, ckVal := scopeDimCols(ev.Scope.CredentialID)
	tkKnown, tkVal := scopeDimCols(ev.Scope.TenantID)
	okKnown, okVal := scopeDimCols(ev.Scope.OrganizationID)
	wkKnown, wkVal := scopeDimCols(ev.Scope.WorkspaceID)
	prkKnown, prkVal := scopeDimCols(ev.Scope.ProjectID)
	dkKnown, dkVal := scopeDimCols(ev.Scope.DepartmentID)
	cckKnown, cckVal := scopeDimCols(ev.Scope.CostCenterID)

	args := []any{
		s.cfg.StoreID, ev.SourceEventKey, string(ev.Category),
		ev.OccurredAt.UnixNano(), ev.RecordedAt.UnixNano(),
		ev.Correlation.TraceID, ev.Correlation.RequestID, ev.Correlation.SessionID,
		ev.Correlation.ALegID, ev.Correlation.BLegID, ev.Correlation.AttemptSeq,
		ev.Correlation.FrontendID, ev.Correlation.BackendID, ev.Correlation.Model, ev.Correlation.ParentTraceID,
		outcome, effect, reasonCode, string(ev.Visibility),
		surfaced, plane, availability,
		string(ev.EvidenceState), string(ev.RedactionState),
		ev.Source.Name, ev.Source.Version, ev.Summary, "{}", scopeJSON, detailJSON,
		s.knownArg(pkKnown), pkVal, s.knownArg(ckKnown), ckVal,
		s.knownArg(tkKnown), tkVal, s.knownArg(okKnown), okVal,
		s.knownArg(wkKnown), wkVal, s.knownArg(prkKnown), prkVal,
		s.knownArg(dkKnown), dkVal, s.knownArg(cckKnown), cckVal,
	}

	placeholderList := s.placeholderList(len(args))
	sqlStr := "INSERT INTO control_plane_events(" + insertColumns + ") VALUES(" + placeholderList + ") RETURNING id"
	rows, err := s.sqlDB.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		if dbinfra.IsUniqueViolation(err) && ev.SourceEventKey != "" {
			if existingID, ok, lookupErr := s.selectIDBySourceKey(ctx, ev.SourceEventKey); lookupErr == nil && ok {
				existing, readErr := s.selectEventByID(ctx, existingID)
				if readErr != nil {
					return cp.RecordResult{}, readErr
				}
				return cp.RecordResult{ID: existingID, Dedupe: cp.DedupeDuplicate, RecordedAt: existing.RecordedAt}, nil
			}
		}
		return cp.RecordResult{}, fmt.Errorf("insert event: %w", controlplaneErrStorage(err))
	}
	var id int64
	if !rows.Next() {
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return cp.RecordResult{}, fmt.Errorf("insert event: %w", controlplaneErrStorage(err))
		}
		return cp.RecordResult{}, fmt.Errorf("ledgerstore: insert returned no id")
	}
	if err := rows.Scan(&id); err != nil {
		_ = rows.Close()
		return cp.RecordResult{}, fmt.Errorf("ledgerstore: scan inserted id: %w", err)
	}
	if err := rows.Close(); err != nil {
		return cp.RecordResult{}, fmt.Errorf("ledgerstore: close insert rows: %w", err)
	}
	return cp.RecordResult{
		ID:         cp.EventID{StoreID: s.cfg.StoreID, Sequence: id},
		Dedupe:     cp.DedupeInserted,
		RecordedAt: ev.RecordedAt,
	}, nil
}

func (s *DurableStore) selectIDBySourceKey(ctx context.Context, key string) (cp.EventID, bool, error) {
	rows, err := s.sqlDB.QueryContext(ctx, s.queryPrefix()+"SELECT id FROM control_plane_events WHERE source_event_key = "+s.placeholder(1), key)
	if err != nil {
		return cp.EventID{}, false, fmt.Errorf("ledgerstore: select source key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cp.EventID{}, false, fmt.Errorf("ledgerstore: select source key: %w", controlplaneErrStorage(err))
		}
		return cp.EventID{}, false, nil
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		return cp.EventID{}, false, fmt.Errorf("ledgerstore: scan source key id: %w", err)
	}
	return cp.EventID{StoreID: s.cfg.StoreID, Sequence: id}, true, nil
}

func (s *DurableStore) selectEventByID(ctx context.Context, id cp.EventID) (cp.Event, error) {
	rows, err := s.sqlDB.QueryContext(ctx, s.queryPrefix()+"SELECT "+eventSelectColumns+" FROM control_plane_events WHERE id = "+s.placeholder(1), id.Sequence)
	if err != nil {
		return cp.Event{}, fmt.Errorf("ledgerstore: select by id: %w", controlplaneErrStorage(err))
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cp.Event{}, fmt.Errorf("ledgerstore: select by id: %w", controlplaneErrStorage(err))
		}
		return cp.Event{}, fmt.Errorf("ledgerstore: dedupe winner missing: %w", controlplane.ErrDegraded)
	}
	r, err := scanEventRow(rows)
	if err != nil {
		return cp.Event{}, controlplaneErrStorage(err)
	}
	ev, err := eventFromRow(r)
	if err != nil {
		return cp.Event{}, controlplaneErrStorage(err)
	}
	return ev, nil
}

// queryPrefix is a no-op marker; kept for symmetry with select helpers so all
// selects route through one place.
func (s *DurableStore) queryPrefix() string { return "" }

func (s *DurableStore) placeholder(n int) string {
	if s.dialect == dialect.PG {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

func (s *DurableStore) placeholderList(count int) string {
	if s.dialect == dialect.PG {
		parts := make([]string, count)
		for i := range count {
			parts[i] = "$" + strconv.Itoa(i+1)
		}
		return strings.Join(parts, ", ")
	}
	return strings.Repeat("?, ", count-1) + "?"
}

// knownArg adapts a canonical 0/1 known flag to the dialect-aware bind value for
// boolean presence columns. Postgres *_known columns are BOOLEAN and reject
// integer bindings, so they bind as bool; SQLite keeps INTEGER (requirement
// 4.3, 9.5: dialect differences stay in the adapter).
func (s *DurableStore) knownArg(known int) any {
	if s.dialect == dialect.PG {
		return known != 0
	}
	return known
}

// extractDetailColumns pulls the filterable detail-derived columns from the
// single typed detail block so SQL WHERE clauses can filter without parsing
// JSON (requirement 2.5).
func extractDetailColumns(ev cp.Event) (outcome, effect, reasonCode, surfaced, plane, availability string) {
	if ev.Attempt() != nil {
		surfaced = string(ev.Attempt().Surfaced)
		outcome = string(ev.Attempt().Outcome)
	}
	if ev.Auth() != nil {
		outcome = ev.Auth().Outcome
		reasonCode = ev.Auth().ReasonCode
	}
	if ev.Policy() != nil {
		outcome = ev.Policy().Outcome
		effect = ev.Policy().Effect
		reasonCode = ev.Policy().ReasonCode
	}
	if ev.Audit() != nil {
		outcome = ev.Audit().Action
		reasonCode = ev.Audit().ReasonCode
	}
	if ev.Usage() != nil {
		plane = string(ev.Usage().Plane)
		availability = string(ev.Usage().Availability)
	}
	return
}

// controlplaneErrStorage wraps a storage error with a stable degraded
// classification so callers can distinguish infrastructure failures from
// control-plane validation failures (requirement 7.3).
func controlplaneErrStorage(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", controlplane.ErrDegraded, err)
}

// ---- queries ----

// whereBuilder accumulates WHERE clauses with dialect-aware placeholders.
type whereBuilder struct {
	dialect dialect.Name
	clauses []string
	args    []any
	n       int
}

func newWhereBuilder(d dialect.Name) *whereBuilder { return &whereBuilder{dialect: d} }

func (w *whereBuilder) eq(column string, arg any) {
	w.clauses = append(w.clauses, column+" = "+w.placeholder())
	w.args = append(w.args, arg)
}

func (w *whereBuilder) gte(column string, arg any) {
	w.clauses = append(w.clauses, column+" >= "+w.placeholder())
	w.args = append(w.args, arg)
}

func (w *whereBuilder) lt(column string, arg any) {
	w.clauses = append(w.clauses, column+" < "+w.placeholder())
	w.args = append(w.args, arg)
}

func (w *whereBuilder) lte(column string, arg any) {
	w.clauses = append(w.clauses, column+" <= "+w.placeholder())
	w.args = append(w.args, arg)
}

func (w *whereBuilder) addRaw(clause string) { w.clauses = append(w.clauses, clause) }

// placeholder allocates and returns the next dialect-aware placeholder. It
// advances the counter so raw clauses and trailing LIMIT placeholders stay
// correctly numbered even when no eq/gte/lt/lte clause preceded them. Postgres
// placeholders are 1-indexed, so the first call yields $1 (requirement 2.5).
func (w *whereBuilder) placeholder() string {
	w.n++
	if w.dialect == dialect.PG {
		return "$" + strconv.Itoa(w.n)
	}
	return "?"
}

func (w *whereBuilder) clause() string {
	if len(w.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.clauses, " AND ")
}

// applyCommonWhere adds correlation, time, and scope-dimension filters. Scope
// dimensions: known filter constrains (known=1, value=?); unknown filter is
// unconstrained (requirement 4.3). Scope filters that the store does not
// support are skipped here and reported via Page.Unsupported by the caller.
func (w *whereBuilder) applyCommon(common cp.CommonFilters, unsupported map[string]struct{}) {
	if common.BackendID != "" && !isUnsupportedField(unsupported, fields.BackendID) {
		w.eq("backend_id", common.BackendID)
	}
	if common.Model != "" && !isUnsupportedField(unsupported, fields.Model) {
		w.eq("model", common.Model)
	}
	if common.FrontendID != "" && !isUnsupportedField(unsupported, fields.FrontendID) {
		w.eq("frontend_id", common.FrontendID)
	}
	if common.TraceID != "" && !isUnsupportedField(unsupported, fields.TraceID) {
		w.eq("trace_id", common.TraceID)
	}
	if common.SessionID != "" && !isUnsupportedField(unsupported, fields.SessionID) {
		w.eq("session_id", common.SessionID)
	}
	if common.ALegID != "" && !isUnsupportedField(unsupported, fields.ALegID) {
		w.eq("a_leg_id", common.ALegID)
	}
	if common.BLegID != "" && !isUnsupportedField(unsupported, fields.BLegID) {
		w.eq("b_leg_id", common.BLegID)
	}
	if common.Outcome != "" && !isUnsupportedField(unsupported, fields.Outcome) {
		w.eq("outcome", common.Outcome)
	}
	if common.ReasonCode != "" && !isUnsupportedField(unsupported, fields.ReasonCode) {
		w.eq("reason_code", common.ReasonCode)
	}
	if (!common.TimeRange.From.IsZero() || !common.TimeRange.To.IsZero()) && !isUnsupportedField(unsupported, fields.TimeRange) {
		if !common.TimeRange.From.IsZero() {
			w.gte("occurred_at_unix", common.TimeRange.From.UnixNano())
		}
		if !common.TimeRange.To.IsZero() {
			w.lt("occurred_at_unix", common.TimeRange.To.UnixNano())
		}
	}
	w.applyScope(common.Scope, unsupported)
}

func (w *whereBuilder) applyScope(sf cp.ScopeFilters, unsupported map[string]struct{}) {
	w.applyScopeDim("principal", sf.PrincipalID, fields.ScopePrincipalID, unsupported)
	w.applyScopeDim("credential", sf.CredentialID, fields.ScopeCredentialID, unsupported)
	w.applyScopeDim("tenant", sf.TenantID, fields.ScopeTenantID, unsupported)
	w.applyScopeDim("organization", sf.OrganizationID, fields.ScopeOrganizationID, unsupported)
	w.applyScopeDim("workspace", sf.WorkspaceID, fields.ScopeWorkspaceID, unsupported)
	w.applyScopeDim("project", sf.ProjectID, fields.ScopeProjectID, unsupported)
	w.applyScopeDim("department", sf.DepartmentID, fields.ScopeDepartmentID, unsupported)
	w.applyScopeDim("cost_center", sf.CostCenterID, fields.ScopeCostCenterID, unsupported)
}

func (w *whereBuilder) applyScopeDim(prefix string, v scope.Value, field string, unsupported map[string]struct{}) {
	if v.IsUnknown() {
		return
	}
	if isUnsupportedField(unsupported, field) {
		return
	}
	w.eqKnown(prefix+"_known", 1)
	w.eq(prefix+"_value", v.Value)
}

// eqKnown binds a 0/1 known flag to a boolean presence column, adapting the
// value to the dialect: Postgres *_known columns are BOOLEAN and reject integer
// bindings, while SQLite keeps INTEGER (requirement 4.3).
func (w *whereBuilder) eqKnown(column string, known int) {
	var arg any
	if w.dialect == dialect.PG {
		arg = known != 0
	} else {
		arg = known
	}
	w.eq(column, arg)
}

// Compile-time assertion that DurableStore satisfies the core-owned Store port.
var _ controlplane.Store = (*DurableStore)(nil)

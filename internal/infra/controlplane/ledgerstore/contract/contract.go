// Package contract provides a reusable store-contract test suite for control-plane
// event stores (spec control-plane-persistence-query-event-ledger, tasks 2.1–2.5).
//
// Both the in-memory store and the Bun-backed durable store must satisfy the
// same core-owned [controlplane.Store] port. This package expresses the shared
// behavioral contract as a set of table-driven test functions that adapters
// invoke from their own _test.go files, passing a [Factory] that builds a fresh
// store instance. The contract covers append, source-event-key dedupe,
// readiness, deterministic ordering, query filters, pagination/continuation,
// retention/redaction, and unsupported-filter reporting (requirements 1.7,
// 1.8, 2.1–2.9, 3.1, 3.4, 3.5, 4.2, 4.3, 6.1–6.6, 7.1, 7.3, 8.5, 9.5).
package contract

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/fields"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// FilterField names re-export the canonical strings owned by the fields
// package so contract tests and adapters agree on the values reported in
// cp.UnsupportedFilter.Field across adapters (requirement 2.5, 8.6, 9.4).
// The fields package is the single source of truth; these aliases keep the
// existing contract API stable for adapters that reference contract.Field*.
const (
	FieldScopePrincipalID    = fields.ScopePrincipalID
	FieldScopeCredentialID   = fields.ScopeCredentialID
	FieldScopeTenantID       = fields.ScopeTenantID
	FieldScopeOrganizationID = fields.ScopeOrganizationID
	FieldScopeWorkspaceID    = fields.ScopeWorkspaceID
	FieldScopeProjectID      = fields.ScopeProjectID
	FieldScopeDepartmentID   = fields.ScopeDepartmentID
	FieldScopeCostCenterID   = fields.ScopeCostCenterID

	FieldTimeRange  = fields.TimeRange
	FieldBackendID  = fields.BackendID
	FieldModel      = fields.Model
	FieldFrontendID = fields.FrontendID
	FieldTraceID    = fields.TraceID
	FieldSessionID  = fields.SessionID
	FieldALegID     = fields.ALegID
	FieldBLegID     = fields.BLegID
	FieldOutcome    = fields.Outcome
	FieldReasonCode = fields.ReasonCode

	FieldAttemptSurfaced   = fields.AttemptSurfaced
	FieldUsagePlane        = fields.UsagePlane
	FieldUsageAvailability = fields.UsageAvailability
	FieldEvidenceEffect    = fields.EvidenceEffect
	FieldEvidenceCategory  = fields.EvidenceCategory
	FieldEventCategory     = fields.EventCategory
	FieldUsageGroupBy      = fields.UsageGroupBy
)

// Factory builds a fresh, empty store for one test case and registers its
// cleanup with t. The returned store must be ready (CheckReadiness == nil) and
// empty. Tests use a factory rather than a shared instance so they remain
// independent and deterministic.
//
// A Factory may optionally implement [FactoryWithUnsupported] to declare which
// filter fields the built store cannot apply, so the contract can exercise
// explicit unsupported-filter reporting (requirement 2.5, 8.6, 9.4).
type Factory interface {
	Build(t *testing.T) (store controlplane.Store)
}

// FactoryParallelism lets adapters opt out of the contract's parallel subtests.
// Most in-process stores should run in parallel. External databases may need
// serial construction/cleanup to keep one shared integration schema isolated.
type FactoryParallelism interface {
	ParallelContract() bool
}

// FactoryWithUnsupported is an optional interface a Factory may implement when
// the built store is configured to reject a known set of filter fields. The
// contract uses it to drive the unsupported-filter assertions.
type FactoryWithUnsupported interface {
	UnsupportedConfig() UnsupportedConfig
}

// UnsupportedConfig lets a store factory declare which filter fields the store
// cannot apply, so the contract can exercise explicit unsupported-filter
// reporting (requirement 2.5, 8.6, 9.4). Stores that support every documented
// filter return an empty config.
type UnsupportedConfig struct {
	Fields []string
}

// IsUnsupported reports whether field is in the unsupported set.
func (c UnsupportedConfig) IsUnsupported(field string) bool {
	return slices.Contains(c.Fields, field)
}

// RunSuite runs the full shared store contract against the supplied factory.
// Adapters call this from their own _test.go files so memory and durable
// adapters share one behavioral definition (tasks 2.1, 2.3, 2.4, 2.5).
func RunSuite(t *testing.T, f Factory) {
	t.Helper()
	t.Run("AppendDedupeOrdering", func(t *testing.T) { testAppendDedupeOrdering(t, f) })
	t.Run("Readiness", func(t *testing.T) { testReadiness(t, f) })
	t.Run("EmptyResults", func(t *testing.T) { testEmptyResults(t, f) })
	t.Run("ScopePresencePreserved", func(t *testing.T) { testScopePresencePreserved(t, f) })
	t.Run("ScopeFiltersKnownValueAndEmpty", func(t *testing.T) { testScopeFiltersKnownValueAndEmpty(t, f) })
	t.Run("EventsQueryFilters", func(t *testing.T) { testEventsQueryFilters(t, f) })
	t.Run("SessionsProjection", func(t *testing.T) { testSessionsProjection(t, f) })
	t.Run("AttemptsProjection", func(t *testing.T) { testAttemptsProjection(t, f) })
	t.Run("UsageProjection", func(t *testing.T) { testUsageProjection(t, f) })
	t.Run("PolicyAuditProjection", func(t *testing.T) { testPolicyAuditProjection(t, f) })
	t.Run("PaginationContinuation", func(t *testing.T) { testPaginationContinuation(t, f) })
	t.Run("ContinuationShapeBound", func(t *testing.T) { testContinuationShapeBound(t, f) })
	t.Run("ContinuationTimeRangeShape", func(t *testing.T) { testContinuationTimeRangeShape(t, f) })
	t.Run("UnsupportedFiltersReported", func(t *testing.T) { testUnsupportedFiltersReported(t, f) })
	t.Run("RetentionIdempotent", func(t *testing.T) { testRetentionIdempotent(t, f) })
	t.Run("RedactionDefaultVisibility", func(t *testing.T) { testRedactionDefaultVisibility(t, f) })
	t.Run("SessionsDefaultVisibility", func(t *testing.T) { testSessionsDefaultVisibility(t, f) })
	t.Run("UnsafeEvidenceRejected", func(t *testing.T) { testUnsafeEvidenceRejected(t, f) })
}

func maybeParallel(t *testing.T, f Factory) {
	t.Helper()
	if p, ok := f.(FactoryParallelism); ok && !p.ParallelContract() {
		return
	}
	t.Parallel()
}

// ---- fixtures ----

// FixedTime is the deterministic base time used by contract fixtures so
// ordering and time-range assertions are stable.
var FixedTime = time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

func ctx(t *testing.T) context.Context {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return context.Background()
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	return ctx
}

func scopeFor(principal string) cp.ScopeSnapshot {
	return cp.ScopeSnapshot{
		Principal: scope.PrincipalScopeView{
			SubjectKind:  scope.SubjectHuman,
			PrincipalID:  scope.Known(principal),
			TenantID:     scope.Known("tenant-a"),
			WorkspaceID:  scope.Known("ws-1"),
			Origin:       scope.OriginClient,
			Roles:        []string{"analyst"},
			SafeClaims:   map[string]string{"team": "data"},
			PolicyLabels: map[string]string{"tier": "standard"},
		},
		PrincipalID:    scope.Known(principal),
		TenantID:       scope.Known("tenant-a"),
		WorkspaceID:    scope.Known("ws-1"),
		OrganizationID: scope.Unknown(),
		ProjectID:      scope.Unknown(),
		DepartmentID:   scope.Unknown(),
		CostCenterID:   scope.Unknown(),
		CredentialID:   scope.Unknown(),
	}
}

func authEvent(seq int, key, principal string) cp.Event {
	occurred := FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryAuth,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:    "trace-" + principal,
			SessionID:  "sess-" + principal,
			ALegID:     "aleg-" + principal,
			FrontendID: "openai-responses",
		},
		Scope:          scopeFor(principal),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.AuthDetail{
			Outcome:    "allowed",
			ReasonCode: "ok",
			Frontend:   "openai-responses",
			AuthMethod: "api_key",
		},
	}
}

func attemptEvent(seq int, key, principal, backend, model, outcome string, surfaced cp.AttemptSurfaced) cp.Event {
	occurred := FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryAttempt,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:    "trace-" + principal,
			SessionID:  "sess-" + principal,
			ALegID:     "aleg-" + principal,
			BLegID:     "bleg-" + principal,
			AttemptSeq: seq,
			BackendID:  backend,
			Model:      model,
		},
		Scope:          scopeFor(principal),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.AttemptDetail{
			ALegID:       "aleg-" + principal,
			BLegID:       "bleg-" + principal,
			AttemptSeq:   seq,
			BackendID:    backend,
			Model:        model,
			RouteOutcome: outcome,
			Surfaced:     surfaced,
			Outcome:      cp.AttemptOutcomeSucceeded,
			StartedAt:    occurred,
			FinishedAt:   occurred.Add(time.Second),
		},
	}
}

func usageEvent(seq int, key, principal, backend, model string, input, output int) cp.Event {
	occurred := FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryUsage,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:   "trace-" + principal,
			SessionID: "sess-" + principal,
			ALegID:    "aleg-" + principal,
			BLegID:    "bleg-" + principal,
			BackendID: backend,
			Model:     model,
		},
		Scope:          scopeFor(principal),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.UsageDetail{
			Plane:               cp.UsagePlaneAccounting,
			Availability:        cp.UsageAvailabilityAccountingAuth,
			InputTokens:         input,
			OutputTokens:        output,
			TotalTokens:         input + output,
			CostNanoUnits:       int64(input + output),
			Currency:            "USD",
			AccountingAuthority: "ledger",
		},
	}
}

func policyEvent(seq int, key, principal string, effect, reason string) cp.Event {
	occurred := FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryPolicy,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:    "trace-" + principal,
			SessionID:  "sess-" + principal,
			ALegID:     "aleg-" + principal,
			FrontendID: "openai-responses",
		},
		Scope:          scopeFor(principal),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.PolicyDetail{
			Stage:      "admission",
			Outcome:    "allow",
			Effect:     effect,
			ReasonCode: reason,
			ProviderID: "openai",
		},
	}
}

func auditEvent(seq int, key, principal, action string) cp.Event {
	occurred := FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryAudit,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:   "trace-" + principal,
			SessionID: "sess-" + principal,
		},
		Scope:          scopeFor(principal),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.AuditDetail{
			Action:     action,
			Result:     "ok",
			ReasonCode: "audit_ok",
		},
	}
}

func appendAll(t *testing.T, s controlplane.Store, events ...cp.Event) {
	t.Helper()
	c := ctx(t)
	for _, ev := range events {
		res, err := s.Append(c, ev)
		if err != nil {
			t.Fatalf("Append(%q) error = %v", ev.SourceEventKey, err)
		}
		if res.Dedupe != cp.DedupeInserted {
			t.Fatalf("Append(%q) dedupe = %q, want inserted", ev.SourceEventKey, res.Dedupe)
		}
		if res.ID.IsZero() {
			t.Fatalf("Append(%q) returned zero identity", ev.SourceEventKey)
		}
	}
}

func unsupportedConfigOf(f Factory) UnsupportedConfig {
	if u, ok := f.(FactoryWithUnsupported); ok {
		return u.UnsupportedConfig()
	}
	return UnsupportedConfig{}
}

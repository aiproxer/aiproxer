package controlplane_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCategoryConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.Category]string{
		controlplane.CategoryAuth:                "auth",
		controlplane.CategorySession:             "session",
		controlplane.CategoryAttempt:             "attempt",
		controlplane.CategoryUsage:               "usage",
		controlplane.CategoryAccountingAuthority: "accounting_authority",
		controlplane.CategoryPolicy:              "policy",
		controlplane.CategoryAudit:               "audit",
		controlplane.CategoryLifecycle:           "lifecycle",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("category drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("category %q must be known", got)
		}
	}
	if controlplane.Category("bogus").IsKnown() {
		t.Fatalf("unknown category reported as known")
	}
}

func TestEvidenceStateConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.EvidenceState]string{
		controlplane.EvidenceRecorded:    "recorded",
		controlplane.EvidencePartial:     "partial",
		controlplane.EvidenceUnavailable: "unavailable",
		controlplane.EvidenceRedacted:    "redacted",
		controlplane.EvidenceExpired:     "expired",
		controlplane.EvidenceUnsupported: "unsupported",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("evidence state drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("evidence state %q must be known", got)
		}
	}
}

func TestRedactionStateConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.RedactionState]string{
		controlplane.RedactionNone:       "none",
		controlplane.RedactionSummarized: "summarized",
		controlplane.RedactionRedacted:   "redacted",
		controlplane.RedactionHashed:     "hashed",
		controlplane.RedactionPrivileged: "privileged",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("redaction state drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("redaction state %q must be known", got)
		}
	}
}

func TestVisibilityConstantsAreStable(t *testing.T) {
	t.Parallel()
	if controlplane.VisibilityDefault != "default" {
		t.Fatalf("visibility default drift: %q", controlplane.VisibilityDefault)
	}
	if controlplane.VisibilityPrivileged != "privileged" {
		t.Fatalf("visibility privileged drift: %q", controlplane.VisibilityPrivileged)
	}
	if !controlplane.VisibilityDefault.IsKnown() || !controlplane.VisibilityPrivileged.IsKnown() {
		t.Fatalf("known visibility values must report known")
	}
	if controlplane.Visibility("bogus").IsKnown() {
		t.Fatalf("unknown visibility reported known")
	}
}

func TestEventIDZeroSemantics(t *testing.T) {
	t.Parallel()
	if !(controlplane.EventID{}).IsZero() {
		t.Fatalf("zero EventID must report zero")
	}
	if (controlplane.EventID{StoreID: "s", Sequence: 1}).IsZero() {
		t.Fatalf("non-zero EventID reported zero")
	}
}

func TestEventRequiresExactlyOneDetail(t *testing.T) {
	t.Parallel()
	now := time.Now()
	base := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
	}
	if err := base.Validate(); err == nil {
		t.Fatalf("event with no detail must be invalid")
	}
	withAuth := base
	withAuth.Detail = &controlplane.AuthDetail{Outcome: "allow"}
	if err := withAuth.Validate(); err != nil {
		t.Fatalf("event with one auth detail must be valid: %v", err)
	}
	var two controlplane.Event
	if err := json.Unmarshal([]byte(`{"category":"auth","occurred_at":"2020-01-01T00:00:01Z","recorded_at":"2020-01-01T00:00:01Z","correlation":{},"scope":{"principal":{},"principal_id":{},"credential_id":{},"tenant_id":{},"organization_id":{},"workspace_id":{},"project_id":{},"department_id":{},"cost_center_id":{}},"source":{},"visibility":"default","evidence_state":"recorded","redaction_state":"none","auth":{"outcome":"allow"},"session":{"action":"created"}}`), &two); err == nil {
		t.Fatalf("JSON with two detail blocks must be invalid")
	}
	withAccountingAuthority := base
	withAccountingAuthority.Category = controlplane.CategoryAccountingAuthority
	withAccountingAuthority.Detail = &controlplane.AccountingAuthorityDetail{
		Outcome:         controlplane.AccountingOutcomeReserve,
		EvidenceState:   controlplane.EvidenceRecorded,
		RedactionState:  controlplane.RedactionNone,
		Authority:       controlplane.AccountingAuthoritySourceReserved,
		Unit:            "tokens",
		RuleID:          "tenant.quota",
		ReservationID:   "reservation-1",
		SettlementState: controlplane.AccountingSettlementPending,
	}
	if err := withAccountingAuthority.Validate(); err != nil {
		t.Fatalf("event with accounting-authority detail must be valid: %v", err)
	}
	withMismatch := withAccountingAuthority
	withMismatch.Detail = &controlplane.AuthDetail{Outcome: "allow"}
	if err := withMismatch.Validate(); err == nil {
		t.Fatalf("event with category/detail mismatch must be invalid")
	}
}

// TestEventValidateRejectsZeroRecordedAt regression-locks the explicit
// RecordedAt.IsZero() guard. Zero RecordedAt was previously only rejected
// indirectly via the RecordedAt.Before(OccurredAt) check (which returns true
// when RecordedAt is the zero time and OccurredAt is non-zero), so the
// failure message could be misleading when both timestamps were zero. The
// extracted validateTimestamps helper now checks each timestamp independently
// and surfaces the right error so the asymmetry between required event
// timestamps and optional rule-window metadata is enforced at the contract
// boundary.
func TestEventValidateRejectsZeroRecordedAt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     time.Time{},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject zero RecordedAt as a required event timestamp")
	}
	if !strings.Contains(err.Error(), "recorded_at is required") {
		t.Fatalf("expected error to mention 'recorded_at is required', got: %v", err)
	}
}

// TestEventValidateRejectsZeroOccurredAt regression-locks the explicit
// OccurredAt.IsZero() guard (companion to TestEventValidateRejectsZeroRecordedAt).
func TestEventValidateRejectsZeroOccurredAt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     time.Time{},
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject zero OccurredAt as a required event timestamp")
	}
	if !strings.Contains(err.Error(), "occurred_at is required") {
		t.Fatalf("expected error to mention 'occurred_at is required', got: %v", err)
	}
}

// TestEventValidateRejectsRecordedAtBeforeOccurredAt regression-locks the
// explicit ordering guard at types.go:247. Both timestamps are non-zero so the
// zero checks pass and the ordering check fires independently. Mirrors the
// core-level TestValidateEventRejectsProblems/recorded_before_occurred test
// in internal/core/controlplane/validate_test.go.
func TestEventValidateRejectsRecordedAtBeforeOccurredAt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now.Add(time.Minute), // OccurredAt is later
		RecordedAt:     now,                  // RecordedAt is earlier
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject RecordedAt before OccurredAt")
	}
	if !strings.Contains(err.Error(), "recorded_at precedes occurred_at") {
		t.Fatalf("expected error to mention 'recorded_at precedes occurred_at', got: %v", err)
	}
}

// TestEventValidateAcceptsZeroWindowFields regression-locks the "no window"
// semantic documented on AccountingAuthorityDetail: a projector that emits
// this detail may not have access to the rule's window metadata, and the
// zero time.Time value is the explicit "not applicable" signal. Event.Validate
// must accept it, and the JSON round-trip must omit the zero window fields
// (so consumers can detect "no window" by absence rather than by a zero-length
// sentinel).
func TestEventValidateAcceptsZeroWindowFields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 9},
		SourceEventKey: "accounting:trace-1:reserve:no_window",
		Category:       controlplane.CategoryAccountingAuthority,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1"},
		Source:         controlplane.SourceRef{Name: "accountingsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail: &controlplane.AccountingAuthorityDetail{
			Correlation:     controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1"},
			RuleID:          "tenant.quota",
			Outcome:         controlplane.AccountingOutcomeReserve,
			ReasonCode:      "reserved",
			Authority:       controlplane.AccountingAuthoritySourceReserved,
			ReservationID:   "reservation-1",
			SettlementState: controlplane.AccountingSettlementPending,
			Unit:            "tokens",
			Limit:           1000,
			Consumed:        0,
			Reserved:        250,
			Remaining:       750,
			EvidenceState:   controlplane.EvidenceRecorded,
			RedactionState:  controlplane.RedactionNone,
			// WindowStart/End/ResetAt intentionally left as the zero value to
			// signal "no window" — see AccountingAuthorityDetail godoc.
		},
	}
	if !ev.AccountingAuthority().WindowStart.IsZero() ||
		!ev.AccountingAuthority().WindowEnd.IsZero() ||
		!ev.AccountingAuthority().WindowResetAt.IsZero() {
		t.Fatalf("test precondition: window fields must be zero before validation")
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("Event.Validate must accept zero WindowStart/End/ResetAt as the 'no window' signal: %v", err)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"window_start", "window_end", "window_reset_at"} {
		if strings.Contains(string(raw), "\""+key+"\"") {
			t.Fatalf("zero window field %q must be omitted from JSON (omitzero tag), got: %s", key, raw)
		}
	}
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.AccountingAuthority() == nil {
		t.Fatalf("accounting-authority detail lost in round-trip")
	}
	if !back.AccountingAuthority().WindowStart.IsZero() ||
		!back.AccountingAuthority().WindowEnd.IsZero() ||
		!back.AccountingAuthority().WindowResetAt.IsZero() {
		t.Fatalf("round-tripped window fields must remain zero (no-window semantic preserved): got start=%v end=%v reset=%v",
			back.AccountingAuthority().WindowStart, back.AccountingAuthority().WindowEnd, back.AccountingAuthority().WindowResetAt)
	}
}

func TestEventRejectsCategoryDetailMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryUsage,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	if err := ev.Validate(); err == nil {
		t.Fatalf("event with auth detail under usage category must be invalid")
	}
}

func TestEventRejectsAccountingAuthorityCategoryDetailMismatch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAccountingAuthority,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	if err := ev.Validate(); err == nil {
		t.Fatalf("event with auth detail under accounting-authority category must be invalid")
	}
}

// TestEventValidateRejectsUnknownVisibility regression-locks the explicit
// Visibility.IsKnown() bound at types.go:204-206. Companion to the constants
// test TestVisibilityConstantsAreStable, which only verifies
// Visibility("bogus").IsKnown() returns false; this test closes the gap by
// exercising Event.Validate rejection directly with the substring
// "unknown visibility".
func TestEventValidateRejectsUnknownVisibility(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.Visibility("bogus"),
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject unknown Visibility")
	}
	if !strings.Contains(err.Error(), "unknown visibility") {
		t.Fatalf("expected error to mention 'unknown visibility', got: %v", err)
	}
}

// TestEventValidateRejectsUnknownEvidenceState regression-locks the explicit
// EvidenceState.IsKnown() bound at types.go:207-209. Companion to the
// constants test TestEvidenceStateConstantsAreStable, which only verifies
// the documented enum values are marked known (no bogus-string IsKnown()
// check); this test closes the gap by exercising Event.Validate rejection
// directly with the substring "unknown evidence state".
func TestEventValidateRejectsUnknownEvidenceState(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceState("bogus"),
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject unknown EvidenceState")
	}
	if !strings.Contains(err.Error(), "unknown evidence state") {
		t.Fatalf("expected error to mention 'unknown evidence state', got: %v", err)
	}
}

// TestEventValidateRejectsUnknownRedactionState regression-locks the explicit
// RedactionState.IsKnown() bound at types.go:210-212. Companion to the
// constants test TestRedactionStateConstantsAreStable, which only verifies
// the documented enum values are marked known (no bogus-string IsKnown()
// check); this test closes the gap by exercising Event.Validate rejection
// directly with the substring "unknown redaction state".
func TestEventValidateRejectsUnknownRedactionState(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionState("bogus"),
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject unknown RedactionState")
	}
	if !strings.Contains(err.Error(), "unknown redaction state") {
		t.Fatalf("expected error to mention 'unknown redaction state', got: %v", err)
	}
}

// TestEventValidateRejectsPrivilegedVisibilityWithoutPrivilegedRedaction
// regression-locks the explicit guard at types.go:220: privileged visibility
// requires privileged redaction state. Mirrors the core-level
// TestValidateEventRejectsProblems/privileged_visibility_without_privileged_redaction
// test in internal/core/controlplane/validate_test.go so the explicit-guard
// contract is locked at both the SDK and core layers.
func TestEventValidateRejectsPrivilegedVisibilityWithoutPrivilegedRedaction(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ev := controlplane.Event{
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Visibility:     controlplane.VisibilityPrivileged,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone, // explicitly NOT privileged
		Detail:         &controlplane.AuthDetail{Outcome: "allow"},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatalf("Event.Validate must reject privileged visibility without privileged redaction state")
	}
	if !strings.Contains(err.Error(), "privileged visibility requires privileged redaction state") {
		t.Fatalf("expected error to mention 'privileged visibility requires privileged redaction state', got: %v", err)
	}
}

func TestEventJSONRoundTripPreservesDetailAndState(t *testing.T) {
	t.Parallel()
	now := time.Time{}.Add(time.Second)
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 7},
		SourceEventKey: "auth:trace-1:allow:ok",
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", SessionID: "sess-1"},
		Source:         controlplane.SourceRef{Name: "authsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow", ReasonCode: "ok"},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Category != ev.Category {
		t.Fatalf("category lost: %q vs %q", back.Category, ev.Category)
	}
	if back.Auth() == nil || back.Auth().Outcome != "allow" {
		t.Fatalf("auth detail lost: %#v", back.Auth())
	}
	if back.Session() != nil || back.Attempt() != nil {
		t.Fatalf("other details leaked through JSON: %#v", back)
	}
}

func TestEventJSONRoundTripPreservesAccountingAuthorityDetail(t *testing.T) {
	t.Parallel()
	now := time.Time{}.Add(2 * time.Second)
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 8},
		SourceEventKey: "accounting:trace-1:reserve:ok",
		Category:       controlplane.CategoryAccountingAuthority,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1", BLegID: "b-1"},
		Source:         controlplane.SourceRef{Name: "accountingsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail: &controlplane.AccountingAuthorityDetail{
			Correlation:    controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1", BLegID: "b-1"},
			Scope:          controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal-1")},
			RuleID:         "tenant.quota",
			RuleType:       "quota",
			Outcome:        controlplane.AccountingOutcomeReserve,
			ReasonCode:     "reservation_required",
			Authority:      controlplane.AccountingAuthoritySourceReserved,
			ReservationID:  "reservation-1",
			Unit:           "tokens",
			Limit:          1000,
			Consumed:       750,
			Reserved:       250,
			Remaining:      0,
			WindowStart:    now,
			WindowEnd:      now.Add(time.Hour),
			WindowResetAt:  now.Add(2 * time.Hour),
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionNone,
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Category != ev.Category {
		t.Fatalf("category lost: %q vs %q", back.Category, ev.Category)
	}
	if back.AccountingAuthority() == nil || back.AccountingAuthority().RuleID != "tenant.quota" {
		t.Fatalf("accounting-authority detail lost: %#v", back.AccountingAuthority())
	}
	if back.Auth() != nil || back.Session() != nil || back.Attempt() != nil || back.Usage() != nil {
		t.Fatalf("other details leaked through JSON: %#v", back)
	}
}

func TestEventHasNoForbiddenSecretFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Bearer", "APIKey", "Secret", "OAuth", "Header", "Password", "RawPayload", "RawBody", "RawUsageJSON", "ResumeToken", "AccessToken", "RefreshToken", "IDToken"}
	var ev controlplane.Event
	assertNoForbiddenFields(t, ev, forbidden)
	var corr controlplane.Correlation
	assertNoForbiddenFields(t, corr, forbidden)
	var snap controlplane.ScopeSnapshot
	assertNoForbiddenFields(t, snap, forbidden)
}

func TestScopeSnapshotPreservesUnknownVsKnownEmpty(t *testing.T) {
	t.Parallel()
	snap := controlplane.ScopeSnapshot{
		Principal: scope.PrincipalScopeView{PrincipalID: scope.Known("u1")},
		TenantID:  scope.Unknown(),
		ProjectID: scope.Known(""),
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.ScopeSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.TenantID.IsUnknown() {
		t.Fatalf("unknown tenant must round-trip as unknown")
	}
	if !back.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty project must round-trip as known-empty")
	}
	if back.Principal.PrincipalID.String() != "u1" {
		t.Fatalf("principal lost: %q", back.Principal.PrincipalID.String())
	}
}

func TestRecordResultRoundTrip(t *testing.T) {
	t.Parallel()
	r := controlplane.RecordResult{
		ID:         controlplane.EventID{StoreID: "mem", Sequence: 3},
		Dedupe:     controlplane.DedupeInserted,
		RecordedAt: time.Now(),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "\"dedupe\":\"inserted\"") {
		t.Fatalf("dedupe constant drift: %s", raw)
	}
}

func assertNoForbiddenFields[T any](t *testing.T, v T, forbidden []string) {
	t.Helper()
	walkFieldNames(t, reflect.TypeOf(v), forbidden, map[reflect.Type]bool{})
}

func walkFieldNames(t *testing.T, rt reflect.Type, forbidden []string, seen map[reflect.Type]bool) {
	t.Helper()
	if rt == nil {
		return
	}
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	if seen[rt] {
		return
	}
	seen[rt] = true
	for fld := range rt.Fields() {
		for _, bad := range forbidden {
			if strings.Contains(fld.Name, bad) {
				t.Fatalf("field %s.%s contains forbidden substring %q (raw secret/transport must not be in control-plane contract)", rt.Name(), fld.Name, bad)
			}
		}
		walkFieldNames(t, fld.Type, forbidden, seen)
	}
}

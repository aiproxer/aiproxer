package ledger

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMemoryLedger_RecordAndListByRequestPreservesAttemptsAndPlanes(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryLedger(Options{Now: func() time.Time { return createdAt }})

	records := []Record{
		baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 10, 20),
		baseRecord("req-1", "attempt-2", aipapi.UsagePlaneProviderBillable, 11, 21),
		baseRecord("req-1", "attempt-2", aipapi.UsagePlaneClientVisible, 12, 22),
	}
	for _, record := range records {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}

	got, err := store.ListByRequest(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("ListByRequest() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListByRequest() len = %d, want 3", len(got))
	}

	assertRecord(t, got[0], "req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 10, 20, createdAt)
	assertRecord(t, got[1], "req-1", "attempt-2", aipapi.UsagePlaneProviderBillable, 11, 21, createdAt)
	assertRecord(t, got[2], "req-1", "attempt-2", aipapi.UsagePlaneClientVisible, 12, 22, createdAt)

	attempt, err := store.ListByAttempt(context.Background(), "req-1", "attempt-2")
	if err != nil {
		t.Fatalf("ListByAttempt() error = %v", err)
	}
	if len(attempt) != 2 {
		t.Fatalf("ListByAttempt() len = %d, want 2", len(attempt))
	}
	if attempt[0].Plane == attempt[1].Plane {
		t.Fatalf("ListByAttempt() merged distinct planes: %#v", attempt)
	}
}

func TestMemoryLedger_RecordClonesInputAndOutput(t *testing.T) {
	t.Parallel()

	store := NewMemoryLedger(Options{Now: fixedNow})
	record := baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 3, 4)
	record.Metadata.Tokenizer.ID = "original-tokenizer"
	record.UnavailableReason = "provider_timeout"

	if err := store.Record(context.Background(), record); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	record.RequestID = "mutated-request"
	record.Metadata.Tokenizer.ID = "mutated-tokenizer"
	record.InputTokens = 99
	record.UnavailableReason = "mutated-reason"

	got, err := store.ListByRequest(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("ListByRequest() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByRequest() len = %d, want 1", len(got))
	}
	got[0].RequestID = "mutated-output"
	got[0].Metadata.Tokenizer.ID = "mutated-output-tokenizer"
	got[0].InputTokens = 100

	again, err := store.ListByRequest(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("ListByRequest() second call error = %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("ListByRequest() second len = %d, want 1", len(again))
	}
	if again[0].RequestID != "req-1" || again[0].Metadata.Tokenizer.ID != "original-tokenizer" || again[0].InputTokens != 3 {
		t.Fatalf("stored record was mutable: %#v", again[0])
	}
	if again[0].UnavailableReason != "provider_timeout" {
		t.Fatalf("UnavailableReason = %q, want provider_timeout", again[0].UnavailableReason)
	}
}

func TestMemoryLedger_DefaultClockFillsCreatedAt(t *testing.T) {
	t.Parallel()

	store := NewMemoryLedger(Options{})
	record := baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 1, 2)

	if err := store.Record(context.Background(), record); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	got, err := store.ListByRequest(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("ListByRequest() error = %v", err)
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero with default clock")
	}
}

func TestMemoryLedger_RecordValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		edit  func(*Record)
		field string
	}{
		{name: "missing request id", edit: func(r *Record) { r.RequestID = "" }, field: "RequestID"},
		{name: "missing attempt id", edit: func(r *Record) { r.AttemptID = "" }, field: "AttemptID"},
		{name: "missing plane", edit: func(r *Record) { r.Plane = aipapi.UsagePlaneUnknown }, field: "Plane"},
		{name: "negative input", edit: func(r *Record) { r.InputTokens = -1 }, field: "InputTokens"},
		{name: "negative output", edit: func(r *Record) { r.OutputTokens = -1 }, field: "OutputTokens"},
		{name: "negative cache read", edit: func(r *Record) { r.CacheReadTokens = -1 }, field: "CacheReadTokens"},
		{name: "negative cache write", edit: func(r *Record) { r.CacheWriteTokens = -1 }, field: "CacheWriteTokens"},
		{name: "negative reasoning", edit: func(r *Record) { r.ReasoningTokens = -1 }, field: "ReasoningTokens"},
		{name: "negative total", edit: func(r *Record) { r.TotalTokens = -1 }, field: "TotalTokens"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := NewMemoryLedger(Options{Now: fixedNow})
			record := baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 1, 2)
			tt.edit(&record)
			err := store.Record(context.Background(), record)
			if err == nil {
				t.Fatal("Record() error = nil, want validation error")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Record() error type = %T, want *ValidationError", err)
			}
			if validationErr.Field != tt.field {
				t.Fatalf("ValidationError.Field = %q, want %q", validationErr.Field, tt.field)
			}
		})
	}
}

func TestValidateRecordRejectsZeroCreatedAt(t *testing.T) {
	t.Parallel()

	record := baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 1, 2)
	record.CreatedAt = time.Time{}

	err := ValidateRecord(record)
	if err == nil {
		t.Fatal("ValidateRecord() error = nil, want validation error")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("ValidateRecord() error type = %T, want *ValidationError", err)
	}
	if validationErr.Field != "CreatedAt" {
		t.Fatalf("ValidationError.Field = %q, want CreatedAt", validationErr.Field)
	}
}

func TestMemoryLedger_HonorsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := NewMemoryLedger(Options{Now: fixedNow})
	if err := store.Record(ctx, baseRecord("req-1", "attempt-1", aipapi.UsagePlaneProviderBillable, 1, 2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record() error = %v, want context.Canceled", err)
	}
	if _, err := store.ListByRequest(ctx, "req-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByRequest() error = %v, want context.Canceled", err)
	}
	if _, err := store.ListByAttempt(ctx, "req-1", "attempt-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByAttempt() error = %v, want context.Canceled", err)
	}
}

func TestRecordHasNoRawContentFields(t *testing.T) {
	t.Parallel()

	for field := range reflect.TypeFor[Record]().Fields() {
		name := strings.ToLower(field.Name)
		for _, forbidden := range []string{"prompt", "message", "content", "text", "output"} {
			if forbidden == "output" && field.Name == "OutputTokens" {
				continue
			}
			if strings.Contains(name, forbidden) {
				t.Fatalf("Record field %q looks like raw content storage", field.Name)
			}
		}
	}
}

func baseRecord(requestID, attemptID string, plane aipapi.UsagePlane, inputTokens, outputTokens int) Record {
	return Record{
		RequestID:     requestID,
		AttemptID:     attemptID,
		Backend:       "openai",
		Model:         "gpt-4.1-mini",
		Plane:         plane,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   inputTokens + outputTokens,
		Metadata:      metadataForPlane(plane),
		FailureReason: "rate_limited_pre_output",
	}
}

func metadataForPlane(plane aipapi.UsagePlane) aipapi.UsageAccountingMetadata {
	return aipapi.UsageAccountingMetadata{
		Plane:     plane,
		Source:    aipapi.UsageSourceProviderReported,
		Authority: aipapi.UsageAuthorityAuthoritative,
		Tokenizer: aipapi.TokenizerRef{Type: "provider", ID: "openai", Version: "2026-05-14", ModelUsed: "gpt-4.1-mini"},
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 14, 12, 30, 0, 0, time.UTC)
}

func assertRecord(t *testing.T, got Record, requestID, attemptID string, plane aipapi.UsagePlane, inputTokens, outputTokens int, createdAt time.Time) {
	t.Helper()

	if got.RequestID != requestID || got.AttemptID != attemptID || got.Plane != plane {
		t.Fatalf("record identity = (%q, %q, %q), want (%q, %q, %q)", got.RequestID, got.AttemptID, got.Plane, requestID, attemptID, plane)
	}
	if got.Backend != "openai" || got.Model != "gpt-4.1-mini" {
		t.Fatalf("route/model = (%q, %q), want (openai, gpt-4.1-mini)", got.Backend, got.Model)
	}
	if got.InputTokens != inputTokens || got.OutputTokens != outputTokens || got.TotalTokens != inputTokens+outputTokens {
		t.Fatalf("tokens = (%d, %d, %d), want (%d, %d, %d)", got.InputTokens, got.OutputTokens, got.TotalTokens, inputTokens, outputTokens, inputTokens+outputTokens)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %s, want %s", got.CreatedAt, createdAt)
	}
	if got.Metadata.Plane != plane || got.Metadata.Source != aipapi.UsageSourceProviderReported || got.Metadata.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("Metadata = %#v, want plane/source/authority preserved", got.Metadata)
	}
	if got.FailureReason != "rate_limited_pre_output" {
		t.Fatalf("FailureReason = %q, want rate_limited_pre_output", got.FailureReason)
	}
}

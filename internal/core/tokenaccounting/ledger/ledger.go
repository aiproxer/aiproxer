package ledger

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Record is the core-owned billing ledger fact for one request attempt and usage plane.
type Record struct {
	RequestID         string
	AttemptID         string
	Backend           string
	Model             string
	Plane             aipapi.UsagePlane
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheWriteTokens  int
	ReasoningTokens   int
	TotalTokens       int
	Metadata          aipapi.UsageAccountingMetadata
	CreatedAt         time.Time
	UnavailableReason string
	FailureReason     string
}

// Options configures MemoryLedger.
type Options struct {
	Now func() time.Time
}

// ValidationError reports a field-specific invalid ledger record.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "ledger: validation error"
	}
	return fmt.Sprintf("ledger: invalid %s: %s", e.Field, e.Message)
}

// Recorder captures usage records without prescribing a persistence backend.
type Recorder interface {
	Record(context.Context, Record) error
}

// MemoryLedger is a minimal in-memory recorder for tests and off-by-default use.
type MemoryLedger struct {
	mu      sync.RWMutex
	now     func() time.Time
	records []Record
}

func NewMemoryLedger(opts Options) *MemoryLedger {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &MemoryLedger{now: now, records: []Record{}}
}

func (l *MemoryLedger) Record(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = l.now()
	}
	if err := ValidateRecord(record); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	l.records = append(l.records, cloneRecord(record))
	return nil
}

func (l *MemoryLedger) ListByRequest(ctx context.Context, requestID string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Record{}
	for _, record := range l.records {
		if record.RequestID == requestID {
			out = append(out, cloneRecord(record))
		}
	}
	return out, nil
}

func (l *MemoryLedger) ListByAttempt(ctx context.Context, requestID, attemptID string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Record{}
	for _, record := range l.records {
		if record.RequestID == requestID && record.AttemptID == attemptID {
			out = append(out, cloneRecord(record))
		}
	}
	return out, nil
}

func ValidateRecord(record Record) error {
	if record.RequestID == "" {
		return validationError("RequestID", "required")
	}
	if record.AttemptID == "" {
		return validationError("AttemptID", "required")
	}
	if record.Plane == aipapi.UsagePlaneUnknown {
		return validationError("Plane", "required")
	}
	if record.CreatedAt.IsZero() {
		return validationError("CreatedAt", "required")
	}
	if err := validateNonNegative("InputTokens", record.InputTokens); err != nil {
		return err
	}
	if err := validateNonNegative("OutputTokens", record.OutputTokens); err != nil {
		return err
	}
	if err := validateNonNegative("CacheReadTokens", record.CacheReadTokens); err != nil {
		return err
	}
	if err := validateNonNegative("CacheWriteTokens", record.CacheWriteTokens); err != nil {
		return err
	}
	if err := validateNonNegative("ReasoningTokens", record.ReasoningTokens); err != nil {
		return err
	}
	if err := validateNonNegative("TotalTokens", record.TotalTokens); err != nil {
		return err
	}
	return nil
}

func validateNonNegative(field string, value int) error {
	if value < 0 {
		return validationError(field, "must be non-negative")
	}
	return nil
}

func validationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func cloneRecord(record Record) Record {
	return record
}

package diag

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Compile-time: production concrete stores satisfy the narrow diagnostics Store
// and optional usage rollup without exposing write methods to HTTP composition.
var (
	_ Store              = (*memory.Store)(nil)
	_ Store              = (*bunstore.Store)(nil)
	_ SessionUsageRollup = (*memory.Store)(nil)
	_ SessionUsageRollup = (*bunstore.Store)(nil)
)

// Compile-time: app.Store method set is a superset of diagnostics Store
// (assignment from app.Store → Store must type-check).
var _ = func(s app.Store) Store { return s }

// readOnlyDiagStore has only the read methods required by [Store].
// If Store ever gains a write method, this conformance must fail to compile.
type readOnlyDiagStore struct{}

func (readOnlyDiagStore) Summary(context.Context, domain.SummaryQuery) ([]domain.Summary, error) {
	return nil, nil
}

func (readOnlyDiagStore) LoadByID(context.Context, domain.SessionID) (domain.Record, error) {
	return domain.Record{}, nil
}

func (readOnlyDiagStore) LoadByALegID(context.Context, string) (domain.Record, error) {
	return domain.Record{}, nil
}

func (readOnlyDiagStore) Transcript(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.TranscriptItem, error) {
	return nil, nil
}

func (readOnlyDiagStore) Audit(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AuditItem, error) {
	return nil, nil
}

func (readOnlyDiagStore) ListAttemptEvidence(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

var _ Store = readOnlyDiagStore{}

func TestStoreContract_readOnlyFakeSatisfiesWithoutWrites(t *testing.T) {
	t.Parallel()
	var s Store = readOnlyDiagStore{}
	// Prove writes are not part of the contract: app.Store is broader.
	var _ app.Store = (*memory.Store)(nil)
	_, hasCreate := any(s).(interface {
		Create(context.Context, domain.CreateRecord) (domain.Record, error)
	})
	if hasCreate {
		t.Fatal("diagnostics Store must not require Create")
	}
}

func TestStoreContract_appStoreProjectsToDiagStore(t *testing.T) {
	t.Parallel()
	mem := memory.New(memory.Options{})
	var asApp app.Store = mem
	var asDiag Store = asApp
	if asDiag == nil {
		t.Fatal("memory store via app.Store must project to diag.Store")
	}
}

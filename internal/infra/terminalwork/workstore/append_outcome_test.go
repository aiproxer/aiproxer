package workstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestAppendOutcome_MemoryStore_InsertedReplayConflict(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "outcome-mem"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rec := sampleRecord("w-out", "sk-out", "prov-a", sdk.WorkKindSettleRequestProvider)

	out, err := store.AppendIntentOutcome(ctx, rec)
	if err != nil || !out.Inserted || out.Replay {
		t.Fatalf("inserted got %+v err=%v", out, err)
	}
	out, err = store.AppendIntentOutcome(ctx, rec)
	if err != nil || out.Inserted || !out.Replay {
		t.Fatalf("replay got %+v err=%v", out, err)
	}
	conflict := rec
	conflict.Payload = []byte(`{"handle":"other"}`)
	out, err = store.AppendIntentOutcome(ctx, conflict)
	if !errors.Is(err, workstore.ErrIdentityCollision) || out.Inserted || out.Replay {
		t.Fatalf("conflict got %+v err=%v", out, err)
	}
}

func TestAppendOutcome_DurableStore_InsertedReplayConflict(t *testing.T) {
	t.Parallel()
	store := newSQLiteOutcomeStore(t)
	ctx := context.Background()
	rec := sampleRecord("w-dout", "sk-dout", "prov-a", sdk.WorkKindSettleRequestProvider)

	out, err := store.AppendIntentOutcome(ctx, rec)
	if err != nil || !out.Inserted || out.Replay {
		t.Fatalf("inserted got %+v err=%v", out, err)
	}
	out, err = store.AppendIntentOutcome(ctx, rec)
	if err != nil || out.Inserted || !out.Replay {
		t.Fatalf("replay got %+v err=%v", out, err)
	}
	conflict := rec
	conflict.Payload = []byte(`{"handle":"other"}`)
	out, err = store.AppendIntentOutcome(ctx, conflict)
	if !errors.Is(err, workstore.ErrIdentityCollision) || out.Inserted || out.Replay {
		t.Fatalf("conflict got %+v err=%v", out, err)
	}
}

func TestAppendOutcome_AmbiguousFakeNeitherFlag(t *testing.T) {
	t.Parallel()
	fake := ambiguousOutcomeStore{}
	out, err := fake.AppendIntentOutcome(context.Background(), terminalwork.WorkRecord{})
	if err == nil || out.Inserted || out.Replay {
		t.Fatalf("ambiguous got %+v err=%v", out, err)
	}
}

type ambiguousOutcomeStore struct{}

func (ambiguousOutcomeStore) AppendIntentOutcome(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	return terminalwork.AppendIntentOutcome{}, errors.New("transport ambiguous")
}

func newSQLiteOutcomeStore(t *testing.T) *workstore.DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outcome.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workstore.NewDurableStore(context.Background(), bunDB, workstore.DurableConfig{StoreID: "outcome"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

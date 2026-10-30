package ledgerstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/uptrace/bun/dialect"
)

// errMidIteration stands in for a driver failure reported after the query
// itself succeeded. database/sql surfaces this shape through Rows.Err(), not
// QueryContext, so a caller that skips the Err check silently loses it.
var errMidIteration = errors.New("driver: iteration aborted")

// iterationErrorConnector opens connections whose result sets fail from Next on
// the first call, the shape a real driver uses for a mid-scan failure.
type iterationErrorConnector struct{}

func (iterationErrorConnector) Connect(context.Context) (driver.Conn, error) {
	return iterationErrorConn{}, nil
}
func (iterationErrorConnector) Driver() driver.Driver { return iterationErrorDriver{} }

type iterationErrorDriver struct{}

func (iterationErrorDriver) Open(string) (driver.Conn, error) { return iterationErrorConn{}, nil }

type iterationErrorConn struct{}

func (iterationErrorConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (iterationErrorConn) Close() error                        { return nil }
func (iterationErrorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("iterationErrorConn: transactions unsupported")
}

func (iterationErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return iterationErrorRows{}, nil
}

type iterationErrorRows struct{}

func (iterationErrorRows) Columns() []string         { return []string{"id"} }
func (iterationErrorRows) Close() error              { return nil }
func (iterationErrorRows) Next([]driver.Value) error { return errMidIteration }

// newIterationErrorStore builds a durable store whose SQL layer always fails
// mid-iteration. Migrations are irrelevant here: the failure under test happens
// after a query has started, so the store is assembled without them.
func newIterationErrorStore(t *testing.T) *DurableStore {
	t.Helper()
	db := sql.OpenDB(iterationErrorConnector{})
	t.Cleanup(func() { _ = db.Close() })
	return &DurableStore{
		cfg:             DurableConfig{StoreID: "iteration-error"},
		sqlDB:           db,
		dialect:         dialect.SQLite,
		maxSummaryBytes: controlplane.MaxSummaryBytes,
		maxScopeBytes:   16384,
		maxDetailBytes:  16384,
	}
}

func TestSelectIDBySourceKeyPreservesIterationError(t *testing.T) {
	t.Parallel()
	store := newIterationErrorStore(t)
	_, ok, err := store.selectIDBySourceKey(context.Background(), "source-key")
	if ok {
		t.Fatal("selectIDBySourceKey reported a found record after an iteration failure")
	}
	if err == nil {
		t.Fatal("selectIDBySourceKey masked the iteration error as a missing record")
	}
	if !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("error = %v, want it to classify as ErrDegraded", err)
	}
	if !strings.Contains(err.Error(), errMidIteration.Error()) {
		t.Fatalf("error = %v, want it to mention %q", err, errMidIteration)
	}
}

func TestSelectEventByIDPreservesIterationError(t *testing.T) {
	t.Parallel()
	store := newIterationErrorStore(t)
	ev, err := store.selectEventByID(context.Background(), cp.EventID{StoreID: "iteration-error", Sequence: 1})
	if err == nil {
		t.Fatal("selectEventByID masked the iteration error as an absent record")
	}
	if ev.ID.Sequence != 0 || ev.Source.Name != "" {
		t.Fatalf("event = %#v, want a zero event alongside the error", ev)
	}
	if !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("error = %v, want it to classify as ErrDegraded", err)
	}
	if !strings.Contains(err.Error(), errMidIteration.Error()) {
		t.Fatalf("error = %v, want it to mention %q", err, errMidIteration)
	}
}

func TestAppendPreservesInsertIterationError(t *testing.T) {
	t.Parallel()
	store := newIterationErrorStore(t)
	ev := contractEvent()
	ev.SourceEventKey = "" // Skip dedupe so the insert query is the first statement.
	_, err := store.Append(context.Background(), ev)
	if err == nil {
		t.Fatal("Append succeeded despite an insert iteration failure")
	}
	if strings.Contains(err.Error(), "insert returned no id") {
		t.Fatalf("Append reported %v, want the underlying storage failure", err)
	}
	if !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("error = %v, want it to classify as ErrDegraded", err)
	}
	if !strings.Contains(err.Error(), errMidIteration.Error()) {
		t.Fatalf("error = %v, want it to mention %q", err, errMidIteration)
	}
}

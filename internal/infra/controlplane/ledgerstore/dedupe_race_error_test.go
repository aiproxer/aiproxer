package ledgerstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// errDedupeRaceRead stands in for a storage read failure after a concurrent
// insert won the source-key uniqueness race.
var errDedupeRaceRead = errors.New("driver: dedupe record read aborted")

// dedupeRaceConnector scripts the query sequence Append performs when its
// pre-insert source-key lookup misses and the INSERT then loses a uniqueness
// race: lookup miss, unique violation, lookup hit, record read failure.
type dedupeRaceConnector struct {
	mu      sync.Mutex
	calls   int
	initial bool
	mode    string
}

func (c *dedupeRaceConnector) Connect(context.Context) (driver.Conn, error) {
	return &dedupeRaceConn{c: c}, nil
}
func (c *dedupeRaceConnector) Driver() driver.Driver { return dedupeRaceDriver{} }

type dedupeRaceDriver struct{}

func (dedupeRaceDriver) Open(string) (driver.Conn, error) {
	return &dedupeRaceConn{c: &dedupeRaceConnector{}}, nil
}

type dedupeRaceConn struct{ c *dedupeRaceConnector }

func (*dedupeRaceConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*dedupeRaceConn) Close() error                        { return nil }
func (*dedupeRaceConn) Begin() (driver.Tx, error) {
	return nil, errors.New("dedupeRaceConn: transactions unsupported")
}

func (c *dedupeRaceConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.c.mu.Lock()
	c.c.calls++
	call := c.c.calls
	c.c.mu.Unlock()
	switch {
	case strings.Contains(query, "INSERT INTO control_plane_events"):
		return nil, pgdriver.NewError(map[byte]string{'C': "23505", 'M': "duplicate key value", 'S': "ERROR"})
	case strings.Contains(query, "WHERE id ="):
		switch c.c.mode {
		case "query":
			return nil, errDedupeRaceRead
		case "missing":
			return &scriptedRows{}, nil
		case "success", "scan", "decode":
			values := []driver.Value{int64(7), "dedupe-race", "key", "auth", int64(100), int64(200), "", "", "", "", "", int64(0), "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "{}", "{}", "{}"}
			if c.c.mode == "scan" {
				values[0] = "invalid"
			}
			if c.c.mode == "decode" {
				values[29] = "invalid"
			}
			return &scriptedRows{values: values}, nil
		default:
			return &scriptedRows{err: errDedupeRaceRead}, nil
		}
	case strings.Contains(query, "source_event_key") && (call > 1 || c.c.initial):
		return &scriptedRows{ids: []int64{7}}, nil
	default:
		return &scriptedRows{}, nil
	}
}

type scriptedRows struct {
	ids    []int64
	err    error
	values []driver.Value
}

func (r *scriptedRows) Columns() []string {
	if r.values != nil {
		return make([]string, len(r.values))
	}
	return []string{"id"}
}
func (*scriptedRows) Close() error { return nil }
func (r *scriptedRows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.values != nil {
		copy(dest, r.values)
		r.values = nil
		return nil
	}
	if len(r.ids) == 0 {
		return io.EOF
	}
	dest[0], r.ids = r.ids[0], r.ids[1:]
	return nil
}

func newDedupeRaceStore(t *testing.T) *DurableStore {
	t.Helper()
	return newDedupeStoreWithScript(t, &dedupeRaceConnector{})
}

func newDedupeStoreWithScript(t *testing.T, script *dedupeRaceConnector) *DurableStore {
	t.Helper()
	db := sql.OpenDB(script)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &DurableStore{
		cfg:             DurableConfig{StoreID: "dedupe-race"},
		sqlDB:           db,
		dialect:         dialect.SQLite,
		maxSummaryBytes: controlplane.MaxSummaryBytes,
		maxScopeBytes:   16384,
		maxDetailBytes:  16384,
	}
}

func TestAppendDedupeWinnerRead(t *testing.T) {
	for _, initial := range []bool{true, false} {
		for _, mode := range []string{"query", "iteration", "missing", "scan", "decode", "success"} {
			t.Run(fmt.Sprintf("initial=%t/%s", initial, mode), func(t *testing.T) {
				store := newDedupeStoreWithScript(t, &dedupeRaceConnector{initial: initial, mode: mode})
				ev := contractEvent()
				ev.SourceEventKey = "key"
				result, err := store.Append(context.Background(), ev)
				if mode == "success" {
					if err != nil || result.ID != (cp.EventID{StoreID: "dedupe-race", Sequence: 7}) || result.Dedupe != cp.DedupeDuplicate || !result.RecordedAt.Equal(time.Unix(0, 200)) {
						t.Fatalf("result = %+v, error = %v", result, err)
					}
					return
				}
				if !errors.Is(err, controlplane.ErrDegraded) {
					t.Fatalf("result = %+v, error = %v; want degraded", result, err)
				}
				if (mode == "query" || mode == "iteration") && !errors.Is(err, errDedupeRaceRead) {
					t.Fatalf("error = %v; want underlying read error", err)
				}
			})
		}
	}
}

// TestAppendPreservesDedupeRecordReadError proves the unique-violation dedupe
// fallback surfaces a failed read of the winning record instead of reporting a
// successful duplicate with an unverified recorded-at timestamp.
func TestAppendPreservesDedupeRecordReadError(t *testing.T) {
	t.Parallel()
	store := newDedupeRaceStore(t)
	ev := contractEvent()
	ev.SourceEventKey = "dedupe-race-source"
	result, err := store.Append(context.Background(), ev)
	if err == nil {
		t.Fatalf("Append reported the duplicate %+v despite a failed dedupe record read", result)
	}
	if !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("error = %v, want it to classify as ErrDegraded", err)
	}
	if !strings.Contains(err.Error(), errDedupeRaceRead.Error()) {
		t.Fatalf("error = %v, want it to mention %q", err, errDedupeRaceRead)
	}
}

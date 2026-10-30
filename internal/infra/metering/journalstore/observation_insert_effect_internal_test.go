package journalstore

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// stubInsertResult drives observationInsertEffect with an exact row count, or
// with an unreadable count, so the fail-closed branches are reachable without
// a driver that misreports.
type stubInsertResult struct {
	rows int64
	err  error
}

func (s stubInsertResult) LastInsertId() (int64, error) { return 0, errors.New("not used") }
func (s stubInsertResult) RowsAffected() (int64, error) { return s.rows, s.err }

var errStubCount = errors.New("stub row count unavailable")

// TestObservationInsertEffectFailsClosed pins how the conflict-suppressed
// insert's effect is read. Only an exact 0 (a durable row already owns this
// observation and its projections) and an exact 1 (this call wrote the row and
// owns the projections) are actionable. Anything else must abort the append
// rather than guess, because guessing wrong either projects a replay twice or
// leaves a fresh row unprojected.
func TestObservationInsertEffectFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		result      sql.Result
		wantInsert  bool
		wantErr     bool
		errContains string
		wantCause   error
	}{
		{name: "wrote one row", result: stubInsertResult{rows: 1}, wantInsert: true},
		{name: "wrote nothing", result: stubInsertResult{rows: 0}, wantInsert: false},
		{name: "nil result", result: nil, wantErr: true, errContains: "no result"},
		{name: "unreadable count", result: stubInsertResult{rows: 1, err: errStubCount}, wantErr: true, errContains: errStubCount.Error(), wantCause: errStubCount},
		{name: "implausible count", result: stubInsertResult{rows: 2}, wantErr: true, errContains: "2 rows, want 0 or 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inserted, err := observationInsertEffect(tc.result)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("observationInsertEffect(%+v) inserted=%v err=nil, want error", tc.result, inserted)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("observationInsertEffect(%+v) err=%v, want it to contain %q", tc.result, err, tc.errContains)
				}
				if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
					t.Fatalf("observationInsertEffect must preserve the driver cause, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("observationInsertEffect(%+v) err=%v, want nil", tc.result, err)
			}
			if inserted != tc.wantInsert {
				t.Fatalf("observationInsertEffect(%+v) inserted=%v, want %v", tc.result, inserted, tc.wantInsert)
			}
		})
	}
}

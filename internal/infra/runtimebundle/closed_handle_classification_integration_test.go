//go:build integration

package runtimebundle_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

var errUnexpectedClosedProbeConnect = errors.New("closed sentinel probe unexpectedly connected")

type (
	closedProbeConnector struct{}
	closedProbeDriver    struct{}
)

func (closedProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errUnexpectedClosedProbeConnect
}
func (closedProbeConnector) Driver() driver.Driver { return closedProbeDriver{} }
func (closedProbeDriver) Open(string) (driver.Conn, error) {
	return nil, errUnexpectedClosedProbeConnect
}

var actualDatabaseSQLClosedError = func() error {
	db := sql.OpenDB(closedProbeConnector{})
	_ = db.Close()
	return db.PingContext(context.Background())
}()

func TestClosedHandleClassificationIsExact(t *testing.T) {
	t.Parallel()
	if actualDatabaseSQLClosedError == nil {
		t.Fatal("database/sql closed sentinel probe returned nil")
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "actual database sql sentinel", err: actualDatabaseSQLClosedError, want: true},
		{name: "wrapped actual database sql sentinel", err: fmt.Errorf("query persistence: %w", actualDatabaseSQLClosedError), want: true},
		{name: "same text imitation", err: errors.New("sql: database is closed"), want: false},
		{name: "suffix imitation", err: errors.New("authorization failed: sql: database is closed"), want: false},
		{name: "maintenance prose", err: errors.New("live postgres: database is closed for maintenance"), want: false},
		{name: "foreign closed phrase", err: errors.New("database is closed"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, got := closedHandleClassification(tc.err)
			if got != tc.want {
				t.Fatalf("closedHandleClassification(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

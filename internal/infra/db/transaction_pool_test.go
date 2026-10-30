package db

import (
	"strings"
	"testing"
)

func TestValidateTransactionPoolDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dsn     string
		wantErr string
	}{
		{name: "plain", dsn: "postgres://host/db?sslmode=require"},
		{name: "search path", dsn: "postgres://host/db?search_path=tenant", wantErr: "search_path"},
		{name: "case insensitive", dsn: "postgres://host/db?SEARCH_PATH=tenant", wantErr: "search_path"},
		{name: "search path via options", dsn: "postgres://host/db?options=-csearch_path=tenant", wantErr: "search_path"},
		{name: "search path via spaced options", dsn: "postgres://host/db?options=-c%20search_path%3Dtenant", wantErr: "search_path"},
		{name: "harmless options", dsn: "postgres://host/db?options=-cstatement_timeout%3D5s"},
		{name: "malformed query", dsn: "postgres://host/db?bad=%zz", wantErr: "parse postgres dsn query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTransactionPoolDSN(tt.dsn)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error=%v want containing %q", err, tt.wantErr)
			}
		})
	}
}

package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/uptrace/bun"
)

var meteringRuntimeSchema = sync.OnceValues(func() ([]byte, error) {
	return runtimeSchemaImage(func(ctx context.Context, database *bun.DB) error {
		_, err := journalstore.NewDurableStore(ctx, database, journalstore.DurableConfig{StoreID: "fixture-template"})
		return err
	})
})

func runtimeSchemaImage(migrate func(context.Context, *bun.DB) error) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	database, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, database); err != nil {
		return nil, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	var image []byte
	err = conn.Raw(func(raw any) error {
		serializer, ok := raw.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return fmt.Errorf("sqlite fixture driver does not support serialization")
		}
		image, err = serializer.Serialize()
		return err
	})
	return image, err
}

// PrepareMeteringSchemaForTest avoids repeating schema construction during host
// setup while retaining the real metering store, host composition and reopen.
func PrepareMeteringSchemaForTest(tb testing.TB, filename string) {
	tb.Helper()
	prepareRuntimeSchema(tb, filename, meteringRuntimeSchema)
}

func prepareRuntimeSchema(tb testing.TB, filename string, template func() ([]byte, error)) {
	tb.Helper()
	if _, err := os.Stat(filename); err == nil {
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		tb.Fatal(err)
	}
	image, err := template()
	if err != nil {
		tb.Fatal(err)
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return
	}
	if err != nil {
		tb.Fatal(err)
	}
	_, writeErr := file.Write(image)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		tb.Fatal(err)
	}
}

func TestRuntimeSchemaFixturesOwnPrivateFilesAndPreserveRestartState(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		prepare func(testing.TB, string)
	}{
		{"metering", PrepareMeteringSchemaForTest},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			first, second := filepath.Join(t.TempDir(), "first.sqlite"), filepath.Join(t.TempDir(), "second.sqlite")
			fixture.prepare(t, first)
			fixture.prepare(t, second)
			if _, err := os.Stat(first); err != nil {
				t.Fatalf("prepared schema missing: %v", err)
			}
			database, err := sql.Open("sqlite", first)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("CREATE TABLE fixture_marker(value TEXT); INSERT INTO fixture_marker VALUES ('preserved')"); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			fixture.prepare(t, first)
			database, err = sql.Open("sqlite", first)
			if err != nil {
				t.Fatal(err)
			}
			var value string
			err = database.QueryRow("SELECT value FROM fixture_marker").Scan(&value)
			_ = database.Close()
			if err != nil || value != "preserved" {
				t.Fatalf("restart value=%q err=%v", value, err)
			}
			database, err = sql.Open("sqlite", second)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = database.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='fixture_marker'").Scan(&count)
			_ = database.Close()
			if err != nil || count != 0 {
				t.Fatalf("independent clone marker count=%d err=%v", count, err)
			}
		})
	}
}

package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
)

// FeatureDatabaseConfig selects the engine and the fallback location of the
// isolated feature databases of one process.
type FeatureDatabaseConfig struct {
	// Store names the continuity backing: "sqlite" or "postgres".
	Store string
	// SQLitePath is the continuity database file. The default isolated file
	// sits next to it.
	SQLitePath string
	// PostgresDSN is the continuity connection string. The isolated feature
	// database uses the same logical database on a dedicated pool, so an
	// advisory write never starves the mandatory pool.
	PostgresDSN string
	// Pool bounds the dedicated PostgreSQL pool.
	Pool PoolSettings
}

// defaultFeatureDatabasePoolConns bounds the dedicated PostgreSQL pool of an
// isolated feature database when the composition root sets no pool settings.
// The isolated database is an optional advisory store: a small dedicated pool
// keeps it from starving the mandatory pool.
const defaultFeatureDatabasePoolConns = 4

// sqliteMemoryPath is the in-memory database spelling. It names no file, so
// it can never be the continuity file.
const sqliteMemoryPath = ":memory:"

// sqliteMemoryURIPath is the URI spelling of the in-memory database.
const sqliteMemoryURIPath = "file::memory:"

// errFeatureDatabaseIsCoreFile rejects an isolated feature database that
// resolves to the core continuity database. The message names the rule, never
// an operator configuration value: a path or a connection string never
// reaches an error.
var errFeatureDatabaseIsCoreFile = errors.New("db: the isolated feature database must not be the continuity database")

// errFeatureDatabaseIsEphemeral rejects an isolated feature database that
// names no durable file. The message names the rule, never an operator
// configuration value: a path never reaches an error.
var errFeatureDatabaseIsEphemeral = errors.New("db: the isolated feature database must be a durable file, not an in-memory database")

// isEphemeralSQLiteTarget reports whether path names the in-memory SQLite
// database instead of a durable file. The comparison is exact and trimmed:
// the SQLite URI query spellings are already refused by the path validation.
func isEphemeralSQLiteTarget(path string) bool {
	trimmed := strings.TrimSpace(path)
	return trimmed == sqliteMemoryPath || trimmed == sqliteMemoryURIPath
}

// featureDatabaseOpener opens the isolated feature databases of one process.
type featureDatabaseOpener struct {
	cfg FeatureDatabaseConfig
}

var _ featuredb.Opener = (*featureDatabaseOpener)(nil)

// NewFeatureDatabaseOpener returns the opener of the isolated feature
// databases of one process.
func NewFeatureDatabaseOpener(cfg FeatureDatabaseConfig) featuredb.Opener {
	return &featureDatabaseOpener{cfg: cfg}
}

// OpenFeatureDatabase opens the isolated durable database of one feature.
//
// SQLite gets its own file: the continuity database has a single connection
// and one writer, so an advisory memory transaction on the same file would
// hold the connection the mandatory core path needs. PostgreSQL keeps the
// same logical database but gets a dedicated pool.
//
// An SQLite request that resolves to the continuity database itself is
// refused before the file is opened, and a request that names the in-memory
// database is refused too: the isolated database is durable. The isolated
// database is a promise the composition root keeps whatever the operator
// configures: an advisory write must never reach the mandatory core
// connection.
func (o *featureDatabaseOpener) OpenFeatureDatabase(ctx context.Context, spec featuredb.Spec) (featuredb.Database, error) {
	if o == nil {
		return featuredb.Database{}, fmt.Errorf("db: nil feature database opener")
	}
	if ctx == nil {
		return featuredb.Database{}, fmt.Errorf("db: nil feature database context")
	}
	switch o.cfg.Store {
	case "sqlite":
		path := strings.TrimSpace(spec.SQLitePath)
		if path == "" {
			derived, err := defaultFeatureDatabasePath(o.cfg.SQLitePath, spec.FeatureID)
			if err != nil {
				return featuredb.Database{}, err
			}
			path = derived
		}
		if isEphemeralSQLiteTarget(path) {
			// The isolated feature database is durable: an in-memory target
			// would lose every advisory write when the process ends, without
			// an operator-visible failure.
			return featuredb.Database{}, errFeatureDatabaseIsEphemeral
		}
		identity, err := o.isolatedSQLiteIdentity(path)
		if err != nil {
			return featuredb.Database{}, err
		}
		bunDB, err := OpenSQLiteBun(ctx, path)
		if err != nil {
			return featuredb.Database{}, fmt.Errorf("db: open feature sqlite database: %w", err)
		}
		return featuredb.Database{DB: bunDB, Identity: identity}, nil
	case "postgres":
		dsn := strings.TrimSpace(o.cfg.PostgresDSN)
		if dsn == "" {
			return featuredb.Database{}, fmt.Errorf("db: continuity postgres_dsn is required")
		}
		pool := o.cfg.Pool
		if pool.MaxOpenConns <= 0 {
			pool.MaxOpenConns = defaultFeatureDatabasePoolConns
		}
		bunDB, err := OpenPostgresBun(ctx, dsn, pool)
		if err != nil {
			return featuredb.Database{}, fmt.Errorf("db: open feature postgres database: %w", err)
		}
		return featuredb.Database{DB: bunDB, Identity: "postgres|continuity"}, nil
	default:
		return featuredb.Database{}, fmt.Errorf(
			"db: continuity store %q has no isolated durable feature database", o.cfg.Store)
	}
}

// isolatedSQLiteIdentity resolves the stable identity of an isolated SQLite
// database and refuses a file that is the core continuity database.
//
// The identity is the canonical path of the file, so two spellings of one
// file name one database: the process opens it once and applies its schema
// once.
func (o *featureDatabaseOpener) isolatedSQLiteIdentity(path string) (string, error) {
	canonical, err := canonicalSQLitePath(path)
	if err != nil {
		return "", err
	}
	if err := o.rejectContinuitySQLiteFile(canonical); err != nil {
		return "", err
	}
	return "sqlite|" + canonical, nil
}

// rejectContinuitySQLiteFile refuses an isolated database that is the core
// continuity database. The comparison covers the canonical path and the
// physical file, so a symlink or a hardlink to the continuity file names the
// same file and is refused too.
//
// The in-memory database names no file, so it is never the continuity file.
func (o *featureDatabaseOpener) rejectContinuitySQLiteFile(canonical string) error {
	continuity := strings.TrimSpace(o.cfg.SQLitePath)
	if continuity == "" || canonical == sqliteMemoryPath {
		return nil
	}
	continuityCanonical, err := canonicalSQLitePath(continuity)
	if err != nil {
		// The continuity path itself is invalid: the core database would
		// have failed to open already, so the feature database is unaffected.
		return nil
	}
	if continuityCanonical == sqliteMemoryPath {
		return nil
	}
	if continuityCanonical == canonical {
		return errFeatureDatabaseIsCoreFile
	}
	if sameSQLiteFile(canonical, continuityCanonical) {
		return errFeatureDatabaseIsCoreFile
	}
	return nil
}

// canonicalSQLitePath resolves a SQLite file path to its canonical absolute
// form. The path characters are validated with the existing DSN validation,
// so an invalid path is refused before any filesystem access, and the
// canonical form uses the same spelling the driver opens.
func canonicalSQLitePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("db: empty sqlite path")
	}
	if _, err := SQLiteFileDSN(trimmed); err != nil {
		return "", err
	}
	if trimmed == sqliteMemoryPath {
		return sqliteMemoryPath, nil
	}
	absolute, err := filepath.Abs(strings.ReplaceAll(trimmed, `\`, `/`))
	if err != nil {
		return "", fmt.Errorf("db: resolve sqlite path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// sameSQLiteFile reports whether two paths name the same existing file.
func sameSQLiteFile(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

// defaultFeatureDatabasePath derives the default isolated database file from
// the continuity database file: the same directory, a different file named
// after the feature.
func defaultFeatureDatabasePath(continuityPath, featureID string) (string, error) {
	continuity := strings.TrimSpace(continuityPath)
	if continuity == "" {
		return "", fmt.Errorf("db: continuity sqlite_path is required")
	}
	name := strings.TrimSpace(featureID)
	if name == "" {
		return "", fmt.Errorf("db: feature id is required for the default feature database path")
	}
	dir := filepath.Dir(continuity)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, name+".db"), nil
}

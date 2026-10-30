package dbparity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// SemanticType represents an engine-agnostic data type category.
type SemanticType string

const (
	TypeText      SemanticType = "text"
	TypeInteger   SemanticType = "integer"
	TypeBoolean   SemanticType = "boolean"
	TypeTimestamp SemanticType = "timestamp"
	TypeBlob      SemanticType = "blob"
	TypeJSON      SemanticType = "json"
	TypeNumeric   SemanticType = "numeric"
)

// PtrBool is a convenience helper returning a pointer to a bool value.
func PtrBool(b bool) *bool {
	return new(b)
}

// IsMissingRow reports whether err is or wraps sql.ErrNoRows.
func IsMissingRow(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// ColumnSpec describes expected column-level invariants.
type ColumnSpec struct {
	Name            string       `json:"name"`
	Type            SemanticType `json:"type,omitempty"`             // Optional semantic type category
	Nullable        *bool        `json:"nullable,omitempty"`         // Optional nullability expectation
	PrimaryKey      bool         `json:"primary_key,omitempty"`      // Part of primary key
	Default         string       `json:"default,omitempty"`          // Expected default fragment ("" means no assertion)
	DefaultValue    string       `json:"default_value,omitempty"`    // Deprecated alias / backwards compatibility for Default
	DefaultPostgres string       `json:"default_postgres,omitempty"` // Optional engine-specific default override for PostgreSQL
}

func (c ColumnSpec) expectedDefault(isPostgres bool) string {
	if isPostgres && c.DefaultPostgres != "" {
		return c.DefaultPostgres
	}
	if c.Default != "" {
		return c.Default
	}
	return c.DefaultValue
}

// ForeignKeySpec describes expected foreign key constraints.
type ForeignKeySpec struct {
	Name       string   `json:"name,omitempty"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns,omitempty"`
}

// UniqueConstraintSpec describes expected unique constraints.
type UniqueConstraintSpec struct {
	Name    string   `json:"name,omitempty"`
	Columns []string `json:"columns"`
}

// CheckConstraintSpec describes expected check constraints.
type CheckConstraintSpec struct {
	Name       string `json:"name,omitempty"`
	Expression string `json:"expression"` // Substring/fragment expected in check definition
}

// TableSpec describes expected table-level logical schema invariants.
type TableSpec struct {
	Name              string                 `json:"name"`
	Columns           []ColumnSpec           `json:"columns,omitempty"`
	PrimaryKey        []string               `json:"primary_key,omitempty"` // Compound/single PK column order
	ForeignKeys       []ForeignKeySpec       `json:"foreign_keys,omitempty"`
	UniqueConstraints []UniqueConstraintSpec `json:"unique_constraints,omitempty"`
	CheckConstraints  []CheckConstraintSpec  `json:"check_constraints,omitempty"`
}

// IndexSpec describes expected index invariants.
type IndexSpec struct {
	Name      string   `json:"name,omitempty"`      // Optional if unique signature is matched
	Table     string   `json:"table"`               // Table on which index resides
	Columns   []string `json:"columns,omitempty"`   // Column list (ordered)
	Unique    bool     `json:"unique,omitempty"`    // Whether the index is unique
	Predicate string   `json:"predicate,omitempty"` // WHERE clause fragment for partial indexes
}

// ImmutabilityProtection describes expected immutability protections (triggers or documented app-level enforcement).
type ImmutabilityProtection struct {
	Name         string `json:"name"`                     // Descriptive name
	Table        string `json:"table"`                    // Target table
	TriggerName  string `json:"trigger_name,omitempty"`   // Required trigger name in DB (if DB-enforced)
	AppLevelOnly bool   `json:"app_level_only,omitempty"` // If true, documented as app-level enforced (absent-by-design in DB)
	Description  string `json:"description,omitempty"`
}

// RetiredColumn represents a column on a table that must not exist.
type RetiredColumn struct {
	Table  string `json:"table"`
	Column string `json:"column"` // Exact name or prefix pattern (e.g. "reserved%nano" or glob)
}

// RetiredArtifacts records tables, columns, indexes, and triggers that must NOT exist.
type RetiredArtifacts struct {
	Tables   []string        `json:"tables,omitempty"`
	Columns  []RetiredColumn `json:"columns,omitempty"`
	Indexes  []string        `json:"indexes,omitempty"`
	Triggers []string        `json:"triggers,omitempty"`
}

// LogicalSchemaSpec represents declared logical schema invariants for a component.
type LogicalSchemaSpec struct {
	ComponentID string                   `json:"component_id,omitempty"`
	Tables      []TableSpec              `json:"tables,omitempty"`
	Indexes     []IndexSpec              `json:"indexes,omitempty"`
	Protections []ImmutabilityProtection `json:"protections,omitempty"`
	Retired     RetiredArtifacts         `json:"retired"`
}

func validateLogicalSchemaSpec(spec LogicalSchemaSpec) error {
	for _, tbl := range spec.Tables {
		if strings.TrimSpace(tbl.Name) == "" {
			return fmt.Errorf("dbparity: table spec has empty name")
		}
		for _, fk := range tbl.ForeignKeys {
			if len(fk.Columns) == 0 {
				return fmt.Errorf("dbparity: table %q: foreign key referencing %q has empty columns", tbl.Name, fk.RefTable)
			}
			for _, col := range fk.Columns {
				if strings.TrimSpace(col) == "" {
					return fmt.Errorf("dbparity: table %q: foreign key referencing %q contains empty column name", tbl.Name, fk.RefTable)
				}
			}
			if strings.TrimSpace(fk.RefTable) == "" {
				return fmt.Errorf("dbparity: table %q: foreign key has empty ref_table", tbl.Name)
			}
			if len(fk.RefColumns) > 0 && len(fk.RefColumns) != len(fk.Columns) {
				return fmt.Errorf("dbparity: table %q: foreign key referencing %q has mismatched column counts (columns=%d, ref_columns=%d)", tbl.Name, fk.RefTable, len(fk.Columns), len(fk.RefColumns))
			}
			for _, rcol := range fk.RefColumns {
				if strings.TrimSpace(rcol) == "" {
					return fmt.Errorf("dbparity: table %q: foreign key referencing %q contains empty ref column name", tbl.Name, fk.RefTable)
				}
			}
		}
		for _, uc := range tbl.UniqueConstraints {
			if len(uc.Columns) == 0 {
				return fmt.Errorf("dbparity: table %q: unique constraint has empty columns", tbl.Name)
			}
			for _, col := range uc.Columns {
				if strings.TrimSpace(col) == "" {
					return fmt.Errorf("dbparity: table %q: unique constraint contains empty column name", tbl.Name)
				}
			}
		}
	}
	for _, idx := range spec.Indexes {
		if len(idx.Columns) == 0 {
			if idx.Name != "" {
				return fmt.Errorf("dbparity: index %q on table %q has empty columns", idx.Name, idx.Table)
			}
			return fmt.Errorf("dbparity: table %q index has empty columns", idx.Table)
		}
		for _, col := range idx.Columns {
			if strings.TrimSpace(col) == "" {
				return fmt.Errorf("dbparity: index on table %q contains empty column name", idx.Table)
			}
		}
	}
	return nil
}

// VerifySchema verifies that the database matches the declared logical schema spec,
// dispatching to engine-native metadata probes based on the database dialect.
func VerifySchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}
	if err := validateLogicalSchemaSpec(spec); err != nil {
		return err
	}
	switch database.Dialect().Name() {
	case dialect.SQLite:
		return VerifySQLiteSchema(ctx, database, spec)
	case dialect.PG:
		return VerifyPostgresSchema(ctx, database, spec)
	default:
		return fmt.Errorf("dbparity: unsupported database dialect %q for schema verification", database.Dialect().Name())
	}
}

// VerifySQLiteSchema verifies the logical schema spec against a SQLite database
// using sqlite_master and PRAGMA table_info / index_list metadata introspection.
func VerifySQLiteSchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}
	if err := validateLogicalSchemaSpec(spec); err != nil {
		return err
	}

	// 1. Check retired artifacts
	for _, table := range spec.Retired.Tables {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired table %q: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired table %q is still present", table)
		}
	}

	for _, rc := range spec.Retired.Columns {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, rc.Table).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: sqlite check table for retired column %q: %w", rc.Table, err)
		}
		if tblCount == 0 {
			continue
		}
		cols, err := sqliteTableColumns(ctx, database, rc.Table)
		if err != nil {
			return err
		}
		for colName := range cols {
			if matchColumnNameOrPattern(rc.Column, colName) {
				return fmt.Errorf("dbparity: table %q contains retired column %q", rc.Table, colName)
			}
		}
	}

	for _, idx := range spec.Retired.Indexes {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired index %q: %w", idx, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired index %q is still present", idx)
		}
	}

	for _, trg := range spec.Retired.Triggers {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: sqlite check retired trigger %q: %w", trg, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired trigger %q is still present", trg)
		}
	}

	// 2. Check required tables & columns
	for _, tbl := range spec.Tables {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl.Name).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: sqlite check table %q: %w", tbl.Name, err)
		}
		if tblCount == 0 {
			return fmt.Errorf("dbparity: table %q not found", tbl.Name)
		}

		colMap, err := sqliteTableColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		var actualPKCols []string
		pkMap := make(map[int]string)
		var pkIndices []int
		for _, info := range colMap {
			if info.pk > 0 {
				pkMap[info.pk] = info.name
				pkIndices = append(pkIndices, info.pk)
			}
		}
		slices.Sort(pkIndices)
		for _, idx := range pkIndices {
			actualPKCols = append(actualPKCols, pkMap[idx])
		}

		for _, col := range tbl.Columns {
			info, exists := colMap[strings.ToLower(col.Name)]
			if !exists {
				return fmt.Errorf("dbparity: table %q: missing column %q", tbl.Name, col.Name)
			}

			if col.Type != "" {
				if !matchSQLiteSemanticType(col.Type, info.colType) {
					return fmt.Errorf("dbparity: table %q: column %q type mismatch: got %q, want category %q", tbl.Name, col.Name, info.colType, col.Type)
				}
			}

			if col.Nullable != nil {
				isNotNull := info.notnull != 0 || info.pk > 0
				if *col.Nullable && isNotNull {
					return fmt.Errorf("dbparity: table %q: column %q must be nullable", tbl.Name, col.Name)
				}
				if !*col.Nullable && !isNotNull {
					return fmt.Errorf("dbparity: table %q: column %q must be NOT NULL", tbl.Name, col.Name)
				}
			}

			if col.PrimaryKey && info.pk == 0 {
				return fmt.Errorf("dbparity: table %q: column %q must be part of primary key", tbl.Name, col.Name)
			}

			if expDefault := col.expectedDefault(false); expDefault != "" {
				if !info.dflt.Valid {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got no default, want %q", tbl.Name, col.Name, expDefault)
				}
				if normalizeDefault(info.dflt.String) != normalizeDefault(expDefault) {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got %q, want %q", tbl.Name, col.Name, info.dflt.String, expDefault)
				}
			}
		}

		if len(tbl.PrimaryKey) > 0 {
			if !equalFoldSlice(actualPKCols, tbl.PrimaryKey) {
				return fmt.Errorf("dbparity: table %q: primary key mismatch: got %v, want %v", tbl.Name, actualPKCols, tbl.PrimaryKey)
			}
		}

		// Introspect table DDL for check constraints and foreign keys
		var tableDDL string
		if err := database.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl.Name).Scan(ctx, &tableDDL); err != nil {
			return fmt.Errorf("dbparity: sqlite table definition %q: %w", tbl.Name, err)
		}
		lowerDDL := strings.ToLower(tableDDL)

		for _, chk := range tbl.CheckConstraints {
			if !strings.Contains(lowerDDL, strings.ToLower(chk.Expression)) {
				return fmt.Errorf("dbparity: table %q missing check constraint containing %q", tbl.Name, chk.Expression)
			}
		}

		if len(tbl.ForeignKeys) > 0 {
			fkGroups, err := sqliteGetForeignKeys(ctx, database, tbl.Name)
			if err != nil {
				return err
			}
			for _, fk := range tbl.ForeignKeys {
				fkMatched := false
				for _, entries := range fkGroups {
					if len(entries) == 0 {
						continue
					}
					slices.SortFunc(entries, func(a, b sqliteFKEntry) int {
						return a.seq - b.seq
					})
					if !strings.EqualFold(entries[0].refTable, fk.RefTable) {
						continue
					}
					var fromCols []string
					var toCols []string
					hasNullTo := false
					for _, e := range entries {
						fromCols = append(fromCols, e.fromColumn)
						if e.toColumn.Valid && strings.TrimSpace(e.toColumn.String) != "" {
							toCols = append(toCols, e.toColumn.String)
						} else {
							hasNullTo = true
						}
					}
					if !equalFoldSlice(fromCols, fk.Columns) {
						continue
					}
					if len(fk.RefColumns) > 0 {
						if hasNullTo {
							parentPKCols, err := sqlitePrimaryKeyColumns(ctx, database, fk.RefTable)
							if err != nil {
								return fmt.Errorf("dbparity: sqlite resolve parent PK for table %q: %w", fk.RefTable, err)
							}
							if len(parentPKCols) == 0 || !equalFoldSlice(parentPKCols, fk.RefColumns) {
								continue
							}
						} else {
							if !equalFoldSlice(toCols, fk.RefColumns) {
								continue
							}
						}
					}
					fkMatched = true
					break
				}
				if !fkMatched {
					return fmt.Errorf("dbparity: table %q missing foreign key referencing %q (%v)", tbl.Name, fk.RefTable, fk.Columns)
				}
			}
		}

		for _, uc := range tbl.UniqueConstraints {
			hasUC, err := sqliteHasUniqueConstraint(ctx, database, tbl.Name, uc.Columns)
			if err != nil {
				return err
			}
			if !hasUC {
				return fmt.Errorf("dbparity: table %q missing unique constraint on %v", tbl.Name, uc.Columns)
			}
		}
	}

	// 3. Check required indexes
	for _, idx := range spec.Indexes {
		if idx.Name != "" {
			var tblName string
			var idxSQL sql.NullString
			if err := database.NewRaw(`SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?`, idx.Name).Scan(ctx, &tblName, &idxSQL); err != nil || tblName == "" {
				if err != nil && !IsMissingRow(err) {
					return fmt.Errorf("dbparity: sqlite check index %q: %w", idx.Name, err)
				}
				return fmt.Errorf("dbparity: missing SQLite index %q on table %q", idx.Name, idx.Table)
			}

			if !strings.EqualFold(tblName, idx.Table) {
				return fmt.Errorf("dbparity: SQLite index %q owning table mismatch: got table %q, want table %q", idx.Name, tblName, idx.Table)
			}

			entries, err := sqliteGetIndexList(ctx, database, tblName)
			if err != nil {
				return fmt.Errorf("dbparity: sqlite get index list for %q: %w", tblName, err)
			}
			var foundEntry *sqliteIndexEntry
			for _, entry := range entries {
				if strings.EqualFold(entry.name, idx.Name) {
					e := entry
					foundEntry = &e
					break
				}
			}
			if foundEntry == nil {
				return fmt.Errorf("dbparity: SQLite index %q not found in table %q index list", idx.Name, idx.Table)
			}

			if foundEntry.unique != idx.Unique {
				return fmt.Errorf("dbparity: SQLite index %q on table %q uniqueness mismatch: got unique=%v, want unique=%v", idx.Name, idx.Table, foundEntry.unique, idx.Unique)
			}

			if len(idx.Columns) > 0 {
				actualCols, err := sqliteIndexColumns(ctx, database, idx.Name)
				if err != nil {
					return fmt.Errorf("dbparity: sqlite query columns for index %q: %w", idx.Name, err)
				}
				if !equalFoldSlice(actualCols, idx.Columns) {
					return fmt.Errorf("dbparity: SQLite index %q on table %q columns mismatch: got %v, want %v", idx.Name, idx.Table, actualCols, idx.Columns)
				}
			}

			if idx.Predicate != "" {
				if !foundEntry.partial {
					return fmt.Errorf("dbparity: SQLite index %q on table %q missing predicate: expected %q, but index is not partial", idx.Name, idx.Table, idx.Predicate)
				}
				if !idxSQL.Valid || !matchPredicate(idx.Predicate, idxSQL.String) {
					return fmt.Errorf("dbparity: SQLite index %q on table %q predicate mismatch: got %q, want matching %q", idx.Name, idx.Table, extractWhereClause(idxSQL.String), idx.Predicate)
				}
			} else if foundEntry.partial {
				return fmt.Errorf("dbparity: SQLite index %q on table %q unexpected partial predicate: got %q, want non-partial index", idx.Name, idx.Table, extractWhereClause(idxSQL.String))
			}
		} else {
			matched, err := sqliteHasIndexMatching(ctx, database, idx.Table, idx.Columns, idx.Unique, idx.Predicate)
			if err != nil {
				return fmt.Errorf("dbparity: sqlite check unnamed index on table %q (%v): %w", idx.Table, idx.Columns, err)
			}
			if !matched {
				return fmt.Errorf("dbparity: table %q missing index on %v (unique=%v)", idx.Table, idx.Columns, idx.Unique)
			}
		}
	}

	// 4. Check immutability protections
	for _, p := range spec.Protections {
		if p.AppLevelOnly {
			continue
		}
		if p.TriggerName != "" {
			var count int
			if err := database.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, p.TriggerName).Scan(ctx, &count); err != nil || count == 0 {
				if err != nil {
					return fmt.Errorf("dbparity: sqlite check trigger %q: %w", p.TriggerName, err)
				}
				return fmt.Errorf("dbparity: missing SQLite immutability trigger %q on table %q", p.TriggerName, p.Table)
			}
		}
	}

	return nil
}

// VerifyPostgresSchema verifies the logical schema spec against a PostgreSQL database
// using information_schema, pg_indexes, pg_constraint, and pg_trigger metadata introspection.
func VerifyPostgresSchema(ctx context.Context, database *bun.DB, spec LogicalSchemaSpec) error {
	if ctx == nil {
		return fmt.Errorf("dbparity: nil context")
	}
	if database == nil {
		return fmt.Errorf("dbparity: nil database")
	}
	if err := validateLogicalSchemaSpec(spec); err != nil {
		return err
	}

	// 1. Check retired artifacts
	for _, table := range spec.Retired.Tables {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired table %q: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired table %q is still present", table)
		}
	}

	for _, rc := range spec.Retired.Columns {
		var colNames []string
		rows, err := database.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`, rc.Table)
		if err != nil {
			return fmt.Errorf("dbparity: postgres check retired column in table %q: %w", rc.Table, err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return fmt.Errorf("dbparity: postgres scan column: %w", err)
			}
			colNames = append(colNames, name)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("dbparity: postgres query retired columns rows for table %q: %w", rc.Table, err)
		}
		_ = rows.Close()

		for _, colName := range colNames {
			if matchColumnNameOrPattern(rc.Column, colName) {
				return fmt.Errorf("dbparity: table %q contains retired column %q", rc.Table, colName)
			}
		}
	}

	for _, idx := range spec.Retired.Indexes {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, idx).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired index %q: %w", idx, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired index %q is still present", idx)
		}
	}

	for _, trg := range spec.Retired.Triggers {
		var count int
		if err := database.NewRaw(`SELECT COUNT(1) FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND tr.tgname = ? AND NOT tr.tgisinternal`, trg).Scan(ctx, &count); err != nil {
			return fmt.Errorf("dbparity: postgres check retired trigger %q: %w", trg, err)
		}
		if count > 0 {
			return fmt.Errorf("dbparity: retired trigger %q is still present", trg)
		}
	}

	// 2. Check required tables & columns
	for _, tbl := range spec.Tables {
		var tblCount int
		if err := database.NewRaw(`SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, tbl.Name).Scan(ctx, &tblCount); err != nil {
			return fmt.Errorf("dbparity: postgres check table %q: %w", tbl.Name, err)
		}
		if tblCount == 0 {
			return fmt.Errorf("dbparity: table %q not found", tbl.Name)
		}

		colMap, err := postgresTableColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		actualPKCols, err := postgresPrimaryKeyColumns(ctx, database, tbl.Name)
		if err != nil {
			return err
		}

		for _, col := range tbl.Columns {
			info, exists := colMap[strings.ToLower(col.Name)]
			if !exists {
				return fmt.Errorf("dbparity: table %q: missing column %q", tbl.Name, col.Name)
			}

			if col.Type != "" {
				if !matchPostgresSemanticType(col.Type, info.dataType, info.udtName) {
					return fmt.Errorf("dbparity: table %q: column %q type mismatch: got %q (%q), want category %q", tbl.Name, col.Name, info.dataType, info.udtName, col.Type)
				}
			}

			if col.Nullable != nil {
				if *col.Nullable && strings.EqualFold(info.isNullable, "NO") {
					return fmt.Errorf("dbparity: table %q: column %q must be nullable", tbl.Name, col.Name)
				}
				if !*col.Nullable && strings.EqualFold(info.isNullable, "YES") {
					return fmt.Errorf("dbparity: table %q: column %q must be NOT NULL", tbl.Name, col.Name)
				}
			}

			if col.PrimaryKey && !slices.Contains(actualPKCols, strings.ToLower(col.Name)) {
				return fmt.Errorf("dbparity: table %q: column %q must be part of primary key", tbl.Name, col.Name)
			}

			if expDefault := col.expectedDefault(true); expDefault != "" {
				if !info.columnDefault.Valid {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got no default, want %q", tbl.Name, col.Name, expDefault)
				}
				if normalizeDefault(info.columnDefault.String) != normalizeDefault(expDefault) {
					return fmt.Errorf("dbparity: table %q: column %q default mismatch: got %q, want %q", tbl.Name, col.Name, info.columnDefault.String, expDefault)
				}
			}
		}

		if len(tbl.PrimaryKey) > 0 {
			if !equalFoldSlice(actualPKCols, tbl.PrimaryKey) {
				return fmt.Errorf("dbparity: table %q: primary key mismatch: got %v, want %v", tbl.Name, actualPKCols, tbl.PrimaryKey)
			}
		}

		// Check Constraints
		if len(tbl.CheckConstraints) > 0 {
			checkDefs, err := postgresConstraintDefs(ctx, database, tbl.Name, "c")
			if err != nil {
				return err
			}
			for _, chk := range tbl.CheckConstraints {
				found := false
				for _, def := range checkDefs {
					if strings.Contains(strings.ToLower(def), strings.ToLower(chk.Expression)) {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("dbparity: table %q missing check constraint containing %q", tbl.Name, chk.Expression)
				}
			}
		}

		// Foreign Keys
		if len(tbl.ForeignKeys) > 0 {
			fks, err := postgresGetForeignKeys(ctx, database, tbl.Name)
			if err != nil {
				return err
			}
			for _, fk := range tbl.ForeignKeys {
				matched := false
				for _, actual := range fks {
					if !strings.EqualFold(actual.refTable, fk.RefTable) {
						continue
					}
					if !equalFoldSlice(actual.localCols, fk.Columns) {
						continue
					}
					if len(fk.RefColumns) > 0 {
						if !equalFoldSlice(actual.refCols, fk.RefColumns) {
							continue
						}
					}
					matched = true
					break
				}
				if !matched {
					return fmt.Errorf("dbparity: table %q missing foreign key referencing %q (%v)", tbl.Name, fk.RefTable, fk.Columns)
				}
			}
		}

		// Unique Constraints
		for _, uc := range tbl.UniqueConstraints {
			hasUC, err := postgresHasUniqueConstraint(ctx, database, tbl.Name, uc.Columns)
			if err != nil {
				return err
			}
			if !hasUC {
				return fmt.Errorf("dbparity: table %q missing unique constraint on %v", tbl.Name, uc.Columns)
			}
		}
	}

	// 3. Check required indexes
	for _, idx := range spec.Indexes {
		if idx.Name != "" {
			info, err := postgresGetIndexDetailed(ctx, database, idx.Name)
			if err != nil || info == nil {
				if err != nil {
					return fmt.Errorf("dbparity: postgres check index %q: %w", idx.Name, err)
				}
				return fmt.Errorf("dbparity: missing PostgreSQL index %q on table %q", idx.Name, idx.Table)
			}

			if !strings.EqualFold(info.tableName, idx.Table) {
				return fmt.Errorf("dbparity: PostgreSQL index %q owning table mismatch: got table %q, want table %q", idx.Name, info.tableName, idx.Table)
			}

			if !info.isValid {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q is invalid (indisvalid = false)", idx.Name, idx.Table)
			}
			if !info.isReady {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q is not ready (indisready = false)", idx.Name, idx.Table)
			}

			if info.unique != idx.Unique {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q uniqueness mismatch: got unique=%v, want unique=%v", idx.Name, idx.Table, info.unique, idx.Unique)
			}

			if len(idx.Columns) > 0 {
				if !equalFoldSlice(info.columns, idx.Columns) {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q columns mismatch: got %v, want %v", idx.Name, idx.Table, info.columns, idx.Columns)
				}
			}

			if idx.Predicate != "" {
				if !info.partial {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q missing predicate: expected %q, but index is not partial", idx.Name, idx.Table, idx.Predicate)
				}
				predSource := info.predicate
				if predSource == "" {
					predSource = info.indexDef
				}
				if !matchPredicate(idx.Predicate, predSource) {
					return fmt.Errorf("dbparity: PostgreSQL index %q on table %q predicate mismatch: got %q, want matching %q", idx.Name, idx.Table, info.predicate, idx.Predicate)
				}
			} else if info.partial {
				return fmt.Errorf("dbparity: PostgreSQL index %q on table %q unexpected partial predicate: got %q, want non-partial index", idx.Name, idx.Table, info.predicate)
			}
		} else {
			matched, err := postgresHasIndexMatching(ctx, database, idx.Table, idx.Columns, idx.Unique, idx.Predicate)
			if err != nil {
				return fmt.Errorf("dbparity: postgres check unnamed index on table %q (%v): %w", idx.Table, idx.Columns, err)
			}
			if !matched {
				return fmt.Errorf("dbparity: table %q missing index on %v (unique=%v)", idx.Table, idx.Columns, idx.Unique)
			}
		}
	}

	// 4. Check immutability protections
	for _, p := range spec.Protections {
		if p.AppLevelOnly {
			continue
		}
		if p.TriggerName != "" {
			var count int
			if err := database.NewRaw(`SELECT COUNT(1) FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = ? AND tr.tgname = ? AND NOT tr.tgisinternal`, p.Table, p.TriggerName).Scan(ctx, &count); err != nil || count == 0 {
				if err != nil {
					return fmt.Errorf("dbparity: postgres check trigger %q on table %q: %w", p.TriggerName, p.Table, err)
				}
				return fmt.Errorf("dbparity: missing PostgreSQL immutability trigger %q on table %q", p.TriggerName, p.Table)
			}
		}
	}

	return nil
}

type sqliteColInfo struct {
	cid     int
	name    string
	colType string
	notnull int
	dflt    sql.NullString
	pk      int
}

type sqliteFKEntry struct {
	id         int
	seq        int
	refTable   string
	fromColumn string
	toColumn   sql.NullString
}

func sqliteGetForeignKeys(ctx context.Context, database *bun.DB, tableName string) (map[int][]sqliteFKEntry, error) {
	rows, err := database.QueryContext(ctx, `SELECT id, seq, "table", "from", "to" FROM pragma_foreign_key_list(?)`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: sqlite foreign_key_list %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	grouped := make(map[int][]sqliteFKEntry)
	for rows.Next() {
		var entry sqliteFKEntry
		if err := rows.Scan(&entry.id, &entry.seq, &entry.refTable, &entry.fromColumn, &entry.toColumn); err != nil {
			return nil, fmt.Errorf("dbparity: sqlite scan foreign_key_list %q: %w", tableName, err)
		}
		grouped[entry.id] = append(grouped[entry.id], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: sqlite foreign_key_list rows %q: %w", tableName, err)
	}
	return grouped, nil
}

func sqlitePrimaryKeyColumns(ctx context.Context, database *bun.DB, tableName string) ([]string, error) {
	colMap, err := sqliteTableColumns(ctx, database, tableName)
	if err != nil {
		return nil, err
	}
	pkMap := make(map[int]string)
	var pkIndices []int
	for _, info := range colMap {
		if info.pk > 0 {
			pkMap[info.pk] = info.name
			pkIndices = append(pkIndices, info.pk)
		}
	}
	slices.Sort(pkIndices)
	var pkCols []string
	for _, idx := range pkIndices {
		pkCols = append(pkCols, pkMap[idx])
	}
	return pkCols, nil
}

func sqliteTableColumns(ctx context.Context, database *bun.DB, tableName string) (map[string]sqliteColInfo, error) {
	rows, err := database.QueryContext(ctx, `SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: sqlite table_info %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	cols := make(map[string]sqliteColInfo)
	for rows.Next() {
		var info sqliteColInfo
		if err := rows.Scan(&info.cid, &info.name, &info.colType, &info.notnull, &info.dflt, &info.pk); err != nil {
			return nil, fmt.Errorf("dbparity: sqlite scan table_info %q: %w", tableName, err)
		}
		cols[strings.ToLower(info.name)] = info
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: sqlite table_info rows %q: %w", tableName, err)
	}
	return cols, nil
}

type sqliteIndexEntry struct {
	name    string
	unique  bool
	partial bool
}

func sqliteGetIndexList(ctx context.Context, database *bun.DB, tableName string) ([]sqliteIndexEntry, error) {
	rows, err := database.QueryContext(ctx, `SELECT seq, name, "unique", origin, partial FROM pragma_index_list(?)`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: sqlite index_list %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	var entries []sqliteIndexEntry
	for rows.Next() {
		var seq int
		var idxName string
		var isUnique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &idxName, &isUnique, &origin, &partial); err != nil {
			return nil, fmt.Errorf("dbparity: sqlite scan index_list %q: %w", tableName, err)
		}
		entries = append(entries, sqliteIndexEntry{
			name:    idxName,
			unique:  isUnique == 1,
			partial: partial == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: sqlite index_list rows %q: %w", tableName, err)
	}
	return entries, nil
}

func sqliteHasUniqueConstraint(ctx context.Context, database *bun.DB, tableName string, columns []string) (bool, error) {
	entries, err := sqliteGetIndexList(ctx, database, tableName)
	if err != nil {
		return false, fmt.Errorf("dbparity: sqlite get index list for %q: %w", tableName, err)
	}
	for _, entry := range entries {
		if entry.unique && !entry.partial {
			idxCols, err := sqliteIndexColumns(ctx, database, entry.name)
			if err != nil {
				return false, fmt.Errorf("dbparity: sqlite index columns for %q: %w", entry.name, err)
			}
			if equalFoldSlice(idxCols, columns) {
				return true, nil
			}
		}
	}
	return false, nil
}

func sqliteIndexColumns(ctx context.Context, database *bun.DB, indexName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT seqno, cid, name, "key" FROM pragma_index_xinfo(?) WHERE "key" = 1 ORDER BY seqno`, indexName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: sqlite index_xinfo %q: %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var seqno, cid, isKey int
		var name sql.NullString
		if err := rows.Scan(&seqno, &cid, &name, &isKey); err != nil {
			return nil, fmt.Errorf("dbparity: sqlite scan index_xinfo %q: %w", indexName, err)
		}
		if isKey != 1 {
			continue
		}
		if cid < 0 || !name.Valid || name.String == "" {
			cols = append(cols, "")
		} else {
			cols = append(cols, name.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: sqlite index_xinfo rows %q: %w", indexName, err)
	}
	return cols, nil
}

func sqliteHasIndexMatching(ctx context.Context, database *bun.DB, tableName string, columns []string, unique bool, predicate string) (bool, error) {
	entries, err := sqliteGetIndexList(ctx, database, tableName)
	if err != nil {
		return false, fmt.Errorf("dbparity: sqlite get index list for %q: %w", tableName, err)
	}

	for _, entry := range entries {
		if entry.unique != unique {
			continue
		}
		if predicate != "" && !entry.partial {
			continue
		}
		if predicate == "" && entry.partial {
			continue
		}
		idxCols, err := sqliteIndexColumns(ctx, database, entry.name)
		if err != nil {
			return false, fmt.Errorf("dbparity: sqlite get index columns for %q: %w", entry.name, err)
		}
		if !equalFoldSlice(idxCols, columns) {
			continue
		}
		if predicate != "" {
			var idxSQL sql.NullString
			if err := database.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, entry.name).Scan(ctx, &idxSQL); err != nil {
				return false, fmt.Errorf("dbparity: sqlite get index definition for %q: %w", entry.name, err)
			}
			if !idxSQL.Valid || !matchPredicate(predicate, idxSQL.String) {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

type pgColInfo struct {
	columnName    string
	dataType      string
	udtName       string
	isNullable    string
	columnDefault sql.NullString
}

func postgresTableColumns(ctx context.Context, database *bun.DB, tableName string) (map[string]pgColInfo, error) {
	rows, err := database.QueryContext(ctx, `
SELECT column_name, data_type, udt_name, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = ?`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query columns for table %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	cols := make(map[string]pgColInfo)
	for rows.Next() {
		var info pgColInfo
		if err := rows.Scan(&info.columnName, &info.dataType, &info.udtName, &info.isNullable, &info.columnDefault); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan column for table %q: %w", tableName, err)
		}
		cols[strings.ToLower(info.columnName)] = info
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: postgres table columns rows for %q: %w", tableName, err)
	}
	return cols, nil
}

func postgresPrimaryKeyColumns(ctx context.Context, database *bun.DB, tableName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
WHERE tc.table_schema = current_schema()
  AND tc.table_name = ?
  AND tc.constraint_type = 'PRIMARY KEY'
ORDER BY kcu.ordinal_position`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query PK for table %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan PK column: %w", err)
		}
		cols = append(cols, strings.ToLower(col))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: postgres PK columns rows for %q: %w", tableName, err)
	}
	return cols, nil
}

func postgresConstraintDefs(ctx context.Context, database *bun.DB, tableName string, contype string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?
  AND c.contype = ?`, tableName, contype)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query constraint %q for table %q: %w", contype, tableName, err)
	}
	defer func() { _ = rows.Close() }()

	var defs []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan constraint def: %w", err)
		}
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: postgres constraint def rows for %q: %w", tableName, err)
	}
	return defs, nil
}

func matchSQLiteSemanticType(expected SemanticType, rawType string) bool {
	upper := strings.ToUpper(strings.TrimSpace(rawType))
	switch expected {
	case TypeText:
		return strings.Contains(upper, "TEXT") ||
			strings.Contains(upper, "CHAR") ||
			strings.Contains(upper, "CLOB") ||
			strings.Contains(upper, "STRING")
	case TypeInteger:
		return strings.Contains(upper, "INT")
	case TypeBoolean:
		return strings.Contains(upper, "BOOL") || strings.Contains(upper, "INT")
	case TypeTimestamp:
		return strings.Contains(upper, "TIME") ||
			strings.Contains(upper, "DATE") ||
			strings.Contains(upper, "INT")
	case TypeBlob:
		return strings.Contains(upper, "BLOB") ||
			strings.Contains(upper, "BINARY") ||
			strings.Contains(upper, "BYTEA") ||
			strings.Contains(upper, "RAW")
	case TypeJSON:
		return strings.Contains(upper, "JSON") ||
			strings.Contains(upper, "TEXT") ||
			strings.Contains(upper, "CHAR")
	case TypeNumeric:
		return strings.Contains(upper, "REAL") ||
			strings.Contains(upper, "FLOA") ||
			strings.Contains(upper, "DOUB") || //nolint:misspell // SQLite type affinity matching rule for DOUBLE ("DOUB")
			strings.Contains(upper, "NUM") ||
			strings.Contains(upper, "DEC")
	default:
		return strings.Contains(upper, strings.ToUpper(string(expected)))
	}
}

func matchPostgresSemanticType(expected SemanticType, dataType, udtName string) bool {
	dt := strings.ToLower(strings.TrimSpace(dataType))
	udt := strings.ToLower(strings.TrimSpace(udtName))
	switch expected {
	case TypeText:
		return dt == "text" ||
			strings.Contains(dt, "char") ||
			strings.Contains(dt, "varchar") ||
			udt == "text" || udt == "varchar" || udt == "citext" || udt == "name"
	case TypeInteger:
		return strings.Contains(dt, "int") ||
			strings.Contains(dt, "serial") ||
			strings.Contains(udt, "int") ||
			strings.Contains(udt, "serial")
	case TypeBoolean:
		return strings.Contains(dt, "bool") || strings.Contains(udt, "bool")
	case TypeTimestamp:
		return strings.Contains(dt, "time") ||
			strings.Contains(dt, "date") ||
			strings.Contains(udt, "time") ||
			strings.Contains(udt, "date") ||
			strings.Contains(dt, "int")
	case TypeBlob:
		return dt == "bytea" || udt == "bytea" || strings.Contains(dt, "blob")
	case TypeJSON:
		return strings.Contains(dt, "json") ||
			strings.Contains(udt, "json") ||
			dt == "text" || udt == "text"
	case TypeNumeric:
		return strings.Contains(dt, "numeric") ||
			strings.Contains(dt, "decimal") ||
			strings.Contains(dt, "real") ||
			strings.Contains(dt, "double") ||
			strings.Contains(dt, "float") ||
			strings.Contains(udt, "numeric") ||
			strings.Contains(udt, "float")
	default:
		return dt == strings.ToLower(string(expected)) || udt == strings.ToLower(string(expected))
	}
}

func matchColumnNameOrPattern(pattern, name string) bool {
	p := strings.ToLower(pattern)
	n := strings.ToLower(name)
	if p == n {
		return true
	}
	if strings.Contains(p, "%") {
		globPattern := strings.ReplaceAll(p, "%", "*")
		matched, err := filepath.Match(globPattern, n)
		if err == nil && matched {
			return true
		}
	}
	if strings.Contains(p, "*") {
		matched, err := filepath.Match(p, n)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func equalFoldSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

type pgFKInfo struct {
	name      string
	refTable  string
	localCols []string
	refCols   []string
}

func postgresGetForeignKeys(ctx context.Context, database *bun.DB, tableName string) ([]pgFKInfo, error) {
	rows, err := database.QueryContext(ctx, `
SELECT
    c.conname AS constraint_name,
    ft.relname AS ref_table,
    (
        SELECT COALESCE(json_agg(a.attname::text ORDER BY k.ord), '[]'::json)
        FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
    )::text AS local_columns_json,
    (
        SELECT COALESCE(json_agg(fa.attname::text ORDER BY fk.ord), '[]'::json)
        FROM unnest(c.confkey) WITH ORDINALITY AS fk(attnum, ord)
        JOIN pg_attribute fa ON fa.attrelid = c.confrelid AND fa.attnum = fk.attnum
    )::text AS ref_columns_json
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_class ft ON ft.oid = c.confrelid
JOIN pg_namespace fn ON fn.oid = ft.relnamespace
WHERE n.nspname = current_schema()
  AND fn.nspname = current_schema()
  AND t.relname = ?
  AND c.contype = 'f'`, tableName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query foreign keys for %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	var fks []pgFKInfo
	for rows.Next() {
		var name, refTable, localJSON, refJSON string
		if err := rows.Scan(&name, &refTable, &localJSON, &refJSON); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan foreign key for %q: %w", tableName, err)
		}
		var localCols, refCols []string
		if err := json.Unmarshal([]byte(localJSON), &localCols); err != nil {
			return nil, fmt.Errorf("dbparity: postgres unmarshal local columns for FK %q: %w", name, err)
		}
		if err := json.Unmarshal([]byte(refJSON), &refCols); err != nil {
			return nil, fmt.Errorf("dbparity: postgres unmarshal ref columns for FK %q: %w", name, err)
		}
		fks = append(fks, pgFKInfo{
			name:      name,
			refTable:  refTable,
			localCols: localCols,
			refCols:   refCols,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: postgres foreign keys rows for %q: %w", tableName, err)
	}
	return fks, nil
}

func postgresHasUniqueConstraint(ctx context.Context, database *bun.DB, tableName string, columns []string) (bool, error) {
	// 1. Check pg_constraint where contype = 'u'
	matchedConstraint, err := func() (bool, error) {
		rows, err := database.QueryContext(ctx, `
SELECT
    c.conname AS constraint_name,
    (
        SELECT COALESCE(json_agg(a.attname::text ORDER BY k.ord), '[]'::json)
        FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
    )::text AS columns_json
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?
  AND c.contype = 'u'`, tableName)
		if err != nil {
			return false, fmt.Errorf("dbparity: postgres query unique constraints for %q: %w", tableName, err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var name, colsJSON string
			if err := rows.Scan(&name, &colsJSON); err != nil {
				return false, fmt.Errorf("dbparity: postgres scan unique constraint for %q: %w", tableName, err)
			}
			var cols []string
			if err := json.Unmarshal([]byte(colsJSON), &cols); err != nil {
				return false, fmt.Errorf("dbparity: postgres unmarshal unique columns for %q: %w", name, err)
			}
			if equalFoldSlice(cols, columns) {
				return true, nil
			}
		}
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("dbparity: postgres unique constraint rows for %q: %w", tableName, err)
		}
		return false, nil
	}()
	if err != nil {
		return false, err
	}
	if matchedConstraint {
		return true, nil
	}

	// 2. Also check unique indexes (non-partial) on the table; ignore INCLUDE columns (k.ord <= i.indnkeyatts)
	// and preserve expression positions via LEFT JOIN.
	matchedIndex, err := func() (bool, error) {
		idxRows, err := database.QueryContext(ctx, `
SELECT
    c.relname AS index_name,
    (
        SELECT COALESCE(json_agg(COALESCE(a.attname::text, '') ORDER BY k.ord), '[]'::json)
        FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
        LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum AND k.attnum > 0
        WHERE k.ord <= i.indnkeyatts
    )::text AS columns_json
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?
  AND i.indisunique = true
  AND i.indpred IS NULL
  AND i.indisvalid = true
  AND i.indisready = true`, tableName)
		if err != nil {
			return false, fmt.Errorf("dbparity: postgres query unique indexes for %q: %w", tableName, err)
		}
		defer func() { _ = idxRows.Close() }()

		for idxRows.Next() {
			var name, colsJSON string
			if err := idxRows.Scan(&name, &colsJSON); err != nil {
				return false, fmt.Errorf("dbparity: postgres scan unique index for %q: %w", tableName, err)
			}
			var cols []string
			if err := json.Unmarshal([]byte(colsJSON), &cols); err != nil {
				return false, fmt.Errorf("dbparity: postgres unmarshal unique index columns for %q: %w", name, err)
			}
			if equalFoldSlice(cols, columns) {
				return true, nil
			}
		}
		if err := idxRows.Err(); err != nil {
			return false, fmt.Errorf("dbparity: postgres unique index rows for %q: %w", tableName, err)
		}
		return false, nil
	}()
	if err != nil {
		return false, err
	}
	return matchedIndex, nil
}

type pgIndexDetailed struct {
	name      string
	tableName string
	unique    bool
	partial   bool
	isValid   bool
	isReady   bool
	columns   []string
	predicate string
	indexDef  string
}

func postgresGetIndexDetailed(ctx context.Context, database *bun.DB, indexName string) (*pgIndexDetailed, error) {
	var info pgIndexDetailed
	row := database.QueryRowContext(ctx, `
SELECT
    c.relname AS index_name,
    t.relname AS table_name,
    i.indisunique AS is_unique,
    i.indpred IS NOT NULL AS is_partial,
    i.indisvalid AS is_valid,
    i.indisready AS is_ready,
    COALESCE(pg_get_expr(i.indpred, i.indrelid), '') AS predicate,
    COALESCE(pi.indexdef, '') AS indexdef
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_indexes pi ON pi.schemaname = n.nspname AND pi.indexname = c.relname
WHERE n.nspname = current_schema()
  AND c.relname = ?`, indexName)

	if err := row.Scan(&info.name, &info.tableName, &info.unique, &info.partial, &info.isValid, &info.isReady, &info.predicate, &info.indexDef); err != nil {
		if IsMissingRow(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("dbparity: postgres query index %q: %w", indexName, err)
	}

	cols, err := postgresIndexColumns(ctx, database, indexName)
	if err != nil {
		return nil, err
	}
	info.columns = cols
	return &info, nil
}

func postgresIndexColumns(ctx context.Context, database *bun.DB, indexName string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `
SELECT COALESCE(a.attname, '')
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON k.ord <= i.indnkeyatts
LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum AND k.attnum > 0
WHERE n.nspname = current_schema()
  AND c.relname = ?
ORDER BY k.ord`, indexName)
	if err != nil {
		return nil, fmt.Errorf("dbparity: postgres query index columns for %q: %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("dbparity: postgres scan index column: %w", err)
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbparity: postgres index columns rows for %q: %w", indexName, err)
	}
	return cols, nil
}

func postgresHasIndexMatching(ctx context.Context, database *bun.DB, tableName string, columns []string, unique bool, predicate string) (bool, error) {
	indexNames, err := func() ([]string, error) {
		rows, err := database.QueryContext(ctx, `
SELECT c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = ?`, tableName)
		if err != nil {
			return nil, fmt.Errorf("dbparity: postgres query index names for table %q: %w", tableName, err)
		}
		defer func() { _ = rows.Close() }()

		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, fmt.Errorf("dbparity: postgres scan index name for table %q: %w", tableName, err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dbparity: postgres query index names rows for %q: %w", tableName, err)
		}
		return names, nil
	}()
	if err != nil {
		return false, err
	}

	for _, idxName := range indexNames {
		info, err := postgresGetIndexDetailed(ctx, database, idxName)
		if err != nil {
			return false, err
		}
		if info == nil {
			continue
		}
		if !info.isValid || !info.isReady {
			continue
		}
		if info.unique != unique {
			continue
		}
		if predicate != "" && !info.partial {
			continue
		}
		if predicate == "" && info.partial {
			continue
		}
		if !equalFoldSlice(info.columns, columns) {
			continue
		}
		if predicate != "" {
			predSource := info.predicate
			if predSource == "" {
				predSource = info.indexDef
			}
			if !matchPredicate(predicate, predSource) {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

var typeCastRegex = regexp.MustCompile(`::[a-zA-Z0-9_]+`)

func normalizePredicate(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if idx := strings.Index(s, "where "); idx >= 0 {
		s = s[idx+len("where "):]
	}
	s = typeCastRegex.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "!=", " <> ")
	s = strings.ReplaceAll(s, "(", " ")
	s = strings.ReplaceAll(s, ")", " ")
	s = strings.ReplaceAll(s, "<>", " __NE__ ")
	s = strings.ReplaceAll(s, "<=", " __LE__ ")
	s = strings.ReplaceAll(s, ">=", " __GE__ ")
	s = strings.ReplaceAll(s, "=", " = ")
	s = strings.ReplaceAll(s, "<", " < ")
	s = strings.ReplaceAll(s, ">", " > ")
	s = strings.ReplaceAll(s, "__NE__", " <> ")
	s = strings.ReplaceAll(s, "__LE__", " <= ")
	s = strings.ReplaceAll(s, "__GE__", " >= ")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func matchPredicate(expected, actual string) bool {
	normExpected := normalizePredicate(expected)
	normActual := normalizePredicate(actual)
	if normExpected == "" && normActual == "" {
		return true
	}
	if normExpected == "" || normActual == "" {
		return false
	}
	return normExpected == normActual
}

func extractWhereClause(sqlStr string) string {
	if idx := strings.Index(strings.ToLower(sqlStr), "where "); idx >= 0 {
		return strings.TrimSpace(sqlStr[idx+len("where "):])
	}
	return strings.TrimSpace(sqlStr)
}

func normalizeDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = typeCastRegex.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		inner = typeCastRegex.ReplaceAllString(inner, "")
		s = strings.TrimSpace(inner)
	}

	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2 {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, "''", "'")
	}
	s = strings.TrimSpace(s)

	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		s = strings.TrimSpace(inner)
	}

	if intVal, err := strconv.ParseInt(s, 10, 64); err == nil {
		s = strconv.FormatInt(intVal, 10)
	} else if floatVal, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "eE") {
		s = strconv.FormatFloat(floatVal, 'f', -1, 64)
	}

	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

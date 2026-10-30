package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

var versionedMigrationRegex = regexp.MustCompile(`^\d{14}_.*\.go$`)

// DialectFinding records a discovered database dialect indicator in production source code.
type DialectFinding struct {
	File   string
	Line   int
	Kind   string
	Detail string
}

func (f DialectFinding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", f.File, f.Line, f.Kind, f.Detail)
}

// DiscoverMigrationRoots walks production trees to find directories containing versioned Bun migration files.
func DiscoverMigrationRoots(repoRoot string) (map[string][]string, error) {
	return DiscoverMigrationRootsIn(repoRoot, ProductionScanRoots())
}

// DiscoverDialectSensitivePackages scans production Go files and groups dialect-sensitive findings by package directory.
func DiscoverDialectSensitivePackages(repoRoot string) (map[string][]DialectFinding, error) {
	findingsByPkg := make(map[string][]DialectFinding)
	err := WalkProductionGoFiles(repoRoot, func(rel, abs string, src []byte) error {
		// Skip test harness and archtest packages
		if isTestSupportPackage(rel) {
			return nil
		}
		findings, err := inspectFileDialectIndicators(abs, rel, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if len(findings) > 0 {
			pkgDir := PackageDirFromRel(rel)
			findingsByPkg[pkgDir] = append(findingsByPkg[pkgDir], findings...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findingsByPkg, nil
}

var dialectSensitiveImports = map[string]string{
	"github.com/uptrace/bun/dialect/sqlitedialect": "sqlite dialect import",
	"github.com/uptrace/bun/dialect/pgdialect":     "postgres dialect import",
	"github.com/uptrace/bun/driver/pgdriver":       "postgres driver import",
	"modernc.org/sqlite":                           "sqlite driver import",
	"modernc.org/sqlite/lib":                       "sqlite driver library import",
	"github.com/uptrace/bun/migrate":               "bun migration registry import",
}

func inspectFileDialectIndicators(abs, rel string, src []byte) ([]DialectFinding, error) {
	fset, f, err := ParseGoSource(abs, src)
	if err != nil {
		return nil, err
	}
	return inspectParsedDialectIndicators(fset, f, rel), nil
}

func inspectParsedDialectIndicators(fset *token.FileSet, f *ast.File, rel string) []DialectFinding {
	var findings []DialectFinding

	// Check dialect imports
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		importPath := strings.Trim(imp.Path.Value, `"`)
		if desc, ok := dialectSensitiveImports[importPath]; ok {
			line := fset.Position(imp.Pos()).Line
			findings = append(findings, DialectFinding{
				File:   rel,
				Line:   line,
				Kind:   "import",
				Detail: fmt.Sprintf("%s (%s)", importPath, desc),
			})
		}
	}

	// Walk AST for dialect constants, selectors, error handlers, and raw SQL indicators
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		line := fset.Position(n.Pos()).Line

		switch node := n.(type) {
		case *ast.SelectorExpr:
			// e.g. dialect.SQLite, dialect.PG, db.DialectSQLite, db.DialectPostgres
			if ident, ok := node.X.(*ast.Ident); ok {
				if (ident.Name == "dialect" && (node.Sel.Name == "SQLite" || node.Sel.Name == "PG")) ||
					(ident.Name == "db" && (node.Sel.Name == "DialectSQLite" || node.Sel.Name == "DialectPostgres")) {
					findings = append(findings, DialectFinding{
						File:   rel,
						Line:   line,
						Kind:   "dialect_selector",
						Detail: fmt.Sprintf("%s.%s", ident.Name, node.Sel.Name),
					})
				}
			}

		case *ast.CallExpr:
			// Error helper calls like isSQLiteBusy, classifySQLiteUniqueConstraint, mapUniqueErrSQLite
			if ident, ok := node.Fun.(*ast.Ident); ok {
				if ident.Name == "isSQLiteBusy" || ident.Name == "classifySQLiteUniqueConstraint" || ident.Name == "mapUniqueErrSQLite" {
					findings = append(findings, DialectFinding{
						File:   rel,
						Line:   line,
						Kind:   "sqlite_error_handler",
						Detail: ident.Name + " call",
					})
				}
			}

		case *ast.BasicLit:
			if node.Kind == token.STRING {
				val := strings.ToLower(node.Value)
				for _, sqlMarker := range []struct {
					substr string
					kind   string
					label  string
				}{
					{"pragma_table_info", "sqlite_schema_metadata", "pragma_table_info query"},
					{"sqlite_master", "sqlite_schema_metadata", "sqlite_master query"},
					{"information_schema.columns", "postgres_schema_metadata", "information_schema.columns query"},
					{"information_schema.tables", "postgres_schema_metadata", "information_schema.tables query"},
					{"pg_indexes", "postgres_schema_metadata", "pg_indexes query"},
					{"pg_constraint", "postgres_schema_metadata", "pg_constraint query"},
					{"pg_get_constraintdef", "postgres_schema_metadata", "pg_get_constraintdef query"},
					{"for update", "dialect_locking_construct", "FOR UPDATE locking query"},
					{"begin immediate", "dialect_locking_construct", "BEGIN IMMEDIATE transaction"},
					{"_txlock=immediate", "dialect_locking_construct", "_txlock=immediate DSN parameter"},
				} {
					if strings.Contains(val, sqlMarker.substr) {
						findings = append(findings, DialectFinding{
							File:   rel,
							Line:   line,
							Kind:   sqlMarker.kind,
							Detail: sqlMarker.label,
						})
					}
				}
			}
		}
		return true
	})

	return findings
}

// DiscoverDeclaredInterfaceAssertions returns all asserted interface names declared as `var _ <Interface> = ...` in f.
func DiscoverDeclaredInterfaceAssertions(f *ast.File) []string {
	var assertions []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			isBlankIdent := false
			for _, name := range vs.Names {
				if name != nil && name.Name == "_" {
					isBlankIdent = true
					break
				}
			}
			if !isBlankIdent {
				continue
			}
			if vs.Type != nil {
				if name := exprToString(vs.Type); name != "" {
					assertions = append(assertions, name)
				}
			}
			for _, val := range vs.Values {
				if call, ok := val.(*ast.CallExpr); ok {
					if name := exprToString(call.Fun); name != "" {
						assertions = append(assertions, name)
					}
				}
			}
		}
	}
	return assertions
}

func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		xStr := exprToString(e.X)
		if xStr != "" {
			return xStr + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.ParenExpr:
		return exprToString(e.X)
	default:
		return ""
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PackageParityWrappers records discovered parity test wrappers in a package directory.
type PackageParityWrappers struct {
	PackageDir              string
	HasSQLite               bool
	SQLiteFile              string
	SQLiteDeclCount         int
	HasPostgresDirect       bool
	PostgresDirectFile      string
	PostgresDirectDeclCount int
	PostgresHasBuildTag     bool
}

// HasIntegrationBuildTag checks if the source code contains //go:build integration or // +build integration.
func HasIntegrationBuildTag(src []byte) bool {
	for line := range strings.SplitSeq(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if strings.HasPrefix(trimmed, "//go:build ") && strings.Contains(trimmed, "integration") {
			return true
		}
		if strings.HasPrefix(trimmed, "// +build ") && strings.Contains(trimmed, "integration") {
			return true
		}
	}
	return false
}

// DiscoverPackageParityWrappers inspects all _test.go files in pkgDir (relative to repoRoot).
func DiscoverPackageParityWrappers(repoRoot, pkgDir string) (PackageParityWrappers, error) {
	absDir := filepath.Join(repoRoot, filepath.FromSlash(pkgDir))
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return PackageParityWrappers{}, fmt.Errorf("read package dir %s: %w", pkgDir, err)
	}

	result := PackageParityWrappers{
		PackageDir: pkgDir,
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filePath := filepath.Join(absDir, entry.Name())
		relFile := filepath.ToSlash(filepath.Join(pkgDir, entry.Name()))
		src, err := os.ReadFile(filePath)
		if err != nil {
			return PackageParityWrappers{}, fmt.Errorf("read test file %s: %w", relFile, err)
		}

		_, f, err := ParseGoSource(filePath, src)
		if err != nil {
			return PackageParityWrappers{}, fmt.Errorf("parse test file %s: %w", relFile, err)
		}

		isIntegrationTagged := HasIntegrationBuildTag(src)

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			if fn.Name.Name == "TestDBParity_SQLite" {
				result.HasSQLite = true
				result.SQLiteFile = relFile
				result.SQLiteDeclCount++
			}
			if fn.Name.Name == "TestDBParity_PostgresDirect" {
				result.HasPostgresDirect = true
				result.PostgresDirectFile = relFile
				result.PostgresDirectDeclCount++
				if isIntegrationTagged {
					result.PostgresHasBuildTag = true
				}
			}
		}
	}

	return result, nil
}

// ValidateCapabilityEvidence validates that a capability's evidence anchor:
// - is non-empty / not blank
// - resolves to an existing file (not a directory) within repoRoot
// - the file is non-empty (> 0 bytes)
// - if a symbol anchor is specified (:SymbolName), the symbol exists in the file AST
// - for non-common capabilities, Rationale is non-empty
func ValidateCapabilityEvidence(repoRoot string, cap dbparity.Capability) error {
	trimmedEvidence := strings.TrimSpace(cap.Evidence)
	if trimmedEvidence == "" {
		return fmt.Errorf("capability %q has empty evidence anchor", cap.ID)
	}
	if trimmedEvidence == "." || trimmedEvidence == "/" {
		return fmt.Errorf("capability %q evidence anchor %q is not a file", cap.ID, trimmedEvidence)
	}

	if cap.Class != dbparity.Common {
		if strings.TrimSpace(cap.Rationale) == "" {
			return fmt.Errorf("non-common capability %q (class %s) must have a non-empty rationale", cap.ID, cap.Class)
		}
	}

	rawPath, symbol, hasSymbol := strings.Cut(trimmedEvidence, ":")
	rawPath = strings.TrimSpace(rawPath)
	symbol = strings.TrimSpace(symbol)

	if rawPath == "" {
		return fmt.Errorf("capability %q evidence file path is empty", cap.ID)
	}
	if hasSymbol && symbol == "" {
		return fmt.Errorf("capability %q evidence specifies empty symbol anchor %q", cap.ID, trimmedEvidence)
	}

	absPath := filepath.Join(repoRoot, filepath.FromSlash(rawPath))
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("capability %q evidence file %q does not exist: %w", cap.ID, rawPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("capability %q evidence path %q is a directory, must be a file", cap.ID, rawPath)
	}
	if info.Size() == 0 {
		return fmt.Errorf("capability %q evidence file %q is empty (0 bytes)", cap.ID, rawPath)
	}

	if symbol != "" && strings.HasSuffix(rawPath, ".go") {
		src, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("read evidence file %q: %w", rawPath, err)
		}
		_, f, err := ParseGoSource(absPath, src)
		if err != nil {
			return fmt.Errorf("parse evidence file %q: %w", rawPath, err)
		}
		if !fileDeclaresSymbol(f, symbol) {
			return fmt.Errorf("capability %q evidence symbol %q not found in %q", cap.ID, symbol, rawPath)
		}
	}

	return nil
}

func fileDeclaresSymbol(f *ast.File, symbol string) bool {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.Name == symbol {
				return true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil && s.Name.Name == symbol {
						return true
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name != nil && name.Name == symbol {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

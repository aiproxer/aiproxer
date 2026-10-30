package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

// loadArchOverlay checks the selected package's current source while importing
// dependency types from the Go command's exports. go/packages otherwise treats
// every dependency's export data as invalid whenever any overlay is present,
// causing these single-package mutation checks to type-check the entire graph.
// The Go command still selects files and builds exports with the same overlay
// and build context, including changes to dependencies.
func loadArchOverlay(cfg *packages.Config, pattern string) ([]*packages.Package, error) {
	if cfg.Tests {
		return nil, fmt.Errorf("architecture overlay loader requires Tests=false")
	}
	metadata := *cfg
	metadata.Mode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedImports | packages.NeedDeps | packages.NeedExportFile |
		packages.NeedTypesSizes | packages.NeedModule
	pkgs, err := packages.Load(&metadata, pattern)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 {
		return nil, fmt.Errorf("load overlay metadata: packages=%d (fail closed)", len(pkgs))
	}
	pkg := pkgs[0]
	ids := make(map[string]string)
	for _, dep := range pkg.Imports {
		if id, ok := ids[dep.PkgPath]; ok && id != dep.ID {
			return nil, fmt.Errorf("conflicting package IDs for %s: %s and %s", dep.PkgPath, id, dep.ID)
		}
		ids[dep.PkgPath] = dep.ID
	}
	if len(pkg.CompiledGoFiles) == 0 || pkg.TypesSizes == nil {
		return nil, fmt.Errorf("load overlay metadata: missing source or type sizes for %s", pkg.PkgPath)
	}
	pkg.Fset = token.NewFileSet()
	for _, filename := range pkg.CompiledGoFiles {
		source, ok := cfg.Overlay[filename]
		if !ok {
			source, err = os.ReadFile(filename)
			if err != nil {
				return nil, err
			}
		}
		file, err := parser.ParseFile(pkg.Fset, filename, source, parser.ParseComments|parser.AllErrors)
		if err != nil {
			return nil, err
		}
		pkg.Syntax = append(pkg.Syntax, file)
	}
	imports := &archExportImporter{
		fset:     pkg.Fset,
		packages: pkg.Imports,
		types:    make(map[string]*types.Package),
	}
	pkg.TypesInfo = &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Instances:    make(map[*ast.Ident]types.Instance),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:       make(map[ast.Node]*types.Scope),
		FileVersions: make(map[*ast.File]string),
	}
	checker := &types.Config{Importer: imports, Sizes: pkg.TypesSizes}
	if pkg.Module != nil && pkg.Module.GoVersion != "" {
		checker.GoVersion = "go" + pkg.Module.GoVersion
	}
	pkg.Types, err = checker.Check(pkg.PkgPath, pkg.Fset, pkg.Syntax, pkg.TypesInfo)
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}

type archExportImporter struct {
	fset     *token.FileSet
	packages map[string]*packages.Package
	types    map[string]*types.Package
}

func (imp *archExportImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	metadata := imp.packages[path]
	if metadata == nil || metadata.ExportFile == "" {
		return nil, fmt.Errorf("missing export data for %s", path)
	}
	// Imports are keyed by the source import string; PkgPath includes the Go
	// command's vendor/import-map resolution and identifies exported types.
	path = metadata.PkgPath
	if pkg := imp.types[path]; pkg != nil && pkg.Complete() {
		return pkg, nil
	}
	file, err := os.Open(metadata.ExportFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	//nolint:staticcheck // Go 1.26 export paths are archives; the pinned API requires NewReader to extract their data.
	reader, err := gcexportdata.NewReader(file)
	if err != nil {
		return nil, err
	}
	return gcexportdata.Read(reader, imp.fset, imp.types, path)
}

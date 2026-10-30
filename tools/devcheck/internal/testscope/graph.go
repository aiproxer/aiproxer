package testscope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
)

type listedPackage struct {
	ImportPath                         string
	Dir                                string
	Imports, TestImports, XTestImports []string
	EmbedFiles                         []string
	TestEmbedFiles, XTestEmbedFiles    []string
	Module                             *struct{ Dir string }
	Error                              *struct{ Err string }
	DepsErrors                         []struct{ Err string }
}

func listPackages(ctx context.Context, root, module string) ([]listedPackage, error) {
	dir := filepath.Join(root, filepath.FromSlash(module))
	out, err := commandOutput(ctx, dir, "go", "list", "-mod=readonly", "-json", "./...")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var graph []listedPackage
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode package graph: %w", err)
		}
		if pkg.Error != nil {
			return nil, fmt.Errorf("package %s: %s", pkg.ImportPath, pkg.Error.Err)
		}
		if len(pkg.DepsErrors) != 0 {
			return nil, fmt.Errorf("package %s dependency: %s", pkg.ImportPath, pkg.DepsErrors[0].Err)
		}
		if pkg.Module != nil && filepath.Clean(pkg.Module.Dir) == filepath.Clean(dir) {
			graph = append(graph, pkg)
		}
	}
	return graph, nil
}

// Only production edges propagate production impact. Test edges select a test
// binary, not consumers of that binary's unchanged production implementation.
func affectedPackages(graph []listedPackage, production, tests map[string]bool) []string {
	affected := make(map[string]bool, len(production))
	for name := range production {
		affected[name] = true
	}
	for changed := true; changed; {
		changed = false
		for _, pkg := range graph {
			if affected[pkg.ImportPath] {
				continue
			}
			for _, dependency := range pkg.Imports {
				if affected[dependency] {
					affected[pkg.ImportPath] = true
					changed = true
					break
				}
			}
		}
	}
	var selected []string
	for _, pkg := range graph {
		include := affected[pkg.ImportPath] || tests[pkg.ImportPath]
		for _, dependencies := range [][]string{pkg.TestImports, pkg.XTestImports} {
			for _, dependency := range dependencies {
				include = include || affected[dependency]
			}
		}
		if include {
			selected = append(selected, pkg.ImportPath)
		}
	}
	slices.Sort(selected)
	return selected
}

func inputOwner(root, name string, graph []listedPackage) (listedPackage, bool, bool) {
	file := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	for _, pkg := range graph {
		for _, embedded := range pkg.EmbedFiles {
			if filepath.Clean(filepath.Join(pkg.Dir, embedded)) == file {
				return pkg, true, true
			}
		}
		for _, files := range [][]string{pkg.TestEmbedFiles, pkg.XTestEmbedFiles} {
			for _, embedded := range files {
				if filepath.Clean(filepath.Join(pkg.Dir, embedded)) == file {
					return pkg, false, true
				}
			}
		}
	}
	for _, pkg := range graph {
		dir := filepath.Clean(pkg.Dir)
		if filepath.Dir(file) == dir && isCompilerInput(name) {
			return pkg, !isTestFile(name), true
		}
		fixtureDir := filepath.Join(dir, "testdata")
		rel, err := filepath.Rel(fixtureDir, file)
		if err == nil && staysWithin(rel) {
			return pkg, false, true
		}
	}
	return listedPackage{}, false, false
}

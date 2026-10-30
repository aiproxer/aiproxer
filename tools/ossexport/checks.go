package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// checkTree runs the export checks over an extracted tree: no exported Go file
// may import the Enterprise tree, and no exported text file may link to a path
// the export drops.
func checkTree(root, modulePath string, enterpriseModules []string, excluded, included []string) ([]violation, error) {
	var violations []violation
	importViolations, err := checkGoImports(root, modulePath, enterpriseModules)
	if err != nil {
		return nil, err
	}
	violations = append(violations, importViolations...)
	linkViolations, err := checkLinks(root, excluded, included)
	if err != nil {
		return nil, err
	}
	violations = append(violations, linkViolations...)
	sortViolations(violations)
	return violations, nil
}

// checkGoImports reports exported Go files that import the Enterprise tree.
// Test files and the Go files of nested modules are checked too.
func checkGoImports(root, modulePath string, enterpriseModules []string) ([]violation, error) {
	var violations []violation
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		source, err := os.ReadFile(current)
		if err != nil {
			return fmt.Errorf("read %s: %w", relative, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), relative, source, parser.ImportsOnly)
		if err != nil {
			violations = append(violations, violation{
				File:   relative,
				Reason: fmt.Sprintf("does not parse as Go: %v", err),
			})
			return nil
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if forbiddenImport(imported, modulePath, enterpriseModules) {
				violations = append(violations, violation{
					File:   relative,
					Reason: fmt.Sprintf("imports Enterprise package %q", imported),
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan the exported Go files: %w", err)
	}
	return violations, nil
}

// checkLinks reports exported text files that link to a path the export drops.
func checkLinks(root string, excluded, included []string) ([]violation, error) {
	var violations []violation
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		format := linkFormatForFile(relative)
		if format == linkFormatNone {
			return nil
		}
		source, err := os.ReadFile(current)
		if err != nil {
			return fmt.Errorf("read %s: %w", relative, err)
		}
		targets, err := linkTargets(string(source), format)
		if err != nil {
			return fmt.Errorf("%s: %w", relative, err)
		}
		for _, target := range targets {
			resolved, ok := resolveLinkTarget(relative, target)
			if !ok {
				continue
			}
			if excludedTarget(excluded, included, resolved) {
				violations = append(violations, violation{
					File:   relative,
					Reason: fmt.Sprintf("links to excluded path %q", resolved),
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan the exported text files: %w", err)
	}
	return violations, nil
}

// forbiddenImport reports whether an import path reaches the Enterprise tree.
// The module path of the exported revision decides the boundary, so a renamed
// module is checked as faithfully as the current one.
func forbiddenImport(importPath, modulePath string, enterpriseModules []string) bool {
	for _, prefix := range enterpriseModules {
		if prefix == "" {
			continue
		}
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	if modulePath == "" {
		return false
	}
	if importPath != modulePath && !strings.HasPrefix(importPath, modulePath+"/") {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(importPath, modulePath), "/")
	return slices.Contains(strings.Split(rest, "/"), enterpriseSegment)
}

// excludedTarget reports whether a repository path addresses content the
// export drops: an excluded file, or a directory whose tracked content is
// entirely excluded.
func excludedTarget(excluded, included []string, target string) bool {
	if target == "" {
		return false
	}
	if index := sort.SearchStrings(excluded, target); index < len(excluded) && excluded[index] == target {
		return true
	}
	prefix := target + "/"
	index := sort.SearchStrings(excluded, prefix)
	if index >= len(excluded) || !strings.HasPrefix(excluded[index], prefix) {
		return false
	}
	kept := sort.SearchStrings(included, prefix)
	return kept >= len(included) || !strings.HasPrefix(included[kept], prefix)
}

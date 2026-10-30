// Package testscope selects default Go tests for local development feedback.
// It does not certify tagged suites, external topologies, or other platforms.
package testscope

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Options controls the comparison reference and explicit full-suite override.
type Options struct {
	Base string
	Full bool
}

// Module describes one isolated module's default test execution.
type Module struct {
	Directory string
	Packages  []string
	Reasons   []string
}

// Plan records the resolved change set and the tests selected from it.
type Plan struct {
	Base     string
	Changed  []string
	Modules  []Module
	Fallback string
}

// Build computes a deterministic plan without changing the checkout or index.
func Build(ctx context.Context, root string, opts Options) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, err
	}
	modules, err := discoverModules(root)
	if err != nil {
		return Plan{}, fmt.Errorf("discover test modules: %w", err)
	}
	plan := Plan{}
	if opts.Full {
		return fullPlan(plan, modules, "explicit full default-test run"), nil
	}
	base := opts.Base
	if base == "" {
		base = "origin/main"
	}
	paths, comparison, err := changedPaths(ctx, root, base)
	if err != nil {
		if ctx.Err() != nil {
			return Plan{}, ctx.Err()
		}
		if opts.Base != "" {
			return Plan{}, err
		}
		return fullPlan(plan, modules, err.Error()), nil
	}
	plan.Base, plan.Changed = comparison, paths
	selected := selectChanges(ctx, root, plan, modules, listPackages)
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	return selected, nil
}

func selectChanges(ctx context.Context, root string, plan Plan, modules []string, list func(context.Context, string, string) ([]listedPackage, error)) Plan {
	byModule := make(map[string][]string)
	for _, name := range plan.Changed {
		if isDocumentation(name) {
			continue
		}
		if requiresFullTests(name) {
			return fullPlan(plan, modules, fmt.Sprintf("shared contract or test policy changed: %q", name))
		}
		module := owningModule(name, modules)
		byModule[module] = append(byModule[module], name)
	}
	for _, module := range modules {
		names := byModule[module]
		if len(names) == 0 {
			continue
		}
		graph, err := list(ctx, root, module)
		if err != nil {
			return fullPlan(plan, modules, fmt.Sprintf("package discovery failed for %q: %v", module, err))
		}
		selected, err := selectModule(root, module, names, graph)
		if err != nil {
			return fullPlan(plan, modules, err.Error())
		}
		plan.Modules = append(plan.Modules, selected)
	}
	return plan
}

func selectModule(root, module string, names []string, graph []listedPackage) (Module, error) {
	production, tests := map[string]bool{}, map[string]bool{}
	var reasons []string
	for _, name := range names {
		pkg, prod, found := inputOwner(root, name, graph)
		if !found {
			return Module{}, fmt.Errorf("unresolved input or deleted package: %q", name)
		}
		kind := "test input"
		if prod {
			production[pkg.ImportPath] = true
			kind = "production input"
		} else {
			tests[pkg.ImportPath] = true
		}
		reasons = append(reasons, fmt.Sprintf("%s %q selects %s", kind, name, pkg.ImportPath))
	}
	names = affectedPackages(graph, production, tests)
	selected := Module{Directory: module, Reasons: reasons}
	for _, pkg := range graph {
		if !slices.Contains(names, pkg.ImportPath) {
			continue
		}
		rel, err := filepath.Rel(filepath.Join(root, filepath.FromSlash(module)), pkg.Dir)
		if err != nil || !staysWithin(rel) {
			return Module{}, fmt.Errorf("package directory escapes module: %q", pkg.Dir)
		}
		pattern := "."
		if rel != "." {
			pattern = "./" + filepath.ToSlash(rel)
		}
		selected.Packages = append(selected.Packages, pattern)
		if !production[pkg.ImportPath] && !tests[pkg.ImportPath] {
			selected.Reasons = append(selected.Reasons, "production/test consumer: "+pkg.ImportPath)
		}
	}
	slices.Sort(selected.Packages)
	slices.Sort(selected.Reasons)
	return selected, nil
}

func fullPlan(plan Plan, modules []string, reason string) Plan {
	plan.Fallback = strings.TrimSpace(reason)
	plan.Modules = nil
	for _, module := range modules {
		plan.Modules = append(plan.Modules, Module{Directory: module, Packages: []string{"./..."}})
	}
	return plan
}

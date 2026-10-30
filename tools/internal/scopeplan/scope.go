// Package scopeplan owns change collection and direct module/package selection.
// Consumer expansion and phase-specific policy remain in their existing planners.
package scopeplan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

type Module struct {
	Directory string   `json:"directory"`
	Packages  []string `json:"packages"`
}

type Plan struct {
	Paths      []string `json:"paths"`
	ExistingGo []string `json:"existing_go"`
	Modules    []Module `json:"modules"`
	Tidy       []string `json:"tidy"`
}

// Changes returns a NUL-safe, sorted union for the requested Git boundary.
func Changes(ctx context.Context, root, mode, base string) ([]string, string, error) {
	command := func(args ...string) ([]byte, error) {
		return taskrunner.Output(ctx, taskrunner.Request{Argv: append([]string{"git"}, args...), Dir: root, Env: gitscope.Environ(), ClearEnv: true, Timeout: 2 * time.Minute})
	}
	comparison := ""
	commands := [][]string{}
	switch mode {
	case "base", "branch":
		if base == "" || strings.HasPrefix(base, "-") {
			return nil, "", errors.New("branch comparison requires a base reference")
		}
		out, err := command("merge-base", "--", base, "HEAD")
		if err != nil {
			return nil, "", err
		}
		comparison = strings.TrimSpace(string(out))
		commands = append(commands, []string{"diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD", comparison, "HEAD", "--"})
	case "staged", "changed":
	default:
		return nil, "", fmt.Errorf("invalid scope mode %q", mode)
	}
	if mode == "staged" || mode == "changed" || mode == "branch" {
		commands = append(commands, []string{"diff", "--cached", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD", "--"})
	}
	if mode == "changed" || mode == "branch" {
		commands = append(commands, []string{"diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD", "--"}, []string{"ls-files", "--others", "--exclude-standard", "-z"})
	}
	var paths []string
	for _, args := range commands {
		out, err := command(args...)
		if err != nil {
			return nil, comparison, err
		}
		for name := range strings.SplitSeq(string(out), "\x00") {
			if name != "" {
				paths = append(paths, filepath.ToSlash(name))
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), comparison, nil
}

// Direct preserves direct-hook semantics, including deleted modules absorbed by
// a surviving parent. Metadata expansion is explicit rather than hidden policy.
func Direct(root string, paths []string, metadata bool) (Plan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Paths: slices.Clone(paths), Modules: []Module{}}
	groups := map[string][]string{}
	tidy := map[string]bool{}
	deleted := map[string]bool{}
	owner := func(name string) string {
		for dir := path.Dir(name); ; dir = path.Dir(dir) {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "go.mod")); err == nil {
				return dir
			}
			if dir == "." {
				return ""
			}
		}
	}
	for _, name := range paths {
		if name != "go.mod" && !strings.HasSuffix(name, "/go.mod") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		module := path.Dir(name)
		found := false
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(module)), func(file string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(file, ".go") {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return Plan{}, err
		}
		deleted[module] = !found
		if found && metadata {
			parent := owner(name)
			if parent == "" {
				return Plan{}, fmt.Errorf("deleted module %s: Go source has no surviving parent module", module)
			}
			groups[parent] = []string{"./..."}
			tidy[parent] = true
		}
	}
	for _, name := range paths {
		if IsSkill(name) {
			continue
		}
		skip := false
		for module, empty := range deleted {
			if empty && (name == module+"/go.mod" || strings.HasPrefix(name, module+"/")) {
				skip = true
			}
		}
		if skip {
			continue
		}
		base := path.Base(name)
		if metadata && (base == "go.mod" || base == "go.sum") {
			module := path.Dir(name)
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(module), "go.mod")); err == nil {
				groups[module] = []string{"./..."}
				tidy[module] = true
			}
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err == nil && !info.IsDir() {
			plan.ExistingGo = append(plan.ExistingGo, name)
		}
		module := owner(name)
		if module == "" {
			continue
		}
		tidy[module] = true
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(path.Dir(name)), "*.go"))
		if err != nil {
			return Plan{}, err
		}
		if len(files) == 0 {
			continue
		}
		if slices.Contains(groups[module], "./...") {
			continue
		}
		rel, err := filepath.Rel(filepath.Join(root, filepath.FromSlash(module)), filepath.Join(root, filepath.FromSlash(path.Dir(name))))
		if err != nil {
			return Plan{}, err
		}
		// The go tool never matches a directory under testdata, so a staged
		// fixture there (often a //go:build ignore generator) is no package.
		if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "testdata") {
			continue
		}
		pkg := "."
		if rel != "." {
			pkg = "./" + filepath.ToSlash(rel)
		}
		groups[module] = append(groups[module], pkg)
	}
	var modules []string
	for module := range groups {
		modules = append(modules, module)
	}
	slices.Sort(modules)
	for _, module := range modules {
		packages := groups[module]
		slices.Sort(packages)
		plan.Modules = append(plan.Modules, Module{Directory: module, Packages: slices.Compact(packages)})
	}
	for module := range tidy {
		plan.Tidy = append(plan.Tidy, module)
	}
	slices.Sort(plan.Tidy)
	slices.Sort(plan.ExistingGo)
	return plan, nil
}

func IsSkill(name string) bool {
	for _, prefix := range []string{".agents/skills/", ".codex/skills/", ".cursor/skills/", ".kiro/skills/", ".opencode/skills/", ".pi/skills/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func Directory(root, module string) (string, error) {
	if filepath.IsAbs(module) {
		return "", errors.New("module must be relative to repository")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, module))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("module escapes repository")
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return "", err
	}
	return dir, nil
}

func Explicit(root, module string, packages []string) (Plan, error) {
	if len(packages) == 0 {
		return Plan{}, errors.New("explicit scope requires packages")
	}
	if _, err := Directory(root, module); err != nil {
		return Plan{}, err
	}
	if err := ValidatePackages(packages); err != nil {
		return Plan{}, err
	}
	return Plan{Modules: []Module{{Directory: filepath.ToSlash(filepath.Clean(module)), Packages: slices.Clone(packages)}}}, nil
}

func ValidatePackages(packages []string) error {
	if len(packages) == 0 {
		return errors.New("explicit package scope required")
	}
	for _, pkg := range packages {
		if (pkg != "." && !strings.HasPrefix(pkg, "./")) || slices.Contains(strings.Split(strings.ReplaceAll(pkg, "\\", "/"), "/"), "..") {
			return fmt.Errorf("invalid explicit package %q", pkg)
		}
	}
	return nil
}

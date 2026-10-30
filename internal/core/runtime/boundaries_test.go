package runtime_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// listedPackage carries the `go list -json` fields the boundary rules need:
// identity, standard-ness, and direct imports for in-memory closure.
// Requesting Imports avoids serializing every package’s entire transitive Deps
// closure; the traversal below still follows every production dependency.
type listedPackage struct {
	ImportPath string
	Standard   bool
	Imports    []string
	CgoFiles   []string
}

// depRule is a substring match against ImportPath with a dedicated failure line.
type depRule struct {
	Substr string
	ErrMsg string
}

// depScanRoots is a superset of every root pattern used by the tests below.
// Both tests share a single `go list -deps` subprocess over this union and
// compute their own closures in memory instead of paying one package-graph
// load each.
var depScanRoots = []string{"./cmd/...", "./internal/...", "./pkg/..."}

var repoDepGraph = sync.OnceValues(func() (map[string]listedPackage, error) {
	root, err := repoRootDir()
	if err != nil {
		return nil, err
	}
	args := append([]string{"list", "-deps", "-test=false", "-json=ImportPath,Standard,Imports,CgoFiles"}, depScanRoots...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	graph := make(map[string]listedPackage, 1024)
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for decoder.More() {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			return nil, err
		}
		// Go adds runtime/cgo implicitly for cgo packages.
		if len(pkg.CgoFiles) > 0 && pkg.ImportPath != "runtime/cgo" {
			pkg.Imports = append(pkg.Imports, "runtime/cgo")
		}
		graph[pkg.ImportPath] = pkg
	}
	return graph, nil
})

var modulePath = sync.OnceValues(func() (string, error) {
	root, err := repoRootDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", errors.New("go.mod: module directive not found")
})

// Core production packages must not import internal/plugins/...; integration tests that need
// concrete plugins may import them from *_test.go only (go list -deps -test=false excludes test
// files, so those imports are not in the production dependency graph). See also
// internal/archtest/ref_support_boundaries_test.go for composition-root reference-emulator rules.
func TestCorePackagesDoNotDependOnConcretePluginPackages(t *testing.T) {
	t.Parallel()

	assertDepsExcludeRules(t, []string{"./internal/core/..."}, []depRule{
		{"/internal/plugins/", "core package dependency leaked to concrete plugin package"},
		{"/internal/refclient", "core package dependency leaked to reference client emulator package"},
		{"/internal/refbackend", "core package dependency leaked to reference backend emulator package"},
	})
}

// TestProductionPackagesDoNotDependOnReferenceBackendEmulators ensures the
// reference backend emulator tree is not in the non-test dependency closure
// of production entrypoints (cmd, plugins, core, pkg, support packages).
// See task 10.0.7 (go-core-reimplementation-v1).
func TestProductionPackagesDoNotDependOnReferenceBackendEmulators(t *testing.T) {
	t.Parallel()

	roots := []string{
		"./cmd/...",
		"./internal/plugins/...",
		"./internal/core/...",
		"./internal/infra/...",
		"./internal/qa/...",
		"./pkg/...",
	}
	assertDepsExcludeRules(t, roots, []depRule{
		{"/internal/refbackend", "production package dependency leaked to reference backend emulator package"},
	})
}

// assertDepsExcludeRules resolves the transitive, test-free dependency closure
// of the given patterns from the shared repo scan and fails if any non-standard
// package ImportPath matches a rule substring. Test dependencies are excluded
// so imports from *_test.go files do not affect the graph.
func assertDepsExcludeRules(t *testing.T, patterns []string, rules []depRule) {
	t.Helper()

	graph, err := repoDepGraph()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	mod, err := modulePath()
	if err != nil {
		t.Fatalf("resolve module path: %v", err)
	}

	var roots []string
	for ip := range graph {
		for _, pattern := range patterns {
			if matchTreePattern(mod, pattern, ip) {
				roots = append(roots, ip)
				break
			}
		}
	}

	seen := make(map[string]struct{}, len(roots))
	stack := roots
	for len(stack) > 0 {
		ip := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		pkg, ok := graph[ip]
		if !ok {
			continue
		}
		if !pkg.Standard {
			for _, rule := range rules {
				if strings.Contains(pkg.ImportPath, rule.Substr) {
					t.Fatalf("%s: %s", rule.ErrMsg, pkg.ImportPath)
				}
			}
		}
		stack = append(stack, pkg.Imports...)
	}
}

// matchTreePattern reports whether ip is selected by a "./dir/..." pattern
// resolved against the module path.
func matchTreePattern(mod, pattern, ip string) bool {
	dir := mod + "/" + strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/...")
	return ip == dir || strings.HasPrefix(ip, dir+"/")
}

var repoRootDir = sync.OnceValues(func() (string, error) {
	output, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == osDevNull() {
		return "", errors.New("go env GOMOD did not return a module path")
	}
	return filepath.Dir(gomod), nil
})

func osDevNull() string {
	return "NUL"
}

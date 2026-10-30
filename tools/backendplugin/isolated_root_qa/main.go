package main

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

const isolatedCommandTimeout = 20 * time.Minute

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := run(root); err != nil {
		fmt.Fprintf(os.Stderr, "isolated_root_qa: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK isolated-root-qa")
}

func run(repoRoot string) error {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	if err := assertRootGoModClean(absRoot); err != nil {
		return err
	}
	excludes := discoverExclusions(absRoot)
	tmp, err := os.MkdirTemp("", "aip-isolated-root-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	fmt.Printf("== copy root -> %s (exclusions: %s) ==\n", tmp, strings.Join(excludes, ","))
	if err := copyRoot(absRoot, tmp, excludes); err != nil {
		return err
	}
	if err := assertCopyLacksExcludedDirs(tmp, excludes); err != nil {
		return err
	}
	if err := assertRootGoModClean(tmp); err != nil {
		return fmt.Errorf("copied root: %w", err)
	}
	if err := assertNoConnectorImports(tmp); err != nil {
		return err
	}
	env := []string{"GOWORK=off"}
	// Architecture/unit gates that do not require connectors/ or connector-support/
	// trees (those trees are intentionally absent from the isolated copy).
	safeArchRun := "TestRootGoMod_NoConnectorModules|TestEssentialBackendBundle_ExactAllowlist|TestEssentialAllowlistFixture_DetectsViolation|TestNoFixedOptional|TestEssentialOnly|TestDynamic_NoOptionalKindNamesInGenericPluginreg|TestPhase85_|TestGenericBackendFactoryDeps|TestDynamic_BackendFactoryDepsIsGenericAlias|TestCriticalFileLineBudgets|TestRootHygiene"
	steps := []struct {
		name string
		args []string
	}{
		{"gofmt-check", nil},
		{"go vet ./...", []string{"vet", "./..."}},
		{"go test standardplugins/pluginreg", []string{"test", "-count=1", "-timeout=15m", "./internal/standardplugins", "./internal/pluginreg"}},
		{"go test isolation-safe archtest", []string{"test", "-count=1", "-timeout=15m", "./internal/archtest", "-run", safeArchRun}},
		{"go build aipstd", []string{"build", "-o", discardOut(), "./cmd/aipstd"}},
	}
	for _, st := range steps {
		fmt.Printf("== %s (GOWORK=off) ==\n", st.name)
		if st.name == "gofmt-check" {
			if err := checkGofmt(tmp); err != nil {
				return err
			}
			continue
		}
		result := runner.Run(context.Background(), runner.Request{
			Argv:      append([]string{"go"}, st.args...),
			Dir:       tmp,
			Env:       env,
			Timeout:   isolatedCommandTimeout,
			Output:    taskrunner.Stream,
			StreamOut: os.Stdout,
			StreamErr: os.Stderr,
			Label:     "isolated_root_qa:" + st.name,
		})
		if result.Kind != taskrunner.Success {
			return fmt.Errorf("%s: %w", st.name, runner.Error(result))
		}
	}
	return nil
}

func discardOut() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}

func discoverExclusions(root string) []string {
	base := []string{
		".git",
		"node_modules",
		".aip-package-staging",
		".aip-plugins",
		"bin",
		".cache",
		".gocache",
		"vendor",
	}
	for _, name := range []string{"connectors", "connector-support"} {
		if st, err := os.Stat(filepath.Join(root, name)); err == nil && st.IsDir() {
			base = append(base, name)
		}
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		baseName := filepath.Base(path)
		if baseName == "node_modules" || strings.HasPrefix(baseName, ".aip-") {
			base = appendUnique(base, rel)
			return fs.SkipDir
		}
		if strings.Count(rel, string(os.PathSeparator)) > 2 {
			return fs.SkipDir
		}
		return nil
	})
	return base
}

func appendUnique(in []string, v string) []string {
	v = filepath.ToSlash(v)
	for _, x := range in {
		if filepath.ToSlash(x) == v {
			return in
		}
	}
	return append(in, v)
}

func copyRoot(src, dst string, excludes []string) error {
	excl := map[string]struct{}{}
	for _, e := range excludes {
		excl[filepath.Clean(e)] = struct{}{}
		excl[filepath.ToSlash(filepath.Clean(e))] = struct{}{}
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		clean := filepath.Clean(rel)
		slash := filepath.ToSlash(clean)
		parts := strings.Split(slash, "/")
		for i := range parts {
			prefix := filepath.FromSlash(strings.Join(parts[:i+1], "/"))
			if _, ok := excl[prefix]; ok {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if _, ok := excl[filepath.ToSlash(prefix)]; ok {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".exe") {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

func assertCopyLacksExcludedDirs(copyRoot string, excludes []string) error {
	for _, e := range excludes {
		base := filepath.Base(e)
		if base != "connectors" && base != "connector-support" && base != "node_modules" {
			continue
		}
		p := filepath.Join(copyRoot, e)
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("excluded path still present in copy: %s", e)
		}
	}
	return nil
}

func assertRootGoModClean(root string) error {
	for _, name := range []string{"go.mod", "go.sum"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if name == "go.sum" && os.IsNotExist(err) {
				continue
			}
			return err
		}
		if bytes.Contains(raw, []byte("connectors/")) || bytes.Contains(raw, []byte("connector-support/")) {
			return fmt.Errorf("%s contains connector module path", name)
		}
	}
	return nil
}

func assertNoConnectorImports(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "/connectors/") || strings.Contains(p, "/connector-support/") ||
				strings.HasPrefix(p, "connectors/") || strings.HasPrefix(p, "connector-support/") {
				rel, _ := filepath.Rel(root, path)
				return fmt.Errorf("%s imports unavailable connector module %s", rel, p)
			}
		}
		return nil
	})
}

func checkGofmt(root string) error {
	result := runner.Run(context.Background(), runner.Request{
		Argv:    []string{"gofmt", "-l", "."},
		Dir:     root,
		Timeout: isolatedCommandTimeout,
		Output:  taskrunner.Capture,
		Label:   "isolated_root_qa:gofmt-check",
	})
	if result.Kind != taskrunner.Success {
		return fmt.Errorf("gofmt: %w", runner.Error(result))
	}
	lines := strings.TrimSpace(string(result.Stdout))
	if lines != "" {
		return fmt.Errorf("gofmt drift:\n%s", lines)
	}
	return nil
}

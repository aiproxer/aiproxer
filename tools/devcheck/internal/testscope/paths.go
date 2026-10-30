package testscope

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func staysWithin(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isTestFile(name string) bool { return strings.HasSuffix(name, "_test.go") }

func isCompilerInput(name string) bool {
	switch path.Ext(name) {
	case ".go", ".c", ".h", ".cc", ".cpp", ".cxx", ".m", ".mm", ".f", ".F", ".s", ".S", ".syso":
		return true
	default:
		return false
	}
}

func isDocumentation(name string) bool {
	base := path.Base(name)
	if name == "AGENTS.md" || name == "README.md" || name == "LICENSE" || (strings.HasPrefix(name, "README.") && strings.HasSuffix(name, ".md")) || (strings.HasPrefix(name, "CHANGELOG") && strings.HasSuffix(name, ".md")) {
		return true
	}
	if strings.HasPrefix(name, "docs/") || strings.HasPrefix(name, ".kiro/steering/") || strings.HasPrefix(name, ".kiro/specs/") || strings.HasPrefix(name, ".agents/skills/") {
		switch path.Ext(base) {
		case ".md", ".markdown", ".rst", ".adoc":
			return true
		}
	}
	return false
}

func requiresFullTests(name string) bool {
	base := path.Base(name)
	if base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" || base == "Makefile" {
		return true
	}
	for _, prefix := range []string{"scripts/", ".github/", "tools/devcheck/", "testdata/enterprise_module/", "testdata/external_connector/", "testdata/external_feature_sdk/", "testdata/external_billing_binding/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if !isTestFile(name) {
		for _, prefix := range []string{"pkg/", "connector-support/", "internal/testkit/", "internal/core/config/"} {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	return false
}

// Match the maintained inventory used by lint-all-modules/check-all-modules;
// do not recursively discover archived specifications or vendored fixtures.
func discoverModules(root string) ([]string, error) {
	modules := []string{"."}
	for _, name := range []string{"testdata/enterprise_module", "testdata/external_connector", "testdata/external_feature_sdk", "testdata/external_billing_binding"} {
		if err := addModule(root, name, &modules); err != nil {
			return nil, err
		}
	}
	for _, base := range []string{"connectors", "connector-support"} {
		entries, err := os.ReadDir(filepath.Join(root, base))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if err := addModule(root, path.Join(base, entry.Name()), &modules); err != nil {
					return nil, err
				}
			}
		}
	}
	slices.Sort(modules)
	return modules, nil
}

func addModule(root, name string, modules *[]string) error {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name), "go.mod"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		*modules = append(*modules, name)
	}
	return err
}

func owningModule(name string, modules []string) string {
	owner := "."
	for _, module := range modules {
		if module != "." && strings.HasPrefix(name, module+"/") && len(module) > len(owner) {
			owner = module
		}
	}
	return owner
}

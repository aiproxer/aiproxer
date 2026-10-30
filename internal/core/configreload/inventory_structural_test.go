package configreload_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

// TestFieldCoverage_NoYAMLSectionSerialization guards requirement 7.2: production
// classifiers must use maintained typed comparators, not ad hoc YAML marshaling.
func TestFieldCoverage_NoYAMLSectionSerialization(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "configreload")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// When tests run as ./internal/core/configreload, package dir is cwd.
		entries, err = os.ReadDir(".")
		if err != nil {
			t.Fatalf("read configreload dir: %v", err)
		}
		dir = "."
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(body)
		if strings.Contains(src, "yaml.Marshal") {
			t.Fatalf("%s: production classifier must not yaml.Marshal config sections (req 7.2)", name)
		}
		if strings.Contains(src, "func yamlEqual") || strings.Contains(src, "func diffYAML") {
			t.Fatalf("%s: remove YAML section equality helpers; use typed comparators", name)
		}
	}
}

// TestUnclassifiedTopLevelField_StructuralGuardFails uses reflection only in
// tests to ensure every YAML-tagged top-level Config field is inventoried.
func TestUnclassifiedTopLevelField_StructuralGuardFails(t *testing.T) {
	t.Parallel()
	yamlFields := topLevelYAMLFields(t, reflect.TypeFor[config.Config]())
	inv := map[string]bool{}
	for _, e := range configreload.Inventory() {
		if strings.HasPrefix(e.Path, "override.") {
			continue
		}
		// Top-level inventory paths are bare section names.
		top := e.Path
		if i := strings.IndexByte(e.Path, '.'); i >= 0 {
			top = e.Path[:i]
		}
		inv[top] = true
	}
	var missing []string
	for _, f := range yamlFields {
		if !inv[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unclassified top-level config fields (add inventory entries): %v", missing)
	}
}

func TestUnclassifiedStartupOverride_StructuralGuardFails(t *testing.T) {
	t.Parallel()
	inv := map[string]bool{}
	for _, e := range configreload.Inventory() {
		inv[e.Path] = true
	}
	var missing []string
	for _, path := range configreload.RequiredStartupOverridePaths() {
		if !inv[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unclassified startup overrides: %v", missing)
	}
}

func TestUnclassifiedFieldDetection_HelperRejectsGap(t *testing.T) {
	t.Parallel()
	// Proves the guard would fail when a newly added field lacks classification.
	declared := []string{"server", "access", "brand_new_section"}
	inventoried := []string{"server", "access"}
	missing := configreload.MissingClassifications(declared, inventoried)
	if len(missing) != 1 || missing[0] != "brand_new_section" {
		t.Fatalf("MissingClassifications = %v, want [brand_new_section]", missing)
	}
}

func topLevelYAMLFields(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var out []string
	for f := range typ.Fields() {
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

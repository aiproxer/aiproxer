package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 2.3: field-for-field mirror mapping must be deleted from production.
var forbiddenReloadMapperSymbols = []string{
	"mapTriggerIn",
	"mapTriggerOut",
	"mapCategoryOut",
	"mapResultOut",
	"mapHistoryOut",
	"mapStatusOut",
}

func TestReloadMapper_ReloadMapFileAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "pkg", "aipruntime", "reload_map.go")
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("pkg/aipruntime/reload_map.go must be deleted (Task 2.3); still present at %s", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat reload_map.go: %v", err)
	}
}

func TestReloadMapper_MappingSymbolsAbsentFromProduction(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var findings []string
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id == nil {
				return true
			}
			for _, name := range forbiddenReloadMapperSymbols {
				if id.Name == name {
					findings = append(findings, rel+": forbidden mapper symbol "+name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(findings) > 0 {
		t.Fatalf("field-for-field reload mapping symbols must be absent from production:\n%s",
			strings.Join(findings, "\n"))
	}
}

package openairesponsesitem_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openairesponsesitem"
)

func TestAPI_noExportedEnvelopeKeysOrItemError(t *testing.T) {
	t.Parallel()
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var exportedKeys, exportedItemError bool
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, n := range vs.Names {
						if n.Name == "EnvelopeKeys" && n.IsExported() {
							exportedKeys = true
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.Name == "ItemError" && d.Name.IsExported() && d.Recv == nil {
					exportedItemError = true
				}
			}
		}
	}
	if exportedKeys {
		t.Error("RED: EnvelopeKeys must not be an exported mutable package var")
	}
	if exportedItemError {
		t.Error("RED: ItemError(string) must not be an exported arbitrary-reason API")
	}
}

func TestMarshalEnvelope_deterministicAllowlistOrder(t *testing.T) {
	t.Parallel()
	fields := map[string]json.RawMessage{
		"status":            json.RawMessage(`"completed"`),
		"unknown":           json.RawMessage(`1`),
		"encrypted_content": json.RawMessage(`"enc"`),
		"id":                json.RawMessage(`"rs_1"`),
		"summary":           json.RawMessage(`[]`),
		"type":              json.RawMessage(`"reasoning"`),
		"content":           json.RawMessage(`[]`),
	}
	got, err := openairesponsesitem.MarshalEnvelope(fields)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"rs_1","type":"reasoning","summary":[],"content":[],"encrypted_content":"enc","status":"completed"}`
	if string(got) != want {
		t.Fatalf("MarshalEnvelope order/allowlist mismatch:\n got %s\nwant %s", got, want)
	}
}

func packageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Test may run from package dir or module root depending on go test invocation.
	candidates := []string{
		wd,
		filepath.Join(wd, "internal", "plugins", "protocols", "openairesponsesitem"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "reasoning_envelope.go")); err == nil {
			return c
		}
	}
	t.Fatalf("reasoning_envelope.go not found from wd=%s", wd)
	return ""
}

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // reads repository-owned source fixtures and checks global authority.
func TestContinuationMirrorRegressionRejectsConcreteCoreAuthority(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "continuation", "authority.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.TypeSpec)
		if !ok || (decl.Name.Name != "MemoryStore" && decl.Name.Name != "StreamRecorder") {
			return true
		}
		if _, concrete := decl.Type.(*ast.StructType); concrete {
			t.Fatalf("core continuation reintroduced mutable %s authority", decl.Name.Name)
		}
		return true
	})
}

//nolint:paralleltest // reads repository-owned source fixtures and checks global authority.
func TestContinuationMirrorRegressionKeepsSDKAuthorityAndCoreAlias(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		filepath.Join(root, "pkg", "aipsdk", "continuation", "memory_store.go"),
		filepath.Join(root, "pkg", "aipsdk", "continuation", "stream_recorder.go"),
	} {
		if _, err := parser.ParseFile(token.NewFileSet(), path, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	core := filepath.Join(root, "internal", "core", "continuation", "authority.go")
	if _, err := parser.ParseFile(token.NewFileSet(), core, nil, 0); err != nil {
		t.Fatal(err)
	}
}

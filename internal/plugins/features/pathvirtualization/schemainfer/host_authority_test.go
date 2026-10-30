package schemainfer_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// forbiddenHostImports lists the packages schema inference must never depend on.
// Inference reads declared JSON schema structure and compares declared names; it
// never touches a host filesystem, a host separator, or host process state, so
// none of these can change what it infers.
var forbiddenHostImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// TestInferenceHasNoHostAuthority parses this package's own non-test sources and
// proves the inference step holds no host OS, host separator, or filesystem
// authority. The lexical core keeps the same guarantee for its own sources; this
// guard extends it to the schema-facing step, which is where a host path helper
// would be most tempting and most dangerous.
func TestInferenceHasNoHostAuthority(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		checked++
		for _, spec := range file.Imports {
			imported, importErr := strconv.Unquote(spec.Path.Value)
			if importErr != nil {
				t.Fatalf("import path in %s is not a string literal: %v", name, importErr)
			}
			for _, forbidden := range forbiddenHostImports {
				if imported == forbidden {
					t.Errorf("%s imports %q; schema inference must stay host-independent", name, imported)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the host-authority guard proved nothing")
	}
}

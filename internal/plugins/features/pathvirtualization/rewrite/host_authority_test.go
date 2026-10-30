package rewrite_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// forbiddenHostImports lists the packages this rewriter must never depend on.
//
// The rewriter reads untrusted canonical payloads and asks the lexical core to
// decide what a value means as a filesystem locator. A host path helper here would
// make a foreign client path unparseable, and os or runtime would let host
// filesystem and process state decide what a backend sees, so none of them can
// appear here.
var forbiddenHostImports = []string{
	"os",
	"path",
	"path/filepath",
	"runtime",
	"syscall",
}

// modulePath is the repository module prefix.
const modulePath = "github.com/aiproxer/aiproxer"

// TestRewritingHasNoHostAuthority parses this package's own non-test sources and
// proves the canonical rewriter holds no host OS, host separator, or filesystem
// authority. The lexical core keeps the same guarantee for its own sources; this
// guard extends it to the step that walks canonical requests, which is where a host
// path helper would be most tempting and would silently make path flavor detection
// host-dependent.
func TestRewritingHasNoHostAuthority(t *testing.T) {
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
					t.Errorf("%s imports %q; the canonical rewriter must stay host-independent", name, imported)
				}
			}
			if strings.HasPrefix(imported, modulePath+"/internal/core") {
				t.Errorf("%s imports %q; a feature plugin must not depend on internal/core", name, imported)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package sources found; the host-authority guard proved nothing")
	}
}

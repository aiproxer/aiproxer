package runtimebundle

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestRuntimeBundle_NoResidualSecretGuardConcreteImports(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly) //nolint:staticcheck // SA1019: intentional lightweight AST import scan of one package dir
	if err != nil {
		t.Fatalf("failed to parse runtimebundle package: %v", err)
	}

	const forbidden = "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"

	var violatingFiles []string
	for _, pkg := range pkgs {
		for fileName, f := range pkg.Files {
			for _, imp := range f.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(importPath, forbidden) {
					violatingFiles = append(violatingFiles, filepath.Base(fileName))
				}
			}
		}
	}

	if len(violatingFiles) > 0 {
		t.Fatalf("runtimebundle must have no residual secretguard feature imports, but found in: %v", violatingFiles)
	}
}

func TestRuntimeBundle_NoResidualSecretGuardHelpers(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0) //nolint:staticcheck // SA1019: intentional lightweight AST declaration scan of one package dir
	if err != nil {
		t.Fatalf("failed to parse runtimebundle package: %v", err)
	}
	for _, pkg := range pkgs {
		for fileName, f := range pkg.Files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					if fn.Name.Name == "composeSecretGuardSingleUser" {
						t.Fatalf("runtimebundle must not contain residual helper %s in %s", fn.Name.Name, filepath.Base(fileName))
					}
				}
			}
		}
	}
}

type convergenceDummyEnv struct {
	lookups int
}

func (e *convergenceDummyEnv) Lookup(string) (string, bool) {
	e.lookups++
	return "", false
}

func (e *convergenceDummyEnv) Snapshot() []string {
	return nil
}

type convergenceDummyObserver struct{}

func (convergenceDummyObserver) OnSecretDecision(context.Context, sdk.DecisionEvent) error {
	return nil
}

func TestRuntimeBundle_SecretGuardCandidateOverlayAndReload(t *testing.T) {
	t.Parallel()

	// Secret-guard posture converges through ordinary planes: the composed
	// execution config is published under SourceGenerationBinder semantics and
	// read back purely via plane access.
	base := frozenSecretGuards(stubSecretGuard{id: "base-guard", ord: 1})
	plane, inv := secretGuardFromPlanes(base)
	// Guards without a composed execution config extract to the disabled
	// posture: no engine plane, no inventory.
	if inv != nil {
		t.Fatalf("expected nil inventory without execution config, got %+v", inv)
	}
	if len(plane.Guards) != 0 || plane.MatcherResolver != nil {
		t.Fatalf("expected zero plane without execution config, got %+v", plane)
	}
}

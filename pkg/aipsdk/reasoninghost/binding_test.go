package reasoninghost_test

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestBinding_HostBindingID(t *testing.T) {
	t.Parallel()

	b := &reasoninghost.Binding{}
	if got := b.HostBindingID(); got != reasoninghost.BindingID {
		t.Fatalf("b.HostBindingID() = %q, want %q", got, reasoninghost.BindingID)
	}

	var typedNil *reasoninghost.Binding
	if got := typedNil.HostBindingID(); got != reasoninghost.BindingID {
		t.Fatalf("typedNil.HostBindingID() = %q, want %q", got, reasoninghost.BindingID)
	}
}

func TestBinding_ValidateHostBinding_TypedNilFails(t *testing.T) {
	t.Parallel()

	var typedNil *reasoninghost.Binding
	err := typedNil.ValidateHostBinding()
	if err == nil {
		t.Fatal("typedNil.ValidateHostBinding() expected error, got nil")
	}
	if !errors.Is(err, reasoninghost.ErrNilBinding) {
		t.Fatalf("expected ErrNilBinding, got: %v", err)
	}
}

func TestBinding_ValidateHostBinding_Valid(t *testing.T) {
	t.Parallel()

	b := &reasoninghost.Binding{
		EgressPolicies: map[string]reasoninghost.EgressPolicy{
			"test-policy": stubPolicy{
				decision: reasoninghost.EgressDecision{
					Action:        reasoninghost.EgressAllow,
					PolicyVersion: "v1",
				},
			},
		},
		MatcherResolver: sdk.MatcherResolver(nil),
	}
	if err := b.ValidateHostBinding(); err != nil {
		t.Fatalf("valid binding failed validation: %v", err)
	}

	reg := b.Registration()
	if reg.Binding != b {
		t.Fatalf("reg.Binding = %v, want %v", reg.Binding, b)
	}

	if err := featurehost.Validate([]featurehost.Registration{reg}); err != nil {
		t.Fatalf("featurehost.Validate on reasoninghost registration failed: %v", err)
	}
}

func TestBinding_ImplementsFeatureHostBinding(t *testing.T) {
	t.Parallel()

	var _ featurehost.Binding = (*reasoninghost.Binding)(nil)
}

func TestBinding_NoInternalImports(t *testing.T) {
	t.Parallel()

	// Read source files directly to ensure no internal packages are imported.
	// This ensures the SDK contract is completely self-contained.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { //nolint:staticcheck // SA1019: intentional lightweight AST import scan of one package dir
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseDir failed: %v", err)
	}

	for pkgName, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(path, "/internal/") {
					t.Errorf("file %s in pkg %s imports internal package %s", fileName, pkgName, path)
				}
			}
		}
	}
}

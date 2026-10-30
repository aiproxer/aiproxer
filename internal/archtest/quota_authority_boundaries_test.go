package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestQuotaAuthorityAdapterHasNoFinancialInfrastructureImport keeps the
// provider gauge adapter on the nonfinancial authority/evidence plane. The
// adapter may publish typed policy refs and observation refs, but it cannot
// reach billing persistence or customer-unit/payable composition.
func TestQuotaAuthorityAdapterHasNoFinancialInfrastructureImport(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "authoritycoord", "quota_provider.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		pathValue := imp.Path.Value
		if pathValue == `"github.com/aiproxer/aiproxer/enterprise/internal/infra/billingstore"` ||
			pathValue == `"github.com/aiproxer/aiproxer/enterprise/internal/core/billing"` {
			t.Fatalf("quota authority adapter imports financial infrastructure/domain package %s", pathValue)
		}
	}
	var financialSelector bool
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Money" || sel.Sel.Name == "Quantity" || sel.Sel.Name == "Payable" || sel.Sel.Name == "CustomerUnit" {
			financialSelector = true
		}
		return true
	})
	if financialSelector {
		t.Fatal("quota authority adapter must not construct financial money, quantity, payable, or customer-unit values")
	}
}

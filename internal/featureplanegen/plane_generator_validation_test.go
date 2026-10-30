package featureplanegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateRequestMaterializerExpr tests the AST validation for RequestMaterializer expressions.
func TestValidateRequestMaterializerExpr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		exprSrc   string
		wantError bool
	}{
		{name: "FuncLit is accepted", exprSrc: `func(v []string) []string { return v }`},
		{name: "Ident is accepted", exprSrc: `materializeFunc`},
		{name: "SelectorExpr is accepted", exprSrc: `request.MaterializeAttemptsSorted`},
		{name: "ParenExpr wrapping SelectorExpr is accepted", exprSrc: `(request.MaterializeAttemptsSorted)`},
		{name: "ParenExpr wrapping FuncLit is accepted", exprSrc: `((func(v []string) []string { return v }))`},
		{name: "BasicLit string is rejected", exprSrc: `"invalid_string"`, wantError: true},
		{name: "CallExpr is rejected", exprSrc: `createMaterializer()`, wantError: true},
		{name: "BinaryExpr is rejected", exprSrc: `1 + 2`, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsedExpr, err := parser.ParseExpr(tt.exprSrc)
			require.NoError(t, err, "ParseExpr failed on %s", tt.exprSrc)

			err = validateRequestMaterializerExpr(parsedExpr, "PlaneTest")
			if tt.wantError {
				assert.Error(t, err, "expected error for expr %s", tt.exprSrc)
			} else {
				assert.NoError(t, err, "expected no error for expr %s", tt.exprSrc)
			}
		})
	}
}

// TestValidatePrivilegeCondition tests the AST grammar validation for privilege if conditions.
func TestValidatePrivilegeCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condSrc   string
		wantError bool
		errSubstr string
	}{
		{name: "bare true boolean literal is accepted", condSrc: `true`},
		{name: "bare false boolean literal is accepted", condSrc: `false`},
		{name: "unary not boolean is accepted", condSrc: `!false`},
		{name: "standard len comparison is accepted", condSrc: `len(v) > 0`},
		{name: "parenthesized len comparison is accepted", condSrc: `(len(v) >= 1)`},
		{name: "parenthesized len argument is accepted", condSrc: `len((v)) == 0`},
		{name: "binary and with comparisons is accepted", condSrc: `len(v) > 0 && len(v) <= 10`},
		{name: "binary or with comparisons is accepted", condSrc: `len(v) == 0 || len(v) != 5`},
		{name: "complex boolean expression with parens and bool literals", condSrc: `(len(v) > 0) && (true || !false)`},
		{name: "arbitrary identifier in boolean position is rejected", condSrc: `isValid`, wantError: true, errSubstr: `unsupported condition identifier "isValid"`},
		{name: "direct len call without comparison is rejected", condSrc: `len(v)`, wantError: true, errSubstr: `len call cannot be used as boolean condition directly`},
		{name: "helper function call is rejected", condSrc: `helper(v)`, wantError: true, errSubstr: `call to helper function "helper" is not allowed`},
		{name: "selector call is rejected", condSrc: `foreign.Mutate(v)`, wantError: true, errSubstr: `selector/method call "foreign.Mutate" is not allowed`},
		{name: "method call is rejected", condSrc: `v.Check()`, wantError: true, errSubstr: `selector/method call "v.Check" is not allowed`},
		{name: "len with call argument is rejected", condSrc: `len(helper(v)) > 0`, wantError: true, errSubstr: `len argument in privilege condition must be a bare parameter identifier`},
		{name: "len with multiple arguments is rejected", condSrc: `len(v, x) > 0`, wantError: true, errSubstr: `len call in privilege condition must have exactly 1 argument`},
		{name: "len with ellipsis is rejected", condSrc: `len(v...) > 0`, wantError: true, errSubstr: `variadic/ellipsis len is unsupported`},
		{name: "len with selector argument is rejected", condSrc: `len(foreign.V) > 0`, wantError: true, errSubstr: `len argument in privilege condition must be a bare parameter identifier`},
		{name: "arithmetic in scalar operand is rejected", condSrc: `len(v) + 1 > 0`, wantError: true, errSubstr: `arithmetic/binary expression`},
		{name: "slice indexing is rejected", condSrc: `v[0] == 0`, wantError: true, errSubstr: `index or slice expression is not allowed`},
		{name: "non-integer literal in comparison is rejected", condSrc: `len(v) > "zero"`, wantError: true, errSubstr: `unsupported literal "zero"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsedExpr, err := parser.ParseExpr(tt.condSrc)
			require.NoError(t, err, "ParseExpr failed on %s", tt.condSrc)

			err = validatePrivilegeCondition("TestPlane", parsedExpr)
			if tt.wantError {
				if assert.Error(t, err) && tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidatePrivilegeReturnStmt_DirectAST validates direct AST properties of return statements.
func TestValidatePrivilegeReturnStmt_DirectAST(t *testing.T) {
	t.Parallel()

	t.Run("untyped composite literal Flags rejected directly", func(t *testing.T) {
		t.Parallel()
		retStmt := &ast.ReturnStmt{
			Results: []ast.Expr{
				&ast.CompositeLit{
					Type: &ast.Ident{Name: "PrivilegeProjection"},
					Elts: []ast.Expr{
						&ast.KeyValueExpr{
							Key: &ast.Ident{Name: "Flags"},
							Value: &ast.CompositeLit{
								Type: nil,
								Elts: []ast.Expr{
									&ast.BasicLit{Kind: token.STRING, Value: `"raw_capture"`},
								},
							},
						},
					},
				},
			},
		}

		err := validatePrivilegeReturnStmt("TestPlane", retStmt)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "plane TestPlane: Flags must use explicit []string literal")
	})
}

package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

var directPolicyFieldMapping = map[string]string{
	"planeID":                "ID",
	"rules":                  "Rules",
	"nilPolicy":              "NilPolicy",
	"isNil":                  "IsNil",
	"validate":               "Validate",
	"validateIdentity":       "ValidateIdentity",
	"combine":                "Combine",
	"identity":               "Identity",
	"exclusiveConflictError": "ExclusiveConflictError",
	"requestMaterializer":    "RequestMaterializer",
	"requestBorrow":          "RequestBorrow",
	"hookTarget":             "HookTarget",
	"requestAccess":          "RequestAccess",
}

var diagPolicyFieldMapping = map[string]string{
	"diagStageID":       "StageID",
	"diagCoalesceGroup": "CoalesceGroup",
	"diagOrder":         "Order",
	"diagMaterialize":   "Materialize",
	"diagPrivileges":    "Privileges",
}

var expectedPolicyFieldsList = []string{
	"planeID",
	"rules",
	"nilPolicy",
	"isNil",
	"validate",
	"validateIdentity",
	"combine",
	"identity",
	"exclusiveConflictError",
	"requestMaterializer",
	"requestBorrow",
	"hookTarget",
	"requestAccess",
	"diagStageID",
	"diagCoalesceGroup",
	"diagOrder",
	"diagMaterialize",
	"diagPrivileges",
}

func isGeneratedPlanesFile(path string) bool {
	norm := filepath.ToSlash(path)
	return norm == "pkg/aipsdk/feature/plane_generated.go" ||
		strings.HasSuffix(norm, "/pkg/aipsdk/feature/plane_generated.go") ||
		filepath.Base(norm) == "plane_generated.go"
}

func isPlaneVarName(name string) bool {
	if name == "PlaneDeclaration" {
		return false
	}
	return strings.HasPrefix(name, "Plane") && len(name) > 5 && name[5] >= 'A' && name[5] <= 'Z'
}

func isPlaneIdent(expr ast.Expr) bool {
	expr = unwrapParen(expr)
	if id, ok := expr.(*ast.Ident); ok {
		return isPlaneVarName(id.Name)
	}
	return false
}

func parseCanonicalPolicyVar(name string) (string, bool) {
	if strings.HasPrefix(name, "canonicalPlane") && strings.HasSuffix(name, "Policy") {
		p := strings.TrimSuffix(strings.TrimPrefix(name, "canonical"), "Policy")
		if isPlaneVarName(p) {
			return p, true
		}
	}
	return "", false
}

func parseCanonicalAccessVar(name string) (string, bool) {
	if strings.HasPrefix(name, "canonicalPlane") && strings.HasSuffix(name, "Access") {
		p := strings.TrimSuffix(strings.TrimPrefix(name, "canonical"), "Access")
		if isPlaneVarName(p) {
			return p, true
		}
	}
	return "", false
}

func isPlaneGeneratedSelector(expr ast.Expr) (string, bool) {
	expr = unwrapParen(expr)
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "generated" {
		return "", false
	}
	p := rootPlaneVar(sel.X)
	if isPlaneVarName(p) {
		return p, true
	}
	return "", false
}

func rootPlaneVar(expr ast.Expr) string {
	expr = unwrapParen(expr)
	switch x := expr.(type) {
	case *ast.Ident:
		if isPlaneVarName(x.Name) {
			return x.Name
		}
	case *ast.SelectorExpr:
		return rootPlaneVar(x.X)
	}
	return ""
}

func extractCompositeLit(expr ast.Expr) *ast.CompositeLit {
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return e
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return extractCompositeLit(e.X)
		}
	}
	return nil
}

func exprString(expr ast.Expr) string {
	expr = unwrapParen(expr)
	if expr == nil {
		return "<nil>"
	}
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func validateCanonicalPolicyFields(
	fset *token.FileSet,
	normalizedRel string,
	policyIdent *ast.Ident,
	expectedPlaneVar string,
	compLit *ast.CompositeLit,
	allowedSelectors map[ast.Expr]bool,
	reportedExprs map[ast.Expr]bool,
) []RuleFinding {
	var findings []RuleFinding
	seenFields := make(map[string]bool)
	for _, elt := range compLit.Elts {
		eltExpr := unwrapParen(elt)
		kv, ok := eltExpr.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyId, ok := unwrapParen(kv.Key).(*ast.Ident)
		if !ok {
			continue
		}
		destField := keyId.Name
		valExpr := unwrapParen(kv.Value)

		if seenFields[destField] {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(kv.Pos()).Line),
				Detail: fmt.Sprintf("duplicate field %q in canonical policy %s", destField, policyIdent.Name),
			})
		}
		seenFields[destField] = true

		if expectedSrc, isDirect := directPolicyFieldMapping[destField]; isDirect {
			sel, ok := valExpr.(*ast.SelectorExpr)
			if !ok {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(valExpr.Pos()).Line),
					Detail: fmt.Sprintf("mismatched source field in %s: destination field %s must capture %s.%s, got %s", policyIdent.Name, destField, expectedPlaneVar, expectedSrc, exprString(valExpr)),
				})
				reportedExprs[valExpr] = true
				continue
			}
			targetPlane := rootPlaneVar(sel.X)
			if targetPlane == "" || targetPlane != expectedPlaneVar {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
					Detail: fmt.Sprintf("cross-plane capture in %s: destination field %s must capture from %s, but captures from %s", policyIdent.Name, destField, expectedPlaneVar, exprString(sel.X)),
				})
				reportedExprs[sel] = true
				continue
			}
			if sel.Sel.Name != expectedSrc {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
					Detail: fmt.Sprintf("mismatched source field in %s: destination field %s must capture %s, but captures %s", policyIdent.Name, destField, expectedSrc, sel.Sel.Name),
				})
				reportedExprs[sel] = true
				continue
			}
			allowedSelectors[sel] = true
		} else if expectedDiagSub, isDiag := diagPolicyFieldMapping[destField]; isDiag {
			sel, ok := valExpr.(*ast.SelectorExpr)
			if !ok {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(valExpr.Pos()).Line),
					Detail: fmt.Sprintf("mismatched source field in %s: destination field %s must capture %s.Diagnostics.%s, got %s", policyIdent.Name, destField, expectedPlaneVar, expectedDiagSub, exprString(valExpr)),
				})
				reportedExprs[valExpr] = true
				continue
			}
			innerSel, ok := unwrapParen(sel.X).(*ast.SelectorExpr)
			if !ok {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
					Detail: fmt.Sprintf("mismatched source field in %s: destination field %s must capture %s.Diagnostics.%s, got %s", policyIdent.Name, destField, expectedPlaneVar, expectedDiagSub, exprString(valExpr)),
				})
				reportedExprs[valExpr] = true
				continue
			}
			targetPlane := rootPlaneVar(innerSel.X)
			if targetPlane == "" || targetPlane != expectedPlaneVar {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(innerSel.Pos()).Line),
					Detail: fmt.Sprintf("cross-plane capture in %s: destination field %s must capture from %s, but captures from %s", policyIdent.Name, destField, expectedPlaneVar, exprString(innerSel.X)),
				})
				reportedExprs[innerSel] = true
				reportedExprs[sel] = true
				continue
			}
			if innerSel.Sel.Name != "Diagnostics" || sel.Sel.Name != expectedDiagSub {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
					Detail: fmt.Sprintf("mismatched source field in %s: destination field %s must capture Diagnostics.%s, but captures %s.%s", policyIdent.Name, destField, expectedDiagSub, innerSel.Sel.Name, sel.Sel.Name),
				})
				reportedExprs[sel] = true
				reportedExprs[innerSel] = true
				continue
			}
			allowedSelectors[sel] = true
			allowedSelectors[innerSel] = true
		} else {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(kv.Pos()).Line),
				Detail: fmt.Sprintf("unknown destination field %s in canonical policy %s", destField, policyIdent.Name),
			})
			reportedExprs[valExpr] = true
		}
	}

	var missingFields []string
	for _, expField := range expectedPolicyFieldsList {
		if !seenFields[expField] {
			missingFields = append(missingFields, expField)
		}
	}
	if len(missingFields) > 0 {
		findings = append(findings, RuleFinding{
			Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
			Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(compLit.Pos()).Line),
			Detail: fmt.Sprintf("incomplete canonical policy initialization in %s: missing expected field %q (missing %d of %d expected fields: %v)", policyIdent.Name, missingFields[0], len(missingFields), len(expectedPolicyFieldsList), missingFields),
		})
	}
	return findings
}

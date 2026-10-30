package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
)

func checkPlaneGeneratedSelectors(fset *token.FileSet, normalizedRel string, f *ast.File) []RuleFinding {
	var findings []RuleFinding

	allowedSelectors := make(map[ast.Expr]bool)
	reportedExprs := make(map[ast.Expr]bool)
	aliasVars := make(map[string]bool)

	// Gather all generated plane names in this file
	generatedPlanes := make(map[string]bool)

	// 1. Gather from package-level variable declarations
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if p, ok := parseCanonicalPolicyVar(name.Name); ok {
					generatedPlanes[p] = true
				}
				if p, ok := parseCanonicalAccessVar(name.Name); ok {
					generatedPlanes[p] = true
				}
			}
		}
	}

	// 2. Gather from assignments in the file (supporting synthetic test snippets without package-level vars)
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				lhsUnwrapped := unwrapParen(lhs)
				if id, ok := lhsUnwrapped.(*ast.Ident); ok {
					if p, ok := parseCanonicalPolicyVar(id.Name); ok {
						generatedPlanes[p] = true
					}
					if p, ok := parseCanonicalAccessVar(id.Name); ok {
						generatedPlanes[p] = true
					}
				} else if p, ok := isPlaneGeneratedSelector(lhsUnwrapped); ok {
					generatedPlanes[p] = true
				}
			}
		}
		return true
	})

	policyInitCount := make(map[string]int)
	accessInitCount := make(map[string]int)
	planeGeneratedInitCount := make(map[string]int)

	policyAttempted := make(map[string]bool)
	accessAttempted := make(map[string]bool)
	planeGeneratedAttempted := make(map[string]bool)

	designatedAssigns := make(map[*ast.AssignStmt]bool)
	reportedAssigns := make(map[*ast.AssignStmt]bool)

	// Step 1: Find init() and whitelist only canonical policy initialization composite literals,
	// canonical access bindings, and PlaneX.generated binding assignments, strictly validating
	// semantic correspondence and completeness.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || fn.Name.Name != "init" || fn.Body == nil {
			continue
		}

		for _, stmt := range fn.Body.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok {
				continue
			}

			if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || assign.Tok != token.ASSIGN {
				continue
			}

			lhs := unwrapParen(assign.Lhs[0])
			rhs := unwrapParen(assign.Rhs[0])

			// 1. Policy initialization: canonicalPlaneXPolicy = &generatedPolicy[...]{ ... }
			if id, ok := lhs.(*ast.Ident); ok {
				if expectedPlaneVar, ok := parseCanonicalPolicyVar(id.Name); ok {
					compLit := extractCompositeLit(rhs)
					if compLit == nil {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(rhs.Pos()).Line),
							Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
						})
						reportedAssigns[assign] = true
						continue
					}
					policyAttempted[expectedPlaneVar] = true
					if policyInitCount[expectedPlaneVar] > 0 {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(assign.Pos()).Line),
							Detail: fmt.Sprintf("duplicate canonical policy initialization for %s in init(); closed planes require exactly one canonical generatedPolicy literal", expectedPlaneVar),
						})
						reportedAssigns[assign] = true
						continue
					}
					policyInitCount[expectedPlaneVar]++
					designatedAssigns[assign] = true

					findings = append(findings, validateCanonicalPolicyFields(fset, normalizedRel, id, expectedPlaneVar, compLit, allowedSelectors, reportedExprs)...)
					continue
				}

				// 2. Access binding: canonicalPlaneXAccess = generatedAccess[...]{ policy: canonicalPlaneXPolicy, ... }
				if expectedPlaneVar, ok := parseCanonicalAccessVar(id.Name); ok {
					compLit := extractCompositeLit(rhs)
					if compLit == nil {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(rhs.Pos()).Line),
							Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
						})
						reportedAssigns[assign] = true
						continue
					}
					accessAttempted[expectedPlaneVar] = true
					if accessInitCount[expectedPlaneVar] > 0 {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(assign.Pos()).Line),
							Detail: fmt.Sprintf("duplicate canonical access binding for %s in init(); closed planes require exactly one canonical access binding", expectedPlaneVar),
						})
						reportedAssigns[assign] = true
						continue
					}
					accessInitCount[expectedPlaneVar]++
					designatedAssigns[assign] = true

					expectedPolicyVar := "canonical" + expectedPlaneVar + "Policy"
					foundPolicy := false
					for _, elt := range compLit.Elts {
						eltExpr := unwrapParen(elt)
						kv, ok := eltExpr.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						keyId, ok := unwrapParen(kv.Key).(*ast.Ident)
						if !ok || keyId.Name != "policy" {
							continue
						}
						foundPolicy = true
						valExpr := unwrapParen(kv.Value)
						valId, ok := valExpr.(*ast.Ident)
						if !ok || valId.Name != expectedPolicyVar {
							findings = append(findings, RuleFinding{
								Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
								Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(valExpr.Pos()).Line),
								Detail: fmt.Sprintf("mismatched policy in %s: policy must be %s, got %s", id.Name, expectedPolicyVar, exprString(valExpr)),
							})
							reportedExprs[valExpr] = true
						}
					}
					if !foundPolicy {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(compLit.Pos()).Line),
							Detail: fmt.Sprintf("missing policy field in %s; canonical access must bind %s", id.Name, expectedPolicyVar),
						})
					}
					continue
				}
			}

			// 3. PlaneX.generated binding: PlaneX.generated = canonicalPlaneXAccess
			if expectedPlaneVar, ok := isPlaneGeneratedSelector(lhs); ok {
				expectedAccess := "canonical" + expectedPlaneVar + "Access"
				rhsId, ok := rhs.(*ast.Ident)
				if planeGeneratedInitCount[expectedPlaneVar] > 0 {
					if ok && rhsId.Name == expectedAccess {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(assign.Pos()).Line),
							Detail: fmt.Sprintf("duplicate %s.generated binding in init(); closed planes require exactly one PlaneX.generated binding", expectedPlaneVar),
						})
					} else {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(assign.Pos()).Line),
							Detail: fmt.Sprintf("forbidden reassignment of %s.generated; closed planes must not be reassigned outside designated init capture/binding statements", expectedPlaneVar),
						})
					}
					reportedAssigns[assign] = true
					continue
				}
				planeGeneratedAttempted[expectedPlaneVar] = true
				planeGeneratedInitCount[expectedPlaneVar]++
				designatedAssigns[assign] = true

				if ok && rhsId.Name == expectedAccess {
					allowedSelectors[lhs] = true
					if sel, ok := lhs.(*ast.SelectorExpr); ok {
						allowedSelectors[sel] = true
					}
				} else {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(rhs.Pos()).Line),
						Detail: fmt.Sprintf("mismatched binding: %s.generated must be bound to %s, got %s", expectedPlaneVar, expectedAccess, exprString(rhs)),
					})
					reportedExprs[rhs] = true
					reportedExprs[lhs] = true
				}
				continue
			}
		}
	}

	// Step 2: Track aliases and forbidden passing/assignments
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				rhsExpr := unwrapParen(rhs)
				if isPlaneIdent(rhsExpr) {
					if i < len(node.Lhs) {
						lhsExpr := unwrapParen(node.Lhs[i])
						if id, ok := lhsExpr.(*ast.Ident); ok {
							aliasVars[id.Name] = true
						}
					}
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(rhs.Pos()).Line),
						Detail: fmt.Sprintf("forbidden alias assignment of %s; closed planes must not be aliased for runtime authority", exprString(rhsExpr)),
					})
				}
			}
		case *ast.ValueSpec:
			for i, val := range node.Values {
				valExpr := unwrapParen(val)
				if isPlaneIdent(valExpr) {
					if i < len(node.Names) {
						aliasVars[node.Names[i].Name] = true
					}
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(val.Pos()).Line),
						Detail: fmt.Sprintf("forbidden alias declaration of %s; closed planes must not be aliased for runtime authority", exprString(valExpr)),
					})
				}
			}
		case *ast.CallExpr:
			calledIdent := extractCalledIdent(node.Fun)
			for _, arg := range node.Args {
				argExpr := unwrapParen(arg)
				if isPlaneIdent(argExpr) && calledIdent != "Get" {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(arg.Pos()).Line),
						Detail: fmt.Sprintf("forbidden passing of %s to %s; closed planes must not be passed for runtime authority", exprString(argExpr), calledIdent),
					})
				}
			}
		}
		return true
	})

	// Step 3: Reject any canonical policy/access or PlaneX.generated reassignment outside designated init capture/binding statements.
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if designatedAssigns[node] || reportedAssigns[node] {
				return true
			}
			for _, lhs := range node.Lhs {
				lhsExpr := unwrapParen(lhs)
				if id, ok := lhsExpr.(*ast.Ident); ok {
					if _, ok := parseCanonicalPolicyVar(id.Name); ok {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(lhs.Pos()).Line),
							Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
						})
						reportedAssigns[node] = true
					} else if _, ok := parseCanonicalAccessVar(id.Name); ok {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(lhs.Pos()).Line),
							Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
						})
						reportedAssigns[node] = true
					}
				} else if planeName, ok := isPlaneGeneratedSelector(lhsExpr); ok {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(lhs.Pos()).Line),
						Detail: fmt.Sprintf("forbidden reassignment of %s.generated; closed planes must not be reassigned outside designated init capture/binding statements", planeName),
					})
					reportedAssigns[node] = true
					reportedExprs[lhsExpr] = true
				}
			}
		case *ast.IncDecStmt:
			xExpr := unwrapParen(node.X)
			if id, ok := xExpr.(*ast.Ident); ok {
				if _, ok := parseCanonicalPolicyVar(id.Name); ok {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(xExpr.Pos()).Line),
						Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
					})
				} else if _, ok := parseCanonicalAccessVar(id.Name); ok {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(xExpr.Pos()).Line),
						Detail: fmt.Sprintf("forbidden reassignment of %s; closed planes must not be reassigned outside designated init capture/binding statements", id.Name),
					})
				}
			} else if planeName, ok := isPlaneGeneratedSelector(xExpr); ok {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(xExpr.Pos()).Line),
					Detail: fmt.Sprintf("forbidden reassignment of %s.generated; closed planes must not be reassigned outside designated init capture/binding statements", planeName),
				})
				reportedExprs[xExpr] = true
			}
		}
		return true
	})

	// Step 4: Inspect all selector expressions
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if allowedSelectors[sel] || reportedExprs[sel] {
			return true
		}

		// Check if target is a PlaneX
		if planeName := rootPlaneVar(sel); planeName != "" {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
				Detail: fmt.Sprintf("forbidden selector %s; closed planes must use canonical policy captured during init", exprString(sel)),
			})
			return false // do not duplicate on nested selectors
		}

		// Check if target is an aliased var
		if id, ok := unwrapParen(sel.X).(*ast.Ident); ok && aliasVars[id.Name] {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(sel.Pos()).Line),
				Detail: fmt.Sprintf("forbidden selector %s on aliased plane; closed planes must use canonical policy captured during init", exprString(sel)),
			})
			return false
		}

		return true
	})

	// Step 5: Verify completeness across all declared generated planes
	sortedPlanes := make([]string, 0, len(generatedPlanes))
	for p := range generatedPlanes {
		sortedPlanes = append(sortedPlanes, p)
	}
	slices.Sort(sortedPlanes)

	for _, planeName := range sortedPlanes {
		if policyInitCount[planeName] == 0 && !policyAttempted[planeName] {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(f.Pos()).Line),
				Detail: fmt.Sprintf("missing canonical policy initialization for %s; closed planes require exactly one complete canonical generatedPolicy literal", planeName),
			})
		}
		if accessInitCount[planeName] == 0 && !accessAttempted[planeName] {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(f.Pos()).Line),
				Detail: fmt.Sprintf("missing canonical access binding for %s; closed planes require exactly one canonical access binding", planeName),
			})
		}
		if planeGeneratedInitCount[planeName] == 0 && !planeGeneratedAttempted[planeName] {
			findings = append(findings, RuleFinding{
				Rule:   RuleClosedPlaneNoGlobalPlaneSelectors,
				Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(f.Pos()).Line),
				Detail: fmt.Sprintf("missing %s.generated binding; closed planes require exactly one PlaneX.generated binding to matching canonical access", planeName),
			})
		}
	}

	return findings
}

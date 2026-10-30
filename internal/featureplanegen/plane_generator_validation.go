package featureplanegen

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

func deriveImports(f *ast.File) ([]string, error) {
	var sdkImports []string
	seen := make(map[string]bool)

	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if !strings.Contains(path, "/") {
			continue
		}
		var importClause string
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" {
			importClause = fmt.Sprintf("%s %q", imp.Name.Name, path)
		} else {
			importClause = fmt.Sprintf("%q", path)
		}
		if !seen[importClause] {
			seen[importClause] = true
			sdkImports = append(sdkImports, importClause)
		}
	}

	if len(sdkImports) == 0 {
		return nil, fmt.Errorf("no SDK imports found in manifest")
	}

	slices.Sort(sdkImports)
	return sdkImports, nil
}

func unwrapParen(e ast.Expr) ast.Expr {
	for {
		if p, ok := e.(*ast.ParenExpr); ok {
			e = p.X
			continue
		}
		return e
	}
}

func formatSelector(sel *ast.SelectorExpr) string {
	if xIdent, ok := sel.X.(*ast.Ident); ok {
		return xIdent.Name + "." + sel.Sel.Name
	}
	return sel.Sel.Name
}

func validateRequestMaterializerExpr(expr ast.Expr, varName string) error {
	switch unwrapParen(expr).(type) {
	case *ast.FuncLit, *ast.Ident, *ast.SelectorExpr:
		return nil
	default:
		return fmt.Errorf("plane %s: RequestMaterializer expression must be a function literal, identifier, or package selector, got %T", varName, expr)
	}
}

var canonicalPrivilegeStringLiterals = map[string]bool{
	"raw_capture":        true,
	"auxiliary_requests": true,
	"auth_provider":      true,
	"completion_gate":    true,
}

var canonicalPrivilegeIdentifiers = map[string]bool{
	"PrivilegeRawCapture":        true,
	"PrivilegeAuxiliaryRequests": true,
	"PrivilegeAuthProvider":      true,
	"PrivilegeCompletionGate":    true,
}

func validatePrivilegesFunc(varName string, expr ast.Expr) error {
	if ident, ok := expr.(*ast.Ident); ok && ident.Name == "nil" {
		return nil
	}
	funcLit, ok := expr.(*ast.FuncLit)
	if !ok {
		if ident, ok := expr.(*ast.Ident); ok {
			if ident.Name == "nil" {
				return nil
			}
			return fmt.Errorf("plane %s: dynamic Privileges function identifier %q cannot be statically validated", varName, ident.Name)
		}
		return fmt.Errorf("plane %s: dynamic Privileges expression (%T) cannot be statically validated", varName, expr)
	}

	returnCount := 0
	if err := validatePrivilegeStmt(varName, funcLit.Body, &returnCount); err != nil {
		return err
	}
	if returnCount == 0 {
		return fmt.Errorf("plane %s: Privileges function does not return static PrivilegeProjection", varName)
	}
	return nil
}

func validatePrivilegeStmt(varName string, stmt ast.Stmt, returnCount *int) error {
	if stmt == nil {
		return nil
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		for _, child := range s.List {
			if err := validatePrivilegeStmt(varName, child, returnCount); err != nil {
				return err
			}
		}
		return nil

	case *ast.EmptyStmt:
		return nil

	case *ast.IfStmt:
		if s.Init != nil {
			return fmt.Errorf("plane %s: unsupported statement type %T", varName, s.Init)
		}
		if err := validatePrivilegeCondition(varName, s.Cond); err != nil {
			return err
		}
		if err := validatePrivilegeStmt(varName, s.Body, returnCount); err != nil {
			return err
		}
		if s.Else != nil {
			if err := validatePrivilegeStmt(varName, s.Else, returnCount); err != nil {
				return err
			}
		}
		return nil

	case *ast.ReturnStmt:
		*returnCount++
		return validatePrivilegeReturnStmt(varName, s)

	default:
		return fmt.Errorf("plane %s: unsupported statement type %T", varName, stmt)
	}
}

func validatePrivilegeCondition(varName string, expr ast.Expr) error {
	if expr == nil {
		return fmt.Errorf("plane %s: nil condition in if statement", varName)
	}
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if e.Name == "true" || e.Name == "false" {
			return nil
		}
		return fmt.Errorf("plane %s: unsupported condition identifier %q; only boolean literals true/false or comparisons are allowed", varName, e.Name)
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return validatePrivilegeCondition(varName, e.X)
		}
		return fmt.Errorf("plane %s: unsupported unary operator %s in privilege condition", varName, e.Op)
	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND, token.LOR:
			if err := validatePrivilegeCondition(varName, e.X); err != nil {
				return err
			}
			return validatePrivilegeCondition(varName, e.Y)
		case token.EQL, token.NEQ, token.GTR, token.GEQ, token.LSS, token.LEQ:
			if err := validatePrivilegeScalarExpr(varName, e.X); err != nil {
				return err
			}
			return validatePrivilegeScalarExpr(varName, e.Y)
		default:
			return fmt.Errorf("plane %s: unsupported binary operator %s in privilege condition", varName, e.Op)
		}
	case *ast.CallExpr:
		return validatePrivilegeDisallowedCall(varName, e)
	case *ast.FuncLit:
		return fmt.Errorf("plane %s: dynamic function literal in if condition is not allowed", varName)
	case *ast.SelectorExpr:
		return fmt.Errorf("plane %s: selector expression %q is not allowed in privilege condition", varName, formatSelector(e))
	default:
		return fmt.Errorf("plane %s: unsupported condition expression (%T)", varName, expr)
	}
}

func validatePrivilegeDisallowedCall(varName string, call *ast.CallExpr) error {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name == "len" {
			return fmt.Errorf("plane %s: len call cannot be used as boolean condition directly; must be in a comparison", varName)
		}
		return fmt.Errorf("plane %s: call to helper function %q is not allowed in privilege condition", varName, fn.Name)
	case *ast.SelectorExpr:
		return fmt.Errorf("plane %s: selector/method call %q is not allowed in privilege condition", varName, formatSelector(fn))
	case *ast.FuncLit:
		return fmt.Errorf("plane %s: dynamic function literal call in privilege condition is not allowed", varName)
	default:
		return fmt.Errorf("plane %s: unsupported call expression (%T) in privilege condition", varName, call.Fun)
	}
}

func validatePrivilegeScalarExpr(varName string, expr ast.Expr) error {
	if expr == nil {
		return fmt.Errorf("plane %s: nil scalar expression in privilege condition", varName)
	}
	switch e := unwrapParen(expr).(type) {
	case *ast.BasicLit:
		if e.Kind == token.INT {
			return nil
		}
		return fmt.Errorf("plane %s: unsupported literal %s (kind %v) in privilege condition; only integer literals are allowed", varName, e.Value, e.Kind)
	case *ast.Ident:
		return nil
	case *ast.CallExpr:
		fnIdent, ok := e.Fun.(*ast.Ident)
		if !ok || fnIdent.Name != "len" {
			return validatePrivilegeDisallowedCall(varName, e)
		}
		if e.Ellipsis != token.NoPos {
			return fmt.Errorf("plane %s: variadic/ellipsis len is unsupported in privilege condition", varName)
		}
		if len(e.Args) != 1 {
			return fmt.Errorf("plane %s: len call in privilege condition must have exactly 1 argument, got %d", varName, len(e.Args))
		}
		arg := unwrapParen(e.Args[0])
		if _, ok := arg.(*ast.Ident); !ok {
			return fmt.Errorf("plane %s: len argument in privilege condition must be a bare parameter identifier, got %T", varName, arg)
		}
		return nil
	case *ast.SelectorExpr:
		return fmt.Errorf("plane %s: selector expression %q is not allowed in privilege condition", varName, formatSelector(e))
	case *ast.IndexExpr, *ast.SliceExpr:
		return fmt.Errorf("plane %s: index or slice expression is not allowed in privilege condition", varName)
	case *ast.FuncLit:
		return fmt.Errorf("plane %s: dynamic function literal in privilege condition is not allowed", varName)
	case *ast.BinaryExpr:
		return fmt.Errorf("plane %s: arithmetic/binary expression (%T with op %s) is not allowed as a scalar expression in privilege condition", varName, expr, e.Op)
	default:
		return fmt.Errorf("plane %s: unsupported scalar expression (%T) in privilege condition", varName, expr)
	}
}

func validatePrivilegeReturnStmt(varName string, ret *ast.ReturnStmt) error {
	if len(ret.Results) != 1 {
		return fmt.Errorf("plane %s: Privileges return must have exactly 1 result, got %d", varName, len(ret.Results))
	}

	compLit, ok := unwrapParen(ret.Results[0]).(*ast.CompositeLit)
	if !ok {
		return fmt.Errorf("plane %s: unsupported return expression (%T)", varName, ret.Results[0])
	}
	if compLit.Type == nil {
		return fmt.Errorf("plane %s: untyped composite literal return not allowed; must use local PrivilegeProjection", varName)
	}

	switch t := compLit.Type.(type) {
	case *ast.Ident:
		if t.Name != "PrivilegeProjection" {
			return fmt.Errorf("plane %s: invalid return type %q; must use local PrivilegeProjection", varName, t.Name)
		}
	case *ast.SelectorExpr:
		return fmt.Errorf("plane %s: foreign projection type %q not allowed; must use local PrivilegeProjection", varName, formatSelector(t))
	default:
		return fmt.Errorf("plane %s: unsupported return type (%T); must use local PrivilegeProjection", varName, compLit.Type)
	}

	if len(compLit.Elts) == 0 {
		return nil
	}

	var flagsExpr ast.Expr
	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return fmt.Errorf("plane %s: positional composite field (%T) not allowed in PrivilegeProjection; must use keyed Flags field", varName, elt)
		}
		kIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			return fmt.Errorf("plane %s: unsupported field key type (%T) in PrivilegeProjection", varName, kv.Key)
		}
		if kIdent.Name != "Flags" {
			return fmt.Errorf("plane %s: unknown field %q in PrivilegeProjection; only Flags is allowed", varName, kIdent.Name)
		}
		if flagsExpr != nil {
			return fmt.Errorf("plane %s: duplicate Flags field in PrivilegeProjection", varName)
		}
		flagsExpr = kv.Value
	}

	if flagsExpr == nil {
		return nil
	}

	sliceLit, ok := unwrapParen(flagsExpr).(*ast.CompositeLit)
	if !ok {
		return fmt.Errorf("plane %s: dynamic Flags expression (%T)", varName, unwrapParen(flagsExpr))
	}

	if sliceLit.Type == nil {
		return fmt.Errorf("plane %s: Flags must use explicit []string literal", varName)
	}

	arrType, ok := sliceLit.Type.(*ast.ArrayType)
	if !ok || arrType.Len != nil {
		return fmt.Errorf("plane %s: Flags must be a slice of string ([]string), got %T", varName, sliceLit.Type)
	}
	eltIdent, ok := arrType.Elt.(*ast.Ident)
	if !ok || eltIdent.Name != "string" {
		return fmt.Errorf("plane %s: Flags element type must be string, got %T", varName, arrType.Elt)
	}

	var flagErrors []string
	for _, flagElt := range sliceLit.Elts {
		switch f := unwrapParen(flagElt).(type) {
		case *ast.BasicLit:
			if f.Kind != token.STRING {
				flagErrors = append(flagErrors, fmt.Sprintf("non-string literal %v", f.Value))
				continue
			}
			val := strings.Trim(f.Value, `"`)
			if !canonicalPrivilegeStringLiterals[val] {
				flagErrors = append(flagErrors, fmt.Sprintf("unknown privilege flag %q", val))
			}
		case *ast.Ident:
			if !canonicalPrivilegeIdentifiers[f.Name] {
				flagErrors = append(flagErrors, fmt.Sprintf("unknown privilege flag identifier %q", f.Name))
			}
		case *ast.SelectorExpr:
			flagErrors = append(flagErrors, fmt.Sprintf("privilege selector expression %q not allowed; must use bare identifier or string literal", formatSelector(f)))
		default:
			flagErrors = append(flagErrors, fmt.Sprintf("dynamic privilege flag expression (%T)", flagElt))
		}
	}

	if len(flagErrors) > 0 {
		return fmt.Errorf("plane %s: %s", varName, strings.Join(flagErrors, "; "))
	}

	return nil
}

func validateDiagnosticsCrossPlane(planes []planeInfo) error {
	seenOrders := make(map[int]planeInfo)
	coalesceStages := make(map[string]string)
	coalescePlanes := make(map[string]string)

	for _, p := range planes {
		// Rule 1: StageID non-empty => Order > 0 and Materialize present.
		if p.hasDiagStageID {
			if p.diagOrder <= 0 {
				return fmt.Errorf("plane %s: diagnostics StageID is set (%q) but Order must be > 0 (got %d)", p.varName, p.diagStageID, p.diagOrder)
			}
			if !p.hasDiagMaterialize {
				return fmt.Errorf("plane %s: diagnostics StageID is set (%q) but Materialize function is missing", p.varName, p.diagStageID)
			}
		}

		// Rule 2: StageID empty => Order == 0 and no Materialize/Privileges/CoalesceGroup.
		if !p.hasDiagStageID {
			if p.diagOrder != 0 {
				return fmt.Errorf("plane %s: diagnostics StageID must not be empty when Order (%d) is provided", p.varName, p.diagOrder)
			}
			if p.hasDiagMaterialize {
				return fmt.Errorf("plane %s: diagnostics StageID must not be empty when Materialize function is provided", p.varName)
			}
			if p.hasDiagPrivileges {
				return fmt.Errorf("plane %s: diagnostics StageID must not be empty when Privileges function is provided", p.varName)
			}
			if p.diagCoalesceGroup != "" {
				return fmt.Errorf("plane %s: diagnostics StageID must not be empty when CoalesceGroup (%q) is provided", p.varName, p.diagCoalesceGroup)
			}
		}

		// Rule 3: Duplicate positive Order is rejected unless both planes have identical non-empty CoalesceGroup and identical StageID.
		if p.diagOrder > 0 {
			if prev, exists := seenOrders[p.diagOrder]; exists {
				if p.diagCoalesceGroup == "" || prev.diagCoalesceGroup == "" ||
					p.diagCoalesceGroup != prev.diagCoalesceGroup ||
					p.diagStageID != prev.diagStageID {
					return fmt.Errorf("duplicate diagnostic order %d between plane %s (%q, stage %q, group %q) and plane %s (%q, stage %q, group %q)",
						p.diagOrder, p.varName, p.planeID, p.diagStageID, p.diagCoalesceGroup,
						prev.varName, prev.planeID, prev.diagStageID, prev.diagCoalesceGroup)
				}
			} else {
				seenOrders[p.diagOrder] = p
			}
		}

		// Rule 4: Same non-empty CoalesceGroup across any orders must always use the same StageID.
		if p.diagCoalesceGroup != "" {
			if prevStage, exists := coalesceStages[p.diagCoalesceGroup]; exists {
				if prevStage != p.diagStageID {
					prevPlane := coalescePlanes[p.diagCoalesceGroup]
					return fmt.Errorf("mismatching stage IDs for coalesce group %q: plane %s uses %q but plane %s uses %q",
						p.diagCoalesceGroup, prevPlane, prevStage, p.varName, p.diagStageID)
				}
			} else {
				coalesceStages[p.diagCoalesceGroup] = p.diagStageID
				coalescePlanes[p.diagCoalesceGroup] = p.varName
			}
		}
	}
	return nil
}

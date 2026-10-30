package archtest

import (
	"go/ast"
	"go/token"
	"path"
	"path/filepath"
	"strings"
)

// IsThinDelegate reports whether a function/method is a strictly thin delegate
// whose body contains either:
//  1. exactly one return statement calling Get or FrozenIdentity from pkg/aipsdk/feature, or RequestExecutionView, OR
//  2. an optional initial receiver nil guard (if receiver == nil { return nil }) with no else or side effects,
//     followed by exactly one return statement calling Get, FrozenIdentity, or RequestExecutionView.
func IsThinDelegate(relPath string, fd *ast.FuncDecl, files ...*ast.File) bool {
	if fd == nil || fd.Body == nil {
		return false
	}
	var f *ast.File
	if len(files) > 0 {
		f = files[0]
	}

	stmts := fd.Body.List
	switch len(stmts) {
	case 1:
		ret, ok := stmts[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return false
		}
		return isGetCall(relPath, ret.Results[0], f)
	case 2:
		ifStmt, ok := stmts[0].(*ast.IfStmt)
		if !ok || !isValidReceiverNilGuard(fd, ifStmt) {
			return false
		}
		ret, ok := stmts[1].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return false
		}
		return isGetCall(relPath, ret.Results[0], f)
	default:
		return false
	}
}

func isValidReceiverNilGuard(fd *ast.FuncDecl, ifStmt *ast.IfStmt) bool {
	if ifStmt == nil || ifStmt.Init != nil || ifStmt.Else != nil {
		return false
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return false
	}
	recvIdent := fd.Recv.List[0].Names[0]
	if recvIdent == nil || recvIdent.Name == "" || recvIdent.Name == "_" {
		return false
	}
	recvName := recvIdent.Name

	cond := ifStmt.Cond
	for {
		if paren, ok := cond.(*ast.ParenExpr); ok {
			cond = paren.X
		} else {
			break
		}
	}

	binExpr, ok := cond.(*ast.BinaryExpr)
	if !ok || binExpr.Op != token.EQL {
		return false
	}
	isRecvNil := (isNilExpr(binExpr.X) && isIdentNamed(binExpr.Y, recvName)) ||
		(isIdentNamed(binExpr.X, recvName) && isNilExpr(binExpr.Y))
	if !isRecvNil {
		return false
	}

	if ifStmt.Body == nil || len(ifStmt.Body.List) != 1 {
		return false
	}
	retStmt, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(retStmt.Results) != 1 {
		return false
	}
	return isNilExpr(retStmt.Results[0])
}

func isIdentNamed(expr ast.Expr, name string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == name
	case *ast.ParenExpr:
		return isIdentNamed(e.X, name)
	default:
		return false
	}
}

func isNilExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.ParenExpr:
		return isNilExpr(e.X)
	default:
		return false
	}
}

func isGetCall(relPath string, expr ast.Expr, f *ast.File) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if isGetOrFrozenIdentity(relPath, call.Fun, f) {
		return true
	}
	return isRequestExecutionViewCall(relPath, call, f)
}

func isFeaturePkgImport(imp *ast.ImportSpec) bool {
	if imp == nil || imp.Path == nil {
		return false
	}
	path := strings.Trim(imp.Path.Value, `"`)
	return path == "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
}

func isFeaturePkgDotImportOrSamePkg(relPath string, f *ast.File) bool {
	if f == nil {
		return false
	}
	if f.Name != nil && f.Name.Name == "feature" {
		slash := filepath.ToSlash(relPath)
		dir := path.Dir(slash)
		if dir == "pkg/aipsdk/feature" {
			return true
		}
	}
	for _, imp := range f.Imports {
		if isFeaturePkgImport(imp) && imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

func isFeaturePkgIdent(relPath string, expr ast.Expr, f *ast.File) bool {
	pkgIdent, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	pkgName := pkgIdent.Name
	if f == nil {
		return false
	}
	for _, imp := range f.Imports {
		if isFeaturePkgImport(imp) {
			if imp.Name != nil {
				if imp.Name.Name == pkgName {
					return true
				}
			} else if pkgName == "feature" {
				return true
			}
		}
	}
	return false
}

func isGetOrFrozenIdentity(relPath string, fn ast.Expr, f *ast.File) bool {
	switch e := fn.(type) {
	case *ast.IndexExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.IndexListExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.ParenExpr:
		return isGetOrFrozenIdentity(relPath, e.X, f)
	case *ast.Ident:
		if e.Name != "Get" && e.Name != "FrozenIdentity" {
			return false
		}
		return isFeaturePkgDotImportOrSamePkg(relPath, f)
	case *ast.SelectorExpr:
		if e.Sel.Name != "Get" && e.Sel.Name != "FrozenIdentity" {
			return false
		}
		return isFeaturePkgIdent(relPath, e.X, f)
	default:
		return false
	}
}

var allowedRequestExecutionMethods = map[string]bool{
	"ToolCallPolicies":   true,
	"ToolCallFinalizers": true,
	"SecretGuards":       true,
	"LocalTurnHandlers":  true,
}

func isRequestExecutionViewCall(relPath string, call *ast.CallExpr, f *ast.File) bool {
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if !allowedRequestExecutionMethods[sel.Sel.Name] {
		return false
	}
	innerCall, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := innerCall.Fun.(type) {
	case *ast.SelectorExpr:
		if fn.Sel.Name != "RequestExecution" {
			return false
		}
		return isFeaturePkgIdent(relPath, fn.X, f)
	case *ast.Ident:
		if fn.Name != "RequestExecution" {
			return false
		}
		return isFeaturePkgDotImportOrSamePkg(relPath, f)
	default:
		return false
	}
}

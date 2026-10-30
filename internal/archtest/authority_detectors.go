package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func DetectDuplicateAuthoritativeRegistries(repoRoot string) (map[string][]string, error) {
	dirs := []string{
		filepath.Join(repoRoot, "internal", "standardplugins"),
		filepath.Join(repoRoot, "internal", "pluginreg"),
		filepath.Join(repoRoot, "internal", "infra", "runtimebundle"),
		filepath.Join(repoRoot, "internal", "providerprofiles"),
	}
	fset := token.NewFileSet()
	out := make(map[string][]string)
	for _, dir := range dirs {
		//nolint:staticcheck // Source mutation scanner intentionally needs ParseDir's file map.
		pkgs, err := parser.ParseDir(fset, dir, nil, 0)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				rel, _ := filepath.Rel(repoRoot, fname)
				rel = filepath.ToSlash(rel)
				ast.Inspect(fileNode, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.ValueSpec:
						for i, name := range x.Names {
							if ast.IsExported(name.Name) && i < len(x.Values) && isContributionIdentityExpr(name.Name, x.Type, x.Values[i]) {
								out[name.Name] = appendUniquePath(out[name.Name], rel)
							}
						}
					case *ast.FuncDecl:
						if x.Body == nil || !ast.IsExported(x.Name.Name) || !isContributionAuthorityName(x.Name.Name) {
							return true
						}
						for _, result := range functionResultTypes(x) {
							if isIdentityType(result) && functionReturnsIdentityLiteral(x) {
								out[x.Name.Name] = appendUniquePath(out[x.Name.Name], rel)
							}
						}
					}
					return true
				})
			}
		}
	}
	return out, nil
}

func appendUniquePath(paths []string, path string) []string {
	if slices.Contains(paths, path) {
		return paths
	}
	return append(paths, path)
}

func isContributionAuthorityName(name string) bool {
	lower := strings.ToLower(name)
	for _, denied := range []string{"pool", "route_registry", "scenario", "health", "affinity", "generation", "config", "state"} {
		if strings.Contains(lower, denied) {
			return false
		}
	}
	if strings.Contains(lower, "contribution") || strings.Contains(lower, "registration") || strings.Contains(lower, "routeclaim") || strings.Contains(lower, "route_claim") {
		return true
	}
	if strings.Contains(lower, "essentialbackend") || strings.Contains(lower, "compatiblebackend") || strings.Contains(lower, "providerprofile") || strings.Contains(lower, "backendfamily") {
		return true
	}
	return strings.Contains(lower, "frontend") && (strings.Contains(lower, "claim") || strings.Contains(lower, "registration"))
}

func isIdentityType(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.ArrayType:
		return x.Len == nil && isStringExpr(x.Elt)
	case *ast.MapType:
		return isStringExpr(x.Key) && (isStringExpr(x.Value) || isFuncExpr(x.Value))
	}
	return false
}

func isStringExpr(expr ast.Expr) bool { id, ok := expr.(*ast.Ident); return ok && id.Name == "string" }
func isFuncExpr(expr ast.Expr) bool   { _, ok := expr.(*ast.FuncType); return ok }

func isContributionIdentityExpr(name string, typ ast.Expr, value ast.Expr) bool {
	if !isContributionAuthorityName(name) {
		return false
	}
	// A compatibility view assigned from the contribution derivation is not a
	// second authority. Only literal identity collections are parallel lists.
	if _, ok := value.(*ast.CallExpr); ok {
		return false
	}
	if !isIdentityType(typ) {
		// Inferred declarations still qualify when their literal has string identity keys.
		if lit, ok := value.(*ast.CompositeLit); ok {
			_, array := lit.Type.(*ast.ArrayType)
			_, object := lit.Type.(*ast.MapType)
			return array || object
		}
		return false
	}
	if typ != nil && isIdentityType(typ) {
		return true
	}
	switch x := value.(type) {
	case *ast.CompositeLit:
		if _, ok := x.Type.(*ast.ArrayType); ok {
			return true
		}
		if _, ok := x.Type.(*ast.MapType); ok {
			return true
		}
	case *ast.CallExpr:
		return isContributionAuthorityName(identName(x.Fun))
	}
	return false
}

func functionResultTypes(fn *ast.FuncDecl) []ast.Expr {
	if fn.Type.Results == nil {
		return nil
	}
	var out []ast.Expr
	for _, field := range fn.Type.Results.List {
		out = append(out, field.Type)
	}
	return out
}

func functionReturnsIdentityLiteral(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for _, result := range ret.Results {
				if lit, ok := result.(*ast.CompositeLit); ok {
					if isIdentityType(lit.Type) {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

func DetectCentralProtocolRouteKinds(repoRoot string) (map[string]string, error) {
	contractDir := filepath.Join(repoRoot, "internal", "stdhttp", "contract")
	fset := token.NewFileSet()
	//nolint:staticcheck // Source mutation scanner intentionally needs ParseDir's file map.
	pkgs, err := parser.ParseDir(fset, contractDir, nil, 0)
	if err != nil {
		return nil, err
	}

	discoveredRouteKinds := make(map[string]string)
	for _, pkg := range pkgs {
		for fname, fileNode := range pkg.Files {
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			ast.Inspect(fileNode, func(n ast.Node) bool {
				vSpec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range vSpec.Names {
					if i >= len(vSpec.Values) {
						continue
					}
					if val, ok := centralRouteKindValue(name.Name, vSpec.Values[i]); ok {
						// RouteKind is an opaque extension-owned identifier. Central
						// route contracts must not own concrete operation IDs.
						discoveredRouteKinds[name.Name] = val
					}
				}
				return true
			})
		}
	}
	return discoveredRouteKinds, nil
}

func centralRouteKindValue(name string, expr ast.Expr) (string, bool) {
	if !strings.HasPrefix(name, "RouteKind") {
		return "", false
	}
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(value)), true
}

// DetectCentralProtocolDiagnosticsDebt scans central surfaces for protocol-specific
// diagnostic DTOs and projectors. Generic/common/neutral diagnostics and
// contribution-owned projectors are bounded, non-protocol infrastructure.
func centralDiagnosticOwnerName(name string) bool {
	lower := strings.ToLower(name)
	for _, neutral := range []string{"compatible", "plugin", "reload", "health", "inventory", "route", "attempt", "generic", "common", "neutral", "extension", "contribution", "instance"} {
		if strings.Contains(lower, neutral) {
			return false
		}
	}
	return strings.HasSuffix(lower, "row") || strings.HasSuffix(lower, "projector")
}

func DetectCentralProtocolDiagnosticsDebt(repoRoot string) (map[string]string, error) {
	fset := token.NewFileSet()
	targets := []string{
		filepath.Join(repoRoot, "internal", "core", "diag"),
		filepath.Join(repoRoot, "internal", "infra", "runtimebundle"),
	}
	debtItems := make(map[string]string)
	for _, targetDir := range targets {
		//nolint:staticcheck // Source mutation scanner intentionally needs ParseDir's file map.
		pkgs, err := parser.ParseDir(fset, targetDir, nil, 0)
		if err != nil && !os.IsNotExist(err) {
			continue
		}
		for _, pkg := range pkgs {
			for fname, fileNode := range pkg.Files {
				if strings.HasSuffix(fname, "_test.go") {
					continue
				}
				relPath, _ := filepath.Rel(repoRoot, fname)
				relPath = filepath.ToSlash(relPath)
				centralSurface := strings.Contains(relPath, "internal/core/diag") || strings.Contains(relPath, "internal/infra/runtimebundle")
				ast.Inspect(fileNode, func(n ast.Node) bool {
					if !centralSurface {
						return true
					}
					if typeSpec, ok := n.(*ast.TypeSpec); ok && centralDiagnosticOwnerName(typeSpec.Name.Name) {
						debtItems[typeSpec.Name.Name] = relPath
					}
					if fn, ok := n.(*ast.FuncDecl); ok && centralDiagnosticOwnerName(fn.Name.Name) {
						debtItems[fn.Name.Name] = relPath
					}
					if switchStmt, ok := n.(*ast.SwitchStmt); ok {
						for _, stmt := range switchStmt.Body.List {
							if clause, ok := stmt.(*ast.CaseClause); ok {
								for _, expr := range clause.List {
									if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
										value := strings.Trim(lit.Value, `"`)
										if centralDiagnosticOwnerName(value) {
											debtItems[fmt.Sprintf("switch-case:%s", value)] = relPath
										}
									}
								}
							}
						}
					}
					return true
				})
			}
		}
	}
	return debtItems, nil
}

// identName extracts the name from an *ast.Ident or dereferenced *ast.Ident.
func identName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

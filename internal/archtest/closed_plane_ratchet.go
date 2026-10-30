package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	RuleClosedPlaneNoArbitraryValueMaps   = "closed_plane_no_arbitrary_value_maps"
	RuleClosedPlaneNoReflectionFallback   = "closed_plane_no_reflection_fallback"
	RuleClosedPlaneNoMapReplayHelpers     = "closed_plane_no_map_replay_helpers"
	RuleClosedPlaneTypedStorageOnly       = "closed_plane_typed_storage_only"
	RuleClosedPlaneNoGlobalPlaneSelectors = "closed_plane_no_global_plane_selectors"
)

// ScanClosedPlaneViolations walks production Go files to verify that arbitrary-plane map/reflection
// fallback has been completely excised from the feature plane lifecycle.
func ScanClosedPlaneViolations(repoRoot string) ([]RuleFinding, error) {
	var findings []RuleFinding
	err := WalkProductionGoFiles(repoRoot, func(rel, abs string, src []byte) error {
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		findings = append(findings, ScanFileClosedPlaneViolations(rel, fset, f)...)
		return nil
	})
	return findings, err
}

// ScanFileClosedPlaneViolations inspects an AST for closed-plane architectural violations.
func ScanFileClosedPlaneViolations(relPath string, fset *token.FileSet, f *ast.File) []RuleFinding {
	normalizedRel := filepath.ToSlash(relPath)
	var findings []RuleFinding

	// Check legacy forbidden map helpers declared or called
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name != nil {
				if reason, forbidden := KnownLegacyForbiddenPlaneMapHelpers[node.Name.Name]; forbidden {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoMapReplayHelpers,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(node.Pos()).Line),
						Detail: fmt.Sprintf("declaration of forbidden map helper %q: %s", node.Name.Name, reason),
					})
				}
			}
		case *ast.CallExpr:
			ident := extractCalledIdent(node.Fun)
			if ident != "" {
				if reason, forbidden := KnownLegacyForbiddenPlaneMapHelpers[ident]; forbidden {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoMapReplayHelpers,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(node.Pos()).Line),
						Detail: fmt.Sprintf("call to forbidden map helper %q: %s", ident, reason),
					})
				}
			}
		}
		return true
	})

	if !strings.HasPrefix(normalizedRel, "pkg/aipsdk/feature/") {
		return findings
	}

	baseFile := filepath.Base(normalizedRel)
	isOperationalFile := ClosedPlaneOperationalFiles[baseFile]

	// 1. Build type symbol table for recursive structural checks within this file
	typeDefs := make(map[string]ast.Expr)
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name != nil {
			typeDefs[ts.Name.Name] = ts.Type
		}
		return true
	})

	// 2. Structural recursive inspection of closed-plane storage structs
	for typeName, expr := range typeDefs {
		if ClosedPlaneTargetStorageStructs[typeName] {
			if st, ok := expr.(*ast.StructType); ok {
				findings = append(findings, checkStructTypeForMaps(fset, normalizedRel, typeName, "", st, typeDefs, make(map[string]bool))...)
			}
		}
	}

	// 3. Structural operational logic inspection in pkg/aipsdk/feature
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			return true
		}

		funcName := fd.Name.Name
		qualFunc := "pkg/aipsdk/feature." + funcName

		// Check reflection allowance
		if !ClosedPlaneAllowedReflectionFuncs[funcName] && !ClosedPlaneAllowedReflectionFuncs[qualFunc] {
			ast.Inspect(fd.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := unwrapParen(call.Fun).(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := unwrapParen(sel.X).(*ast.Ident)
				if !ok || pkgIdent.Name != "reflect" {
					return true
				}
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoReflectionFallback,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(call.Pos()).Line),
					Detail: fmt.Sprintf("forbidden reflection call reflect.%s in function %q; dynamic plane reflection is removed", sel.Sel.Name, funcName),
				})
				return true
			})
		}

		if ClosedPlaneAllowedPackageMapFuncs[funcName] || ClosedPlaneAllowedPackageMapFuncs[qualFunc] {
			return true
		}

		recvType := extractReceiverTypeName(fd.Recv)
		isTargetFunc := isOperationalFile || (recvType != "" && ClosedPlaneTargetStorageStructs[recvType]) || isOperationalFuncName(funcName)
		if !isTargetFunc {
			return true
		}

		// Prohibit map parameters (except pluginIDs map[string]string)
		if fd.Type != nil && fd.Type.Params != nil {
			for _, p := range fd.Type.Params.List {
				resolved := resolveTypeExpr(p.Type, typeDefs, make(map[string]bool))
				if mt, isMap := resolved.(*ast.MapType); isMap {
					if len(p.Names) != 1 || p.Names[0].Name != "pluginIDs" || !isMapStringString(mt) {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoMapReplayHelpers,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(p.Pos()).Line),
							Detail: fmt.Sprintf("forbidden map parameter in closed-plane function %q; dynamic plane fallback is removed", funcName),
						})
					}
				}
			}
		}

		localMapVars := collectLocalMapVars(fd, typeDefs)

		ast.Inspect(fd.Body, func(inner ast.Node) bool {
			switch node := inner.(type) {
			case *ast.CallExpr:
				if extractCalledIdent(node.Fun) == "make" && len(node.Args) > 0 {
					resolved := resolveTypeExpr(node.Args[0], typeDefs, make(map[string]bool))
					if mt, isMap := resolved.(*ast.MapType); isMap && !isMapStringString(mt) {
						findings = append(findings, RuleFinding{
							Rule:   RuleClosedPlaneNoMapReplayHelpers,
							Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(node.Pos()).Line),
							Detail: fmt.Sprintf("forbidden make(map) in closed-plane function %q; dynamic plane fallback is removed", funcName),
						})
					}
				}
			case *ast.RangeStmt:
				rangeX := unwrapParen(node.X)
				if !isAllowedPluginIDsRangeExpr(rangeX) && isMapRangeTarget(rangeX, localMapVars, typeDefs) {
					findings = append(findings, RuleFinding{
						Rule:   RuleClosedPlaneNoMapReplayHelpers,
						Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(node.Pos()).Line),
						Detail: fmt.Sprintf("forbidden map range loop in closed-plane function %q; dynamic replay is removed", funcName),
					})
				}
			case *ast.TypeAssertExpr:
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoMapReplayHelpers,
					Path:   fmt.Sprintf("%s:%d", normalizedRel, fset.Position(node.Pos()).Line),
					Detail: fmt.Sprintf("forbidden dynamic type assertion in closed-plane function %q; typed plane storage is required", funcName),
				})
			}
			return true
		})
		return true
	})

	// 4. Reject PlaneX policy selectors and aliases outside narrow canonical initialization (narrowly for plane_generated.go)
	if isGeneratedPlanesFile(normalizedRel) {
		findings = append(findings, checkPlaneGeneratedSelectors(fset, normalizedRel, f)...)
	}

	return findings
}

func checkStructTypeForMaps(
	fset *token.FileSet,
	relPath, rootStructName, parentPath string,
	st *ast.StructType,
	typeDefs map[string]ast.Expr,
	visitedTypes map[string]bool,
) []RuleFinding {
	if st == nil || st.Fields == nil {
		return nil
	}
	var findings []RuleFinding
	for _, field := range st.Fields.List {
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{{Name: extractTypeName(field.Type)}}
		}
		for _, name := range names {
			fieldName := name.Name
			currentPath := fieldName
			if parentPath != "" {
				currentPath = parentPath + "." + fieldName
			}
			if isAllowedAttributionMapField(rootStructName, fieldName, field.Type) {
				continue
			}
			if fieldName == "values" || fieldName == "identities" || fieldName == "fallback" {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoArbitraryValueMaps,
					Path:   fmt.Sprintf("%s:%d", relPath, fset.Position(name.Pos()).Line),
					Detail: fmt.Sprintf("forbidden fallback field %q on struct %q; arbitrary plane storage is removed", currentPath, rootStructName),
				})
			}
			resolved := resolveTypeExpr(field.Type, typeDefs, copyVisited(visitedTypes))
			if _, isMap := resolved.(*ast.MapType); isMap {
				findings = append(findings, RuleFinding{
					Rule:   RuleClosedPlaneNoArbitraryValueMaps,
					Path:   fmt.Sprintf("%s:%d", relPath, fset.Position(name.Pos()).Line),
					Detail: fmt.Sprintf("forbidden map field %q on struct %q; arbitrary plane storage is removed", currentPath, rootStructName),
				})
			} else if subSt, isStruct := resolved.(*ast.StructType); isStruct {
				typeName := extractTypeName(field.Type)
				if typeName == "" || !visitedTypes[typeName] {
					if typeName != "" {
						visitedTypes[typeName] = true
					}
					findings = append(findings, checkStructTypeForMaps(fset, relPath, rootStructName, currentPath, subSt, typeDefs, visitedTypes)...)
				}
			}
		}
	}
	return findings
}

func resolveTypeExpr(expr ast.Expr, typeDefs map[string]ast.Expr, visited map[string]bool) ast.Expr {
	curr := expr
	for {
		switch t := curr.(type) {
		case *ast.Ident:
			if visited[t.Name] {
				return t
			}
			visited[t.Name] = true
			if target, ok := typeDefs[t.Name]; ok {
				curr = target
				continue
			}
			return t
		case *ast.StarExpr:
			curr = t.X
		default:
			return curr
		}
	}
}

func extractTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return extractTypeName(t.X)
	default:
		return ""
	}
}

func copyVisited(in map[string]bool) map[string]bool {
	return maps.Clone(in)
}

func isAllowedAttributionMapField(structName, fieldName string, expr ast.Expr) bool {
	if fieldName != "pluginIDs" {
		return false
	}
	allowedStructs := ClosedPlaneAllowedStorageMaps[structName]
	return allowedStructs != nil && allowedStructs[fieldName] != "" && isMapStringString(expr)
}

func isMapStringString(expr ast.Expr) bool {
	mt, ok := expr.(*ast.MapType)
	if !ok {
		return false
	}
	k, okK := mt.Key.(*ast.Ident)
	v, okV := mt.Value.(*ast.Ident)
	return okK && okV && k.Name == "string" && v.Name == "string"
}

func isAllowedPluginIDsRangeExpr(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name == "pluginIDs" || x.Name == "pluginIDsCopy"
	case *ast.SelectorExpr:
		return x.Sel.Name == "pluginIDs" || x.Sel.Name == "pluginIDsCopy"
	default:
		return false
	}
}

func extractReceiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return extractTypeName(recv.List[0].Type)
}

func isOperationalFuncName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "contribut") ||
		strings.Contains(lower, "replay") ||
		strings.Contains(lower, "freeze") ||
		strings.Contains(lower, "materializ") ||
		strings.Contains(lower, "validat")
}

func collectLocalMapVars(fd *ast.FuncDecl, typeDefs map[string]ast.Expr) map[string]bool {
	localMaps := make(map[string]bool)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range stmt.Rhs {
				if i < len(stmt.Lhs) && isMapInitExpr(rhs, typeDefs) {
					if id, ok := stmt.Lhs[i].(*ast.Ident); ok && id.Name != "pluginIDsCopy" && id.Name != "pluginIDs" {
						localMaps[id.Name] = true
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := stmt.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && vs.Type != nil {
						if _, isMap := resolveTypeExpr(vs.Type, typeDefs, make(map[string]bool)).(*ast.MapType); isMap {
							for _, name := range vs.Names {
								if name.Name != "pluginIDsCopy" && name.Name != "pluginIDs" {
									localMaps[name.Name] = true
								}
							}
						}
					}
				}
			}
		}
		return true
	})
	return localMaps
}

func isMapInitExpr(expr ast.Expr, typeDefs map[string]ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.CallExpr:
		if extractCalledIdent(x.Fun) == "make" && len(x.Args) > 0 {
			_, isMap := resolveTypeExpr(x.Args[0], typeDefs, make(map[string]bool)).(*ast.MapType)
			return isMap
		}
	case *ast.CompositeLit:
		_, isMap := resolveTypeExpr(x.Type, typeDefs, make(map[string]bool)).(*ast.MapType)
		return isMap
	}
	return false
}

func isMapRangeTarget(expr ast.Expr, localMapVars map[string]bool, typeDefs map[string]ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		if localMapVars[x.Name] {
			return true
		}
		if _, isMap := resolveTypeExpr(x, typeDefs, make(map[string]bool)).(*ast.MapType); isMap {
			return true
		}
		lower := strings.ToLower(x.Name)
		return strings.Contains(lower, "map") || lower == "values" || lower == "fallback" || lower == "identities"
	case *ast.SelectorExpr:
		selLower := strings.ToLower(x.Sel.Name)
		return strings.Contains(selLower, "map") || selLower == "values" || selLower == "fallback" || selLower == "identities"
	case *ast.CallExpr:
		return extractCalledIdent(x.Fun) == "make"
	default:
		return false
	}
}

func extractCalledIdent(fun ast.Expr) string {
	fun = unwrapParen(fun)
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return extractCalledIdent(x.X)
	case *ast.IndexListExpr:
		return extractCalledIdent(x.X)
	default:
		return ""
	}
}

func scanClosedPlaneSyntheticSource(t *testing.T, relPath, src string) []RuleFinding {
	t.Helper()
	fset, f, err := ParseGoSource(relPath, []byte(src))
	require.NoError(t, err)
	return ScanFileClosedPlaneViolations(relPath, fset, f)
}

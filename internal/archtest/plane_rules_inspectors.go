package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"strings"
)

func inspectAppendBody(fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd.Body == nil {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
			pos := fset.Position(sel.Pos())
			record(MirrorAppendBranch, name, meta.PlaneID,
				fmt.Sprintf("hand-authored Append branch for plane %q is forbidden in wave %s", name, meta.Wave),
				pos.Line, meta.Wave)
		}
		return true
	})
}

func inspectProjectionBody(fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd.Body == nil {
		return
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if k, ok := node.Key.(*ast.Ident); ok {
				if k.Name == "ToolReactorErrorPolicy" {
					return true
				}
				if meta, exists := KnownPlaneFields[k.Name]; exists && meta.Wave <= maxCompletedWave {
					pos := fset.Position(k.Pos())
					record(MirrorProjectionBranch, k.Name, meta.PlaneID,
						fmt.Sprintf("projection branch %q in %q is forbidden in wave %s", k.Name, fd.Name.Name, meta.Wave),
						pos.Line, meta.Wave)
				}
			}
		case *ast.SelectorExpr:
			name := node.Sel.Name
			if name == "ToolReactorErrorPolicy" {
				return true
			}
			if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
				pos := fset.Position(node.Pos())
				record(MirrorProjectionBranch, name, meta.PlaneID,
					fmt.Sprintf("projection branch %q in %q is forbidden in wave %s", name, fd.Name.Name, meta.Wave),
					pos.Line, meta.Wave)
			}
		}
		return true
	})
}

func receiverTypeName(t ast.Expr) string {
	switch expr := t.(type) {
	case *ast.StarExpr:
		if id, ok := expr.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return expr.Name
	}
	return ""
}

func isGenerationOpMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	typeName := receiverTypeName(fd.Recv.List[0].Type)
	return typeName == "GenerationBundle" || typeName == "generationOperations"
}

func inspectGenerationOpMethod(relPath string, f *ast.File, fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	meta, exists := KnownPlaneFields[fd.Name.Name]
	if !exists {
		return
	}
	if meta.Wave > maxCompletedWave {
		return
	}
	if IsThinDelegate(relPath, fd, f) {
		return
	}
	pos := fset.Position(fd.Pos())
	record(MirrorGenerationOpField, fd.Name.Name, meta.PlaneID,
		fmt.Sprintf("generation accessor %q does not delegate to Get in wave %s", fd.Name.Name, meta.Wave),
		pos.Line, meta.Wave)
}

func resolvePlaneMeta(expr ast.Expr) (PlaneFieldMetadata, bool) {
	var identName string
	switch e := expr.(type) {
	case *ast.Ident:
		identName = e.Name
	case *ast.SelectorExpr:
		identName = e.Sel.Name
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			rawID := strings.Trim(e.Value, `"`)
			for _, meta := range KnownPlaneFields {
				if meta.PlaneID == rawID {
					return meta, true
				}
			}
		}
		return PlaneFieldMetadata{}, false
	default:
		return PlaneFieldMetadata{}, false
	}

	if meta, exists := KnownPlaneFields[identName]; exists {
		return meta, true
	}
	if trimmed, ok := strings.CutPrefix(identName, "Plane"); ok {
		if meta, exists := KnownPlaneFields[trimmed]; exists {
			return meta, true
		}
	}
	for _, meta := range KnownPlaneFields {
		trimmed, _ := strings.CutPrefix(identName, "Plane")
		if strings.EqualFold(meta.PlaneID, identName) || strings.EqualFold(meta.PlaneID, trimmed) {
			return meta, true
		}
	}
	return PlaneFieldMetadata{}, false
}

func extractPlaneMetaFromCall(call *ast.CallExpr) (PlaneFieldMetadata, bool) {
	if call == nil {
		return PlaneFieldMetadata{}, false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if allowedRequestExecutionMethods[sel.Sel.Name] {
			if meta, ok := KnownPlaneFields[sel.Sel.Name]; ok {
				return meta, true
			}
		}
	}
	for _, arg := range call.Args {
		if meta, ok := resolvePlaneMeta(arg); ok {
			return meta, true
		}
	}
	return PlaneFieldMetadata{}, false
}

func inspectStageConsumers(relPath string, f *ast.File, fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd == nil || fd.Body == nil {
		return
	}

	qualSym := QualifiedSymbol(relPath, fd)
	isWhitelisted := IsAllowedStageConsumer(qualSym)

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isGetCall(relPath, call, f) {
			return true
		}
		meta, ok := extractPlaneMetaFromCall(call)
		if !ok {
			return true
		}
		if meta.Wave > maxCompletedWave {
			return true
		}

		pos := fset.Position(call.Pos())
		if !isWhitelisted {
			record(MirrorStageConsumer, fd.Name.Name, meta.PlaneID,
				fmt.Sprintf("stage consumer %q is not in AllowedStageConsumers allowlist in wave %s", qualSym, meta.Wave),
				pos.Line, meta.Wave)
			return true
		}
		if !IsThinDelegate(relPath, fd, f) {
			record(MirrorStageConsumer, fd.Name.Name, meta.PlaneID,
				fmt.Sprintf("stage consumer %q does not thinly delegate to Get in wave %s", qualSym, meta.Wave),
				pos.Line, meta.Wave)
		}
		return true
	})
}

var forbiddenCanonicalSDKPackages = map[string]bool{
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks":       true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog": true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request":     true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest":  true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint":   true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy":  true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall":    true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion":  true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response":    true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard": true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic":     true,
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn":   true,
}

var forbiddenFamilyMaterializers = map[string]bool{
	"MaterializeSorted":          true,
	"MaterializeAttemptsSorted":  true,
	"MaterializeSortedRedactors": true,
}

func isForbiddenMaterializerPackage(pkgIdent *ast.Ident, f *ast.File) bool {
	if pkgIdent == nil || f == nil {
		return false
	}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		p := strings.Trim(imp.Path.Value, `"`)
		if !forbiddenCanonicalSDKPackages[p] {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == pkgIdent.Name {
				return true
			}
		} else {
			defaultPkg := path.Base(p)
			if defaultPkg == pkgIdent.Name {
				return true
			}
		}
	}
	return false
}

func hasForbiddenDotImport(f *ast.File) bool {
	if f == nil {
		return false
	}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		p := strings.Trim(imp.Path.Value, `"`)
		if forbiddenCanonicalSDKPackages[p] && imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

func isFeatureBundleType(expr ast.Expr, f *ast.File) bool {
	if expr == nil {
		return false
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		return isFeatureBundleType(t.X, f)
	case *ast.ArrayType:
		return isFeatureBundleType(t.Elt, f)
	case *ast.SelectorExpr:
		if t.Sel.Name == "FeatureBundle" {
			return isFeaturePkgIdent("", t.X, f)
		}
	case *ast.Ident:
		if t.Name == "FeatureBundle" {
			return isFeaturePkgDotImportOrSamePkg("", f)
		}
	}
	return false
}

func collectFeatureBundleIdentifiers(fd *ast.FuncDecl, f *ast.File) map[string]bool {
	bundleVars := make(map[string]bool)

	// 1. Function parameters
	if fd.Type != nil && fd.Type.Params != nil {
		for _, field := range fd.Type.Params.List {
			if isFeatureBundleType(field.Type, f) {
				for _, name := range field.Names {
					bundleVars[name.Name] = true
				}
			}
		}
	}

	// 2. Local variable declarations and assignments
	if fd.Body != nil {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.DeclStmt:
				if genDecl, ok := stmt.Decl.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
					for _, spec := range genDecl.Specs {
						if valSpec, ok := spec.(*ast.ValueSpec); ok {
							if isFeatureBundleType(valSpec.Type, f) {
								for _, name := range valSpec.Names {
									bundleVars[name.Name] = true
								}
							}
						}
					}
				}
			case *ast.AssignStmt:
				for i, rhs := range stmt.Rhs {
					isBundle := isFeatureBundleType(rhs, f)
					if !isBundle {
						switch expr := rhs.(type) {
						case *ast.CompositeLit:
							isBundle = isFeatureBundleType(expr.Type, f)
						case *ast.UnaryExpr:
							if expr.Op == token.AND {
								if compLit, ok := expr.X.(*ast.CompositeLit); ok {
									isBundle = isFeatureBundleType(compLit.Type, f)
								}
							}
						case *ast.TypeAssertExpr:
							isBundle = isFeatureBundleType(expr.Type, f)
						case *ast.Ident:
							isBundle = bundleVars[expr.Name]
						case *ast.CallExpr:
							if sel, ok := expr.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "BuildFeatureBundle" {
								isBundle = true
							}
						}
					}
					if isBundle && i < len(stmt.Lhs) {
						if id, ok := stmt.Lhs[i].(*ast.Ident); ok {
							bundleVars[id.Name] = true
						}
					}
				}
			case *ast.RangeStmt:
				isSlice := isFeatureBundleType(stmt.X, f)
				if !isSlice {
					if id, ok := stmt.X.(*ast.Ident); ok && bundleVars[id.Name] {
						isSlice = true
					}
				}
				if isSlice && stmt.Value != nil {
					if id, ok := stmt.Value.(*ast.Ident); ok {
						bundleVars[id.Name] = true
					}
				}
			}
			return true
		})
	}

	return bundleVars
}

func inspectDiagnosticsBody(f *ast.File, fd *ast.FuncDecl, fset *token.FileSet, maxCompletedWave MigrationWave, record func(MirrorShapeKind, string, string, string, int, MigrationWave)) {
	if fd == nil || fd.Body == nil || maxCompletedWave < Wave5b_LocalTurnTerminal {
		return
	}

	bundleVars := collectFeatureBundleIdentifiers(fd, f)

	checkPlaneIDStringLit := func(expr ast.Expr) {
		if basicLit, ok := expr.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
			planeID := strings.Trim(basicLit.Value, `"`)
			if meta, exists := KnownPlaneIDs[planeID]; exists && meta.Wave <= maxCompletedWave {
				pos := fset.Position(basicLit.Pos())
				record(MirrorDiagArm, planeID, meta.PlaneID,
					fmt.Sprintf("diagnostics code branching on plane ID %q is forbidden in wave %s", planeID, meta.Wave),
					pos.Line, meta.Wave)
			}
		}
	}

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			isBundleRecv := false
			if xIdent, ok := node.X.(*ast.Ident); ok {
				if bundleVars[xIdent.Name] {
					isBundleRecv = true
				}
			} else if isFeatureBundleType(node.X, f) {
				isBundleRecv = true
			}

			if isBundleRecv {
				name := node.Sel.Name
				if meta, exists := KnownPlaneFields[name]; exists && meta.Wave <= maxCompletedWave {
					pos := fset.Position(node.Pos())
					record(MirrorDiagArm, name, meta.PlaneID,
						fmt.Sprintf("diagnostics arm accessing plane field %q is forbidden in wave %s", name, meta.Wave),
						pos.Line, meta.Wave)
				}
			}
		case *ast.BinaryExpr:
			if node.Op == token.EQL || node.Op == token.NEQ {
				checkPlaneIDStringLit(node.X)
				checkPlaneIDStringLit(node.Y)
			}
		case *ast.CaseClause:
			for _, expr := range node.List {
				checkPlaneIDStringLit(expr)
			}
		case *ast.IndexExpr:
			checkPlaneIDStringLit(node.Index)
		case *ast.CallExpr:
			switch fn := node.Fun.(type) {
			case *ast.SelectorExpr:
				fnName := fn.Sel.Name
				if forbiddenFamilyMaterializers[fnName] {
					if pkgIdent, ok := fn.X.(*ast.Ident); ok {
						if isForbiddenMaterializerPackage(pkgIdent, f) {
							pos := fset.Position(node.Pos())
							record(MirrorDiagArm, fnName, "",
								fmt.Sprintf("diagnostics code calling family materializer %q is forbidden in wave %s", fnName, maxCompletedWave),
								pos.Line, maxCompletedWave)
						}
					}
				}
			case *ast.Ident:
				fnName := fn.Name
				if forbiddenFamilyMaterializers[fnName] && hasForbiddenDotImport(f) {
					pos := fset.Position(node.Pos())
					record(MirrorDiagArm, fnName, "",
						fmt.Sprintf("diagnostics code calling family materializer %q is forbidden in wave %s", fnName, maxCompletedWave),
						pos.Line, maxCompletedWave)
				}
			}
		}
		return true
	})
}

package archtest

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"
)

func findStructFields(f *ast.File, structName string) (fields []string) {
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					fields = append(fields, name.Name)
				}
			}
		}
		return false
	})
	return
}

func findStructFieldsInFiles(files []turnRecvASTFile, name string) []string {
	for _, file := range files {
		if fields := findStructFields(file.AST, name); len(fields) > 0 {
			return fields
		}
	}
	return nil
}

func hasTypeInFiles(files []turnRecvASTFile, typeName string) bool {
	for _, file := range files {
		found := false
		ast.Inspect(file.AST, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if ok && ts.Name.Name == typeName {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func findPointerOutFieldsInFiles(files []turnRecvASTFile) (fields []string) {
	for _, file := range files {
		ast.Inspect(file.AST, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "recoveryController" {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				for _, f := range st.Fields.List {
					if _, isPtr := f.Type.(*ast.StarExpr); isPtr {
						for _, name := range f.Names {
							switch name.Name {
							case "lastHardReject", "lastHardTransportReject", "lastAdmissionErr", "lastParallelFailure", "isContextLimitExhaustion", "transformExcludes":
								fields = append(fields, name.Name)
							}
						}
					}
				}
			}
			return false
		})
	}
	return
}

func findDuplicatedFieldsInFiles(files []turnRecvASTFile, fields1, fields2 []string) (dup []string) {
	set := make(map[string]bool)
	for _, f := range fields1 {
		set[f] = true
	}
	for _, f := range fields2 {
		if set[f] {
			switch f {
			case "budget", "ttft", "sel", "requestSize", "session", "excluded", "rng", "interleaved":
				dup = append(dup, f)
			}
		}
	}
	return
}

func findTranslationSites(files []turnRecvASTFile) (sites []string) {
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			// Record file plus enclosing function name (file only when there
			// is none) so behavior-preserving line shifts do not move the baseline.
			var body ast.Node
			name := ""
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				name = d.Name.Name
				if d.Recv != nil {
					for _, field := range d.Recv.List {
						switch rt := field.Type.(type) {
						case *ast.Ident:
							name = rt.Name + "." + name
						case *ast.StarExpr:
							if id, ok := rt.X.(*ast.Ident); ok {
								name = id.Name + "." + name
							}
						}
					}
				}
				body = d.Body
			case *ast.GenDecl:
				body = d
			default:
				continue
			}
			ast.Inspect(body, func(n ast.Node) bool {
				if cl, ok := n.(*ast.CompositeLit); ok {
					if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "routePlanState" {
						pos := file.FSet.Position(cl.Pos())
						rel := filepath.ToSlash(pos.Filename)
						if idx := strings.Index(rel, "internal/"); idx != -1 {
							rel = rel[idx:]
						}
						if name == "" {
							sites = append(sites, rel)
						} else {
							sites = append(sites, fmt.Sprintf("%s:%s", rel, name))
						}
					}
				}
				return true
			})
		}
	}
	return
}

func countDirectFieldAssignments(files []turnRecvASTFile) (count int) {
	carriers := map[string]bool{"preparedRequest": true, "routePlanState": true, "attemptOpenParams": true, "attemptOpenResult": true, "recvTurnFacts": true, "recvTurnFactsInput": true, "recoveryControllerInput": true, "attemptSessionInput": true}
	for _, file := range files {
		ast.Inspect(file.AST, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); ok && carriers[id.Name] {
					count += len(x.Elts)
				}
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					if isFieldSelectorOf(lhs) {
						count++
					}
				}
				for _, rhs := range x.Rhs {
					if isFieldSelectorOf(rhs) {
						count++
					}
				}
			}
			return true
		})
	}
	return
}

func isFieldSelectorOf(expr ast.Expr) bool {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			name := strings.ToLower(id.Name)
			return name == "prep" || name == "plan" || name == "out" || name == "in" || name == "p" || name == "rs" || name == "facts"
		}
	}
	return false
}

func countContextBusinessStateRereads(files []turnRecvASTFile) (count int) {
	rereadFuncs := map[string]bool{"FromContext": true, "SecureSessionTurnFromContext": true, "BoundViewFromContext": true, "NativeModelResolverFromContext": true, "RouteCandidatePreferences": true}
	for _, file := range files {
		if !strings.HasSuffix(file.RelPath, "_test.go") {
			ast.Inspect(file.AST, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					pkg, name := qualifiedCall(call.Fun)
					if rereadFuncs[name] && (pkg == "execctx" || pkg == "modelregistry" || pkg == "modelcatalog" || pkg == "modelview" || pkg == "routing") {
						count++
					}
				}
				return true
			})
		}
	}
	return
}

func countPreHandoffCleanupSites(files []turnRecvASTFile) (count int) {
	for _, file := range files {
		if !strings.HasSuffix(file.RelPath, "_test.go") {
			ast.Inspect(file.AST, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					_, name := qualifiedCall(call.Fun)
					switch name {
					case "releaseRequestAuthority", "finalizeIncurredOrRelease", "Release":
						count++
					case "Cancel":
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if id, ok := sel.X.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "scope") {
								count++
							}
						}
					}
				}
				return true
			})
		}
	}
	return
}

func evaluateRequestAttemptTarget(current RequestAttemptStateInventory, target RequestAttemptStateTarget) []string {
	var findings []string
	if target.AttemptOpenParamsDeleted && len(current.AttemptOpenParamsFields) > 0 {
		findings = append(findings, "target requires attemptOpenParams to be deleted, but type or fields exist")
	}
	if len(current.PointerOutFields) > target.MaxPointerOutFields {
		findings = append(findings, fmt.Sprintf("pointer-out fields=%d > target max %d (%v)", len(current.PointerOutFields), target.MaxPointerOutFields, current.PointerOutFields))
	}
	if len(current.RouteProgressDuplicatedFields) > target.MaxRouteProgressDuplicates {
		findings = append(findings, fmt.Sprintf("route progress duplicates=%d > target max %d (%v)", len(current.RouteProgressDuplicatedFields), target.MaxRouteProgressDuplicates, current.RouteProgressDuplicatedFields))
	}
	if len(current.TranslationSites) > target.MaxTranslationSites {
		findings = append(findings, fmt.Sprintf("translation sites=%d > target max %d (%v)", len(current.TranslationSites), target.MaxTranslationSites, current.TranslationSites))
	}
	if current.DirectFieldCopyAssignments > target.MaxDirectFieldCopyAssignments {
		findings = append(findings, fmt.Sprintf("direct field copy assignments=%d > target max %d", current.DirectFieldCopyAssignments, target.MaxDirectFieldCopyAssignments))
	}
	if current.ContextBusinessStateRereads > target.MaxContextBusinessStateRereads {
		findings = append(findings, fmt.Sprintf("context business state re-reads=%d > target max %d", current.ContextBusinessStateRereads, target.MaxContextBusinessStateRereads))
	}
	if current.PreHandoffCleanupSites > target.MaxPreHandoffCleanupSites {
		findings = append(findings, fmt.Sprintf("pre-handoff cleanup sites=%d > target max %d", current.PreHandoffCleanupSites, target.MaxPreHandoffCleanupSites))
	}
	return findings
}

func findHandoffSeamFromAST(files []turnRecvASTFile) (RequestAttemptHandoffSeam, error) {
	var seam RequestAttemptHandoffSeam

	var assembleFunc *ast.FuncDecl
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "assemble" {
				assembleFunc = fd
				break
			}
		}
	}
	if assembleFunc == nil {
		return seam, fmt.Errorf("assemble function not found in AST")
	}

	varValTypes := make(map[string]string)
	funcReturnTypes := make(map[string]string)
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if fd.Type.Results != nil && len(fd.Type.Results.List) > 0 {
					funcReturnTypes[fd.Name.Name] = typeExprToString(fd.Type.Results.List[0].Type)
				}
			}
		}
	}

	var receiverType string
	ast.Inspect(assembleFunc.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == 1 && len(n.Rhs) == 1 {
				if id, ok := n.Lhs[0].(*ast.Ident); ok {
					if call, ok := n.Rhs[0].(*ast.CallExpr); ok {
						if callId, ok := call.Fun.(*ast.Ident); ok {
							if retType, ok := funcReturnTypes[callId.Name]; ok {
								varValTypes[id.Name] = retType
							}
						}
					}
				}
			}
		case *ast.CompositeLit:
			hasFacts := false
			hasAttempt := false
			hasRecovery := false
			for _, elt := range n.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						switch id.Name {
						case "facts":
							hasFacts = true
						case "attempt":
							hasAttempt = true
						case "recovery":
							hasRecovery = true
						}
					}
				}
			}
			if hasFacts && hasAttempt && hasRecovery {
				receiverType = typeExprToString(n.Type)
				seam.ReceiverType = receiverType
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if keyIdent, ok := kv.Key.(*ast.Ident); ok {
							fieldName := keyIdent.Name
							switch fieldName {
							case "facts":
								seam.FactsType = resolveExprType(kv.Value, varValTypes, files)
							case "attempt":
								seam.AttemptType = resolveExprType(kv.Value, varValTypes, files)
							case "recovery":
								seam.RecoveryType = resolveExprType(kv.Value, varValTypes, files)
							}
						}
					}
				}
				return false
			}
		}
		return true
	})

	if receiverType == "" {
		return seam, fmt.Errorf("handoff receiver struct literal containing facts, attempt, and recovery fields not found in assemble function")
	}

	seam.ReceiverFields = findStructFieldsInFiles(files, receiverType)
	return seam, nil
}

func typeExprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeExprToString(t.X)
	default:
		return ""
	}
}

func resolveExprType(expr ast.Expr, varTypes map[string]string, files []turnRecvASTFile) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if t, ok := varTypes[e.Name]; ok {
			return t
		}
		return e.Name
	case *ast.CompositeLit:
		return typeExprToString(e.Type)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "plan" {
			var fieldType string
			for _, file := range files {
				ast.Inspect(file.AST, func(n ast.Node) bool {
					if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "routePlanState" {
						if st, ok := ts.Type.(*ast.StructType); ok {
							for _, f := range st.Fields.List {
								for _, name := range f.Names {
									if name.Name == e.Sel.Name {
										fieldType = typeExprToString(f.Type)
										return false
									}
								}
							}
						}
					}
					return true
				})
				if fieldType != "" {
					return fieldType
				}
			}
		}
		return e.Sel.Name
	default:
		return ""
	}
}

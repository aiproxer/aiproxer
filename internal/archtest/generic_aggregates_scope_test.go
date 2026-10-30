package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archTestModulePath prevents foreign packages from spoofing approved identities.
const archTestModulePath = "github.com/aiproxer/aiproxer"

// archPkgFile is one parsed non-test file plus its local import names.
type archPkgFile struct {
	name    string
	file    *ast.File
	imports map[string]string
}

// archPkgScope holds declarations, alias chains, and imports for identity checks.
type archPkgScope struct {
	packagePath string
	files       []*archPkgFile
	structs     map[string]*ast.StructType
	ownerOf     map[string]*archPkgFile
	declared    map[string]ast.Expr
	declFile    map[string]*archPkgFile
	// aliasOf distinguishes transparent aliases from opaque defined types.
	aliasOf map[string]bool
}

// archForbiddenFeatureTokens flags per-feature names in generic aggregates.
var archForbiddenFeatureTokens = []string{
	"reasoning",
	"keepwarm",
	"compaction",
	"secretguard",
	"interleavedthinking",
	"interleaved",
	"sessionpolicy",

	"sessionclassification",
}

func archForbiddenPkg(importPath string) bool {
	if strings.HasPrefix(importPath, archTestModulePath+"/internal/plugins/features") {
		return true
	}
	if strings.HasPrefix(importPath, archTestModulePath+"/internal/standardplugins/featurehost/") {
		return true
	}
	if strings.Contains(importPath, "secretguardcompose") ||
		strings.Contains(importPath, "compactioncompose") ||
		strings.Contains(importPath, "reasoningcompose") ||
		strings.Contains(importPath, "reasoningreplay") {
		return true
	}
	return false
}

func archFileImports(node *ast.File) map[string]string {
	imports := make(map[string]string, len(node.Imports))
	for _, imp := range node.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		localName := filepath.Base(importPath)
		if imp.Name != nil {
			localName = imp.Name.Name
		}
		imports[localName] = importPath
	}
	return imports
}

func archScopeFromParsed(files map[string]*ast.File) *archPkgScope {
	scope := &archPkgScope{
		structs:  make(map[string]*ast.StructType),
		ownerOf:  make(map[string]*archPkgFile),
		declared: make(map[string]ast.Expr),
		declFile: make(map[string]*archPkgFile),
		aliasOf:  make(map[string]bool),
	}
	for name, node := range files {
		pf := &archPkgFile{name: name, file: node, imports: archFileImports(node)}
		scope.files = append(scope.files, pf)
		for _, decl := range node.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := unwrapArchParens(ts.Type).(*ast.StructType); ok {
					if _, exists := scope.structs[ts.Name.Name]; !exists {
						scope.structs[ts.Name.Name] = st
						scope.ownerOf[ts.Name.Name] = pf
					}
				}
				if _, exists := scope.declared[ts.Name.Name]; !exists {
					scope.declared[ts.Name.Name] = ts.Type
					scope.declFile[ts.Name.Name] = pf
					scope.aliasOf[ts.Name.Name] = ts.Assign.IsValid()
				}
			}
		}
	}
	return scope
}

func archParseDir(t *testing.T, dir string) *archPkgScope {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files[name] = node
	}
	scope := archScopeFromParsed(files)
	rel, err := filepath.Rel(repoRoot(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	scope.packagePath = archTestModulePath + "/" + filepath.ToSlash(rel)
	return scope
}

func archParseSources(t *testing.T, sources map[string]string) *archPkgScope {
	t.Helper()
	files := make(map[string]*ast.File, len(sources))
	for name, src := range sources {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files[name] = node
	}
	return archScopeFromParsed(files)
}

// scanStructForFeatureFields is the single-file entry point used by the
// direct-reference negative fixtures; it delegates to the package scope.
func scanStructForFeatureFields(node *ast.File, structName string, allowedExceptions map[string]string) []string {
	return archScopeFromParsed(map[string]*ast.File{"synthetic.go": node}).scan(structName, allowedExceptions)
}

// splitArchShape unwraps pointer/array/paren layers, returning the shape prefix.
func splitArchShape(expr ast.Expr) (string, ast.Expr) {
	var shape strings.Builder
	curr := expr
	for {
		switch t := curr.(type) {
		case *ast.StarExpr:
			shape.WriteString("*")
			curr = t.X
		case *ast.ArrayType:
			shape.WriteString("[]")
			curr = t.Elt
		case *ast.ParenExpr:
			curr = t.X
		default:
			return shape.String(), curr
		}
	}
}

// resolveArchLocal follows alias and defined chains to a local or imported type.
func (s *archPkgScope) resolveArchLocal(name string) (importPath, typeName string, ok bool) {
	seen := make(map[string]bool)
	curr := name
	for {
		if seen[curr] {
			return "", "", false
		}
		seen[curr] = true
		if _, isStruct := s.structs[curr]; isStruct {
			return "", curr, true
		}
		under, declared := s.declared[curr]
		if !declared {
			return "", "", false
		}
		owner := s.declFile[curr]
		_, base := splitArchShape(under)
		switch b := base.(type) {
		case *ast.SelectorExpr:
			x, ok := b.X.(*ast.Ident)
			if !ok {
				return "", "", false
			}
			p, ok := owner.imports[x.Name]
			if !ok {
				return "", "", false
			}
			return p, b.Sel.Name, true
		case *ast.Ident:
			curr = b.Name
		default:
			return "", curr, true
		}
	}
}

// forbiddenPkgInType reports whether a field type references a forbidden
// feature package, directly or through a same-package chain. Only type
// positions are visited (full audit in generic_aggregates_scope_resolve_test.go);
// inline-struct field names are never type references. Array lengths are not
// visited: an index length is never a type.
func (s *archPkgScope) forbiddenPkgInType(expr ast.Expr, file *archPkgFile) (string, bool) {
	bad := ""
	hit := false
	var visit func(e ast.Expr)
	visit = func(e ast.Expr) {
		if hit || e == nil {
			return
		}
		switch t := e.(type) {
		case *ast.SelectorExpr:
			if x, ok := t.X.(*ast.Ident); ok {
				if p, ok := file.imports[x.Name]; ok && archForbiddenPkg(p) {
					bad, hit = p, true
				}
			}
		case *ast.Ident:
			if p, _, ok := s.resolveArchLocal(t.Name); ok && p != "" && archForbiddenPkg(p) {
				bad, hit = p, true
			} else if badDecl, hitDecl := s.forbiddenInDeclType(t.Name, make(map[string]bool)); hitDecl {
				bad, hit = badDecl, true
			}
		case *ast.StarExpr:
			visit(t.X)
		case *ast.ArrayType:
			visit(t.Elt)
		case *ast.MapType:
			visit(t.Key)
			visit(t.Value)
		case *ast.ChanType:
			visit(t.Value)
		case *ast.Ellipsis:
			visit(t.Elt)
		case *ast.ParenExpr:
			visit(t.X)
		case *ast.StructType:
			if t.Fields != nil {
				for _, f := range t.Fields.List {
					visit(f.Type)
				}
			}
		case *ast.FuncType:
			if t.Params != nil {
				for _, f := range t.Params.List {
					visit(f.Type)
				}
			}
			if t.Results != nil {
				for _, f := range t.Results.List {
					visit(f.Type)
				}
			}
		case *ast.InterfaceType:
			if t.Methods != nil {
				for _, f := range t.Methods.List {
					visit(f.Type)
				}
			}
		case *ast.IndexExpr:
			visit(t.X)
			visit(t.Index)
		case *ast.IndexListExpr:
			visit(t.X)
			for _, idx := range t.Indices {
				visit(idx)
			}
		}
	}
	visit(expr)
	return bad, hit
}

// parseArchApprovedRef extracts shape, package path, and name; local names stay local.
func parseArchApprovedRef(expected string) (shape, path, name string) {
	rest := expected
	for strings.HasPrefix(rest, "*") || strings.HasPrefix(rest, "[]") {
		if strings.HasPrefix(rest, "*") {
			shape += "*"
			rest = rest[1:]
		} else {
			shape += "[]"
			rest = rest[2:]
		}
	}
	if idx := strings.LastIndex(rest, "."); idx >= 0 && strings.Contains(rest[:idx], "/") {
		return shape, rest[:idx], rest[idx+1:]
	}
	return shape, "", rest
}

// approvedExceptionMatches requires exact resolved package, type, and composed shape.
// Aliases preserve identity; defined types and foreign package spellings cannot spoof it.
func (s *archPkgScope) approvedExceptionMatches(expr ast.Expr, file *archPkgFile, expected string) bool {
	expShape, expPath, expName := parseArchApprovedRef(expected)
	actShape, actPath, actName, ok := s.resolveArchFullType(expr, file)
	if !ok {
		return false
	}
	return actShape == expShape && actPath == expPath && actName == expName
}

// namedArchRefs returns same-package candidate type names referenced by a
// field type (audit table in generic_aggregates_scope_resolve_test.go).
func namedArchRefs(expr ast.Expr) []string {
	switch t := expr.(type) {
	case *ast.Ident:
		return []string{t.Name}
	case *ast.StarExpr:
		return namedArchRefs(t.X)
	case *ast.ArrayType:
		return namedArchRefs(t.Elt)
	case *ast.MapType:
		return append(namedArchRefs(t.Key), namedArchRefs(t.Value)...)
	case *ast.ChanType:
		return namedArchRefs(t.Value)
	case *ast.Ellipsis:
		return namedArchRefs(t.Elt)
	case *ast.ParenExpr:
		return namedArchRefs(t.X)
	case *ast.FuncType:
		return append(namedArchFieldRefs(t.Params), namedArchFieldRefs(t.Results)...)
	case *ast.InterfaceType:
		return namedArchFieldRefs(t.Methods)
	case *ast.IndexExpr:
		return append(namedArchRefs(t.X), namedArchRefs(t.Index)...)
	case *ast.IndexListExpr:
		refs := namedArchRefs(t.X)
		for _, idx := range t.Indices {
			refs = append(refs, namedArchRefs(idx)...)
		}
		return refs
	default:
		return nil
	}
}

// scan checks one aggregate struct with its own exceptions; see scanWithRows.
func (s *archPkgScope) scan(structName string, allowedExceptions map[string]string) []string {
	return s.scanWithRows(structName, map[string]map[string]string{structName: allowedExceptions})
}

// scanWithRows checks roots and nested groups, preserving each struct's approvals.
func (s *archPkgScope) scanWithRows(root string, rows map[string]map[string]string) []string {
	rootST, ok := s.structs[root]
	if !ok {
		return nil
	}
	rootExceptions := rows[root]
	lookupApproval := func(curr, qualName, fieldName string) (string, bool) {
		if row := rows[curr]; row != nil {
			if exp, ok := row[qualName]; ok {
				return exp, true
			}
			if exp, ok := row[fieldName]; ok {
				return exp, true
			}
		}
		if rootExceptions != nil {
			if exp, ok := rootExceptions[qualName]; ok {
				return exp, true
			}
			if exp, ok := rootExceptions[fieldName]; ok {
				return exp, true
			}
		}
		return "", false
	}
	var violations []string
	visited := make(map[string]bool)
	var scanNamed func(currName string, st *ast.StructType, owner *archPkgFile)
	var scanFields func(prefix string, st *ast.StructType, owner *archPkgFile)
	recurseNamed := func(ref string) {
		if visited[ref] {
			return
		}
		if target, ok := s.structs[ref]; ok {
			scanNamed(ref, target, s.ownerOf[ref])
			return
		}
		if p, n, ok := s.resolveArchLocal(ref); ok && p == "" {
			if target, ok := s.structs[n]; ok {
				scanNamed(n, target, s.ownerOf[n])
			}
		}
	}
	scanFields = func(prefix string, st *ast.StructType, owner *archPkgFile) {
		for _, field := range st.Fields.List {
			shape, pkg, name, ok := s.resolveArchFullType(field.Type, owner)
			if pkg == "" {
				pkg = s.packagePath
			}
			// The exact core-owned ports aggregate is one approved seam.
			corePorts := ok && shape == "" && pkg == archTestModulePath+"/internal/core/runtime" && name == "CorePorts"
			typeStr := aggregateFieldTypeToString(field.Type)
			isEmbedded := len(field.Names) == 0
			var namesToCheck []string
			if isEmbedded {
				derived := typeStr
				if star, ok := field.Type.(*ast.StarExpr); ok {
					derived = aggregateFieldTypeToString(star.X)
				}
				if sel, ok := field.Type.(*ast.SelectorExpr); ok {
					derived = sel.Sel.Name
				} else if star, ok := field.Type.(*ast.StarExpr); ok {
					if sel, ok := star.X.(*ast.SelectorExpr); ok {
						derived = sel.Sel.Name
					}
				}
				namesToCheck = append(namesToCheck, derived)
			} else {
				for _, n := range field.Names {
					namesToCheck = append(namesToCheck, n.Name)
				}
			}
			for _, fieldName := range namesToCheck {
				qualName := prefix + "." + fieldName
				allowed := false
				if exp, ok := lookupApproval(prefix, qualName, fieldName); ok {
					if !s.approvedExceptionMatches(field.Type, owner, exp) {
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type does not match approved type %q",
								qualName, typeStr, exp))
						continue
					}
					allowed = true
				}
				label := qualName
				if isEmbedded {
					label = fmt.Sprintf("%s.[embedded %s]", prefix, typeStr)
				}
				if !allowed {
					if bad, found := s.forbiddenPkgInType(field.Type, owner); found {
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type imports forbidden feature package %q",
								label, typeStr, bad))
						continue
					}
					lowerFieldName := strings.ToLower(fieldName)
					for _, tok := range archForbiddenFeatureTokens {
						if strings.Contains(lowerFieldName, tok) {
							violations = append(violations,
								fmt.Sprintf("%s: field name contains forbidden feature token %q",
									label, tok))
							break
						}
					}
					lowerTypeStr := strings.ToLower(typeStr)
					for _, tok := range archForbiddenFeatureTokens {
						if strings.Contains(lowerTypeStr, tok) {
							violations = append(violations,
								fmt.Sprintf("%s (%s): field type contains forbidden feature token %q",
									label, typeStr, tok))
							break
						}
					}
				}
				if corePorts {
					continue
				}
				for _, ref := range namedArchRefs(field.Type) {
					recurseNamed(ref)
				}
				s.scanNestedInline(qualName, field.Type, owner, make(map[string]bool), scanFields, recurseNamed)
			}
		}
	}
	scanNamed = func(currName string, st *ast.StructType, owner *archPkgFile) {
		if visited[currName] {
			return
		}
		visited[currName] = true
		scanFields(currName, st, owner)
	}
	scanNamed(root, rootST, s.ownerOf[root])
	return violations
}

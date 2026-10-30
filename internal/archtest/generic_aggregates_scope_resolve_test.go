package archtest

import (
	"go/ast"
	"strings"
	"testing"
)

// Alias/defined-container resolution for the generic-aggregates ratchet.
//
// R3a: forbiddenPkgInType must see through alias AND defined container
// declarations (map/array/chan/func/interface shapes) instead of stopping at
// the declaration boundary. R3b: exception matching must compare the complete
// resolved shape (outer field shape composed with alias-inner shapes) and must
// treat defined types as opaque identities distinct from what they wrap.
//
// Type-position audit (every go/ast Expr valid in type position vs. the four
// R3 type switches — forbiddenInDeclExpr, forbiddenPkgInType, namedArchRefs,
// aggregateFieldTypeToString; Unhandled column justifies each gap):
//
//	Ident: all four handle (name / resolve / name / name).
//	SelectorExpr: walkers check-then-terminal; namedArchRefs nil (foreign
//	  package, no same-package name); strings render pkg.Type.
//	StarExpr, ArrayType, MapType, ChanType, Ellipsis, ParenExpr: all four
//	  recurse into the wrapped type.
//	StructType: walkers visit fields; namedArchRefs nil (inline struct has no
//	  name); field-name scanning reaches anonymous structs explicitly via
//	  scanNestedInline, which recurses through Index/IndexList type args,
//	  ParenExpr, FuncType params/results, and Array/Star/Chan/Ellipsis/Map
//	  shapes (resolving alias/defined chains with cycle protection) and scans
//	  each anonymous StructType with the enclosing field prefix; strings
//	  render "struct".
//	FuncType, InterfaceType: walkers visit params/results/methods;
//	  namedArchRefs collects member refs (audit fix: locals only reachable as
//	  func params were previously never recursed into); strings render the
//	  func signature / "interface".
//	IndexExpr, IndexListExpr: all four walk base plus every type argument
//	  (audit fix: generic instantiations previously bypassed all walkers).
//	All other ast.Expr kinds (BasicLit, CallExpr, TypeAssertExpr, ...): not
//	  valid in type position, default branch is unreachable for compilable
//	  aggregate code; BadExpr only arises from syntax errors. Array lengths
//	  are deliberately not visited (an index length is never a type).

// unwrapArchParens strips (possibly nested) ParenExpr wrappers.
func unwrapArchParens(expr ast.Expr) ast.Expr {
	for p, ok := expr.(*ast.ParenExpr); ok; p, ok = expr.(*ast.ParenExpr) {
		expr = p.X
	}
	return expr
}

// forbiddenInDeclType reports whether the declaration of local type name
// references a forbidden feature package, walking into container shapes and
// following nested local declarations. visiting guards alias cycles.
func (s *archPkgScope) forbiddenInDeclType(name string, visiting map[string]bool) (string, bool) {
	if visiting[name] {
		return "", false
	}
	under, declared := s.declared[name]
	if !declared {
		return "", false
	}
	visiting[name] = true
	return s.forbiddenInDeclExpr(under, s.declFile[name], visiting)
}

// forbiddenInDeclExpr scans one declaration expression for forbidden package
// references using the declaration owner's import map. Nested local names are
// followed through both aliases and defined types with cycle protection.
//
// Type-position exhaustiveness: every go/ast expression valid in type position
// is handled — Ident, SelectorExpr (checked terminal), StarExpr, ArrayType,
// MapType, ChanType, Ellipsis, ParenExpr, StructType, FuncType, InterfaceType,
// IndexExpr and IndexListExpr (base plus every type argument; args may nest,
// carry selectors, or name local aliases, all handled by recursion). All other
// ast.Expr kinds (BasicLit, CallExpr, TypeAssertExpr, ...) are not valid in
// type position; BadExpr only arises from syntax errors. Array lengths are not
// visited: an index length is never a type reference.
func (s *archPkgScope) forbiddenInDeclExpr(e ast.Expr, file *archPkgFile, visiting map[string]bool) (string, bool) {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if p, ok := file.imports[x.Name]; ok && archForbiddenPkg(p) {
				return p, true
			}
		}
		return "", false
	case *ast.Ident:
		if p, _, ok := s.resolveArchLocal(t.Name); ok && p != "" && archForbiddenPkg(p) {
			return p, true
		}
		if _, declared := s.declared[t.Name]; declared {
			return s.forbiddenInDeclType(t.Name, visiting)
		}
		return "", false
	case *ast.StarExpr:
		return s.forbiddenInDeclExpr(t.X, file, visiting)
	case *ast.ArrayType:
		return s.forbiddenInDeclExpr(t.Elt, file, visiting)
	case *ast.MapType:
		if bad, hit := s.forbiddenInDeclExpr(t.Key, file, visiting); hit {
			return bad, true
		}
		return s.forbiddenInDeclExpr(t.Value, file, visiting)
	case *ast.ChanType:
		return s.forbiddenInDeclExpr(t.Value, file, visiting)
	case *ast.Ellipsis:
		return s.forbiddenInDeclExpr(t.Elt, file, visiting)
	case *ast.ParenExpr:
		return s.forbiddenInDeclExpr(t.X, file, visiting)
	case *ast.StructType:
		if t.Fields != nil {
			for _, f := range t.Fields.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	case *ast.FuncType:
		if t.Params != nil {
			for _, f := range t.Params.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		if t.Results != nil {
			for _, f := range t.Results.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	case *ast.InterfaceType:
		if t.Methods != nil {
			for _, f := range t.Methods.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	case *ast.IndexExpr:
		if bad, hit := s.forbiddenInDeclExpr(t.X, file, visiting); hit {
			return bad, true
		}
		return s.forbiddenInDeclExpr(t.Index, file, visiting)
	case *ast.IndexListExpr:
		if bad, hit := s.forbiddenInDeclExpr(t.X, file, visiting); hit {
			return bad, true
		}
		for _, idx := range t.Indices {
			if bad, hit := s.forbiddenInDeclExpr(idx, file, visiting); hit {
				return bad, true
			}
		}
		return "", false
	default:
		return "", false
	}
}

// namedArchFieldRefs collects candidate same-package names from one func
// param/result or interface method list.
func namedArchFieldRefs(fields *ast.FieldList) []string {
	var refs []string
	if fields == nil {
		return nil
	}
	for _, f := range fields.List {
		refs = append(refs, namedArchRefs(f.Type)...)
	}
	return refs
}

// resolveArchFullType resolves a field type to its complete identity: the// composed shape plus the terminal package path and type name. Alias
// declarations are transparent (their inner shapes compose with the outer
// field shape); defined types are opaque (traversal stops and reports the
// defined name itself). Selectors resolve through the owning file's imports.
func (s *archPkgScope) resolveArchFullType(expr ast.Expr, file *archPkgFile) (shape, importPath, typeName string, ok bool) {
	shape, base := splitArchShape(expr)
	switch b := base.(type) {
	case *ast.SelectorExpr:
		x, ok := b.X.(*ast.Ident)
		if !ok {
			return "", "", "", false
		}
		p, ok := file.imports[x.Name]
		if !ok {
			return "", "", "", false
		}
		return shape, p, b.Sel.Name, true
	case *ast.Ident:
		return s.resolveArchFullName(b.Name, shape, make(map[string]bool))
	default:
		return "", "", "", false
	}
}

// resolveArchFullName walks one local name through alias declarations,
// composing shapes. A struct or defined type ends the walk as a same-package
// identity; an alias to another package ends it as a qualified identity.
func (s *archPkgScope) resolveArchFullName(name, shape string, visiting map[string]bool) (string, string, string, bool) {
	curr := name
	for {
		if visiting[curr] {
			return "", "", "", false
		}
		visiting[curr] = true
		if _, isStruct := s.structs[curr]; isStruct {
			return shape, "", curr, true
		}
		under, declared := s.declared[curr]
		if !declared {
			return "", "", "", false
		}
		if !s.aliasOf[curr] {
			return shape, "", curr, true
		}
		innerShape, innerBase := splitArchShape(under)
		shape += innerShape
		switch b := innerBase.(type) {
		case *ast.SelectorExpr:
			x, ok := b.X.(*ast.Ident)
			if !ok {
				return "", "", "", false
			}
			p, ok := s.declFile[curr].imports[x.Name]
			if !ok {
				return "", "", "", false
			}
			return shape, p, b.Sel.Name, true
		case *ast.Ident:
			curr = b.Name
		default:
			return "", "", "", false
		}
	}
}

// TestGenericAggregates_R3bGenericInstantiationForms closes the
// IndexExpr/IndexListExpr bypass: generic instantiations carrying a forbidden
// feature package in their type arguments must be flagged whether written
// directly, through an alias chain, or through a defined-type chain, and
// local names reachable only as type arguments (or only as func params) must
// still be recursed into for nested checks. Neutral instantiations with no
// feature types stay silent.
func TestGenericAggregates_R3bGenericInstantiationForms(t *testing.T) {
	t.Parallel()
	keepwarmImport := `"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"`
	decls := `type Box[T any] struct{ Value T }
type Pair[A, B any] struct {
	First  A
	Second B
}
`
	positive := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "direct single-arg instantiation",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
` + decls + `type DirectGenericAggregate struct {
	Slot Box[*keepwarm.Manager]
}
`},
			target: "DirectGenericAggregate",
		},
		{
			name: "alias single-arg chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
` + decls + `type Wrapped = Box[*keepwarm.Manager]
type WrappedChain = Wrapped
type AliasGenericAggregate struct {
	Direct  Wrapped
	Chained WrappedChain
}
`},
			target: "AliasGenericAggregate",
		},
		{
			name: "defined multi-arg chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
` + decls + `type WrappedDef Pair[string, *keepwarm.Manager]
type WrappedDefChain WrappedDef
type DefinedGenericAggregate struct {
	Slot WrappedDefChain
}
`},
			target: "DefinedGenericAggregate",
		},
		{
			name: "nested instantiation and alias type argument",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
` + decls + `type ManagerPtr = *keepwarm.Manager
type NestedGenericAggregate struct {
	Slot Box[Pair[string, ManagerPtr]]
}
`},
			target: "NestedGenericAggregate",
		},
		{
			name: "local group reachable only as generic type argument",
			sources: map[string]string{"synthetic.go": `package fixture
` + decls + `type InnerGroup struct {
	KeepwarmReplicaCount int
}
type NestOnlyGenericAggregate struct {
	Slot Box[InnerGroup]
}
`},
			target: "NestOnlyGenericAggregate",
		},
		{
			name: "local group reachable only as parenthesized type",
			sources: map[string]string{"synthetic.go": `package fixture
` + decls + `type InnerGroup struct {
	KeepwarmReplicaCount int
}
type NestOnlyParenAggregate struct {
	Grouped (InnerGroup)
}
`},
			target: "NestOnlyParenAggregate",
		},
		{
			name: "local group reachable only as func param",
			sources: map[string]string{"synthetic.go": `package fixture
type FuncGroup struct {
	KeepwarmReplicaCount int
}
type FuncOnlyAggregate struct {
	Handler func(FuncGroup) int
}
`},
			target: "FuncOnlyAggregate",
		},
	}
	for _, tc := range positive {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) == 0 {
				t.Fatalf("expected at least 1 violation for %s, got zero", tc.target)
			}
		})
	}

	neutral := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "neutral instantiations stay silent",
			sources: map[string]string{"synthetic.go": `package fixture
` + decls + `type NeutralGenericAggregate struct {
	Single Box[string]
	Multi  Pair[string, int]
	Nested Box[Pair[string, int]]
}
`},
			target: "NeutralGenericAggregate",
		},
	}
	for _, tc := range neutral {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) != 0 {
				t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
			}
		})
	}
}

// TestGenericAggregates_R3aVariadicParenForms closes the Ellipsis/ParenExpr
// bypass: variadic func aliases/chains and paren-wrapped map values hiding a
// forbidden package must be flagged, in both alias and defined-type forms.
// Neutral variadic/paren shapes with no forbidden package stay silent.
func TestGenericAggregates_R3aVariadicParenForms(t *testing.T) {
	t.Parallel()
	keepwarmImport := `"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"`
	positive := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "alias func variadic chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type Callback = func(...*keepwarm.Manager)
type CallbackChain = Callback
type VariadicAliasAggregate struct {
	Handler CallbackChain
}
`},
			target: "VariadicAliasAggregate",
		},
		{
			name: "defined func variadic chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type CallbackDef func(...*keepwarm.Manager)
type CallbackDefChain CallbackDef
type VariadicDefinedAggregate struct {
	Handler CallbackDefChain
}
`},
			target: "VariadicDefinedAggregate",
		},
		{
			name: "alias paren-wrapped map value chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type ManagerBagParen = map[string](*keepwarm.Manager)
type ManagerBagParenChain = ManagerBagParen
type ParenAliasAggregate struct {
	Managers ManagerBagParenChain
}
`},
			target: "ParenAliasAggregate",
		},
		{
			name: "defined paren-wrapped map value chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type ManagerBagParenDef map[string](*keepwarm.Manager)
type ManagerBagParenDefChain ManagerBagParenDef
type ParenDefinedAggregate struct {
	Managers ManagerBagParenDefChain
}
`},
			target: "ParenDefinedAggregate",
		},
	}
	for _, tc := range positive {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) == 0 {
				t.Fatalf("expected at least 1 violation for %s, got zero", tc.target)
			}
		})
	}

	neutral := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "neutral variadic func",
			sources: map[string]string{"synthetic.go": `package fixture
type NeutralCallback = func(...int)
type NeutralCallbackChain = NeutralCallback
type NeutralVariadicAggregate struct {
	Handler NeutralCallbackChain
}
`},
			target: "NeutralVariadicAggregate",
		},
		{
			name: "neutral paren-wrapped types",
			sources: map[string]string{"synthetic.go": `package fixture
type NeutralBag = map[string](*int)
type NeutralBagChain = NeutralBag
type NeutralParenAggregate struct {
	Managers NeutralBagChain
	Single (int)
}
`},
			target: "NeutralParenAggregate",
		},
	}
	for _, tc := range neutral {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) != 0 {
				t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
			}
		})
	}
}

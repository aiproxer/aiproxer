package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenericAggregatesContainNoPerFeatureFields enforces Requirement 13.4 and Task 10.4:
// generic aggregates (ProcessServices, ProcessServicesInput, executorBuildInput, ExecutorConfig,
// BuildOptions, ProductionOptions, TestingOptions, runtimehost.Generation)
// must contain zero fields named or typed after concrete standard feature IDs or packages,
// except the single StandardFeatures *featurehost.Runtime handle and design-approved consumer ports.
func TestGenericAggregatesContainNoPerFeatureFields(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	type targetStruct struct {
		relFile           string
		structName        string
		allowedExceptions map[string]string
	}

	targets := []targetStruct{
		{
			relFile:    "internal/infra/runtimebundle/process_services_types.go",
			structName: "ProcessServices",
			allowedExceptions: map[string]string{
				"StandardFeatures": "*" + archTestModulePath + "/internal/standardplugins/featurehost.Runtime",
			},
		},
		{
			relFile:           "internal/infra/runtimebundle/process_services_types.go",
			structName:        "ProcessServicesInput",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:    "internal/infra/runtimebundle/build_executor.go",
			structName: "executorBuildInput",
			allowedExceptions: map[string]string{
				"CorePorts": archTestModulePath + "/internal/core/runtime.CorePorts",
			},
		},
		{
			relFile:    "internal/core/runtime/executor_config.go",
			structName: "ExecutorConfig",
			allowedExceptions: map[string]string{
				"Compaction":                 "CompactionRuntime",
				"SecretGuardDecisionMetrics": archTestModulePath + "/internal/core/extensions.SecretGuardDecisionMetrics",
				"ConversationViewObserver":   "ConversationViewObserver",
				"TerminalPolicyReader":       "TerminalPolicyReader",
			},
		},
		{
			relFile:           "internal/infra/runtimebundle/options.go",
			structName:        "BuildOptions",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:           "internal/infra/runtimebundle/production_options.go",
			structName:        "ProductionOptions",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:           "internal/infra/runtimebundle/options.go",
			structName:        "TestingOptions",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:           "internal/infra/runtimehost/generation.go",
			structName:        "Generation",
			allowedExceptions: map[string]string{},
		},
	}

	var violations []string

	scopes := make(map[string]*archPkgScope)
	dirRows := make(map[string]map[string]map[string]string)
	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		dir := filepath.Dir(absPath)
		if _, ok := scopes[dir]; !ok {
			scopes[dir] = archParseDir(t, dir)
		}
		rows, ok := dirRows[dir]
		if !ok {
			rows = make(map[string]map[string]string)
			dirRows[dir] = rows
		}
		rows[tgt.structName] = tgt.allowedExceptions
	}
	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		dir := filepath.Dir(absPath)
		if _, ok := scopes[dir].structs[tgt.structName]; !ok {
			t.Fatalf("generic aggregate target %q (%s) missing from parsed scope: scanner blind spot, refusing to pass", tgt.structName, tgt.relFile)
		}
		structViolations := scopes[dir].scanWithRows(tgt.structName, dirRows[dir])
		violations = append(violations, structViolations...)
	}
	if len(violations) > 0 {
		t.Fatalf("generic aggregates contain forbidden per-feature fields (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestGenericAggregates_NegativeFixtures(t *testing.T) {
	t.Parallel()
	const fixtureSrc = `package fixture
import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

type SyntheticViolationAggregate struct {
	Policy *sessionpolicy.Store
	Manager *keepwarm.Manager
}

type SyntheticEmbeddedAggregate struct {
	*keepwarm.Manager
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "synthetic.go", fixtureSrc, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	violations := scanStructForFeatureFields(node, "SyntheticViolationAggregate", nil)
	if len(violations) < 2 {
		t.Fatalf("expected at least 2 violations for Policy *sessionpolicy.Store and Manager *keepwarm.Manager, got %d:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}

	embeddedViolations := scanStructForFeatureFields(node, "SyntheticEmbeddedAggregate", nil)
	if len(embeddedViolations) < 1 {
		t.Fatalf("expected at least 1 violation for embedded *keepwarm.Manager, got %d", len(embeddedViolations))
	}
}

func TestGenericAggregates_NegativeFixtures_RatchetBypasses(t *testing.T) {
	t.Parallel()
	realSGPlane := "*" + archTestModulePath + "/internal/core/extensions.SecretGuardPlane"
	cases := []struct {
		name       string
		sources    map[string]string
		target     string
		exceptions map[string]string
		wantCount  int // minimum violations when positive, exact zero when negative
	}{
		{
			name: "alias indirection hides forbidden package",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
type ManagerAlias = keepwarm.Manager
type ManagerAliasChain = ManagerAlias
type AliasAggregate struct {
	Mgr ManagerAlias
	MgrChain ManagerAliasChain
}
`},
			target:     "AliasAggregate",
			exceptions: nil,
			wantCount:  2,
		},
		{
			name: "cross-file nested group escapes single-file recursion",
			sources: map[string]string{
				"outer.go": `package fixture
type OuterAggregate struct {
	Inner NestedGroup
}
`,
				"inner.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
type NestedGroup struct {
	Manager *keepwarm.Manager
}
`,
			},
			target:     "OuterAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "inline anonymous struct hides forbidden field name",
			sources: map[string]string{"synthetic.go": `package fixture
type InlineAggregate struct {
	Nested struct {
		SecretGuardDecisionMetrics int
	}
}
`},
			target:     "InlineAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "textual exception match spoofs foreign package",
			sources: map[string]string{"synthetic.go": `package fixture
import extensions "github.com/example/fake/extensions"
type SpoofAggregate struct {
	SecretGuard *extensions.SecretGuardPlane
}
`},
			target:     "SpoofAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  1,
		},
		{
			name: "control: genuine approved reference stays silent",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/core/extensions"
type GenuineAggregate struct {
	SecretGuard *extensions.SecretGuardPlane
}
`},
			target:     "GenuineAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  0,
		},
		{
			name: "control: alias to approved reference stays silent",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/core/extensions"
type PlaneAlias = extensions.SecretGuardPlane
type AliasApprovedAggregate struct {
	SecretGuard *PlaneAlias
}
`},
			target:     "AliasApprovedAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  0,
		},
		{
			name: "alias to map with feature value hides forbidden package",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
type ManagerBag = map[string]*keepwarm.Manager
type ManagerBagChain = ManagerBag
type BagAliasAggregate struct {
	Managers ManagerBagChain
}
`},
			target:     "BagAliasAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "defined type to map with feature value hides forbidden package",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
type ManagerBagDef map[string]*keepwarm.Manager
type ManagerBagDefChain ManagerBagDef
type BagDefinedAggregate struct {
	Managers ManagerBagDefChain
}
`},
			target:     "BagDefinedAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "alias hiding pointer shape mismatches single-pointer exception",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/core/extensions"
type PlanePtrAlias = *extensions.SecretGuardPlane
type DoublePtrAggregate struct {
	SecretGuard *PlanePtrAlias
}
`},
			target:     "DoublePtrAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  1,
		},
		{
			name: "defined type spoofing approved reference is rejected",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/aiproxer/aiproxer/internal/core/extensions"
type GuardDef extensions.SecretGuardPlane
type SpoofDefinedAggregate struct {
	SecretGuard *GuardDef
}
`},
			target:     "SpoofDefinedAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, tc.exceptions)
			if tc.wantCount == 0 {
				if len(got) != 0 {
					t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
				}
				return
			}
			if len(got) < tc.wantCount {
				t.Fatalf("expected at least %d violations for %s, got %d", tc.wantCount, tc.target, len(got))
			}
		})
	}
}

func aggregateFieldTypeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return aggregateFieldTypeToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + aggregateFieldTypeToString(t.X)
	case *ast.ArrayType:
		return "[]" + aggregateFieldTypeToString(t.Elt)
	case *ast.MapType:
		return "map[" + aggregateFieldTypeToString(t.Key) + "]" + aggregateFieldTypeToString(t.Value)
	case *ast.ChanType:
		return "chan " + aggregateFieldTypeToString(t.Value)
	case *ast.Ellipsis:
		return "..." + aggregateFieldTypeToString(t.Elt)
	case *ast.ParenExpr:
		return "(" + aggregateFieldTypeToString(t.X) + ")"
	case *ast.StructType:
		return "struct"
	case *ast.FuncType:
		return "func" + aggregateFieldListToString(t.Params) + aggregateFieldListToString(t.Results)
	case *ast.InterfaceType:
		return "interface"
	case *ast.IndexExpr:
		return aggregateFieldTypeToString(t.X) + "[" + aggregateFieldTypeToString(t.Index) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, 0, len(t.Indices))
		for _, idx := range t.Indices {
			parts = append(parts, aggregateFieldTypeToString(idx))
		}
		return aggregateFieldTypeToString(t.X) + "[" + strings.Join(parts, ", ") + "]"
	default:
		return ""
	}
}

// aggregateFieldListToString renders one func param/result list for the
// textual type label; unhandled shapes render empty but never occur in the
// valid type positions exercised here.
func aggregateFieldListToString(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	parts := make([]string, 0, len(fields.List))
	for _, f := range fields.List {
		parts = append(parts, aggregateFieldTypeToString(f.Type))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// scanNestedInline scans every anonymous *ast.StructType reached through a field
// type's enclosing shapes (Index/IndexList args, ParenExpr, FuncType, Array/Star/
// Chan/Ellipsis/Map, alias/defined chains with cycle protection). Named structs
// met during expansion route back through the caller's deduplicated named
// recursion: recurse shares the named visited set, so directly-referenced
// structs are never reported twice and cycles still terminate. resolveArchLocal
// semantics are unchanged (it still stops at generic instantiations).
func (s *archPkgScope) scanNestedInline(prefix string, e ast.Expr, owner *archPkgFile, visiting map[string]bool, scan func(string, *ast.StructType, *archPkgFile), recurse func(string)) {
	switch t := e.(type) {
	case *ast.StructType:
		scan(prefix, t, owner)
	case *ast.StarExpr:
		s.scanNestedInline(prefix, t.X, owner, visiting, scan, recurse)
	case *ast.ArrayType:
		s.scanNestedInline(prefix, t.Elt, owner, visiting, scan, recurse)
	case *ast.Ellipsis:
		s.scanNestedInline(prefix, t.Elt, owner, visiting, scan, recurse)
	case *ast.ChanType:
		s.scanNestedInline(prefix, t.Value, owner, visiting, scan, recurse)
	case *ast.ParenExpr:
		s.scanNestedInline(prefix, t.X, owner, visiting, scan, recurse)
	case *ast.MapType:
		s.scanNestedInline(prefix, t.Key, owner, visiting, scan, recurse)
		s.scanNestedInline(prefix, t.Value, owner, visiting, scan, recurse)
	case *ast.FuncType:
		if t.Params != nil {
			for _, f := range t.Params.List {
				s.scanNestedInline(prefix, f.Type, owner, visiting, scan, recurse)
			}
		}
		if t.Results != nil {
			for _, f := range t.Results.List {
				s.scanNestedInline(prefix, f.Type, owner, visiting, scan, recurse)
			}
		}
	case *ast.InterfaceType:
		if t.Methods != nil {
			for _, f := range t.Methods.List {
				s.scanNestedInline(prefix, f.Type, owner, visiting, scan, recurse)
			}
		}
	case *ast.IndexExpr:
		s.scanNestedInline(prefix, t.X, owner, visiting, scan, recurse)
		s.scanNestedInline(prefix, t.Index, owner, visiting, scan, recurse)
	case *ast.IndexListExpr:
		s.scanNestedInline(prefix, t.X, owner, visiting, scan, recurse)
		for _, idx := range t.Indices {
			s.scanNestedInline(prefix, idx, owner, visiting, scan, recurse)
		}
	case *ast.Ident:
		if visiting[t.Name] {
			return
		}
		if _, isStruct := s.structs[t.Name]; isStruct {
			recurse(t.Name)
			return
		}
		if under, ok := s.declared[t.Name]; ok {
			visiting[t.Name] = true
			s.scanNestedInline(prefix, under, s.declFile[t.Name], visiting, scan, recurse)
		}
	}
}

// TestGenericAggregates_R3cNestedInlineStructFieldNames closes the nested
// inline-struct bypass: per-feature FIELD NAMES inside anonymous structs nested
// in generic type arguments, func params, or parenthesized shapes must be
// flagged, directly or through alias/defined chains. Neutral structs stay silent.
func TestGenericAggregates_R3cNestedInlineStructFieldNames(t *testing.T) {
	t.Parallel()
	decls := "type Box[T any] struct{ Value T }\ntype Pair[A, B any] struct{ First A\nSecond B }\n"
	src := func(body string) map[string]string {
		return map[string]string{"synthetic.go": "package fixture\n" + body + decls}
	}
	cases := []struct {
		name      string
		body      string
		target    string
		wantCount int // minimum violations when positive, exact zero when neutral
	}{
		{"direct inline struct in generic arg", "type DirectInlineAggregate struct {\n\tSlot Box[struct{ KeepwarmReplicaCount int }]\n}\n", "DirectInlineAggregate", 1},
		{"alias to inline struct in generic arg", "type InlineAlias = struct{ KeepwarmReplicaCount int }\ntype InlineAliasChain = InlineAlias\ntype AliasInlineAggregate struct {\n\tSlot Box[InlineAliasChain]\n}\n", "AliasInlineAggregate", 1},
		{"alias chain to generic instantiation of inline struct", "type WrappedInline = Box[struct{ KeepwarmReplicaCount int }]\ntype WrappedInlineChain = WrappedInline\ntype AliasWrappedAggregate struct {\n\tSlot WrappedInlineChain\n}\n", "AliasWrappedAggregate", 1},
		{"defined chain to generic instantiation of inline struct", "type WrappedInlineDef Box[struct{ KeepwarmReplicaCount int }]\ntype WrappedInlineDefChain WrappedInlineDef\ntype DefinedWrappedAggregate struct {\n\tSlot WrappedInlineDefChain\n}\n", "DefinedWrappedAggregate", 1},
		{"func param inline struct", "type FuncInlineAggregate struct {\n\tHandler func(struct{ KeepwarmReplicaCount int }) int\n}\n", "FuncInlineAggregate", 1},
		{"paren-wrapped inline struct in generic arg", "type ParenInlineAggregate struct {\n\tSlot Box[(struct{ KeepwarmReplicaCount int })]\n}\n", "ParenInlineAggregate", 1},
		{"paren root struct with name+package violations", "import \"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm\"\ntype ParenBothAggregate (struct {\n\tKeepwarmReplicaCount int\n\tSlot *keepwarm.Manager\n})\n", "ParenBothAggregate", 2},
		{"doubly-paren root struct with name-only violation", "type ParenNameOnlyAggregate ((struct {\n\tKeepwarmReplicaCount int\n}))\n", "ParenNameOnlyAggregate", 1},
		{"neutral paren root struct stays silent", "type ParenNeutralAggregate (struct {\n\tCount int\n})\n", "ParenNeutralAggregate", 0},
		// R3d (Phase-10 review): named structs reached only by expanding an
		// alias/defined generic container route through the named scanner.
		{"alias container to generic instantiation of named struct", "type InnerGroup struct{ KeepwarmReplicaCount int }\ntype Wrapped = Box[InnerGroup]\ntype AliasContainerAggregate struct {\n\tSlot Wrapped\n}\n", "AliasContainerAggregate", 1},
		{"defined container to generic instantiation of named struct", "type InnerGroup struct{ KeepwarmReplicaCount int }\ntype WrappedDef Box[InnerGroup]\ntype DefinedContainerAggregate struct {\n\tSlot WrappedDef\n}\n", "DefinedContainerAggregate", 1},
		{"multi-arg alias container of named struct", "type InnerGroup struct{ KeepwarmReplicaCount int }\ntype PairWrapped = Pair[string, InnerGroup]\ntype MultiAliasContainerAggregate struct {\n\tSlot PairWrapped\n}\n", "MultiAliasContainerAggregate", 1},
		{"multi-arg defined container of named struct", "type InnerGroup struct{ KeepwarmReplicaCount int }\ntype PairWrappedDef Pair[string, InnerGroup]\ntype MultiDefinedContainerAggregate struct {\n\tSlot PairWrappedDef\n}\n", "MultiDefinedContainerAggregate", 1},
		{"neutral inline struct in generic arg stays silent", "type NeutralInlineAggregate struct {\n\tSlot Box[struct{ Count int }]\n}\n", "NeutralInlineAggregate", 0},
		{"neutral func param inline struct stays silent", "type NeutralFuncInlineAggregate struct {\n\tHandler func(struct{ Count int }) int\n}\n", "NeutralFuncInlineAggregate", 0},
		{"neutral named struct via generic container stays silent", "type InnerNeutral struct{ Count int }\ntype NeutralContainerAggregate struct {\n\tSlot Box[InnerNeutral]\n}\n", "NeutralContainerAggregate", 0},
		{"neutral named struct via alias container chain stays silent", "type InnerNeutral struct{ Count int }\ntype NeutralWrapped = Box[InnerNeutral]\ntype NeutralWrappedChain = NeutralWrapped\ntype NeutralChainContainerAggregate struct {\n\tSlot NeutralWrappedChain\n}\n", "NeutralChainContainerAggregate", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, src(tc.body))
			got := scope.scan(tc.target, nil)
			if tc.wantCount == 0 {
				if len(got) != 0 {
					t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
				}
				return
			}
			if len(got) < tc.wantCount {
				t.Fatalf("expected at least %d violations for %s, got %d", tc.wantCount, tc.target, len(got))
			}
		})
	}
}

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// featurehostImportNames resolves the local identifiers bound to the
// featurehost package import in f, so aliased imports cannot bypass the
// identifier boundary. A dot import hides every selector and is reported
// separately: it must never appear in generic runtimebundle code.
func featurehostImportNames(f *ast.File) (locals map[string]bool, dotImport bool) {
	locals = map[string]bool{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost" {
			continue
		}
		if imp.Name == nil {
			locals["featurehost"] = true
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImport = true
		case "_":
			// Blank import exposes no identifiers; nothing to track.
		default:
			locals[imp.Name.Name] = true
		}
	}
	return locals, dotImport
}

// scanFeaturehostRefs reports boundary violations in one Go source file:
// forbidden concrete-feature imports, dot imports of the facade package, and
// `featurehost.X` (under any local import name) selectors outside the pinned
// allowlist. Production scanning and negative fixtures share this scanner.
func scanFeaturehostRefs(rel string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, imp := range FileImportPaths(f) {
		for _, forbidden := range forbiddenFeatureImportSubstrings {
			if strings.Contains(imp, forbidden) {
				violations = append(violations, fmt.Sprintf("%s: imports forbidden %s", rel, imp))
			}
		}
	}
	locals, dotImport := featurehostImportNames(f)
	if dotImport {
		violations = append(violations, fmt.Sprintf("%s: dot-import of featurehost hides selectors", rel))
	}
	aliases := runtimeValueAliases(f, locals)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && locals[ident.Name] {
			if !allowedFeaturehostQualifiers[sel.Sel.Name] {
				violations = append(violations, fmt.Sprintf("%s: forbidden featurehost.%s reference", rel, sel.Sel.Name))
			}
			return true
		}
		// Instance-method access on a *featurehost.Runtime value reaches the
		// same facade surface as a package-qualified reference: a concrete
		// feature accessor (KeepwarmPolicy, BoundSecretGuard, ...) bypasses
		// the vocabulary gate above. Only pinned lifecycle/composition ports
		// may be invoked on Runtime values.
		if recv, method, ok := splitRuntimeMethodCall(sel); ok && receiverIsRuntimeValue(recv, aliases) {
			if !allowedRuntimeMethods[method] {
				violations = append(violations, fmt.Sprintf("%s: forbidden Runtime.%s method access", rel, method))
			}
		}
		return true
	})
	return violations, nil
}

// allowedRuntimeMethods pins the exact set of *featurehost.Runtime methods
// generic runtimebundle production code may invoke: facade construction,
// lifecycle, and the grandfathered composition ports. Any new concrete
// feature accessor (KeepwarmPolicy, BoundSecretGuard, TerminalDecisionPolicy,
// BuildSecretGuardRuntime, ...) fails loudly here instead of reopening a
// per-feature channel beside the facade.
var allowedRuntimeMethods = map[string]bool{
	"CompileGeneration":  true,
	"Close":              true,
	"Closed":             true,
	"CompactionDetector": true,
	"ConversationReader": true,
	"ConversationStore":  true,
}

// splitRuntimeMethodCall splits sel into its receiver expression and final
// method name.
func splitRuntimeMethodCall(sel *ast.SelectorExpr) (recv ast.Expr, method string, ok bool) {
	if sel == nil {
		return nil, "", false
	}
	return sel.X, sel.Sel.Name, true
}

// receiverIsRuntimeValue reports whether e denotes a *featurehost.Runtime
// value: a `.StandardFeatures`/`.standardFeatures` selector chain link, an
// explicitly typed alias, or a locally tracked alias thereof.
func receiverIsRuntimeValue(e ast.Expr, aliases map[string]bool) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return aliases[t.Name]
	case *ast.SelectorExpr:
		if t.Sel.Name == "StandardFeatures" || t.Sel.Name == "standardFeatures" {
			return true
		}
		return receiverIsRuntimeValue(t.X, aliases)
	default:
		return false
	}
}

// runtimeValueAliases collects local identifiers denoting a
// *featurehost.Runtime value: parameters explicitly typed as
// featurehost.Runtime (under any local import name), variables assigned
// directly from a `.StandardFeatures`/`.standardFeatures` chain, and
// transitive copies thereof. Composite literals and call results are
// deliberately NOT tracked: merely mentioning a Runtime value does not make
// the assigned variable one (that imprecision aliased execRun-style structs
// and produced false positives).
func runtimeValueAliases(f *ast.File, locals map[string]bool) map[string]bool {
	aliases := map[string]bool{}
	featurehostRuntime := func(e ast.Expr) bool {
		if e == nil {
			return false
		}
		if sel, ok := e.(*ast.StarExpr); ok {
			e = sel.X
		}
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		ident, ok := s.X.(*ast.Ident)
		return ok && locals[ident.Name] && s.Sel.Name == "Runtime"
	}
	// isRuntimeChain reports whether e IS a Runtime-denoting chain: a
	// `.StandardFeatures`/`.standardFeatures` selector, an explicitly typed
	// reference, or a previously tracked alias (transitive copies).
	isRuntimeChain := func(e ast.Expr) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return aliases[t.Name]
		case *ast.SelectorExpr:
			if t.Sel.Name == "StandardFeatures" || t.Sel.Name == "standardFeatures" {
				return true
			}
			return false
		default:
			return featurehostRuntime(e)
		}
	}
	trackFieldList := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			if featurehostRuntime(field.Type) {
				for _, name := range field.Names {
					aliases[name.Name] = true
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.FuncDecl:
			trackFieldList(t.Type.Params)
		case *ast.FuncLit:
			trackFieldList(t.Type.Params)
		case *ast.AssignStmt:
			// Multi-assign pairs positionally; single RHS fans out only to
			// a direct chain (not to composite literals or call results).
			for i, lhs := range t.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name == "_" {
					continue
				}
				if len(t.Rhs) == 1 {
					if isRuntimeChain(t.Rhs[0]) {
						aliases[ident.Name] = true
					}
				} else if i < len(t.Rhs) && isRuntimeChain(t.Rhs[i]) {
					aliases[ident.Name] = true
				}
			}
		case *ast.ValueSpec:
			if featurehostRuntime(t.Type) {
				for _, name := range t.Names {
					aliases[name.Name] = true
				}
				break
			}
			if len(t.Values) == 1 {
				if isRuntimeChain(t.Values[0]) {
					for _, name := range t.Names {
						aliases[name.Name] = true
					}
				}
			} else {
				for i, name := range t.Names {
					if i < len(t.Values) && isRuntimeChain(t.Values[i]) {
						aliases[name.Name] = true
					}
				}
			}
		}
		return true
	})
	return aliases
}

// allowedFeaturehostQualifiers pins the exact set of `featurehost.X`
// package-qualified identifiers generic runtimebundle production code may
// reference. Concrete feature knowledge routed through featurehost (concrete
// admin services, policy projections, per-feature planes) must never appear
// here: reaching it would bypass the runtimebundle concrete-feature import
// rule through the back door.
//
// InventoryMetadataFromMergeSurface and InventoryMetadataForConfig are the
// compiled-inventory read-model authority added by the inventory-featurehost
// relocation: featurehost reduces the compiled merge surface's diagnostic
// projections into finished diag rows and associates the composed secret-guard
// posture with its configured instance. Runtimebundle only captures and passes
// those finished rows (no feature internals, no reduction), so the two names
// join the vocabulary as the deliberate facade capability for that seam.
var allowedFeaturehostQualifiers = map[string]bool{
	"Runtime":                           true,
	"NewProcess":                        true,
	"ProcessInput":                      true,
	"HostEnvironment":                   true,
	"CompactionSchedulerBounds":         true,
	"GenerationInput":                   true,
	"GenerationOutput":                  true,
	"CorePorts":                         true,
	"HostProjections":                   true,
	"MetricsRegistry":                   true,
	"Registration":                      true,
	"ValidateSecretGuardRegistrations":  true,
	"InventoryMetadataFromMergeSurface": true,
	"InventoryMetadataForConfig":        true,
}

// forbiddenFeatureImportSubstrings pins concrete-feature package path
// vocabulary that must never be imported by runtimebundle production code.
// Standard-distribution composition (featurehost itself), core contracts, and
// stdhttp admin contracts are not in this set.
var forbiddenFeatureImportSubstrings = []string{
	"/internal/plugins/features/",
	"/pkg/aipsdk/secretguardhost",
	"internal/standardplugins/featurehost/secretguard",
	"internal/standardplugins/featurehost/sessionpolicy",
	"internal/standardplugins/featurehost/compaction",
	"internal/standardplugins/featurehost/reasoning",
	"internal/infra/secretguardcompose",
	"internal/infra/reasoningcompose",
	"internal/infra/compactioncompose",
}

// TestRuntimeBundleFeatureIdentifierBoundary asserts that generic
// runtimebundle production code reaches the standard distribution only
// through the pinned facade allowlist and imports zero concrete-feature
// packages. See allowedFeaturehostQualifiers for the closed vocabulary.
func TestRuntimeBundleFeatureIdentifierBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/infra/runtimebundle") {
			return nil
		}
		got, err := scanFeaturehostRefs(rel, src)
		if err != nil {
			return err
		}
		violations = append(violations, got...)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("runtimebundle reaches concrete feature knowledge through featurehost (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestRuntimeBundleFeatureIdentifierBoundary_NegativeFixtures proves the
// boundary scanner is not vacuous: concrete feature identifiers routed
// through featurehost-qualified access are flagged, while pinned facade
// references stay silent.
func TestRuntimeBundleFeatureIdentifierBoundary_NegativeFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		src       string
		wantCount int
	}{
		{
			name: "concrete admin service through featurehost is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
var _ = featurehost.TerminalDecisionPolicyHTTPProjection
`,
			wantCount: 1,
		},
		{
			name: "concrete feature package import is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
var _ = keepwarm.Config{}
`,
			wantCount: 1,
		},
		{
			name: "dedicated featurehost subpackage import is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
var _ = sessionpolicy.Store{}
`,
			wantCount: 1,
		},
		{
			name: "pinned facade references stay silent",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
var _ featurehost.GenerationOutput
var _ featurehost.CorePorts
var _ featurehost.ProcessInput
`,
			wantCount: 0,
		},
		{
			name: "aliased featurehost import is still flagged",
			src: `package runtimebundle
import fh "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
var _ = fh.TerminalDecisionPolicyHTTPProjection
`,
			wantCount: 1,
		},
		{
			name: "dot import of featurehost is flagged",
			src: `package runtimebundle
import . "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
var _ = GenerationOutput{}
`,
			wantCount: 1,
		},
		{
			name: "aliased pinned facade references stay silent",
			src: `package runtimebundle
import fh "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
var _ fh.GenerationOutput
var _ fh.CorePorts
`,
			wantCount: 0,
		},
		{
			name: "concrete feature access through Runtime value is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
func bypass(r *featurehost.Runtime) {
	_ = r.KeepwarmPolicy()
	_ = r.BoundSecretGuard()
}
`,
			wantCount: 2,
		},
		{
			name: "concrete feature access through StandardFeatures chain is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
func bypass(ps *ProcessServices) {
	_ = ps.StandardFeatures.TerminalDecisionPolicy()
}
`,
			wantCount: 1,
		},
		{
			name: "concrete feature access through tracked alias is flagged",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
func bypass(ps *ProcessServices) {
	sf := ps.StandardFeatures
	_ = sf.KeepwarmPolicy()
}
`,
			wantCount: 1,
		},
		{
			name: "pinned Runtime lifecycle methods stay silent",
			src: `package runtimebundle
import "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
func ok(r *featurehost.Runtime) {
	_ = r.Close
	_ = r.CompileGeneration
}
`,
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scanFeaturehostRefs("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("scanFeaturehostRefs: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d violations %v, want %d", len(got), got, tc.wantCount)
			}
		})
	}
}

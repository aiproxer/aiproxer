package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Pinned facade shapes shared by the production conformance test and the
// negative fixtures below, so the two cannot drift apart.
var wantGenerationOutputShape = []string{
	"Bundle:aipfeature.FeatureBundle",
	"Planes:aipfeature.FrozenPlaneSet",
	"Lifecycles:[]aipplugin.Lifecycle",
	"CorePorts:CorePorts",
	"HostProjections:HostProjections",
}

var wantGenerationInputShape = []string{
	"Registrations:[]aipsdk.Registration",
	"HostRegistrations:[]sdkfeaturehost.Registration",
	"MergeSurface:featurebundle.GeneratedMergeSurface",
	"Planes:aipfeature.FrozenPlaneSet",
	"Lifecycles:[]aipplugin.Lifecycle",
	"CandidatePlanes:aipfeature.FrozenPlaneSet",
	"BackgroundClient:auxiliary.BackgroundClient",
	"BackgroundPoller:auxiliary.BackgroundPoller",
	// The generation-bound target compiler the proxy-memory feature consumes
	// to compile a phase target into the final compiled actual target
	// (design §1). The composition root binds it from the generation's
	// executor; the feature owns the port it speaks.
	"ProxyMemoryTargetCompiler:proxymemory.TargetCompiler",
	"ReasoningProdOpts:ReasoningCompressionOptions",
	"ReasoningTestOpts:ReasoningCompressionOptions",
	"InterleavedConfig:interleavedthinking.Config",
	"ConfigInterleaved:config.InterleavedConfig",
	"KeepwarmConfig:keepwarm.Config",
	"NowFn:func()(time.Time)",
	"KeepwarmAccounting:billingport.ProviderMaintenanceUsageObserver",
	"ConfigDir:string",
	"AccessMode:accessmode.Mode",
	"SecretEnv:SecretGuardEnvironment",
	"SecretInputs:SecretGuardInputs",
	"DecisionObserver:SecretDecisionObserver",
	"FaultInject:error",
	// Existing bounded store observer forwarded to generation producers, not a
	// feature-specific input or new SDK plane (design: Error Handling and Observability).
	"ConversationObserver:conversationview.Observer",
}

// TestGenerationFacadeShape pins the Task 2.2 conformance of the
// standard-distribution generation facade against the shared want-shape
// variables above. New featurehost output or input fields fail here instead
// of leaking back into generic runtimebundle.
func TestGenerationFacadeShape(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	absPath := filepath.Join(root, filepath.FromSlash("internal/standardplugins/featurehost/inputs.go"))

	wantOutput := wantGenerationOutputShape
	wantInput := wantGenerationInputShape

	fieldsOf := func(structName string) []string {
		t.Helper()
		got, err := scanFacadeStructFields(absPath, structName)
		if err != nil {
			t.Fatalf("scanFacadeStructFields: %v", err)
		}
		return got
	}

	gotOutput := fieldsOf("GenerationOutput")
	if !slices.Equal(gotOutput, wantOutput) {
		t.Fatalf("GenerationOutput facade shape drift:\n got=%v\nwant=%v", gotOutput, wantOutput)
	}
	gotInput := fieldsOf("GenerationInput")
	if !slices.Equal(gotInput, wantInput) {
		t.Fatalf("GenerationInput facade shape drift:\n got=%v\nwant=%v", gotInput, wantInput)
	}
}

// archTypeString renders a type expression deterministically for facade-shape
// comparison. Unknown composite forms render as "complex", which never matches
// the allowlist: new shapes fail closed instead of slipping through.
func archTypeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return archTypeString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + archTypeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + archTypeString(t.Elt)
		}
		return "[n]" + archTypeString(t.Elt)
	case *ast.FuncType:
		var params []string
		if t.Params != nil {
			for _, p := range t.Params.List {
				n := len(p.Names)
				if n == 0 {
					n = 1
				}
				for i := 0; i < n; i++ {
					params = append(params, archTypeString(p.Type))
				}
			}
		}
		s := "func(" + strings.Join(params, ",") + ")"
		if t.Results != nil && len(t.Results.List) > 0 {
			var results []string
			for _, r := range t.Results.List {
				n := len(r.Names)
				if n == 0 {
					n = 1
				}
				for i := 0; i < n; i++ {
					results = append(results, archTypeString(r.Type))
				}
			}
			s += "(" + strings.Join(results, ",") + ")"
		}
		return s
	case *ast.ParenExpr:
		return archTypeString(t.X)
	default:
		return "complex"
	}
}

// scanFacadeStructFields returns "Name:Type" entries for a struct declared in
// the file at absPath. Embedded fields (no names) are recorded as
// "embedded:Type" so they can never silently match a named allowlist entry.
func scanFacadeStructFields(absPath, structName string) ([]string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, absPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("ParseFile(%s): %w", absPath, err)
	}
	for _, decl := range node.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != structName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return nil, fmt.Errorf("%s is not a struct", structName)
			}
			var out []string
			for _, f := range st.Fields.List {
				typ := archTypeString(f.Type)
				if len(f.Names) == 0 {
					out = append(out, "embedded:"+typ)
					continue
				}
				for _, name := range f.Names {
					out = append(out, name.Name+":"+typ)
				}
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("struct %s not found in %s: scanner blind spot, refusing to pass", structName, absPath)
}

// TestFacadeShapeScanner_NegativeFixtures proves the production shape
// scanner rejects embedded fields and type drift that a name-only check
// would miss. Each case writes a synthetic inputs.go through the SAME
// scanFacadeStructFields + want-list comparison the production test uses.
func TestFacadeShapeScanner_NegativeFixtures(t *testing.T) {
	t.Parallel()
	fullOutput := `package featurehost
type GenerationOutput struct {
	Bundle     aipfeature.FeatureBundle
	Planes     aipfeature.FrozenPlaneSet
	Lifecycles []aipplugin.Lifecycle
	CorePorts  CorePorts
 HostProjections HostProjections
`
	cases := []struct {
		name       string
		structName string
		src        string
		want       []string
		silent     bool
	}{
		{
			name:       "embedded field is flagged",
			structName: "GenerationOutput",
			src: fullOutput + `	embeddedPort
}
`,
			silent: false,
		},
		{
			name:       "field type drift is flagged",
			structName: "GenerationOutput",
			src:        strings.Replace(fullOutput, "Bundle     aipfeature.FeatureBundle", "Bundle string", 1) + "}\n",
			silent:     false,
		},
		{
			name:       "exact typed shape stays silent",
			structName: "GenerationOutput",
			src: fullOutput + `}
`,
			silent: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "inputs.go")
			if err := os.WriteFile(path, []byte(tc.src), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got, err := scanFacadeStructFields(path, tc.structName)
			if err != nil {
				t.Fatalf("scanFacadeStructFields: %v", err)
			}
			var want []string
			switch tc.structName {
			case "GenerationOutput":
				want = wantGenerationOutputShape
			default:
				t.Fatalf("unknown struct %q: fixture must reference a pinned shape", tc.structName)
			}
			if tc.silent && !slices.Equal(got, want) {
				t.Fatalf("shape drift:\n got=%v\nwant=%v", got, want)
			}
			if !tc.silent && slices.Equal(got, want) {
				t.Fatalf("bypass not flagged: got production shape %v", got)
			}
		})
	}
}

// wantFacadeNamedTypes pins the underlying shapes of featurehost-local named
// types referenced by the facade contract. A field typed
// TerminalPolicyProjectionFunc stays silent in the shape test even if the
// underlying signature changes — this test closes that drift channel. Only
// deliberate edits here (with design justification) may change these entries.
var wantFacadeNamedTypes = map[string]string{
	"TerminalPolicyProjectionFunc": "func(snapshot *extensions.RequestRuntimeSnapshot, headers aipsdk.HTTPHeaders, maxBodyBytes int64, store ssessionapp.Store) httpcontract.TerminalDecisionPolicyInput",
	"SecretGuardEnvironment":       "secretguard.Environment",
	"SecretGuardInputs":            "secretguard.SecretGuardInputs",
	"SecretDecisionObserver":       "sdk.Observer",
	"HostEnvironment":              "interface { Lookup(name string) (value string, ok bool) Snapshot() []string }",
}

// TestFacadeNamedTypesStable pins the underlying declarations behind the
// named types in the facade contract. Changing a target (e.g. widening the
// projection factory signature or the host environment capability) fails
// loudly here even though field-level shape text is unchanged.
func TestFacadeNamedTypesStable(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, filepath.FromSlash("internal/standardplugins/featurehost"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	found := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", entry.Name(), err)
		}
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
				if _, wanted := wantFacadeNamedTypes[ts.Name.Name]; !wanted {
					continue
				}
				var buf strings.Builder
				if err := printer.Fprint(&buf, fset, ts.Type); err != nil {
					t.Fatalf("print %s: %v", ts.Name.Name, err)
				}
				normalized := strings.Join(strings.Fields(buf.String()), " ")
				if prev, dup := found[ts.Name.Name]; dup && prev != normalized {
					t.Fatalf("duplicate conflicting declarations of %s", ts.Name.Name)
				}
				found[ts.Name.Name] = normalized
			}
		}
	}
	for name, want := range wantFacadeNamedTypes {
		got, ok := found[name]
		if !ok {
			t.Errorf("named facade type %s not found: scanner blind spot, refusing to pass", name)
			continue
		}
		if got != want {
			t.Errorf("named facade type %s drift:\n got=%s\nwant=%s", name, got, want)
		}
	}
}

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTask81TerminalDecisionRuntimeIsProviderNeutral is the blocker ratchet for
// the platform migration. Runtime orchestration may consume the generic
// terminal-decision contract, but it must not name or import the concrete ALG
// implementation (or the old stop gate). The current RED state intentionally
// reports the first remaining coupling.
func TestTask81TerminalDecisionRuntimeIsProviderNeutral(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	forbiddenImports := []string{
		"/internal/core/stopgate",
		"/internal/core/stopguard",
		"/internal/core/continuationsafety",
		"/internal/core/stopguardverify",
	}
	forbiddenNames := map[string]struct{}{
		"AgentLoopGuard":     {},
		"LoopGuard":          {},
		"agentLoopGuard":     {},
		"continuationsafety": {},
		"guardHidden":        {},
		"isLoopGuardEnabled": {},
		"loopGuard":          {},
		"stopgate":           {},
		"stopguard":          {},
	}

	var offender string
	err := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		if !strings.HasPrefix(rel, "internal/core/runtime/") || offender != "" {
			return nil
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(file) {
			for _, forbidden := range forbiddenImports {
				if strings.Contains(imp, forbidden) {
					offender = rel + ": provider-specific import " + imp
					return nil
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, forbidden := forbiddenNames[ident.Name]; forbidden {
				offender = rel + ": provider-specific identifier " + ident.Name
				return false
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan runtime production: %v", err)
	}
	if offender != "" {
		t.Fatalf("runtime core is not provider-neutral: %s", offender)
	}
}

// TestTask81FeatureBundleHasOneTerminalDecisionProvider ratchets the
// provider contribution shape: no hand-authored provider field on FeatureBundle,
// with exclusivity handled by PlaneTerminalDecisionProvider on PlaneSet.
func TestTask81FeatureBundleHasOneTerminalDecisionProvider(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "pkg", "aipsdk", "feature", "bundle.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse FeatureBundle: %v", err)
	}

	var bundle *ast.StructType
	ast.Inspect(file, func(node ast.Node) bool {
		ts, ok := node.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "FeatureBundle" {
			return true
		}
		bundle, _ = ts.Type.(*ast.StructType)
		return false
	})
	if bundle == nil {
		t.Fatal("FeatureBundle struct not found")
	}

	for _, field := range bundle.Fields.List {
		for _, name := range field.Names {
			if name.Name == "TerminalDecisionProviders" || name.Name == "TerminalDecisionProvider" {
				t.Fatalf("FeatureBundle must not expose named provider field %q", name.Name)
			}
		}
	}
}

// TestTask81ContinuationDoesNotOwnHiddenContent checks only the narrow
// platform writer seam. The deterministic lifecycle and race matrices remain
// in internal/core/runtime; this ratchet prevents their invariant from being
// bypassed by a direct canonical-call append or the removed guardHidden path.
func TestTask81ContinuationDoesNotOwnHiddenContent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "terminal_decision_continuation.go")
	src := string(readTask81File(t, path))
	if !strings.Contains(src, "steeringWriterFactory") {
		t.Fatalf("continuation transaction must use the canonical SDK writer: %s", path)
	}
	for _, forbidden := range []string{
		"Call.Messages = append",
		"Call.Items = append",
		"Messages = append",
		"Items = append",
		"guardHidden",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("continuation transaction contains forbidden hidden-content path %q", forbidden)
		}
	}
}

// TestTask81TerminalDecisionPolicyStoreIsProcessOwned ensures a policy store
// is created once by featurehost process construction and is not owned directly
// by ProcessServices or constructed by generation compilation.
func TestTask81TerminalDecisionPolicyStoreIsProcessOwned(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	typesPath := filepath.Join(root, "internal", "infra", "runtimebundle", "process_services_types.go")
	servicesPath := filepath.Join(root, "internal", "infra", "runtimebundle", "process_services.go")
	featurehostProcPath := filepath.Join(root, "internal", "standardplugins", "featurehost", "process.go")
	typesSrc := string(readTask81File(t, typesPath))
	servicesSrc := string(readTask81File(t, servicesPath))
	fhProcSrc := string(readTask81File(t, featurehostProcPath))

	if strings.Contains(typesSrc, "TerminalDecisionPolicy *terminaldecisionpolicy.Store") ||
		strings.Contains(typesSrc, "terminaldecisionpolicy") ||
		strings.Contains(typesSrc, "sessionpolicy.Store") {
		t.Fatalf("ProcessServices must not own a concrete terminal decision policy store field")
	}
	if !strings.Contains(typesSrc, "StandardFeatures *featurehost.Runtime") {
		t.Fatalf("ProcessServices must own StandardFeatures *featurehost.Runtime")
	}
	if strings.Contains(servicesSrc, "terminaldecisionpolicy.NewStore(") ||
		strings.Contains(servicesSrc, "sessionpolicy.NewStore(") {
		t.Fatalf("NewProcessServices must not construct the policy store directly")
	}
	if !strings.Contains(fhProcSrc, "sessionpolicy.NewStore") && !strings.Contains(fhProcSrc, "newSessionPolicyStore") {
		t.Fatalf("featurehost process construction must construct sessionpolicy store")
	}

	err := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		if strings.HasPrefix(rel, "internal/standardplugins/featurehost/sessionpolicy/") {
			return nil
		}
		if rel == "internal/standardplugins/featurehost/process.go" {
			return nil
		}
		if strings.Contains(string(src), "sessionpolicy.NewStore(") || strings.Contains(string(src), "terminaldecisionpolicy.NewStore(") {
			t.Fatalf("non-process file %s constructs a policy store", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan policy store ownership: %v", err)
	}
}

// TestTask81TerminalDecisionPolicyLookupStopsAtAdmission prevents mutable
// policy reads from reappearing in terminal/stream processing. The one
// allowed lookup is the immutable snapshot taken at request admission.
func TestTask81TerminalDecisionPolicyLookupStopsAtAdmission(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	admissionPath := "internal/core/runtime/terminal_decision_policy_admission.go"
	prepPath := filepath.Join(root, "internal", "core", "runtime", "executor_prepare_request.go")
	admissionSrc := string(readTask81File(t, filepath.Join(root, filepath.FromSlash(admissionPath))))
	prepSrc := string(readTask81File(t, prepPath))
	if !strings.Contains(admissionSrc, "TerminalPolicyReader.Effective(") {
		t.Fatal("request admission must invoke TerminalPolicyReader.Effective")
	}
	if !strings.Contains(prepSrc, "snapshotTerminalDecisionPolicy(") {
		t.Fatal("request preparation must invoke the admission policy snapshot seam")
	}

	err := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		if !strings.HasPrefix(rel, "internal/core/runtime/") || rel == admissionPath {
			return nil
		}
		text := string(src)
		for _, lookup := range []string{
			"TerminalPolicyReader.Effective(",
			"TerminalDecisionPolicy.Snapshot(",
			"TerminalDecisionPolicy.Set(",
			"TerminalDecisionPolicy.Delete(",
			"terminalDecisionPolicy.Snapshot(",
		} {
			if strings.Contains(text, lookup) {
				t.Fatalf("runtime hot path %s performs mutable policy lookup %q", rel, lookup)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan runtime policy lookups: %v", err)
	}
}

// TestTask81DiagnosticsHaveBoundedDimensions keeps the platform diagnostic
// projection content-free: dimensions are counts/revisions only, never raw
// prompt, output, verifier, or identity values.
func TestTask81DiagnosticsHaveBoundedDimensions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "conversationprojection", "snapshot.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse diagnostic observer: %v", err)
	}

	wantFields := map[string]string{
		"StateRevision":      "uint64",
		"FilteredCount":      "int",
		"InjectedCount":      "int",
		"StablePrefixCount":  "int",
		"AfterMessageCount":  "int",
		"FallbackCount":      "int",
		"MaxOverlayRevision": "uint64",
		"MaxSlotOrdinal":     "uint64",
	}
	var summary *ast.StructType
	ast.Inspect(file, func(node ast.Node) bool {
		ts, ok := node.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "ProjectionSummary" {
			return true
		}
		summary, _ = ts.Type.(*ast.StructType)
		return false
	})
	if summary == nil {
		t.Fatal("bounded ProjectionSummary type not found")
	}
	if len(summary.Fields.List) != len(wantFields) {
		t.Fatalf("ProjectionSummary has %d fields, want exactly %d bounded dimensions", len(summary.Fields.List), len(wantFields))
	}
	for _, field := range summary.Fields.List {
		if len(field.Names) != 1 {
			t.Fatalf("ProjectionSummary contains an unnamed or multi-name field")
		}
		name := field.Names[0].Name
		wantType, ok := wantFields[name]
		if !ok {
			t.Fatalf("ProjectionSummary exposes unbounded diagnostic dimension %q", name)
		}
		typ, ok := field.Type.(*ast.Ident)
		if !ok || typ.Name != wantType {
			t.Fatalf("ProjectionSummary.%s type = %T, want bounded %s", name, field.Type, wantType)
		}
	}

	wantStages := map[string]string{
		"StageEarly":      "early",
		"StageFinal":      "final",
		"StageSDKResolve": "sdk_resolve",
	}
	stages := constStringValues(file)
	for name, want := range wantStages {
		if stages[name] != want {
			t.Fatalf("diagnostic stage %s = %q, want bounded value %q", name, stages[name], want)
		}
	}
}

// TestTask81ContinuationOrderingUsesDeterministicFixture deliberately checks
// for the existing schedule rather than rebuilding its synchronization matrix.
func TestTask81ContinuationOrderingUsesDeterministicFixture(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "terminal_decision_continuation_order_red_test.go")
	src := string(readTask81File(t, path))
	for _, marker := range []string{
		"schedulekit.B2PublishedSettlementSchedule()",
		"TestContinuationTransactionPublishesB2BeforeB1Settlement",
		"settleCalls.Load() != 1",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("deterministic continuation fixture is missing %q", marker)
		}
	}
}

func readTask81File(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.ToSlash(path), err)
	}
	return src
}

func constStringValues(file *ast.File) map[string]string {
	values := make(map[string]string)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Values) != 1 || len(valueSpec.Names) != 1 {
				continue
			}
			literal, ok := valueSpec.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil {
				values[valueSpec.Names[0].Name] = value
			}
		}
	}
	return values
}

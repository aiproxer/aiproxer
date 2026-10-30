package archtest

// This file is the Phase-A ownership evidence for the turn/recv simplification.
// It intentionally contains no production hooks: the scanner reads the current
// production AST and compares it with the checked-in baseline artifact. The
// final-topology test is a RED ratchet until the migration has introduced the
// owners described by the spec design.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const turnRecvOwnershipBaselineRelPath = "internal/archtest/testdata/turn_recv_ownership_baseline.json"

const turnRecvOwnershipSchemaVersion = 1

const runtimeModulePrefix = "github.com/aiproxer/aiproxer/"

var turnRecvOwnershipCategories = []string{
	"immutable_request_fact",
	"current_attempt_state",
	"recovery_routing_state",
	"response_pipeline_state",
	"request_terminal_state",
	"infrastructure_compatibility_state",
}

type turnRecvField struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Category  string   `json:"category"`
	SyncTypes []string `json:"sync_types,omitempty"`
}

type turnRecvSyncPrimitive struct {
	Field string `json:"field"`
	Type  string `json:"type"`
}

type turnRecvMethod struct {
	Name            string `json:"name"`
	File            string `json:"file"`
	Line            int    `json:"line"`
	Responsibility  string `json:"responsibility"`
	ReachesExecutor bool   `json:"reaches_executor"`
}

type turnRecvDomainFanout struct {
	Package string   `json:"package"`
	Methods []string `json:"methods"`
}

type turnRecvStateCopy struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`
	Left     string `json:"left"`
	Right    string `json:"right"`
}

type turnRecvMirror struct {
	Fact        string   `json:"fact"`
	Authorities []string `json:"authorities"`
	Evidence    []string `json:"evidence"`
}

type turnRecvExecutorReachability struct {
	FieldPresent        bool     `json:"field_present"`
	MethodCount         int      `json:"method_count"`
	Methods             []string `json:"methods"`
	BroadFieldForbidden bool     `json:"broad_field_forbidden"`
}

type turnRecvCurrentInventory struct {
	FieldCount               int                          `json:"field_count"`
	Fields                   []turnRecvField              `json:"fields"`
	CategoryCounts           map[string]int               `json:"category_counts"`
	SyncPrimitiveCount       int                          `json:"sync_primitive_count"`
	SyncPrimitives           []turnRecvSyncPrimitive      `json:"sync_primitives"`
	MethodCount              int                          `json:"method_count"`
	Methods                  []turnRecvMethod             `json:"methods"`
	ResponsibilityCounts     map[string]int               `json:"responsibility_counts"`
	CrossDomainMethodCount   int                          `json:"cross_domain_method_count"`
	DomainPackageFanoutCount int                          `json:"domain_package_fanout_count"`
	DomainPackageFanout      []turnRecvDomainFanout       `json:"domain_package_fanout"`
	ExecutorReachability     turnRecvExecutorReachability `json:"executor_reachability"`
	StateCopyAssignmentCount int                          `json:"state_copy_assignment_count"`
	StateCopyAssignments     []turnRecvStateCopy          `json:"state_copy_assignments"`
	MirroredFacts            []turnRecvMirror             `json:"mirrored_facts"`
}

type turnRecvTargetTopology struct {
	OwnerNames                        []string `json:"owner_names"`
	FacadeMaxDirectFields             int      `json:"facade_max_direct_fields"`
	FacadeMaxSyncPrimitives           int      `json:"facade_max_sync_primitives"`
	FacadeMaxReceiverMethods          int      `json:"facade_max_receiver_methods"`
	FacadeMaxCrossDomainMethods       int      `json:"facade_max_cross_domain_methods"`
	FacadeMaxDomainPackageFanout      int      `json:"facade_max_domain_package_fanout"`
	FacadeMaxExecutorReachableMethods int      `json:"facade_max_executor_reachable_methods"`
	FacadeMaxStateCopyAssignments     int      `json:"facade_max_state_copy_assignments"`
	ForbiddenDirectFields             []string `json:"forbidden_direct_fields"`
	ExactlyOneAuthorityFacts          []string `json:"exactly_one_authority_facts"`
}

type turnRecvOwnershipBaseline struct {
	SchemaVersion int                      `json:"schema_version"`
	Feature       string                   `json:"feature"`
	Phase         string                   `json:"phase"`
	Source        string                   `json:"source"`
	Current       turnRecvCurrentInventory `json:"current"`
	Target        turnRecvTargetTopology   `json:"target"`
}

type turnRecvASTFile struct {
	RelPath string
	AST     *ast.File
	FSet    *token.FileSet
	Imports map[string]string
}

func TestTurnRecvOwnershipBaselineFileExistsAndSchema(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	if baseline.SchemaVersion != turnRecvOwnershipSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", baseline.SchemaVersion, turnRecvOwnershipSchemaVersion)
	}
	if baseline.Feature != "turn-recv-terminal-ownership-simplification" {
		t.Fatalf("feature = %q, want turn-recv-terminal-ownership-simplification", baseline.Feature)
	}
	if baseline.Phase != "1.1-current-baseline" {
		t.Fatalf("phase = %q, want 1.1-current-baseline", baseline.Phase)
	}
	if len(baseline.Current.Fields) == 0 || len(baseline.Current.Methods) == 0 {
		t.Fatal("baseline must contain the direct field and receiver-method inventory")
	}
	if len(baseline.Current.MirroredFacts) != 4 {
		t.Fatalf("mirrored_facts = %d, want four commitment/finished/attempt-terminal/request-context facts", len(baseline.Current.MirroredFacts))
	}
	for _, category := range turnRecvOwnershipCategories {
		if _, ok := baseline.Current.CategoryCounts[category]; !ok {
			t.Fatalf("category_counts missing accepted category %q", category)
		}
	}
	if len(baseline.Target.OwnerNames) != 5 {
		t.Fatalf("target owner_names = %d, want five cohesive owners", len(baseline.Target.OwnerNames))
	}
}

func TestTurnRecvOwnershipBaselineMatchesCurrentAST(t *testing.T) {
	if os.Getenv("CHECK_TURN_RECV_BASELINE") != "1" {
		t.Skip("set CHECK_TURN_RECV_BASELINE=1 to exercise the exact Phase-A AST baseline ratchet")
	}
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	if !sameJSON(current, baseline.Current) {
		t.Fatalf("checked-in current baseline does not match deterministic AST scan; run with GENERATE_TURN_RECV_BASELINE=1 to refresh the Phase-A artifact")
	}
}

// TestTurnRecvOwnershipFinalTopology is the always-on architecture ratchet for
// the final façade over facts, attempt, recovery, response, and terminal owners.
func TestTurnRecvOwnershipFinalTopology(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load turn/recv ownership baseline: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	findings := evaluateTurnRecvTarget(current, baseline.Target)
	if len(findings) != 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: final EventStream topology is not established:\n%s", strings.Join(findings, "\n"))
	}
}

func TestGenerateTurnRecvOwnershipBaseline(t *testing.T) {
	generate := os.Getenv("GENERATE_TURN_RECV_BASELINE")
	// Setenv marks this process-global fixture mutation as intentionally serial
	// while preserving the opt-in generation behavior.
	t.Setenv("GENERATE_TURN_RECV_BASELINE", generate)
	if generate != "1" {
		t.Skip("set GENERATE_TURN_RECV_BASELINE=1 to regenerate the deterministic Phase-A artifact")
	}
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(turnRecvOwnershipBaselineRelPath))
	baseline, err := loadTurnRecvOwnershipBaseline(root)
	if err != nil {
		t.Fatalf("load target topology before generation: %v", err)
	}
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan current retryRecvStream ownership: %v", err)
	}
	baseline.Current = current
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create baseline directory: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	t.Logf("wrote %s", path)
}

func loadTurnRecvOwnershipBaseline(root string) (turnRecvOwnershipBaseline, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(turnRecvOwnershipBaselineRelPath)))
	if err != nil {
		return turnRecvOwnershipBaseline{}, err
	}
	var baseline turnRecvOwnershipBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return turnRecvOwnershipBaseline{}, err
	}
	return baseline, nil
}

func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func scanTurnRecvOwnership(root string) (turnRecvCurrentInventory, error) {
	files, err := loadTurnRecvASTFiles(root)
	if err != nil {
		return turnRecvCurrentInventory{}, err
	}
	var out turnRecvCurrentInventory
	categoryCounts := make(map[string]int, len(turnRecvOwnershipCategories))
	for _, cat := range turnRecvOwnershipCategories {
		categoryCounts[cat] = 0
	}
	responsibilityCounts := make(map[string]int)
	fieldNames := make(map[string]bool)
	var receiverMethods []turnRecvMethod
	fanout := make(map[string]map[string]bool)
	var syncs []turnRecvSyncPrimitive

	for _, file := range files {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok.String() != "type" {
				return true
			}
			for _, spec := range decl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "retryRecvStream" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					typeText := nodeText(field.Type)
					syncTypes := syncTypesFor(typeText)
					for _, name := range field.Names {
						category := turnRecvFieldCategory(name.Name)
						out.Fields = append(out.Fields, turnRecvField{Name: name.Name, Type: typeText, Category: category, SyncTypes: syncTypes})
						fieldNames[name.Name] = true
						categoryCounts[category]++
						for _, syncType := range syncTypes {
							syncs = append(syncs, turnRecvSyncPrimitive{Field: name.Name, Type: syncType})
						}
					}
				}
			}
			return true
		})
	}
	if len(out.Fields) == 0 {
		return turnRecvCurrentInventory{}, fmt.Errorf("retryRecvStream struct not found")
	}

	for _, file := range files {
		for _, decl := range file.AST.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || funcDecl.Body == nil || len(funcDecl.Recv.List) == 0 || !isRetryRecvReceiver(funcDecl.Recv.List[0].Type) {
				continue
			}
			name := funcDecl.Name.Name
			reachesExecutor := false
			methodPackages := make(map[string]bool)
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				switch expr := node.(type) {
				case *ast.SelectorExpr:
					if ident, ok := expr.X.(*ast.Ident); ok {
						if ident.Name == turnRecvReceiverName(funcDecl.Recv.List[0].Type) && expr.Sel.Name == "executor" {
							reachesExecutor = true
						}
						if importPath, ok := file.Imports[ident.Name]; ok && strings.HasPrefix(importPath, runtimeModulePrefix) {
							methodPackages[importPath] = true
						}
					}
				}
				return true
			})
			responsibility := turnRecvMethodResponsibility(name)
			method := turnRecvMethod{
				Name: name, File: file.RelPath, Line: file.FSet.Position(funcDecl.Pos()).Line,
				Responsibility: responsibility, ReachesExecutor: reachesExecutor,
			}
			receiverMethods = append(receiverMethods, method)
			responsibilityCounts[responsibility]++
			for importPath := range methodPackages {
				if fanout[importPath] == nil {
					fanout[importPath] = make(map[string]bool)
				}
				fanout[importPath][name] = true
			}
		}
	}

	stateCopies := scanTurnRecvStateCopies(files, fieldNames)
	sort.Slice(out.Fields, func(i, j int) bool { return i < j }) // preserve declaration order explicitly
	sort.Slice(syncs, func(i, j int) bool {
		if syncs[i].Field != syncs[j].Field {
			return syncs[i].Field < syncs[j].Field
		}
		return syncs[i].Type < syncs[j].Type
	})
	sort.Slice(receiverMethods, func(i, j int) bool {
		if receiverMethods[i].File != receiverMethods[j].File {
			return receiverMethods[i].File < receiverMethods[j].File
		}
		if receiverMethods[i].Line != receiverMethods[j].Line {
			return receiverMethods[i].Line < receiverMethods[j].Line
		}
		return receiverMethods[i].Name < receiverMethods[j].Name
	})
	for path, methodSet := range fanout {
		methods := make([]string, 0, len(methodSet))
		for method := range methodSet {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		out.DomainPackageFanout = append(out.DomainPackageFanout, turnRecvDomainFanout{Package: path, Methods: methods})
	}
	sort.Slice(out.DomainPackageFanout, func(i, j int) bool { return out.DomainPackageFanout[i].Package < out.DomainPackageFanout[j].Package })
	out.FieldCount = len(out.Fields)
	out.CategoryCounts = categoryCounts
	out.SyncPrimitives = syncs
	out.SyncPrimitiveCount = len(syncs)
	out.Methods = receiverMethods
	out.MethodCount = len(receiverMethods)
	out.ResponsibilityCounts = responsibilityCounts
	out.CrossDomainMethodCount = 0
	for _, method := range receiverMethods {
		if method.Responsibility != "eventstream_control" && method.Responsibility != "transport_adapter" {
			out.CrossDomainMethodCount++
		}
	}
	out.DomainPackageFanoutCount = len(out.DomainPackageFanout)
	out.ExecutorReachability = turnRecvExecutorReachability{BroadFieldForbidden: true}
	for _, field := range out.Fields {
		if field.Name == "executor" && field.Type == "*Executor" {
			out.ExecutorReachability.FieldPresent = true
		}
	}
	for _, method := range receiverMethods {
		if method.ReachesExecutor {
			out.ExecutorReachability.MethodCount++
			out.ExecutorReachability.Methods = append(out.ExecutorReachability.Methods, method.Name)
		}
	}
	out.StateCopyAssignments = stateCopies
	out.StateCopyAssignmentCount = len(stateCopies)
	out.MirroredFacts = turnRecvMirroredFacts()
	return out, nil
}

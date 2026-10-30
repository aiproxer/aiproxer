package runtimebundle_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 7.1 truthful inventory of generation-resource lifecycle / idempotency
// mechanisms. This is a current-state contract: it fails if a listed mechanism
// silently disappears or moves before Tasks 7.2/7.3 update the inventory.
// Dispositions encode the final ownership target (req 8.1-8.5, 8.8, 8.10).

type lifeDisposition string

const (
	lifeCanonicalResource   lifeDisposition = "canonical_resource_owner"
	lifeResourceLocal       lifeDisposition = "canonical_resource_local"
	lifeGenerationState     lifeDisposition = "legitimate_generation_state_owner"
	lifeManagerPolicy       lifeDisposition = "manager_policy_owner"
	lifeDiagnosticsOnly     lifeDisposition = "diagnostics_only"
	lifeDuplicateToDelete   lifeDisposition = "duplicate_wrapper_to_delete"
	lifeProcessOnlyNegative lifeDisposition = "process_only_not_generation"
)

type lifeInventoryEntry struct {
	Type          string
	FieldOrMethod string
	Operation     string // transfer | prepare | activate | rollback | quiesce | close | publication_refcount_drain | retirement_policy | diagnostics | shutdown
	Owner         string
	Disposition   lifeDisposition
	File          string // production file relative to package / sibling package
}

// lifecycleOwnerInventory enumerates every production generation-resource
// lifecycle/idempotency mechanism relevant to CandidateRuntime,
// GenerationBundle, Generation, ResourceLedger/ledgerEntry, BackendInstance,
// and Manager (Task 7.3 post-state). LifecycleWorker was deleted in Task 7.3:
// retirement scheduling moved to Manager (cleanupPolicy/observer fields,
// scheduleRetire, RetireGeneration) and the retireGeneration function in
// retire.go. Generation.closed/closeErr (the sole Task 7.2 duplicate-wrapper
// entries) were deleted in Task 7.3; Close/Discard now derive terminal state
// from the lifecycle word plus owned-payload presence only.
var lifecycleOwnerInventory = []lifeInventoryEntry{
	// --- ResourceLedger / ledgerEntry: canonical resource phase owner ---
	{Type: "ResourceLedger", FieldOrMethod: "mu", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "cond", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "state", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "preparing", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "activating", Operation: "activate", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "quiescing", Operation: "quiesce", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "rollingBack", Operation: "rollback", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "closing", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "prepareDone", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "activateDone", Operation: "activate", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "quiesceDone", Operation: "quiesce", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "prepareErr", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "activateErr", Operation: "activate", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "quiesceErr", Operation: "quiesce", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "rollbackErr", Operation: "rollback", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "closeErr", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "sealed", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ResourceLedger", FieldOrMethod: "prepared", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "mu", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "cond", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "cleaning", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "cleanedOK", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "terminalClaimed", Operation: "rollback", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "cleanErr", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "startAttempted", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},
	{Type: "ledgerEntry", FieldOrMethod: "started", Operation: "prepare", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "resource_ledger.go"},

	// --- BackendInstance: resource-local lifecycle (not generation aggregate duplicate) ---
	{Type: "BackendInstance", FieldOrMethod: "startOnce", Operation: "prepare", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},
	{Type: "BackendInstance", FieldOrMethod: "startErr", Operation: "prepare", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},
	{Type: "BackendInstance", FieldOrMethod: "startAttempted", Operation: "prepare", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},
	{Type: "BackendInstance", FieldOrMethod: "closeOnce", Operation: "close", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},
	{Type: "BackendInstance", FieldOrMethod: "closeErr", Operation: "close", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},
	{Type: "BackendInstance", FieldOrMethod: "started", Operation: "prepare", Owner: "BackendInstance", Disposition: lifeResourceLocal, File: "backend_instance.go"},

	// --- candidateAssembly: transfer vs lifecycle exclusive claim; no phase guards ---
	{Type: "candidateAssembly", FieldOrMethod: "lifeMu", Operation: "transfer", Owner: "candidateAssembly", Disposition: lifeCanonicalResource, File: "candidate_http.go"},
	{Type: "candidateAssembly", FieldOrMethod: "lifeClaimed", Operation: "transfer", Owner: "candidateAssembly", Disposition: lifeCanonicalResource, File: "candidate_http.go"},
	{Type: "candidateAssembly", FieldOrMethod: "ledgerTransferred", Operation: "transfer", Owner: "candidateAssembly", Disposition: lifeCanonicalResource, File: "candidate_http.go"},
	{Type: "candidateAssembly", FieldOrMethod: "transferLedgerOwnership", Operation: "transfer", Owner: "candidateAssembly", Disposition: lifeCanonicalResource, File: "candidate_lifecycle.go"},
	{Type: "candidateAssembly", FieldOrMethod: "claimLifecycleLedger", Operation: "transfer", Owner: "candidateAssembly", Disposition: lifeCanonicalResource, File: "candidate_lifecycle.go"},

	// --- GenerationBundle: stores/delegates to canonical ledger only ---
	{Type: "GenerationBundle", FieldOrMethod: "ledger", Operation: "close", Owner: "ResourceLedger", Disposition: lifeCanonicalResource, File: "generation_bundle.go"},

	// --- Generation: identity/refcount/drain/payload binding/retire admission
	// are legitimate generation state (Task 7.3 deleted the closed/closeErr
	// duplicate resource-close caches entirely; Close/Discard derive terminal
	// state from the lifecycle word plus owned-payload presence only) ---
	{Type: "Generation", FieldOrMethod: "word", Operation: "publication_refcount_drain", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "drainMu", Operation: "publication_refcount_drain", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "drainCh", Operation: "publication_refcount_drain", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "drainClosed", Operation: "publication_refcount_drain", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "retireAdmit", Operation: "retirement_policy", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "closeMu", Operation: "close", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "payloadMu", Operation: "close", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "owned", Operation: "close", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "requestPlane", Operation: "publication_refcount_drain", Owner: "Generation", Disposition: lifeGenerationState, File: "../runtimehost/generation.go"},
	{Type: "Generation", FieldOrMethod: "closeCount", Operation: "diagnostics", Owner: "Generation", Disposition: lifeDiagnosticsOnly, File: "../runtimehost/generation.go"},

	// --- Manager publication / retention / shutdown / retirement scheduling
	// (Task 7.3: Manager owns post-publish retirement scheduling; LifecycleWorker
	// deleted) ---
	{Type: "Manager", FieldOrMethod: "active", Operation: "publication_refcount_drain", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "retained", Operation: "retirement_policy", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "mu", Operation: "publication_refcount_drain", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "shuttingDown", Operation: "shutdown", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "cleanupPolicy", Operation: "retirement_policy", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "observer", Operation: "diagnostics", Owner: "Manager", Disposition: lifeDiagnosticsOnly, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "Publish", Operation: "publication_refcount_drain", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "scheduleRetire", Operation: "retirement_policy", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "RetireGeneration", Operation: "retirement_policy", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/manager.go"},
	{Type: "Manager", FieldOrMethod: "BeginShutdown", Operation: "shutdown", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/shutdown.go"},
	{Type: "Manager", FieldOrMethod: "ShutdownDetached", Operation: "shutdown", Owner: "Manager", Disposition: lifeManagerPolicy, File: "../runtimehost/shutdown.go"},

	// --- Process-only negative control (not generation ownership) ---
	{Type: "ProcessServices", FieldOrMethod: "closeOnce", Operation: "close", Owner: "ProcessServices", Disposition: lifeProcessOnlyNegative, File: "process_services_types.go"},
	{Type: "ProcessServices", FieldOrMethod: "closeErr", Operation: "close", Owner: "ProcessServices", Disposition: lifeProcessOnlyNegative, File: "process_services_types.go"},
	{Type: "ProcessServices", FieldOrMethod: "closed", Operation: "close", Owner: "ProcessServices", Disposition: lifeProcessOnlyNegative, File: "process_services_types.go"},
}

func TestLifecycleOwner_InventoryCurrentMechanismsPresent(t *testing.T) {
	t.Parallel()
	if len(lifecycleOwnerInventory) == 0 {
		t.Fatal("lifecycleOwnerInventory must not be empty")
	}
	seen := map[string]bool{}
	for _, e := range lifecycleOwnerInventory {
		key := e.Type + "." + e.FieldOrMethod
		if e.Type == "" || e.FieldOrMethod == "" || e.Operation == "" || e.Owner == "" || e.File == "" {
			t.Fatalf("incomplete inventory entry: %+v", e)
		}
		if !validLifeDisposition(e.Disposition) {
			t.Fatalf("%s: invalid disposition %q", key, e.Disposition)
		}
		if !validLifeOperation(e.Operation) {
			t.Fatalf("%s: invalid operation %q", key, e.Operation)
		}
		if seen[key] {
			t.Fatalf("duplicate inventory key %s", key)
		}
		seen[key] = true
		if !lifeSymbolPresent(t, e.File, e.Type, e.FieldOrMethod) {
			t.Fatalf("inventory drift: %s.%s missing from %s (update inventory when Task 7.2/7.3 changes owners)", e.Type, e.FieldOrMethod, e.File)
		}
	}
}

// TestLifecycleOwner_InventoryNamesDuplicateWrappers expects zero remaining
// duplicate-wrapper entries: Task 7.3 deleted Generation.closed/closeErr (the
// only Task 7.2 survivors) and LifecycleWorker entirely.
func TestLifecycleOwner_InventoryNamesDuplicateWrappers(t *testing.T) {
	t.Parallel()
	wantDup := map[string]bool{}
	found := map[string]bool{}
	for _, e := range lifecycleOwnerInventory {
		key := e.Type + "." + e.FieldOrMethod
		if e.Disposition == lifeDuplicateToDelete {
			found[key] = true
		}
	}
	for key := range wantDup {
		if !found[key] {
			t.Fatalf("expected duplicate disposition for %s", key)
		}
	}
	for key := range found {
		if !wantDup[key] {
			t.Fatalf("unexpected remaining duplicate disposition %s (Task 7.2 should leave only Generation.closed/closeErr)", key)
		}
	}
}

func TestLifecycleOwner_InventoryCanonicalLedgerOwner(t *testing.T) {
	t.Parallel()
	var ledgerCanonical int
	for _, e := range lifecycleOwnerInventory {
		if e.Owner == "ResourceLedger" && e.Disposition == lifeCanonicalResource {
			ledgerCanonical++
		}
	}
	if ledgerCanonical < 10 {
		t.Fatalf("ResourceLedger must remain the canonical resource owner in inventory, got %d canonical entries", ledgerCanonical)
	}
}

func TestLifecycleOwner_InventoryResourceLocalBackendNotDuplicate(t *testing.T) {
	t.Parallel()
	var local int
	for _, e := range lifecycleOwnerInventory {
		if e.Type == "BackendInstance" {
			if e.Disposition != lifeResourceLocal {
				t.Fatalf("BackendInstance.%s disposition=%s want resource-local", e.FieldOrMethod, e.Disposition)
			}
			local++
		}
	}
	if local < 6 {
		t.Fatalf("BackendInstance resource-local entries=%d want >=6", local)
	}
}

func TestLifecycleOwner_InventoryProcessOnlyNegativeControl(t *testing.T) {
	t.Parallel()
	var n int
	for _, e := range lifecycleOwnerInventory {
		if e.Disposition == lifeProcessOnlyNegative {
			if e.Type != "ProcessServices" {
				t.Fatalf("process-only control must be ProcessServices, got %s", e.Type)
			}
			n++
		}
	}
	if n < 3 {
		t.Fatalf("process-only negative controls=%d want >=3", n)
	}
}

func validLifeDisposition(d lifeDisposition) bool {
	switch d {
	case lifeCanonicalResource, lifeResourceLocal, lifeGenerationState, lifeManagerPolicy,
		lifeDiagnosticsOnly, lifeDuplicateToDelete, lifeProcessOnlyNegative:
		return true
	default:
		return false
	}
}

func validLifeOperation(op string) bool {
	switch op {
	case "transfer", "prepare", "activate", "rollback", "quiesce", "close",
		"publication_refcount_drain", "retirement_policy", "diagnostics", "shutdown":
		return true
	default:
		return false
	}
}

func lifeSymbolPresent(t *testing.T, relFile, typeName, member string) bool {
	t.Helper()
	path := filepath.Clean(relFile)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if lifeFindInFile(f, typeName, member) {
		return true
	}
	// Methods / locals may live in another file of the same package.
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if filepath.Clean(p) == path {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		pf, err := parser.ParseFile(fset, p, b, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if lifeFindInFile(pf, typeName, member) {
			return true
		}
	}
	return false
}

func lifeFindInFile(f *ast.File, typeName, member string) bool {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != typeName {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, field := range st.Fields.List {
					for _, n := range field.Names {
						if n.Name == member {
							return true
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			// Function-local lifecycle vars (e.g. buildBackends.releaseOnce).
			if d.Name.Name == typeName && d.Body != nil && lifeLocalVarInFunc(d, member) {
				return true
			}
			if d.Name.Name != member {
				continue
			}
			if d.Recv == nil || len(d.Recv.List) == 0 {
				if typeName == member {
					return true
				}
				continue
			}
			recv := lifeRecvTypeName(d.Recv.List[0].Type)
			if recv == typeName || recv == "*"+typeName {
				return true
			}
		}
	}
	return false
}

// lifeLocalVarInFunc truthfully recognizes function-local lifecycle variables via
// AST (var / :=), without raw file substring matching.
func lifeLocalVarInFunc(fd *ast.FuncDecl, member string) bool {
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if ok && id.Name == member {
					found = true
					return false
				}
			}
		case *ast.GenDecl:
			if x.Tok != token.VAR {
				return true
			}
			for _, spec := range x.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, id := range vs.Names {
					if id.Name == member {
						found = true
						return false
					}
				}
			}
		}
		return true
	})
	return found
}

func lifeRecvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + lifeRecvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

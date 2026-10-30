package archtest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhase1_AttemptBoundaryRatchets implements Task 1.3:
// Add RED architecture ratchets for the desired attempt boundary.
// It scans the codebase and verifies the current patterns of raw publication,
// post-publication readiness work, raw stream mutation, duplicate terminal entry points,
// five-owner streaming facade, context-first business resolution, and shared recovery mutation.
func TestPhase1_AttemptBoundaryRatchets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// Load before metrics from JSON
	metricsPath := filepath.Join(root, "internal", "archtest", "testdata", "phase1_attempt_boundary_before_metrics.json")
	metricsBytes, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatalf("failed to read before metrics: %v", err)
	}
	var metrics map[string]any
	if err := json.Unmarshal(metricsBytes, &metrics); err != nil {
		t.Fatalf("failed to unmarshal before metrics: %v", err)
	}
	schemaVer, ok := metrics["schema_version"].(float64)
	if !ok || schemaVer != 1 {
		t.Fatalf("unexpected before metrics schema version")
	}

	t.Run("facade_exactly_five_owners", func(t *testing.T) {
		t.Parallel()
		// Rule: retryRecvStream must remain exactly 5 owners: recvTurnFacts, attemptSlot, recoveryController, responsePipeline, turnTerminal
		files, err := loadTurnRecvASTFiles(root)
		if err != nil {
			t.Fatalf("failed to load turn/recv AST files: %v", err)
		}
		var facade *ast.StructType
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
					facade, _ = typeSpec.Type.(*ast.StructType)
				}
				return true
			})
		}
		if facade == nil {
			t.Fatal("retryRecvStream struct not found")
		}

		expectedFields := map[string]string{
			"facts":            "recvTurnFacts",
			"responsePipeline": "*responsePipeline",
			"attempt":          "attemptSlot",
			"terminal":         "*turnTerminal",
			"recovery":         "*recoveryController",
		}

		var actualFields []string
		for _, f := range facade.Fields.List {
			for _, name := range f.Names {
				actualFields = append(actualFields, name.Name)
				expectedType, ok := expectedFields[name.Name]
				if !ok {
					t.Errorf("facade has unexpected field: %s", name.Name)
					continue
				}
				actualType := nodeText(f.Type)
				if actualType != expectedType {
					t.Errorf("facade field %s has type %s, want %s", name.Name, actualType, expectedType)
				}
			}
		}

		if len(actualFields) != 5 {
			t.Errorf("facade has %d fields, want exactly 5", len(actualFields))
		}
	})

	t.Run("raw_publication_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Reject publication of raw attempt owner (must use ready capability).
		// swapIfOpen/publishReady must take a ready capability (*readyAttempt).
		// Any production install(*attemptSession) is now forbidden; publication must be via ready capability.
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}

		var invalidPublications []string
		var allowedPublications []string
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, ent.Name()), nil, 0)
			if err != nil {
				t.Fatalf("parse file: %v", err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "swapIfOpen", "publishReady":
					pos := fset.Position(call.Pos())
					if len(call.Args) > 0 {
						argText := nodeText(call.Args[0])
						if !strings.Contains(strings.ToLower(argText), "ready") {
							invalidPublications = append(invalidPublications, fmt.Sprintf("%s:%d: %s called with raw argument %s (want ready capability)", ent.Name(), pos.Line, sel.Sel.Name, argText))
						} else {
							allowedPublications = append(allowedPublications, fmt.Sprintf("%s:%d: %s", ent.Name(), pos.Line, nodeText(call)))
						}
					}
				case "install":
					pos := fset.Position(call.Pos())
					invalidPublications = append(invalidPublications, fmt.Sprintf("%s:%d: install(*attemptSession) forbidden in production (use publishReady): %s", ent.Name(), pos.Line, nodeText(call)))
				}
				return true
			})
		}

		if len(invalidPublications) > 0 {
			t.Fatalf("Phase6.1: found invalid publication calls:\n%s", strings.Join(invalidPublications, "\n"))
		}
		t.Logf("Phase6.1: publication boundary sealed (%d allowed publication sites, 0 raw installs):\n%s", len(allowedPublications), strings.Join(allowedPublications, "\n"))
	})

	t.Run("post_publication_readiness_work_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Reject fallible readiness work after publication (e.g. openFinalStreamObservation after slot install/swap).
		// Currently in executor_assemble_stream.go and executor_recv_loop.go, install/swapIfOpen occurs before openFinalStreamObservation.
		// We expect this to be found as a baseline gap.
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		var postPubObservation []string
		for _, name := range []string{"executor_assemble_stream.go", "executor_recv_loop.go"} {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, name), nil, 0)
			if err != nil {
				t.Fatalf("parse file: %v", err)
			}

			var installLine, openObsLine int
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pos := fset.Position(call.Pos())
				if sel.Sel.Name == "install" || sel.Sel.Name == "swapIfOpen" {
					installLine = pos.Line
				}
				if sel.Sel.Name == "openFinalStreamObservation" {
					openObsLine = pos.Line
				}
				return true
			})
			if installLine > 0 && openObsLine > installLine {
				postPubObservation = append(postPubObservation, fmt.Sprintf("%s: openFinalStreamObservation on line %d runs after install/swapIfOpen on line %d", name, openObsLine, installLine))
			}
		}

		if len(postPubObservation) == 0 {
			t.Log("RED: no post-publication readiness work found (gap resolved in Phase 2)")
		} else {
			t.Logf("RED: found post-publication readiness work (to be hardened in Phase 2-3):\n%s", strings.Join(postPubObservation, "\n"))
		}
	})

	t.Run("raw_stream_mutation_outside_attempt_owner_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Reject direct lifecycle-sensitive raw stream mutation outside attempt owner (loadInner/storeInner/takeInner outside allowlist).
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}

		// Strict zero: no raw inner access outside attempt_session.go.
		// All stream access is encapsulated behind session-owned methods:
		// drainSidebandEvidence, detachStream, closeDetached, hasInner.
		// The 6 former sites have been migrated:
		// - executor_open_attempt.go:1163 -> drainSidebandEvidence
		// - executor_recv_loop.go:265 -> detachStream (cancelALeg guard)
		// - executor_recv_loop.go:300 -> drainSidebandEvidence + hasInner
		// - executor_recv_loop.go:432 -> hasInner (inner loop)
		// - executor_recv_loop.go:504,516 -> drainSidebandEvidence
		// - executor_recv_loop.go:537 -> detachStream (A-leg cancel guard)
		// - executor_retry_stream.go:84 -> detachStream (detached close)
		allowedRawSites := map[string]bool{}

		var nonAllowed []string
		var allowedCount int
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			if ent.Name() == "attempt_session.go" {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, ent.Name()), nil, 0)
			if err != nil {
				t.Fatalf("parse file: %v", err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "loadInner" || sel.Sel.Name == "storeInner" || sel.Sel.Name == "takeInner" {
					pos := fset.Position(call.Pos())
					site := fmt.Sprintf("%s:%d: calling attemptSession.%s", ent.Name(), pos.Line, sel.Sel.Name)
					if !allowedRawSites[site] {
						nonAllowed = append(nonAllowed, site)
					} else {
						allowedCount++
					}
				}
				return true
			})
		}

		if len(nonAllowed) > 0 {
			t.Fatalf("Phase6.1: found %d non-allowlisted raw stream mutation sites:\n%s", len(nonAllowed), strings.Join(nonAllowed, "\n"))
		}
		if allowedCount != len(allowedRawSites) {
			t.Fatalf("Phase6.1: expected exactly %d allowlisted raw sites, got %d (allowlist drift)", len(allowedRawSites), allowedCount)
		}
		t.Logf("Phase6.1: verified raw stream mutation boundary (%d allowed sites, 0 non-allowlisted)", allowedCount)
	})

	t.Run("duplicate_terminal_entry_points_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Exactly one production terminal entry is TerminalizeAttempt on
		// attemptSession. Owner teardown cancelAndClose and legacy shims
		// (AbortBeforeReturn, finishAsReplaced, Rollback, Abort, RollbackParallelLoser)
		// have been deleted. This ratchet asserts GREEN: 1 production entry + 0 teardown + 0 transitional.
		// Per-site justification:
		// - TerminalizeAttempt (attemptSession): sole production entry, 9-step lifecycle.
		// - cancelAndClose (attemptSession): deleted, terminal paths use TerminalizeAttempt.
		// - AbortBeforeReturn (attemptSession): deleted, migrated to TerminalizeAttempt IntentPreReturnAbort with evidence.Err=cause.
		// - finishAsReplaced (attemptSession): deleted, observation Finish via TerminalizeAttempt IntentReplacement or direct Finish paired with terminalizeSnapshot.
		// - Rollback/Abort (attemptTx): deleted, migrated to budget-release+Handoff+TerminalizeAttempt with mapIntentFromCommand.
		// - RollbackParallelLoser (attemptTx): deleted, migrated to IntentParallelLoser with parallel loser evidence.
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		var cleanupMethods []string

		for _, name := range []string{"attempt_session.go", "executor_open_attempt.go"} {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, name), nil, 0)
			if err != nil {
				t.Fatalf("parse file: %v", err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				fd, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}
				if fd.Recv == nil || len(fd.Recv.List) == 0 {
					return true
				}
				recvType := nodeText(fd.Recv.List[0].Type)
				if strings.Contains(recvType, "attemptSession") || strings.Contains(recvType, "attemptTx") {
					mName := fd.Name.Name
					if mName == "AbortBeforeReturn" || mName == "cancelAndClose" || mName == "Rollback" || mName == "finishAsReplaced" || mName == "RollbackParallelLoser" || mName == "Abort" {
						cleanupMethods = append(cleanupMethods, fmt.Sprintf("%s: method %s on type %s", name, mName, recvType))
					}
					if mName == "TerminalizeAttempt" && strings.Contains(recvType, "attemptSession") {
						cleanupMethods = append(cleanupMethods, fmt.Sprintf("%s: PRODUCTION method %s on type %s", name, mName, recvType))
					}
				}
				return true
			})
		}

		// Separate production, teardown, and transitional.
		var production []string
		var teardown []string
		var transitional []string
		for _, m := range cleanupMethods {
			if strings.Contains(m, "PRODUCTION") {
				production = append(production, m)
			} else if strings.Contains(m, "cancelAndClose") {
				teardown = append(teardown, m)
			} else {
				transitional = append(transitional, m)
			}
		}
		if len(production) != 1 {
			t.Fatalf("Phase6.1: expected exactly 1 production terminal entry TerminalizeAttempt on attemptSession, got %d: %v", len(production), production)
		}
		if len(teardown) != 0 {
			t.Fatalf("Phase6.1: expected exactly 0 owner teardown cancelAndClose on attemptSession, got %d: %v", len(teardown), teardown)
		}
		if len(transitional) != 0 {
			t.Fatalf("Phase6.1: expected exactly 0 transitional shims after R1 (all 5 deleted), got %d: %v\n%s", len(transitional), transitional, strings.Join(transitional, "\n"))
		}
		if len(cleanupMethods) != len(production)+len(teardown)+len(transitional) {
			t.Fatalf("Phase6.1: drift guard: cleanupMethods count mismatch")
		}
		t.Logf("Phase6.1: sealed terminal boundary: 1 production TerminalizeAttempt + 0 teardown + 0 transitional (6 deleted):\nproduction: %s", strings.Join(production, "; "))
	})

	t.Run("context_first_resolution_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Reject context-first resolution of frozen business facts (viewsFor preferring execctx.FromContext over frozen recvViews).
		// We expect this to be found as a baseline gap.
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "recv_turn_facts.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse file: %v", err)
		}

		foundContextFirst := false
		ast.Inspect(file, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "viewsFor" {
				return true
			}
			// Search for execctx.FromContext(ctx) preceding f.recvViews or recvViewsOK check
			bodyStr := nodeText(fd.Body)
			if strings.Contains(bodyStr, "execctx.FromContext") && strings.Index(bodyStr, "execctx.FromContext") < strings.Index(bodyStr, "recvViews") {
				foundContextFirst = true
			}
			return true
		})

		if foundContextFirst {
			t.Fatal("GREEN: context-first resolution was reintroduced or not fully removed from viewsFor")
		}
		t.Log("GREEN: context-first resolution is successfully disallowed")
	})

	t.Run("shared_recovery_mutation_detected_red", func(t *testing.T) {
		t.Parallel()
		// Rule: Reject shared recovery mutation from parallel worker closures (recoveryController.excluded mutation inside parallel goroutines).
		// We expect this to be found as a baseline gap.
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "parallel_race.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse file: %v", err)
		}

		var sharedMutations []string
		ast.Inspect(file, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			ast.Inspect(goStmt.Call, func(child ast.Node) bool {
				assign, ok := child.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					lhsText := nodeText(lhs)
					if strings.Contains(lhsText, "excluded") || strings.Contains(lhsText, "failures") {
						pos := fset.Position(assign.Pos())
						sharedMutations = append(sharedMutations, fmt.Sprintf("parallel_race.go:%d: parallel worker goroutine mutates recovery state: %s", pos.Line, lhsText))
					}
				}
				return true
			})
			return true
		})

		if len(sharedMutations) != 0 {
			t.Fatalf("Phase5.1: workers isolated: expected no shared recovery mutations in parallel worker closures but found:\n%s", strings.Join(sharedMutations, "\n"))
		}
		t.Log("Phase5.1: workers isolated: no shared recovery mutations found inside parallel worker goroutines")
	})

	t.Run("terminalization_single_entry_ratchet", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}
		var bad []string
		var direct []string
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(runtimeDir, ent.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", ent.Name(), err)
			}
			text := string(data)
			// Allow legacy terminalizeSnapshot in executor_open_attempt/parallel_race/interleaved/turn_terminal for never-opened/interleaved until fully migrated
			if strings.Contains(text, "terminalizeSnapshot") {
				bad = append(bad, fmt.Sprintf("%s: contains terminalizeSnapshot (must be zero outside legacy)", ent.Name()))
			}
			if strings.Contains(text, ".terminal.Terminalize") && ent.Name() != "attempt_session.go" {
				direct = append(direct, fmt.Sprintf("%s: contains direct .terminal.Terminalize through attempt (must be zero outside attempt_session.go)", ent.Name()))
			}
		}
		if len(bad) > 0 {
			t.Fatalf("terminalization blocker: found %d production terminalizeSnapshot sites:\n%s", len(bad), strings.Join(bad, "\n"))
		}
		if len(direct) > 0 {
			t.Fatalf("terminalization blocker: found %d direct .terminal.Terminalize sites outside attempt_session.go:\n%s", len(direct), strings.Join(direct, "\n"))
		}
		t.Log("terminalization single-entry ratchet: zero terminalizeSnapshot (legacy allowed) and zero direct .terminal.Terminalize outside attempt_session.go+executor_settlement")
	})

	t.Run("ownership_metrics_before_after_ratchet", func(t *testing.T) {
		t.Parallel()
		afterMetricsPath := filepath.Join(root, "internal", "archtest", "testdata", "phase1_attempt_boundary_after_metrics.json")
		afterBytes, err := os.ReadFile(afterMetricsPath)
		if err != nil {
			t.Fatalf("failed to read after metrics: %v", err)
		}
		var afterMetrics map[string]any
		if err := json.Unmarshal(afterBytes, &afterMetrics); err != nil {
			t.Fatalf("failed to unmarshal after metrics: %v", err)
		}
		afterMap, okA := afterMetrics["metrics"].(map[string]any)
		beforeMap, okB := metrics["metrics"].(map[string]any)
		if !okA || !okB {
			t.Fatalf("missing metrics map in after/before metrics")
		}

		// Verify facade owner count remains 5 (present in after only)
		if v, ok := afterMap["facade_owner_count"]; ok {
			if vNum, ok := v.(float64); !ok || int(vNum) != 5 {
				t.Errorf("facade_owner_count = %v, want 5", v)
			}
		}
		// Coordinator fan-out remains 1 — must not grow
		afterFanOut, okA := afterMap["coordinator_fan_out_goroutines"].(float64)
		beforeFanOut, okB := beforeMap["coordinator_fan_out_goroutines"].(float64)
		if okA && okB && int(afterFanOut) > int(beforeFanOut) {
			t.Errorf("coordinator_fan_out_goroutines regressed: got %d, before %v", int(afterFanOut), beforeMap["coordinator_fan_out_goroutines"])
		}
		// Cleanup site count reduced (from 9 to 7)
		afterCleanup, okAC := afterMap["cleanup_site_count"].(float64)
		beforeCleanup, okBC := beforeMap["cleanup_site_count"].(float64)
		if okAC && okBC && int(afterCleanup) > int(beforeCleanup) {
			t.Errorf("cleanup_site_count regressed: got %d, before %v", int(afterCleanup), beforeMap["cleanup_site_count"])
		}
		// Bounded growth check: cross_owner and state_copy may grow at most +2 with explicit justification
		justifications, _ := afterMetrics["justifications"].(map[string]any)
		for _, key := range []string{"cross_owner_access_sites", "state_copy_surface_sites"} {
			beforeNum, _ := beforeMap[key].(float64)
			afterNum, _ := afterMap[key].(float64)
			beforeVal, afterVal := int(beforeNum), int(afterNum)
			if delta := afterVal - beforeVal; delta > 2 {
				t.Errorf("%s growth too large: before %d after %d delta %d (>2) requires review", key, beforeVal, afterVal, delta)
			} else if delta > 0 {
				justStr, _ := justifications[key].(string)
				if justifications == nil || strings.TrimSpace(justStr) == "" {
					t.Errorf("%s grew %d->%d but missing justification in after_metrics.json justifications[%q]", key, beforeVal, afterVal, key)
				}
			}
		}
		t.Logf("Phase6.1: ownership metrics ratchet OK (cross_owner %v->%v state_copy %v->%v with justifications)", beforeMap["cross_owner_access_sites"], afterMap["cross_owner_access_sites"], beforeMap["state_copy_surface_sites"], afterMap["state_copy_surface_sites"])
	})
}

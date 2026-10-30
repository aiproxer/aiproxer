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

func TestAttemptPublicationRatchets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	t.Run("ready_session_fence", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}
		var violations []string
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			if ent.Name() == "attempt_session.go" {
				continue
			}
			path := filepath.Join(runtimeDir, ent.Name())
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", ent.Name(), err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "session" || sel.Sel.Name == "pending" || sel.Sel.Name == "consumed" {
					if ident, ok := sel.X.(*ast.Ident); ok && strings.Contains(strings.ToLower(ident.Name), "ready") {
						pos := fset.Position(sel.Pos())
						violations = append(violations, path+":"+itoa(pos.Line)+": direct ready."+sel.Sel.Name+" (use narrow methods)")
					}
					if sel.Sel.Name == "session" {
						txt := nodeText(sel)
						if strings.Contains(txt, "ready") && strings.Contains(txt, "session") {
							pos := fset.Position(sel.Pos())
							violations = append(violations, path+":"+itoa(pos.Line)+": direct ready.session "+txt)
						}
					}
				}
				return true
			})
		}
		if len(violations) > 0 {
			t.Fatalf("readyAttempt fence violated (%d):\n%s", len(violations), strings.Join(violations, "\n"))
		}
	})

	t.Run("winner_publish_order", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		content, err := os.ReadFile(filepath.Join(runtimeDir, "parallel_race.go"))
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		text := string(content)
		if strings.Contains(text, "winnerOut.ready.Consume") {
			t.Fatalf("winner publish order violated: winnerOut.ready.Consume found (must stay ready through fallible effects)")
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "parallel_race.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "session" && strings.Contains(nodeText(sel), "ready") {
				pos := fset.Position(sel.Pos())
				t.Fatalf("ready.session access at %d: %s", pos.Line, nodeText(sel))
			}
			return true
		})
	})

	t.Run("slot_publish_vs_close_linearizability", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "attempt_session.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		hasPublishCheck := false
		hasDisposeUnlock := false
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if fn.Name.Name == "swapIfOpen" {
				body := nodeText(fn.Body)
				if strings.Contains(body, "publicationClosed") && strings.Index(body, "publicationClosed") < strings.Index(body, "Consume") {
					hasPublishCheck = true
				}
			}
			if fn.Name.Name == "Dispose" {
				body := nodeText(fn.Body)
				unlockIdx := strings.Index(body, "Unlock()")
				termIdx := strings.Index(body, "TerminalizeAttempt")
				if unlockIdx > 0 && termIdx > 0 && unlockIdx < termIdx {
					hasDisposeUnlock = true
				}
			}
			return true
		})
		if !hasPublishCheck {
			t.Fatal("swapIfOpen must check publicationClosed before Consume")
		}
		if !hasDisposeUnlock {
			t.Fatal("Dispose must unlock before TerminalizeAttempt")
		}
	})

	t.Run("recovery_controller_no_direct_authority_finalization", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "recovery_controller.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		var violations []string
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
			case "finalizeIncurredOrRelease", "Settle", "Release", "ApplyUnreservedUsage", "ReconcileAuthoritative":
				txt := nodeText(sel.X)
				if strings.Contains(txt, "authority") {
					pos := fset.Position(call.Pos())
					violations = append(violations, "recovery_controller.go:"+itoa(pos.Line)+": direct authority finalization call: "+nodeText(call))
				}
			}
			return true
		})
		if len(violations) > 0 {
			t.Fatalf("recoveryController cannot directly finalize prior attempt authority (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
		}
	})

	t.Run("recv_loop_no_post_swap_terminalize", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		content, err := os.ReadFile(filepath.Join(runtimeDir, "executor_recv_loop.go"))
		if err != nil {
			t.Fatalf("read executor_recv_loop.go: %v", err)
		}
		text := string(content)
		swapIdx := strings.Index(text, "swapIfOpen(ready)")
		if swapIdx == -1 {
			t.Fatal("swapIfOpen(ready) not found in executor_recv_loop.go")
		}
		postSwap := text[swapIdx:]
		if strings.Contains(postSwap, "old.TerminalizeAttempt") {
			t.Fatal("recv loop cannot contain second TerminalizeAttempt on old attempt after swap")
		}
	})

	t.Run("parallel_worker_isolation_and_immutable_outcome", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "parallel_race.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse parallel_race.go: %v", err)
		}

		// 1. Verify parallelArmOutcome struct fields
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "parallelArmOutcome" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == "hist" {
						t.Fatalf("parallelArmOutcome contains forbidden 'hist' field; must use typed immutable delta")
					}
				}
				typStr := nodeText(field.Type)
				if strings.Contains(typStr, "candidateFailureHistory") {
					t.Fatalf("parallelArmOutcome contains mutable 'candidateFailureHistory' type: %s", typStr)
				}
				if strings.Contains(typStr, "*transformExcludeTracker") || strings.Contains(typStr, "*recoveryController") || strings.Contains(typStr, "*ttftBudget") {
					t.Fatalf("parallelArmOutcome contains pointer to mutable shared type: %s", typStr)
				}
			}
			return true
		})

		// 2. Verify worker goroutine does not capture req.progress or use legProgress := *req.progress
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "tryOpenParallelGroup" {
				return true
			}
			ast.Inspect(fn.Body, func(child ast.Node) bool {
				goStmt, ok := child.(*ast.GoStmt)
				if !ok {
					return true
				}
				// Inside go func(entry legEntry)
				ast.Inspect(goStmt.Call, func(workerNode ast.Node) bool {
					if sel, ok := workerNode.(*ast.SelectorExpr); ok {
						if sel.Sel.Name == "progress" && nodeText(sel.X) == "req" {
							pos := fset.Position(sel.Pos())
							t.Fatalf("parallel_race.go:%d: worker goroutine captures req.progress", pos.Line)
						}
					}
					if assign, ok := workerNode.(*ast.AssignStmt); ok {
						for _, rhs := range assign.Rhs {
							txt := nodeText(rhs)
							if strings.Contains(txt, "req.progress") || strings.Contains(txt, "*req.progress") {
								pos := fset.Position(assign.Pos())
								t.Fatalf("parallel_race.go:%d: worker goroutine shallow-copies req.progress: %s", pos.Line, txt)
							}
						}
					}
					return true
				})
				return true
			})
			return true
		})

		// 3. Verify reduceParallelOutcomes / Reduce does not access o.hist
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || (fn.Name.Name != "Reduce" && fn.Name.Name != "reduceParallelOutcomes") {
				return true
			}
			ast.Inspect(fn.Body, func(child ast.Node) bool {
				if sel, ok := child.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "hist" {
						pos := fset.Position(sel.Pos())
						t.Fatalf("parallel_race.go:%d: reducer accesses .hist: %s", pos.Line, nodeText(sel))
					}
				}
				return true
			})
			return true
		})
	})

	t.Run("no_raw_session_readiness_signature", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatalf("read runtime dir: %v", err)
		}
		var violations []string
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(runtimeDir, ent.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", ent.Name(), err)
			}
			if strings.Contains(string(content), "prepareReadyAttempt") {
				violations = append(violations, fmt.Sprintf("%s: contains raw-session prepareReadyAttempt (forbidden in production)", ent.Name()))
			}
		}
		if len(violations) > 0 {
			t.Fatalf("prepareReadyAttempt signature found in production:\n%s", strings.Join(violations, "\n"))
		}
	})

	t.Run("unified_ready_readiness_api", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		// Check that initial, replacement, and interleaved flows invoke .Prepare on readyAttempt
		for _, file := range []string{"executor_assemble_stream.go", "executor_recv_loop.go", "interleaved_open.go"} {
			content, err := os.ReadFile(filepath.Join(runtimeDir, file))
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			text := string(content)
			if !strings.Contains(text, ".Prepare(") {
				t.Errorf("%s does not invoke ready.Prepare", file)
			}
			if strings.Contains(text, "DrainSidebandEvidence") || strings.Contains(text, "OpenFinalStreamObservation") {
				t.Errorf("%s directly invokes narrow readiness methods instead of unified ready.Prepare", file)
			}
		}
	})

	t.Run("retry_stream_close_clean", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		content, err := os.ReadFile(filepath.Join(runtimeDir, "executor_retry_stream.go"))
		if err != nil {
			t.Fatalf("read executor_retry_stream.go: %v", err)
		}
		text := string(content)
		if strings.Contains(text, ".detachStream") {
			t.Fatalf("executor_retry_stream.go must not call .detachStream (must rely on TerminalizeAttempt)")
		}
		if strings.Contains(text, "cancelForClose") {
			t.Fatalf("executor_retry_stream.go must not call cancelForClose")
		}
		if strings.Contains(text, "closeBackend") {
			t.Fatalf("executor_retry_stream.go must not call closeBackend")
		}
		if !strings.Contains(text, "s.terminal.closeClose(") {
			t.Fatalf("executor_retry_stream.go must call named turn helper closeClose")
		}
	})

	t.Run("turn_terminal_close_and_gate_single_terminalize", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "turn_terminal.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse turn_terminal.go: %v", err)
		}
		var hasCloseBackend, hasCancelForClose bool
		var closeCloseTerminalizeCount int
		var gateReplacementTerminalizeCount int
		var gateReplacementRecordLegCount int

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if fn.Name.Name == "closeBackend" {
				hasCloseBackend = true
			}
			if fn.Name.Name == "cancelForClose" {
				hasCancelForClose = true
			}
			if fn.Name.Name == "closeClose" {
				ast.Inspect(fn.Body, func(child ast.Node) bool {
					if call, ok := child.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TerminalizeAttempt" {
							closeCloseTerminalizeCount++
						}
					}
					return true
				})
			}
			if fn.Name.Name == "terminalizeGateReplacement" {
				ast.Inspect(fn.Body, func(child ast.Node) bool {
					if call, ok := child.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if sel.Sel.Name == "TerminalizeAttempt" {
								gateReplacementTerminalizeCount++
							}
							if sel.Sel.Name == "recordBillingLegForAttempt" {
								gateReplacementRecordLegCount++
							}
						}
					}
					return true
				})
			}
			return true
		})

		if hasCloseBackend {
			t.Fatal("turnTerminal must not define closeBackend")
		}
		if hasCancelForClose {
			t.Fatal("turnTerminal must not define cancelForClose")
		}
		if closeCloseTerminalizeCount != 1 {
			t.Fatalf("closeClose must call TerminalizeAttempt exactly once, got %d", closeCloseTerminalizeCount)
		}
		if gateReplacementTerminalizeCount != 1 {
			t.Fatalf("terminalizeGateReplacement must call TerminalizeAttempt exactly once, got %d", gateReplacementTerminalizeCount)
		}
		if gateReplacementRecordLegCount != 0 {
			t.Fatalf("terminalizeGateReplacement must not call recordBillingLegForAttempt (TerminalizeAttempt owns leg recording), got %d", gateReplacementRecordLegCount)
		}
	})

	t.Run("interleaved_stream_no_raw_attempt_teardown", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "interleaved_stream.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse interleaved_stream.go: %v", err)
		}
		var violations []string
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
			case "cancelAndClose", "finalizeIncurredOrRelease", "releaseBLeg", "appendTerminalLeg":
				pos := fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("interleaved_stream.go:%d: forbidden raw attempt teardown call: %s", pos.Line, sel.Sel.Name))
			case "closeThinkerInner", "closeActiveInner":
				pos := fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("interleaved_stream.go:%d: forbidden helper call: %s", pos.Line, sel.Sel.Name))
			}
			return true
		})
		if len(violations) > 0 {
			t.Fatalf("interleaved_stream.go contains raw attempt teardown calls (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
		}
	})

	t.Run("parallel_race_release_losers_single_terminalize", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(runtimeDir, "parallel_race.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse parallel_race.go: %v", err)
		}
		var violations []string
		// 1. Check forbidden function declarations in parallel_race.go
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				switch fn.Name.Name {
				case "cancelLosers", "releaseBLegs", "terminalizeAttemptEphemeral", "recordParallelBillingLeg":
					pos := fset.Position(fn.Pos())
					violations = append(violations, fmt.Sprintf("parallel_race.go:%d: forbidden function declaration: %s", pos.Line, fn.Name.Name))
				}
			}
		}

		// 2. Check forbidden calls in releaseLosers and whole file
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(fn.Body, func(child ast.Node) bool {
				call, ok := child.(*ast.CallExpr)
				if !ok {
					return true
				}
				callName := ""
				if ident, ok := call.Fun.(*ast.Ident); ok {
					callName = ident.Name
				} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					callName = sel.Sel.Name
				}
				switch callName {
				case "cancelLosers", "releaseBLegs", "recordParallelBillingLeg", "terminalizeAttemptEphemeral":
					pos := fset.Position(call.Pos())
					violations = append(violations, fmt.Sprintf("parallel_race.go:%d: forbidden manual cleanup call in %s: %s", pos.Line, fn.Name.Name, callName))
				case "finalizeIncurredOrRelease", "ReleaseBLeg":
					if fn.Name.Name == "releaseLosers" {
						pos := fset.Position(call.Pos())
						violations = append(violations, fmt.Sprintf("parallel_race.go:%d: direct attempt authority/bleg call in releaseLosers (must use TerminalizeAttempt): %s", pos.Line, callName))
					}
				}
				return true
			})
			return true
		})
		if len(violations) > 0 {
			t.Fatalf("parallel_race.go releaseLosers contains split cleanup calls (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
		}
	})
}

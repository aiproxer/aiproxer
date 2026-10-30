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

// DetectNonLinearizedBackendOpen checks the one production backend-open choke
// point. It follows the context value returned by BeginBLegLaunch through the
// small set of assignments between launch and Open; unrelated methods named
// Open are outside this contract.
func DetectNonLinearizedBackendOpen(root string) ([]string, error) {
	path := filepath.Join(root, "internal", "core", "runtime", "executor_open_attempt.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse executor_open_attempt.go: %w", err)
	}
	return inspectBackendOpenAST(fset, file, "executor_open_attempt.go"), nil
}

func inspectBackendOpenAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "openAttemptTx" {
			continue
		}

		authorityVars := map[string]bool{}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok {
				for i, rhs := range assign.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if !ok || !isBeginBLegLaunch(call) {
						continue
					}
					if len(assign.Lhs) == 0 {
						continue
					}
					id, ok := assign.Lhs[min(i, len(assign.Lhs)-1)].(*ast.Ident)
					if !ok || id.Name == "_" {
						pos := fset.Position(call.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: BeginBLegLaunch context result is discarded before Backend.Open", relPath, pos.Line))
						continue
					}
					authorityVars[id.Name] = true
				}
			}

			if assign, ok := n.(*ast.AssignStmt); ok {
				for i, rhs := range assign.Rhs {
					if !containsAuthorityIdent(rhs, authorityVars) {
						continue
					}
					if i >= len(assign.Lhs) {
						continue
					}
					if id, ok := assign.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
						authorityVars[id.Name] = true
					}
				}
			}

			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Open" || !isBackendReceiver(sel.X) {
				return true
			}

			pos := fset.Position(call.Pos())
			if len(call.Args) == 0 || !containsAuthorityIdent(call.Args[0], authorityVars) {
				violations = append(violations, fmt.Sprintf("%s:%d: Backend.Open context is not derived from BeginBLegLaunch", relPath, pos.Line))
				return true
			}
			return true
		})
	}
	return violations
}

func isBeginBLegLaunch(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "BeginBLegLaunch"
}

func isBackendReceiver(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "be"
}

func containsAuthorityIdent(node ast.Node, names map[string]bool) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && names[id.Name] {
			found = true
			return false
		}
		return !found
	})
	return found
}

// DetectRawRegistrationInRuntime scans runtime production code to ensure all A-leg
// registrations and launch permit commits use ready.lifecycleHandle(), never raw streams or raw sessions.
func DetectRawRegistrationInRuntime(root string) ([]string, error) {
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("read runtime dir: %w", err)
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
			return nil, fmt.Errorf("parse %s: %w", ent.Name(), err)
		}
		violations = append(violations, inspectRegistrationAST(fset, file, ent.Name())...)
	}
	return violations, nil
}

func inspectRegistrationAST(fset *token.FileSet, file *ast.File, relPath string) []string {
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

		if sel.Sel.Name == "RegisterBLeg" {
			pos := fset.Position(call.Pos())
			for _, arg := range call.Args {
				argText := nodeText(arg)
				if strings.Contains(argText, "sess.lifecycleHandle") ||
					strings.Contains(argText, "session.lifecycleHandle") ||
					strings.Contains(argText, "stream") ||
					strings.Contains(argText, "inner") {
					violations = append(violations, fmt.Sprintf("%s:%d: RegisterBLeg called with raw attempt/stream handle (must use ready.lifecycleHandle): %s", relPath, pos.Line, argText))
				}
			}
		}

		if sel.Sel.Name == "Commit" {
			pos := fset.Position(call.Pos())
			recvText := nodeText(sel.X)
			if strings.Contains(recvText, "launchPermit") || strings.Contains(recvText, "permit") {
				if len(call.Args) > 0 {
					argText := nodeText(call.Args[0])
					if !strings.Contains(argText, "ready.lifecycleHandle") {
						violations = append(violations, fmt.Sprintf("%s:%d: LaunchPermit.Commit called with non-ready handle %q (must use ready.lifecycleHandle)", relPath, pos.Line, argText))
					}
				}
			}
		}

		return true
	})
	return violations
}

// Tests for Launch Authority and Registration Ratchets

func TestArch_LaunchAuthority_LinearizedOpen(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectNonLinearizedBackendOpen(root)
	if err != nil {
		t.Fatalf("DetectNonLinearizedBackendOpen failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Launch authority ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_LaunchAuthority_LinearizedOpen_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: an Open method outside the production choke point is unrelated.
	badSourceA := `package runtime
import "context"
func otherFunc(ctx context.Context, be backend) {
	_, _ = be.Open(ctx, nil, nil)
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "other_file.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectBackendOpenAST(fsetA, fileA, "other_file.go")
	if len(violationsA) != 0 {
		t.Fatalf("expected unrelated Open outside openAttemptTx to be ignored, got: %v", violationsA)
	}

	// Fixture B: openAttemptTx calling Backend.Open without BeginBLegLaunch
	badSourceB := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	stream, err := be.Open(ctx, nil, nil)
	return err
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "executor_open_attempt.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectBackendOpenAST(fsetB, fileB, "executor_open_attempt.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (openAttemptTx without BeginBLegLaunch) to be rejected")
	}

	// Fixture C: Valid openAttemptTx with BeginBLegLaunch before Backend.Open
	goodSourceC := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	permitCtx, permit, perr := tx.reqFacts.aScope.BeginBLegLaunch(ctx, tx.bleg.BLegID)
	if perr != nil { return perr }
	stream, err := be.Open(permitCtx, nil, nil)
	return err
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "executor_open_attempt.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectBackendOpenAST(fsetC, fileC, "executor_open_attempt.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got violations: %v", violationsC)
	}

	// Fixture D: a custom backend call in an arbitrary file is unrelated.
	badSourceD := `package runtime
import "context"
func runCustom(ctx context.Context, customBackend any) {
	_, _ = customBackend.Open(ctx, nil, nil)
}`
	fsetD := token.NewFileSet()
	fileD, err := parser.ParseFile(fsetD, "custom.go", badSourceD, 0)
	if err != nil {
		t.Fatalf("parse fixture D: %v", err)
	}
	violationsD := inspectBackendOpenAST(fsetD, fileD, "custom.go")
	if len(violationsD) != 0 {
		t.Fatalf("expected custom Open in custom.go to be ignored, got: %v", violationsD)
	}

	// Fixture E: a discarded launch context must not authorize Backend.Open.
	badSourceE := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	_, _, perr := tx.reqFacts.aScope.BeginBLegLaunch(ctx, tx.bleg.BLegID)
	if perr != nil { return perr }
	_, err := be.Open(ctx, nil, nil)
	return err
}`
	fsetE := token.NewFileSet()
	fileE, err := parser.ParseFile(fsetE, "executor_open_attempt.go", badSourceE, 0)
	if err != nil {
		t.Fatalf("parse fixture E: %v", err)
	}
	violationsE := inspectBackendOpenAST(fsetE, fileE, "executor_open_attempt.go")
	if len(violationsE) == 0 {
		t.Fatal("expected fixture E (discarded BeginBLegLaunch context) to be rejected")
	}
}

func TestArch_LaunchAuthority_IgnoresUnrelatedOpenMethods(t *testing.T) {
	t.Parallel()

	source := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	permitCtx, _, perr := tx.reqFacts.aScope.BeginBLegLaunch(ctx, tx.bleg.BLegID)
	if perr != nil { return perr }
	_, _ = observer.Open(ctx)
	_, err := be.Open(permitCtx, nil, nil)
	return err
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "executor_open_attempt.go", source, 0)
	if err != nil {
		t.Fatalf("parse unrelated Open fixture: %v", err)
	}
	if violations := inspectBackendOpenAST(fset, file, "executor_open_attempt.go"); len(violations) != 0 {
		t.Fatalf("unrelated Open call must be ignored, got: %v", violations)
	}
}

func TestArch_UnpublishedRegistration_ReadyLifecycleHandleOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectRawRegistrationInRuntime(root)
	if err != nil {
		t.Fatalf("DetectRawRegistrationInRuntime failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Unpublished registration ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_UnpublishedRegistration_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: LaunchPermit.Commit called with raw stream
	badSourceA := `package runtime
func (e *Executor) finish(tx *attemptTx) {
	tx.launchPermit.Commit(tx.stream)
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "finish.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectRegistrationAST(fsetA, fileA, "finish.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (Commit with raw stream) to be rejected")
	}

	// Fixture B: RegisterBLeg called with sess.lifecycleHandle directly
	badSourceB := `package runtime
func (e *Executor) reg(aScope *ALeg, sess *attemptSession) {
	aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "b1", Attempt: sess.lifecycleHandle()})
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "reg.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectRegistrationAST(fsetB, fileB, "reg.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (RegisterBLeg with sess.lifecycleHandle) to be rejected")
	}

	// Fixture C: Valid Commit with ready.lifecycleHandle()
	goodSourceC := `package runtime
func (e *Executor) finish(tx *attemptTx, ready *readyAttempt) {
	tx.launchPermit.Commit(ready.lifecycleHandle())
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "finish.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectRegistrationAST(fsetC, fileC, "finish.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got violations: %v", violationsC)
	}

	// Fixture D: RegisterBLeg called with raw stream handle directly
	badSourceD := `package runtime
func (e *Executor) regStream(aScope *ALeg, stream aipapi.ManagedEventStream) {
	aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "b1", Attempt: stream})
}`
	fsetD := token.NewFileSet()
	fileD, err := parser.ParseFile(fsetD, "reg_stream.go", badSourceD, 0)
	if err != nil {
		t.Fatalf("parse fixture D: %v", err)
	}
	violationsD := inspectRegistrationAST(fsetD, fileD, "reg_stream.go")
	if len(violationsD) == 0 {
		t.Fatal("expected fixture D (RegisterBLeg with raw stream) to be rejected")
	}
}

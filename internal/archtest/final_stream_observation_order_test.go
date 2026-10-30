package archtest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	runFinalStreamObservationStage = "RunFinalStreamObservationStage"
	emitClientFacingObserved       = "emitClientFacingObserved"
)

const dispatchClientFacingEventName = "dispatchClientFacingEvent"

// firstLocalCallPos finds the first matching call in body while skipping nested
// function-literal bodies. Defining a closure does not execute its body, so a
// sibling closure's call cannot establish execution order relative to the
// statements around it. Only the coordinator's own statements count here.
//
// This is deliberately a LOCAL walker. The shared firstCallPos recursively
// inspects everything (see attempt_transform_open_order_test.go) and is used
// unchanged by the other architecture guards; widening or narrowing it here
// would silently change their scope.
func firstLocalCallPos(body *ast.BlockStmt, match func(pkg, name string, call *ast.CallExpr) bool) token.Pos {
	var pos token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if pos != 0 {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			// Skip the whole closure body: its statements belong to the closure,
			// not to this coordinator's execution order.
			return false
		case *ast.CallExpr:
			pkg, name := qualifiedCall(node.Fun)
			if match(pkg, name, node) {
				pos = node.Pos()
				return false
			}
		}
		return true
	})
	return pos
}

// recvDispatchCoordinator extracts the one top-level assignment of
// dispatchClientFacingEvent to a function literal from Recv's own body. Exactly
// one definition is required: a missing, duplicated, or non-literal definition is
// an explicit failure, never a silently skipped check.
//
// The ordinary client-facing dispatch lives in this closure. The approved
// private pending-completion drain is a separate sibling closure in Recv, and it
// runs the mandatory recorder and fail-closed final observer during preflight
// before installation/activation, then records `recorded`/`finalObserved` so the
// observation never repeats at release. That ordering is covered behaviorally by
// the runtime pending-completion drain and lifetime tests; it is not a second
// raw-event transform path, so it must not be compared against this coordinator.
// assignmentBindsCoordinator reports whether any LHS position of an assignment is
// a plain identifier naming the coordinator. Assignments without a matching
// identifier are unrelated to the coordinator contract and are ignored.
func assignmentBindsCoordinator(def *ast.AssignStmt) bool {
	for _, lhs := range def.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if ok && ident.Name == dispatchClientFacingEventName {
			return true
		}
	}
	return false
}

func recvDispatchCoordinator(fn *ast.FuncDecl) (*ast.FuncLit, error) {
	if fn == nil || fn.Body == nil {
		return nil, errors.New("Recv declaration not found")
	}
	var found *ast.FuncLit
	count := 0
	for _, stmt := range fn.Body.List {
		switch def := stmt.(type) {
		case *ast.AssignStmt:
			// Every LHS position is inspected for the coordinator name BEFORE the
			// single-target shape restriction, so a multi-target assignment that
			// rebinds the coordinator is an explicit shape failure rather than a
			// silently skipped statement that would leave a stale literal accepted.
			if !assignmentBindsCoordinator(def) {
				continue
			}
			if len(def.Lhs) != 1 || len(def.Rhs) != 1 {
				return nil, fmt.Errorf("%s must be assigned a function literal by a single-target top-level assignment", dispatchClientFacingEventName)
			}
			count++
			lit, ok := def.Rhs[0].(*ast.FuncLit)
			if !ok {
				return nil, fmt.Errorf("%s must be assigned a function literal", dispatchClientFacingEventName)
			}
			found = lit
		case *ast.DeclStmt:
			// A var/const binding of the coordinator name is a definition that is
			// not the required top-level literal assignment, so it is reported as
			// such instead of silently counting as absent.
			gen, ok := def.Decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.VAR && gen.Tok != token.CONST) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name.Name == dispatchClientFacingEventName {
						return nil, fmt.Errorf("%s must be assigned a function literal", dispatchClientFacingEventName)
					}
				}
			}
		}
	}
	switch {
	case count == 0:
		return nil, fmt.Errorf("Recv must define %s exactly once as a top-level assignment", dispatchClientFacingEventName)
	case count > 1:
		return nil, fmt.Errorf("Recv defines %s %d times, want exactly one coordinator", dispatchClientFacingEventName, count)
	case found == nil || found.Body == nil:
		return nil, fmt.Errorf("%s has no body to inspect", dispatchClientFacingEventName)
	}
	return found, nil
}

// recvDispatchSpec names the required coordinator calls and the strict order the
// actual dispatch must satisfy. Every entry is required: a missing call and a
// misordered call are both defects.
type recvDispatchSpec struct {
	owner string
	calls []string
}

// validateRecvDispatchOrder is the error-returning form of the three
// recv-coordinator ordering ratchets, so miniature parsed fixtures can assert the
// validator's verdict rather than only that a file parses.
func validateRecvDispatchOrder(fn *ast.FuncDecl, spec recvDispatchSpec) error {
	lit, err := recvDispatchCoordinator(fn)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.owner, err)
	}
	positions := make([]token.Pos, 0, len(spec.calls))
	for _, name := range spec.calls {
		pos := firstLocalCallPos(lit.Body, func(_, callName string, _ *ast.CallExpr) bool { return callName == name })
		if pos == 0 {
			return fmt.Errorf("%s: %s must call %s inside the %s coordinator", spec.owner, fn.Name.Name, name, dispatchClientFacingEventName)
		}
		positions = append(positions, pos)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			return fmt.Errorf("%s: %s must call %s before %s inside the %s coordinator; %s=%d %s=%d",
				spec.owner, fn.Name.Name, spec.calls[i-1], spec.calls[i], dispatchClientFacingEventName,
				spec.calls[i-1], positions[i-1], spec.calls[i], positions[i])
		}
	}
	return nil
}

func firstObservationEmitPos(body *ast.BlockStmt) (obsPos, emitPos token.Pos) {
	obsPos = firstCallPos(body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runFinalStreamObservationStage || name == emitClientFacingObserved
	})
	emitPos = firstCallPos(body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "emitTrafficPTCFinal" || name == emitClientFacingObserved
	})
	return obsPos, emitPos
}

func parseRuntimeFunction(t *testing.T, root, filename, name string) *ast.FuncDecl {
	t.Helper()
	path := filepath.Join(root, "internal", "core", "runtime", filename)
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	fn := findFunc(f, name)
	if fn == nil {
		t.Fatalf("%s not found in %s", name, filename)
	}
	return fn
}

func assertResponseObservationOrder(t *testing.T, fn *ast.FuncDecl) (obsPos, emitPos, rememberPos token.Pos) {
	t.Helper()
	obsPos, emitPos = firstObservationEmitPos(fn.Body)
	rememberPos = firstCallPos(fn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "rememberClientEvent"
	})
	if obsPos == 0 {
		t.Fatalf("%s must call %s before response output side effects", fn.Name.Name, runFinalStreamObservationStage)
	}
	if emitPos == 0 {
		t.Fatalf("%s must emit response traffic after final observation", fn.Name.Name)
	}
	if rememberPos == 0 {
		t.Fatalf("%s must remember client events after final observation", fn.Name.Name)
	}
	if obsPos >= emitPos || obsPos >= rememberPos {
		t.Fatalf("want observation < emit/remember; obs=%d emit=%d remember=%d", obsPos, emitPos, rememberPos)
	}
	return obsPos, emitPos, rememberPos
}

func TestResponsePipeline_transformObserveFinalStreamObservationAfterHooksBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	transform := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "transformClientEvent")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	hooksPos := firstCallPos(transform.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "RunResponsePartHooks" })
	assertResponseObservationOrder(t, observe)
	if hooksPos == 0 {
		t.Fatal("transformClientEvent must call RunResponsePartHooks")
	}
	// The ordinary dispatch coordinator runs the response hooks before delegating
	// observation. Private drains are separate sibling closures and are covered by
	// the pending-completion preflight/activation behavior tests.
	if err := validateRecvDispatchOrder(recv, recvDispatchSpec{
		owner: dispatchClientFacingEventName,
		calls: []string{"transformClientEvent", "observeClientFacing"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponsePipeline_gatedObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	gate := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "applyCompletionGates")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	gatePos := firstCallPos(gate.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "completionGatedEmit" })
	preflightPos := firstCallPos(gate.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "recordClientFacing" })
	assertResponseObservationOrder(t, observe)
	if gatePos == 0 {
		t.Fatal("applyCompletionGates must resolve gates before client observation")
	}
	if preflightPos == 0 || gatePos >= preflightPos {
		t.Fatalf("want gate-resolution < gate preflight; gate=%d preflight=%d", gatePos, preflightPos)
	}
	if err := validateRecvDispatchOrder(recv, recvDispatchSpec{
		owner: dispatchClientFacingEventName,
		calls: []string{"applyCompletionGates", "observeClientFacing"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecv_responseFinishedObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	obsPos, emitPos, rememberPos := assertResponseObservationOrder(t, observe)
	if err := validateRecvDispatchOrder(recv, recvDispatchSpec{
		owner: dispatchClientFacingEventName,
		calls: []string{"finalizeResponseFinishedAuthority", "observeClientFacing"},
	}); err != nil {
		t.Fatal(err)
	}
	if obsPos >= emitPos || obsPos >= rememberPos {
		t.Fatalf("response_finished observation must precede output and remember; obs=%d emit=%d remember=%d", obsPos, emitPos, rememberPos)
	}
}

func TestRuntimeFinalStreamOutcomes_freezeGateReplacedAndSuccessReleased(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: intentional lightweight AST scan of one package dir
	pkgs, err := parser.ParseDir(fset, path, func(info os.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	var sawGateReplaced, sawSuccessReleased bool
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				switch sel.Sel.Name {
				case "OutcomeGateReplaced":
					sawGateReplaced = true
				case "OutcomeSuccessReleased":
					sawSuccessReleased = true
				}
				return true
			})
		}
	}
	if !sawGateReplaced {
		t.Error("RED: runtime must finalize gate-replaced observers with response.OutcomeGateReplaced")
	}
	if !sawSuccessReleased {
		t.Error("RED: runtime must finalize successful release with response.OutcomeSuccessReleased after response_finished")
	}
}

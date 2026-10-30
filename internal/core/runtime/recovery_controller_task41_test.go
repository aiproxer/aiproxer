package runtime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
)

func TestTask41RecoveryBoundaryRejectsUnretiredPriorBeforeOpen(t *testing.T) {
	var opens atomic.Int32
	r := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	prior := &attemptSession{authority: newAuthorityLifecycle(nil, nil, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(1),
		admissionResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true},
	}, authorityCandidate())}
	_, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), prior, false)
	if !errors.Is(err, errRecoveryPriorAttemptNotRetired) {
		t.Fatalf("openReplacement error = %v, want %v", err, errRecoveryPriorAttemptNotRetired)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("opener calls for unsettled prior = %d, want 0", got)
	}

	settled := &attemptSession{authority: authorityLifecycle{
		control: &authorityLifecycleControl{terminal: authorityTerminalReleased},
	}}
	if _, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), settled, false); err != nil {
		t.Fatalf("settled prior replacement: %v", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opener calls for settled prior = %d, want 1", got)
	}
}

func TestTask41RecoveryBoundaryRejectsCommittedTurnBeforeOpen(t *testing.T) {
	var opens atomic.Int32
	r := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	terminal := newTurnTerminal()
	terminal.markCommitted(nil)
	_, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), nil, terminal.committed())
	if !errors.Is(err, errRecoveryTurnCommitted) {
		t.Fatalf("openReplacement error = %v, want %v", err, errRecoveryTurnCommitted)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("opener calls for committed turn = %d, want 0", got)
	}
}

func TestTask41RecoveryOwnershipShape(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(sourceFile)

	streamFields := structFields(t, filepath.Join(dir, "executor_retry_stream.go"), "retryRecvStream")
	for _, field := range []string{
		"budget", "ttft", "sel", "requestSize", "session", "excluded", "rng",
		"lastHardReject", "lastHardTransportReject", "lastAdmissionErr",
		"isContextLimitExhaustion", "transformExcludes", "affinityKey", "affinitySet",
		"affinityCommitOnce", "recoverPolicy", "interleaved", "suppressThinker",
		"suppressVisibleMemo", "lastParallelFailure",
	} {
		if streamFields[field] {
			t.Errorf("retryRecvStream still directly owns recovery field %q", field)
		}
	}

	recoveryFields := structFields(t, filepath.Join(dir, "recovery_controller.go"), "recoveryController")
	for _, field := range []string{
		"budget", "ttft", "sel", "requestSize", "session", "excluded", "rng",
		"affinityKey", "affinitySet",
		"affinityCommitOnce", "recoverPolicy", "interleaved", "suppressThinker",
		"suppressVisibleMemo",
	} {
		if !recoveryFields[field] {
			t.Errorf("recoveryController does not own recovery field %q", field)
		}
	}

	adapterPath := filepath.Join(dir, "recovery_controller.go")
	adapterSource, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatalf("read recovery adapter: %v", err)
	}
	if !containsIdent(t, adapterPath, "replacementOpenRequest") || !containsIdent(t, adapterPath, "replacementOpenResult") {
		t.Fatal("recovery adapter must define the D10 replacement request/result seam")
	}
	_ = adapterSource
}

func structFields(t *testing.T, path, typeName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	fields := make(map[string]bool)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != typeName {
				continue
			}
			st, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a struct in %s", typeName, path)
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					fields[name.Name] = true
				}
			}
		}
	}
	return fields
}

func containsIdent(t *testing.T, path, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if ok && ident.Name == name {
			found = true
			return false
		}
		return !found
	})
	return found
}

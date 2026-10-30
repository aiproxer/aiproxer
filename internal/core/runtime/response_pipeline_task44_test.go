package runtime

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func task44ParseRuntimeFile(t *testing.T, name string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file
}

func task44TypeFields(t *testing.T, file *ast.File, typeName string) map[string]bool {
	t.Helper()
	fields := make(map[string]bool)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
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

func task44ReceiverMethods(t *testing.T, file *ast.File, receiver string) map[string]bool {
	t.Helper()
	methods := make(map[string]bool)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name == nil {
			continue
		}
		if task44ExprName(fn.Recv.List[0].Type) == receiver {
			methods[fn.Name.Name] = true
		}
	}
	return methods
}

func task44ExprName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return task44ExprName(expr.X)
	case *ast.SelectorExpr:
		return expr.Sel.Name
	default:
		return ""
	}
}

func TestTask44LogicalToolStateHasOneResponseOwnerAndAttemptAssembler(t *testing.T) {
	response := task44ParseRuntimeFile(t, "response_pipeline.go")
	stream := task44ParseRuntimeFile(t, "executor_retry_stream.go")
	attempt := task44ParseRuntimeFile(t, "attempt_session.go")

	responseFields := task44TypeFields(t, response, "responsePipeline")
	for _, field := range []string{"toolClass", "committedTools", "compactionOpenMeta"} {
		if !responseFields[field] {
			t.Errorf("responsePipeline does not own logical-stream field %s", field)
		}
	}
	streamFields := task44TypeFields(t, stream, "retryRecvStream")
	for _, field := range []string{"toolClass", "committedTools", "compactionOpenMeta", "secureRecvRecordingHardStop"} {
		if streamFields[field] {
			t.Errorf("retryRecvStream still owns migrated logical-stream field %s", field)
		}
	}
	attemptFields := task44TypeFields(t, attempt, "attemptSession")
	if !attemptFields["toolFinal"] {
		t.Fatal("attemptSession lost the attempt-local tool assembler/finalizer")
	}
}

func TestTask44RecordingIsTypedAndNoFacadeRecordingAuthorityRemains(t *testing.T) {
	recording := task44ParseRuntimeFile(t, "secure_session_stream_record.go")
	response := task44ParseRuntimeFile(t, "response_pipeline.go")
	stream := task44ParseRuntimeFile(t, "executor_retry_stream.go")
	fields := task44TypeFields(t, recording, "responseRecordingResult")
	if !fields["outcome"] || !fields["err"] {
		t.Fatalf("responseRecordingResult fields = %#v, want typed outcome and error", fields)
	}
	streamFields := task44TypeFields(t, stream, "retryRecvStream")
	if streamFields["secureRecvRecordingHardStop"] {
		t.Fatal("retryRecvStream retains a duplicate secure-recording hard-stop truth")
	}
	responseFields := task44TypeFields(t, response, "responsePipeline")
	if responseFields["recordingErr"] {
		t.Error("responsePipeline retains duplicate recording error truth instead of only the typed outcome")
	}
	methods := task44ReceiverMethods(t, stream, "retryRecvStream")
	for _, forbidden := range []string{"beforeEmitClientFacing", "mandatoryClientFacingPreflight", "emitTrafficBTP"} {
		if methods[forbidden] {
			t.Errorf("retryRecvStream retains forwarding-only response method %s", forbidden)
		}
	}
}

func TestTask44PromptCacheAndFinalObserverRemainAttemptLocal(t *testing.T) {
	attempt := task44ParseRuntimeFile(t, "attempt_session.go")
	fields := task44TypeFields(t, attempt, "attemptSession")
	for _, field := range []string{"promptCacheSource", "promptCacheController", "finalStreamObs"} {
		if !fields[field] {
			t.Errorf("attemptSession no longer owns attempt-local field %s", field)
		}
	}
	response := task44ParseRuntimeFile(t, "response_pipeline.go")
	responseFields := task44TypeFields(t, response, "responsePipeline")
	for _, field := range []string{"promptCacheSource", "promptCacheController", "finalStreamObs"} {
		if responseFields[field] {
			t.Errorf("responsePipeline incorrectly owns attempt-local field %s", field)
		}
	}
}

func TestTask44ResponsePipelineHasNoTerminalMutationPath(t *testing.T) {
	for _, path := range []string{
		"response_pipeline.go",
		"response_pipeline_observations.go",
		"executor_final_stream_obs.go",
		"executor_compaction.go",
		"secure_session_stream_record.go",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, forbidden := range []string{"terminalizeSnapshot", "markFinished", "markCommitted", "endALeg", "handoffBillingTurn"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("response owner file %s contains terminal mutation %q", path, forbidden)
			}
		}
	}
}

func TestTask44ObservationBoundaryIsResponseOwned(t *testing.T) {
	file := task44ParseRuntimeFile(t, "response_pipeline_observations.go")
	methods := task44ReceiverMethods(t, file, "responsePipeline")
	if !methods["observeClientFacing"] {
		t.Fatal("responsePipeline has no cohesive emitted-event observation boundary")
	}
}

func TestResponsePipeline_ObserveSynthesizedUsage_NilReceiver(t *testing.T) {
	t.Parallel()
	var p *responsePipeline
	_, _, err := p.observeSynthesizedUsage(t.Context(), aipapi.Event{}, requestTerminalFacts{}, nil, sdk.PartMeta{}, false)
	if !errors.Is(err, errNilRetryRecvStream) {
		t.Fatalf("got %v, want %v", err, errNilRetryRecvStream)
	}
}

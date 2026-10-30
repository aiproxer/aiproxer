package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
)

func TestResponsePipelineSnapshotIsCoherentWhileTerminalReads(t *testing.T) {
	p := newResponsePipeline()
	terminal := newTurnTerminal()
	terminal.markCommitted(nil)
	terminal.accountingFinalizedState.Store(true)
	p.bindTerminalSnapshot(func() (bool, bool) {
		return terminal.committed(), terminal.accountingFinalized()
	})
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventTextDelta, Delta: "b"},
	} {
		p.rememberClientEvent(ev)
	}

	var wg sync.WaitGroup
	started := make(chan struct{})
	wg.Go(func() {
		close(started)
		for range 250 {
			p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
		}
	})
	<-started

	for range 250 {
		snap := p.accumulatorSnapshot()
		var wire struct {
			Events int    `json:"e"`
			Text   string `json:"t"`
			Final  bool   `json:"f"`
		}
		if err := json.Unmarshal(snap.Bytes(), &wire); err != nil {
			t.Fatalf("decode coherent snapshot: %v", err)
		}
		if wire.Events != len(wire.Text) {
			t.Fatalf("torn response snapshot: events=%d text=%q", wire.Events, wire.Text)
		}
		if !snap.OutputCommitted() || !wire.Final {
			t.Fatalf("snapshot lost terminal evidence: committed=%v final=%v", snap.OutputCommitted(), wire.Final)
		}
	}
	wg.Wait()
	if got := p.accumulatorSnapshot(); !got.OutputCommitted() {
		t.Fatal("final snapshot lost terminal commitment")
	}
}

func TestResponsePipelineSnapshotDoesNotHoldResponseLockWhileReadingTerminalFlags(t *testing.T) {
	p := newResponsePipeline()
	p.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	p.bindTerminalSnapshot(func() (bool, bool) {
		// A terminal owner may inspect already-published response evidence while
		// exposing its flags. The response snapshot must not nest its lock across
		// that owner boundary.
		_ = p.seenEventsCopy()
		return true, true
	})

	done := make(chan struct{})
	go func() {
		_ = p.accumulatorSnapshot()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("response snapshot held its lock while reading terminal flags")
	}
}

func TestResponsePipelineKeepsOperatorEvidenceDistinctAndAttemptOwnsDedupe(t *testing.T) {
	p := newResponsePipeline()
	if _, duplicated := reflect.TypeFor[responsePipeline]().FieldByName("internalUsageKeys"); duplicated {
		t.Fatal("response pipeline duplicated attempt-local usage dedupe truth")
	}
	provider := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  90,
		OutputTokens: 10,
		TotalTokens:  100,
		Accounting: aipapi.UsageAccountingMetadata{
			DedupeKey: "provider-1",
			Plane:     aipapi.UsagePlaneProviderBillable,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	customer := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 3, TotalTokens: 5}

	firstAttempt := &attemptSession{}
	if !firstAttempt.rememberUsageEvidenceOnce(provider) || firstAttempt.rememberUsageEvidenceOnce(provider) {
		t.Fatal("provider evidence dedupe did not suppress a repeated key")
	}
	p.rememberInternalUsage(provider)
	p.setLastAuthorityUsage(provider)
	p.setLastCustomerUsage(customer)

	operator := p.operatorUsageForFinalize()
	if operator.InputTokens != 90 || operator.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("operator evidence = %+v, want provider evidence", operator)
	}
	visible := p.lastCustomerUsageSnapshot()
	if visible.InputTokens != 2 || visible.OutputTokens != 3 || visible.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
		t.Fatalf("customer evidence = %+v, want reconstructed customer usage", visible)
	}
	if got := len(p.usageEventsSnapshot()); got != 1 {
		t.Fatalf("deduped internal usage count = %d, want 1", got)
	}

	replacementAttempt := &attemptSession{}
	if !replacementAttempt.rememberUsageEvidenceOnce(provider) {
		t.Fatal("replacement should get a fresh per-attempt evidence dedupe scope")
	}
}

func TestResponsePipelinePreservesGateAndRecoveryDrainOrder(t *testing.T) {
	p := newResponsePipeline()
	buffered := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "buffered"}
	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	if _, _, err := p.completionGatedEmit(context.Background(), nil, buffered, responseGateInput{limits: completion.DefaultBufferLimits()}); !errors.Is(err, errGateContinueInner) {
		t.Fatalf("first gated event error = %v, want buffering sentinel", err)
	}
	out, replaced, err := p.completionGatedEmit(context.Background(), nil, finish, responseGateInput{limits: completion.DefaultBufferLimits()})
	if err != nil || replaced || !reflect.DeepEqual(out, buffered) {
		t.Fatalf("gate output = %+v, replaced=%v, err=%v; want buffered event", out, replaced, err)
	}
	p.setRecoveryDrain([]aipapi.Event{{Kind: aipapi.EventWarning, WarningCode: "recover"}})
	wantRecovery := aipapi.Event{Kind: aipapi.EventWarning, WarningCode: "recover"}
	if got, ok := p.popRecoveryDrain(); !ok || !reflect.DeepEqual(got, wantRecovery) {
		t.Fatalf("recovery drain = %+v, %v; want warning", got, ok)
	}
	if got, ok := p.popGateDrain(); !ok || !reflect.DeepEqual(got, finish) {
		t.Fatalf("gate drain = %+v, %v; want finish after buffered text", got, ok)
	}
	if _, ok := p.popRecoveryDrain(); ok {
		t.Fatal("recovery drain retained an event after ordered consumption")
	}
	if _, ok := p.popGateDrain(); ok {
		t.Fatal("gate drain retained an event after ordered consumption")
	}
}

func TestTask43FacadeDoesNotRetainResponsePipelineStateOrForwarders(t *testing.T) {
	path := "executor_retry_stream.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenFields := map[string]bool{
		"seenEvents": true, "visibleText": true, "customer": true,
		"gateBuf": true, "gateDrain": true, "gateLive": true,
		"recoverDrain": true, "lastAuthorityUsage": true, "lastCustomerUsage": true,
		"eventsMu": true,
	}
	forbiddenMethods := map[string]bool{
		"rememberClientEvent": true, "usageEventsSnapshot": true,
		"seenEventsCopy": true, "clearClientAccumulators": true,
		"accumulatorSnapshot": true, "completionGatedEmit": true,
		"popGateDrainHead": true, "emitGateDrained": true,
		"operatorUsageForFinalize": true, "usageEvidenceOrEmpty": true,
		"resolveCustomerUsage": true, "customerUsageFromReleased": true,
		"releasedOutputText": true,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if fn.Name != nil && forbiddenMethods[fn.Name.Name] && strings.Contains(formatNode(fset, fn.Recv.List[0].Type), "retryRecvStream") {
			t.Errorf("response-pipeline helper %s remains on retryRecvStream", fn.Name.Name)
		}
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "retryRecvStream" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if forbiddenFields[name.Name] {
						t.Errorf("response-pipeline field %s remains on retryRecvStream", name.Name)
					}
				}
			}
		}
	}
	ownerRefs := 0
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "retryRecvStream" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				if (len(field.Names) == 0 && strings.Contains(formatNode(fset, field.Type), "*responsePipeline")) ||
					(len(field.Names) == 1 && field.Names[0].Name == "responsePipeline" && strings.Contains(formatNode(fset, field.Type), "responsePipeline")) {
					ownerRefs++
				}
			}
		}
	}
	if ownerRefs != 1 {
		t.Fatalf("retryRecvStream response owner references = %d, want exactly one", ownerRefs)
	}
	owner, err := parser.ParseFile(fset, "response_pipeline.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOwnerFields := map[string]bool{
		"customer": true, "seenEvents": true, "visibleText": true,
		"gateBuf": true, "gateDrain": true,
		"gateLive": true, "recoverDrain": true,
		"lastAuthorityUsage": true, "lastCustomerUsage": true, "eventsMu": true,
	}
	ownerCounts := make(map[string]int)
	for _, decl := range owner.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "responsePipeline" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if wantOwnerFields[name.Name] {
						ownerCounts[name.Name]++
					}
				}
			}
		}
	}
	for name := range wantOwnerFields {
		if ownerCounts[name] != 1 {
			t.Errorf("response-pipeline field %s count=%d, want exactly one owner", name, ownerCounts[name])
		}
	}
}

func TestTask43ResponseStateMutatesOnlyThroughOwner(t *testing.T) {
	if _, delegatedTerminalMutation := reflect.TypeFor[responsePipeline]().FieldByName("gateDrainHook"); delegatedTerminalMutation {
		t.Fatal("response pipeline retains a callback that can mutate terminal authority")
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "response_pipeline.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		forbidden := map[string]bool{"recoverDrain": true, "gateBuf": true, "gateDrain": true, "gateLive": true, "seenEvents": true, "visibleText": true, "lastAuthorityUsage": true, "lastCustomerUsage": true}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel != nil && forbidden[selector.Sel.Name] {
				t.Errorf("%s directly accesses response-owned field %s", name, selector.Sel.Name)
			}
			return true
		})
	}
}

func formatNode(fset *token.FileSet, node ast.Node) string {
	start := fset.Position(node.Pos())
	end := fset.Position(node.End())
	data, _ := os.ReadFile("executor_retry_stream.go")
	if start.Offset >= 0 && end.Offset <= len(data) && start.Offset <= end.Offset {
		return string(data[start.Offset:end.Offset])
	}
	return ""
}

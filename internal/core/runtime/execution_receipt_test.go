package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// receiptTestExecutor builds a real executor over a recording backend and a
// trusted billing identity whose store identity is a fixture value. The
// canonical call deliberately carries no identity fields.
func receiptTestExecutor(t *testing.T) (*Executor, *int) {
	t.Helper()
	principalID := "principal-receipt"
	ex := newReasoningCompressorExecutor(t, &reasoningCompressorCapture{}, "acct:"+principalID)
	ex.BillingIdentity.StoreID = func(context.Context) string { return "store-receipt" }
	opens := new(int)
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				(*opens)++
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	return ex, opens
}

func receiptTestCall() *aipapi.Call {
	return &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "backend:aux-model"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("auxiliary request")}}},
	}
}

// TestExecutor_ExecuteWithReceiptCarriesTheHostOperationIdentity pins the
// neutral lexical receipt seam: the receipt carries the host-stamped store and
// billing-call identity plus the trusted scope and detached role, and it is
// published before the backend open. The canonical call cannot supply any of
// those values, and the ordinary Execute path is unchanged.
func TestExecutor_ExecuteWithReceiptCarriesTheHostOperationIdentity(t *testing.T) {
	ex, opens := receiptTestExecutor(t)
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-receipt"),
		TenantID:    scope.Known("tenant-receipt"),
		Origin:      scope.OriginClient,
	})
	ctx = execctx.WithDetachedSession(ctx, execctx.DetachedSession{AuxiliaryRole: "proxy_memory_phase1"})
	call := receiptTestCall()
	// Poisoned request fields must never become the receipt identity.
	call.ID = "request-supplied-call"

	stream, receipt, err := ex.ExecuteWithReceipt(ctx, call)
	if err != nil {
		t.Fatalf("ExecuteWithReceipt: %v", err)
	}
	if _, err := aipapi.Collect(ctx, stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if *opens != 1 {
		t.Fatalf("backend opens = %d, want exactly one", *opens)
	}
	if !receipt.Available() {
		t.Fatal("an admitted auxiliary execution must publish an available receipt")
	}
	if receipt.StoreID != "store-receipt" {
		t.Fatalf("receipt store = %q, want the host store identity", receipt.StoreID)
	}
	if receipt.BillingCallID == "" || receipt.BillingCallID == call.ID {
		t.Fatalf("receipt call = %q, want a host-minted identity distinct from the request id %q", receipt.BillingCallID, call.ID)
	}
	if receipt.TenantID != "tenant-receipt" || receipt.PrincipalID != "principal-receipt" {
		t.Fatalf("receipt scope = %q/%q, want the trusted scope", receipt.TenantID, receipt.PrincipalID)
	}
	if receipt.Role != "proxy_memory_phase1" {
		t.Fatalf("receipt role = %q, want the trusted detached role", receipt.Role)
	}

	// The ordinary path is the same canonical implementation and still works.
	plain, err := ex.Execute(ctx, receiptTestCall())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := aipapi.Collect(ctx, plain); err != nil {
		t.Fatalf("collect plain: %v", err)
	}
	if *opens != 2 {
		t.Fatalf("backend opens after ordinary Execute = %d, want two total", *opens)
	}
}

// TestExecutor_ExecuteWithReceiptOmitsAPreAdmissionReceipt pins the honest
// absence rule: a pre-admission refusal opens no backend and returns no linked
// receipt, so a caller cannot certify a fact against an operation that never
// happened.
func TestExecutor_ExecuteWithReceiptOmitsAPreAdmissionReceipt(t *testing.T) {
	ex, opens := receiptTestExecutor(t)
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error {
		return errors.New("credit refused")
	})
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-receipt"),
		Origin:      scope.OriginClient,
	})
	ctx = execctx.WithDetachedSession(ctx, execctx.DetachedSession{AuxiliaryRole: "proxy_memory_phase1"})

	_, receipt, err := ex.ExecuteWithReceipt(ctx, receiptTestCall())
	if err == nil {
		t.Fatal("the pre-admission credit refusal must be returned")
	}
	if receipt.Available() {
		t.Fatalf("pre-admission receipt = %+v, want an unavailable receipt", receipt)
	}
	if *opens != 0 {
		t.Fatalf("backend opens = %d, want zero", *opens)
	}
}

// TestExecutor_ExecuteWithReceiptLeavesAnIncompleteStoreLinkUnavailable pins
// the honest incomplete-identity rule: a host without a durable store identity
// still executes (usage-only behaviour is preserved), but the returned receipt
// is explicitly unavailable and never names a fabricated store.
func TestExecutor_ExecuteWithReceiptLeavesAnIncompleteStoreLinkUnavailable(t *testing.T) {
	ex, opens := receiptTestExecutor(t)
	ex.BillingIdentity.StoreID = func(context.Context) string { return "" }
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-receipt"),
		Origin:      scope.OriginClient,
	})
	ctx = execctx.WithDetachedSession(ctx, execctx.DetachedSession{AuxiliaryRole: "proxy_memory_phase1"})

	stream, receipt, err := ex.ExecuteWithReceipt(ctx, receiptTestCall())
	if err != nil {
		t.Fatalf("ExecuteWithReceipt: %v", err)
	}
	if _, err := aipapi.Collect(ctx, stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if *opens != 1 {
		t.Fatalf("backend opens = %d, want exactly one", *opens)
	}
	if receipt.Available() {
		t.Fatalf("incomplete store receipt = %+v, want an unavailable link", receipt)
	}
	if receipt.StoreID != "" {
		t.Fatalf("receipt store = %q, want no fabricated store identity", receipt.StoreID)
	}
	if receipt.BillingCallID == "" {
		t.Fatal("the runtime still minted its billing call identity")
	}
}

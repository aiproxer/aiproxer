package auxreq_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// receiptRunner is a generation runner that publishes the host operation
// receipt the runtime executor is expected to produce. It implements the same
// lexical optional method as *runtime.Executor without importing it.
type receiptRunner struct {
	receipt execctx.ExecutionReceipt
}

func (r *receiptRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return finishedStream(), nil
}

func (r *receiptRunner) ExecuteWithReceipt(context.Context, *aipapi.Call) (aipapi.EventStream, execctx.ExecutionReceipt, error) {
	return finishedStream(), r.receipt, nil
}

func receiptFixture() execctx.ExecutionReceipt {
	return execctx.ExecutionReceipt{
		StoreID:       "store-receipt",
		BillingCallID: "call-receipt",
		TenantID:      "tenant-receipt",
		PrincipalID:   "principal-receipt",
		Role:          "proxy_memory_phase1",
	}
}

// TestBackgroundScheduler_AwaitWithReceiptReturnsTheExecutorReceiptAtomically
// pins the scheduler-owned receipt: the receipt returned with a result is the
// host receipt the runtime executor published for that operation, not a value
// derived from the auxiliary request or the worker's coalesce key. A coalesced
// submit observes the same job receipt, and the ordinary Await result is
// unchanged.
func TestBackgroundScheduler_AwaitWithReceiptReturnsTheExecutorReceiptAtomically(t *testing.T) {
	t.Parallel()
	runner := &receiptRunner{receipt: receiptFixture()}
	s := newBackground(context.Background(), t, nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	client := s.BindRunner(runner)
	waiter, ok := client.(auxreq.ReceiptAwaiter)
	if !ok {
		t.Fatal("the generation-bound client must preserve the optional receipt capability")
	}

	first, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "receipt-same-job"})
	if err != nil {
		t.Fatal(err)
	}
	collected, got, err := waiter.AwaitWithReceipt(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution != receiptFixture() {
		t.Fatalf("receipt = %+v, want the executor receipt %+v", got.Execution, receiptFixture())
	}
	if got.CompletedAt.IsZero() {
		t.Fatal("the scheduler must retain an immutable completion time with the receipt")
	}
	if collected.TerminalError != nil {
		t.Fatalf("collected terminal error = %+v, want a clean collection", collected.TerminalError)
	}

	coalesced, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "receipt-same-job"})
	if err != nil {
		t.Fatal(err)
	}
	if coalesced != first {
		t.Fatalf("coalesced job = %q, want %q", coalesced, first)
	}
	_, again, err := waiter.AwaitWithReceipt(context.Background(), coalesced)
	if err != nil {
		t.Fatal(err)
	}
	if again.Execution != got.Execution || !again.CompletedAt.Equal(got.CompletedAt) {
		t.Fatalf("coalesced receipt = %+v/%v, want the same job receipt %+v/%v",
			again.Execution, again.CompletedAt, got.Execution, got.CompletedAt)
	}

	plain, err := client.Await(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if plain.TerminalError != nil {
		t.Fatalf("Await terminal error = %+v, want the same clean collection", plain.TerminalError)
	}
}

// TestBackgroundScheduler_LegacyRunnerReturnsAnUnavailableReceipt pins the
// compatibility rule: an ordinary Execute-only runner keeps working, and the
// missing receipt is explicit (zero value), never fabricated from request
// fields.
func TestBackgroundScheduler_LegacyRunnerReturnsAnUnavailableReceipt(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	client := s.BindRunner(backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
		return finishedStream(), nil
	}))
	waiter, ok := client.(auxreq.ReceiptAwaiter)
	if !ok {
		t.Fatal("the generation-bound client must expose the optional receipt capability")
	}
	id, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "receipt-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	collected, receipt, err := waiter.AwaitWithReceipt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if collected.TerminalError != nil {
		t.Fatalf("collected terminal error = %+v, want a clean collection", collected.TerminalError)
	}
	if receipt.Execution.Available() {
		t.Fatalf("legacy runner receipt = %+v, want an explicitly unavailable receipt", receipt.Execution)
	}
}

// TestBackgroundSponsorship_ForwardsTheReceiptCapability pins the composition
// truth: the featurehost worker's client is the sponsored wrapper, so a
// receipt method added only to the bound scheduler would be lost there.
func TestBackgroundSponsorship_ForwardsTheReceiptCapability(t *testing.T) {
	t.Parallel()
	runner := &receiptRunner{receipt: receiptFixture()}
	s := newBackground(context.Background(), t, nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	bound := s.BindRunner(runner)
	sponsored := auxreq.WithSponsorship(bound, execctx.Sponsorship{Mode: execctx.SponsorshipModeOperatorSponsored})
	waiter, ok := sponsored.(auxreq.ReceiptAwaiter)
	if !ok {
		t.Fatal("the sponsored wrapper must preserve the optional receipt capability")
	}
	id, err := sponsored.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "receipt-sponsored"})
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := waiter.AwaitWithReceipt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Execution != receiptFixture() {
		t.Fatalf("sponsored receipt = %+v, want %+v", receipt.Execution, receiptFixture())
	}
}

// TestBackgroundScheduler_ReceiptDisappearsWithTheJob pins the single
// lifetime: the receipt is retained by the scheduler's existing bounded job
// and disappears with Forget, so no second registry can pair another job's
// receipt with this result.
func TestBackgroundScheduler_ReceiptDisappearsWithTheJob(t *testing.T) {
	t.Parallel()
	runner := &receiptRunner{receipt: receiptFixture()}
	s := newBackground(context.Background(), t, nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	client := s.BindRunner(runner)
	waiter, ok := client.(auxreq.ReceiptAwaiter)
	if !ok {
		t.Fatal("the generation-bound client must expose the optional receipt capability")
	}
	id, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "receipt-forget"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := waiter.AwaitWithReceipt(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	client.Forget(id)
	if _, _, err := waiter.AwaitWithReceipt(context.Background(), id); !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("AwaitWithReceipt after Forget error = %v, want ErrJobNotFound", err)
	}
}

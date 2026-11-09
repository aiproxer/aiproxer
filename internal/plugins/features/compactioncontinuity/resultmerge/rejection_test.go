package resultmerge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestServiceValidatesParentBeforeAwait(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	parent.validateErr = ErrWrongParentBranch
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrWrongParentBranch) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want wrong-parent rejection", outcome, err)
	}
	if background.awaits != 0 || len(background.forgotten) != 0 {
		t.Fatalf("background await=%d forgotten=%#v; parent must be checked first", background.awaits, background.forgotten)
	}
}

func TestServiceTimeoutKeepsPendingResult(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	background.awaitErr = context.DeadlineExceeded
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrAwaitTimeout) || outcome.Status != StatusPending {
		t.Fatalf("outcome=%#v err=%v, want pending timeout", outcome, err)
	}
	if len(background.forgotten) != 0 || parent.commits != 0 || decoder.calls != 0 {
		t.Fatalf("timeout side effects forgotten=%#v commits=%d decode=%d", background.forgotten, parent.commits, decoder.calls)
	}
}

func TestServiceRejectsSchedulerShutdownInsteadOfKeepingPending(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	// Scheduler shutdown finalizes a queued job with a terminal error that wraps
	// both its closed sentinel and context.Canceled. Cause identity alone must
	// not make that terminal job look like a retryable await timeout.
	background.awaitErr = fmt.Errorf("%w: %w", errors.New("auxreq: background scheduler is closed"), context.Canceled)
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrRejected) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want terminal shutdown rejection", outcome, err)
	}
	if !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("forgotten=%#v, want exactly the terminal job", background.forgotten)
	}
	if parent.commits != 0 || decoder.calls != 0 {
		t.Fatalf("commits=%d decode=%d, want no merge or decode", parent.commits, decoder.calls)
	}
}

func TestServiceKeepsPendingWhenTheCallerCancelsAwait(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	background.awaitErr = context.Canceled
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err := service.Consume(ctx, job)
	if !errors.Is(err, ErrAwaitTimeout) || outcome.Status != StatusPending {
		t.Fatalf("outcome=%#v err=%v, want pending caller cancellation", outcome, err)
	}
	if len(background.forgotten) != 0 || parent.commits != 0 || decoder.calls != 0 {
		t.Fatalf("cancellation side effects forgotten=%#v commits=%d decode=%d", background.forgotten, parent.commits, decoder.calls)
	}
}

func TestServiceRejectsInvalidResultAndForgetsIt(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	decoder.err = errors.New("invalid extractor schema")
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrInvalidResult) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want invalid-result rejection", outcome, err)
	}
	if parent.commits != 0 || !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("commits=%d forgotten=%#v", parent.commits, background.forgotten)
	}
}

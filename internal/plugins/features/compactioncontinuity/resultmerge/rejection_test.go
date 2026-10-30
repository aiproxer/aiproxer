package resultmerge

import (
	"context"
	"errors"
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

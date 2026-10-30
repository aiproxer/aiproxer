package resultmerge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

var (
	ErrWrongParentBranch   = errors.New("wrong parent branch")
	ErrStaleParentRevision = errors.New("stale parent revision")
)

type fakeBackground struct {
	result    aipapi.Collected
	awaitErr  error
	awaits    int
	forgotten []auxiliary.JobID
}

func (f *fakeBackground) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	f.awaits++
	return f.result, f.awaitErr
}

func (f *fakeBackground) Forget(id auxiliary.JobID) { f.forgotten = append(f.forgotten, id) }

type fakeParent struct {
	state             ParentState
	validateErr       error
	commitErr         error
	afterValidate     func()
	validates         int
	commits           int
	committedBytes    []byte
	commitContextErr  error
	commitHasDeadline bool
	commitDeadline    time.Time
}

func (f *fakeParent) ValidatePendingJob(context.Context, string, auxiliary.JobID) (ParentState, error) {
	f.validates++
	if f.validateErr != nil {
		return ParentState{}, f.validateErr
	}
	out := f.state.clone()
	if f.afterValidate != nil {
		f.afterValidate()
	}
	return out, nil
}

func (f *fakeParent) CommitCapsuleForJob(ctx context.Context, _ string, _ auxiliary.JobID, _ string, expectedRevision uint64, data []byte, digest [32]byte, highWatermark string) (ParentState, error) {
	f.commits++
	f.commitContextErr = ctx.Err()
	f.commitDeadline, f.commitHasDeadline = ctx.Deadline()
	if f.commitErr != nil {
		return ParentState{}, f.commitErr
	}
	if f.state.Revision != expectedRevision {
		return ParentState{}, ErrStaleParentRevision
	}
	f.committedBytes = append([]byte(nil), data...)
	f.state.Revision++
	f.state.CapsuleJSON = append([]byte(nil), data...)
	f.state.CapsuleDigest = digest
	f.state.SourceHighWatermark = highWatermark
	f.state.PendingJobID = ""
	return f.state.clone(), nil
}

func TestService_usesFreshBoundedCommitContextAfterBarrierExpires(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	barrierCtx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err := service.Consume(barrierCtx, job)
	if err != nil || outcome.Status != StatusMerged {
		t.Fatalf("Consume outcome=%#v err=%v, want merged despite expired barrier", outcome, err)
	}
	if parent.commitContextErr != nil {
		t.Fatalf("commit received expired barrier context: %v", parent.commitContextErr)
	}
	if !parent.commitHasDeadline || !parent.commitDeadline.After(time.Now()) {
		t.Fatalf("commit context was not freshly bounded: deadline=%v hasDeadline=%v", parent.commitDeadline, parent.commitHasDeadline)
	}
}

type fakeDecoder struct {
	delta capsule.Delta
	err   error
	calls int
	input DecodeInput
}

func (f *fakeDecoder) Decode(_ aipapi.Collected, input DecodeInput) (capsule.Delta, error) {
	f.calls++
	f.input = input
	if input.Previous.BranchBinding != f.delta.BranchBinding || input.ExpectedBranch != f.delta.BranchBinding {
		return capsule.Delta{}, errors.New("decoder received wrong parent authority")
	}
	if input.Previous.Revision != f.delta.BaseRevision {
		return capsule.Delta{}, errors.New("decoder received wrong parent revision")
	}
	return f.delta, f.err
}

func testCapsule(t *testing.T, binding string) (capsule.Envelope, []byte, [32]byte) {
	t.Helper()
	e, err := capsule.New(binding)
	if err != nil {
		t.Fatal(err)
	}
	b, err := capsule.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	digest := mustDigestArray(t, e.ContentDigest)
	return e, b, digest
}

func mustDigestArray(t *testing.T, value string) [32]byte {
	t.Helper()
	encoded := strings.TrimPrefix(value, "sha256:")
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		t.Fatalf("decode digest %q: %v", value, err)
	}
	var out [32]byte
	copy(out[:], raw)
	return out
}

func validFixture(t *testing.T) (Job, *fakeBackground, *fakeParent, *fakeDecoder, capsule.Envelope) {
	t.Helper()
	binding := "sha256:" + strings.Repeat("a", 64)
	base, raw, digest := testCapsule(t, binding)
	parent := &fakeParent{state: ParentState{
		BranchBinding:            binding,
		Revision:                 base.Revision,
		CapsuleJSON:              raw,
		CapsuleDigest:            digest,
		SourceHighWatermark:      "source-revision-1",
		PendingJobID:             "job-1",
		PendingJobTargetRevision: base.Revision,
		PendingJobBranchBinding:  binding,
	}}
	background := &fakeBackground{}
	decoder := &fakeDecoder{delta: capsule.Delta{
		SchemaVersion: capsule.SchemaVersion,
		BaseRevision:  base.Revision,
		BranchBinding: binding,
		Decisions: []capsule.Decision{{
			ID: "decision-1", ConflictKey: "runtime.mode", Statement: "Use the bounded mode.",
			Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic,
		}},
	}}
	return Job{ID: "job-1", ParentBranchBinding: binding}, background, parent, decoder, base
}

func TestService_ConsumesValidatedResultAndForgetsRawOutput(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, base := validFixture(t)
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Consume(context.Background(), job)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if outcome.Status != StatusMerged || outcome.State.Revision != base.Revision+1 {
		t.Fatalf("outcome = %#v, want merged revision %d", outcome, base.Revision+1)
	}
	if parent.validates != 1 || parent.commits != 1 || decoder.calls != 1 || background.awaits != 1 {
		t.Fatalf("calls validate=%d commit=%d decode=%d await=%d", parent.validates, parent.commits, decoder.calls, background.awaits)
	}
	if decoder.input.Previous.ContentDigest != base.ContentDigest || decoder.input.Previous.Revision != base.Revision || decoder.input.ExpectedBranch != job.ParentBranchBinding || decoder.input.SourceHighWatermark != parent.state.SourceHighWatermark {
		t.Fatalf("decoder input = %#v, want verified parent and watermark", decoder.input)
	}
	if !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("forgotten = %#v, want %q", background.forgotten, job.ID)
	}
	merged, err := capsule.Decode(parent.committedBytes)
	if err != nil {
		t.Fatalf("decode committed capsule: %v", err)
	}
	if merged.Revision != base.Revision+1 || len(merged.Decisions) != 1 {
		t.Fatalf("merged capsule = %#v", merged)
	}
}

func TestServiceRejectsWrongBranchResultAndForgetsIt(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	decoder.delta.BranchBinding = "sha256:" + strings.Repeat("b", 64)
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrInvalidResult) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want wrong-branch rejection", outcome, err)
	}
	if parent.commits != 0 || !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("commits=%d forgotten=%#v", parent.commits, background.forgotten)
	}
}

func TestServiceRejectsConflictInvalidDeltaWithoutStateChange(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	decoder.delta.Decisions = append(decoder.delta.Decisions, capsule.Decision{
		ID: "decision-1", ConflictKey: "runtime.mode", Statement: "different statement",
		Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic,
	})
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	before := parent.state.clone()
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrInvalidResult) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want conflict rejection", outcome, err)
	}
	if parent.commits != 0 || !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("commits=%d forgotten=%#v", parent.commits, background.forgotten)
	}
	if parent.state.Revision != before.Revision || string(parent.state.CapsuleJSON) != string(before.CapsuleJSON) {
		t.Fatalf("parent state changed after rejected delta: before=%#v after=%#v", before, parent.state)
	}
}

func TestServiceRejectsStoredDigestMismatchBeforeDecode(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, _ := validFixture(t)
	parent.state.CapsuleDigest[0]++
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrInvalidParentState) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want stored-digest rejection", outcome, err)
	}
	if decoder.calls != 0 || parent.commits != 0 || !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("decode=%d commits=%d forgotten=%#v", decoder.calls, parent.commits, background.forgotten)
	}
}

func TestServiceLateResultCannotDefeatNewerParentRevision(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, base := validFixture(t)
	newer, _, _ := testCapsule(t, job.ParentBranchBinding)
	newer.Revision = base.Revision + 1
	if err := newer.Seal(); err != nil {
		t.Fatal(err)
	}
	raw, err := capsule.Encode(newer)
	if err != nil {
		t.Fatal(err)
	}
	parent.afterValidate = func() {
		corrected := make(chan struct{})
		release := make(chan struct{})
		go func() {
			<-corrected
			parent.state.Revision = newer.Revision
			parent.state.CapsuleJSON = raw
			parent.state.CapsuleDigest = mustDigestArray(t, newer.ContentDigest)
			parent.state.PendingJobTargetRevision = base.Revision
			close(release)
		}()
		close(corrected)
		<-release
	}

	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Consume(context.Background(), job)
	if !errors.Is(err, ErrStaleParentRevision) || outcome.Status != StatusRejected {
		t.Fatalf("outcome=%#v err=%v, want stale rejection", outcome, err)
	}
	if parent.commits != 1 || parent.state.Revision != newer.Revision || !equalIDs(background.forgotten, []auxiliary.JobID{job.ID}) {
		t.Fatalf("commits=%d forgotten=%#v", parent.commits, background.forgotten)
	}
}

func equalIDs(got, want []auxiliary.JobID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

package testkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestFakeSecureSessionStore_injectedCreateError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	f := &FakeSecureSessionStore{CreateErr: want}
	_, err := f.Create(context.Background(), domain.CreateRecord{SessionID: "x"})
	if !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestFakeSecureSessionStore_delegateLoad(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = mem
	f := &FakeB2BUAStore{Impl: mem}
	rec, err := f.CreateALeg(context.Background(), "ck")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ALegID == "" {
		t.Fatal("expected a-leg")
	}
}

func TestFakeSecureSessionRecorder_postOutputFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("recorder failed after output")
	r := &FakeSecureSessionRecorder{PostOutputFailure: want}
	if err := r.RecordPostHookStreamEvent(context.Background(), app.StreamEventRecordInput{EventKind: "text_delta"}); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	if r.StreamRecords != 1 {
		t.Fatalf("calls: %d", r.StreamRecords)
	}
}

func TestFakeSecureSessionStore_checkReadiness(t *testing.T) {
	t.Parallel()
	want := domain.ErrStorageUnavailable
	f := &FakeSecureSessionStore{CheckReadinessErr: want}
	if err := f.CheckReadiness(context.Background(), domain.PolicyMetadata{}); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestFakeSecureSessionRecorder_touchCounts(t *testing.T) {
	t.Parallel()
	r := &FakeSecureSessionRecorder{}
	if err := r.TouchActivity(context.Background(), domain.SessionID("s"), time.Time{}, domain.ActivityClientRequest); err != nil {
		t.Fatal(err)
	}
	if r.TouchCalls != 1 {
		t.Fatalf("touch calls %d", r.TouchCalls)
	}
}

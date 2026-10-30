package continuation_test

import (
	"context"
	"errors"
	"testing"

	corecont "github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestRound5MemoryStoreRejectsUnprotectedNativeReferences(t *testing.T) {
	t.Parallel()
	store := corecont.NewMemoryStore()
	scope := aipcont.Scope{PrincipalID: "round5-principal"}
	id, err := store.Reserve(context.Background(), scope, aipcont.StoragePolicy{Mode: aipcont.PersistencePersistent})
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutTerminal(context.Background(), aipcont.ContinuationRecord{
		ID:         id,
		Scope:      scope,
		Terminal:   true,
		NativeRefs: []aipcont.NativeReference{{Provider: "provider", ID: "native"}},
	})
	if !errors.Is(err, aipcont.ErrNativeReferencesUnprotected) {
		t.Fatalf("PutTerminal error = %v, want ErrNativeReferencesUnprotected", err)
	}
}

type round5Recorder struct{ calls int }

func (r *round5Recorder) RecordTerminal(context.Context, aipcont.ContinuationRecord) error {
	r.calls++
	return nil
}

func TestRound5RecorderCountsReasoningBytesAgainstQuota(t *testing.T) {
	t.Parallel()
	backend := &round5Recorder{}
	recorder := corecont.NewStreamRecorder(backend, aipcont.ContinuationRecord{
		Policy: aipcont.StoragePolicy{Limits: aipcont.StorageLimits{MaxRecordBytes: 5}},
	}, func() {})
	recorder.Observe(context.Background(), aipapi.Event{
		Kind: aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{
			Text:      "reasoning",
			Signature: "sig",
			Opaque:    []byte("opaque"),
		},
	})
	if backend.calls != 0 {
		t.Fatal("reasoning event exceeded quota but was persisted")
	}
}

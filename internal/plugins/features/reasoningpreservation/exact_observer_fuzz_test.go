package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

func FuzzStreamObserver_exactReasoningPart(f *testing.F) {
	f.Add([]byte(`{"id":"rs_1","type":"reasoning","summary":[]}`))
	f.Add([]byte(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}`))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"id":"rs_1","summary":[],"extra":1}`))
	f.Fuzz(func(t *testing.T, opaque []byte) {
		if len(opaque) > 8<<10 {
			opaque = opaque[:8<<10]
		}
		cfg := observeExactConfig(t)
		store := newMemoryStore(t, exactStoreOptions(time.Now))
		obs := openExactObserver(t, cfg, store, "sess-fuzz", nil)
		_ = obs.Observe(context.Background(), aipapi.Event{
			Kind: aipapi.EventReasoningPart,
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
				Opaque:  append([]byte(nil), opaque...),
			},
		})
		_ = obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
		_ = obs.Finish(context.Background(), response.OutcomeSuccessReleased)
	})
}

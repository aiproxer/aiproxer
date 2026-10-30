package codex

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/localtok/countapp"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/localtok/imageestimator"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/localtok/tiktoken"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type usageEstimator struct {
	counter *tiktoken.Counter
}

func newUsageEstimator() (*usageEstimator, error) {
	counter, err := tiktoken.NewCounter(tiktoken.Config{
		DefaultEncoding: "o200k_base",
		Image: tiktoken.ImageConfig{
			UseDefaultTokens: true,
			DefaultTokens:    imageestimator.DefaultBaseTokens,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%s: create usage token counter: %w", ID, err)
	}
	return &usageEstimator{counter: counter}, nil
}

func (e *usageEstimator) estimateUsage(ctx context.Context, call aipapi.Call, model, outputText string) (aipapi.Event, error) {
	callResult, err := e.counter.CountCall(ctx, countapp.CountCallInput{Call: call, Model: model})
	if err != nil {
		return aipapi.Event{}, err
	}
	outputResult, err := e.counter.CountOutput(ctx, countapp.CountOutputInput{Model: model, Text: outputText})
	if err != nil {
		return aipapi.Event{}, err
	}
	inputTokens := callResult.InputTokens
	outputTokens := outputResult.OutputTokens
	accounting := callResult.Accounting
	accounting.Plane = aipapi.UsagePlaneProviderBillable
	accounting.Tokenizer.ModelUsed = model
	return aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		Accounting:   accounting,
	}, nil
}

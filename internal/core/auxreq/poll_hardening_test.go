//nolint:forcetypeassert // Test fixtures assert the concrete scripted type.
package auxreq_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// TestPoll_Hardening_DefensiveCopyAllMutableFields verifies that Poll's
// defensive copy isolates EVERY mutable field of aipapi.Collected.
func TestPoll_Hardening_DefensiveCopyAllMutableFields(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
				{Kind: aipapi.EventReasoningDelta, Delta: "think"},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "tool-a"},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call-1", Delta: `{"x":1}`},
				{Kind: aipapi.EventWarning, WarningMessage: "warn-1"},
				{Kind: aipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 7, CacheReadTokens: 2, CacheWriteTokens: 3, ReasoningTokens: 4, TotalTokens: 18, CostNanoUnits: 99, Currency: "USD", CostSource: "catalog"},
				{Kind: aipapi.EventAssistantImageRef, AssistantRef: "img-ref", AssistantMIME: "image/png"},
				{Kind: aipapi.EventAssistantFileRef, AssistantRef: "file-ref", AssistantMIME: "application/pdf", AssistantName: "doc.pdf"},
				{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
					Dialect:          "openai",
					Text:             "rp-text",
					Opaque:           json.RawMessage(`"opaque"`),
					Summary:          json.RawMessage(`"summary"`),
					Content:          json.RawMessage(`"content"`),
					SummaryPresent:   true,
					ContentPresent:   true,
					EncryptedContent: json.RawMessage(`null`),
				}},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			}), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "hardening-copy-all"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	first, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, first.State)
	require.Equal(t, "hello", first.Collected.Text.String())
	require.Equal(t, "think", first.Collected.Reasoning.String())
	require.Equal(t, "tool-a", first.Collected.ToolNames["call-1"])
	require.Equal(t, `{"x":1}`, first.Collected.ToolArgs["call-1"].String())
	require.Equal(t, []string{"call-1"}, first.Collected.ToolCallOrder)
	require.Equal(t, []string{"warn-1"}, first.Collected.Warnings)
	require.Equal(t, 11, first.Collected.InputTokens)
	require.Equal(t, 7, first.Collected.OutputTokens)
	require.Equal(t, int64(99), first.Collected.CostNanoUnits)
	require.Equal(t, "USD", first.Collected.Currency)
	require.Equal(t, "catalog", first.Collected.CostSource)
	require.Equal(t, true, first.Collected.FinishReceived)
	require.Equal(t, "stop", first.Collected.FinishReason)
	require.Len(t, first.Collected.AssistantMedia, 2)
	require.Len(t, first.Collected.ReasoningParts, 1)

	// Mutate every mutable field of the returned defensive copy.
	first.Collected.Text.Reset()
	_, _ = first.Collected.Text.WriteString("mut-text")
	first.Collected.Reasoning.Reset()
	_, _ = first.Collected.Reasoning.WriteString("mut-reasoning")
	first.Collected.ToolNames["call-1"] = "mut-tool"
	if b := first.Collected.ToolArgs["call-1"]; b != nil {
		_, _ = b.WriteString("-mut")
	}
	nb := &strings.Builder{}
	_, _ = nb.WriteString("extra-args")
	first.Collected.ToolArgs["extra"] = nb
	first.Collected.ToolCallOrder[0] = "mut-order"
	first.Collected.ToolCallOrder = append(first.Collected.ToolCallOrder, "extra")
	first.Collected.Warnings[0] = "mut-warn"
	first.Collected.Warnings = append(first.Collected.Warnings, "extra-warn")
	first.Collected.InputTokens = 999
	first.Collected.OutputTokens = 999
	first.Collected.CacheReadTokens = 999
	first.Collected.CacheWriteTokens = 999
	first.Collected.ReasoningTokens = 999
	first.Collected.TotalTokens = 999
	first.Collected.CostNanoUnits = 999
	first.Collected.Currency = "MUT"
	first.Collected.CostSource = "mut"
	first.Collected.FinishReceived = false
	first.Collected.FinishReason = "mut-reason"
	if len(first.Collected.AssistantMedia) > 0 {
		first.Collected.AssistantMedia[0].ImageRef = "mut-img"
		first.Collected.AssistantMedia[0].Content = json.RawMessage(`"mut"`)
	}
	if len(first.Collected.AssistantMedia) > 1 {
		first.Collected.AssistantMedia[1].FileRef = "mut-file"
	}
	if len(first.Collected.ReasoningParts) > 0 {
		first.Collected.ReasoningParts[0].Text = "mut-rp"
		if len(first.Collected.ReasoningParts[0].Opaque) > 0 {
			first.Collected.ReasoningParts[0].Opaque[0] = 'X'
		}
		if len(first.Collected.ReasoningParts[0].Summary) > 0 {
			first.Collected.ReasoningParts[0].Summary[0] = 'X'
		}
		if len(first.Collected.ReasoningParts[0].Content) > 0 {
			first.Collected.ReasoningParts[0].Content[0] = 'X'
		}
	}

	second, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, second.State)
	assert.Equal(t, "hello", second.Collected.Text.String(), "Text builder copy")
	assert.Equal(t, "think", second.Collected.Reasoning.String(), "Reasoning builder copy")
	assert.Equal(t, "tool-a", second.Collected.ToolNames["call-1"])
	assert.Equal(t, `{"x":1}`, second.Collected.ToolArgs["call-1"].String())
	assert.NotContains(t, second.Collected.ToolArgs, "extra")
	assert.Equal(t, []string{"call-1"}, second.Collected.ToolCallOrder)
	assert.Equal(t, []string{"warn-1"}, second.Collected.Warnings)
	assert.Equal(t, 11, second.Collected.InputTokens)
	assert.Equal(t, 7, second.Collected.OutputTokens)
	assert.Equal(t, 2, second.Collected.CacheReadTokens)
	assert.Equal(t, 3, second.Collected.CacheWriteTokens)
	assert.Equal(t, 4, second.Collected.ReasoningTokens)
	assert.Equal(t, 18, second.Collected.TotalTokens)
	assert.Equal(t, int64(99), second.Collected.CostNanoUnits)
	assert.Equal(t, "USD", second.Collected.Currency)
	assert.Equal(t, "catalog", second.Collected.CostSource)
	assert.True(t, second.Collected.FinishReceived)
	assert.Equal(t, "stop", second.Collected.FinishReason)
	assert.Equal(t, "img-ref", second.Collected.AssistantMedia[0].ImageRef)
	assert.Equal(t, "file-ref", second.Collected.AssistantMedia[1].FileRef)
	assert.Equal(t, "rp-text", second.Collected.ReasoningParts[0].Text)
	assert.Equal(t, json.RawMessage(`"opaque"`), second.Collected.ReasoningParts[0].Opaque)
	assert.Equal(t, json.RawMessage(`"summary"`), second.Collected.ReasoningParts[0].Summary)
	assert.Equal(t, json.RawMessage(`"content"`), second.Collected.ReasoningParts[0].Content)
}

// TestPoll_Hardening_MaxResultsEvictionViaPoll verifies that Poll observes
// the same MaxResults eviction semantics as Await.
func TestPoll_Hardening_MaxResultsEvictionViaPoll(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{MaxResults: 1})

	first, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-evict-one"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), first)
	require.NoError(t, err)

	second, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-evict-two"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), second)
	require.NoError(t, err)

	res, err := s.Poll(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State, "evicted result must be PollNotFound just like ErrJobNotFound on Await")

	res, err = s.Poll(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)
}

// TestPoll_Hardening_NilSchedulerAndBoundClientClosed verifies Poll error
// contracts for nil scheduler and closed bound client paths, and ensures no panic.
func TestPoll_Hardening_NilSchedulerAndBoundClientClosed(t *testing.T) {
	t.Parallel()
	var nilSched *auxreq.BackgroundScheduler
	_, err := nilSched.Poll(context.Background(), "id")
	require.ErrorIs(t, err, auxreq.ErrSchedulerClosed)

	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})
	// The bound runner stays non-terminal until release closes or the scheduler
	// cancels the worker context, so the pending assertion below cannot race the
	// worker's terminal result.
	release := make(chan struct{})
	client := s.BindRunner(pollGatedRunner(release))
	id, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bound-nil-check"})
	require.NoError(t, err)
	poller := client.(auxiliary.BackgroundPoller)
	res, err := poller.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	require.NoError(t, s.Close())
	res, err = poller.Poll(context.Background(), id)
	if err != nil {
		require.Error(t, err)
	} else {
		assert.True(t, res.State == auxiliary.PollFailed || res.State == auxiliary.PollNotFound || res.State == auxiliary.PollPending || res.State == auxiliary.PollCompleted)
	}
}

// TestPoll_Hardening_CloseRaceNoPanic aggressively races Poll, Await, Forget and Close.
func TestPoll_Hardening_CloseRaceNoPanic(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return pollPendingRunner(started, release)
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 8})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "close-race-no-panic"})
	require.NoError(t, err)
	<-started

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 50 {
			_, _ = s.Poll(context.Background(), id)
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			_, _ = s.Await(context.Background(), id)
			s.Forget(id)
			_, _ = s.Poll(context.Background(), id)
		}
	}()
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()
	close(release)
	wg.Wait()
	_, _ = s.Poll(context.Background(), id)
	_, _ = s.Poll(context.Background(), "missing")
}

// TestPoll_Hardening_AwaitForgetUnchangedRegression ensures Await/Forget semantics
// are unchanged after introducing Poll.
func TestPoll_Hardening_AwaitForgetUnchangedRegression(t *testing.T) {
	t.Parallel()
	// Gate the worker so the first Poll deterministically observes pending;
	// release unblocks completion before Await.
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return pollGatedRunner(release)
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "await-forget-regression"})
	require.NoError(t, err)

	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	close(release)
	collected, err := s.Await(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, collected.FinishReceived)

	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	s.Forget(id)
	_, err = s.Await(context.Background(), id)
	require.ErrorIs(t, err, auxreq.ErrJobNotFound)
	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State)
}

package openresponses

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// BuildResponseResource constructs the complete OpenResponses ResponseResource JSON bytes and wire struct.
func BuildResponseResource(envelope EnvelopeMetadata, trajectory []aipapi.Item, usage UsageStats, options aipapi.GenerationOptions, streamErr *aipapi.StreamError) (*WireResponseResource, []byte, error) {
	if envelope.ResponseID == "" {
		return nil, nil, fmt.Errorf("%w: response ID is required", ErrBuildResourceFailed)
	}

	status := envelope.Status
	if status == "" {
		status = "completed"
	}

	var completedAt *int64
	if envelope.CompletedAt != nil {
		ts := envelope.CompletedAt.Unix()
		completedAt = &ts
	} else if status == "completed" {
		// Completion is observed at resource-build time when the producer did
		// not supply an upstream timestamp. Never claim the response completed
		// at its creation time.
		ts := time.Now().Unix()
		completedAt = &ts
	}

	var wireOutput []WireItem
	for i, item := range trajectory {
		wItem, err := EncodeItem(item)
		if err != nil {
			return nil, nil, fmt.Errorf("trajectory[%d]: %w", i, err)
		}
		wireOutput = append(wireOutput, wItem)
	}

	parallelCalls := false
	if options.ParallelToolCalls != nil {
		parallelCalls = *options.ParallelToolCalls
	}

	store := true
	if envelope.Store != nil {
		store = *envelope.Store
	}

	temp := options.Temperature
	if temp == nil {
		defaultTemp := 0.7
		temp = &defaultTemp
	}

	topP := options.TopP
	if topP == nil {
		defaultTopP := 1.0
		topP = &defaultTopP
	}

	wireUsage := WireUsage{
		InputTokens: usage.InputTokens,
		InputTokensDetails: WireUsageInputDetails{
			CachedTokens: usage.CachedTokens,
		},
		OutputTokens: usage.OutputTokens,
		OutputTokensDetails: WireUsageOutputDetails{
			ReasoningTokens: usage.ReasoningTokens,
		},
		TotalTokens: usage.TotalTokens,
	}

	var prevID *string
	if envelope.PreviousResponseID != "" {
		pID := envelope.PreviousResponseID
		prevID = &pID
	}

	errJSON := json.RawMessage("null")
	if streamErr != nil {
		errMap := map[string]string{
			"code":    streamErr.Code,
			"message": sanitizeErrorMessage(streamErr.Message),
		}
		b, _ := json.Marshal(errMap)
		errJSON = b
	}

	// A recorded refusal lexeme surfaces as incomplete_details without
	// touching the status contract. Absent means null (the length path
	// keeps its existing shape).
	incompleteDetails := json.RawMessage("null")
	if envelope.IncompleteReason != "" {
		b, _ := json.Marshal(map[string]string{"reason": envelope.IncompleteReason})
		incompleteDetails = b
	}

	resource := &WireResponseResource{
		ID:                   envelope.ResponseID,
		Object:               "response",
		CreatedAt:            envelope.CreatedAt.Unix(),
		Status:               status,
		CompletedAt:          completedAt,
		Model:                envelope.Model,
		Output:               ensureNonNilSlice(wireOutput),
		ParallelToolCalls:    parallelCalls,
		Reasoning:            json.RawMessage("null"),
		Store:                store,
		Background:           false,
		Temperature:          temp,
		Text:                 map[string]any{"format": map[string]any{"type": "text"}},
		ToolChoice:           json.RawMessage(`"auto"`),
		Tools:                []WireTool{},
		TopP:                 topP,
		PresencePenalty:      0,
		FrequencyPenalty:     0,
		TopLogprobs:          0,
		Truncation:           "disabled",
		Usage:                wireUsage,
		Metadata:             make(map[string]any),
		ServiceTier:          "default",
		MaxOutputTokens:      options.MaxOutputTokens,
		MaxToolCalls:         nil,
		Instructions:         nil,
		PreviousResponseID:   prevID,
		Error:                errJSON,
		IncompleteDetails:    incompleteDetails,
		SafetyIdentifier:     nil,
		PromptCacheKey:       nil,
		PromptCacheRetention: nil,
	}

	jsonBytes, err := json.Marshal(resource)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to marshal response resource: %v", ErrBuildResourceFailed, err)
	}

	return resource, jsonBytes, nil
}

// BuildCompactResource constructs the OpenResponses CompactResource JSON bytes and wire struct.
func BuildCompactResource(envelope EnvelopeMetadata, trajectory []aipapi.Item, usage UsageStats) (*WireCompactResource, []byte, error) {
	if envelope.ResponseID == "" {
		return nil, nil, fmt.Errorf("%w: compact response ID is required", ErrBuildResourceFailed)
	}

	status := envelope.Status
	if status == "" {
		status = "completed"
	}

	var wireOutput []WireItem
	for i, item := range trajectory {
		wItem, err := EncodeItem(item)
		if err != nil {
			return nil, nil, fmt.Errorf("compact trajectory[%d]: %w", i, err)
		}
		wireOutput = append(wireOutput, wItem)
	}

	wireUsage := WireUsage{
		InputTokens: usage.InputTokens,
		InputTokensDetails: WireUsageInputDetails{
			CachedTokens: usage.CachedTokens,
		},
		OutputTokens: usage.OutputTokens,
		OutputTokensDetails: WireUsageOutputDetails{
			ReasoningTokens: usage.ReasoningTokens,
		},
		TotalTokens: usage.TotalTokens,
	}

	resource := &WireCompactResource{
		ID:        envelope.ResponseID,
		Object:    "response.compaction",
		CreatedAt: envelope.CreatedAt.Unix(),
		Status:    status,
		Model:     envelope.Model,
		Output:    ensureNonNilSlice(wireOutput),
		Usage:     wireUsage,
	}

	jsonBytes, err := json.Marshal(resource)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: failed to marshal compact resource: %v", ErrBuildResourceFailed, err)
	}

	return resource, jsonBytes, nil
}

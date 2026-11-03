package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/infra/diagredact"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func capsToAipapi(c backendplugin.CapabilitySummary) aipapi.BackendCaps {
	var list []aipapi.Capability
	if c.Streaming {
		list = append(list, aipapi.CapabilityStreaming)
	}
	if c.Tools {
		list = append(list, aipapi.CapabilityTools)
	}
	if c.Vision {
		list = append(list, aipapi.CapabilityVision)
	}
	if c.Documents {
		list = append(list, aipapi.CapabilityDocuments)
	}
	if c.StructuredOutputs {
		list = append(list, aipapi.CapabilityStructuredOutputs)
	}
	if c.Reasoning {
		list = append(list, aipapi.CapabilityReasoning)
	}
	if c.ReasoningReplay {
		list = append(list, aipapi.CapabilityReasoningReplay)
	}
	if c.ParallelToolCalls {
		list = append(list, aipapi.CapabilityParallelToolCalls)
	}
	if c.OrderedItems {
		list = append(list, aipapi.CapabilityOrderedItems)
	}
	if c.ItemReferences {
		list = append(list, aipapi.CapabilityItemReferences)
	}
	if c.Compaction {
		list = append(list, aipapi.CapabilityCompaction)
	}
	if c.AssistantPhase {
		list = append(list, aipapi.CapabilityAssistantPhase)
	}
	if c.OpaqueExtensions {
		list = append(list, aipapi.CapabilityOpaqueExtensions)
	}
	if c.VideoInput {
		list = append(list, aipapi.CapabilityVideoInput)
	}
	if c.Annotations {
		list = append(list, aipapi.CapabilityAnnotations)
	}
	if c.AssistantMediaRefs {
		list = append(list, aipapi.CapabilityAssistantMediaRefs)
	}
	return aipapi.NewBackendCaps(list...)
}

func eventToAipapi(ev *backendplugin.CanonicalEvent) (aipapi.Event, error) {
	if ev == nil {
		return aipapi.Event{}, fmt.Errorf("nil event")
	}
	if err := backendplugin.ValidateEventKind(ev.Kind); err != nil {
		return aipapi.Event{}, err
	}
	out := aipapi.Event{Kind: ev.Kind}
	if ev.MessageIndex != nil {
		out.MessageIndex = int(*ev.MessageIndex)
	}
	if ev.Delta != nil {
		out.Delta = *ev.Delta
	}
	if ev.Signature != nil {
		out.Signature = *ev.Signature
	}
	if len(ev.Opaque) > 0 {
		out.Opaque = append([]byte(nil), ev.Opaque...)
	}
	if ev.ReasoningDialect != nil || len(ev.ReasoningOpaque) > 0 ||
		ev.ReasoningSummary.State() != backendplugin.RawJSONAbsent ||
		ev.ReasoningContent.State() != backendplugin.RawJSONAbsent ||
		ev.ReasoningEncryptedContent.State() != backendplugin.RawJSONAbsent {
		out.Reasoning = &aipapi.ReasoningPart{
			Opaque: append([]byte(nil), ev.ReasoningOpaque...),
		}
		if ev.ReasoningDialect != nil {
			out.Reasoning.Dialect = aipapi.ReasoningDialect(*ev.ReasoningDialect)
		}
		applyReasoningExactFieldsToAipapi(ev, out.Reasoning)
	}
	if ev.ToolCallID != nil {
		out.ToolCallID = *ev.ToolCallID
	}
	if ev.ToolName != nil {
		out.ToolName = *ev.ToolName
	}
	if ev.Usage != nil {
		out.UsagePresence = ev.Usage.Presence
		// The input relations travel with the counters: a connector that
		// reports its cache reads or its cache writes outside the input
		// counter keeps that declaration, so a frontend can translate the
		// counters into its own input semantics. The two declarations are
		// independent: a producer may exclude reads while including writes,
		// exclude writes while including reads, or exclude both.
		out.Accounting.InputTokensExcludeCacheRead = ev.Usage.InputTokensExcludeCacheRead
		out.Accounting.InputTokensExcludeCacheWrite = ev.Usage.InputTokensExcludeCacheWrite
		if ev.Usage.Presence.InputTokens && ev.Usage.InputTokens != nil {
			out.InputTokens = int(*ev.Usage.InputTokens)
		}
		if ev.Usage.Presence.OutputTokens && ev.Usage.OutputTokens != nil {
			out.OutputTokens = int(*ev.Usage.OutputTokens)
		}
		if ev.Usage.Presence.CacheReadTokens && ev.Usage.CacheReadTokens != nil {
			out.CacheReadTokens = int(*ev.Usage.CacheReadTokens)
		}
		if ev.Usage.Presence.CacheWriteTokens && ev.Usage.CacheWriteTokens != nil {
			out.CacheWriteTokens = int(*ev.Usage.CacheWriteTokens)
		}
		if ev.Usage.Presence.ReasoningTokens && ev.Usage.ReasoningTokens != nil {
			out.ReasoningTokens = int(*ev.Usage.ReasoningTokens)
		}
		if ev.Usage.Presence.TotalTokens && ev.Usage.TotalTokens != nil {
			out.TotalTokens = int(*ev.Usage.TotalTokens)
		}
	}
	if ev.Warning != nil {
		out.WarningMessage = sanitizeDiagnosticText(*ev.Warning, 256)
	}
	if ev.Error != nil {
		out.ErrorCode = truncate(string(ev.Error.Code), 64)
		out.ErrorMessage = sanitizeDiagnosticText(ev.Error.Message, 256)
	}
	if ev.ImageRef != nil {
		out.AssistantRef = *ev.ImageRef
	}
	if ev.FileRef != nil {
		out.AssistantRef = *ev.FileRef
	}
	if ev.FinishReason != nil {
		out.FinishReason = *ev.FinishReason
	}
	if ev.ResponseStatus != nil {
		out.ResponseStatus = *ev.ResponseStatus
	}
	if err := aipapi.ValidateEventEnvelope(&out); err != nil {
		return aipapi.Event{}, err
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// applyReasoningExactFieldsToAipapi restores canonical OpenAI Responses
// reasoning-item exact fields (summary/content arrays, nullable encrypted_content)
// from ABI RawJSON carriers with absent/null/value presence.
func applyReasoningExactFieldsToAipapi(ev *backendplugin.CanonicalEvent, out *aipapi.ReasoningPart) {
	switch ev.ReasoningSummary.State() {
	case backendplugin.RawJSONValue:
		out.Summary = append(json.RawMessage(nil), ev.ReasoningSummary.Bytes()...)
		out.SummaryPresent = true
	}
	switch ev.ReasoningContent.State() {
	case backendplugin.RawJSONValue:
		out.Content = append(json.RawMessage(nil), ev.ReasoningContent.Bytes()...)
		out.ContentPresent = true
	}
	switch ev.ReasoningEncryptedContent.State() {
	case backendplugin.RawJSONNull:
		out.EncryptedContent = json.RawMessage("null")
		out.EncryptedContentPresent = true
	case backendplugin.RawJSONValue:
		out.EncryptedContent = append(json.RawMessage(nil), ev.ReasoningEncryptedContent.Bytes()...)
		out.EncryptedContentPresent = true
	}
}

// ClassifiedError is a sanitized plugin/transport error for core ownership.
type ClassifiedError struct {
	Code            string
	Message         string
	Retryable       bool
	OutputCommitted bool
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return "plugin error"
	}
	return e.Code + ": " + e.Message
}

// ClassificationCode returns the stable machine-readable failure class. Core
// logs use this constant instead of the untrusted message text.
func (e *ClassifiedError) ClassificationCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// Unwrap exposes aipapi.ErrRecoverablePreOutput for pre-output retryable failures
// so the existing executor seam can swallow/failover without importing adapter.
// A pre-output non-retryable failed_precondition is the exact terminal the shared
// SDK repair emits, restricted to ErrCapabilityReject (verified sole current
// producer), so it bridges to *aipapi.RejectError for the frontend reject branch.
// A new FailedPrecondition emitter needs its own classification review.
// A pre-output non-retryable permission_denied is a backend entitlement refusal
// (for example a subscription plan refusing third-party extra usage), so it
// bridges to the existing *prerequest.RejectError 403 branch. Its reason is a
// constant: the plugin's own message never crosses the client boundary. A
// retryable or post-output permission failure keeps its current classification,
// so a committed attempt is never rewritten.
func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Retryable && !e.OutputCommitted {
		return aipapi.ErrRecoverablePreOutput
	}
	if e.Code == string(backendplugin.ErrorCodeFailedPrecondition) && !e.Retryable && !e.OutputCommitted {
		return &aipapi.RejectError{Reason: capabilityRejectedReason}
	}
	if e.Code == string(backendplugin.ErrorCodePermissionDenied) && !e.Retryable && !e.OutputCommitted {
		return &prerequest.RejectError{Message: backendPermissionDeniedReason}
	}
	return nil
}

// capabilityRejectedReason is the bounded wire-safe reason for projected capability
// rejections: a constant, never plugin text, so nothing operator-controlled crosses.
const capabilityRejectedReason = "capability rejected"

// backendPermissionDeniedReason is the bounded wire-safe reason for a backend
// entitlement refusal: a constant, never plugin text, so nothing
// operator-controlled crosses the client boundary.
const backendPermissionDeniedReason = "backend permission denied"

func sanitizePluginError(e *backendplugin.PluginError) *ClassifiedError {
	if e == nil {
		return nil
	}
	return &ClassifiedError{
		Code:            string(e.Code),
		Message:         sanitizeDiagnosticText(e.Message, 256),
		Retryable:       e.Retryable,
		OutputCommitted: e.OutputCommitted,
	}
}

func sanitizeDiagnosticText(msg string, n int) string {
	return diagredact.Sanitize(msg, n)
}

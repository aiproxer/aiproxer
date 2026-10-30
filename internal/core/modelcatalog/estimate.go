package modelcatalog

import (
	"context"
	"math"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EstimateBasis documents how [SizeEstimate.Input] was derived (requirement 7.7).
const (
	EstimateBasisCanonicalUTF8                  = "canonical_utf8_bytes"
	EstimateBasisCanonicalUTF8AndTools          = "canonical_utf8_bytes+tools_json_bytes"
	EstimateBasisCanonicalUTF8AndSession        = "canonical_utf8_bytes+session_bytes"
	EstimateBasisSessionContributionUnavailable = "session_contribution_unavailable"
)

// sizeEstimator is the internal estimate hook used by [EligibilityResolverImpl].
type sizeEstimator interface {
	Estimate(ctx context.Context, call aipapi.Call) SizeEstimate
}

// SizeEstimate is a deterministic, diagnostics-friendly size view (design §SizeEstimate).
type SizeEstimate struct {
	Available bool
	Units     string
	Input     int64
	Basis     string
}

type ctxKeySessionSize struct{}

// WithSessionSizeContribution attaches a proxy-known byte contribution for resumed or continuous sessions.
// When the call carries session or continuity hints, this value must be present for an available estimate.
func WithSessionSizeContribution(ctx context.Context, contributionBytes int64) context.Context {
	return context.WithValue(ctx, ctxKeySessionSize{}, contributionBytes)
}

// addSaturatingInt64 returns a+b clamped at math.MaxInt64 so size estimates never wrap negative on overflow.
func addSaturatingInt64(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func sessionSizeContributionFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	v := ctx.Value(ctxKeySessionSize{})
	if v == nil {
		return 0, false
	}
	n, ok := v.(int64)
	return n, ok
}

// DefaultSizeEstimator implements conservative sizing using UTF-8 byte counts of canonical message content
// plus tool declaration JSON-ish footprint. Non-text parts use deterministic byte proxies.
type DefaultSizeEstimator struct{}

var _ sizeEstimator = DefaultSizeEstimator{}

func (DefaultSizeEstimator) Estimate(ctx context.Context, call aipapi.Call) SizeEstimate {
	body := canonicalMessageBytes(call)
	if call.HasItemAuthority() {
		body = addSaturatingInt64(body, canonicalItemBytes(call))
	}
	if call.Decision != nil {
		body = addSaturatingInt64(body, int64(len(call.Decision.Evidence)))
		for _, q := range call.Decision.Questions {
			body = addSaturatingInt64(body, int64(len(q.ID)))
			body = addSaturatingInt64(body, int64(len(q.Instructions)))
			body = addSaturatingInt64(body, int64(len(q.TrueCriteria)))
			body = addSaturatingInt64(body, int64(len(q.FalseCriteria)))
			for _, option := range q.Options {
				body = addSaturatingInt64(body, int64(len(option.Name)))
				body = addSaturatingInt64(body, int64(len(option.Description)))
			}
			for _, level := range q.Levels {
				body = addSaturatingInt64(body, int64(len(level)))
			}
		}
	}
	tools := toolsFootprintBytes(call.Tools)

	if needsSessionContribution(call.Session) {
		extra, ok := sessionSizeContributionFromContext(ctx)
		if !ok {
			return SizeEstimate{Available: false, Units: "bytes", Input: 0, Basis: EstimateBasisSessionContributionUnavailable}
		}
		total := addSaturatingInt64(addSaturatingInt64(body, tools), extra)
		basis := estimateBasisForSessionPath(tools > 0)
		return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: basis}
	}

	total := addSaturatingInt64(body, tools)
	if tools > 0 {
		return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: EstimateBasisCanonicalUTF8AndTools}
	}
	return SizeEstimate{Available: true, Units: "bytes", Input: total, Basis: EstimateBasisCanonicalUTF8}
}

func (e DefaultSizeEstimator) EstimateRequestTokens(ctx context.Context, call aipapi.Call) SizeEstimate {
	return e.Estimate(ctx, call)
}

func estimateBasisForSessionPath(hasTools bool) string {
	if hasTools {
		return EstimateBasisCanonicalUTF8AndTools + "+session_bytes"
	}
	return EstimateBasisCanonicalUTF8AndSession
}

func needsSessionContribution(s aipapi.SessionRef) bool {
	// Only resume-token turns imply additional upstream transcript bytes beyond this request's
	// canonical envelope. Continuity and authoritative session ids are correlation hints and do
	// not by themselves imply unknown size unless a caller attaches [WithSessionSizeContribution].
	//
	// TODO: Wire [WithSessionSizeContribution] from the executor when the proxy has a conservative
	// byte estimate for resumed session context (e.g. from secure-session transcript accounting).
	// Until then, resume turns report EstimateBasisSessionContributionUnavailable (Req 7.5 safe).
	if strings.TrimSpace(s.ResumeToken) != "" {
		return true
	}
	return false
}

func canonicalMessageBytes(call aipapi.Call) int64 {
	var n int64
	for _, m := range call.Instructions {
		n = addSaturatingInt64(n, messageBytes(m))
	}
	for _, m := range call.Messages {
		n = addSaturatingInt64(n, messageBytes(m))
	}
	return n
}

// canonicalItemBytes sizes item-authority content, not retained Go memory.
// The item trajectory is the request content when Call.Items carries authority;
// Instructions and Messages are empty then. Item.PayloadBytes is deliberately
// NOT used here: it charges struct and slice storage, which is unrelated to the
// bytes sent to the model and can falsely exceed a context limit for many small
// items.
func canonicalItemBytes(call aipapi.Call) int64 {
	var n int64
	for i := range call.Items {
		n = addSaturatingInt64(n, itemContentBytes(call.Items[i]))
	}
	return n
}

func itemContentBytes(item aipapi.Item) int64 {
	n := int64(len(item.ID) + len(item.Role))
	for i := range item.Content {
		n += contentPartContentBytes(item.Content[i])
	}
	if item.Reference != nil {
		n += int64(len(item.Reference.ID))
	}
	if item.ToolCall != nil {
		n += int64(len(item.ToolCall.CallID) + len(item.ToolCall.Name) + len(item.ToolCall.Arguments))
	}
	if item.ToolResult != nil {
		n += int64(len(item.ToolResult.CallID) + len(item.ToolResult.Name) + len(item.ToolResult.Output))
		for i := range item.ToolResult.Parts {
			n += contentPartContentBytes(item.ToolResult.Parts[i])
		}
	}
	if item.Reasoning != nil {
		n += int64(aipapi.ReasoningPayloadBytes(item.Reasoning.Reasoning))
	}
	if item.Compaction != nil {
		n += int64(len(item.Compaction.EncapsulatedID) + len(item.Compaction.Dialect) +
			len(item.Compaction.Implementor) + len(item.Compaction.EncryptedContent) + len(item.Compaction.Opaque))
	}
	if item.Extension != nil {
		n += int64(len(item.Extension.Namespace) + len(item.Extension.Type) +
			len(item.Extension.Implementor) + len(item.Extension.Direction) + len(item.Extension.Data))
	}
	return n
}

func contentPartContentBytes(part aipapi.ContentPart) int64 {
	n := int64(len(part.Text) + len(part.ImageRef) + len(part.ImageMIME) +
		len(part.FileRef) + len(part.FileData) + len(part.FileMIME) + len(part.FileName) +
		len(part.VideoRef) + len(part.VideoMIME) + len(part.Refusal) + len(part.Summary) + len(part.AssistantRef))
	if part.Reasoning != nil {
		n += int64(aipapi.ReasoningPayloadBytes(part.Reasoning))
	}
	if part.Annotation != nil {
		n += int64(len(part.Annotation.Type) + len(part.Annotation.Data))
	}
	if part.Extension != nil {
		n += int64(len(part.Extension.Namespace) + len(part.Extension.Type) +
			len(part.Extension.Implementor) + len(part.Extension.Data))
	}
	return n
}

func messageBytes(m aipapi.Message) int64 {
	var n int64
	for _, p := range m.Parts {
		n = addSaturatingInt64(n, partBytes(p))
	}
	return n
}

func partBytes(p aipapi.Part) int64 {
	switch p.Kind {
	case aipapi.PartText:
		return int64(len(p.Text))
	case aipapi.PartJSON:
		return int64(len(p.Content))
	case aipapi.PartToolResult:
		// The literal result is the primary carrier for message-authority
		// histories; omitting it lets a large tool result pass context-limit
		// eligibility as if it were only its call identity.
		return int64(len(p.Text)) + int64(len(p.Content)) + int64(len(p.ToolCallID)) + int64(len(p.ToolName))
	case aipapi.PartImageRef:
		return int64(len(p.ImageRef)) + int64(len(p.ImageMIME))
	case aipapi.PartFileRef:
		return int64(len(p.FileRef)) + int64(len(p.FileMIME)) + int64(len(p.FileName))
	case aipapi.PartReasoning:
		// Dialect is catalog metadata; sizing counts replay payload bytes only (text+signature+opaque).
		return int64(aipapi.ReasoningPayloadBytes(p.Reasoning))
	default:
		return 0
	}
}

func toolsFootprintBytes(tools []aipapi.ToolDef) int64 {
	var n int64
	for _, t := range tools {
		n = addSaturatingInt64(n, int64(len(t.Name)))
		n = addSaturatingInt64(n, int64(len(t.Description)))
		n = addSaturatingInt64(n, int64(len(t.Parameters)))
	}
	return n
}

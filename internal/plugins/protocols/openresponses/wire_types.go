package openresponses

import (
	"encoding/json"
	"time"
)

// UsageStats carries token count statistics for building ResponseResource / CompactResource.
type UsageStats struct {
	InputTokens     int
	CachedTokens    int
	OutputTokens    int
	ReasoningTokens int
	TotalTokens     int
}

// WireResponseParam represents the wire request body for OpenResponses POST /responses.
type WireResponseParam struct {
	Model                *string         `json:"model,omitempty"`
	Input                json.RawMessage `json:"input"`
	Instructions         *string         `json:"instructions,omitempty"`
	Tools                []WireTool      `json:"tools,omitempty"`
	ToolChoice           json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool           `json:"parallel_tool_calls,omitempty"`
	Temperature          *float64        `json:"temperature,omitempty"`
	TopP                 *float64        `json:"top_p,omitempty"`
	MaxOutputTokens      *int            `json:"max_output_tokens,omitempty"`
	MaxToolCalls         *int            `json:"max_tool_calls,omitempty"`
	Truncation           *string         `json:"truncation,omitempty"`
	Text                 json.RawMessage `json:"text,omitempty"`
	Reasoning            json.RawMessage `json:"reasoning,omitempty"`
	Store                *bool           `json:"store,omitempty"`
	Background           *bool           `json:"background,omitempty"`
	PreviousResponseID   *string         `json:"previous_response_id,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	ServiceTier          *string         `json:"service_tier,omitempty"`
	SafetyIdentifier     *string         `json:"safety_identifier,omitempty"`
	PromptCacheKey       *string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string         `json:"prompt_cache_retention,omitempty"`
	// Include selects additional fields to include in the response resource.
	Include []string `json:"include,omitempty"`
	// PresencePenalty/FrequencyPenalty/TopLogprobs are pinned standard sampling
	// controls the canonical call cannot represent; non-null values are rejected
	// before network by frontend admission.
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	TopLogprobs      *int     `json:"top_logprobs,omitempty"`
	// StreamOptions is the pinned standard SSE transport control. It is a typed
	// known field; non-null values are rejected before network because the
	// canonical call has no lossless carrier.
	StreamOptions json.RawMessage            `json:"stream_options,omitempty"`
	ExtraFields   map[string]json.RawMessage `json:"-"`
}

// WireItem represents a single item on the wire in OpenResponses.
type WireItem struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type"`
	Status    string          `json:"status,omitempty"`
	Role      string          `json:"role,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Reasoning json.RawMessage `json:"reasoning,omitempty"`
	// Summary is the official reasoning-item summary array. Content is reused
	// for the official reasoning-item content array and for message content.
	Summary json.RawMessage `json:"summary,omitempty"`
	// Signature preserves canonical reasoning integrity metadata that is not
	// represented by the legacy text-only reasoning field.
	Signature string `json:"signature,omitempty"`
	// ReasoningEncryptedContent carries exact reasoning-item encrypted_content
	// presence without changing the legacy string compaction field below.
	ReasoningEncryptedContent        json.RawMessage `json:"-"`
	ReasoningEncryptedContentPresent bool            `json:"-"`
	SummaryPresent                   bool            `json:"-"`
	ContentPresent                   bool            `json:"-"`

	EncapsulatedID string `json:"encapsulated_id,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
	Implementor    string `json:"implementor,omitempty"`
	// EncryptedContent is the pinned-profile compaction blob carried on a
	// response.compaction output item.
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Opaque           json.RawMessage `json:"opaque,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`
	Direction        string          `json:"direction,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`
}

// UnmarshalJSON preserves reasoning encrypted_content, including explicit null,
// while retaining the legacy string field used by compaction items.
func (w *WireItem) UnmarshalJSON(data []byte) error {
	type alias WireItem
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	enc := raw["encrypted_content"]
	summary, summaryPresent := raw["summary"]
	content, contentPresent := raw["content"]
	delete(raw, "encrypted_content")
	base, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var decoded alias
	if err := json.Unmarshal(base, &decoded); err != nil {
		return err
	}
	*w = WireItem(decoded)
	if w.Type == "reasoning" {
		w.SummaryPresent = summaryPresent
		w.ContentPresent = contentPresent
		if summaryPresent {
			w.Summary = append(json.RawMessage(nil), summary...)
		}
		if contentPresent {
			w.Content = append(json.RawMessage(nil), content...)
		}
	}
	if len(enc) > 0 {
		if w.Type == "reasoning" {
			w.ReasoningEncryptedContentPresent = true
			w.ReasoningEncryptedContent = append(json.RawMessage(nil), enc...)
		} else if err := json.Unmarshal(enc, &w.EncryptedContent); err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON emits official reasoning encrypted_content presence without
// affecting compaction's legacy string field.
func (w WireItem) MarshalJSON() ([]byte, error) {
	type alias WireItem
	base, err := json.Marshal(alias(w))
	if err != nil {
		return nil, err
	}
	if w.Type != "reasoning" || (!w.ReasoningEncryptedContentPresent && !w.SummaryPresent && !w.ContentPresent) {
		return base, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(base, &raw); err != nil {
		return nil, err
	}
	if w.SummaryPresent {
		if len(w.Summary) == 0 {
			raw["summary"] = json.RawMessage("null")
		} else {
			raw["summary"] = append(json.RawMessage(nil), w.Summary...)
		}
	}
	if w.ContentPresent {
		if len(w.Content) == 0 {
			raw["content"] = json.RawMessage("null")
		} else {
			raw["content"] = append(json.RawMessage(nil), w.Content...)
		}
	}
	if w.ReasoningEncryptedContentPresent {
		if len(w.ReasoningEncryptedContent) == 0 {
			raw["encrypted_content"] = json.RawMessage("null")
		} else {
			raw["encrypted_content"] = append(json.RawMessage(nil), w.ReasoningEncryptedContent...)
		}
	}
	return json.Marshal(raw)
}

// WireContentPart represents a content part inside a message or tool result on the wire.
type WireContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url,omitempty"`
	// FileID and VideoData capture wire fields that the pinned 2026-04-24
	// profile does not define. The decoder rejects non-null values before
	// canonical construction so an unpinned file/video carrier is never silently
	// dropped; the fields exist only to prove presence and are never emitted.
	FileID    json.RawMessage `json:"file_id,omitempty"`
	FileURL   json.RawMessage `json:"file_url,omitempty"`
	FileData  json.RawMessage `json:"file_data,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	VideoURL  json.RawMessage `json:"video_url,omitempty"`
	VideoData json.RawMessage `json:"video_data,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`

	Reasoning    json.RawMessage `json:"reasoning,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	AssistantRef string          `json:"assistant_ref,omitempty"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
	// rawExtension carries the verbatim wire object of a vendor-prefixed custom
	// content part so encoding preserves the structured payload losslessly
	// instead of re-flattening it through the typed struct.
	rawExtension json.RawMessage `json:"-"`
}

// MarshalJSON emits a prefixed custom content part verbatim when the decoder
// preserved its raw structured payload; otherwise it renders the typed struct.
func (p WireContentPart) MarshalJSON() ([]byte, error) {
	if len(p.rawExtension) > 0 {
		return p.rawExtension, nil
	}
	type alias WireContentPart
	return json.Marshal(alias(p))
}

// WireTool represents a tool definition on the wire.
type WireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// WireUsageInputDetails captures known input token detail counters.
type WireUsageInputDetails struct {
	CachedTokens        int  `json:"cached_tokens"`
	TextTokens          int  `json:"text_tokens,omitempty"`
	AudioTokens         int  `json:"audio_tokens,omitempty"`
	Images              int  `json:"images,omitempty"`
	CachedTokensPresent bool `json:"-"`
	TextTokensPresent   bool `json:"-"`
	AudioTokensPresent  bool `json:"-"`
	ImagesPresent       bool `json:"-"`
}

// WireUsageOutputDetails captures known output token detail counters.
type WireUsageOutputDetails struct {
	ReasoningTokens        int  `json:"reasoning_tokens"`
	TextTokens             int  `json:"text_tokens,omitempty"`
	AudioTokens            int  `json:"audio_tokens,omitempty"`
	Images                 int  `json:"images,omitempty"`
	ReasoningTokensPresent bool `json:"-"`
	TextTokensPresent      bool `json:"-"`
	AudioTokensPresent     bool `json:"-"`
	ImagesPresent          bool `json:"-"`
}

// WireUsage represents token usage statistics on the wire.
type WireUsage struct {
	InputTokens         int                    `json:"input_tokens"`
	InputTokensDetails  WireUsageInputDetails  `json:"input_tokens_details"`
	OutputTokens        int                    `json:"output_tokens"`
	OutputTokensDetails WireUsageOutputDetails `json:"output_tokens_details"`
	TotalTokens         int                    `json:"total_tokens"`
	InputTokensPresent  bool                   `json:"-"`
	OutputTokensPresent bool                   `json:"-"`
	TotalTokensPresent  bool                   `json:"-"`
	RawJSON             json.RawMessage        `json:"-"`
}

func (d *WireUsageInputDetails) UnmarshalJSON(data []byte) error {
	type alias WireUsageInputDetails
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*d = WireUsageInputDetails(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, d.CachedTokensPresent = fields["cached_tokens"]
	_, d.TextTokensPresent = fields["text_tokens"]
	_, d.AudioTokensPresent = fields["audio_tokens"]
	_, d.ImagesPresent = fields["images"]
	return nil
}

func (d *WireUsageOutputDetails) UnmarshalJSON(data []byte) error {
	type alias WireUsageOutputDetails
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*d = WireUsageOutputDetails(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, d.ReasoningTokensPresent = fields["reasoning_tokens"]
	_, d.TextTokensPresent = fields["text_tokens"]
	_, d.AudioTokensPresent = fields["audio_tokens"]
	_, d.ImagesPresent = fields["images"]
	return nil
}

func (u *WireUsage) UnmarshalJSON(data []byte) error {
	type alias WireUsage
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*u = WireUsage(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, u.InputTokensPresent = fields["input_tokens"]
	_, u.OutputTokensPresent = fields["output_tokens"]
	_, u.TotalTokensPresent = fields["total_tokens"]
	u.RawJSON = append(u.RawJSON[:0], data...)
	return nil
}

// WireToolChoiceFunction is the official direct object form of a required
// function tool choice: {"type":"function","name":"..."}.
type WireToolChoiceFunction struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// WireToolChoiceFunctionName is retained as a decode-only compatibility shape
// for older nested function tool_choice payloads.
type WireToolChoiceFunctionName struct {
	Name string `json:"name"`
}

// WireToolChoiceAllowedTools is the object form of an allowed_tools tool choice:
// it restricts which tools the model may invoke to the named subset while the
// full Tools list stays visible (cache-preserving control surface). Mode is one
// of "auto", "none", or "required"; empty means auto.
type WireToolChoiceAllowedTools struct {
	Type  string                         `json:"type"`
	Tools []WireToolChoiceAllowedToolRef `json:"tools"`
	Mode  string                         `json:"mode,omitempty"`
}

// WireToolChoiceAllowedToolRef names one tool in an allowed_tools subset.
type WireToolChoiceAllowedToolRef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// WireResponseResource represents the complete OpenResponses response resource.
type WireResponseResource struct {
	ID                   string          `json:"id"`
	Object               string          `json:"object"`
	CreatedAt            int64           `json:"created_at"`
	Status               string          `json:"status"`
	CompletedAt          *int64          `json:"completed_at"`
	Model                string          `json:"model"`
	Output               []WireItem      `json:"output"`
	ParallelToolCalls    bool            `json:"parallel_tool_calls"`
	Reasoning            json.RawMessage `json:"reasoning"`
	Store                bool            `json:"store"`
	Background           bool            `json:"background"`
	Temperature          *float64        `json:"temperature"`
	Text                 map[string]any  `json:"text"`
	ToolChoice           json.RawMessage `json:"tool_choice"`
	Tools                []WireTool      `json:"tools"`
	TopP                 *float64        `json:"top_p"`
	PresencePenalty      float64         `json:"presence_penalty"`
	FrequencyPenalty     float64         `json:"frequency_penalty"`
	TopLogprobs          int             `json:"top_logprobs"`
	Truncation           string          `json:"truncation"`
	Usage                WireUsage       `json:"usage"`
	Metadata             map[string]any  `json:"metadata"`
	ServiceTier          string          `json:"service_tier"`
	MaxOutputTokens      *int            `json:"max_output_tokens"`
	MaxToolCalls         *int            `json:"max_tool_calls"`
	Instructions         *string         `json:"instructions"`
	PreviousResponseID   *string         `json:"previous_response_id"`
	Error                json.RawMessage `json:"error"`
	IncompleteDetails    json.RawMessage `json:"incomplete_details"`
	SafetyIdentifier     *string         `json:"safety_identifier"`
	PromptCacheKey       *string         `json:"prompt_cache_key"`
	PromptCacheRetention *string         `json:"prompt_cache_retention"`
	UsagePresent         bool            `json:"-"`
	RawJSON              json.RawMessage `json:"-"`
}

func (r *WireResponseResource) UnmarshalJSON(data []byte) error {
	type alias WireResponseResource
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = WireResponseResource(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["usage"]; ok && string(raw) != "null" {
		// Preserve explicit zero counters. The nested decoder records field
		// presence independently from values, so an all-zero provider usage
		// object is evidence when its counters were actually surfaced.
		r.UsagePresent = usageHasValue(r.Usage) || usageHasPresence(r.Usage)
	}
	r.RawJSON = append(r.RawJSON[:0], data...)
	return nil
}

// WireCompactResource represents the OpenResponses response.compaction resource.
type WireCompactResource struct {
	ID           string          `json:"id"`
	Object       string          `json:"object"`
	CreatedAt    int64           `json:"created_at"`
	Status       string          `json:"status"`
	Model        string          `json:"model"`
	Output       []WireItem      `json:"output"`
	Usage        WireUsage       `json:"usage"`
	UsagePresent bool            `json:"-"`
	RawJSON      json.RawMessage `json:"-"`
}

func (r *WireCompactResource) UnmarshalJSON(data []byte) error {
	type alias WireCompactResource
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = WireCompactResource(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["usage"]; ok && string(raw) != "null" {
		r.UsagePresent = usageHasValue(r.Usage) || usageHasPresence(r.Usage)
	}
	r.RawJSON = append(r.RawJSON[:0], data...)
	return nil
}

func usageHasValue(u WireUsage) bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.TotalTokens != 0 ||
		u.InputTokensDetails.CachedTokens != 0 || u.InputTokensDetails.TextTokens != 0 ||
		u.InputTokensDetails.AudioTokens != 0 || u.InputTokensDetails.Images != 0 ||
		u.OutputTokensDetails.ReasoningTokens != 0 || u.OutputTokensDetails.TextTokens != 0 ||
		u.OutputTokensDetails.AudioTokens != 0 || u.OutputTokensDetails.Images != 0
}

func usageHasPresence(u WireUsage) bool {
	return u.InputTokensPresent || u.OutputTokensPresent || u.TotalTokensPresent ||
		u.InputTokensDetails.CachedTokensPresent || u.InputTokensDetails.TextTokensPresent ||
		u.InputTokensDetails.AudioTokensPresent || u.InputTokensDetails.ImagesPresent ||
		u.OutputTokensDetails.ReasoningTokensPresent || u.OutputTokensDetails.TextTokensPresent ||
		u.OutputTokensDetails.AudioTokensPresent || u.OutputTokensDetails.ImagesPresent
}

// EnvelopeMetadata carries proxy-owned response metadata for resource construction.
type EnvelopeMetadata struct {
	ResponseID         string
	PreviousResponseID string
	CreatedAt          time.Time
	CompletedAt        *time.Time
	Model              string
	Status             string
	Store              *bool
	// IncompleteReason carries a terminal refusal lexeme (content_filter)
	// recorded from the canonical ResponseFinished event. When set, the
	// resource surfaces incomplete_details with that reason while the
	// status contract stays untouched (a refusal is complete, never a
	// truncation). Empty means absent: incomplete_details stays null.
	IncompleteReason string
}

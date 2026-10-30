package aipapi

import (
	"slices"
	"strings"
)

// CapabilitySet is a declared capability bundle for a plugin registration surface.
type CapabilitySet struct {
	Provides []Capability
}

// Capability names the semantic features a frontend call may require and a backend may provide.
type Capability string

// AttemptPolicy decides what an attempt candidate does when it cannot
// replay the dialect of client-sent reasoning items. It reuses the existing
// reject | log_skip vocabulary; no second knob exists for the same choice.
//
// The zero value ("") stays hard (reject): only an explicitly resolved
// log_skip softens reasoning_replay, per attempt only. The standard
// distribution resolves unset to DefaultAttemptPolicy (log_skip);
// aipapi itself never invents that default for callers that do not opt in.
type AttemptPolicy string

const (
	AttemptLogSkip AttemptPolicy = "log_skip"
	AttemptReject  AttemptPolicy = "reject"

	// DefaultAttemptPolicy is the standard-distribution default for
	// client-sent reasoning. It explicitly reverses the archived
	// openai-responses-reasoning-preservation requirement line 34 ("hard
	// reasoning_replay capability semantics shall remain non-downgradeable")
	// for client-sent reasoning only.
	DefaultAttemptPolicy = AttemptLogSkip
)

// allowsSkip reports whether the policy softens unrepresentable client-sent
// reasoning into a per-attempt strip. Empty and unknown values stay hard
// (fail closed); only an explicit log_skip softens.
func (p AttemptPolicy) allowsSkip() bool {
	switch AttemptPolicy(strings.TrimSpace(strings.ToLower(string(p)))) {
	case AttemptLogSkip:
		return true
	default:
		return false
	}
}

const (
	// CapabilityDecisions requires faithful typed decision evaluation.
	CapabilityDecisions         Capability = "decisions"
	CapabilityStreaming         Capability = "streaming"
	CapabilityTools             Capability = "tools"
	CapabilityVision            Capability = "vision"
	CapabilityDocuments         Capability = "documents"
	CapabilityStructuredOutputs Capability = "structured_outputs"
	CapabilityReasoning         Capability = "reasoning"
	CapabilityReasoningReplay   Capability = "reasoning_replay" // hard; required when historical reasoning parts are present
	CapabilityParallelToolCalls Capability = "parallel_tool_calls"

	// OpenResponses ordered-item semantic capabilities (Task 1.4).
	CapabilityOrderedItems       Capability = "ordered_items"
	CapabilityAssistantPhase     Capability = "assistant_phase"
	CapabilityVideoInput         Capability = "video_input"
	CapabilityItemReferences     Capability = "item_references"
	CapabilityCompaction         Capability = "compaction"
	CapabilityOpaqueExtensions   Capability = "opaque_extensions"
	CapabilityAnnotations        Capability = "annotations"
	CapabilityAssistantMediaRefs Capability = "assistant_media_refs"
)

// BackendCaps is a set of capabilities supported by a backend adapter instance.
type BackendCaps map[Capability]struct{}

// NewBackendCaps builds a set for negotiation helpers and tests.
func NewBackendCaps(caps ...Capability) BackendCaps {
	m := make(BackendCaps, len(caps))
	for _, c := range caps {
		m[c] = struct{}{}
	}
	return m
}

// NegotiationKind classifies the outcome of comparing a call against backend capabilities.
type NegotiationKind string

const (
	NegotiationLossless  NegotiationKind = "lossless"
	NegotiationDowngrade NegotiationKind = "downgrade"
	NegotiationReject    NegotiationKind = "reject"
)

// NegotiationResult is a deterministic capability negotiation outcome.
type NegotiationResult struct {
	Kind       NegotiationKind
	Missing    []Capability // hard rejects only
	Downgraded []Capability // capabilities that will be stripped or softened
}

// Err returns a typed reject error for Kind==NegotiationReject, otherwise nil.
func (r NegotiationResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	return &RejectError{Missing: append([]Capability{}, r.Missing...)}
}

// RequiredCapabilities derives required capabilities from call shape.
func RequiredCapabilities(c Call) []Capability {
	out := []Capability{}
	add := func(capability Capability) {
		if slices.Contains(out, capability) {
			return
		}
		out = append(out, capability)
	}

	scanMessageParts := func(msgs []Message) {
		for _, m := range msgs {
			for _, p := range m.Parts {
				if p.Kind == PartImageRef {
					add(CapabilityVision)
				}
				if p.Kind == PartFileRef {
					add(CapabilityDocuments)
				}
				if p.Kind == PartReasoning {
					add(CapabilityReasoningReplay)
				}
			}
		}
	}
	if c.Decision != nil {
		add(CapabilityDecisions)
	}
	if c.HasItemAuthority() {
		for _, item := range NormalizedItems(c) {
			scanParts := func(parts []ContentPart) {
				for _, cp := range parts {
					switch cp.Kind {
					case ContentPartImageRef:
						add(CapabilityVision)
					case ContentPartFileRef:
						add(CapabilityDocuments)
					case ContentPartVideoRef:
						add(CapabilityVideoInput)
					case ContentPartReasoning:
						add(CapabilityReasoningReplay)
					case ContentPartExtension:
						add(CapabilityOpaqueExtensions)
					}
				}
			}
			scanParts(item.Content)
			if item.ToolResult != nil {
				scanParts(item.ToolResult.Parts)
			}
			if item.Kind == ItemKindToolCall || item.Kind == ItemKindToolResult {
				add(CapabilityTools)
			}
		}
		add(CapabilityOrderedItems)
	} else {
		scanMessageParts(c.Instructions)
		scanMessageParts(c.Messages)
		for _, m := range c.Messages {
			if m.Role == RoleAssistant {
				for _, p := range m.Parts {
					if p.Kind == PartJSON && p.ToolCallID != "" {
						add(CapabilityTools)
					}
				}
			}
			if m.Role == RoleTool {
				for _, p := range m.Parts {
					if p.Kind == PartToolResult {
						add(CapabilityTools)
					}
				}
			}
		}
	}
	if len(c.Tools) > 0 {
		add(CapabilityTools)
	}
	if c.Options.ResponseMIMEType != "" {
		add(CapabilityStructuredOutputs)
	}
	if c.Options.ReasoningEffort != "" {
		add(CapabilityReasoning)
	}
	if c.Options.ParallelToolCalls != nil && *c.Options.ParallelToolCalls {
		add(CapabilityParallelToolCalls)
	}
	return out
}

// Negotiate compares required capabilities with backend-provided capabilities.
//
// Downgrade is returned only when every missing capability is explicitly soft:
// reasoning and parallel tool calls may be stripped by the executor before upstream calls.
// Any other missing capability is a hard reject before upstream work begins.
func Negotiate(required []Capability, backend BackendCaps) NegotiationResult {
	missing := []Capability{}
	for _, r := range required {
		if backend == nil {
			missing = append(missing, r)
			continue
		}
		if _, ok := backend[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return NegotiationResult{Kind: NegotiationLossless}
	}

	downgradable := map[Capability]struct{}{
		CapabilityReasoning:         {},
		CapabilityParallelToolCalls: {},
	}

	soft := []Capability{}
	hard := []Capability{}
	for _, m := range missing {
		if _, ok := downgradable[m]; ok {
			soft = append(soft, m)
			continue
		}
		hard = append(hard, m)
	}
	if len(hard) > 0 {
		return NegotiationResult{Kind: NegotiationReject, Missing: hard}
	}
	return NegotiationResult{Kind: NegotiationDowngrade, Downgraded: soft}
}

// NegotiateWithAttemptPolicy compares required capabilities with
// backend-provided capabilities, extending the downgradable set to cover
// reasoning_replay only when the attempt policy is log_skip. Under reject
// (or empty/unknown) the capability stays hard, identical to Negotiate.
// Lossless candidates are unaffected: no missing capability means no
// downgrade regardless of the policy. reasoning_replay is never globally
// soft: only callers that explicitly pass log_skip observe the downgrade.
func NegotiateWithAttemptPolicy(required []Capability, backend BackendCaps, policy AttemptPolicy) NegotiationResult {
	missing := []Capability{}
	for _, r := range required {
		if backend == nil {
			missing = append(missing, r)
			continue
		}
		if _, ok := backend[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return NegotiationResult{Kind: NegotiationLossless}
	}

	downgradable := map[Capability]struct{}{
		CapabilityReasoning:         {},
		CapabilityParallelToolCalls: {},
	}
	if policy.allowsSkip() {
		downgradable[CapabilityReasoningReplay] = struct{}{}
	}

	soft := []Capability{}
	hard := []Capability{}
	for _, m := range missing {
		if _, ok := downgradable[m]; ok {
			soft = append(soft, m)
			continue
		}
		hard = append(hard, m)
	}
	if len(hard) > 0 {
		return NegotiationResult{Kind: NegotiationReject, Missing: hard}
	}
	return NegotiationResult{Kind: NegotiationDowngrade, Downgraded: soft}
}

// StripReasoningForAttempt removes client-sent reasoning parts from the
// per-attempt clone only. Every non-reasoning item keeps its bytes and its
// order; emptied messages and items dissolve (omission, never reorder). It
// returns whether anything was removed. Callers must pass a per-attempt clone
// (CloneCall of the baseline); the shared call is never mutated here.
func StripReasoningForAttempt(c *Call) bool {
	if c == nil {
		return false
	}
	stripped := false
	if c.HasItemAuthority() {
		kept := c.Items[:0]
		for _, item := range c.Items {
			if item.Kind == ItemKindReasoning {
				stripped = true
				continue
			}
			if stripContentParts(item.Content) {
				stripped = true
				item.Content = withoutReasoningParts(item.Content)
			}
			if item.ToolResult != nil && stripContentParts(item.ToolResult.Parts) {
				stripped = true
				item.ToolResult.Parts = withoutReasoningParts(item.ToolResult.Parts)
			}
			if itemHasPayload(item) {
				kept = append(kept, item)
			} else {
				stripped = true
			}
		}
		// Zero the tail so a dissolved item's backing array is not retained.
		for i := len(kept); i < len(c.Items); i++ {
			c.Items[i] = Item{}
		}
		c.Items = kept
	} else {
		c.Instructions = stripMessageReasoning(c.Instructions, &stripped)
		c.Messages = stripMessageReasoning(c.Messages, &stripped)
	}
	return stripped
}

// stripContentParts reports whether parts carry reasoning.
func stripContentParts(parts []ContentPart) bool {
	for _, cp := range parts {
		if cp.Kind == ContentPartReasoning {
			return true
		}
	}
	return false
}

// withoutReasoningParts returns parts minus reasoning parts, order preserved.
func withoutReasoningParts(parts []ContentPart) []ContentPart {
	kept := parts[:0]
	for _, cp := range parts {
		if cp.Kind == ContentPartReasoning {
			continue
		}
		kept = append(kept, cp)
	}
	for i := len(kept); i < len(parts); i++ {
		parts[i] = ContentPart{}
	}
	return kept
}

// itemHasPayload reports whether an item still carries non-reasoning payload
// after a strip. Reasoning-only residue never survives on its own.
func itemHasPayload(item Item) bool {
	if len(item.Content) > 0 || item.ToolCall != nil || item.ToolResult != nil ||
		item.Reference != nil || item.Compaction != nil || item.Extension != nil {
		return true
	}
	// A reasoning item whose payload was stripped above dissolves.
	if item.Kind == ItemKindReasoning {
		return false
	}
	// Structural items (message/tool_call/tool_result shells) keep their
	// identity even when their content list emptied; the backend adapters
	// already tolerate empty content lists on those kinds.
	return true
}

// ApplyNegotiatedDowngrades mutates c.Options to strip capabilities classified as soft
// downgrade by Negotiate. Call only when NegotiationResult.Kind == NegotiationDowngrade.
func ApplyNegotiatedDowngrades(c *Call, down NegotiationResult) {
	if c == nil || down.Kind != NegotiationDowngrade {
		return
	}
	for _, capability := range down.Downgraded {
		switch capability {
		case CapabilityReasoning:
			c.Options.ReasoningEffort = ""
		case CapabilityParallelToolCalls:
			c.Options.ParallelToolCalls = nil
		}
	}
}

// stripMessageReasoning removes reasoning parts from legacy messages,
// dissolving messages left with zero parts. Order is never changed.
func stripMessageReasoning(msgs []Message, stripped *bool) []Message {
	kept := msgs[:0]
	for _, m := range msgs {
		parts := m.Parts[:0]
		for _, p := range m.Parts {
			if p.Kind == PartReasoning {
				*stripped = true
				continue
			}
			parts = append(parts, p)
		}
		for i := len(parts); i < len(m.Parts); i++ {
			m.Parts[i] = Part{}
		}
		m.Parts = parts
		if len(m.Parts) == 0 {
			*stripped = true
			continue
		}
		kept = append(kept, m)
	}
	for i := len(kept); i < len(msgs); i++ {
		msgs[i] = Message{}
	}
	return kept
}

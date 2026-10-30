package aipapi

import (
	"encoding/json"
	"maps"
	"slices"
)

// cloneSlice copies a slice so the clone owns no part of the original backing
// array: nil stays nil, an empty slice becomes a fresh S{} with no reference
// to the original array, and anything else is copied element-wise.
func cloneSlice[S ~[]E, E any](s S) S {
	if s == nil {
		return nil
	}
	if len(s) == 0 {
		return S{}
	}
	return append(S(nil), s...)
}

// CloneCall returns a deep copy of c suitable as an immutable baseline for per-attempt derivation.
func CloneCall(c Call) Call {
	out := c
	out.Instructions = cloneMessages(c.Instructions)
	out.Messages = cloneMessages(c.Messages)
	out.Items = cloneItems(c.Items)
	out.Decision = cloneDecisionRequest(c.Decision)
	out.Tools = cloneTools(c.Tools)
	out.Options = CloneGenerationOptions(c.Options)
	if len(c.ToolChoice.AllowedTools) > 0 {
		out.ToolChoice.AllowedTools = append([]string(nil), c.ToolChoice.AllowedTools...)
	}
	if len(c.Extensions) > 0 {
		out.Extensions = make(map[string]json.RawMessage, len(c.Extensions))
		for k, v := range c.Extensions {
			out.Extensions[k] = append(json.RawMessage(nil), v...)
		}
	}
	if len(c.SemanticExtensions) > 0 {
		out.SemanticExtensions = make([]SemanticExtension, len(c.SemanticExtensions))
		for i, ext := range c.SemanticExtensions {
			out.SemanticExtensions[i] = ext
			out.SemanticExtensions[i].Data = append(json.RawMessage(nil), ext.Data...)
		}
	}
	if c.Session.Metadata != nil {
		out.Session.Metadata = make(map[string]string, len(c.Session.Metadata))
		maps.Copy(out.Session.Metadata, c.Session.Metadata)
	}
	return out
}

func cloneDecisionRequest(in *DecisionRequest) *DecisionRequest {
	if in == nil {
		return nil
	}
	out := *in
	out.Evidence = slices.Clone(in.Evidence)
	out.Questions = slices.Clone(in.Questions)
	for i := range out.Questions {
		q := &out.Questions[i]
		q.Instructions = slices.Clone(q.Instructions)
		q.TrueCriteria = slices.Clone(q.TrueCriteria)
		q.FalseCriteria = slices.Clone(q.FalseCriteria)
		q.Options = slices.Clone(q.Options)
		for j := range q.Options {
			q.Options[j].Description = slices.Clone(q.Options[j].Description)
		}
		q.Levels = slices.Clone(q.Levels)
		for j := range q.Levels {
			q.Levels[j] = slices.Clone(q.Levels[j])
		}
	}
	return &out
}

// CloneGenerationOptions returns a copy with independent pointer fields.
func CloneGenerationOptions(o GenerationOptions) GenerationOptions {
	out := o
	if o.Temperature != nil {
		t := *o.Temperature
		out.Temperature = &t
	}
	if o.MaxOutputTokens != nil {
		n := *o.MaxOutputTokens
		out.MaxOutputTokens = &n
	}
	if o.TopP != nil {
		p := *o.TopP
		out.TopP = &p
	}
	if o.ParallelToolCalls != nil {
		b := *o.ParallelToolCalls
		out.ParallelToolCalls = &b
	}
	return out
}

func cloneMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, len(in))
	for i := range in {
		out[i].Role = in[i].Role
		out[i].Parts = cloneParts(in[i].Parts)
		if in[i].Metadata != nil {
			out[i].Metadata = maps.Clone(in[i].Metadata)
		}
	}
	return out
}

func cloneParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}
	out := make([]Part, len(in))
	copy(out, in)
	for i := range out {
		if len(in[i].Content) > 0 {
			out[i].Content = append(json.RawMessage(nil), in[i].Content...)
		}
		if in[i].Reasoning != nil {
			out[i].Reasoning = cloneReasoningPart(in[i].Reasoning)
		}
	}
	return out
}

func cloneReasoningPart(in *ReasoningPart) *ReasoningPart {
	if in == nil {
		return nil
	}
	out := *in
	out.Opaque = cloneSlice(in.Opaque)
	out.Summary = cloneSlice(in.Summary)
	out.Content = cloneSlice(in.Content)
	out.EncryptedContent = cloneSlice(in.EncryptedContent)
	return &out
}

func cloneTools(in []ToolDef) []ToolDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolDef, len(in))
	for i := range in {
		out[i].Name = in[i].Name
		out[i].Description = in[i].Description
		if len(in[i].Parameters) > 0 {
			out[i].Parameters = append(json.RawMessage(nil), in[i].Parameters...)
		}
	}
	return out
}

func cloneItems(in []Item) []Item {
	if len(in) == 0 {
		return nil
	}
	out := make([]Item, len(in))
	for i := range in {
		out[i] = cloneItemValue(in[i])
	}
	return out
}

// cloneItemValue returns a deep copy of one canonical item. It is the shared
// single-item clone used by call cloning and by event carriers that retain an
// item (EventItem).
func cloneItemValue(in Item) Item {
	out := in
	if len(in.Content) > 0 {
		out.Content = cloneContentParts(in.Content)
	} else {
		out.Content = cloneSlice(in.Content)
	}
	if in.Reference != nil {
		ref := *in.Reference
		out.Reference = &ref
	}
	if in.ToolCall != nil {
		tc := *in.ToolCall
		tc.Arguments = cloneSlice(in.ToolCall.Arguments)
		out.ToolCall = &tc
	}
	if in.ToolResult != nil {
		tr := *in.ToolResult
		if len(in.ToolResult.Parts) > 0 {
			tr.Parts = cloneContentParts(in.ToolResult.Parts)
		} else {
			tr.Parts = cloneSlice(in.ToolResult.Parts)
		}
		out.ToolResult = &tr
	}
	if in.Reasoning != nil {
		ri := *in.Reasoning
		if in.Reasoning.Reasoning != nil {
			ri.Reasoning = cloneReasoningPart(in.Reasoning.Reasoning)
		}
		out.Reasoning = &ri
	}
	if in.Compaction != nil {
		ci := *in.Compaction
		ci.Opaque = cloneSlice(in.Compaction.Opaque)
		out.Compaction = &ci
	}
	if in.Extension != nil {
		ext := *in.Extension
		ext.Data = cloneSlice(in.Extension.Data)
		out.Extension = &ext
	}
	return out
}

func cloneContentParts(in []ContentPart) []ContentPart {
	if len(in) == 0 {
		return nil
	}
	out := make([]ContentPart, len(in))
	copy(out, in)
	for i := range out {
		if in[i].Reasoning != nil {
			out[i].Reasoning = cloneReasoningPart(in[i].Reasoning)
		}
		if in[i].Annotation != nil {
			ann := *in[i].Annotation
			ann.Data = cloneSlice(in[i].Annotation.Data)
			out[i].Annotation = &ann
		}
		if in[i].Extension != nil {
			ext := *in[i].Extension
			ext.Data = cloneSlice(in[i].Extension.Data)
			out[i].Extension = &ext
		}
	}
	return out
}

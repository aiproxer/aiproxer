package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/ssestream"
)

type sliceStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	idx    int
	closed bool
}

func (s *sliceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("openaicompat: stream closed")
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// chatToolTrack mirrors essential openaicompat chatStream index→id tracking so
// finish_reason=="tool_calls" can emit EventToolCallFinished with stable IDs.
type chatToolTrack struct {
	activeTools     map[int64]string
	pendingToolArgs map[int64][]string
	activeToolOrder []int64
}

// responsesTerminal captures the terminal taxonomy across responses SSE
// frames. Only an explicit response.incomplete classifies; completed and
// failed frames leave the terminal clean here (failures surface as errors
// through the upstream_error concern, deferred slice).
type responsesTerminal struct {
	incomplete bool
	reason     string
}

// observe records an incomplete envelope with its vendor reason.
func (r *responsesTerminal) observe(status, reason string) {
	if status == "incomplete" {
		r.incomplete = true
		r.reason = reason
	}
}

// terminalEvent builds the canonical terminal: incomplete with the mapped
// reason (max_output_tokens folds to length, content_filter stays verbatim),
// empty otherwise.
func (r *responsesTerminal) terminalEvent() aipapi.Event {
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	if !r.incomplete {
		return ev
	}
	ev.ResponseStatus = "incomplete"
	switch r.reason {
	case "max_output_tokens":
		ev.FinishReason = "length"
	case "content_filter":
		ev.FinishReason = "content_filter"
	}
	return ev
}

// chatTerminal captures the terminal taxonomy across chat SSE chunks.
// length wins over content_filter; anything else stays clean.
type chatTerminal struct {
	outputLimit bool
	refusal     bool
}

// terminalEvent builds the canonical terminal: incomplete/length for
// output-token-limit, verbatim content_filter for refusal, empty otherwise.
func (c *chatTerminal) terminalEvent() aipapi.Event {
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	switch {
	case c.outputLimit:
		ev.ResponseStatus = "incomplete"
		ev.FinishReason = "length"
	case c.refusal:
		ev.FinishReason = "content_filter"
	}
	return ev
}

// observe records one chunk finish_reason with length precedence.
func (c *chatTerminal) observe(reason string) {
	switch reason {
	case "length":
		c.outputLimit = true
		c.refusal = false
	case "content_filter":
		if !c.outputLimit {
			c.refusal = true
		}
	}
}

// newSSEStream decodes a streaming chat or responses body. Framing, the byte
// cap, and [DONE] handling live in ssestream; this file owns only the payload
// mapping and the terminal rule (finish only after the response started).
func newSSEStream(resp *http.Response, flavor Flavor, maxBytes int64) *ssestream.Stream {
	var (
		started, msgStart bool
		tools             chatToolTrack
		term              chatTerminal
		respTerm          responsesTerminal
		respTools         responsesToolTrack
	)
	return ssestream.New(resp.Body, ssestream.Decoder{
		Data: func(payload []byte) ([]aipapi.Event, error) {
			return decodeSSEData(payload, flavor, &started, &msgStart, &tools, &term, &respTerm, &respTools)
		},
		End: func() []aipapi.Event {
			if !started {
				return nil
			}
			if flavor == FlavorResponses {
				return []aipapi.Event{respTerm.terminalEvent()}
			}
			return []aipapi.Event{term.terminalEvent()}
		},
	}, ssestream.Options{Name: "openaicompat", MaxBytes: maxBytes})
}

func decodeSSEData(raw []byte, flavor Flavor, started, msgStart *bool, tools *chatToolTrack, term *chatTerminal, respTerm *responsesTerminal, respTools *responsesToolTrack) ([]aipapi.Event, error) {
	switch flavor {
	case FlavorResponses:
		return decodeResponsesSSE(raw, started, msgStart, respTerm, respTools)
	default:
		return decodeChatSSE(raw, started, msgStart, tools, term)
	}
}

func decodeChatSSE(raw []byte, started, msgStart *bool, tools *chatToolTrack, term *chatTerminal) ([]aipapi.Event, error) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Index    int64  `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return nil, fmt.Errorf("openaicompat: chat sse: %w", err)
	}
	if tools == nil {
		tools = &chatToolTrack{}
	}
	var out []aipapi.Event
	ensure := func() {
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
	}
	for _, ch := range chunk.Choices {
		d := ch.Delta
		if r := firstNonEmpty(d.Reasoning, d.ReasoningContent); r != "" {
			ensure()
			out = append(out, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: r})
		}
		for _, tc := range d.ToolCalls {
			ensure()
			if tc.ID != "" {
				if tools.activeTools == nil {
					tools.activeTools = make(map[int64]string)
				}
				if _, seen := tools.activeTools[tc.Index]; !seen {
					tools.activeToolOrder = append(tools.activeToolOrder, tc.Index)
				}
				tools.activeTools[tc.Index] = tc.ID
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallStarted, ToolCallID: tc.ID, ToolName: tc.Function.Name,
				})
				for _, args := range tools.pendingToolArgs[tc.Index] {
					out = append(out, aipapi.Event{
						Kind: aipapi.EventToolCallArgsDelta, ToolCallID: tc.ID, Delta: args,
					})
				}
				delete(tools.pendingToolArgs, tc.Index)
			}
			if tc.Function.Arguments != "" {
				id := tools.activeTools[tc.Index]
				if id == "" {
					if tools.pendingToolArgs == nil {
						tools.pendingToolArgs = make(map[int64][]string)
					}
					tools.pendingToolArgs[tc.Index] = append(tools.pendingToolArgs[tc.Index], tc.Function.Arguments)
					continue
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: tc.Function.Arguments,
				})
			}
		}
		if d.Content != "" {
			ensure()
			out = append(out, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: d.Content})
		}
		if term != nil {
			// A terminal-only chunk still opens the frame: an empty turn
			// must terminate instead of vanishing without events.
			if ch.FinishReason != "" {
				ensure()
			}
			term.observe(ch.FinishReason)
		}
		if ch.FinishReason == "tool_calls" {
			for _, idx := range tools.activeToolOrder {
				if id := tools.activeTools[idx]; id != "" {
					out = append(out, aipapi.Event{
						Kind: aipapi.EventToolCallFinished, ToolCallID: id,
					})
				}
			}
			tools.activeTools = nil
			tools.pendingToolArgs = nil
			tools.activeToolOrder = nil
		}
	}
	if chunk.Usage != nil {
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		out = append(out, aipapi.Event{
			Kind:          aipapi.EventUsageDelta,
			InputTokens:   chunk.Usage.PromptTokens,
			OutputTokens:  chunk.Usage.CompletionTokens,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		})
	}
	return out, nil
}

// responsesToolTrack mirrors chatToolTrack for responses function_call
// items so the terminal response.completed can emit EventToolCallFinished
// with stable IDs.
type responsesToolTrack struct {
	activeID    string
	activeName  string
	pendingArgs []string
	finished    bool
}

// finish closes one tracked call: buffered args flush first, then finished.
func (rt *responsesToolTrack) finish(out *[]aipapi.Event) {
	if rt == nil || rt.finished || rt.activeID == "" {
		return
	}
	for _, args := range rt.pendingArgs {
		*out = append(*out, aipapi.Event{
			Kind: aipapi.EventToolCallArgsDelta, ToolCallID: rt.activeID, Delta: args,
		})
	}
	rt.pendingArgs = nil
	*out = append(*out, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: rt.activeID,
	})
	rt.finished = true
}

func decodeResponsesSSE(raw []byte, started, msgStart *bool, respTerm *responsesTerminal, tools *responsesToolTrack) ([]aipapi.Event, error) {
	var envelope struct {
		Type        string          `json:"type"`
		Delta       json.RawMessage `json:"delta"`
		Text        string          `json:"text"`
		OutputIndex int             `json:"output_index"`
		ItemID      string          `json:"item_id"`
		Item        *struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
		Response *struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("openaicompat: responses sse: %w", err)
	}
	var out []aipapi.Event
	switch {
	case envelope.Type == "response.created" || envelope.Type == "response.started":
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
	case strings.Contains(envelope.Type, "output_text.delta"):
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
		text := envelope.Text
		if text == "" && len(envelope.Delta) > 0 {
			_ = json.Unmarshal(envelope.Delta, &text)
			if text == "" {
				text = string(bytes.Trim(envelope.Delta, `"`))
			}
		}
		if text != "" {
			out = append(out, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text})
		}
	case envelope.Type == "response.output_item.added" && envelope.Item != nil && envelope.Item.Type == "function_call":
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
		id := envelope.Item.CallID
		if id == "" {
			id = envelope.Item.ID
		}
		if id != "" && tools != nil && !tools.finished {
			tools.activeID = id
			tools.activeName = envelope.Item.Name
			out = append(out, aipapi.Event{
				Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: envelope.Item.Name,
			})
			for _, args := range tools.pendingArgs {
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: args,
				})
			}
			tools.pendingArgs = nil
			if envelope.Item.Arguments != "" {
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: envelope.Item.Arguments,
				})
			}
		}
	case envelope.Type == "response.function_call_arguments.delta":
		delta := envelope.Text
		if delta == "" && len(envelope.Delta) > 0 {
			_ = json.Unmarshal(envelope.Delta, &delta)
			if delta == "" {
				delta = string(bytes.Trim(envelope.Delta, `"`))
			}
		}
		if delta != "" {
			if !*started {
				*started = true
				out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
			}
			if !*msgStart {
				*msgStart = true
				out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted})
			}
			id := envelope.ItemID
			if tools != nil && !tools.finished {
				if tools.activeID == "" {
					tools.pendingArgs = append(tools.pendingArgs, delta)
					break
				}
				if id == "" {
					id = tools.activeID
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: delta,
				})
			}
		}
	case envelope.Type == "response.completed" || envelope.Type == "response.incomplete":
		if !*started {
			*started = true
			out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
		if tools != nil {
			tools.finish(&out)
		}
		if envelope.Response != nil {
			if respTerm != nil {
				reason := ""
				if envelope.Response.IncompleteDetails != nil {
					reason = envelope.Response.IncompleteDetails.Reason
				}
				respTerm.observe(envelope.Response.Status, reason)
			}
			if envelope.Response.Usage != nil {
				out = append(out, aipapi.Event{
					Kind:          aipapi.EventUsageDelta,
					InputTokens:   envelope.Response.Usage.InputTokens,
					OutputTokens:  envelope.Response.Usage.OutputTokens,
					UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
				})
			}
		}
	}
	return out, nil
}

func decodeNonStream(raw []byte, flavor Flavor) ([]aipapi.Event, error) {
	started, msgStart := false, false
	switch flavor {
	case FlavorResponses:
		var resp struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Output []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Status    string `json:"status"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("openaicompat: responses json: %w", err)
		}
		out := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
		for _, item := range resp.Output {
			if item.Type == "function_call" {
				id := item.CallID
				if id == "" {
					id = item.ID
				}
				if id == "" {
					continue
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: item.Name,
				})
				if item.Arguments != "" {
					out = append(out, aipapi.Event{
						Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: item.Arguments,
					})
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallFinished, ToolCallID: id,
				})
				continue
			}
			for _, c := range item.Content {
				if c.Text != "" {
					out = append(out, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: c.Text})
				}
			}
		}
		if resp.Usage != nil {
			out = append(out, aipapi.Event{
				Kind: aipapi.EventUsageDelta, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
				UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			})
		}
		var respTerm responsesTerminal
		if resp.Status == "incomplete" {
			reason := ""
			if resp.IncompleteDetails != nil {
				reason = resp.IncompleteDetails.Reason
			}
			respTerm.observe(resp.Status, reason)
		}
		out = append(out, respTerm.terminalEvent())
		return out, nil
	default:
		var comp struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &comp); err != nil {
			return nil, fmt.Errorf("openaicompat: chat json: %w", err)
		}
		_ = started
		_ = msgStart
		out := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
		var term chatTerminal
		for _, ch := range comp.Choices {
			term.observe(ch.FinishReason)
			if r := firstNonEmpty(ch.Message.Reasoning, ch.Message.ReasoningContent); r != "" {
				out = append(out, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: r})
			}
			if ch.Message.Content != "" {
				out = append(out, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: ch.Message.Content})
			}
			for _, tc := range ch.Message.ToolCalls {
				if tc.ID == "" {
					continue
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallStarted, ToolCallID: tc.ID, ToolName: tc.Function.Name,
				})
				if tc.Function.Arguments != "" {
					out = append(out, aipapi.Event{
						Kind: aipapi.EventToolCallArgsDelta, ToolCallID: tc.ID, Delta: tc.Function.Arguments,
					})
				}
				out = append(out, aipapi.Event{
					Kind: aipapi.EventToolCallFinished, ToolCallID: tc.ID,
				})
			}
		}
		if comp.Usage != nil {
			out = append(out, aipapi.Event{
				Kind: aipapi.EventUsageDelta, InputTokens: comp.Usage.PromptTokens, OutputTokens: comp.Usage.CompletionTokens,
				UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			})
		}
		out = append(out, term.terminalEvent())
		return out, nil
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type RetentionPolicy int

const (
	PreserveReasoning RetentionPolicy = iota
	DropReasoning
)

type EncryptedPresence string

const (
	EncryptedAbsent EncryptedPresence = "absent"
	EncryptedNull   EncryptedPresence = "null"
	EncryptedEmpty  EncryptedPresence = "empty"
	EncryptedValue  EncryptedPresence = "value"
)

type ObservedReasoning struct {
	ID         string
	Encrypted  EncryptedPresence
	Status     string
	HasContent bool
	RawItem    json.RawMessage
}

type historyItemKind byte

const (
	historyReasoning historyItemKind = iota
	historyMessage
	historyTool
)

type historyItem struct {
	kind        historyItemKind
	reasoning   ObservedReasoning
	visibleText string
	toolCallID  string
	toolName    string
	toolArgs    string
}

type History struct {
	policy RetentionPolicy
	all    []ObservedReasoning
	items  []historyItem
}

func NewHistory(policy RetentionPolicy) *History {
	return &History{policy: policy}
}

func (h *History) ObservedReasoning() []ObservedReasoning {
	if h == nil || len(h.all) == 0 {
		return nil
	}
	out := make([]ObservedReasoning, len(h.all))
	for i := range h.all {
		out[i] = h.all[i]
		if h.all[i].RawItem != nil {
			out[i].RawItem = append(json.RawMessage(nil), h.all[i].RawItem...)
		}
	}
	return out
}

func (h *History) ObservedReasoningIDs() []string {
	items := h.ObservedReasoning()
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func (h *History) ObserveResponse(res *responses.Response) error {
	if h == nil {
		return fmt.Errorf("openairesponses history: nil")
	}
	if res == nil {
		return fmt.Errorf("openairesponses history: nil response")
	}
	for _, item := range res.Output {
		switch item.Type {
		case "reasoning":
			ri := item.AsReasoning()
			obs, err := observedFromReasoning(ri)
			if err != nil {
				return err
			}
			h.all = append(h.all, obs)
			h.items = append(h.items, historyItem{kind: historyReasoning, reasoning: obs})
		case "message":
			msg := item.AsMessage()
			var text strings.Builder
			for _, c := range msg.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
			h.items = append(h.items, historyItem{kind: historyMessage, visibleText: text.String()})
		case "function_call":
			fc := item.AsFunctionCall()
			h.items = append(h.items, historyItem{
				kind:       historyTool,
				toolCallID: fc.CallID,
				toolName:   fc.Name,
				toolArgs:   fc.Arguments,
			})
		}
	}
	return nil
}

func observedFromReasoning(ri responses.ResponseReasoningItem) (ObservedReasoning, error) {
	raw := ri.RawJSON()
	if raw == "" {
		b, err := json.Marshal(ri)
		if err != nil {
			return ObservedReasoning{}, fmt.Errorf("openairesponses history: reasoning item")
		}
		raw = string(b)
	}
	obs := ObservedReasoning{
		ID:      ri.ID,
		RawItem: append(json.RawMessage(nil), raw...),
		Status:  string(ri.Status),
	}
	if !ri.JSON.Status.Valid() && ri.JSON.Status.Raw() != respjson.Null {
		obs.Status = ""
	}
	obs.HasContent = ri.JSON.Content.Valid()
	switch {
	case ri.JSON.EncryptedContent.Raw() == "" || ri.JSON.EncryptedContent.Raw() == respjson.Omitted:
		obs.Encrypted = EncryptedAbsent
	case ri.JSON.EncryptedContent.Raw() == respjson.Null:
		obs.Encrypted = EncryptedNull
	case !ri.JSON.EncryptedContent.Valid():
		obs.Encrypted = EncryptedAbsent
	case ri.EncryptedContent == "":
		obs.Encrypted = EncryptedEmpty
	default:
		obs.Encrypted = EncryptedValue
	}
	return obs, nil
}

func (h *History) NewParams(model, userText string) responses.ResponseNewParams {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(h.items)+1)
	for _, it := range h.items {
		switch it.kind {
		case historyReasoning:
			if h.policy == PreserveReasoning {
				items = append(items, reasoningToInput(it.reasoning))
			}
		case historyMessage:
			if it.visibleText != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(it.visibleText, responses.EasyInputMessageRoleAssistant))
			}
		case historyTool:
			fc := responses.ResponseFunctionToolCallParam{
				CallID:    it.toolCallID,
				Name:      it.toolName,
				Arguments: it.toolArgs,
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCall: &fc})
		}
	}
	items = append(items, responses.ResponseInputItemParamOfMessage(userText, responses.EasyInputMessageRoleUser))
	return responses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: items},
	}
}

func reasoningToInput(obs ObservedReasoning) responses.ResponseInputItemUnionParam {
	raw := append(json.RawMessage(nil), obs.RawItem...)
	p := param.Override[responses.ResponseReasoningItemParam](json.RawMessage(raw))
	return responses.ResponseInputItemUnionParam{OfReasoning: &p}
}

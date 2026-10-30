package openairesponses

import (
	"encoding/json"
)

// Typed SSE JSON payloads (avoid map[string]any boxing in the hot loop).

type streamOutputTextDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	Delta          string `json:"delta"`
}

type streamReasoningSummaryTextDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Delta          string `json:"delta"`
}

type streamFuncArgsDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	Delta          string `json:"delta"`
}

type streamFuncArgsDone struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	Arguments      string `json:"arguments"`
	OutputIndex    int64  `json:"output_index"`
}

type streamFuncItemDone struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type streamOutputItemDone struct {
	Type           string             `json:"type"`
	SequenceNumber int                `json:"sequence_number"`
	OutputIndex    int64              `json:"output_index"`
	Item           streamFuncItemDone `json:"item"`
}

type streamOutputTextDone struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	Text           string `json:"text"`
}

type streamFuncCallInProgress struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type streamOutputItemAddedFunc struct {
	Type           string                   `json:"type"`
	SequenceNumber int                      `json:"sequence_number"`
	OutputIndex    int64                    `json:"output_index"`
	Item           streamFuncCallInProgress `json:"item"`
}

type streamMsgContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"filename,omitempty"`
}

type streamMessageItem struct {
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Status  string             `json:"status"`
	Role    string             `json:"role"`
	Content []streamMsgContent `json:"content"`
}

type streamOutputItemAddedMsg struct {
	Type           string            `json:"type"`
	SequenceNumber int               `json:"sequence_number"`
	OutputIndex    int64             `json:"output_index"`
	Item           streamMessageItem `json:"item"`
}

type streamContentPartAdded struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	Part           struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part"`
}

type streamReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// streamReasoningItem is the reasoning output item shape used in
// response.output_item.added/done and the completed output array.
type streamReasoningItem struct {
	Type    string                   `json:"type"` // "reasoning"
	ID      string                   `json:"id"`
	Status  string                   `json:"status"`
	Summary []streamReasoningSummary `json:"summary"`
}

type streamOutputItemAddedReasoning struct {
	Type           string              `json:"type"`
	SequenceNumber int                 `json:"sequence_number"`
	OutputIndex    int64               `json:"output_index"`
	Item           streamReasoningItem `json:"item"`
}

type streamOutputItemDoneReasoning struct {
	Type           string              `json:"type"`
	SequenceNumber int                 `json:"sequence_number"`
	OutputIndex    int64               `json:"output_index"`
	Item           streamReasoningItem `json:"item"`
}

// streamOutputItemExactReasoning carries a presence-preserving reasoning item body.
type streamOutputItemExactReasoning struct {
	Type           string          `json:"type"`
	SequenceNumber int             `json:"sequence_number"`
	OutputIndex    int64           `json:"output_index"`
	Item           json.RawMessage `json:"item"`
}

type streamReasoningSummaryPart struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text"`
}

type streamReasoningSummaryPartAdded struct {
	Type           string                     `json:"type"`
	SequenceNumber int                        `json:"sequence_number"`
	ItemID         string                     `json:"item_id"`
	OutputIndex    int64                      `json:"output_index"`
	SummaryIndex   int                        `json:"summary_index"`
	Part           streamReasoningSummaryPart `json:"part"`
}

type streamReasoningSummaryPartDone struct {
	Type           string                     `json:"type"`
	SequenceNumber int                        `json:"sequence_number"`
	ItemID         string                     `json:"item_id"`
	OutputIndex    int64                      `json:"output_index"`
	SummaryIndex   int                        `json:"summary_index"`
	Part           streamReasoningSummaryPart `json:"part"`
}

type streamReasoningSummaryTextDone struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Text           string `json:"text"`
}

type streamContentPartDone struct {
	Type           string           `json:"type"`
	SequenceNumber int              `json:"sequence_number"`
	ItemID         string           `json:"item_id"`
	OutputIndex    int64            `json:"output_index"`
	Part           streamMsgContent `json:"part"`
}

type streamOutputItemDoneMessage struct {
	Type           string            `json:"type"`
	SequenceNumber int               `json:"sequence_number"`
	OutputIndex    int64             `json:"output_index"`
	Item           streamMessageItem `json:"item"`
}

type streamCompletedOut struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Status    string                   `json:"status,omitempty"`
	Role      string                   `json:"role,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
	Content   []streamMsgContent       `json:"content,omitempty"`
	Summary   []streamReasoningSummary `json:"summary,omitempty"`
}

type streamCompletedEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	Response       struct {
		ID                string          `json:"id"`
		Object            string          `json:"object"`
		CreatedAt         int64           `json:"created_at"`
		Status            string          `json:"status"`
		Model             string          `json:"model"`
		Output            []any           `json:"output"`
		Usage             *wireUsage      `json:"usage,omitempty"`
		IncompleteDetails *wireIncomplete `json:"incomplete_details,omitempty"`
	} `json:"response"`
}

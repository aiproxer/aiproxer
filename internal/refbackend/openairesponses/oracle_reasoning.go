package openairesponses

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type EncryptedPresence string

const (
	EncryptedAbsent EncryptedPresence = "absent"
	EncryptedNull   EncryptedPresence = "null"
	EncryptedEmpty  EncryptedPresence = "empty"
	EncryptedValue  EncryptedPresence = "value"
)

type ReasoningInputExpect struct {
	Label      string
	ID         string
	SummaryLen int
	ContentLen int
	HasContent bool
	Encrypted  EncryptedPresence
	Status     string
}

type InputItemExpect struct {
	Kind      string // reasoning|message|function_call
	Label     string
	ToolName  string
	Reasoning *ReasoningInputExpect
}

func ExpectNoReasoningInput() RequestValidator {
	return func(body []byte) error {
		items, err := extractReasoningInputRaw(body)
		if err != nil {
			return err
		}
		if len(items) != 0 {
			return fmt.Errorf("openairesponses oracle: structural mismatch: unexpected_reasoning count=%d", len(items))
		}
		return nil
	}
}

func ExpectReasoningInput(want []ReasoningInputExpect) RequestValidator {
	cp := append([]ReasoningInputExpect(nil), want...)
	return func(body []byte) error {
		return CheckReasoningInput(body, cp)
	}
}

func ExpectInputItems(want []InputItemExpect) RequestValidator {
	cp := append([]InputItemExpect(nil), want...)
	return func(body []byte) error {
		return CheckInputItems(body, cp)
	}
}

func CheckReasoningInput(body []byte, want []ReasoningInputExpect) error {
	items, err := extractReasoningInputRaw(body)
	if err != nil {
		return err
	}
	if len(items) != len(want) {
		return fmt.Errorf("openairesponses oracle: structural mismatch: reasoning_count got=%d want=%d", len(items), len(want))
	}
	for i := range want {
		if err := checkOneReasoningInput(want[i], items[i]); err != nil {
			return err
		}
	}
	return nil
}

func CheckInputItems(body []byte, want []InputItemExpect) error {
	items, err := extractInputItemsRaw(body)
	if err != nil {
		return err
	}
	if len(items) != len(want) {
		return fmt.Errorf("openairesponses oracle: structural mismatch: input_count got=%d want=%d", len(items), len(want))
	}
	for i := range want {
		label := want[i].Label
		if label == "" {
			label = fmt.Sprintf("idx_%d", i)
		}
		prefix := fmt.Sprintf("openairesponses oracle: label=%s index=%d", label, i)
		gotKind := items[i].kind
		if gotKind != want[i].Kind {
			return fmt.Errorf("%s structural mismatch: field=type detail=got=%s want=%s", prefix, gotKind, want[i].Kind)
		}
		switch want[i].Kind {
		case "reasoning":
			if want[i].Reasoning == nil {
				return fmt.Errorf("%s structural mismatch: field=reasoning detail=missing_expect", prefix)
			}
			exp := *want[i].Reasoning
			if exp.Label == "" {
				exp.Label = label
			}
			if err := checkOneReasoningInput(exp, items[i].fields); err != nil {
				return err
			}
		case "function_call":
			if want[i].ToolName != "" {
				var name string
				if raw, ok := items[i].fields["name"]; ok {
					_ = json.Unmarshal(raw, &name)
				}
				if name != want[i].ToolName {
					return fmt.Errorf("%s structural mismatch: field=tool_name detail=mismatch", prefix)
				}
			}
		case "message":
		default:
			return fmt.Errorf("%s structural mismatch: field=type detail=unknown_want", prefix)
		}
	}
	return nil
}

func checkOneReasoningInput(want ReasoningInputExpect, raw map[string]json.RawMessage) error {
	label := want.Label
	if label == "" {
		label = "unnamed"
	}
	prefix := fmt.Sprintf("openairesponses oracle: label=%s", label)
	idRaw, ok := raw["id"]
	if !ok {
		return fmt.Errorf("%s structural mismatch: field=id detail=absent", prefix)
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err != nil {
		return fmt.Errorf("%s structural mismatch: field=id detail=type", prefix)
	}
	if idToken(id) != idToken(want.ID) {
		return fmt.Errorf("%s structural mismatch: field=id detail=token_mismatch", prefix)
	}
	sumRaw, ok := raw["summary"]
	if !ok {
		return fmt.Errorf("%s structural mismatch: field=summary detail=absent", prefix)
	}
	var summary []json.RawMessage
	if err := json.Unmarshal(sumRaw, &summary); err != nil {
		return fmt.Errorf("%s structural mismatch: field=summary detail=type", prefix)
	}
	if len(summary) != want.SummaryLen {
		return fmt.Errorf("%s structural mismatch: field=summary detail=len_got=%d want=%d", prefix, len(summary), want.SummaryLen)
	}
	contentRaw, hasContent := raw["content"]
	if want.HasContent || want.ContentLen > 0 {
		if !hasContent {
			return fmt.Errorf("%s structural mismatch: field=content detail=absent", prefix)
		}
		var content []json.RawMessage
		if err := json.Unmarshal(contentRaw, &content); err != nil {
			return fmt.Errorf("%s structural mismatch: field=content detail=type", prefix)
		}
		if len(content) != want.ContentLen {
			return fmt.Errorf("%s structural mismatch: field=content detail=len_got=%d want=%d", prefix, len(content), want.ContentLen)
		}
	} else if hasContent {
		return fmt.Errorf("%s structural mismatch: field=content detail=unexpected", prefix)
	}
	encRaw, hasEnc := raw["encrypted_content"]
	gotEnc := classifyEncryptedRaw(hasEnc, encRaw)
	if gotEnc != want.Encrypted {
		return fmt.Errorf("%s structural mismatch: field=encrypted_content detail=presence_got=%s want=%s", prefix, gotEnc, want.Encrypted)
	}
	statusRaw, hasStatus := raw["status"]
	switch {
	case want.Status == "" && hasStatus:
		return fmt.Errorf("%s structural mismatch: field=status detail=unexpected", prefix)
	case want.Status != "" && !hasStatus:
		return fmt.Errorf("%s structural mismatch: field=status detail=absent", prefix)
	case want.Status != "":
		var st string
		if err := json.Unmarshal(statusRaw, &st); err != nil || st != want.Status {
			return fmt.Errorf("%s structural mismatch: field=status detail=mismatch", prefix)
		}
	}
	return nil
}

func idToken(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func classifyEncryptedRaw(present bool, raw json.RawMessage) EncryptedPresence {
	if !present {
		return EncryptedAbsent
	}
	trim := bytes.TrimSpace(raw)
	if bytes.Equal(trim, []byte("null")) {
		return EncryptedNull
	}
	var s string
	if err := json.Unmarshal(trim, &s); err != nil {
		return EncryptedValue
	}
	if s == "" {
		return EncryptedEmpty
	}
	return EncryptedValue
}

type rawInputItem struct {
	kind   string
	fields map[string]json.RawMessage
}

func extractInputItemsRaw(body []byte) ([]rawInputItem, error) {
	var root struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("openairesponses oracle: structural mismatch: request_json")
	}
	if len(bytes.TrimSpace(root.Input)) == 0 {
		return nil, nil
	}
	trim := bytes.TrimSpace(root.Input)
	if trim[0] == '"' {
		return []rawInputItem{{kind: "message"}}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trim, &items); err != nil {
		return nil, fmt.Errorf("openairesponses oracle: structural mismatch: input_type")
	}
	out := make([]rawInputItem, 0, len(items))
	for _, it := range items {
		var probe struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if err := json.Unmarshal(it, &probe); err != nil {
			return nil, fmt.Errorf("openairesponses oracle: structural mismatch: input_item")
		}
		kind := probe.Type
		if kind == "" && probe.Role != "" {
			kind = "message"
		}
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(it, &obj)
		out = append(out, rawInputItem{kind: kind, fields: obj})
	}
	return out, nil
}

func extractReasoningInputRaw(body []byte) ([]map[string]json.RawMessage, error) {
	items, err := extractInputItemsRaw(body)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]json.RawMessage, 0)
	for _, it := range items {
		if it.kind == "reasoning" {
			out = append(out, it.fields)
		}
	}
	return out, nil
}

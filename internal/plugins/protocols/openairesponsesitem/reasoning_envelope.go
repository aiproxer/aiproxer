package openairesponsesitem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var envelopeKeyOrder = [...]string{
	"id",
	"type",
	"summary",
	"content",
	"encrypted_content",
	"status",
}

var envelopeAllowed = map[string]struct{}{
	"id":                {},
	"type":              {},
	"summary":           {},
	"content":           {},
	"encrypted_content": {},
	"status":            {},
}

func itemError(reason string) error {
	return fmt.Errorf("openairesponses: invalid reasoning item: %s", reason)
}

// ParseIncompleteFields structurally parses a reasoning item object in incomplete-allowed mode:
// JSON object, exact EOF, duplicate-key rejection, unknown-field rejection, and max size.
// Missing required final fields (id/summary/...) are allowed; CanonizeReasoningItemOpaque
// performs final validation after merge.
//
// The returned map is a new caller-owned copy.
func ParseIncompleteFields(raw []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, itemError("empty")
	}
	if len(raw) > aipapi.MaxReasoningOpaqueBytes {
		return nil, itemError("oversize")
	}
	if err := rejectDuplicateJSONObjectKeys(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		return nil, itemError("malformed")
	}
	if obj == nil {
		return nil, itemError("malformed")
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, itemError("malformed")
	}
	for k := range obj {
		if _, ok := envelopeAllowed[k]; !ok {
			return nil, itemError("unknown field")
		}
	}
	out := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out, nil
}

// CanonizeReasoningItemOpaque validates a Responses reasoning item JSON object
// against the exact Opaque allowlist and returns a presence-preserving canonical form.
//
// type may be absent on input; it is always emitted as "reasoning" (required semantically).
func CanonizeReasoningItemOpaque(raw []byte) (json.RawMessage, error) {
	obj, err := ParseIncompleteFields(raw)
	if err != nil {
		return nil, err
	}

	idRaw, ok := obj["id"]
	if !ok {
		return nil, itemError("missing id")
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err != nil {
		return nil, itemError("invalid id")
	}
	if strings.TrimSpace(id) == "" {
		return nil, itemError("invalid id")
	}

	sumRaw, ok := obj["summary"]
	if !ok {
		return nil, itemError("missing summary")
	}
	sumCanon, err := canonizeReasoningTextArray(sumRaw, "summary_text", "invalid summary")
	if err != nil {
		return nil, err
	}

	out := make(map[string]json.RawMessage, 6)
	idBytes, err := json.Marshal(id)
	if err != nil {
		return nil, itemError("invalid id")
	}
	out["id"] = idBytes
	out["type"] = json.RawMessage(`"reasoning"`)
	out["summary"] = sumCanon

	if typRaw, ok := obj["type"]; ok {
		var typ string
		if err := json.Unmarshal(typRaw, &typ); err != nil || typ != "reasoning" {
			return nil, itemError("invalid type")
		}
	}

	if contentRaw, ok := obj["content"]; ok {
		if isJSONNull(contentRaw) {
			return nil, itemError("invalid content")
		}
		contentCanon, err := canonizeReasoningTextArray(contentRaw, "reasoning_text", "invalid content")
		if err != nil {
			return nil, err
		}
		out["content"] = contentCanon
	}

	if encRaw, ok := obj["encrypted_content"]; ok {
		if isJSONNull(encRaw) {
			out["encrypted_content"] = json.RawMessage("null")
		} else {
			var enc string
			if err := json.Unmarshal(encRaw, &enc); err != nil {
				return nil, itemError("invalid encrypted_content")
			}
			encBytes, err := json.Marshal(enc)
			if err != nil {
				return nil, itemError("invalid encrypted_content")
			}
			out["encrypted_content"] = encBytes
		}
	}

	if statusRaw, ok := obj["status"]; ok {
		var status string
		if err := json.Unmarshal(statusRaw, &status); err != nil {
			return nil, itemError("invalid status")
		}
		switch status {
		case "in_progress", "completed", "incomplete":
			statusBytes, err := json.Marshal(status)
			if err != nil {
				return nil, itemError("invalid status")
			}
			out["status"] = statusBytes
		default:
			return nil, itemError("invalid status")
		}
	}

	canon, err := MarshalEnvelope(out)
	if err != nil {
		return nil, itemError("malformed")
	}
	if len(canon) > aipapi.MaxReasoningOpaqueBytes {
		return nil, itemError("oversize")
	}
	return json.RawMessage(canon), nil
}

// MarshalEnvelope writes allowlisted fields in deterministic envelope key order.
func MarshalEnvelope(fields map[string]json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, key := range envelopeKeyOrder {
		val, ok := fields[key]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func requireJSONArray(raw json.RawMessage) error {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '[' {
		return errors.New("not array")
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(trim, &arr); err != nil {
		return err
	}
	return nil
}

func canonizeReasoningTextArray(raw json.RawMessage, wantType, errReason string) (json.RawMessage, error) {
	if err := requireJSONArray(raw); err != nil {
		return nil, itemError(errReason)
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(raw), &elems); err != nil {
		return nil, itemError(errReason)
	}
	out := make([]map[string]string, 0, len(elems))
	for _, el := range elems {
		item, err := canonizeReasoningTextElement(el, wantType, errReason)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, itemError(errReason)
	}
	return json.RawMessage(b), nil
}

func canonizeReasoningTextElement(raw json.RawMessage, wantType, errReason string) (map[string]string, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '{' {
		return nil, itemError(errReason)
	}
	if err := rejectDuplicateJSONObjectKeys(trim); err != nil {
		return nil, itemError(errReason)
	}
	dec := json.NewDecoder(bytes.NewReader(trim))
	dec.UseNumber()
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, itemError(errReason)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, itemError(errReason)
	}
	allowed := map[string]struct{}{"type": {}, "text": {}}
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			return nil, itemError(errReason)
		}
	}
	typRaw, ok := obj["type"]
	if !ok {
		return nil, itemError(errReason)
	}
	var typ string
	if err := json.Unmarshal(typRaw, &typ); err != nil || typ != wantType {
		return nil, itemError(errReason)
	}
	textRaw, ok := obj["text"]
	if !ok {
		return nil, itemError(errReason)
	}
	var text string
	if err := json.Unmarshal(textRaw, &text); err != nil {
		return nil, itemError(errReason)
	}
	return map[string]string{"type": wantType, "text": text}, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func rejectDuplicateJSONObjectKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return itemError("malformed")
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return itemError("malformed")
	}
	if err := rejectDuplicateKeysInObject(dec); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateKeysInObject(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return itemError("malformed")
		}
		key, ok := keyTok.(string)
		if !ok {
			return itemError("malformed")
		}
		if _, dup := seen[key]; dup {
			return itemError("duplicate key")
		}
		seen[key] = struct{}{}
		if err := skipJSONValueRejectingDuplicateKeys(dec); err != nil {
			return err
		}
	}
	tok, err := dec.Token()
	if err != nil {
		return itemError("malformed")
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '}' {
		return itemError("malformed")
	}
	return nil
}

func skipJSONValueRejectingDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return itemError("malformed")
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			return rejectDuplicateKeysInObject(dec)
		case '[':
			for dec.More() {
				if err := skipJSONValueRejectingDuplicateKeys(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return itemError("malformed")
			}
			ed, ok := end.(json.Delim)
			if !ok || ed != ']' {
				return itemError("malformed")
			}
			return nil
		default:
			return itemError("malformed")
		}
	}
	return nil
}

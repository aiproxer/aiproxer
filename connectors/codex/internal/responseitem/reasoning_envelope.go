package responseitem

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

const maxOpaqueJSONDepth = 100

// The OpenResponses compact schema permits a 10 MiB encrypted summary. Keep
// this connector-private limit separate from the smaller canonical part cap.
const maxCompactionSummaryBytes = 10 << 20

var (
	envelopeAllowed     = keySet(envelopeKeyOrder[:])
	reasoningTextFields = keySet([]string{"type", "text"})
	// Some Codex deployments include the response lifecycle status on the
	// returned item even though the published compact-item schema omits it.
	// It is validated and discarded during canonicalization.
	compactionAllowed        = keySet([]string{"type", "id", "encrypted_content", "created_by", "status"})
	compactionSummaryAllowed = keySet([]string{"type", "id", "encrypted_content", "created_by", "status"})
)

func keySet(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
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

// ValidateJSONObject applies the bounded JSON-object checks used for opaque
// Responses resources without interpreting provider content.
func ValidateJSONObject(raw []byte, maxBytes int) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (maxBytes > 0 && len(trimmed) > maxBytes) {
		return itemError("invalid object")
	}
	if err := rejectDuplicateJSONObjectKeys(trimmed); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var object map[string]json.RawMessage
	if err := dec.Decode(&object); err != nil || object == nil {
		return itemError("invalid object")
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return itemError("trailing JSON")
	}
	return nil
}

// ParseObjectFields validates a bounded JSON object, rejects duplicate keys,
// and applies an exact field allowlist. The returned fields are caller-owned.
func ParseObjectFields(raw []byte, allowed map[string]struct{}, maxBytes int) (map[string]json.RawMessage, error) {
	return parseAllowedObjectWithMax(raw, allowed, maxBytes)
}

// CanonizeReasoningItemOpaque validates a Responses reasoning item JSON object
// against the exact Opaque allowlist and returns a presence-preserving canonical form.
//
// type may be absent on input; it is always emitted as "reasoning" (required semantically).
func CanonizeReasoningItemOpaque(raw []byte) (json.RawMessage, error) {
	return canonizeReasoningItem(raw, true)
}

// CanonizeReasoningItemForInput removes response-only lifecycle fields before a
// reasoning item is replayed as Responses input. Codex CLI serializes its
// internal reasoning model without status; the provider may reject status on
// the stricter compaction path.
func CanonizeReasoningItemForInput(raw []byte) (json.RawMessage, error) {
	return canonizeReasoningItem(raw, false)
}

func canonizeReasoningItem(raw []byte, preserveStatus bool) (json.RawMessage, error) {
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

	if preserveStatus {
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

// CanonizeCompactionItemOpaque validates the exact Responses compaction output
// envelope. A compaction trigger is only {"type":"compaction"}; this helper
// is intentionally for the completed output item and does not accept reasoning
// fields or a stream lifecycle status.
func CanonizeCompactionItemOpaque(raw []byte) (json.RawMessage, error) {
	obj, err := parseAllowedObject(raw, compactionAllowed)
	if err != nil {
		return nil, itemError("invalid compaction item")
	}
	var typ, id, encrypted, status string
	if json.Unmarshal(obj["type"], &typ) != nil || typ != "compaction" ||
		json.Unmarshal(obj["id"], &id) != nil || strings.TrimSpace(id) == "" ||
		json.Unmarshal(obj["encrypted_content"], &encrypted) != nil || encrypted == "" {
		return nil, itemError("invalid compaction item")
	}
	if statusRaw, ok := obj["status"]; ok {
		if json.Unmarshal(statusRaw, &status) != nil || status != "completed" {
			return nil, itemError("invalid compaction item")
		}
	}
	fields := map[string]json.RawMessage{
		"type":              json.RawMessage(`"compaction"`),
		"id":                mustMarshalString(id),
		"encrypted_content": mustMarshalString(encrypted),
	}
	if createdBy, ok := obj["created_by"]; ok {
		var value string
		if json.Unmarshal(createdBy, &value) != nil {
			return nil, itemError("invalid compaction item")
		}
		fields["created_by"] = mustMarshalString(value)
	}
	canon, err := marshalCompactionEnvelope(fields)
	if err != nil || len(canon) > aipapi.MaxPartJSONBytes {
		return nil, itemError("invalid compaction item")
	}
	return json.RawMessage(canon), nil
}

// CanonizeCompactionSummaryItemOpaque validates the private input item emitted
// by the dedicated Codex compact endpoint. The raw object is returned unchanged
// after validation so provider fields and their presence survive replay.
func CanonizeCompactionSummaryItemOpaque(raw []byte) (json.RawMessage, error) {
	obj, err := parseAllowedObjectWithMax(raw, compactionSummaryAllowed, maxCompactionSummaryBytes)
	if err != nil {
		return nil, itemError("invalid compaction summary item")
	}
	var typ string
	if json.Unmarshal(obj["type"], &typ) != nil || typ != "compaction_summary" {
		return nil, itemError("invalid compaction summary item")
	}
	encrypted, ok := obj["encrypted_content"]
	if !ok {
		return nil, itemError("invalid compaction summary item")
	}
	var value string
	if json.Unmarshal(encrypted, &value) != nil || value == "" || len(value) > maxCompactionSummaryBytes {
		return nil, itemError("invalid compaction summary item")
	}
	if id, ok := obj["id"]; ok && !isJSONNull(id) {
		var value string
		if json.Unmarshal(id, &value) != nil || strings.TrimSpace(value) == "" {
			return nil, itemError("invalid compaction summary item")
		}
	}
	if status, ok := obj["status"]; ok {
		var value string
		if json.Unmarshal(status, &value) != nil || value != "completed" {
			return nil, itemError("invalid compaction summary item")
		}
	}
	if createdBy, ok := obj["created_by"]; ok {
		var value string
		if json.Unmarshal(createdBy, &value) != nil {
			return nil, itemError("invalid compaction summary item")
		}
	}
	return json.RawMessage(append([]byte(nil), bytes.TrimSpace(raw)...)), nil
}

// CompactionSummaryMaxBytes is the provider schema bound used by the Codex
// connector for the opaque encrypted summary envelope.
const CompactionSummaryMaxBytes = maxCompactionSummaryBytes

func mustMarshalString(value string) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func parseAllowedObject(raw []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	return parseAllowedObjectWithMax(raw, allowed, aipapi.MaxPartJSONBytes)
}

func parseAllowedObjectWithMax(raw []byte, allowed map[string]struct{}, maxBytes int) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || (maxBytes > 0 && len(raw) > maxBytes) {
		return nil, errors.New("invalid object")
	}
	if err := rejectDuplicateJSONObjectKeys(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("invalid object")
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	for key := range obj {
		if _, ok := allowed[key]; !ok {
			return nil, errors.New("unknown field")
		}
	}
	return obj, nil
}

func marshalCompactionEnvelope(fields map[string]json.RawMessage) ([]byte, error) {
	order := [...]string{"type", "id", "encrypted_content", "created_by"}
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, key := range order {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		b.Write(encodedKey)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// MarshalEnvelope validates allowlisted fields and writes them in deterministic key order.
func MarshalEnvelope(fields map[string]json.RawMessage) ([]byte, error) {
	for key, val := range fields {
		if _, ok := envelopeAllowed[key]; !ok {
			return nil, fmt.Errorf("unknown envelope field %q", key)
		}
		if !json.Valid(val) {
			return nil, fmt.Errorf("invalid JSON for envelope field %q", key)
		}
	}
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

func requireJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '[' {
		return nil, errors.New("not array")
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(trim, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func canonizeReasoningTextArray(raw json.RawMessage, wantType, errReason string) (json.RawMessage, error) {
	elems, err := requireJSONArray(raw)
	if err != nil {
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
	for k := range obj {
		if _, ok := reasoningTextFields[k]; !ok {
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
	if err := rejectDuplicateKeysInObject(dec, 1); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateKeysInObject(dec *json.Decoder, depth int) error {
	if depth > maxOpaqueJSONDepth {
		return itemError("max depth exceeded")
	}
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
		if err := skipJSONValueRejectingDuplicateKeys(dec, depth); err != nil {
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

func skipJSONValueRejectingDuplicateKeys(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return itemError("malformed")
	}
	if delim, ok := tok.(json.Delim); ok {
		if depth >= maxOpaqueJSONDepth {
			return itemError("max depth exceeded")
		}
		switch delim {
		case '{':
			return rejectDuplicateKeysInObject(dec, depth+1)
		case '[':
			for dec.More() {
				if err := skipJSONValueRejectingDuplicateKeys(dec, depth+1); err != nil {
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

package reasoningpreservation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func ComputeAnchor(msg aipapi.Message) ([32]byte, error) {
	if msg.Role != aipapi.RoleAssistant {
		return [32]byte{}, fmt.Errorf("%s: anchor requires assistant message", ID)
	}
	var buf bytes.Buffer
	writeLenString(&buf, "role")
	writeLenString(&buf, string(msg.Role))
	seq := 0
	for _, p := range msg.Parts {
		if p.Kind == aipapi.PartReasoning {
			continue
		}
		if err := writeNonReasoningPart(&buf, seq, p); err != nil {
			return [32]byte{}, err
		}
		seq++
	}
	return sha256.Sum256(buf.Bytes()), nil
}

// DerivePlacements places every reasoning block at index 0 when only a flat
// reasoning slice is available (equal indexes preserve block order). Prefer
// DerivePlacementsFromParts when the interleaved assistant parts are known.
func DerivePlacements(nonReasoningCount int, reasoning []aipapi.Part) ([]PlacedReasoning, error) {
	if nonReasoningCount < 0 {
		return nil, fmt.Errorf("%s: nonReasoningCount must be >= 0", ID)
	}
	out := make([]PlacedReasoning, 0, len(reasoning))
	for i, p := range reasoning {
		if p.Kind != aipapi.PartReasoning || p.Reasoning == nil {
			return nil, fmt.Errorf("%s: DerivePlacements requires PartReasoning at index %d", ID, i)
		}
		out = append(out, PlacedReasoning{
			BeforeNonReasoningPart: 0,
			Part:                   clonePart(p),
		})
	}
	return out, nil
}

func DerivePlacementsFromParts(parts []aipapi.Part) ([]PlacedReasoning, int, error) {
	nonReasoning := 0
	var reasoning []PlacedReasoning
	for _, p := range parts {
		if p.Kind == aipapi.PartReasoning {
			if p.Reasoning == nil {
				return nil, 0, fmt.Errorf("%s: nil reasoning payload", ID)
			}
			reasoning = append(reasoning, PlacedReasoning{
				BeforeNonReasoningPart: nonReasoning,
				Part:                   clonePart(p),
			})
			continue
		}
		nonReasoning++
	}
	return reasoning, nonReasoning, nil
}

func writeNonReasoningPart(w io.Writer, index int, p aipapi.Part) error {
	writeLenString(w, "part")
	_ = binary.Write(w, binary.BigEndian, int32(index))
	writeLenString(w, string(p.Kind))
	switch p.Kind {
	case aipapi.PartText:
		writeLenString(w, p.Text)
	case aipapi.PartJSON:
		canon, err := canonicalizeJSON(p.Content)
		if err != nil {
			return err
		}
		writeLenBytes(w, canon)
		writeLenString(w, p.ToolCallID)
		writeLenString(w, p.ToolName)
	case aipapi.PartToolResult:
		writeLenString(w, p.ToolCallID)
		writeLenString(w, p.ToolName)
		canon, err := canonicalizeJSON(p.Content)
		if err != nil {
			writeLenBytes(w, p.Content)
		} else {
			writeLenBytes(w, canon)
		}
	case aipapi.PartImageRef:
		writeLenString(w, p.ImageRef)
		writeLenString(w, p.ImageMIME)
	case aipapi.PartFileRef:
		writeLenString(w, p.FileRef)
		writeLenString(w, p.FileMIME)
		writeLenString(w, p.FileName)
	default:
		writeLenString(w, p.Text)
		writeLenBytes(w, p.Content)
		writeLenString(w, p.ToolCallID)
		writeLenString(w, p.ToolName)
		writeLenString(w, p.ImageRef)
		writeLenString(w, p.FileRef)
	}
	return nil
}

func canonicalizeJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s: empty json", ID)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	norm, err := normalizeJSONValue(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(norm)
}

func normalizeJSONValue(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			nv, err := normalizeJSONValue(t[k])
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i := range t {
			nv, err := normalizeJSONValue(t[i])
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	case json.Number:
		return t, nil
	default:
		return t, nil
	}
}

func writeLenString(w io.Writer, s string) {
	writeLenBytes(w, []byte(s))
}

func writeLenBytes(w io.Writer, b []byte) {
	_ = binary.Write(w, binary.BigEndian, uint32(len(b)))
	_, _ = w.Write(b)
}

func clonePart(p aipapi.Part) aipapi.Part {
	out := p
	if len(p.Content) > 0 {
		out.Content = append(json.RawMessage(nil), p.Content...)
	}
	if p.Reasoning != nil {
		rp := *p.Reasoning
		if p.Reasoning.Opaque != nil {
			rp.Opaque = append(json.RawMessage(nil), p.Reasoning.Opaque...)
		}
		out.Reasoning = &rp
	}
	return out
}

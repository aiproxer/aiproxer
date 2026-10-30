package compactionfacts

import (
	"crypto/sha256"
	"hash"
	"io"
	"strconv"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// WriteField writes a length-prefixed key-value field to w:
// name + "=" + strconv.Itoa(len(val)) + ":" + val + "|"
func WriteField(w io.Writer, name, val string) {
	_, _ = io.WriteString(w, name)
	_, _ = w.Write([]byte{'='})
	_, _ = io.WriteString(w, strconv.Itoa(len(val)))
	_, _ = w.Write([]byte{':'})
	_, _ = io.WriteString(w, val)
	_, _ = w.Write([]byte{'|'})
}

// WriteFieldHeader writes the length-prefixed header for a field:
// name + "=" + strconv.Itoa(valLen) + ":"
func WriteFieldHeader(w io.Writer, name string, valLen int) {
	_, _ = io.WriteString(w, name)
	_, _ = w.Write([]byte{'='})
	_, _ = io.WriteString(w, strconv.Itoa(valLen))
	_, _ = w.Write([]byte{':'})
}

// WriteFieldTrailer writes the field delimiter "|".
func WriteFieldTrailer(w io.Writer) {
	_, _ = w.Write([]byte{'|'})
}

// WriteItemCanonical streams the deterministic canonical representation of an Item
// directly into w, matching the exact format required for semantic hashing without
// allocating intermediate strings.Builder payload copies.
func WriteItemCanonical(w io.Writer, it aipapi.Item) {
	WriteField(w, "kind", string(it.Kind))
	WriteField(w, "role", string(it.Role))
	for _, cp := range it.Content {
		WriteField(w, "content.kind", string(cp.Kind))
		WriteField(w, "content.text", cp.Text)
		WriteField(w, "content.refusal", cp.Refusal)
		WriteField(w, "content.summary", cp.Summary)
	}
	if it.ToolCall != nil {
		WriteField(w, "tool_call", "present")
		WriteField(w, "tool_call.name", it.ToolCall.Name)
	}
	if it.ToolResult != nil {
		WriteField(w, "tool_result", "present")
		WriteField(w, "tool_result.output", it.ToolResult.Output)
	}
}

// HashItem computes the SHA-256 semantic hash of a single canonical aipapi.Item.
func HashItem(it aipapi.Item) [32]byte {
	h := sha256.New()
	WriteItemCanonical(h, it)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ItemHasher computes an item hash incrementally by streaming fields and chunks.
type ItemHasher struct {
	h hash.Hash
}

// NewItemHasher starts hashing an item with the given kind and role.
func NewItemHasher(kind aipapi.ItemKind, role aipapi.Role) *ItemHasher {
	ih := &ItemHasher{h: sha256.New()}
	WriteField(ih.h, "kind", string(kind))
	WriteField(ih.h, "role", string(role))
	return ih
}

// AddContentPart records a content part on the item.
func (ih *ItemHasher) AddContentPart(kind aipapi.ContentPartKind, text, refusal, summary string) {
	WriteField(ih.h, "content.kind", string(kind))
	WriteField(ih.h, "content.text", text)
	WriteField(ih.h, "content.refusal", refusal)
	WriteField(ih.h, "content.summary", summary)
}

// AddToolCall records a tool call on the item.
func (ih *ItemHasher) AddToolCall(name string) {
	WriteField(ih.h, "tool_call", "present")
	WriteField(ih.h, "tool_call.name", name)
}

// AddToolResult records a tool result on the item.
func (ih *ItemHasher) AddToolResult(output string) {
	WriteField(ih.h, "tool_result", "present")
	WriteField(ih.h, "tool_result.output", output)
}

// BeginContentText begins streaming a ContentPartText with a known byte length.
// It writes "content.kind=4:text|" and the header "content.text=<valLen>:".
// The returned io.Writer streams into the hash.
// Call EndContentText() after writing exactly valLen bytes to complete the part.
func (ih *ItemHasher) BeginContentText(valLen int) io.Writer {
	WriteField(ih.h, "content.kind", string(aipapi.ContentPartText))
	WriteFieldHeader(ih.h, "content.text", valLen)
	return ih.h
}

// EndContentText closes the content.text field and emits trailing empty refusal and summary fields.
func (ih *ItemHasher) EndContentText() {
	WriteFieldTrailer(ih.h)
	WriteField(ih.h, "content.refusal", "")
	WriteField(ih.h, "content.summary", "")
}

// Writer returns an io.Writer to stream payload chunks directly into the item hash.
func (ih *ItemHasher) Writer() io.Writer {
	return ih.h
}

// Sum returns the final 32-byte SHA-256 hash.
func (ih *ItemHasher) Sum() [32]byte {
	var out [32]byte
	copy(out[:], ih.h.Sum(nil))
	return out
}

// Digest computes a deterministic 32-byte SHA-256 digest over ALL behavior-relevant
// compaction fact fields and the completion status.
func Digest(f RequestFacts, complete bool) [32]byte {
	h := sha256.New()
	if complete {
		WriteField(h, "complete", "true")
	} else {
		WriteField(h, "complete", "false")
	}
	WriteField(h, "operation", string(f.Operation))
	WriteField(h, "tool_count", strconv.Itoa(f.ToolCount))
	WriteField(h, "estimated_tokens", strconv.Itoa(f.EstimatedTokens))
	WriteField(h, "item_count", strconv.Itoa(f.ItemCount))
	WriteField(h, "item_hashes.len", strconv.Itoa(len(f.ItemHashes)))
	for i, ih := range f.ItemHashes {
		WriteField(h, "item_hash."+strconv.Itoa(i), string(ih[:]))
	}
	WriteField(h, "tail_len", strconv.Itoa(f.TailLen))
	for i := 0; i < f.TailLen && i < len(f.TailHashes); i++ {
		WriteField(h, "tail_hash."+strconv.Itoa(i), string(f.TailHashes[i][:]))
	}
	WriteField(h, "prefix_items", strconv.Itoa(f.PrefixItems))
	WriteField(h, "prefix_hash", string(f.PrefixHash[:]))
	if f.StartRuleMatched {
		WriteField(h, "start_rule_matched", "true")
	} else {
		WriteField(h, "start_rule_matched", "false")
	}
	WriteField(h, "start_rule_id", f.StartRuleID)
	WriteField(h, "start_rule_mode", strconv.Itoa(int(f.StartRuleMode)))
	WriteField(h, "start_rule_evidence", string(f.StartRuleEvidence))

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

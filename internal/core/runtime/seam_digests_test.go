package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func seamTestCall(texts ...string) aipapi.Call {
	msgs := make([]aipapi.Message, len(texts))
	for i, text := range texts {
		msgs[i] = aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}
	}
	return aipapi.Call{Messages: msgs}
}

// seamTestKey returns a fixed non-zero process key for deterministic tests.
// Production keys come from NewSeamDigestKey (crypto/rand); tests pin one key
// so the same prompt yields the same digests within one process.
func seamTestKey() SeamDigestKey {
	var key SeamDigestKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func seamOtherKey() SeamDigestKey {
	var key SeamDigestKey
	for i := range key {
		key[i] = byte(0xFF - i)
	}
	return key
}

// TestNewSeamDigestKey_UniqueNonZero proves the process-stable secret is
// actually secret: every generation is non-zero and unique, so digests from
// different processes cannot be dictionary-matched against each other.
func TestNewSeamDigestKey_UniqueNonZero(t *testing.T) {
	t.Parallel()
	a, err := NewSeamDigestKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSeamDigestKey()
	if err != nil {
		t.Fatal(err)
	}
	if a.IsZero() || b.IsZero() {
		t.Fatal("process keys must never be zero")
	}
	if a == b {
		t.Fatal("process keys must be unique per generation")
	}
}

// TestSeamItemDigests_Deterministic proves the producer is a pure function
// of the model-bound content and the borrowed process key: the same call
// under the same key yields the same fixed-size hashes, so a collapse
// episode can compare legs without ever seeing content.
func TestSeamItemDigests_Deterministic(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	call := seamTestCall("sys", "history", "user")
	first := seamItemDigests(call, key)
	second := seamItemDigests(call, key)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("digests=%d,%d want 3,3", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("digest[%d] not deterministic", i)
		}
		if first[i] == ([32]byte{}) {
			t.Fatalf("digest[%d] must not be zero", i)
		}
	}
}

// TestSeamItemDigests_DifferentKeysDiffer proves the keying: the same prompt
// under different process keys yields different digests, so an offline
// dictionary built without the key cannot match them.
func TestSeamItemDigests_DifferentKeysDiffer(t *testing.T) {
	t.Parallel()
	call := seamTestCall("low-entropy", "prompt")
	a := seamItemDigests(call, seamTestKey())
	b := seamItemDigests(call, seamOtherKey())
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("digests=%d,%d want 2,2", len(a), len(b))
	}
	for i := range a {
		if a[i] == b[i] {
			t.Fatalf("digest[%d] must differ across process keys", i)
		}
	}
}

// TestSeamItemDigests_NotPlainSHA256 proves dictionary resistance directly:
// the keyed digest is not the plain SHA-256 of the marshaled item, so a
// holder of the digest feed without the key cannot reproduce it by hashing
// candidate prompts.
func TestSeamItemDigests_NotPlainSHA256(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	call := seamTestCall("hello")
	got := seamItemDigests(call, key)
	if len(got) != 1 {
		t.Fatalf("digests=%d want 1", len(got))
	}
	items := aipapi.NormalizedItemsCapped(call, seamDigestMaxItems)
	item := items[0]
	item.ID = ""
	item.Status = ""
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256(raw)
	if got[0] == plain {
		t.Fatal("keyed digest must not equal plain SHA-256: dictionary matching would survive keying")
	}
}

// TestSeamItemDigests_ContentSensitive proves an elision-like text change
// moves the digest at the edited index while keeping the prefix stable, so
// the diagnostic reports the first divergent item index.
func TestSeamItemDigests_ContentSensitive(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	base := seamItemDigests(seamTestCall("sys", "history", "user"), key)
	edited := seamItemDigests(seamTestCall("sys", "EDITED", "user"), key)
	if len(base) != 3 || len(edited) != 3 {
		t.Fatalf("digests=%d,%d want 3,3", len(base), len(edited))
	}
	if base[0] != edited[0] {
		t.Fatal("prefix digest must stay stable when a later entry changes")
	}
	if base[1] == edited[1] {
		t.Fatal("edited entry digest must change")
	}
}

// TestSeamItemDigests_Bounded proves the per-request hashing cost is
// bounded: only the first seamDigestMaxItems trajectory entries are
// projected and hashed.
func TestSeamItemDigests_Bounded(t *testing.T) {
	t.Parallel()
	texts := make([]string, seamDigestMaxItems+32)
	for i := range texts {
		texts[i] = string(rune('a'+i%26)) + string(rune('0'+i%10))
	}
	got := seamItemDigests(seamTestCall(texts...), seamTestKey())
	if len(got) != seamDigestMaxItems {
		t.Fatalf("digests=%d want %d", len(got), seamDigestMaxItems)
	}
}

// TestSeamItemDigests_EmptyCallFeedsNothing proves an empty trajectory
// yields no digests, so the diagnostic keeps its unattributed behavior.
func TestSeamItemDigests_EmptyCallFeedsNothing(t *testing.T) {
	t.Parallel()
	if got := seamItemDigests(aipapi.Call{}, seamTestKey()); len(got) != 0 {
		t.Fatalf("empty call digests=%d want 0", len(got))
	}
}

// TestSeamItemDigests_StripsVolatileIdentity proves proxy bookkeeping
// (IDs, statuses) does not shift digests: only prompt content matters.
func TestSeamItemDigests_StripsVolatileIdentity(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	a := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, ID: "a", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
	}}
	b := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, ID: "b", Status: aipapi.ItemStatusInProgress, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
	}}
	first := seamItemDigests(a, key)
	second := seamItemDigests(b, key)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("digests=%d,%d want 1,1", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Fatal("volatile identity must not shift digests")
	}
}

// TestSeamItemDigests_MixedKindsAreIndexed proves the index contract: the
// vector addresses the normalized item trajectory, so a change to a
// non-message item shifts the digest at its trajectory position and the
// diagnostic must label it an item index, not a message ordinal.
func TestSeamItemDigests_MixedKindsAreIndexed(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	base := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "read"}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Output: "same-output"}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "followup"}}},
	}}
	changedTool := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "read"}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Output: "CHANGED-output"}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "followup"}}},
	}}
	changedMsg := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "read"}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Output: "same-output"}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "CHANGED-followup"}}},
	}}
	baseDigests := seamItemDigests(base, key)
	toolDigests := seamItemDigests(changedTool, key)
	msgDigests := seamItemDigests(changedMsg, key)
	if len(baseDigests) != 4 || len(toolDigests) != 4 || len(msgDigests) != 4 {
		t.Fatalf("digests=%d,%d,%d want 4,4,4", len(baseDigests), len(toolDigests), len(msgDigests))
	}
	// Tool-result change at trajectory position 2: prefix 0-1 stable.
	if baseDigests[0] != toolDigests[0] || baseDigests[1] != toolDigests[1] {
		t.Fatal("prefix before the changed item must stay stable")
	}
	if baseDigests[2] == toolDigests[2] {
		t.Fatal("changed tool-result item must shift its trajectory digest")
	}
	// Final-message change at trajectory position 3, not message ordinal 1.
	if baseDigests[3] == msgDigests[3] {
		t.Fatal("changed final message must shift its trajectory digest")
	}
	if baseDigests[2] != msgDigests[2] {
		t.Fatal("unchanged tool-result item must keep its digest")
	}
}

// TestSeamItemDigests_InstructionsLeadTrajectory proves legacy instructions
// occupy the leading trajectory positions, so an instruction change shifts
// index 0.
func TestSeamItemDigests_InstructionsLeadTrajectory(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	base := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}},
		Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	changed := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("CHANGED-sys")}}},
		Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	first := seamItemDigests(base, key)
	second := seamItemDigests(changed, key)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("digests=%d,%d want 2,2", len(first), len(second))
	}
	if first[0] == second[0] {
		t.Fatal("instruction change must shift trajectory index 0")
	}
	if first[1] != second[1] {
		t.Fatal("unchanged message must keep its digest")
	}
}

func BenchmarkSeamItemDigests_LargePrompt(b *testing.B) {
	texts := make([]string, seamDigestMaxItems+64)
	for i := range texts {
		texts[i] = "benchmark-payload-" + strings.Repeat("x", 64)
	}
	call := seamTestCall(texts...)
	key := seamTestKey()
	b.ResetTimer()
	for b.Loop() {
		if got := seamItemDigests(call, key); len(got) != seamDigestMaxItems {
			b.Fatalf("digests=%d want %d", len(got), seamDigestMaxItems)
		}
	}
}

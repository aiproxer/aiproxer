package compactionfacts

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const maxMarkerLen = 64

var knownStartMarkers = []string{
	MarkerCodexCheckpoint,
	MarkerConversationTag,
	MarkerPiSummaryCarrier,
	MarkerClineSummaryPost,
	MarkerSystemNotice,
	MarkerClineCompactedPost,
	MarkerOpenCodeHistoryHead,
	MarkerHermesRefOnly,
	MarkerHermesLegacySummary,
	MarkerKiloObjective,
	MarkerKiloDetails,
	MarkerKiloWorkState,
	MarkerKiloNextMove,
	MarkerKiloFiles,
	MarkerAiderUserTag,
	MarkerAiderAssistantTag,
	"summar",
	"checkpoint",
	"continuation-note",
	"text only",
	"compaction",
	"conversation",
	"state snapshot",
	"generate",
	"verify",
	"condense",
	"session summary",
	"preserve",
	"context",
}

var knownStartMarkersBytes [][]byte

func init() {
	knownStartMarkersBytes = make([][]byte, len(knownStartMarkers))
	for i, m := range knownStartMarkers {
		knownStartMarkersBytes[i] = []byte(m)
	}
}

// foldRune applies canonical strings.ToLower Unicode mapping and folding.
// It maps uppercase runes to lowercase (including Kelvin sign \u212A -> 'k')
// matching canonical strings.ToLower marker expectations.
func foldRune(r rune) rune {
	return unicode.ToLower(r)
}

// TextMatcher performs bounded substring matching for start-rule markers
// across streamed chunks of text without buffering full request text.
// It distinguishes chunks within ONE field (carrying overlap and incomplete UTF-8 bytes)
// from different fields (separated by the canonical '\n' field boundary).
type TextMatcher struct {
	found          map[string]bool
	tailOverlap    [maxMarkerLen]byte
	tailOverlapLen int
	pendingUTF8    [4]byte
	pendingUTF8Len int
	buf            []byte
	scratch        []byte
}

// NewTextMatcher constructs an empty bounded text matcher.
func NewTextMatcher() *TextMatcher {
	return &TextMatcher{
		found:   make(map[string]bool, len(knownStartMarkers)),
		buf:     make([]byte, 0, 32*1024+maxMarkerLen+4),
		scratch: make([]byte, 0, 32*1024+4),
	}
}

// incompleteUTF8SuffixLen returns the number of trailing bytes (1..3) that form
// an incomplete multi-byte UTF-8 sequence at the end of data.
func incompleteUTF8SuffixLen(data []byte) int {
	n := len(data)
	if n == 0 {
		return 0
	}
	b0 := data[n-1]
	if b0 >= 0xC2 && b0 <= 0xF4 {
		return 1
	}
	if n >= 2 && isContinuation(data[n-1]) {
		b1 := data[n-2]
		if b1 >= 0xE0 && b1 <= 0xF4 {
			return 2
		}
	}
	if n >= 3 && isContinuation(data[n-1]) && isContinuation(data[n-2]) {
		b2 := data[n-3]
		if b2 >= 0xF0 && b2 <= 0xF4 {
			return 3
		}
	}
	return 0
}

func isContinuation(b byte) bool {
	return b >= 0x80 && b <= 0xBF
}

func (tm *TextMatcher) feed(chunkBytes []byte, chunkStr string, isStr bool) int {
	chunkLen := len(chunkBytes)
	if isStr {
		chunkLen = len(chunkStr)
	}
	if chunkLen == 0 && tm.pendingUTF8Len == 0 {
		return 0
	}

	tm.buf = tm.buf[:0]
	if tm.tailOverlapLen > 0 {
		tm.buf = append(tm.buf, tm.tailOverlap[:tm.tailOverlapLen]...)
	}

	tm.scratch = tm.scratch[:0]
	if tm.pendingUTF8Len > 0 {
		tm.scratch = append(tm.scratch, tm.pendingUTF8[:tm.pendingUTF8Len]...)
		tm.pendingUTF8Len = 0
	}

	if isStr {
		tm.scratch = append(tm.scratch, chunkStr...)
	} else {
		tm.scratch = append(tm.scratch, chunkBytes...)
	}

	inc := incompleteUTF8SuffixLen(tm.scratch)
	if inc > 0 {
		copy(tm.pendingUTF8[:], tm.scratch[len(tm.scratch)-inc:])
		tm.pendingUTF8Len = inc
		tm.scratch = tm.scratch[:len(tm.scratch)-inc]
	}

	if len(tm.scratch) == 0 {
		return 0
	}

	raw := tm.scratch
	runes := 0
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		raw = raw[size:]
		runes++
		lr := foldRune(r)
		if lr < utf8.RuneSelf {
			tm.buf = append(tm.buf, byte(lr))
		} else {
			var runeBuf [utf8.UTFMax]byte
			n := utf8.EncodeRune(runeBuf[:], lr)
			tm.buf = append(tm.buf, runeBuf[:n]...)
		}
	}

	// Search for any un-found markers in tm.buf
	for i, m := range knownStartMarkersBytes {
		markerStr := knownStartMarkers[i]
		if !tm.found[markerStr] && bytes.Contains(tm.buf, m) {
			tm.found[markerStr] = true
		}
	}

	if len(tm.buf) > maxMarkerLen {
		copy(tm.tailOverlap[:], tm.buf[len(tm.buf)-maxMarkerLen:])
		tm.tailOverlapLen = maxMarkerLen
	} else {
		copy(tm.tailOverlap[:], tm.buf)
		tm.tailOverlapLen = len(tm.buf)
	}

	return runes
}

// FeedBytesChunk updates the matcher with a new byte slice within the CURRENT field.
// It carries overlap and incomplete UTF-8 bytes across chunks within this field,
// and returns the number of complete UTF-8 runes processed in this chunk.
func (tm *TextMatcher) FeedBytesChunk(chunk []byte) int {
	return tm.feed(chunk, "", false)
}

// FeedChunk updates the matcher with a new chunk of text within the CURRENT field.
// It carries overlap and incomplete UTF-8 bytes across chunks within this field,
// and returns the number of complete UTF-8 runes processed in this chunk.
func (tm *TextMatcher) FeedChunk(chunk string) int {
	return tm.feed(nil, chunk, true)
}

// flushPendingUTF8 processes any trailing incomplete UTF-8 bytes remaining in pendingUTF8
// (e.g. at end of stream or field) and returns the number of runes decoded.
func (tm *TextMatcher) flushPendingUTF8() int {
	if tm.pendingUTF8Len == 0 {
		return 0
	}
	raw := tm.pendingUTF8[:tm.pendingUTF8Len]
	tm.pendingUTF8Len = 0

	tm.buf = tm.buf[:0]
	if tm.tailOverlapLen > 0 {
		tm.buf = append(tm.buf, tm.tailOverlap[:tm.tailOverlapLen]...)
	}

	runes := 0
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		raw = raw[size:]
		runes++
		lr := foldRune(r)
		if lr < utf8.RuneSelf {
			tm.buf = append(tm.buf, byte(lr))
		} else {
			var runeBuf [utf8.UTFMax]byte
			n := utf8.EncodeRune(runeBuf[:], lr)
			tm.buf = append(tm.buf, runeBuf[:n]...)
		}
	}

	for i, m := range knownStartMarkersBytes {
		markerStr := knownStartMarkers[i]
		if !tm.found[markerStr] && bytes.Contains(tm.buf, m) {
			tm.found[markerStr] = true
		}
	}

	if len(tm.buf) > maxMarkerLen {
		copy(tm.tailOverlap[:], tm.buf[len(tm.buf)-maxMarkerLen:])
		tm.tailOverlapLen = maxMarkerLen
	} else {
		copy(tm.tailOverlap[:], tm.buf)
		tm.tailOverlapLen = len(tm.buf)
	}

	return runes
}

// EndField ends the current text field. It flushes any pending UTF-8 bytes and
// feeds the canonical '\n' separator, matching base collectCallText where each
// nonempty field is joined with a newline. This ensures markers cannot match
// across distinct field boundaries.
func (tm *TextMatcher) EndField() int {
	runes := tm.flushPendingUTF8()

	tm.buf = tm.buf[:0]
	if tm.tailOverlapLen > 0 {
		tm.buf = append(tm.buf, tm.tailOverlap[:tm.tailOverlapLen]...)
	}
	tm.buf = append(tm.buf, '\n')

	for i, m := range knownStartMarkersBytes {
		markerStr := knownStartMarkers[i]
		if !tm.found[markerStr] && bytes.Contains(tm.buf, m) {
			tm.found[markerStr] = true
		}
	}

	if len(tm.buf) > maxMarkerLen {
		copy(tm.tailOverlap[:], tm.buf[len(tm.buf)-maxMarkerLen:])
		tm.tailOverlapLen = maxMarkerLen
	} else {
		copy(tm.tailOverlap[:], tm.buf)
		tm.tailOverlapLen = len(tm.buf)
	}

	return runes
}

// FeedField feeds an entire field, immediately inserting the canonical field boundary.
func (tm *TextMatcher) FeedField(text string) int {
	if text == "" {
		return 0
	}
	runes := tm.FeedChunk(text)
	runes += tm.EndField()
	return runes
}

// Feed is an alias for FeedChunk for backward compatibility with chunk streaming.
func (tm *TextMatcher) Feed(chunk string) {
	_ = tm.FeedChunk(chunk)
}

// HasText reports whether the specified marker was encountered in fed text.
func (tm *TextMatcher) HasText(marker string) bool {
	if tm == nil {
		return false
	}
	lower := strings.ToLower(marker)
	if v, ok := tm.found[lower]; ok && v {
		return true
	}
	if tm.tailOverlapLen > 0 {
		return bytes.Contains(tm.tailOverlap[:tm.tailOverlapLen], []byte(lower))
	}
	return false
}

// Builder accumulates bounded semantic facts across incremental decoded fragments.
type Builder struct {
	op          aipapi.Operation
	toolCount   int
	maxItems    int // <= 0 means unbounded
	itemCount   int
	itemHashes  [][32]byte
	totalRunes  int
	textMatcher *TextMatcher
	err         error // sticky error
}

// NewBuilder creates a new facts Builder. maxFactItems bounds the number of retained
// ItemHashes.
//   - If maxFactItems > 0: strictly bounds retained item hashes. When exceeded, the
//     builder sets a sticky error (ErrFactBudgetExceeded) and Build returns empty facts
//     and the sticky error. The bound is checked BEFORE item-hash allocation.
//   - If maxFactItems <= 0: unbounded (retains all item hashes without limit), preserving
//     canonical exact semantics for arbitrary item counts.
func NewBuilder(op aipapi.Operation, maxFactItems int) *Builder {
	return &Builder{
		op:          op,
		maxItems:    maxFactItems,
		textMatcher: NewTextMatcher(),
	}
}

// NewBuilderWithByteBudget creates a Builder whose item hash budget is derived from
// a byte budget: maxFactItems = maxFactBytes / ItemHashSizeBytes.
// If maxFactBytes <= 0, the builder is unbounded.
func NewBuilderWithByteBudget(op aipapi.Operation, maxFactBytes int) *Builder {
	if maxFactBytes <= 0 {
		return NewBuilder(op, 0)
	}
	items := max(maxFactBytes/ItemHashSizeBytes, 1)
	return NewBuilder(op, items)
}

// AddToolCount increments the tool count for the request.
func (b *Builder) AddToolCount(n int) {
	if b.err != nil {
		return
	}
	b.toolCount += n
}

// FeedTextChunk feeds a chunk of text within the current field into the bounded text
// matcher and accumulates its UTF-8 rune count towards EstimatedTokens.
func (b *Builder) FeedTextChunk(chunk string) {
	if b.err != nil || chunk == "" {
		return
	}
	runes := b.textMatcher.FeedChunk(chunk)
	b.totalRunes += runes
}

// FeedTextChunkBytes feeds a chunk of byte text within the current field into the bounded text
// matcher and accumulates its UTF-8 rune count towards EstimatedTokens.
func (b *Builder) FeedTextChunkBytes(chunk []byte) {
	if b.err != nil || len(chunk) == 0 {
		return
	}
	runes := b.textMatcher.FeedBytesChunk(chunk)
	b.totalRunes += runes
}

// EndField ends the current text field, flushing any pending UTF-8 bytes and
// inserting the canonical '\n' field boundary.
func (b *Builder) EndField() {
	if b.err != nil {
		return
	}
	runes := b.textMatcher.EndField()
	b.totalRunes += runes
}

// FeedField feeds an entire field and ends it with a canonical field boundary.
func (b *Builder) FeedField(text string) {
	if b.err != nil || text == "" {
		return
	}
	b.FeedTextChunk(text)
	b.EndField()
}

// AddItemHash appends a precomputed item hash. If the budget is exceeded,
// it sets and returns a sticky ErrFactBudgetExceeded BEFORE allocating or appending.
func (b *Builder) AddItemHash(h [32]byte) error {
	if b.err != nil {
		return b.err
	}
	if b.maxItems > 0 && len(b.itemHashes) >= b.maxItems {
		b.err = ErrFactBudgetExceeded
		return b.err
	}
	b.itemHashes = append(b.itemHashes, h)
	b.itemCount++
	return nil
}

// AddItem hashes a complete aipapi.Item and feeds its text fields into the
// text matcher and token counter with proper field boundaries. If the budget is
// exceeded, it sets and returns a sticky ErrFactBudgetExceeded BEFORE hashing
// or allocating.
func (b *Builder) AddItem(it aipapi.Item) error {
	if b.err != nil {
		return b.err
	}
	if b.maxItems > 0 && len(b.itemHashes) >= b.maxItems {
		b.err = ErrFactBudgetExceeded
		return b.err
	}
	h := HashItem(it)
	if err := b.AddItemHash(h); err != nil {
		return err
	}
	if it.ToolCall != nil {
		b.toolCount++
	}
	for _, cp := range it.Content {
		if cp.Text != "" {
			b.FeedField(cp.Text)
		}
		if cp.Refusal != "" {
			b.FeedField(cp.Refusal)
		}
		if cp.Summary != "" {
			b.FeedField(cp.Summary)
		}
		if cp.Reasoning != nil && cp.Reasoning.Text != "" {
			b.FeedField(cp.Reasoning.Text)
		}
	}
	if it.ToolResult != nil {
		if it.ToolResult.Output != "" {
			b.FeedField(it.ToolResult.Output)
		}
		for _, cp := range it.ToolResult.Parts {
			if cp.Text != "" {
				b.FeedField(cp.Text)
			}
			if cp.Refusal != "" {
				b.FeedField(cp.Refusal)
			}
			if cp.Summary != "" {
				b.FeedField(cp.Summary)
			}
			if cp.Reasoning != nil && cp.Reasoning.Text != "" {
				b.FeedField(cp.Reasoning.Text)
			}
		}
	}
	if it.Reasoning != nil && it.Reasoning.Reasoning != nil && it.Reasoning.Reasoning.Text != "" {
		b.FeedField(it.Reasoning.Reasoning.Text)
	}
	return nil
}

// Build produces the final bounded RequestFacts. If the builder encountered an error
// (such as ErrFactBudgetExceeded), it returns an empty RequestFacts and the sticky error.
func (b *Builder) Build() (RequestFacts, error) {
	if b.err != nil {
		return RequestFacts{}, b.err
	}

	// Flush any trailing pending UTF-8 bytes.
	runes := b.textMatcher.flushPendingUTF8()
	b.totalRunes += runes

	facts := RequestFacts{
		Operation:       b.op,
		ToolCount:       b.toolCount,
		EstimatedTokens: b.totalRunes / 4,
		ItemCount:       b.itemCount,
		ItemHashes:      b.itemHashes,
	}

	nTail := min(len(b.itemHashes), HeuristicTailItems)
	for i := range nTail {
		facts.TailHashes[i] = b.itemHashes[len(b.itemHashes)-nTail+i]
	}
	facts.TailLen = nTail

	nPrefix := min(len(b.itemHashes), HeuristicPrefixItems)
	var prefix []byte
	for i := range nPrefix {
		prefix = append(prefix, b.itemHashes[i][:]...)
	}
	facts.PrefixHash = sha256.Sum256(prefix)
	facts.PrefixItems = nPrefix

	rule, matched := MatchStartRule(b.op, b.toolCount, b.textMatcher.HasText)
	facts.StartRuleMatched = matched
	facts.StartRuleID = rule.ID
	facts.StartRuleMode = rule.Mode
	facts.StartRuleEvidence = rule.Evidence

	return facts, nil
}

// ExtractFactsFromCall extracts exact bounded RequestFacts from a canonical Call.
// It preserves ALL WalkCallTexts sources with canonical '\n' field boundaries,
// and retains full exact semantics for arbitrary item counts without silent truncation.
func ExtractFactsFromCall(call aipapi.Call) RequestFacts {
	items := aipapi.NormalizedItems(call)
	toolCount := len(call.Tools)
	hashes := make([][32]byte, 0, len(items))
	for _, it := range items {
		if it.ToolCall != nil {
			toolCount++
		}
		hashes = append(hashes, HashItem(it))
	}

	matcher := NewTextMatcher()
	totalRunes := 0
	_ = aipapi.WalkCallTexts(call, func(_ string, text string) error {
		if text == "" {
			return nil
		}
		totalRunes += utf8.RuneCountInString(text)
		matcher.FeedField(text)
		return nil
	})

	facts := RequestFacts{
		Operation:       call.Invocation.Operation,
		ToolCount:       toolCount,
		EstimatedTokens: totalRunes / 4,
		ItemCount:       len(items),
		ItemHashes:      hashes,
	}

	nTail := min(len(hashes), HeuristicTailItems)
	for i := range nTail {
		facts.TailHashes[i] = hashes[len(hashes)-nTail+i]
	}
	facts.TailLen = nTail

	nPrefix := min(len(hashes), HeuristicPrefixItems)
	var prefix []byte
	for i := range nPrefix {
		prefix = append(prefix, hashes[i][:]...)
	}
	facts.PrefixHash = sha256.Sum256(prefix)
	facts.PrefixItems = nPrefix

	rule, matched := MatchStartRule(call.Invocation.Operation, toolCount, matcher.HasText)
	facts.StartRuleMatched = matched
	facts.StartRuleID = rule.ID
	facts.StartRuleMode = rule.Mode
	facts.StartRuleEvidence = rule.Evidence

	return facts
}

// EstimateTokens calculates characters/4 from all WalkCallTexts sources.
func EstimateTokens(call aipapi.Call) int {
	total := 0
	_ = aipapi.WalkCallTexts(call, func(_ string, text string) error {
		total += utf8.RuneCountInString(text)
		return nil
	})
	return total / 4
}

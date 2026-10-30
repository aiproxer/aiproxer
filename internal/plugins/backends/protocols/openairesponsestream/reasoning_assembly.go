package openairesponsestream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/mediaurl"
	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openairesponsesitem"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func reasoningItemError(fixed string) error {
	return fmt.Errorf("openairesponses: invalid reasoning item: %s", fixed)
}

// In-flight assembly bounds reuse canonical call/part caps so a single stream cannot
// retain unbounded draft maps or summary/content delta text.
const (
	maxReasoningDrafts        = aipapi.MaxReasoningPartsPerMessage
	maxReasoningAssemblyBytes = aipapi.MaxReasoningBytesPerCall
)

type textPartDraft struct {
	text string
	set  bool
}

type reasoningDraft struct {
	outputIndex int64
	id          string
	open        bool
	ready       bool
	emitted     bool
	// awaitingCompleted is set when stream output_item.done observed a non-terminal
	// status (in_progress/incomplete). It must be cleared by response.completed via
	// upgrade-to-ready or explicit abandon; otherwise finalize/EOF fail closed.
	awaitingCompleted bool
	opaque            json.RawMessage

	seedRaw         json.RawMessage
	summaryParts    map[int64]*textPartDraft
	contentParts    map[int64]*textPartDraft
	contentObserved bool
	readyItem       responses.ResponseOutputItemUnion
	readyHasItem    bool
}

func reasoningAssemblyError(reason string) error {
	return fmt.Errorf("openairesponses: reasoning assembly: %s", reason)
}

func (m *Mapper) ensureReasoningMaps() {
	if m.reasoningByID == nil {
		m.reasoningByID = make(map[string]*reasoningDraft)
	}
	if m.reasoningByIndex == nil {
		m.reasoningByIndex = make(map[int64]*reasoningDraft)
	}
}

func (m *Mapper) addAssemblyBytes(n int) error {
	if n <= 0 {
		return nil
	}
	if m.reasoningAssemblyBytes > maxReasoningAssemblyBytes-n {
		m.discardReasoningDrafts()
		return reasoningAssemblyError("byte limit")
	}
	m.reasoningAssemblyBytes += n
	return nil
}

func (m *Mapper) discardReasoningDrafts() {
	m.reasoningByID = nil
	m.reasoningByIndex = nil
	m.reasoningAssemblyBytes = 0
}

// AbortReasoningAssembly drops in-flight drafts without error (upstream cancel/error path).
func (m *Mapper) AbortReasoningAssembly() {
	m.discardReasoningDrafts()
}

// FinalizeOnEOF fails closed when reasoning drafts remain open/ready/awaitingCompleted
// without response.completed. Streams with no reasoning drafts return nil. After a
// successful completed reconcile, this is a no-op.
func (m *Mapper) FinalizeOnEOF() error {
	if m.reasoningCompletedFinal {
		return nil
	}
	return m.failIfUnresolvedReasoning()
}

func (m *Mapper) failIfUnresolvedReasoning() error {
	m.ensureReasoningMaps()
	for _, d := range m.reasoningByID {
		if d == nil || d.emitted {
			continue
		}
		if d.open || d.ready || d.awaitingCompleted {
			m.discardReasoningDrafts()
			return reasoningAssemblyError("unresolved ordering hole")
		}
	}
	return nil
}

func (m *Mapper) finalizeCompletedReasoning() error {
	if err := m.flushReasoningReady(); err != nil {
		return err
	}
	if err := m.failIfUnresolvedReasoning(); err != nil {
		return err
	}
	m.reasoningCompletedFinal = true
	return nil
}

func (m *Mapper) draftFor(itemID string, outputIndex int64) (*reasoningDraft, error) {
	m.ensureReasoningMaps()
	if itemID == "" {
		return nil, nil
	}
	if d, ok := m.reasoningByID[itemID]; ok {
		// Never silently move an emitted/ready draft across output indices.
		if d.outputIndex != outputIndex && (d.emitted || d.ready) {
			return d, nil
		}
		if d.outputIndex != outputIndex {
			if other, ok := m.reasoningByIndex[outputIndex]; ok && other != nil && other != d && other.id != itemID {
				return nil, reasoningAssemblyError("index collision")
			}
			delete(m.reasoningByIndex, d.outputIndex)
			d.outputIndex = outputIndex
			m.reasoningByIndex[outputIndex] = d
		}
		return d, nil
	}
	if other, ok := m.reasoningByIndex[outputIndex]; ok && other != nil && other.id != itemID {
		return nil, reasoningAssemblyError("index collision")
	}
	if len(m.reasoningByID) >= maxReasoningDrafts {
		return nil, reasoningAssemblyError("draft limit")
	}
	d := &reasoningDraft{
		outputIndex:  outputIndex,
		id:           itemID,
		open:         true,
		summaryParts: make(map[int64]*textPartDraft),
		contentParts: make(map[int64]*textPartDraft),
	}
	m.reasoningByID[itemID] = d
	m.reasoningByIndex[outputIndex] = d
	return d, nil
}

func (d *reasoningDraft) ensureMaps() {
	if d.summaryParts == nil {
		d.summaryParts = make(map[int64]*textPartDraft)
	}
	if d.contentParts == nil {
		d.contentParts = make(map[int64]*textPartDraft)
	}
}

func (d *reasoningDraft) summaryPart(idx int64) *textPartDraft {
	d.ensureMaps()
	p, ok := d.summaryParts[idx]
	if !ok {
		p = &textPartDraft{}
		d.summaryParts[idx] = p
	}
	return p
}

func (d *reasoningDraft) contentPart(idx int64) *textPartDraft {
	d.ensureMaps()
	p, ok := d.contentParts[idx]
	if !ok {
		p = &textPartDraft{}
		d.contentParts[idx] = p
	}
	return p
}

// ReasoningOutputItemAdded opens mapper-private assembly for a reasoning item.
func (m *Mapper) ReasoningOutputItemAdded(outputIndex int64, item responses.ResponseOutputItemUnion) error {
	if item.Type != "reasoning" {
		return nil
	}
	id := reasoningItemID(item)
	if id == "" {
		return nil
	}
	d, err := m.draftFor(id, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	d.open = true
	raw := item.RawJSON()
	if raw == "" {
		raw = item.AsReasoning().RawJSON()
	}
	if raw != "" {
		prev := len(d.seedRaw)
		d.seedRaw = append(json.RawMessage(nil), raw...)
		if _, err := openairesponsesitem.ParseIncompleteFields(d.seedRaw); err != nil {
			return err
		}
		if err := m.addAssemblyBytes(len(d.seedRaw) - prev); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningSummaryPartAdded records a typed summary slot without canonical emission.
func (m *Mapper) ReasoningSummaryPartAdded(itemID string, outputIndex, summaryIndex int64, text string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	p := d.summaryPart(summaryIndex)
	if !p.set {
		old := len(p.text)
		p.text = text
		if err := m.addAssemblyBytes(len(p.text) - old); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningSummaryPartDone sets the authoritative summary part text for an index.
func (m *Mapper) ReasoningSummaryPartDone(itemID string, outputIndex, summaryIndex int64, text string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	p := d.summaryPart(summaryIndex)
	old := len(p.text)
	p.text = text
	p.set = true
	if len(text) > old {
		if err := m.addAssemblyBytes(len(text) - old); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningSummaryTextDelta appends provider-observed summary text for an index.
func (m *Mapper) ReasoningSummaryTextDelta(itemID string, outputIndex, summaryIndex int64, delta string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	p := d.summaryPart(summaryIndex)
	if p.set {
		return nil
	}
	if err := m.addAssemblyBytes(len(delta)); err != nil {
		return err
	}
	p.text += delta
	return nil
}

// ReasoningSummaryTextDone sets summary text (does not append onto prior deltas).
func (m *Mapper) ReasoningSummaryTextDone(itemID string, outputIndex, summaryIndex int64, text string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	p := d.summaryPart(summaryIndex)
	old := len(p.text)
	p.text = text
	p.set = true
	if len(text) > old {
		if err := m.addAssemblyBytes(len(text) - old); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningTextDelta appends provider-observed reasoning content text for an index.
func (m *Mapper) ReasoningTextDelta(itemID string, outputIndex, contentIndex int64, delta string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	d.contentObserved = true
	p := d.contentPart(contentIndex)
	if p.set {
		return nil
	}
	if err := m.addAssemblyBytes(len(delta)); err != nil {
		return err
	}
	p.text += delta
	return nil
}

// ReasoningTextDone sets reasoning content text (does not append onto prior deltas).
func (m *Mapper) ReasoningTextDone(itemID string, outputIndex, contentIndex int64, text string) error {
	d, err := m.draftFor(itemID, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	d.contentObserved = true
	p := d.contentPart(contentIndex)
	old := len(p.text)
	p.text = text
	p.set = true
	if len(text) > old {
		if err := m.addAssemblyBytes(len(text) - old); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningOutputItemDone validates and queues one terminal EventReasoningPart when legal.
func (m *Mapper) ReasoningOutputItemDone(outputIndex int64, item responses.ResponseOutputItemUnion) error {
	if item.Type != "reasoning" {
		return nil
	}
	if err := m.queueReasoningReady(outputIndex, item, false); err != nil {
		return err
	}
	return m.flushReasoningReady()
}

// EmitCompletedReasoningItems emits any not-yet-emitted valid reasoning items by output index.
func (m *Mapper) EmitCompletedReasoningItems(resp responses.Response) error {
	for i, item := range resp.Output {
		if item.Type != "reasoning" {
			continue
		}
		if err := m.queueReasoningReady(int64(i), item, true); err != nil {
			return err
		}
	}
	return m.finalizeCompletedReasoning()
}

// EmitCompletedOutputByIndex emits completed-response output in structural output_index order:
// reasoning exact parts interleaved with message text fallback (and media) relative to other items.
func (m *Mapper) EmitCompletedOutputByIndex(resp responses.Response) error {
	for i, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			if err := m.queueReasoningReady(int64(i), item, true); err != nil {
				return err
			}
			if err := m.flushReasoningReady(); err != nil {
				return err
			}
		case "message":
			if err := m.flushReasoningReady(); err != nil {
				return err
			}
			if !m.SawTextDelta() {
				if err := m.CompletedTextFallback(messageOutputText(item)); err != nil {
					return err
				}
			}
			if err := emitOutputMediaFromMessage(m, item); err != nil {
				return err
			}
		case "function_call":
			if err := m.flushReasoningReady(); err != nil {
				return err
			}
			fc := item.AsFunctionCall()
			if err := m.EmitCompletedToolCall(ToolCallID(fc.ID, fc.CallID), fc.Name, fc.Arguments); err != nil {
				return err
			}
		}
	}
	return m.finalizeCompletedReasoning()
}

func (m *Mapper) queueReasoningReady(outputIndex int64, item responses.ResponseOutputItemUnion, fromCompleted bool) error {
	id := reasoningItemID(item)
	if id == "" {
		if _, err := buildReasoningOpaque(nil, item); err != nil {
			return err
		}
		return reasoningItemError("invalid id")
	}
	d, err := m.draftFor(id, outputIndex)
	if err != nil {
		return err
	}
	if d == nil {
		return reasoningItemError("invalid id")
	}
	if d.emitted {
		if d.outputIndex != outputIndex {
			return reasoningItemError("duplicate id at different output_index")
		}
		opaque, err := buildReasoningOpaque(d, item)
		if err != nil {
			return err
		}
		if !reasoningOpaqueSemanticallyEqual(d.opaque, opaque) {
			return reasoningItemError("conflicting duplicate id")
		}
		return nil
	}
	status := reasoningItemStatus(item)
	if status == "in_progress" || status == "incomplete" {
		d.open = false
		d.ready = false
		if fromCompleted {
			// Completed payload explicitly resolves the item as non-emittable.
			d.awaitingCompleted = false
		} else {
			// Stream done with non-terminal status: must reconcile via response.completed.
			d.awaitingCompleted = true
		}
		return nil
	}
	if d.outputIndex != outputIndex && (d.ready || d.emitted) {
		return reasoningItemError("duplicate id at different output_index")
	}
	if other, ok := m.reasoningByIndex[outputIndex]; ok && other != nil && other != d {
		return reasoningAssemblyError("index collision")
	}
	if d.outputIndex != outputIndex {
		delete(m.reasoningByIndex, d.outputIndex)
		d.outputIndex = outputIndex
	}
	d.ready = true
	d.readyItem = item
	d.readyHasItem = true
	d.open = false
	d.awaitingCompleted = false
	m.reasoningByIndex[outputIndex] = d
	return nil
}

func (m *Mapper) flushReasoningReady() error {
	m.ensureReasoningMaps()
	for {
		var ready []*reasoningDraft
		for _, d := range m.reasoningByID {
			if d != nil && d.ready && !d.emitted && d.readyHasItem {
				ready = append(ready, d)
			}
		}
		if len(ready) == 0 {
			return nil
		}
		sort.Slice(ready, func(i, j int) bool {
			return ready[i].outputIndex < ready[j].outputIndex
		})
		d := ready[0]
		if m.hasOpenReasoningBelow(d.outputIndex) {
			return nil
		}
		if err := m.emitReasoningDraft(d); err != nil {
			return err
		}
	}
}

func (m *Mapper) hasOpenReasoningBelow(index int64) bool {
	for idx, d := range m.reasoningByIndex {
		if idx < index && d != nil && !d.emitted && (d.open || d.awaitingCompleted) {
			return true
		}
	}
	for _, d := range m.reasoningByID {
		if d == nil || d.emitted {
			continue
		}
		if d.outputIndex < index && (d.open || d.awaitingCompleted) {
			return true
		}
	}
	return false
}

func (m *Mapper) emitReasoningDraft(d *reasoningDraft) error {
	if d == nil || d.emitted || !d.readyHasItem {
		return nil
	}
	opaque, err := buildReasoningOpaque(d, d.readyItem)
	if err != nil {
		return err
	}
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opaque, &probe); err != nil || probe.ID == "" {
		return reasoningItemError("invalid id")
	}
	if existing, ok := m.reasoningByID[probe.ID]; ok && existing.emitted {
		if existing.outputIndex != d.outputIndex {
			return reasoningItemError("duplicate id at different output_index")
		}
		if !reasoningOpaqueSemanticallyEqual(existing.opaque, opaque) {
			return reasoningItemError("conflicting duplicate id")
		}
		d.ready = false
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	part := &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  append(json.RawMessage(nil), opaque...),
	}
	if err := m.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: part}); err != nil {
		return err
	}
	d.emitted = true
	d.ready = false
	d.open = false
	d.opaque = append(json.RawMessage(nil), opaque...)
	d.id = probe.ID
	m.reasoningByID[probe.ID] = d
	m.reasoningByIndex[d.outputIndex] = d
	return nil
}

func buildReasoningOpaque(d *reasoningDraft, item responses.ResponseOutputItemUnion) (json.RawMessage, error) {
	raw := item.RawJSON()
	if raw == "" {
		raw = item.AsReasoning().RawJSON()
	}

	var fields map[string]json.RawMessage
	switch {
	case len(bytes.TrimSpace([]byte(raw))) == 0:
		// No authoritative done raw: assembly/seed may supply allowlisted fields only.
		fields = map[string]json.RawMessage{}
	default:
		parsed, err := openairesponsesitem.ParseIncompleteFields([]byte(raw))
		if err != nil {
			// Authoritative done raw present but invalid: never sanitize via assembly.
			return nil, err
		}
		fields = parsed
	}

	if d != nil && d.seedRaw != nil {
		seed, err := openairesponsesitem.ParseIncompleteFields(d.seedRaw)
		if err != nil {
			return nil, err
		}
		for k, v := range seed {
			if _, ok := fields[k]; !ok {
				fields[k] = v
			}
		}
	}

	// Precedence: done/raw fields win when present; assembly fills omitted summary/content.
	if _, ok := fields["summary"]; !ok {
		if d != nil {
			if sum, ok := d.assembledSummaryJSON(); ok {
				fields["summary"] = sum
			}
		}
	}
	if _, ok := fields["content"]; !ok {
		if d != nil && d.contentObserved {
			if content, ok := d.assembledContentJSON(); ok {
				fields["content"] = content
			}
		}
	}

	if idRaw, ok := fields["id"]; !ok || len(bytes.TrimSpace(idRaw)) == 0 {
		if d != nil && d.id != "" {
			idBytes, err := json.Marshal(d.id)
			if err != nil {
				return nil, reasoningItemError("invalid id")
			}
			fields["id"] = idBytes
		}
	}

	// Reconstruct allowlisted key order, then validate via Canonize.
	rawOrdered, err := openairesponsesitem.MarshalEnvelope(fields)
	if err != nil {
		return nil, reasoningItemError("malformed")
	}
	return openairesponsesitem.CanonizeReasoningItemOpaque(rawOrdered)
}

func (d *reasoningDraft) assembledSummaryJSON() (json.RawMessage, bool) {
	if d == nil || len(d.summaryParts) == 0 {
		return nil, false
	}
	idxs := make([]int64, 0, len(d.summaryParts))
	for i := range d.summaryParts {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	parts := make([]map[string]string, 0, len(idxs))
	for _, i := range idxs {
		p := d.summaryParts[i]
		if p == nil {
			continue
		}
		parts = append(parts, map[string]string{"type": "summary_text", "text": p.text})
	}
	if len(parts) == 0 {
		return nil, false
	}
	b, err := json.Marshal(parts)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

func (d *reasoningDraft) assembledContentJSON() (json.RawMessage, bool) {
	if d == nil || len(d.contentParts) == 0 {
		return nil, false
	}
	idxs := make([]int64, 0, len(d.contentParts))
	for i := range d.contentParts {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	parts := make([]map[string]string, 0, len(idxs))
	for _, i := range idxs {
		p := d.contentParts[i]
		if p == nil {
			continue
		}
		parts = append(parts, map[string]string{"type": "reasoning_text", "text": p.text})
	}
	if len(parts) == 0 {
		return nil, false
	}
	b, err := json.Marshal(parts)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

// reasoningOpaqueSemanticallyEqual treats status-only upgrades
// (absent/in_progress -> completed) as non-conflicting when other allowlisted
// fields match. Other status changes and field differences remain conflicts.
func reasoningOpaqueSemanticallyEqual(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	af, err := openairesponsesitem.ParseIncompleteFields(a)
	if err != nil {
		return false
	}
	bf, err := openairesponsesitem.ParseIncompleteFields(b)
	if err != nil {
		return false
	}
	as := statusFromFields(af)
	bs := statusFromFields(bf)
	if !statusUpgradeCompatible(as, bs) {
		return false
	}
	delete(af, "status")
	delete(bf, "status")
	if len(af) != len(bf) {
		return false
	}
	for k, av := range af {
		bv, ok := bf[k]
		if !ok || !bytes.Equal(bytes.TrimSpace(av), bytes.TrimSpace(bv)) {
			return false
		}
	}
	return true
}

func statusFromFields(fields map[string]json.RawMessage) string {
	raw, ok := fields["status"]
	if !ok {
		return ""
	}
	var status string
	if err := json.Unmarshal(raw, &status); err != nil {
		return ""
	}
	return status
}

func statusUpgradeCompatible(a, b string) bool {
	if a == b {
		return true
	}
	// Allow either ordering: emitted may be absent/in_progress and fallback completed, or reverse.
	pair := func(x, y string) bool {
		return (x == "" || x == "in_progress") && y == "completed"
	}
	return pair(a, b) || pair(b, a)
}

func reasoningItemID(item responses.ResponseOutputItemUnion) string {
	if id := item.AsReasoning().ID; id != "" {
		return id
	}
	raw := item.RawJSON()
	if raw == "" {
		return ""
	}
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	return probe.ID
}

func reasoningItemStatus(item responses.ResponseOutputItemUnion) string {
	st := string(item.AsReasoning().Status)
	if st != "" {
		return st
	}
	raw := item.RawJSON()
	if raw == "" {
		raw = item.AsReasoning().RawJSON()
	}
	if raw == "" {
		return ""
	}
	var probe struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(raw), &probe)
	return probe.Status
}

func messageOutputText(item responses.ResponseOutputItemUnion) string {
	if item.Type != "message" {
		return ""
	}
	// Prefer union Content (works for SDK-constructed fixtures without RawJSON).
	var b strings.Builder
	for _, c := range item.Content {
		if c.Type == "output_text" {
			b.WriteString(c.Text)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	msg := item.AsMessage()
	for _, c := range msg.Content {
		if c.Type == "output_text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func emitOutputMediaFromMessage(m *Mapper, item responses.ResponseOutputItemUnion) error {
	if item.Type != "message" {
		return nil
	}
	msg := item.AsMessage()
	for _, c := range msg.Content {
		raw := c.RawJSON()
		if raw == "" {
			continue
		}
		var probe struct {
			Type     string          `json:"type"`
			ImageURL json.RawMessage `json:"image_url"`
			FileID   string          `json:"file_id"`
		}
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "input_image":
			url := mediaurl.ExtractImageURL(probe.ImageURL)
			if url == "" {
				continue
			}
			if err := m.EnsureResponseStarted(); err != nil {
				return err
			}
			if err := m.EnsureMessageStarted(); err != nil {
				return err
			}
			if err := m.pending.Push(aipapi.Event{Kind: aipapi.EventAssistantImageRef, AssistantRef: url, AssistantMIME: mediaurl.SniffImageMIME(url)}); err != nil {
				return err
			}
		case "input_file":
			if strings.TrimSpace(probe.FileID) == "" {
				continue
			}
			if err := m.EnsureResponseStarted(); err != nil {
				return err
			}
			if err := m.EnsureMessageStarted(); err != nil {
				return err
			}
			if err := m.pending.Push(aipapi.Event{Kind: aipapi.EventAssistantFileRef, AssistantRef: probe.FileID, AssistantMIME: "application/octet-stream"}); err != nil {
				return err
			}
		}
	}
	return nil
}

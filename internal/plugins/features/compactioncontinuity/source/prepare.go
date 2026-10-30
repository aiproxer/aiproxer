package source

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// Prepare walks a canonical call without mutating it or making external
// calls. User decision text has highest priority, followed by relevant
// assistant planning and recognized structured carriers. Ordinary tools,
// media, reasoning, logs and dumps are omitted.
func Prepare(ctx context.Context, in Input) (Prepared, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	cfg := in.Config.normalized()
	items := aipapi.NormalizedItems(in.Call)
	previous := in.Previous
	if isZeroWatermark(previous) {
		previous = in.Existing.HighWatermark
	}

	start := 0
	existing := cloneEntries(in.Existing.Entries)
	if previous.ItemCount > 0 && previous.ItemCount <= len(items) && previous.Digest != "" && digestItems(items[:previous.ItemCount]) == previous.Digest && len(in.Existing.Entries) > 0 {
		start = previous.ItemCount
	} else if previous.ItemCount == len(items) && previous.Digest != "" && digestItems(items) == previous.Digest && len(in.Existing.Entries) > 0 {
		start = len(items)
	} else if previous.ItemCount > 0 || len(in.Existing.Entries) > 0 {
		// A missing or changed source prefix cannot safely be merged with the
		// existing window; rebuild from the canonical call.
		existing = nil
	}

	newEntries := make([]Entry, 0)
	for i := start; i < len(items); i++ {
		if entry, ok := prepareItem(ctx, items[i], i, in, cfg); ok {
			entry.New = true
			newEntries = append(newEntries, entry)
		}
	}
	all := append(existing, newEntries...)
	all = boundEntries(all, cfg)

	w := HighWatermark{ItemCount: len(items), Digest: digestItems(items)}
	for i := range all {
		all[i].New = all[i].New && all[i].ItemIndex >= start
	}
	envelope := Envelope{Version: EnvelopeVersion, HighWatermark: w, Entries: all}
	envelope.Bytes = entriesPayloadBytes(all)

	// Keep NewEntries aligned with the bounded envelope. Entries removed by
	// count/byte limits must never trigger a semantic call.
	kept := make(map[string]struct{}, len(all))
	for _, e := range all {
		kept[entryKey(e)] = struct{}{}
	}
	filteredNew := newEntries[:0]
	for _, e := range newEntries {
		if _, ok := kept[entryKey(e)]; ok {
			filteredNew = append(filteredNew, e)
		}
	}
	newEntries = filteredNew
	decision := EvaluateEligibility(EligibilityInput{Entries: newEntries, OnlyNew: true})
	return Prepared{Envelope: envelope, NewEntries: cloneEntries(newEntries), HighWatermark: w, Candidate: decision.Eligible}, nil
}

func prepareItem(ctx context.Context, item aipapi.Item, index int, in Input, cfg Config) (Entry, bool) {
	if in.Recognizer != nil {
		if carrier, ok := in.Recognizer.Recognize(item); ok {
			carrier.Type = strings.TrimSpace(carrier.Type)
			carrier.Payload = strings.TrimSpace(carrier.Payload)
			if carrier.Type != "" && carrier.Version > 0 && carrier.Payload != "" && len(carrier.Payload) <= cfg.MaxCarrierBytes && json.Valid([]byte(carrier.Payload)) {
				payload, ok := redact(ctx, in.Redactor, carrier.Payload)
				if !ok || !json.Valid([]byte(payload)) {
					return Entry{}, false
				}
				return Entry{Kind: EntryStructuredCarrier, ItemID: item.ID, ItemIndex: index, Role: item.Role, Carrier: &StructuredCarrier{Type: carrier.Type, Version: carrier.Version, Payload: payload}, priority: 2}, true
			}
		}
	}

	if item.Kind == aipapi.ItemKindToolResult {
		if item.ToolResult == nil || !toolOutputRelevant(item.ToolResult.Output) || len(item.ToolResult.Output) > cfg.MaxUntrustedToolBytes {
			return Entry{}, false
		}
		text, ok := redact(ctx, in.Redactor, strings.TrimSpace(item.ToolResult.Output))
		if !ok {
			return Entry{}, false
		}
		text = boundUntrusted(text, cfg.MaxUntrustedToolBytes)
		if text == "" {
			return Entry{}, false
		}
		return Entry{Kind: EntryUntrustedTool, ItemID: item.ID, ItemIndex: index, Role: aipapi.RoleTool, Text: UntrustedOpen + text + UntrustedClose, Untrusted: true, priority: 3}, true
	}
	if item.Kind != aipapi.ItemKindMessage {
		return Entry{}, false
	}

	var texts []string
	for _, cp := range item.Content {
		switch cp.Kind {
		case aipapi.ContentPartText:
			texts = append(texts, cp.Text)
		case aipapi.ContentPartRefusal:
			if item.Role == aipapi.RoleUser {
				texts = append(texts, cp.Refusal)
			}
		case aipapi.ContentPartSummary:
			if item.Role == aipapi.RoleAssistant {
				texts = append(texts, cp.Summary)
			}
		}
	}
	text := normalizeText(strings.Join(texts, "\n"))
	if text == "" || likelyDump(text) {
		return Entry{}, false
	}
	switch item.Role {
	case aipapi.RoleUser:
		text, ok := redact(ctx, in.Redactor, text)
		if !ok {
			return Entry{}, false
		}
		decision := explicitDecision(text)
		kind := EntryUserText
		priority := 1
		if decision {
			kind, priority = EntryUserDecision, 0
		}
		text = truncateUTF8(normalizeText(text), cfg.MaxEntryBytes)
		if text == "" {
			return Entry{}, false
		}
		return Entry{Kind: kind, ItemID: item.ID, ItemIndex: index, Role: item.Role, Text: text, DecisionRelevant: decision, priority: priority}, true
	case aipapi.RoleAssistant:
		planning := planningSignal(text)
		if !planning || len([]byte(text)) < 12 {
			return Entry{}, false
		}
		text, ok := redact(ctx, in.Redactor, text)
		if !ok {
			return Entry{}, false
		}
		text = truncateUTF8(normalizeText(text), cfg.MaxEntryBytes)
		return Entry{Kind: EntryAssistantPlan, ItemID: item.ID, ItemIndex: index, Role: item.Role, Text: text, PlanningRelevant: true, priority: 1}, text != ""
	default:
		return Entry{}, false
	}
}

func redact(ctx context.Context, r Redactor, text string) (string, bool) {
	if r == nil {
		if matcher, ok := secretguard.RequestMatcherFromContext(ctx); ok {
			r = MatcherRedactor{Matcher: matcher}
		}
	}
	if r == nil {
		return text, true
	}
	out, err := r.Redact(ctx, text)
	if err != nil {
		return "", false
	}
	return normalizeText(out), true
}

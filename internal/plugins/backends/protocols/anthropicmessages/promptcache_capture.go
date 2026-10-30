package anthropicmessages

import (
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// renewalSnapshotFromParams builds the minimal renewal snapshot from the
// outgoing Messages API params. It keeps the typed System/Messages for
// existing tests and also captures an exact wire body in RawRequest so
// renewals reproduce the cached prefix byte-for-byte. The last block
// matches Anthropic's top-level cache_control semantics (last cacheable
// block); the raw body uses top-level cache_control which the API applies
// to the last cacheable block.
func renewalSnapshotFromParams(p anthropic.MessageNewParams, ttl string) RenewalSnapshot {
	model := string(p.Model)
	var system []RenewalSystemBlock
	for i, blk := range p.System {
		rb := RenewalSystemBlock{Type: "text", Text: blk.Text}
		if i == len(p.System)-1 && strings.TrimSpace(ttl) != "" {
			rb.CacheControl = &RenewalCacheControl{Type: "ephemeral", TTL: ttl}
		}
		system = append(system, rb)
	}
	var messages []RenewalMessage
	for _, m := range p.Messages {
		var texts []string
		for _, c := range m.Content {
			if c.OfText != nil && strings.TrimSpace(c.OfText.Text) != "" {
				texts = append(texts, c.OfText.Text)
			}
		}
		if len(texts) == 0 {
			continue
		}
		content := strings.Join(texts, "\n")
		messages = append(messages, RenewalMessage{Role: string(m.Role), Content: content})
	}
	snap := RenewalSnapshot{Model: model, System: system, Messages: messages}
	if raw, err := rawRenewalBodyFromParams(p, ttl); err == nil && len(raw) > 0 {
		snap.RawRequest = raw
	}
	return snap
}

func rawRenewalBodyFromParams(p anthropic.MessageNewParams, ttl string) (json.RawMessage, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if strings.TrimSpace(ttl) != "" {
		raw["cache_control"] = map[string]string{"type": "ephemeral", "ttl": strings.TrimSpace(ttl)}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

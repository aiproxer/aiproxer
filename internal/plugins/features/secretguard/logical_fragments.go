package secretguard

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FragmentKind identifies the canonical representation supplied to a detector.
type FragmentKind uint8

const (
	FragmentText FragmentKind = iota
	FragmentJSON
)

// FragmentKind aliases keep the representation names explicit at call sites.
const (
	FragmentKindText = FragmentText
	FragmentKindJSON = FragmentJSON
)

// LogicalFragment is one bounded canonical content unit. Raw is transient
// request content and must stay inside the feature boundary. Raw may borrow
// canonical bytes: detectors must treat it as immutable until the scan returns.
type LogicalFragment struct {
	Location string
	Kind     FragmentKind
	// Text retains an admitted text fragment in its immutable canonical form.
	// Raw is used for JSON fragments and remains available for feature-private
	// test/source construction. Exactly one representation is populated by the
	// canonical walker.
	Text string
	Raw  []byte

	// privateID distinguishes canonical fields that intentionally share a
	// public location (for example tool-result text and raw JSON content).
	// It never crosses the feature-private merger boundary.
	privateID   string
	replace     func([]byte)
	replaceText func(string)
}

func (f LogicalFragment) setRaw(raw []byte) {
	if f.replaceText != nil {
		f.replaceText(string(raw))
		return
	}
	if f.replace != nil {
		f.replace(raw)
	}
}

func (f LogicalFragment) setText(text string) {
	if f.replaceText != nil {
		f.replaceText(text)
		return
	}
	if f.replace != nil {
		f.replace([]byte(text))
	}
}

// textValue returns the immutable text representation without materializing a
// byte slice. Raw-backed fragments are retained for private test/source
// callers, so the fallback is intentionally lazy.
func (f LogicalFragment) textValue() string {
	if f.Text != "" || f.Raw == nil {
		return f.Text
	}
	return string(f.Raw)
}

// rawBytes materializes text only for byte-oriented occurrence mapping or
// mutation. JSON fragments already retain their admitted byte representation.
func (f LogicalFragment) rawBytes() []byte {
	if f.Raw != nil {
		return f.Raw
	}
	return []byte(f.Text)
}

type scanBudget struct {
	maxBytes int
	used     int
	limitHit bool
}

func newScanBudget(maxBytes int) *scanBudget {
	if maxBytes <= 0 {
		maxBytes = DefaultScanMaxBytes
	}
	return &scanBudget{maxBytes: maxBytes}
}

// admit reserves one original logical fragment before either detector sees it.
// Derived JSON scalar values never pass through this method.
func (b *scanBudget) admit(n int) bool {
	if b == nil || n <= 0 || b.limitHit {
		return false
	}
	if b.used+n > b.maxBytes {
		b.limitHit = true
		return false
	}
	b.used += n
	return true
}

// walkLogicalFragments emits whole text and raw JSON units from eligible user
// prompts and tool output in stable canonical order. Instructions, model
// tool-call arguments, and tool definitions are never admitted or emitted.
func walkLogicalFragments(call *aipapi.Call, budget *scanBudget) []LogicalFragment {
	if call == nil {
		return nil
	}
	if budget == nil {
		budget = newScanBudget(0)
	}

	fragments := make([]LogicalFragment, 0)
	appendText := func(location string, value *string) bool {
		if value == nil || len(*value) == 0 || !budget.admit(len(*value)) {
			return budget.limitHit
		}
		fragments = append(fragments, LogicalFragment{
			Location:    location,
			Kind:        FragmentText,
			Text:        *value,
			privateID:   fmt.Sprintf("fragment[%d]", len(fragments)),
			replaceText: func(text string) { *value = text },
		})
		return false
	}
	appendJSON := func(location string, value *json.RawMessage) bool {
		if value == nil || len(*value) == 0 || !budget.admit(len(*value)) {
			return budget.limitHit
		}
		fragments = append(fragments, LogicalFragment{
			Location: location,
			Kind:     FragmentJSON,
			// Scans are synchronous/read-only; redact traverses an owned Call
			// clone and replacement installs new bytes rather than editing Raw.
			Raw:       *value,
			privateID: fmt.Sprintf("fragment[%d]", len(fragments)),
			replace:   func(raw []byte) { *value = bytes.Clone(raw) },
		})
		return false
	}
	appendJSONText := func(location string, value *string) bool {
		if value == nil || len(*value) == 0 || !budget.admit(len(*value)) {
			return budget.limitHit
		}
		fragments = append(fragments, LogicalFragment{
			Location:  location,
			Kind:      FragmentJSON,
			Raw:       []byte(*value),
			privateID: fmt.Sprintf("fragment[%d]", len(fragments)),
			replace:   func(raw []byte) { *value = string(raw) },
		})
		return false
	}
	appendContentPart := func(location string, part *aipapi.ContentPart) bool {
		if part == nil {
			return false
		}
		switch part.Kind {
		case aipapi.ContentPartText, aipapi.ContentPartToolResult:
			return appendText(location, &part.Text)
		case aipapi.ContentPartJSON:
			before := len(fragments)
			limitHit := appendJSONText(location, &part.Text)
			if len(fragments) > before && part.Annotation != nil && part.Annotation.Type == "json_content" {
				// Text is authoritative; update its admitted mirror on the same
				// working clone, never unrelated provenance annotations.
				fragments[len(fragments)-1].replace = func(raw []byte) {
					part.Text = string(raw)
					part.Annotation.Data = bytes.Clone(raw)
				}
			}
			return limitHit
		default:
			return false
		}
	}
	appendMessage := func(messages []aipapi.Message, prefix string) bool {
		for i := range messages {
			if messages[i].Role != aipapi.RoleUser && messages[i].Role != aipapi.RoleTool {
				continue
			}
			for j := range messages[i].Parts {
				loc := fmt.Sprintf("%s[%d].parts[%d]", prefix, i, j)
				part := &messages[i].Parts[j]
				switch part.Kind {
				case aipapi.PartText:
					if appendText(loc, &part.Text) {
						return true
					}
				case aipapi.PartJSON:
					if appendJSON(loc, &part.Content) {
						return true
					}
				case aipapi.PartToolResult:
					if appendText(loc, &part.Text) {
						return true
					}
					if appendJSON(loc, &part.Content) {
						return true
					}
				}
			}
		}
		return false
	}

	appendItems := func(items []aipapi.Item) bool {
		for i := range items {
			item := &items[i]
			switch item.Kind {
			case aipapi.ItemKindMessage:
				if item.Role != aipapi.RoleUser && item.Role != aipapi.RoleTool {
					continue
				}
				for j := range item.Content {
					if appendContentPart(fmt.Sprintf("items[%d].content[%d]", i, j), &item.Content[j]) {
						return true
					}
				}
			case aipapi.ItemKindToolResult:
				if item.ToolResult == nil {
					continue
				}
				if appendText(fmt.Sprintf("items[%d].tool_result.output", i), &item.ToolResult.Output) {
					return true
				}
				for j := range item.ToolResult.Parts {
					if appendContentPart(fmt.Sprintf("items[%d].tool_result.parts[%d]", i, j), &item.ToolResult.Parts[j]) {
						return true
					}
				}
			}
		}
		return false
	}

	if call.HasItemAuthority() {
		if appendItems(call.Items) {
			return fragments
		}
	} else if appendMessage(call.Messages, "messages") {
		return fragments
	}
	return fragments
}

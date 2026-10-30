// Package source prepares a small, canonical and privacy-bounded source window
// for continuity extraction. It deliberately knows nothing about provider
// wire DTOs or the implementation of structured plan carriers.
package source

import (
	"context"
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const (
	EnvelopeVersion = 1

	UntrustedOpen  = "[UNTRUSTED TOOL DATA]\n"
	UntrustedClose = "\n[/UNTRUSTED TOOL DATA]"
)

// EntryKind identifies the small set of source records the extractor may see.
type EntryKind string

const (
	EntryUserDecision      EntryKind = "user_decision"
	EntryUserText          EntryKind = "user_text"
	EntryAssistantPlan     EntryKind = "assistant_plan"
	EntryStructuredCarrier EntryKind = "structured_carrier"
	EntryUntrustedTool     EntryKind = "untrusted_tool"
)

// StructuredCarrier is the narrow carrier seam consumed by Prepare. A
// carrier implementation can recognize its own canonical shape and return
// bounded JSON without this package importing that implementation.
type StructuredCarrier struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Payload string `json:"payload"`
}

// CarrierRecognizer recognizes provider-neutral structured plan carriers.
// It must return a bounded, canonical payload and must not perform I/O.
type CarrierRecognizer interface {
	Recognize(item aipapi.Item) (StructuredCarrier, bool)
}

// CarrierRecognizerFunc adapts a pure function to CarrierRecognizer.
type CarrierRecognizerFunc func(item aipapi.Item) (StructuredCarrier, bool)

func (f CarrierRecognizerFunc) Recognize(item aipapi.Item) (StructuredCarrier, bool) {
	if f == nil {
		return StructuredCarrier{}, false
	}
	return f(item)
}

// Redactor is the consumed privacy seam. Implementations own secret matching
// and return only the transformed string; no secret catalog is exposed here.
type Redactor interface {
	Redact(context.Context, string) (string, error)
}

// MatcherRedactor adapts the existing secretguard.Matcher contract to the
// source package's smaller redaction interface. Findings are intentionally
// discarded: source preparation never exposes secret metadata or excerpts.
type MatcherRedactor struct {
	Matcher secretguard.Matcher
}

func (r MatcherRedactor) Redact(ctx context.Context, in string) (string, error) {
	if r.Matcher == nil {
		return in, nil
	}
	out, _, err := r.Matcher.RedactString(ctx, in)
	return out, err
}

// Config bounds source retention. Zero values are replaced by DefaultConfig.
type Config struct {
	MaxBytes              int
	MaxEntries            int
	MaxEntryBytes         int
	MaxCarrierBytes       int
	MaxUntrustedToolBytes int
}

// DefaultConfig returns the conservative source bounds used by the feature.
func DefaultConfig() Config {
	return Config{
		MaxBytes:              16 * 1024,
		MaxEntries:            64,
		MaxEntryBytes:         4 * 1024,
		MaxCarrierBytes:       4 * 1024,
		MaxUntrustedToolBytes: 512,
	}
}

func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.MaxBytes > 0 {
		d.MaxBytes = c.MaxBytes
	}
	if c.MaxEntries > 0 {
		d.MaxEntries = c.MaxEntries
	}
	if c.MaxEntryBytes > 0 {
		d.MaxEntryBytes = c.MaxEntryBytes
	}
	if c.MaxCarrierBytes > 0 {
		d.MaxCarrierBytes = c.MaxCarrierBytes
	}
	if c.MaxUntrustedToolBytes > 0 {
		d.MaxUntrustedToolBytes = c.MaxUntrustedToolBytes
	}
	return d
}

// HighWatermark is deterministic and content-free. ItemCount advances over
// every walked canonical item, including dropped items, while Digest detects a
// changed prefix before incremental preparation is allowed to append.
type HighWatermark struct {
	ItemCount int    `json:"item_count"`
	Digest    string `json:"digest"`
}

// Entry is a bounded source record. Text and carrier payload are egress data;
// metadata fields are content-free classification only.
type Entry struct {
	Kind             EntryKind          `json:"kind"`
	ItemID           string             `json:"item_id,omitempty"`
	ItemIndex        int                `json:"item_index"`
	Role             aipapi.Role        `json:"role,omitempty"`
	Text             string             `json:"text,omitempty"`
	Carrier          *StructuredCarrier `json:"carrier,omitempty"`
	Untrusted        bool               `json:"untrusted,omitempty"`
	DecisionRelevant bool               `json:"decision_relevant,omitempty"`
	PlanningRelevant bool               `json:"planning_relevant,omitempty"`
	New              bool               `json:"new,omitempty"`
	priority         int
}

// Envelope is the bounded canonical source window. It is not a transcript and
// contains no route, branch, session, or provider-specific metadata.
type Envelope struct {
	Version       int           `json:"version"`
	HighWatermark HighWatermark `json:"high_watermark"`
	Entries       []Entry       `json:"entries,omitempty"`
	Bytes         int           `json:"-"`
}

// Canonical returns stable JSON suitable for hashing or test comparisons.
func (e Envelope) Canonical() string {
	b, _ := json.Marshal(struct {
		Version       int           `json:"version"`
		HighWatermark HighWatermark `json:"high_watermark"`
		Entries       []Entry       `json:"entries,omitempty"`
	}{e.Version, e.HighWatermark, e.Entries})
	return string(b)
}

// Input is the pure preparation input. Existing is copied and only the new
// suffix is walked when Previous proves the prefix unchanged.
type Input struct {
	Call       aipapi.Call
	Existing   Envelope
	Previous   HighWatermark
	Recognizer CarrierRecognizer
	Redactor   Redactor
	Config     Config
}

// Prepared is the bounded source result. NewEntries contains only records
// discovered after the accepted prefix and is safe for incremental scheduling.
type Prepared struct {
	Envelope      Envelope
	NewEntries    []Entry
	HighWatermark HighWatermark
	Candidate     bool
}

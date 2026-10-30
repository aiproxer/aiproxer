package reftraffictranscript

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const defaultOrder = 30

// Transcript records structured observations (redacted path) for tests and diagnostics.
type Transcript struct {
	mu   sync.Mutex
	Rows []Row
}

// Row is one observed sample after redaction.
type Row struct {
	Leg         traffic.Leg
	Body        []byte
	At          time.Time
	Trace       string
	Redact      bool
	AttemptSeq  int
	BLegID      string
	BackendID   string
	FrontendID  string
	SessionID   string
	PrincipalID string
}

// NewTranscript returns an empty transcript buffer.
func NewTranscript() *Transcript { return &Transcript{} }

func (t *Transcript) OnObservation(_ context.Context, ev traffic.Observation) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Rows = append(t.Rows, Row{
		Leg:         ev.Leg,
		Body:        append([]byte(nil), ev.Body...),
		At:          ev.RecordedAt,
		Trace:       ev.TraceID,
		Redact:      true,
		AttemptSeq:  ev.AttemptSeq,
		BLegID:      ev.BLegID,
		BackendID:   ev.BackendID,
		FrontendID:  ev.FrontendID,
		SessionID:   ev.SessionID,
		PrincipalID: ev.PrincipalID,
	})
	return nil
}

// RawLog records privileged verbatim payloads (unredacted).
type RawLog struct {
	mu   sync.Mutex
	Rows []RawRow
}

// RawRow is a raw capture line.
type RawRow struct {
	Leg   traffic.Leg
	Bytes []byte
	Meta  traffic.CaptureMeta
}

// NewRawLog returns a raw capture sink.
func NewRawLog() *RawLog { return &RawLog{} }

func (r *RawLog) WriteRaw(_ context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := meta
	r.Rows = append(r.Rows, RawRow{
		Leg:   leg,
		Bytes: append([]byte(nil), payload...),
		Meta:  m,
	})
	return nil
}

type subRedactor struct {
	id   string
	subs []string
}

// NewPatternRedactor removes configured substrings on the observation path.
func NewPatternRedactor(cfg Config) traffic.Redactor {
	return subRedactor{
		id:   ID + "-redact",
		subs: append([]string(nil), cfg.RedactSubstrings...),
	}
}

func (r subRedactor) ID() string { return r.id }

func (r subRedactor) Redact(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, body []byte) ([]byte, error) {
	out := body
	for _, s := range r.subs {
		if s == "" {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(s), []byte("[redacted]"))
	}
	return out, nil
}

// Priority for stable redactor ordering in traffic.MaterializeSortedRedactors.
func (r subRedactor) Priority() int { return defaultOrder }

// BundleForTest builds a [traffic.PortBundle] for unit tests: fresh [Transcript], [PatternRedactor], and [RawLog].
func BundleForTest(cfg Config) (traffic.PortBundle, *Transcript, *RawLog) {
	obs := NewTranscript()
	raw := NewRawLog()
	return traffic.PortBundle{
		Obs: obs,
		Red: []traffic.Redactor{NewPatternRedactor(cfg)},
		Raw: raw,
	}, obs, raw
}

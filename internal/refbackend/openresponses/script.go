package openresponses

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Mode selects the transport/output shape the server serves for the active script.
type Mode string

const (
	ModeJSON      Mode = "json"
	ModeSSE       Mode = "sse"
	ModeCompact   Mode = "compact"
	ModeWebSocket Mode = "websocket"
)

// AuthExpectation asserts the request credential posture.
type AuthExpectation string

const (
	AuthOptional AuthExpectation = "optional" // accept with or without credentials
	AuthBearer   AuthExpectation = "bearer"   // require a Bearer token
	AuthNone     AuthExpectation = "none"     // require no Authorization header
)

// MalformedMode deliberately violates the profile so the independent client's
// parser must reject the served body/stream without panicking.
type MalformedMode string

const (
	MalformedNone                   MalformedMode = ""
	MalformedResourceMissingField   MalformedMode = "resource_missing_field"
	MalformedResourceBadType        MalformedMode = "resource_bad_type"
	MalformedItemDiscriminator      MalformedMode = "item_discriminator"
	MalformedBodyNotJSON            MalformedMode = "body_not_json"
	MalformedOversizedBody          MalformedMode = "oversized_body"
	MalformedEventNoHeader          MalformedMode = "event_no_header"
	MalformedEventMismatch          MalformedMode = "event_mismatch"
	MalformedEventDuplicateTerminal MalformedMode = "event_duplicate_terminal"
	MalformedEventAfterTerminal     MalformedMode = "event_after_terminal"
	MalformedDoneBeforeTerminal     MalformedMode = "done_before_terminal"
	MalformedMissingDONE            MalformedMode = "missing_done"
	MalformedContentType            MalformedMode = "content_type"
)

// ValidMalformed reports whether m is a known malformed mode.
func ValidMalformed(m MalformedMode) bool {
	switch m {
	case MalformedNone, MalformedResourceMissingField, MalformedResourceBadType,
		MalformedItemDiscriminator, MalformedBodyNotJSON, MalformedOversizedBody,
		MalformedEventNoHeader, MalformedEventMismatch, MalformedEventDuplicateTerminal,
		MalformedEventAfterTerminal, MalformedDoneBeforeTerminal, MalformedMissingDONE,
		MalformedContentType:
		return true
	}
	return false
}

// ExpectedRequest is a strict assertion over the captured request. Every declared
// constraint must hold; a mismatch is recorded and surfaced as a 400-style error.
type ExpectedRequest struct {
	Method                string          // exact method, e.g. POST
	PathSuffix            string          // URL path suffix, e.g. /responses/compact
	ContentType           string          // request Content-Type, e.g. application/json
	Auth                  AuthExpectation // credential posture assertion
	Model                 string          // exact model when non-empty
	Stream                *bool           // exact stream flag when non-nil
	MinInputItems         int             // minimum input item count (0 = unconstrained)
	MaxInputItems         int             // maximum input item count (0 = unconstrained)
	RequireTools          int             // minimum declared tool count
	RequireExtensionItems []string        // input item types that must be present
	Contains              []string        // raw body substrings that must be present
	MustOmit              []string        // raw body substrings that must be absent
}

// ErrorStep forces a structured HTTP/WS error response.
type ErrorStep struct {
	Status     int    // HTTP status; 0 defaults to 400
	Type       string // e.g. invalid_request, requests, server_error
	Code       string // stable code, e.g. model_not_found, rate_limit_exceeded
	Message    string
	Param      string
	RetryAfter string // sent as Retry-After when Status is 429
}

// DelayPlan applies virtual/controlled delays around serving.
type DelayPlan struct {
	BeforeFirst   time.Duration // sleep before the first event/response
	BetweenEvents time.Duration // sleep between streamed events
	SlowWrite     time.Duration // additional per-event write sleep (slow-write mode)
}

// WireStep is an explicit scripted stream event (used when the stream is not
// derived from a Resource by the independent stream builder).
type WireStep struct {
	Type     string
	Sequence int64
	Data     json.RawMessage
}

// Script is the declarative contract for one server interaction.
type Script struct {
	ID              string
	Description     string
	Mode            Mode
	Expected        ExpectedRequest
	Resource        *Resource        // success body for JSON; stream source for SSE/WS
	CompactResource *CompactResource // success body for compact mode
	SSE             []WireStep       // optional explicit stream when Resource is nil
	RawBody         []byte           // optional verbatim body (e.g. pinned official fixture bytes)
	Error           *ErrorStep
	Delay           DelayPlan
	Malformed       MalformedMode
	DisconnectAfter int // close the connection after this many streamed events (0 = never)
	Status          int // success status override; 0 = 200
}

var validScriptID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate enforces script-state hygiene: every declared script must be
// well-formed, non-empty, and self-consistent.
func (s *Script) Validate() error {
	if s == nil {
		return fmt.Errorf("script cannot be nil")
	}
	if !validScriptID.MatchString(s.ID) {
		return fmt.Errorf("script id %q must be lowercase kebab-case", s.ID)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("script %q description cannot be empty", s.ID)
	}
	switch s.Mode {
	case ModeJSON, ModeSSE, ModeCompact, ModeWebSocket:
	default:
		return fmt.Errorf("script %q unknown mode %q", s.ID, s.Mode)
	}
	if s.Status < 0 || s.Status > 599 {
		return fmt.Errorf("script %q status %d out of range", s.ID, s.Status)
	}
	if s.DisconnectAfter < 0 {
		return fmt.Errorf("script %q DisconnectAfter cannot be negative", s.ID)
	}
	if s.Delay.BeforeFirst < 0 || s.Delay.BetweenEvents < 0 || s.Delay.SlowWrite < 0 {
		return fmt.Errorf("script %q delay plan cannot be negative", s.ID)
	}
	if s.Expected.MinInputItems < 0 || s.Expected.MaxInputItems < 0 || s.Expected.RequireTools < 0 {
		return fmt.Errorf("script %q expected bounds cannot be negative", s.ID)
	}
	if s.Expected.MaxInputItems > 0 && s.Expected.MinInputItems > s.Expected.MaxInputItems {
		return fmt.Errorf("script %q min input items exceeds max", s.ID)
	}
	if s.Expected.Stream != nil && s.Mode == ModeCompact {
		return fmt.Errorf("script %q compact mode cannot assert stream", s.ID)
	}
	switch s.Expected.Auth {
	case "", AuthOptional, AuthBearer, AuthNone:
	default:
		return fmt.Errorf("script %q unknown auth expectation %q", s.ID, s.Expected.Auth)
	}
	if s.Malformed != "" && !ValidMalformed(s.Malformed) {
		return fmt.Errorf("script %q unknown malformed mode %q", s.ID, s.Malformed)
	}
	if s.Error != nil {
		if s.Error.Status != 0 && (s.Error.Status < 400 || s.Error.Status > 599) {
			return fmt.Errorf("script %q error status %d must be 4xx/5xx", s.ID, s.Error.Status)
		}
		if strings.TrimSpace(s.Error.Type) == "" || strings.TrimSpace(s.Error.Code) == "" ||
			strings.TrimSpace(s.Error.Message) == "" {
			return fmt.Errorf("script %q error step requires type/code/message", s.ID)
		}
		return nil
	}
	// Success modes must be able to produce a body.
	if s.Malformed == MalformedBodyNotJSON || s.Malformed == MalformedOversizedBody {
		return nil
	}
	if len(s.RawBody) > 0 {
		return nil
	}
	needsResource := true
	switch s.Mode {
	case ModeJSON, ModeSSE, ModeWebSocket:
		needsResource = s.Resource == nil && len(s.SSE) == 0
	case ModeCompact:
		needsResource = s.CompactResource == nil
	}
	if needsResource {
		return fmt.Errorf("script %q success mode requires a resource body", s.ID)
	}
	return nil
}

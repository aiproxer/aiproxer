package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

const (
	VisibilityPrivate = "private"
	RecursionPluginID = "agent-loop-guard"

	DefaultRole    = "loop_guard"
	DefaultTimeout = 4 * time.Second

	MaxResponseBytes  = 4096
	MaxPromptBytes    = 12 * 1024
	MaxReasonBytes    = 512
	MaxObjectiveBytes = 512
	MaxRoleBytes      = 128
)

// Collector is the only auxiliary capability consumed by this verifier. It
// deliberately excludes streaming, backend, and runtime execution surfaces.
type Collector interface {
	Collect(context.Context, auxiliary.Request) (aipapi.Collected, error)
}

// Verifier is the seam consumed by the feature provider. The platform owns
// candidate admission and continuation; this package only returns a bounded
// semantic verdict.
type Verifier interface {
	Verify(context.Context, terminaldecision.Input) (Verdict, error)
}

// Config bounds the detached verifier request. Zero values select conservative
// defaults; composition owns any later feature configuration policy.
type Config struct {
	Role    string
	Timeout time.Duration
}

// Adapter runs one detached private auxiliary semantic check.
type Adapter struct {
	collector Collector
	role      string
	timeout   time.Duration
}

var _ Verifier = (*Adapter)(nil)

// New constructs a verifier adapter over the smallest auxiliary collector
// interface. A nil collector is retained and fails closed at Verify time.
func New(collector Collector, cfg Config) *Adapter {
	role := strings.TrimSpace(cfg.Role)
	if role == "" {
		role = DefaultRole
	}
	role = boundText(role, MaxRoleBytes)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Adapter{collector: collector, role: role, timeout: timeout}
}

// Verify builds one bounded detached request and conservatively maps every
// auxiliary, timeout, input, or parser failure to UNCERTAIN plus its typed
// boundary error.
func (a *Adapter) Verify(ctx context.Context, in terminaldecision.Input) (Verdict, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return uncertain(), err
	}
	if err := in.Validate(); err != nil {
		return uncertain(), err
	}
	if a == nil || a.collector == nil {
		return uncertain(), ErrCollectorUnavailable
	}

	dctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	if err := dctx.Err(); err != nil {
		return uncertain(), err
	}

	req := auxiliary.Request{
		Role:          a.role,
		Visibility:    VisibilityPrivate,
		SessionMode:   auxiliary.SessionModeDetached,
		ParentTraceID: in.Request.TraceID,
		ParentALegID:  in.Request.ALegID,
		ParentBLegID:  in.Request.BLegID,
		DisablePlugins: []string{
			RecursionPluginID,
		},
		Call: &aipapi.Call{
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(buildPrompt(in))},
			}},
		},
	}
	collected, err := a.collector.Collect(dctx, req)
	if err != nil {
		return uncertain(), err
	}
	verdict, err := Parse(collected.Text.String())
	if err != nil {
		return uncertain(), err
	}
	return verdict, nil
}

func uncertain() Verdict { return Verdict{Kind: VerdictUncertain} }

func buildPrompt(in terminaldecision.Input) string {
	var b strings.Builder
	b.Grow(4096)
	b.WriteString("You are a bounded semantic completion verifier.\n")
	b.WriteString("Decide only whether the existing requested work is complete.\n")
	b.WriteString("Return exactly one JSON object with kind COMPLETE|INCOMPLETE|UNCERTAIN.\n")
	b.WriteString("INCOMPLETE requires a concrete existing objective executable without new user input.\n")
	b.WriteString("Do not invent approval, permission, credentials, tools, scope, or user answers.\n")
	b.WriteString("Do not include chain-of-thought.\n")
	b.WriteString("Output fields: {\"kind\":\"...\",\"reason\":\"...\",\"objective\":\"...\"}.\n\n")
	b.WriteString("<evidence>\n")
	fmt.Fprintf(&b, "cause=%s output_committed=%t explicit_completion=%t\n", in.Candidate.Cause, in.Candidate.OutputCommitted, in.Evidence.ExplicitCompletion)
	fmt.Fprintf(&b, "objective=%s\n", in.Evidence.Objective)
	fmt.Fprintf(&b, "recent_text=%s\n", in.Evidence.RecentText)
	fmt.Fprintf(&b, "candidate_text=%s\n", in.Evidence.CandidateText)
	fmt.Fprintf(&b, "trajectory_ref=%s progress_ref=%s attempt=%d\n", in.Evidence.Lineage.TrajectoryRef, in.Evidence.Lineage.ProgressRef, in.Evidence.Lineage.Attempt)
	for i := 0; i < int(in.Evidence.ActionCount); i++ {
		action := in.Evidence.Actions[i]
		fmt.Fprintf(&b, "action item_id=%s call_id=%s kind=%s status=%s name=%s\n", action.ItemID, action.CallID, action.Kind, action.Status, action.Name)
	}
	b.WriteString("</evidence>\n")
	return boundText(b.String(), MaxPromptBytes)
}

// Kind is the strict semantic verifier result vocabulary.
type Kind string

const (
	VerdictComplete   Kind = "COMPLETE"
	VerdictIncomplete Kind = "INCOMPLETE"
	VerdictUncertain  Kind = "UNCERTAIN"
)

// Verdict is the bounded semantic result. Objective is meaningful only for an
// INCOMPLETE result and never carries verifier chain-of-thought.
type Verdict struct {
	Kind      Kind
	Reason    string
	Objective string
}

// ParseErrorKind identifies a strict parser rejection without response data.
type ParseErrorKind string

const (
	ParseErrorEmpty            ParseErrorKind = "empty_response"
	ParseErrorTooLarge         ParseErrorKind = "response_too_large"
	ParseErrorMalformed        ParseErrorKind = "malformed_response"
	ParseErrorMultipleValues   ParseErrorKind = "multiple_values"
	ParseErrorUnknownField     ParseErrorKind = "unknown_field"
	ParseErrorDuplicateField   ParseErrorKind = "duplicate_field"
	ParseErrorInvalidKind      ParseErrorKind = "invalid_kind"
	ParseErrorMissingObjective ParseErrorKind = "missing_objective"
)

// ParseError is safe to expose in bounded diagnostics: its message contains
// only a stable parser classification.
type ParseError struct{ Kind ParseErrorKind }

func (e *ParseError) Error() string {
	if e == nil {
		return "verifier: unknown_parse_error"
	}
	return "verifier: " + string(e.Kind)
}

// ErrCollectorUnavailable means no auxiliary collector was composed.
var ErrCollectorUnavailable = errors.New("verifier: collector unavailable")

// Parse accepts exactly one bounded JSON object with the strict verifier
// vocabulary. COMPLETE and UNCERTAIN may omit objective; INCOMPLETE may not.
func Parse(text string) (Verdict, error) {
	if len(text) > MaxResponseBytes {
		return Verdict{}, &ParseError{Kind: ParseErrorTooLarge}
	}
	if !utf8.ValidString(text) {
		return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
	}
	raw := strings.TrimSpace(text)
	if raw == "" {
		return Verdict{}, &ParseError{Kind: ParseErrorEmpty}
	}
	if raw[0] != '{' {
		return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
		}
		switch key {
		case "kind", "reason", "objective":
		default:
			return Verdict{}, &ParseError{Kind: ParseErrorUnknownField}
		}
		if _, exists := fields[key]; exists {
			return Verdict{}, &ParseError{Kind: ParseErrorDuplicateField}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
		}
		fields[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return Verdict{}, &ParseError{Kind: ParseErrorMultipleValues}
	default:
		return Verdict{}, &ParseError{Kind: ParseErrorMalformed}
	}

	kindText, err := parseStringField(fields, "kind")
	if err != nil {
		return Verdict{}, err
	}
	reasonText, err := parseOptionalStringField(fields, "reason")
	if err != nil {
		return Verdict{}, err
	}
	objectiveText, err := parseOptionalStringField(fields, "objective")
	if err != nil {
		return Verdict{}, err
	}
	kind := Kind(kindText)
	switch kind {
	case VerdictComplete, VerdictIncomplete, VerdictUncertain:
	default:
		return Verdict{}, &ParseError{Kind: ParseErrorInvalidKind}
	}
	objective := boundText(strings.TrimSpace(objectiveText), MaxObjectiveBytes)
	if kind == VerdictIncomplete && objective == "" {
		return Verdict{}, &ParseError{Kind: ParseErrorMissingObjective}
	}
	return Verdict{Kind: kind, Reason: boundText(reasonText, MaxReasonBytes), Objective: objective}, nil
}

func parseStringField(fields map[string]json.RawMessage, name string) (string, error) {
	field, ok := fields[name]
	if !ok {
		return "", &ParseError{Kind: ParseErrorMalformed}
	}
	return decodeString(field)
}

func parseOptionalStringField(fields map[string]json.RawMessage, name string) (string, error) {
	field, ok := fields[name]
	if !ok {
		return "", nil
	}
	return decodeString(field)
}

func decodeString(field json.RawMessage) (string, error) {
	field = bytes.TrimSpace(field)
	if len(field) == 0 || field[0] != '"' {
		return "", &ParseError{Kind: ParseErrorMalformed}
	}
	var value string
	if err := json.Unmarshal(field, &value); err != nil {
		return "", &ParseError{Kind: ParseErrorMalformed}
	}
	return value, nil
}

func boundText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	cut := value[:max]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

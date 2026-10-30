package agentloopguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// completionToolProviderID is the stable feature-local control-tool identity.
// It is bounded, non-configurable, and independent of the terminal provider ID.
const completionToolProviderID = "agent-loop-guard.completion"

const (
	completionCompleteReason = "completion_complete"
	completionInvalidReason  = "completion_invalid"
)

// completionToolProvider is the feature-local concrete zero-state completion
// provider. It holds no configuration, stored contexts, goroutines, or
// metadata authority; generic core owns provenance, bounded capture, and
// handler safety. This parser only validates its invocation content.
type completionToolProvider struct{}

// NewCompletionToolProvider is the stable SDK-provider construction seam for
// the explicit-completion control tool.
func NewCompletionToolProvider() controltool.Provider { return completionToolProvider{} }

func (completionToolProvider) ID() string { return completionToolProviderID }

func (completionToolProvider) Spec() controltool.Spec { return completionToolSpec() }

// Handle implements the strict completion-argument protocol. It performs no
// command execution, file or network access, ordinary tool execution,
// approval, client publication, or metadata authority. Tool name and ID values
// are opaque data and never confer ownership; invalid contract bounds fail
// closed through the existing SDK validators without echoing raw content.
func (completionToolProvider) Handle(ctx context.Context, call controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controltool.Outcome{}, err
	}
	// Model-controlled argument bytes are expected mistakes, not wiring
	// defects: oversized or non-UTF-8 args stay in the invalid channel with a
	// static reason. Identity and provenance bounds below remain typed errors.
	if len(call.ArgsJSON) > controltool.DefaultMaxArgsBytes {
		return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: completionInvalidReason}, nil
	}
	if !utf8.Valid(call.ArgsJSON) {
		return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: completionInvalidReason}, nil
	}
	if err := controltool.ValidateCompletedCall(call, controltool.DefaultMaxArgsBytes); err != nil {
		return controltool.Outcome{}, err
	}
	if err := controltool.ValidateMeta(meta); err != nil {
		return controltool.Outcome{}, err
	}
	result, ok := parseCompletionArgs(call.ArgsJSON)
	if !ok {
		return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: completionInvalidReason}, nil
	}
	out := controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: completionCompleteReason}
	if err := controltool.ValidateOutcome(out); err != nil {
		return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: completionInvalidReason}, nil
	}
	if err := ctx.Err(); err != nil {
		return controltool.Outcome{}, err
	}
	return out, nil
}

// parseCompletionArgs is the pure strict parser for the frozen completion
// contract: exactly one JSON object with exactly the key `result` mapped to a
// trimmed-non-empty bounded string. It never mutates its input, never logs,
// and returns only an owned string plus a boolean so no parser detail can
// leak into errors, reasons, or labels.
func parseCompletionArgs(raw []byte) (string, bool) {
	if len(raw) > controltool.DefaultMaxArgsBytes {
		return "", false
	}
	if !utf8.Valid(raw) {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return "", false
	}
	seen := false
	var result string
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return "", false
		}
		key, ok := ktok.(string)
		if !ok || key != "result" {
			return "", false
		}
		if seen {
			return "", false
		}
		seen = true
		var value string
		if err := dec.Decode(&value); err != nil {
			return "", false
		}
		if !utf8.ValidString(value) {
			return "", false
		}
		if strings.ContainsRune(value, 0) {
			return "", false
		}
		if strings.TrimSpace(value) == "" {
			return "", false
		}
		if len(value) > controltool.MaxResultTextBytes {
			return "", false
		}
		result = value
	}
	tok, err = dec.Token()
	if err != nil {
		return "", false
	}
	delim, ok = tok.(json.Delim)
	if !ok || delim != '}' {
		return "", false
	}
	if !seen {
		return "", false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", false
	}
	return result, true
}

package reasoninge2e

import (
	"fmt"
	"strings"
)

// FailTrace is a content-safe structural failure description for E2E drivers.
// It must never carry reasoning text, signatures, opaque payloads, or anchors.
type FailTrace struct {
	Seed   uint64
	Policy string
	Mode   string
	TurnID string
	Field  string
	Detail string
}

func (f FailTrace) String() string {
	var b strings.Builder
	b.WriteString("reasoninge2e failtrace:")
	if f.Seed != 0 || f.Policy != "" {
		fmt.Fprintf(&b, " seed=%d policy=%s", f.Seed, f.Policy)
	}
	if f.TurnID != "" {
		fmt.Fprintf(&b, " turn=%s", f.TurnID)
	}
	if f.Mode != "" {
		fmt.Fprintf(&b, " mode=%s", f.Mode)
	}
	if f.Field != "" {
		fmt.Fprintf(&b, " field=%s", f.Field)
	}
	if f.Detail != "" {
		fmt.Fprintf(&b, " detail=%s", f.Detail)
	}
	return b.String()
}

// FormatFail wraps an oracle/driver error with content-safe seed/mode/turn context.
func FormatFail(plan Plan, turnID string, mode RetentionMode, field, detail string) string {
	return FailTrace{
		Seed:   plan.Seed,
		Policy: plan.Policy.String(),
		Mode:   string(mode),
		TurnID: turnID,
		Field:  field,
		Detail: detail,
	}.String()
}

// FormatRetentionDiag extracts content-safe retention fields from an oracle error
// for matrix/soak fail lines. Returns "" when err is nil or carries no retention diagnostics.
func FormatRetentionDiag(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var b strings.Builder
	for _, key := range []string{
		"request_turn_id=",
		"history_turn_id=",
		"request_turn=",
		"history_turn=",
		"artifact_state=",
	} {
		tok, ok := retentionDiagToken(msg, key)
		if !ok {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(tok)
	}
	return b.String()
}

func retentionDiagToken(msg, key string) (string, bool) {
	start := 0
	for {
		i := strings.Index(msg[start:], key)
		if i < 0 {
			return "", false
		}
		abs := start + i
		if key == "request_turn=" && strings.HasPrefix(msg[abs:], "request_turn_id=") {
			start = abs + len("request_turn_")
			continue
		}
		if key == "history_turn=" && strings.HasPrefix(msg[abs:], "history_turn_id=") {
			start = abs + len("history_turn_")
			continue
		}
		rest := msg[abs:]
		end := len(rest)
		for j, r := range rest {
			if r == ' ' || r == '\t' {
				end = j
				break
			}
		}
		return rest[:end], true
	}
}

// CheckPrefix validates a prefix of the plan against a backend observation.
// Used by multi-turn HTTP drivers where each request carries only history to date.
func CheckPrefix(plan Plan, obs BackendRequestObservation) error {
	want := plan.Turns()
	if len(obs.AssistantTurns) > len(want) {
		return fmt.Errorf(
			"reasoninge2e oracle: seed=%d policy=%s structural mismatch: turn_count got=%d want<=%d",
			plan.Seed, plan.Policy.String(), len(obs.AssistantTurns), len(want),
		)
	}
	for i := range obs.AssistantTurns {
		if err := checkTurn(plan.Seed, want[i], obs.AssistantTurns[i]); err != nil {
			return err
		}
	}
	return nil
}

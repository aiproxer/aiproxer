package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This private contract deliberately cannot be implemented by arbitrary
// external transports. Test doubles in this package borrow the stdio state.
type sessionOptionsTransport interface {
	acpSessionOptions() *sessionOptionStore
}

func (t *stdioTransport) acpSessionOptions() *sessionOptionStore {
	return &t.sessionOptions
}

type sessionOptionStore struct {
	current atomic.Pointer[sessionOptionState]
	ended   atomic.Bool
}

func (s *sessionOptionStore) publish(previous, next *sessionOptionState) bool {
	if s.ended.Load() || !s.current.CompareAndSwap(previous, next) {
		return false
	}
	if s.ended.Load() {
		s.current.Store(nil)
		return false
	}
	return true
}

func (s *sessionOptionStore) close() {
	s.ended.Store(true)
	s.current.Store(nil)
}

type sessionOption struct {
	current, defaultValue string
	values                map[string]bool
}

// State belongs to one transport generation, not the protocol or turn handle.
// Immutable snapshots are replaced while the existing runtime claim is held.
type sessionOptionState struct {
	sessionID, model, effortID, defaultEffort string
	options                                   map[string]sessionOption
	admission                                 json.RawMessage
}

// The pool owner handles withdrawal; no second process owner is introduced.
type sessionOptionsFailure struct{ cause error }

func (e *sessionOptionsFailure) Error() string { return "acp session options: " + e.cause.Error() }
func (e *sessionOptionsFailure) Unwrap() error { return e.cause }
func isSessionOptionsFailure(err error) bool {
	var failure *sessionOptionsFailure
	return errors.As(err, &failure)
}

func failSessionOptions(c *client, err error) error {
	return withdrawSessionOptions(c, fmt.Errorf("%w: %w", aipapi.ErrCapabilityReject, err))
}

// Withdrawal owns state invalidation, not capability classification. I/O and
// RPC failures retain their causes for the provider's pre-output classifier.
func withdrawSessionOptions(c *client, err error) error {
	if transport, ok := c.t.(sessionOptionsTransport); ok {
		transport.acpSessionOptions().current.Store(nil)
	}
	return &sessionOptionsFailure{cause: err}
}

func decodeSessionOptions(raw json.RawMessage) (map[string]sessionOption, error) {
	var result struct {
		Options []struct {
			ID      string            `json:"id"`
			Type    string            `json:"type"`
			Current *string           `json:"currentValue"`
			Default *string           `json:"defaultValue"`
			Values  []json.RawMessage `json:"options"`
		} `json:"configOptions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode configOptions: %w", err)
	}
	if len(result.Options) == 0 {
		return nil, fmt.Errorf("missing configOptions")
	}
	options := make(map[string]sessionOption, len(result.Options))
	for _, wire := range result.Options {
		if wire.ID == "" || wire.Type != "select" || wire.Current == nil {
			return nil, fmt.Errorf("invalid select option %q", wire.ID)
		}
		if _, exists := options[wire.ID]; exists {
			return nil, fmt.Errorf("duplicate config option %q", wire.ID)
		}
		option := sessionOption{current: *wire.Current, defaultValue: *wire.Current, values: make(map[string]bool)}
		if wire.Default != nil {
			option.defaultValue = *wire.Default
		}
		if err := decodeOptionValues(wire.Values, option.values, true); err != nil {
			return nil, fmt.Errorf("option %q: %w", wire.ID, err)
		}
		if !option.values[option.current] || !option.values[option.defaultValue] {
			return nil, fmt.Errorf("option %q has unadvertised current/default value", wire.ID)
		}
		options[wire.ID] = option
	}
	return options, nil
}

func decodeOptionValues(entries []json.RawMessage, values map[string]bool, allowGroups bool) error {
	if len(entries) == 0 {
		return fmt.Errorf("missing option values")
	}
	for _, raw := range entries {
		var entry struct {
			Value   *string           `json:"value"`
			Group   *string           `json:"group"`
			Options []json.RawMessage `json:"options"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decode option value: %w", err)
		}
		if entry.Group != nil && entry.Value == nil && allowGroups {
			if err := decodeOptionValues(entry.Options, values, false); err != nil {
				return err
			}
			continue
		}
		if entry.Value == nil || *entry.Value == "" || entry.Group != nil || len(entry.Options) != 0 || values[*entry.Value] {
			return fmt.Errorf("invalid or duplicate option value")
		}
		values[*entry.Value] = true
	}
	return nil
}

func validateRequiredOptions(options map[string]sessionOption, effortID string) error {
	for _, id := range []string{"model", effortID} {
		if _, ok := options[id]; !ok {
			return fmt.Errorf("missing required option %q", id)
		}
	}
	return nil
}

func exchangeSessionOption(ctx context.Context, c *client, state *sessionOptionState, id, value string) error {
	option, ok := state.options[id]
	if !ok || !option.values[value] {
		return failSessionOptions(c, fmt.Errorf("option %q does not advertise requested value", id))
	}
	raw, err := c.setSessionConfigOptionResult(ctx, state.sessionID, id, value)
	if err != nil {
		return withdrawSessionOptions(c, err)
	}
	options, err := decodeSessionOptions(raw)
	if err != nil {
		return failSessionOptions(c, err)
	}
	if err := validateRequiredOptions(options, state.effortID); err != nil {
		return failSessionOptions(c, err)
	}
	selected, ok := options[id]
	if !ok || selected.current != value || options["model"].current != state.model {
		return failSessionOptions(c, fmt.Errorf("option %q was silently substituted or model changed", id))
	}
	state.options = options
	return nil
}

func handshakeSessionOptions(ctx context.Context, c *client, hp HandshakeProfile, spec SessionOptionsSpec, model string) (string, error) {
	return handshakeSessionOptionsWithProfile(ctx, c, hp, spec, model)
}

// completeSessionOptions issues session/new and publishes the negotiated
// model/effort state. It is the shared tail of initial handshakes and
// instruction-driven session rotations; both callers own transport lifecycle.
func completeSessionOptions(ctx context.Context, c *client, transport sessionOptionsTransport, hp HandshakeProfile, spec SessionOptionsSpec, model string) (string, error) {
	effortID := spec.EffortConfigID()
	initial := spec.InitialSessionOptions(model)
	if effortID == "" || effortID == "model" || model == "" || initial["model"] != model {
		return "", failSessionOptions(c, fmt.Errorf("invalid initial model/effort option specification"))
	}
	sid, raw, err := c.sessionNewResult(ctx, hp)
	if err != nil {
		return "", err
	}
	options, err := decodeSessionOptions(raw)
	if err != nil {
		return "", failSessionOptions(c, err)
	}
	if err := validateRequiredOptions(options, effortID); err != nil {
		return "", failSessionOptions(c, err)
	}
	state := &sessionOptionState{sessionID: sid, model: model, effortID: effortID, options: options}
	if err := exchangeSessionOption(ctx, c, state, "model", model); err != nil {
		return "", err
	}
	state.defaultEffort = state.options[effortID].defaultValue
	ids := make([]string, 0, len(initial))
	for id := range initial {
		if id != "model" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := exchangeSessionOption(ctx, c, state, id, initial[id]); err != nil {
			return "", err
		}
	}
	if !transport.acpSessionOptions().publish(nil, state) {
		return "", withdrawSessionOptions(c, fmt.Errorf("transport ended during handshake: %w", io.ErrClosedPipe))
	}
	return sid, nil
}

func applySessionEffort(ctx context.Context, c *client, sid, model, effortID, effort string) error {
	transport, ok := c.t.(sessionOptionsTransport)
	if !ok {
		return failSessionOptions(c, fmt.Errorf("transport does not support private session option state"))
	}
	state := transport.acpSessionOptions().current.Load()
	if state == nil || state.sessionID != sid || state.model != model || state.effortID != effortID {
		return failSessionOptions(c, fmt.Errorf("missing or mismatched negotiated session state"))
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = state.defaultEffort
	}
	// Never mutate a published snapshot, including during concurrent Close.
	next := *state
	if err := exchangeSessionOption(ctx, c, &next, effortID, effort); err != nil {
		return err
	}
	// Close can withdraw state while the exchange is in flight. Do not revive it.
	if !transport.acpSessionOptions().publish(state, &next) {
		return withdrawSessionOptions(c, fmt.Errorf("session option state withdrawn during exchange: %w", io.ErrClosedPipe))
	}
	return nil
}

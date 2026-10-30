package progress

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	stateTokenPrefix              = "alg-state-v1."
	stateTokenVersion        byte = 1
	MaxStateTokenBytes            = 256
	MaxStateFingerprintBytes      = 96
	maxStateCounter               = int(^uint32(0))
)

var ErrInvalidStateToken = errors.New("agent-loop-guard progress: invalid state token")

// EncodeState serializes only bounded progress counters and the canonical
// fingerprint into an opaque continuation control reference.
func EncodeState(state State) (string, error) {
	if state.TotalAttempts < 0 || state.TotalAttempts > maxStateCounter || state.ConsecutiveNoProgress < 0 || state.ConsecutiveNoProgress > maxStateCounter {
		return "", ErrInvalidStateToken
	}
	if !utf8.ValidString(state.LastFingerprint) || len(state.LastFingerprint) > MaxStateFingerprintBytes {
		return "", ErrInvalidStateToken
	}
	flags := byte(0)
	if state.HasBaseline {
		flags |= 1 << 0
	}
	if state.NoProgressTripped {
		flags |= 1 << 1
	}
	if state.BudgetExhausted {
		flags |= 1 << 2
	}
	if state.Terminal {
		flags |= 1 << 3
	}
	payload := make([]byte, 1+1+4+4+1+len(state.LastFingerprint))
	payload[0] = stateTokenVersion
	payload[1] = flags
	binary.BigEndian.PutUint32(payload[2:6], uint32(state.TotalAttempts))
	binary.BigEndian.PutUint32(payload[6:10], uint32(state.ConsecutiveNoProgress))
	payload[10] = byte(len(state.LastFingerprint))
	copy(payload[11:], state.LastFingerprint)
	token := stateTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
	if len(token) > MaxStateTokenBytes {
		return "", ErrInvalidStateToken
	}
	return token, nil
}

// DecodeState validates and decodes one opaque progress control reference.
func DecodeState(token string) (State, error) {
	if len(token) == 0 || len(token) > MaxStateTokenBytes || !strings.HasPrefix(token, stateTokenPrefix) {
		return State{}, ErrInvalidStateToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, stateTokenPrefix))
	if err != nil || len(raw) < 11 || raw[0] != stateTokenVersion {
		return State{}, ErrInvalidStateToken
	}
	fingerprintLen := int(raw[10])
	if fingerprintLen > MaxStateFingerprintBytes || len(raw) != 11+fingerprintLen {
		return State{}, ErrInvalidStateToken
	}
	fingerprint := string(raw[11:])
	if !utf8.ValidString(fingerprint) {
		return State{}, ErrInvalidStateToken
	}
	return State{
		LastFingerprint:       fingerprint,
		HasBaseline:           raw[1]&(1<<0) != 0,
		TotalAttempts:         int(binary.BigEndian.Uint32(raw[2:6])),
		ConsecutiveNoProgress: int(binary.BigEndian.Uint32(raw[6:10])),
		NoProgressTripped:     raw[1]&(1<<1) != 0,
		BudgetExhausted:       raw[1]&(1<<2) != 0,
		Terminal:              raw[1]&(1<<3) != 0,
	}, nil
}

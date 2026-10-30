package protocolstate

import (
	"encoding/base64"
	"strings"
)

// TokenPrefix is the independent preferred-strategy state namespace. It is
// deliberately distinct from the legacy `alg-state-v1` progress token and is
// never translated to or from it.
const (
	TokenPrefix = "alg-proto-v1."
	// MaxTokenBytes keeps the whole token within the SDK opaque-identifier
	// bound of 256 bytes.
	MaxTokenBytes = 256

	// tokenVersion is the only accepted payload version.
	tokenVersion = 1
	// terminalFlag is the only defined payload flag bit.
	terminalFlag = 0x01

	// payloadHeaderBytes is version, flags, reprompts, consecutive, and the
	// fingerprint length.
	payloadHeaderBytes = 5
	// maxFingerprintBytes is the canonical digest length.
	maxFingerprintBytes = len(FingerprintPrefix) + FingerprintHexDigits
	// maxPayloadBytes bounds the decoded payload.
	maxPayloadBytes = payloadHeaderBytes + maxFingerprintBytes
)

// Encode serializes only bounded counters and the canonical fingerprint into
// an independent opaque protocol token. Only valid State values are encodable;
// an invalid value never produces a token and never consumes a reprompt slot.
func Encode(state State) (string, error) {
	if err := state.Validate(); err != nil {
		return "", err
	}

	flags := byte(0)
	if state.Terminal {
		flags |= terminalFlag
	}
	payload := make([]byte, payloadHeaderBytes, payloadHeaderBytes+len(state.LastFingerprint))
	payload[0] = tokenVersion
	payload[1] = flags
	payload[2] = byte(state.Reprompts)
	payload[3] = byte(state.ConsecutiveNoProgress)
	payload[4] = byte(len(state.LastFingerprint))
	payload = append(payload, state.LastFingerprint...)

	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
	if len(token) > MaxTokenBytes {
		return "", ErrInvalidToken
	}
	return token, nil
}

// Decode validates and decodes one opaque protocol token.
//
// Decoding is structural corruption detection, not authentication: it
// rejects unknown versions and flags, malformed or noncanonical base64url,
// oversize or truncated payloads, trailing payload, out-of-range counters,
// malformed fingerprints, and internally inconsistent combinations.
//
// Validation uses the absolute V1 bound of MaxReprompts and is independent of
// any lower currently configured cap, so a token carrying Reprompts two stays
// valid under a configured cap of one; stopping in that case is eligibility's
// job, not a decode failure. Malformed tokens are never reset to zero state.
func Decode(token string) (State, error) {
	if len(token) <= len(TokenPrefix) || len(token) > MaxTokenBytes {
		return State{}, ErrInvalidToken
	}
	payload, found := strings.CutPrefix(token, TokenPrefix)
	if !found {
		return State{}, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || len(raw) < payloadHeaderBytes || len(raw) > maxPayloadBytes {
		return State{}, ErrInvalidToken
	}
	// base64 decoding silently ignores embedded CR/LF, so a token carrying
	// whitespace would decode to valid bytes while not being the canonical
	// encoding this package emits. Reject any noncanonical spelling.
	if base64.RawURLEncoding.EncodeToString(raw) != payload {
		return State{}, ErrInvalidToken
	}
	if raw[0] != tokenVersion || raw[1]&^terminalFlag != 0 {
		return State{}, ErrInvalidToken
	}

	fingerprintLen := int(raw[4])
	if fingerprintLen > maxFingerprintBytes || len(raw) != payloadHeaderBytes+fingerprintLen {
		return State{}, ErrInvalidToken
	}
	// Copy into freshly owned bytes; the decoder never retains the caller's
	// token storage and exposes no mutable shared global.
	fingerprint := string(raw[payloadHeaderBytes : payloadHeaderBytes+fingerprintLen])

	state := State{
		Reprompts:             int(raw[2]),
		LastFingerprint:       fingerprint,
		ConsecutiveNoProgress: int(raw[3]),
		Terminal:              raw[1]&terminalFlag != 0,
	}
	if err := state.Validate(); err != nil {
		return State{}, ErrInvalidToken
	}
	return state, nil
}

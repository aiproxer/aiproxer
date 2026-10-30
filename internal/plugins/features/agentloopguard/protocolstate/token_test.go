package protocolstate

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testRaw(t *testing.T, token string) []byte {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, TokenPrefix))
	if err != nil {
		t.Fatalf("decode test token: %v", err)
	}
	return raw
}

func testToken(t *testing.T, raw []byte) string {
	t.Helper()

	return TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	for name, want := range map[string]State{
		"zero":                {},
		"terminal_zero":       {Terminal: true},
		"baseline_only":       {LastFingerprint: fp},
		"reprompted":          {Reprompts: 1, LastFingerprint: fp},
		"no_progress":         {Reprompts: 1, LastFingerprint: fp, ConsecutiveNoProgress: 3},
		"max_counters":        {Reprompts: MaxReprompts, LastFingerprint: fp, ConsecutiveNoProgress: MaxConsecutiveNoProgress},
		"terminal_with_state": {Reprompts: 2, LastFingerprint: fp, ConsecutiveNoProgress: 5, Terminal: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			token, err := Encode(want)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !strings.HasPrefix(token, TokenPrefix) {
				t.Fatalf("token %q lacks the independent protocol prefix", token)
			}
			if strings.HasPrefix(token, "alg-state-v1.") {
				t.Fatalf("token %q reuses the legacy state namespace", token)
			}
			if len(token) > MaxTokenBytes {
				t.Fatalf("token length %d exceeds %d", len(token), MaxTokenBytes)
			}
			if strings.Contains(token, fp) {
				t.Fatalf("token %q is not opaque", token)
			}
			got, err := Decode(token)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got != want {
				t.Fatalf("decoded state=%+v, want %+v", got, want)
			}
		})
	}
}

func TestDecodeAcceptsRepromptsAboveLaterConfiguredCap(t *testing.T) {
	t.Parallel()

	token, err := Encode(State{Reprompts: MaxReprompts, LastFingerprint: Fingerprint(fingerprintInput())})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(token)
	if err != nil {
		t.Fatalf("Decode must validate the absolute V1 bound only: %v", err)
	}
	if got.Reprompts != MaxReprompts {
		t.Fatalf("decoded reprompts=%d, want %d", got.Reprompts, MaxReprompts)
	}
}

func TestEncodeRejectsInvalidState(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	for name, state := range map[string]State{
		"negative_reprompts":        {Reprompts: -1, LastFingerprint: fp},
		"reprompts_over_cap":        {Reprompts: MaxReprompts + 1, LastFingerprint: fp},
		"negative_consecutive":      {ConsecutiveNoProgress: -1, LastFingerprint: fp},
		"consecutive_over_bound":    {ConsecutiveNoProgress: MaxConsecutiveNoProgress + 1, LastFingerprint: fp},
		"no_progress_no_baseline":   {ConsecutiveNoProgress: 1},
		"reprompt_without_baseline": {Reprompts: 1},
		"malformed_fingerprint":     {LastFingerprint: "sha256:zz"},
		"short_fingerprint":         {LastFingerprint: "sha256:abcd"},
		"uppercase_fingerprint":     {LastFingerprint: "sha256:" + strings.ToUpper(strings.TrimPrefix(fp, "sha256:"))},
		"unprefixed_fingerprint":    {LastFingerprint: strings.TrimPrefix(fp, "sha256:")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			token, err := Encode(state)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Encode(%+v) err=%v, want ErrInvalidState", state, err)
			}
			if token != "" {
				t.Fatalf("Encode returned token %q with an error", token)
			}
		})
	}
}

func TestEncodeErrorsCarryNoStateContent(t *testing.T) {
	t.Parallel()

	_, err := Encode(State{LastFingerprint: "sha256:secret-evidence"})
	if err == nil {
		t.Fatal("Encode accepted a malformed fingerprint")
	}
	if strings.Contains(err.Error(), "secret-evidence") {
		t.Fatalf("error text leaked state content: %v", err)
	}
}

func TestDecodeRejectsMalformedTokens(t *testing.T) {
	t.Parallel()

	valid, err := Encode(State{Reprompts: 1, LastFingerprint: Fingerprint(fingerprintInput()), ConsecutiveNoProgress: 2})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw := testRaw(t, valid)
	payload := strings.TrimPrefix(valid, TokenPrefix)
	mutate := func(index int, fn func(byte) byte) string {
		clone := append([]byte(nil), raw...)
		clone[index] = fn(clone[index])
		return testToken(t, clone)
	}
	withoutBaseline := append([]byte(nil), raw...)
	withoutBaseline[2] = 2
	withoutBaseline[4] = 0
	withoutBaseline = withoutBaseline[:payloadHeaderBytes]

	cases := map[string]string{
		"empty":                 "",
		"prefix_only":           TokenPrefix,
		"legacy_prefix":         "alg-state-v1." + strings.TrimPrefix(valid, TokenPrefix),
		"other_prefix":          "other-v1." + strings.TrimPrefix(valid, TokenPrefix),
		"future_version":        mutate(0, func(b byte) byte { return b + 1 }),
		"zero_version":          mutate(0, func(byte) byte { return 0 }),
		"unknown_flags":         mutate(1, func(b byte) byte { return b | 0x80 }),
		"reprompts_over_cap":    mutate(2, func(byte) byte { return MaxReprompts + 1 }),
		"consecutive_over":      mutate(3, func(byte) byte { return MaxConsecutiveNoProgress + 1 }),
		"reprompts_no_baseline": testToken(t, withoutBaseline),
		"fingerprint_len_over":  mutate(4, func(b byte) byte { return b + 1 }),
		"trailing_payload":      valid + "AA",
		"padded_base64":         valid + "=",
		"noncanonical_base64":   mutate(0, func(byte) byte { return 2 }),
		"malformed_base64":      TokenPrefix + "!!!!",
		"embedded_newline":      TokenPrefix + payload[:8] + "\n" + payload[8:],
		"embedded_carriage":     TokenPrefix + payload[:8] + "\r" + payload[8:],
		"embedded_space":        TokenPrefix + payload[:8] + " " + payload[8:],
		"truncated_payload":     TokenPrefix + strings.TrimPrefix(valid, TokenPrefix)[:payloadHeaderBytes-1],
		"oversize":              TokenPrefix + strings.Repeat("A", MaxTokenBytes),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			state, err := Decode(token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Decode(%s) err=%v, want ErrInvalidToken", name, err)
			}
			if state != (State{}) {
				t.Fatalf("Decode(%s) returned state %+v with an error", name, state)
			}
			if err != nil && token != "" && strings.Contains(err.Error(), token) {
				t.Fatalf("error text leaked token: %v", err)
			}
		})
	}
}

func TestDecodeReturnsIndependentValue(t *testing.T) {
	t.Parallel()

	fp := Fingerprint(fingerprintInput())
	first, err := Encode(State{Reprompts: 1, LastFingerprint: fp})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	second, err := Encode(State{Reprompts: 2, LastFingerprint: fp, Terminal: true})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	firstState, err := Decode(first)
	if err != nil {
		t.Fatalf("Decode(first): %v", err)
	}
	if _, err := Decode(second); err != nil {
		t.Fatalf("Decode(second): %v", err)
	}
	again, err := Decode(first)
	if err != nil {
		t.Fatalf("Decode(first) again: %v", err)
	}
	if firstState != again || again.Reprompts != 1 || again.Terminal {
		t.Fatalf("decode is not an independent value: %+v then %+v", firstState, again)
	}
}

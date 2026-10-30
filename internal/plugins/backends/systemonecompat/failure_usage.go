package systemonecompat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type failureUsageError struct {
	cause error
	usage aipapi.Event
}

func (e *failureUsageError) Error() string { return e.cause.Error() }

func (e *failureUsageError) Unwrap() error { return e.cause }

func withFailureUsage(body []byte, cause error) error {
	var envelope struct {
		Usage wireUsage `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return cause
	}
	u := envelope.Usage
	if u.Input == nil && u.Output == nil && u.Cost == nil {
		return cause
	}
	usage, err := u.event()
	if err != nil {
		return cause // Invalid accounting evidence is never repaired or invented.
	}
	usage.Accounting = aipapi.UsageAccountingMetadata{
		Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
		Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "systemone:failed-attempt-usage",
	}
	return &failureUsageError{cause: cause, usage: usage}
}

// failedAttemptDedupeKey assigns a distinct nonsecret source identity to each
// billable upstream attempt so the core billing ledger admits every rotated
// attempt instead of deduplicating them as one. The credential ID (c0, c1,
// ... or slot name) is nonsecret by pool contract and distinct per attempt
// within one rotation; an empty ID falls back to a fixed direct slot. The
// result stays well under the core 4096-byte dedupe-key bound.
func failedAttemptDedupeKey(credentialID string) string {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		credentialID = "direct"
	}
	// Credential IDs are pool-local opaque identities (cN or slot names);
	// they never carry secret material. Bound defensively anyway.
	if len(credentialID) > 128 {
		credentialID = credentialID[:128]
	}
	return "systemone:failed-attempt-usage:" + credentialID
}

// failureUsageStream delivers only the pre-output failure. The existing host-only
// sideband retains provider evidence even though no answer/event is released.
type failureUsageStream struct {
	*aipapi.FixedEventStream
	failure  error
	mu       sync.Mutex
	evidence []aipapi.Event
}

func (s *failureUsageStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	return aipapi.Event{}, s.failure
}

func (s *failureUsageStream) DrainUsageEvidence() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evidence := s.evidence
	s.evidence = nil
	return evidence
}

// rotatedSuccessStream delivers a successful decision stream while retaining
// prior failed-attempt billable evidence in the host-only sideband. Without
// it, a rotation that succeeds on the second key would silently drop the
// first key's billable usage. The success events flow via Recv; the failed
// attempts drain via DrainUsageEvidence with distinct source identities.
type rotatedSuccessStream struct {
	*aipapi.FixedEventStream
	mu       sync.Mutex
	evidence []aipapi.Event
}

func (s *rotatedSuccessStream) DrainUsageEvidence() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evidence := s.evidence
	s.evidence = nil
	return evidence
}

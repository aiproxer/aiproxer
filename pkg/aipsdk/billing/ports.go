package billing

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxBindingIDLength bounds the stable external binding identifier.
	MaxBindingIDLength = 128
	// MaxIdentityBytes bounds account, store, envelope, and resource IDs.
	MaxIdentityBytes = 512
	// MaxReasonBytes bounds human-readable decision reasons.
	MaxReasonBytes = 512
)

// validateRef requires a trimmed, valid-UTF-8 identity within max bytes.
func validateRef(field, value string, max int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("billing: %s contains invalid UTF-8", field)
	}
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("billing: %s is required", field)
	}
	if len(value) > max {
		return fmt.Errorf("billing: %s exceeds %d bytes", field, max)
	}
	return nil
}

// validateOptionalRef permits empty and otherwise applies validateRef.
func validateOptionalRef(field, value string, max int) error {
	if value == "" {
		return nil
	}
	return validateRef(field, value, max)
}

// validatePayloadHash requires a lowercase SHA-256 hex digest.
func validatePayloadHash(field, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("billing: %s must be a lowercase SHA-256 hex digest", field)
	}
	if strings.ToLower(value) != value {
		return fmt.Errorf("billing: %s must use lowercase hexadecimal", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("billing: %s must be hexadecimal: %v", field, err)
	}
	return nil
}

// validateSnapshotRef requires an immutable snapshot identity.
func validateSnapshotRef(kind, id, version string) error {
	if err := validateRef(kind+" snapshot id", id, MaxIdentityBytes); err != nil {
		return err
	}
	return validateRef(kind+" snapshot version", version, MaxIdentityBytes)
}

// CreditScreener is the cheap pre-route credit gate. It performs a bounded
// account read and never posts financial state.
type CreditScreener interface {
	Check(ctx context.Context, in CreditScreenInput) (CreditScreenResult, error)
}

// ExposureAdmitter is the atomic operational exposure admission authority. It
// consumes one frozen quote and finite execution limits and returns an
// admitted handle or a typed rejection.
type ExposureAdmitter interface {
	Admit(ctx context.Context, in ExposureAdmissionInput) (ExposureHandle, error)
}

// TerminalSink is the terminal evidence handoff. It durably persists the
// envelope and returns an idempotent acknowledgement; it is never a
// best-effort observer.
type TerminalSink interface {
	AppendTerminal(ctx context.Context, envelope TerminalEnvelope) (TerminalAck, error)
}

// RecordCarrier is the optional terminal-sink declaration for bindings that
// persist the exact usage-evidence records carried by version-2 envelopes. A
// sink without the declaration keeps receiving version-1 envelopes that are
// byte-for-byte identical to today's output.
type RecordCarrier interface {
	CarriesUsageRecords()
}

// DurableTerminalHandoff is the optional capability of a terminal sink that
// holds the obligation durably before it acknowledges the terminal append.
//
// A binding that does not implement it on its terminal port has no durable
// handoff: a completed obligation would die with the process. The binding
// validation does not require the capability (a legacy binding stays
// valid), but a host must not claim a durable handoff without it, and a
// customer-funded operation must not run on a host that lacks it.
//
// The readiness is a live fact: a bounded durable holder can be full or
// closed, and it must then refuse new payable work instead of accepting an
// obligation it cannot hold.
type DurableTerminalHandoff interface {
	// TerminalHandoffReady reports whether the handoff can durably accept a
	// new obligation right now. The reason is a bounded, content-free label
	// for the refusal; it never names an account, a customer or a request.
	TerminalHandoffReady() (ready bool, reason string)
}

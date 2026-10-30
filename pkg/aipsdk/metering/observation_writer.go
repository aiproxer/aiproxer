package metering

import (
	"errors"
	"fmt"
)

// ErrObservationWriterConflict identifies a rejected observation-writer
// declaration: one process was asked to run two observation writers, or an
// explicitly declared external pair was incomplete. The class is typed so a
// caller can distinguish a declaration conflict from a downstream storage
// failure without parsing an error string.
var ErrObservationWriterConflict = errors.New("metering: observation writer conflict")

// ObservationWriterSelection is the neutral declaration of the process's single
// observation writer.
//
// The enum names no concrete writer implementation, no storage and no
// callback: it only states which writer the process is allowed to run, so an
// explicitly borrowed external pair and the configuration-backed legacy
// writer can never both be active in one process.
type ObservationWriterSelection int

const (
	// ObservationWriterUnspecified is the declaration of a caller that did not
	// select a writer. It stays valid only for callers that carry no explicit
	// recorder/sink pair, and it preserves their existing legacy behavior.
	ObservationWriterUnspecified ObservationWriterSelection = iota
	// ObservationWriterExternal selects the explicitly borrowed external
	// recorder/sink pair. The pair is the sole observation writer of the
	// process; no legacy observation writer is constructed or started.
	ObservationWriterExternal
	// ObservationWriterLegacy selects the configuration-backed legacy
	// observation writer and its relay.
	ObservationWriterLegacy
	// ObservationWriterNone declares that the process runs no observation
	// writer at all. It is the neutral stock posture.
	ObservationWriterNone
)

// String returns the stable neutral name of the selection. It is used in
// diagnostics and never in control flow.
func (s ObservationWriterSelection) String() string {
	switch s {
	case ObservationWriterUnspecified:
		return "unspecified"
	case ObservationWriterExternal:
		return "external"
	case ObservationWriterLegacy:
		return "legacy"
	case ObservationWriterNone:
		return "none"
	default:
		return fmt.Sprintf("observation-writer(%d)", int(s))
	}
}

// ObservationJournalID is the opaque identity of one durable observation
// journal.
//
// The identity is minted when the journal opens. It is never derived from the
// durable store identity, so two distinct journals over the same store never
// share an identity, and a wrapper around one journal always reports that
// journal's identity unchanged.
type ObservationJournalID string

// ObservationJournalIdentity is the optional capability an observation writer
// exposes to declare which durable journal it writes to.
//
// A recorder and a sink that belong to the same journal both report the same
// identity. A pair that reports different identities is a mismatched pair, and
// the runtime refuses it before it acquires or opens anything. A port that does
// not implement this capability cannot prove its journal, so it is not
// accepted as half of an explicit external observation-writer pair.
type ObservationJournalIdentity interface {
	// ObservationJournalIdentity reports the identity of the durable journal
	// the writer writes to.
	ObservationJournalIdentity() ObservationJournalID
}

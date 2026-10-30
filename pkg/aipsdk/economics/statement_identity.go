package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ErrInvalidStatementIdentity identifies an incomplete or unsafe statement or
// statement-line identity. Statement revisions are immutable, so accepting an
// identity that cannot be reconstructed would make replay/conflict decisions
// ambiguous.
var ErrInvalidStatementIdentity = errors.New("economics: invalid statement identity")

// StatementIdentity is the immutable identity of one normalized statement
// revision: trusted store, provider account, statement identifier, billing
// period and revision. It intentionally contains no raw statement payload and
// no request/B-leg lineage.
type StatementIdentity struct {
	StoreID            string `json:"store_id"`
	ProviderAccountKey string `json:"provider_account_key"`
	StatementID        string `json:"statement_id"`
	PeriodID           string `json:"period_id"`
	Revision           uint64 `json:"revision"`
}

// Validate checks the complete bounded identity without mutating the value.
func (i StatementIdentity) Validate() error {
	for name, value := range map[string]string{
		"statement store_id": i.StoreID, "statement provider_account_key": i.ProviderAccountKey,
		"statement statement_id": i.StatementID, "statement period_id": i.PeriodID,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStatementIdentity, err)
		}
	}
	if i.Revision == 0 {
		return fmt.Errorf("%w: statement revision required", ErrInvalidStatementIdentity)
	}
	return nil
}

// Key returns a bounded opaque identity suitable for durable statement keys.
// Every identity member participates in the SHA-256 preimage.
func (i StatementIdentity) Key() string {
	if err := i.Validate(); err != nil {
		return ""
	}
	encoded, err := json.Marshal(statementIdentityPreimage(i))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "statement:v1:" + hex.EncodeToString(sum[:])
}

// Equal reports whether two identities describe the same statement revision.
func (i StatementIdentity) Equal(other StatementIdentity) bool { return i == other }

type statementIdentityPreimage struct {
	StoreID            string `json:"store_id"`
	ProviderAccountKey string `json:"provider_account_key"`
	StatementID        string `json:"statement_id"`
	PeriodID           string `json:"period_id"`
	Revision           uint64 `json:"revision"`
}

// StatementLineIdentity is the immutable identity of one statement-line
// revision. It is deliberately scoped to the statement fields without the
// statement envelope revision: a line claim is versioned by its own revision,
// so a later statement revision that restates the same line revision with
// changed content is a conflict rather than a new identity.
type StatementLineIdentity struct {
	StoreID            string `json:"store_id"`
	ProviderAccountKey string `json:"provider_account_key"`
	StatementID        string `json:"statement_id"`
	PeriodID           string `json:"period_id"`
	LineID             string `json:"line_id"`
	Revision           uint64 `json:"revision"`
}

// Validate checks the complete bounded line identity without mutating the value.
func (i StatementLineIdentity) Validate() error {
	for name, value := range map[string]string{
		"statement line store_id": i.StoreID, "statement line provider_account_key": i.ProviderAccountKey,
		"statement line statement_id": i.StatementID, "statement line period_id": i.PeriodID,
		"statement line line_id": i.LineID,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStatementIdentity, err)
		}
	}
	if i.Revision == 0 {
		return fmt.Errorf("%w: statement line revision required", ErrInvalidStatementIdentity)
	}
	return nil
}

// Key returns a bounded opaque identity suitable for durable line keys.
func (i StatementLineIdentity) Key() string {
	if err := i.Validate(); err != nil {
		return ""
	}
	encoded, err := json.Marshal(statementLineIdentityPreimage(i))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "statement-line:v1:" + hex.EncodeToString(sum[:])
}

// Equal reports whether two identities describe the same line revision.
func (i StatementLineIdentity) Equal(other StatementLineIdentity) bool { return i == other }

type statementLineIdentityPreimage struct {
	StoreID            string `json:"store_id"`
	ProviderAccountKey string `json:"provider_account_key"`
	StatementID        string `json:"statement_id"`
	PeriodID           string `json:"period_id"`
	LineID             string `json:"line_id"`
	Revision           uint64 `json:"revision"`
}

// Identity returns the canonical statement revision identity of this batch.
func (b StatementBatch) Identity() (StatementIdentity, error) {
	if err := b.Validate(); err != nil {
		return StatementIdentity{}, err
	}
	identity := StatementIdentity{
		StoreID:            b.Subject.StoreID,
		ProviderAccountKey: b.ProviderAccountKey,
		StatementID:        b.StatementID,
		PeriodID:           b.PeriodID,
		Revision:           b.Revision,
	}
	if err := identity.Validate(); err != nil {
		return StatementIdentity{}, err
	}
	return identity, nil
}

// Identity returns this line's canonical identity inside the supplied
// statement. The line subject must belong to that statement; the statement
// envelope revision is intentionally not part of the line identity.
func (l StatementLine) Identity(statement StatementIdentity) (StatementLineIdentity, error) {
	if err := statement.Validate(); err != nil {
		return StatementLineIdentity{}, err
	}
	if err := l.Subject.Validate(); err != nil {
		return StatementLineIdentity{}, fmt.Errorf("%w: line subject: %v", ErrInvalidStatementIdentity, err)
	}
	for name, pair := range map[string][2]string{
		"store":            {l.Subject.StoreID, statement.StoreID},
		"provider account": {l.Subject.ProviderAccountKey, statement.ProviderAccountKey},
		"statement":        {l.Subject.StatementID, statement.StatementID},
		"period":           {l.Subject.PeriodID, statement.PeriodID},
		"subject line":     {l.Subject.StatementLineID, l.ID},
	} {
		if pair[0] != pair[1] {
			return StatementLineIdentity{}, fmt.Errorf("%w: line subject %s does not match the statement", ErrInvalidStatementIdentity, name)
		}
	}
	identity := StatementLineIdentity{
		StoreID:            statement.StoreID,
		ProviderAccountKey: statement.ProviderAccountKey,
		StatementID:        statement.StatementID,
		PeriodID:           statement.PeriodID,
		LineID:             l.ID,
		Revision:           l.Revision,
	}
	if err := identity.Validate(); err != nil {
		return StatementLineIdentity{}, err
	}
	return identity, nil
}

// Canonical returns a normalized deep copy with observations and lines in
// deterministic order. The receiver is not mutated; semantically unordered
// sets are the only members reordered.
func (b StatementBatch) Canonical() (StatementBatch, error) {
	if err := b.Validate(); err != nil {
		return StatementBatch{}, err
	}
	out := b
	out.Observations = make([]metering.Observation, len(b.Observations))
	for i, observation := range b.Observations {
		canonical, err := observation.Canonical()
		if err != nil {
			return StatementBatch{}, fmt.Errorf("economics: statement observation %d: %w", i, err)
		}
		out.Observations[i] = canonical
	}
	slices.SortFunc(out.Observations, func(a, c metering.Observation) int {
		if a.ID != c.ID {
			return strings.Compare(a.ID, c.ID)
		}
		if a.Revision != c.Revision {
			if a.Revision < c.Revision {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Fingerprint(), c.Fingerprint())
	})
	out.Lines = append([]StatementLine(nil), b.Lines...)
	slices.SortFunc(out.Lines, func(a, c StatementLine) int {
		if a.ID != c.ID {
			return strings.Compare(a.ID, c.ID)
		}
		if a.Revision != c.Revision {
			if a.Revision < c.Revision {
				return -1
			}
			return 1
		}
		return strings.Compare(string(a.Outcome), string(c.Outcome))
	})
	return out, nil
}

// StatementFingerprints is the deterministic replay identity of one statement
// revision. Statement and line fingerprints cover only economic claim
// content: transport receipt timestamps and the input order of observations
// and lines do not change them. Lines maps each StatementLineIdentity.Key to
// its line fingerprint.
type StatementFingerprints struct {
	Identity  StatementIdentity
	Statement string
	Lines     map[string]string
}

// ReplayFingerprints returns the canonical statement and per-line replay
// fingerprints for this batch. An exact replay of the same claim content
// produces the same fingerprints; any economically significant change
// produces a different one.
func (b StatementBatch) ReplayFingerprints() (StatementFingerprints, error) {
	canonical, err := b.Canonical()
	if err != nil {
		return StatementFingerprints{}, err
	}
	identity, err := canonical.Identity()
	if err != nil {
		return StatementFingerprints{}, err
	}

	observations := make(map[statementObservationRevisionKey]metering.Observation, len(canonical.Observations))
	observationReplays := make([]statementObservationReplayIdentity, 0, len(canonical.Observations))
	for i, observation := range canonical.Observations {
		replayHash, err := observation.ReplayFingerprint()
		if err != nil {
			return StatementFingerprints{}, fmt.Errorf("economics: statement observation %d replay identity: %w", i, err)
		}
		key := statementObservationRevisionKey{observationID: observation.ID, revision: observation.Revision}
		observations[key] = observation
		observationReplays = append(observationReplays, statementObservationReplayIdentity{
			ObservationID: observation.ID, Revision: observation.Revision, ReplayHash: replayHash,
		})
	}
	slices.SortFunc(observationReplays, func(a, c statementObservationReplayIdentity) int {
		if a.ObservationID != c.ObservationID {
			return strings.Compare(a.ObservationID, c.ObservationID)
		}
		if a.Revision != c.Revision {
			if a.Revision < c.Revision {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ReplayHash, c.ReplayHash)
	})

	lines := make(map[string]string, len(canonical.Lines))
	lineFingerprints := make([]statementLineFingerprint, 0, len(canonical.Lines))
	for i, line := range canonical.Lines {
		lineIdentity, err := line.Identity(identity)
		if err != nil {
			return StatementFingerprints{}, fmt.Errorf("economics: statement line %d identity: %w", i, err)
		}
		fingerprint, err := statementLineReplayFingerprint(lineIdentity, line, observations)
		if err != nil {
			return StatementFingerprints{}, fmt.Errorf("economics: statement line %d fingerprint: %w", i, err)
		}
		lines[lineIdentity.Key()] = fingerprint
		lineFingerprints = append(lineFingerprints, statementLineFingerprint{Key: lineIdentity.Key(), Fingerprint: fingerprint})
	}
	slices.SortFunc(lineFingerprints, func(a, c statementLineFingerprint) int {
		return strings.Compare(a.Key, c.Key)
	})

	statementPreimage := statementFingerprintPreimage{
		Version:      canonical.Version,
		Identity:     identity.Key(),
		Observations: observationReplays,
		Lines:        lineFingerprints,
	}
	encoded, err := json.Marshal(statementPreimage)
	if err != nil {
		return StatementFingerprints{}, fmt.Errorf("economics: statement fingerprint preimage: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return StatementFingerprints{
		Identity:  identity,
		Statement: hex.EncodeToString(sum[:]),
		Lines:     lines,
	}, nil
}

func statementLineReplayFingerprint(identity StatementLineIdentity, line StatementLine, observations map[statementObservationRevisionKey]metering.Observation) (string, error) {
	preimage := statementLineFingerprintPreimage{
		Identity:        identity.Key(),
		ChargeItemID:    line.ChargeItemID,
		Outcome:         line.Outcome,
		UnmatchedReason: line.UnmatchedReason,
	}
	if line.Outcome != StatementLineUnmatched {
		key := statementObservationRevisionKey{observationID: line.Observation.ObservationID, revision: line.Observation.Revision}
		observation, ok := observations[key]
		if !ok {
			return "", fmt.Errorf("economics: statement line observation is not included exactly")
		}
		replayHash, err := observation.ReplayFingerprint()
		if err != nil {
			return "", fmt.Errorf("economics: statement line observation replay identity: %w", err)
		}
		preimage.Observation = statementObservationReplayIdentity{
			ObservationID: observation.ID, Revision: observation.Revision, ReplayHash: replayHash,
		}
	}
	encoded, err := json.Marshal(preimage)
	if err != nil {
		return "", fmt.Errorf("economics: statement line fingerprint preimage: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type statementObservationRevisionKey struct {
	observationID string
	revision      uint64
}

type statementObservationReplayIdentity struct {
	ObservationID string `json:"observation_id"`
	Revision      uint64 `json:"revision"`
	ReplayHash    string `json:"replay_hash"`
}

type statementLineFingerprintPreimage struct {
	Identity        string                             `json:"identity"`
	Observation     statementObservationReplayIdentity `json:"observation"`
	ChargeItemID    string                             `json:"charge_item_id,omitempty"`
	Outcome         StatementLineOutcome               `json:"outcome,omitempty"`
	UnmatchedReason string                             `json:"unmatched_reason,omitempty"`
}

type statementLineFingerprint struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
}

type statementFingerprintPreimage struct {
	Version      uint32                               `json:"version"`
	Identity     string                               `json:"identity"`
	Observations []statementObservationReplayIdentity `json:"observations"`
	Lines        []statementLineFingerprint           `json:"lines"`
}

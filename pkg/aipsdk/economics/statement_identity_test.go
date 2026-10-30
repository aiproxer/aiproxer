package economics_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// statementIdentityFixture builds one deterministic statement batch with a
// matched line linked to a charged statement observation and an explicitly
// unmatched aggregate line. No request/B-leg lineage is present anywhere.
func statementIdentityFixture(t *testing.T) economics.StatementBatch {
	t.Helper()

	observation := statementIdentityObservation(t, statementIdentityObservationOptions{
		ID:       "statement-observation-1",
		LineID:   "line-1",
		ChargeID: "charge-1",
		Amount:   "1.25",
	})
	ref := metering.ObservationRef{
		StoreID:       observation.Subject.StoreID,
		ObservationID: observation.ID,
		Revision:      observation.Revision,
		PayloadHash:   observation.Fingerprint(),
	}
	subject := observation.Subject
	unmatchedSubject := subject
	unmatchedSubject.StatementLineID = "line-2"

	return economics.StatementBatch{
		Version:            1,
		ProviderAccountKey: "provider-account",
		StatementID:        "statement-1",
		Revision:           1,
		PeriodID:           "period-1",
		Subject:            subject,
		Observations:       []metering.Observation{observation},
		Lines: []economics.StatementLine{
			{
				ID: "line-1", Revision: 1, Subject: subject,
				Observation: ref, ChargeItemID: "charge-1", Outcome: economics.StatementLineMatched,
			},
			{
				ID: "line-2", Revision: 1, Subject: unmatchedSubject,
				Outcome: economics.StatementLineUnmatched, UnmatchedReason: "account-period aggregate",
			},
		},
	}
}

type statementIdentityObservationOptions struct {
	ID       string
	LineID   string
	ChargeID string
	Amount   string
	Received time.Time
}

func statementIdentityObservation(t *testing.T, opts statementIdentityObservationOptions) metering.Observation {
	t.Helper()

	received := opts.Received
	if received.IsZero() {
		received = time.Unix(1_700_000_000, 0).UTC()
	}
	component := metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: metering.ComponentAudio,
		Unit:      metering.UnitSecond,
		SchemaID:  "statement:audio:v1",
	}
	amount, err := metering.ParseDecimal(opts.Amount)
	require.NoError(t, err)

	return metering.Observation{
		Version:        2,
		ID:             opts.ID,
		SourceEventKey: "statement-event-" + opts.ID,
		Revision:       1,
		StreamID:       "statement-stream-1",
		Sequence:       1,
		Origin:         metering.OriginStatement,
		Acquisition:    metering.AcquisitionStatementImporter,
		Authority:      metering.AuthorityVerifiedStatement,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: "store-1",
			ProviderAccountKey: "provider-account", StatementID: "statement-1",
			StatementLineID: opts.LineID, PeriodID: "period-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-1", ProviderAccountKey: "provider-account", PeriodID: "period-1",
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
		ReceivedAt: received,
		MappingRef: "statement:test:v1",
		Measures: []metering.Measure{{
			Key: component, Value: decimalPtrForStatement("1"), Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: opts.ChargeID, Amount: &amount, Currency: "USD",
			Kind: metering.ChargeKindComponent, Component: &component,
		}},
	}
}

func decimalPtrForStatement(raw string) *metering.Decimal {
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return &value
}

func lineIdentityOf(t *testing.T, batch economics.StatementBatch, lineID string) string {
	t.Helper()
	statement, err := batch.Identity()
	require.NoError(t, err)
	for _, line := range batch.Lines {
		if line.ID != lineID {
			continue
		}
		identity, err := line.Identity(statement)
		require.NoError(t, err)
		return identity.Key()
	}
	t.Fatalf("line %q not present in fixture", lineID)
	return ""
}

func TestStatementIdentityIsDeterministicAndFieldScoped(t *testing.T) {
	t.Parallel()

	batch := statementIdentityFixture(t)
	identity, err := batch.Identity()
	require.NoError(t, err)
	require.NoError(t, identity.Validate())

	assert.Equal(t, "store-1", identity.StoreID)
	assert.Equal(t, "provider-account", identity.ProviderAccountKey)
	assert.Equal(t, "statement-1", identity.StatementID)
	assert.Equal(t, "period-1", identity.PeriodID)
	assert.Equal(t, uint64(1), identity.Revision)

	key := identity.Key()
	require.True(t, strings.HasPrefix(key, "statement:v1:"), "key = %q", key)
	require.Len(t, strings.TrimPrefix(key, "statement:v1:"), 64)
	assert.Equal(t, key, identity.Key(), "identity keys must be deterministic")
	assert.True(t, identity.Equal(identity))

	for name, mutate := range map[string]func(*economics.StatementIdentity){
		"store":     func(i *economics.StatementIdentity) { i.StoreID = "store-2" },
		"account":   func(i *economics.StatementIdentity) { i.ProviderAccountKey = "other-account" },
		"statement": func(i *economics.StatementIdentity) { i.StatementID = "statement-2" },
		"period":    func(i *economics.StatementIdentity) { i.PeriodID = "period-2" },
		"revision":  func(i *economics.StatementIdentity) { i.Revision = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := identity
			mutate(&changed)
			require.NoError(t, changed.Validate())
			assert.NotEqual(t, key, changed.Key())
			assert.False(t, identity.Equal(changed))
		})
	}

	for name, invalid := range map[string]economics.StatementIdentity{
		"zero":         {},
		"no store":     {ProviderAccountKey: "a", StatementID: "s", PeriodID: "p", Revision: 1},
		"no account":   {StoreID: "store-1", StatementID: "s", PeriodID: "p", Revision: 1},
		"no statement": {StoreID: "store-1", ProviderAccountKey: "a", PeriodID: "p", Revision: 1},
		"no period":    {StoreID: "store-1", ProviderAccountKey: "a", StatementID: "s", Revision: 1},
		"no revision":  {StoreID: "store-1", ProviderAccountKey: "a", StatementID: "s", PeriodID: "p"},
		"padded":       {StoreID: " store-1", ProviderAccountKey: "a", StatementID: "s", PeriodID: "p", Revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, invalid.Validate())
			assert.Empty(t, invalid.Key())
		})
	}
}

func TestStatementLineIdentityIsScopedToStatementAndLineRevision(t *testing.T) {
	t.Parallel()

	batch := statementIdentityFixture(t)
	statement, err := batch.Identity()
	require.NoError(t, err)

	identity, err := batch.Lines[0].Identity(statement)
	require.NoError(t, err)
	require.NoError(t, identity.Validate())
	assert.Equal(t, "line-1", identity.LineID)
	assert.Equal(t, uint64(1), identity.Revision)
	assert.Equal(t, "store-1", identity.StoreID)
	assert.Equal(t, "provider-account", identity.ProviderAccountKey)
	assert.Equal(t, "statement-1", identity.StatementID)
	assert.Equal(t, "period-1", identity.PeriodID)

	key := identity.Key()
	require.True(t, strings.HasPrefix(key, "statement-line:v1:"), "key = %q", key)
	require.Len(t, strings.TrimPrefix(key, "statement-line:v1:"), 64)
	assert.Equal(t, key, identity.Key())

	revisioned := identity
	revisioned.Revision = 2
	require.NoError(t, revisioned.Validate())
	assert.NotEqual(t, key, revisioned.Key(), "a line revision is a new immutable identity")
	assert.False(t, identity.Equal(revisioned))

	otherLine := identity
	otherLine.LineID = "line-2"
	require.NoError(t, otherLine.Validate())
	assert.NotEqual(t, key, otherLine.Key())

	// The statement envelope revision is not part of the line identity: a
	// later statement revision that restates this line revision must still be
	// compared against the same immutable claim.
	laterStatementRevision := statement
	laterStatementRevision.Revision = 2
	require.NoError(t, laterStatementRevision.Validate())
	laterIdentity, err := batch.Lines[0].Identity(laterStatementRevision)
	require.NoError(t, err)
	assert.Equal(t, key, laterIdentity.Key())

	otherStatement := statement
	otherStatement.StatementID = "statement-2"
	require.NoError(t, otherStatement.Validate())
	if _, err := batch.Lines[0].Identity(otherStatement); err == nil {
		t.Fatal("line from one statement must not resolve against another statement")
	}
}

func TestStatementBatchCanonicalIsDeterministicAndDetached(t *testing.T) {
	t.Parallel()

	batch := statementIdentityFixture(t)
	second := statementIdentityObservation(t, statementIdentityObservationOptions{
		ID: "statement-observation-0", LineID: "line-0", ChargeID: "charge-0", Amount: "2",
	})
	second.Subject.StatementLineID = "line-0"
	secondRef := metering.ObservationRef{
		StoreID: second.Subject.StoreID, ObservationID: second.ID,
		Revision: second.Revision, PayloadHash: second.Fingerprint(),
	}
	secondSubject := second.Subject
	batch.Observations = append([]metering.Observation{second}, batch.Observations...)
	batch.Lines = append([]economics.StatementLine{{
		ID: "line-0", Revision: 1, Subject: secondSubject,
		Observation: secondRef, ChargeItemID: "charge-0", Outcome: economics.StatementLineMatched,
	}}, batch.Lines...)

	canonical, err := batch.Canonical()
	require.NoError(t, err)
	first, err := canonical.Identity()
	require.NoError(t, err)

	reversed := batch
	reversed.Observations = slices.Clone(batch.Observations)
	reversed.Lines = slices.Clone(batch.Lines)
	slices.Reverse(reversed.Observations)
	slices.Reverse(reversed.Lines)
	reversedCanonical, err := reversed.Canonical()
	require.NoError(t, err)

	secondIdentity, err := reversedCanonical.Identity()
	require.NoError(t, err)
	assert.Equal(t, first, secondIdentity)
	assert.Equal(t, canonical.Observations, reversedCanonical.Observations)
	assert.Equal(t, canonical.Lines, reversedCanonical.Lines)

	// Canonical detaches observations so a caller cannot mutate retained data
	// through the original slices.
	batch.Observations[0].Charges[0].ChargeItemID = "mutated"
	batch.Lines[0].ID = "mutated"
	assert.Equal(t, "charge-0", canonical.Observations[0].Charges[0].ChargeItemID)
	assert.Equal(t, "line-0", canonical.Lines[0].ID)
}

func TestStatementBatchReplayFingerprintsIgnoreReceiptAndInputOrder(t *testing.T) {
	t.Parallel()

	batch := statementIdentityFixture(t)
	fingerprints, err := batch.ReplayFingerprints()
	require.NoError(t, err)
	require.NoError(t, fingerprints.Identity.Validate())
	assert.Len(t, fingerprints.Lines, 2)
	require.Len(t, fingerprints.Statement, 64)

	// A replayed payload may arrive with a later receipt timestamp and a
	// shuffled observation/line order; neither changes economic identity.
	replayed := batch
	replayed.Observations = slices.Clone(batch.Observations)
	replayed.Lines = slices.Clone(batch.Lines)
	later := batch.Observations[0]
	later.ReceivedAt = later.ReceivedAt.Add(time.Hour)
	replayed.Observations[0] = later
	replayed.Lines[0].Observation.PayloadHash = later.Fingerprint()
	slices.Reverse(replayed.Observations)
	slices.Reverse(replayed.Lines)

	replayedFingerprints, err := replayed.ReplayFingerprints()
	require.NoError(t, err)
	assert.Equal(t, fingerprints, replayedFingerprints, "receipt metadata and input order must not change replay identity")

	for _, line := range replayed.Lines {
		identity, err := replayed.Identity()
		require.NoError(t, err)
		lineIdentity, err := line.Identity(identity)
		require.NoError(t, err)
		assert.Equal(t, fingerprints.Lines[lineIdentity.Key()], replayedFingerprints.Lines[lineIdentity.Key()])
	}

	changedAmount := statementIdentityFixture(t)
	changedAmount.Observations[0].Charges[0].Amount = decimalPtrForStatement("2.50")
	changedAmount.Lines[0].Observation.PayloadHash = changedAmount.Observations[0].Fingerprint()
	changedFingerprints, err := changedAmount.ReplayFingerprints()
	require.NoError(t, err)
	assert.NotEqual(t, fingerprints.Statement, changedFingerprints.Statement)
	changedIdentity, err := changedAmount.Identity()
	require.NoError(t, err)
	changedLineIdentity, err := changedAmount.Lines[0].Identity(changedIdentity)
	require.NoError(t, err)
	assert.NotEqual(t, fingerprints.Lines[lineIdentityOf(t, batch, "line-1")], changedFingerprints.Lines[changedLineIdentity.Key()])

	changedReason := statementIdentityFixture(t)
	changedReason.Lines[1].UnmatchedReason = "different aggregate basis"
	reasonFingerprints, err := changedReason.ReplayFingerprints()
	require.NoError(t, err)
	assert.NotEqual(t, fingerprints.Statement, reasonFingerprints.Statement)
	assert.NotEqual(t, fingerprints.Lines[lineIdentityOf(t, batch, "line-2")], reasonFingerprints.Lines[lineIdentityOf(t, changedReason, "line-2")])
}

func TestStatementBatchReplayFingerprintsRejectInvalidBatches(t *testing.T) {
	t.Parallel()

	valid := statementIdentityFixture(t)
	require.NoError(t, valid.Validate())

	for name, mutate := range map[string]func(*economics.StatementBatch){
		"zero version":   func(b *economics.StatementBatch) { b.Version = 0 },
		"zero revision":  func(b *economics.StatementBatch) { b.Revision = 0 },
		"foreign store":  func(b *economics.StatementBatch) { b.Observations[0].Subject.StoreID = "store-2" },
		"missing charge": func(b *economics.StatementBatch) { b.Lines[0].ChargeItemID = "charge-missing" },
		"invalid amount": func(b *economics.StatementBatch) {
			b.Observations[0].Charges[0].Amount = &metering.Decimal{Coefficient: "1.2.5", Scale: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invalid := valid
			invalid.Observations = slices.Clone(valid.Observations)
			invalid.Lines = slices.Clone(valid.Lines)
			if name == "invalid amount" {
				invalid.Observations[0].Charges = slices.Clone(valid.Observations[0].Charges)
			}
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatalf("batch accepted %s", name)
			}
			if _, err := invalid.Canonical(); err == nil {
				t.Fatalf("canonical accepted %s", name)
			}
			if _, err := invalid.ReplayFingerprints(); err == nil {
				t.Fatalf("fingerprints accepted %s", name)
			}
		})
	}
}

func FuzzStatementBatchReplayFingerprints(f *testing.F) {
	seed := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-account", StatementID: "statement-1",
		Revision: 1, PeriodID: "period-1",
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: "store-1",
			ProviderAccountKey: "provider-account", StatementID: "statement-1",
			StatementLineID: "line-1", PeriodID: "period-1",
		},
		Lines: []economics.StatementLine{{
			ID: "line-1", Revision: 1,
			Subject: metering.SubjectRef{
				Kind: metering.SubjectStatementLine, StoreID: "store-1",
				ProviderAccountKey: "provider-account", StatementID: "statement-1",
				StatementLineID: "line-1", PeriodID: "period-1",
			},
			Outcome: economics.StatementLineUnmatched, UnmatchedReason: "aggregate",
		}},
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte(`{"version":0}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var batch economics.StatementBatch
		if err := json.Unmarshal(data, &batch); err != nil {
			return
		}
		fingerprints, err := batch.ReplayFingerprints()
		if err != nil {
			return
		}
		canonical, err := batch.Canonical()
		if err != nil {
			t.Fatalf("canonical rejected a batch whose fingerprints succeeded: %v", err)
		}
		again, err := canonical.ReplayFingerprints()
		if err != nil {
			t.Fatalf("canonical batch lost its replay identity: %v", err)
		}
		if !reflect.DeepEqual(fingerprints, again) {
			t.Fatalf("canonicalization changed replay identity:\n%+v\n%+v", fingerprints, again)
		}
		reversed := batch
		reversed.Observations = slices.Clone(batch.Observations)
		reversed.Lines = slices.Clone(batch.Lines)
		slices.Reverse(reversed.Observations)
		slices.Reverse(reversed.Lines)
		reversedFingerprints, err := reversed.ReplayFingerprints()
		if err != nil {
			t.Fatalf("reordered batch lost its replay identity: %v", err)
		}
		if !reflect.DeepEqual(fingerprints, reversedFingerprints) {
			t.Fatal("observation/line input order must not change replay identity")
		}
	})
}

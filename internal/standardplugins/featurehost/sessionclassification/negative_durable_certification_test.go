package sessionclassification_test

// Task 10.2 remote negative certification for requirements 1.5, 6.8 and 6.9.
//
// The composed false-positive suite in
// internal/standardplugins/featurehost covers the LOCAL negatives through the real
// canonical and wire lanes. The remote negatives cannot be certified there: the
// standard featurehost wires its decider by dependency injection from the vendor
// adapter, and a hermetic run has to supply the decider itself. So the durable
// half is certified here, against the production SQLite-backed store, the
// production coordinator and lease, and the production classifier's remote phase,
// with only the vendor transport replaced by a scripted bounded answer.
//
// What is proven, per outcome:
//
//   - a valid remote result BELOW the configured threshold leaves the session
//     unknown, spends a bounded attempt, and persists NO negative classification;
//   - a remote timeout, rate limit, server failure, transport failure and
//     malformed/unmappable answer each preserve the user request, leave the
//     session unknown, and persist NO negative classification;
//   - the same negative turns never rewrite an established positive; and
//   - an ABOVE-threshold result in the very same harness does persist a positive,
//     which is what makes every negative above load-bearing instead of vacuous.
//
// The durable claim is checked twice, because the store's own Load path validates a
// row and would report a persisted negative as an error rather than as a value:
//
//   - through an INDEPENDENT store instance over the same database, and
//   - through a RAW census of the classification table, so a row carrying any kind
//     outside the V1 vocabulary is visible as a fact rather than hidden behind a
//     validation refusal.

import (
	"context"
	"testing"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// negativeRemoteRow is one physical row of the feature-owned classification table.
type negativeRemoteRow struct {
	ScopeKind  string `bun:"scope_kind"`
	ScopeID    string `bun:"scope_id"`
	Kind       string `bun:"kind"`
	Source     string `bun:"source"`
	Confidence string `bun:"confidence"`
	Evidence   string `bun:"evidence_code"`
	Revision   int64  `bun:"classification_revision"`
	Attempts   int64  `bun:"remote_attempts"`
}

// negativeRemoteCensus reads every durable classification row directly.
func negativeRemoteCensus(t *testing.T, database *bun.DB) []negativeRemoteRow {
	t.Helper()
	var rows []negativeRemoteRow
	require.NoError(t, database.NewSelect().
		Table("session_classification").
		Column("scope_kind", "scope_id", "kind", "source", "confidence", "evidence_code",
			"classification_revision", "remote_attempts").
		Order("scope_kind", "scope_id").
		Scan(context.Background(), &rows))
	return rows
}

// assertNoDurableNegativeKind fails when ANY row holds a kind outside the V1
// vocabulary, and when a row is meant to be a plain unknown but carries
// classification content anyway. Requirement 1.5 forbids a not_coding state, so a
// negative classification is not merely discouraged: it has no representation and
// must not be reachable through the store.
func assertNoDurableNegativeKind(t *testing.T, database *bun.DB) []negativeRemoteRow {
	t.Helper()
	rows := negativeRemoteCensus(t, database)
	for _, row := range rows {
		switch row.Kind {
		case string(session.KindUnknown), string(session.KindCodingAgent):
		default:
			t.Errorf("durable row (%s/%s) holds kind %q; requirement 1.5's vocabulary is unknown/coding_agent only",
				row.ScopeKind, row.ScopeID, row.Kind)
		}
		if row.Kind == string(session.KindUnknown) &&
			(row.Source != "" || row.Confidence != "" || row.Evidence != "" || row.Revision != 0) {
			t.Errorf("durable row (%s/%s) is unknown but carries classification content "+
				"(source=%q confidence=%q evidence=%q revision=%d)",
				row.ScopeKind, row.ScopeID, row.Source, row.Confidence, row.Evidence, row.Revision)
		}
	}
	return rows
}

// negativeRemoteCase is one scripted remote outcome and its durable expectation.
type negativeRemoteCase struct {
	name      string
	answer    deciderAnswer
	wantCalls int
	// wantPositive distinguishes the control case, which must persist a positive.
	wantPositive bool
}

func negativeRemoteCases() []negativeRemoteCase {
	return []negativeRemoteCase{
		{
			name:      "below threshold",
			answer:    deciderAnswer{decision: featurestate.RemoteDecision{CodingProbability: 0.10}},
			wantCalls: 1,
		},
		{
			name:      "just below threshold",
			answer:    deciderAnswer{decision: featurestate.RemoteDecision{CodingProbability: 0.8999}},
			wantCalls: 1,
		},
		{
			name:      "zero probability",
			answer:    deciderAnswer{decision: featurestate.RemoteDecision{CodingProbability: 0}},
			wantCalls: 1,
		},
		{
			name: "timeout",
			answer: deciderAnswer{err: boundedDeciderFailure{
				outcome: featurestate.RemoteTimeout, cause: context.DeadlineExceeded,
			}},
			wantCalls: 1,
		},
		{
			name: "rate limited",
			answer: deciderAnswer{err: boundedDeciderFailure{
				outcome: featurestate.RemoteRateLimited,
			}},
			wantCalls: 1,
		},
		{
			name: "server error",
			answer: deciderAnswer{err: boundedDeciderFailure{
				outcome: featurestate.RemoteServerError,
			}},
			wantCalls: 1,
		},
		{
			name: "transport failure",
			answer: deciderAnswer{err: boundedDeciderFailure{
				outcome: featurestate.RemoteNetworkError,
			}},
			wantCalls: 1,
		},
		{
			name: "malformed or unmappable answer",
			answer: deciderAnswer{err: boundedDeciderFailure{
				outcome: featurestate.RemoteMalformed,
			}},
			wantCalls: 1,
		},
		{
			// The control: the same harness, the same store, the same lease. Without
			// it a broken remote phase could report unknown for every outcome and the
			// negatives above would certify nothing.
			name:         "above threshold control",
			answer:       deciderAnswer{decision: featurestate.RemoteDecision{CodingProbability: 0.95}},
			wantCalls:    1,
			wantPositive: true,
		},
	}
}

// TestRemoteNegativeOutcomesPersistNoDurableNegativeClassification is the
// requirement 6.8/6.9 durable certification.
//
// Each case runs against its own on-disk SQLite database with a real store, a real
// coordinator and the production classifier, so the durable claim is about the
// committed row rather than about an in-memory value.
func TestRemoteNegativeOutcomesPersistNoDurableNegativeClassification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, tc := range negativeRemoteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dsn := sqliteTestDSN(t, "classification-negative-remote.db")
			_, database := openSQLiteBunDBWithConnections(t, dsn, 8)
			inner, err := store.NewBunStore(database)
			require.NoError(t, err)
			require.NoError(t, inner.EnsureSchema(ctx))

			decider := &countingRemoteDecider{fallback: tc.answer}
			// One attempt per session keeps every case deterministic: the finite
			// budget is consumed by the single scripted answer, so wantCalls is exact.
			classifier := flowClassifier(t, remoteFlowConfig(1, 0), mustCoordinator(t, inner), decider)
			key := flowKey("negative-remote/" + tc.name)

			got, err := classifier.Classify(ctx, ambiguousFlowInput(string(key.ID)))
			require.NoError(t, err, "a bounded remote outcome must never fail the user request (requirement 6.9)")
			require.Equal(t, tc.wantCalls, decider.callCount(),
				"the remote phase must have been attempted exactly once for this outcome")
			if tc.wantPositive {
				require.True(t, got.IsCodingAgent(), "the control outcome must persist a positive: %+v", got)
				assert.Equal(t, session.SourceRemote, got.Source)
				assert.Equal(t, featurestate.EvidenceCodeRemoteAboveThreshold, got.Evidence)
			} else {
				assert.Equal(t, session.Classification{}, got,
					"a below-threshold or failed remote outcome must leave the session unknown")
			}

			// Read back through an INDEPENDENT store instance so a process cache
			// cannot stand in for committed state.
			reader, err := store.NewBunStore(database)
			require.NoError(t, err)
			record, found, err := reader.Load(ctx, key)
			require.NoError(t, err)
			if tc.wantPositive {
				require.True(t, found)
				assert.True(t, record.Classification.IsCodingAgent())
				assert.Equal(t, uint64(1), record.Classification.Revision)
				assert.Empty(t, record.RemoteLeaseID, "an accepted completion must release the lease")
			} else {
				require.True(t, found,
					"the remote phase claimed a lease, so a row must exist; its classification must be the zero value")
				assert.Equal(t, session.Classification{}, record.Classification,
					"the committed row must hold the exact unknown value, never a negative")
				assert.Equal(t, uint32(1), record.RemoteAttempts,
					"the attempt must be durably consumed, which is what makes the unknown load-bearing")
				assert.Empty(t, record.RemoteLeaseID, "a completed attempt must release its lease")
			}

			rows := assertNoDurableNegativeKind(t, database)
			require.Len(t, rows, 1, "this case must own exactly one durable row")

			// A second turn with the same ambiguous evidence must not turn the
			// accumulated unknown into a negative, and must not re-spend the budget.
			second, err := classifier.Classify(ctx, ambiguousFlowInput(string(key.ID)))
			require.NoError(t, err)
			if !tc.wantPositive {
				assert.Equal(t, session.Classification{}, second)
				assert.Equal(t, 1, decider.callCount(),
					"the spent per-session attempt budget must stop a second remote call")
			}
			afterRows := assertNoDurableNegativeKind(t, database)
			require.Len(t, afterRows, 1, "a second unknown turn must not add a durable row")
			assert.Equal(t, rows[0], afterRows[0],
				"a second unknown turn must leave the durable row byte-identical")
		})
	}
}

// TestBelowThresholdRemoteCompletionCannotBeRecordedAsANegative is the
// requirement 6.8 half that is not reachable through the Store API, and the
// reason the census above exists.
//
// A below-threshold completion carries the ZERO proposal by construction
// (remoteProposal), so no API sequence can make it persist a negative. The durable
// table is nevertheless the shared multi-writer boundary, so the row this suite
// relies on being blank is constructed directly with raw SQL, carrying every field
// a negative classification would carry, and then read back through the production
// store. The store must refuse it rather than serve it.
//
// This pins the table-boundary defence and proves the census is not satisfied by a
// store that happens to validate its own reads: the row is invisible to the API and
// visible to the census.
func TestBelowThresholdRemoteCompletionCannotBeRecordedAsANegative(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := sqliteTestDSN(t, "classification-negative-row.db")
	_, database := openSQLiteBunDBWithConnections(t, dsn, 4)
	classificationStore, err := store.NewBunStore(database)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	// A row shaped exactly like a persisted negative classification. The raw
	// insert bypasses every API guard on purpose: this is the state at the table
	// boundary, which no single-writer sequence can produce.
	negativeKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "negative-row/authority"}
	_, err = database.NewRaw(`INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code,
		classification_revision, remote_attempts, remote_lease_id,
		remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, 'not_coding', 'local_tooling', 'high', 'tooling.distinct_coding_cluster', 1, 1, '', NULL, NULL, ?)`,
		negativeKey.Kind, negativeKey.ID, testStoreTime(),
	).Exec(ctx)
	require.NoError(t, err)

	// The raw census must SEE the negative even though the store refuses to serve
	// it. Without this, "no durable negative classification" could be satisfied by
	// a store that simply could not read such a row.
	rows := negativeRemoteCensus(t, database)
	require.Len(t, rows, 1)
	assert.Equal(t, "not_coding", rows[0].Kind,
		"the raw census must observe the constructed negative kind")

	// The production store must refuse the row instead of projecting it, so no
	// consumer can ever observe a negative classification through the SDK contract.
	_, found, err := classificationStore.Load(ctx, negativeKey)
	if err == nil {
		t.Fatalf("the production store served a persisted negative kind as a loadable record (found=%t); "+
			"requirement 1.5 forbids a negative classification from being representable", found)
	}
	assert.ErrorIs(t, err, store.ErrInvalidBunRecord)

	// And the classifier over the same store must fail open to unknown rather than
	// project the negative. The decider is a scripted zero answer so a jev-mode
	// classifier can be constructed at all; it is never reached, because the
	// unreadable durable record is the first thing the turn consults.
	decider := &countingRemoteDecider{fallback: deciderAnswer{}}
	classifier := flowClassifier(t, remoteFlowConfig(1, 0), mustCoordinator(t, classificationStore), decider)
	got, err := classifier.Classify(ctx, ambiguousFlowInput(negativeKey.ID))
	if err == nil {
		// A state failure is allowed to surface; when it does not, the projection
		// must still be the conservative zero value.
		assert.Equal(t, session.Classification{}, got,
			"a state failure must fail open to the conservative unknown, never to a negative")
	}
	assert.NotEqual(t, session.Kind("not_coding"), got.Kind,
		"a persisted negative kind must never reach a consumer's classification snapshot")
	assert.Equal(t, 0, decider.callCount(),
		"an unreadable durable record must stop the turn before any remote egress")
}

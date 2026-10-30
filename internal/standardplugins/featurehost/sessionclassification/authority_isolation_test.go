package sessionclassification_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 9.2 authority-isolation certification.
//
// Requirement 2.1/2.2 scope classification state to a proxy-owned secure SessionID
// or, only when that is exactly empty, to a proxy-owned A-leg. Requirement 2.3
// names the failure this file exists to kill: two clients that reuse one
// client-controlled session hint must not share classification state. The spec's
// own implementation note warns that the generic SessionView.PartitionKey()
// still falls back to ClientSessionHint, so "keyed by authority" is not something
// the type system enforces. It is pinned here by construction, at every layer that
// could key on a client value: the key resolver, the authoritative stores, and the
// process-local coordinator cache.

// authoritySharedClientHint is the single client-controlled value every certified
// view carries. Requirement 2.3 is exactly the case where several unrelated
// clients reuse it, so no assertion below may ever see it in a store or cache key.
const authoritySharedClientHint = "client-shared-session-hint"

// authorityIsolationViews builds one view per authority. Even indices resolve to a
// secure-session scope and odd indices to an A-leg scope, and every view reuses the
// same client hint, so a key resolver that consults the hint collapses all of them.
func authorityIsolationViews(count int) []session.SessionView {
	views := make([]session.SessionView, 0, count)
	for i := range count {
		view := session.SessionView{
			ALegID:            fmt.Sprintf("isolation/a-leg-%d", i),
			ClientSessionHint: authoritySharedClientHint,
		}
		if i%2 == 0 {
			view.AuthoritativeSessionID = fmt.Sprintf("isolation/secure-session-%d", i)
		}
		views = append(views, view)
	}
	return views
}

// TestResolveKeyScopesToProxyAuthorityNotSharedClientHint pins requirement 2.1, 2.2,
// 2.3 and 2.9 at the only place a session view becomes a feature key.
//
// Two mutations are killed here, and they are the two the spec's implementation
// note warns about. First, ResolveKey consulting ClientSessionHint: a view with no
// proxy-owned authority must produce no key at all, so a hint-only view fails the
// ErrNoAuthority assertion below, and a hint-preferring resolver makes every
// distinctness assertion collapse onto the one shared hint. Second, ResolveKey using
// view.PartitionKey(), which falls back to the hint by construction: that returns a
// bare string, so it can only reach a Key by labelling the hint with a scope kind,
// which the per-authority ID and scope assertions reject.
func TestResolveKeyScopesToProxyAuthorityNotSharedClientHint(t *testing.T) {
	t.Parallel()

	views := authorityIsolationViews(8)
	keys := make([]featurestate.Key, 0, len(views))
	for i, view := range views {
		key, err := featurestate.ResolveKey(view)
		require.NoError(t, err, "view %d carries proxy-owned authority", i)
		require.NoError(t, featurestate.ValidateKey(key))

		wantKind := featurestate.ScopeALeg
		wantID := view.ALegID
		if view.AuthoritativeSessionID != "" {
			wantKind = featurestate.ScopeSecureSession
			wantID = view.AuthoritativeSessionID
		}
		assert.Equal(t, wantKind, key.Kind, "view %d resolved the wrong authority scope", i)
		assert.Equal(t, wantID, key.ID, "view %d resolved a value other than its proxy-owned identifier", i)
		assert.NotEqual(t, authoritySharedClientHint, key.ID,
			"view %d keyed classification state by the client-supplied session hint", i)
		keys = append(keys, key)
	}

	distinct := make(map[featurestate.Key]struct{}, len(keys))
	for i, key := range keys {
		if _, duplicate := distinct[key]; duplicate {
			t.Errorf("view %d resolved key %+v, which an earlier view already owns: authorities sharing one client hint must not collide", i, key)
		}
		distinct[key] = struct{}{}
	}
	assert.Len(t, distinct, len(views))
}

// TestResolveKeyRefusesClientHintWithoutProxyAuthority is the requirement 2.2 half
// that distinctness alone cannot express: a turn whose only scope-like value is the
// client-supplied hint must resolve to NO key, so no store call and no cache entry
// can ever be created for it.
//
// This is the assertion a hint fallback breaks first, and it is what makes the
// isolation guarantee total rather than merely collision-free among authorities.
func TestResolveKeyRefusesClientHintWithoutProxyAuthority(t *testing.T) {
	t.Parallel()

	for name, view := range map[string]session.SessionView{
		"hint only": {
			ClientSessionHint: authoritySharedClientHint,
		},
		"hint matching another authority's identifier": {
			ClientSessionHint: "isolation/secure-session-0",
		},
		"hint and workspace only": {
			ClientSessionHint: authoritySharedClientHint,
			WorkspaceID:       "workspace-from-a-resolver",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			key, err := featurestate.ResolveKey(view)
			require.ErrorIs(t, err, featurestate.ErrNoAuthority,
				"a client-controlled value must never become classification state authority")
			assert.Equal(t, featurestate.Key{}, key)
			assert.NotContains(t, err.Error(), view.ClientSessionHint,
				"the refusal must not echo the client-supplied value")
		})
	}
}

// TestResolveKeyNegativeControls pins the two controls that make the distinctness
// assertions above meaningful: the same authority resolved twice is the same key
// (so a session keeps its own row), and the same identifier under two different
// authority scopes is two keys (so scope is part of the key).
func TestResolveKeyNegativeControls(t *testing.T) {
	t.Parallel()

	const authorityID = "negative-control-authority"
	first, err := featurestate.ResolveKey(session.SessionView{
		AuthoritativeSessionID: authorityID,
		ALegID:                 "negative-control/a-leg",
		ClientSessionHint:      authoritySharedClientHint,
	})
	require.NoError(t, err)
	// The same authoritative session, reached through a different A-leg and a
	// different client hint: still one key, so its row is still its own.
	again, err := featurestate.ResolveKey(session.SessionView{
		AuthoritativeSessionID: authorityID,
		ALegID:                 "negative-control/another-a-leg",
		ClientSessionHint:      "another-client-hint",
	})
	require.NoError(t, err)
	assert.Equal(t, first, again, "the same proxy-owned authority must resolve to one stable key")

	// The same identifier bytes under two authorities are two keys. This is the
	// control for a key resolver that keeps only the identifier and drops the scope.
	aLeg, err := featurestate.ResolveKey(session.SessionView{ALegID: authorityID, ClientSessionHint: authoritySharedClientHint})
	require.NoError(t, err)
	assert.Equal(t, featurestate.Key{Kind: featurestate.ScopeALeg, ID: authorityID}, aLeg)
	assert.NotEqual(t, first, aLeg, "authority kind must be part of the resolved key")

	// A client hint that happens to equal another authority's identifier is still
	// not that authority's state.
	hintOnly, err := featurestate.ResolveKey(session.SessionView{
		ALegID:            "negative-control/hint-collision",
		ClientSessionHint: authorityID,
	})
	require.NoError(t, err)
	assert.NotEqual(t, first, hintOnly)
	assert.NotEqual(t, hintOnly.ID, authorityID)
}

// runSessionClassificationAuthorityIsolationContract is the dialect-neutral durable
// contract: one client hint reused by many proxy-owned authorities yields one row
// per authority, each carrying only its own classification.
//
// It is invoked for the in-process store and, through
// runSessionClassificationParityContract, for SQLite and direct PostgreSQL, so the
// same assertions run on every durable topology the feature supports.
func runSessionClassificationAuthorityIsolationContract(t *testing.T, newStore func(t *testing.T) featurestate.Store) {
	t.Helper()
	ctx := context.Background()
	authority := newStore(t)

	views := authorityIsolationViews(6)
	keys := make([]featurestate.Key, 0, len(views))
	evidenceCodes := make([]session.EvidenceCode, 0, len(views))
	for i := range views {
		key, err := featurestate.ResolveKey(views[i])
		require.NoError(t, err)
		keys = append(keys, key)
		evidenceCodes = append(evidenceCodes, session.EvidenceCode(fmt.Sprintf("isolation.authority_%d", i)))
	}

	// Every authority must be promotable: if any key resolved to another
	// authority's row, the first-positive-wins guard would refuse it.
	for i, key := range keys {
		record, promoted, err := authority.Promote(ctx, key, positiveProposal(session.SourceLocalIdentity, evidenceCodes[i]), testStoreTime())
		require.NoError(t, err, "authority %d", i)
		require.True(t, promoted, "authority %d (%+v) was refused: it observed another authority's positive", i, key)
		require.Equal(t, key, record.Key)
		require.Equal(t, uint64(1), record.Classification.Revision)
		require.Equal(t, evidenceCodes[i], record.Classification.Evidence)
	}

	// Every authority must read back exactly its own row, with no other
	// authority's source or evidence anywhere in the store.
	for i, key := range keys {
		record, found, err := authority.Load(ctx, key)
		require.NoError(t, err, "authority %d", i)
		require.True(t, found, "authority %d (%+v) has no row of its own", i, key)
		require.Equal(t, evidenceCodes[i], record.Classification.Evidence,
			"authority %d (%+v) read another authority's row", i, key)
		require.Equal(t, key, record.Key)
	}

	// Negative control: the same authority reused sees its own row and cannot
	// rewrite it, so isolation is not implemented by refusing every second write.
	winner, found, err := authority.Load(ctx, keys[0])
	require.NoError(t, err)
	require.True(t, found)
	replay, promoted, err := authority.Promote(ctx, keys[0], positiveProposal(session.SourceLocalTooling, "isolation.replayed_evidence"), testStoreTime().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, promoted, "replaying one authority's own proposal must be idempotent")
	assert.Equal(t, winner, replay, "replaying one authority's own proposal must return its own row")

	// The scope is part of the durable key: equal identifier bytes under two
	// authorities own two rows with two independent classifications. Its
	// identifier deliberately sits outside the certified census prefix.
	const collidedID = "authority-collision/collided-id"
	secureKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: collidedID}
	aLegKey := featurestate.Key{Kind: featurestate.ScopeALeg, ID: collidedID}
	secureRecord, promoted, err := authority.Promote(ctx, secureKey, positiveProposal(session.SourceLocalIdentity, "isolation.collide_secure"), testStoreTime())
	require.NoError(t, err)
	require.True(t, promoted)
	aLegRecord, promoted, err := authority.Promote(ctx, aLegKey, positiveProposal(session.SourceLocalTooling, "isolation.collide_a_leg"), testStoreTime())
	require.NoError(t, err)
	require.True(t, promoted, "an A-leg authority must not collide with a same-ID secure-session authority")
	assert.NotEqual(t, secureRecord.Classification.Evidence, aLegRecord.Classification.Evidence)
	assert.Equal(t, session.SourceLocalIdentity, secureRecord.Classification.Source)
	assert.Equal(t, session.SourceLocalTooling, aLegRecord.Classification.Source)
}

// TestAuthorityIsolationContractInProcessStore runs the durable isolation contract
// against the bounded in-process authoritative store.
func TestAuthorityIsolationContractInProcessStore(t *testing.T) {
	t.Parallel()

	runSessionClassificationAuthorityIsolationContract(t, func(t *testing.T) featurestate.Store {
		t.Helper()
		memory, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 64, IdleTTL: time.Hour})
		require.NoError(t, err)
		return memory
	})
}

// authorityIsolationRow is the census projection: it reads the durable columns
// directly instead of through the store, so a read path that filtered wrongly
// cannot mask a wrong write.
type authorityIsolationRow struct {
	ScopeKind string `bun:"scope_kind"`
	ScopeID   string `bun:"scope_id"`
	Evidence  string `bun:"evidence_code"`
}

// TestAuthorityIsolationContractSQLite runs the same contract against the durable
// Bun store and censuses the physical rows, so a store that silently overwrote one
// authority's row with another's cannot hide behind a correct read path.
func TestAuthorityIsolationContractSQLite(t *testing.T) {
	t.Parallel()

	dsn := sqliteTestDSN(t, "classification-authority-isolation.db")
	_, database := openSQLiteBunDBWithConnections(t, dsn, 4)
	classificationStore, err := store.NewBunStore(database)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(context.Background()))

	runSessionClassificationAuthorityIsolationContract(t, func(t *testing.T) featurestate.Store {
		t.Helper()
		return classificationStore
	})

	ctx := context.Background()
	views := authorityIsolationViews(6)
	var census []authorityIsolationRow
	require.NoError(t, database.NewRaw(
		"SELECT scope_kind, scope_id, evidence_code FROM session_classification WHERE scope_id LIKE ? ORDER BY scope_id",
		"isolation/%",
	).Scan(ctx, &census))
	require.Len(t, census, len(views),
		"one durable row per proxy-owned authority that shared the client hint, got %+v", census)
	for i, view := range views {
		key, err := featurestate.ResolveKey(view)
		require.NoError(t, err)
		var rows []authorityIsolationRow
		require.NoError(t, database.NewRaw(
			"SELECT scope_kind, scope_id, evidence_code FROM session_classification WHERE scope_kind = ? AND scope_id = ?",
			key.Kind, key.ID,
		).Scan(ctx, &rows), "authority %d", i)
		require.Len(t, rows, 1, "authority %d (%+v) owns exactly one durable row", i, key)
		require.Equal(t, fmt.Sprintf("isolation.authority_%d", i), rows[0].Evidence,
			"authority %d must own the row carrying only its own evidence", i)
	}

	// No row anywhere in the table may be keyed by the shared client hint: a
	// resolver that fell back to the hint would write every authority's
	// classification into that one scope.
	var hinted []authorityIsolationRow
	require.NoError(t, database.NewRaw(
		"SELECT scope_kind, scope_id, evidence_code FROM session_classification WHERE scope_id = ?",
		authoritySharedClientHint,
	).Scan(ctx, &hinted))
	assert.Empty(t, hinted,
		"a classification row was written under the client-supplied session hint")
}

// authorityLoadCounter wraps a real store and records every store call. It exists
// so a cache-isolation claim is measured in durable calls rather than inferred:
// a second turn that is served from the process cache must not reach the store,
// and a cache keyed by anything but the authority would show up as extra calls.
type authorityLoadCounter struct {
	inner featurestate.Store

	mu     sync.Mutex
	loads  []featurestate.Key
	promot []featurestate.Key
}

func (s *authorityLoadCounter) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	s.mu.Lock()
	s.loads = append(s.loads, key)
	s.mu.Unlock()
	return s.inner.Load(ctx, key)
}

func (s *authorityLoadCounter) Promote(
	ctx context.Context,
	key featurestate.Key,
	proposal session.Classification,
	now time.Time,
) (featurestate.Record, bool, error) {
	s.mu.Lock()
	s.promot = append(s.promot, key)
	s.mu.Unlock()
	return s.inner.Promote(ctx, key, proposal, now)
}

func (s *authorityLoadCounter) ClaimRemote(
	ctx context.Context,
	key featurestate.Key,
	now time.Time,
	maxAttempts uint32,
	leaseTTL, retryBackoff time.Duration,
) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	return s.inner.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
}

func (s *authorityLoadCounter) CompleteRemote(
	ctx context.Context,
	claim featurestate.RemoteClaim,
	result featurestate.RemoteCompletion,
	now time.Time,
) (featurestate.Record, error) {
	return s.inner.CompleteRemote(ctx, claim, result, now)
}

func (s *authorityLoadCounter) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.loads)
}

func (s *authorityLoadCounter) promoteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.promot)
}

// TestCoordinatorCacheIsKeyedByAuthorityNotClientHint pins the process-local half of
// requirement 2.3. The coordinator is the only cache between a session view and the
// authoritative store, so a cache entry keyed by a client-controlled value would
// hand one authority's positive to another.
//
// The census is the load-bearing part: after every authority's positive is cached,
// a second round of turns must issue zero durable loads. A cache that collapsed the
// authorities onto one key would keep only one entry and the missing entries would
// fall through to the store.
func TestCoordinatorCacheIsKeyedByAuthorityNotClientHint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	memory, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 64, IdleTTL: time.Hour})
	require.NoError(t, err)
	counted := &authorityLoadCounter{inner: memory}
	coordinator, err := store.NewCoordinator(counted, store.CoordinatorConfig{
		CacheCapacity: 64,
		MaxInflight:   64,
		IdleTTL:       time.Hour,
	})
	require.NoError(t, err)

	views := authorityIsolationViews(6)
	keys := make([]featurestate.Key, 0, len(views))
	for i, view := range views {
		key, err := featurestate.ResolveKey(view)
		require.NoError(t, err)
		keys = append(keys, key)
		record, promoted, err := coordinator.Promote(ctx, key,
			positiveProposal(session.SourceLocalIdentity, session.EvidenceCode(fmt.Sprintf("cache.authority_%d", i))),
			testStoreTime())
		require.NoError(t, err)
		require.True(t, promoted, "authority %d was refused its own cache entry", i)
		require.Equal(t, key, record.Key)
	}
	require.Equal(t, len(keys), counted.promoteCount(),
		"each authority must own exactly one durable promotion")

	warm := counted.loadCount()
	for round := range 2 {
		for i, key := range keys {
			record, found, err := coordinator.Load(ctx, key)
			require.NoError(t, err)
			require.True(t, found, "round %d authority %d (%+v) has no cached positive", round, i, key)
			require.Equal(t, session.EvidenceCode(fmt.Sprintf("cache.authority_%d", i)), record.Classification.Evidence,
				"round %d authority %d (%+v) read another authority's cached positive", round, i, key)
			require.Equal(t, key, record.Key)
		}
	}
	assert.Equal(t, warm, counted.loadCount(),
		"warm authoritative positives must be served from the process cache, one entry per proxy-owned authority")
}

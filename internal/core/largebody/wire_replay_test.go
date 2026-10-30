package largebody

import (
	"crypto/sha256"
	"io"
	"testing"
)

// testWireReplayAssessment binds an accepted assessment over body with the
// given rewrite semantics for replay-proof tests.
func testWireReplayAssessment(t *testing.T, body []byte, rewrite RewriteSemantics) (Source, Assessment) {
	t.Helper()
	src := NewMemorySource(body)
	if src.Size() != int64(len(body)) {
		t.Fatalf("source size = %d, want %d", src.Size(), len(body))
	}
	stamp, err := NewAssessmentStamp(
		"gen-replay-test",
		"profile-replay-test",
		src.Digest(),
		src.Size(),
		BodyModeIdentityJSON,
		rewrite,
		NewIdentityDigest(sha256.Sum256([]byte("canonical-identity"))),
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp failed: %v", err)
	}
	accepted, err := NewAcceptedAssessment(stamp, WireRequestFacts{}, WireDomainFacts{})
	if err != nil {
		t.Fatalf("NewAcceptedAssessment failed: %v", err)
	}
	return src, accepted
}

func readWireReplayAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	return out
}

func TestWireReplay_ProveMemorySourceReplaysExactBytes(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"hello replay"}`)
	src, accepted := testWireReplayAssessment(t, body, NewNoRewrite())

	factory, ok := ProveWireReplay(src, accepted, "gpt-4o")
	if !ok {
		t.Fatal("ProveWireReplay refused a completed memory source with no rewrite")
	}
	if !factory.Valid() {
		t.Fatal("proven factory must be valid")
	}

	first, firstLen, err := factory.NewReader()
	if err != nil {
		t.Fatalf("first NewReader failed: %v", err)
	}
	second, secondLen, err := factory.NewReader()
	if err != nil {
		t.Fatalf("second NewReader failed: %v", err)
	}
	if firstLen != int64(len(body)) || secondLen != int64(len(body)) {
		t.Fatalf("lengths = %d/%d, want %d", firstLen, secondLen, len(body))
	}
	// Readers are independent: consume the second fully before the first.
	secondBytes := readWireReplayAll(t, second)
	firstBytes := readWireReplayAll(t, first)
	if string(firstBytes) != string(body) || string(secondBytes) != string(body) {
		t.Fatalf("replay bytes differ from exact source bytes")
	}
	if err := src.Close(); err != nil {
		t.Fatalf("source close failed: %v", err)
	}
}

func TestWireReplay_ProveModelSpliceReplaysRewrittenBytes(t *testing.T) {
	t.Parallel()

	raw := `{"model":"m1","input":"hi"}`
	span := Span{Offset: 9, Length: 4} // the "m1" token
	rewrite, err := NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("NewModelTokenRewrite failed: %v", err)
	}
	src, accepted := testWireReplayAssessment(t, []byte(raw), rewrite)

	factory, ok := ProveWireReplay(src, accepted, "longer-model-name")
	if !ok {
		t.Fatal("ProveWireReplay refused a certified model-token splice")
	}
	// Hard-coded rewritten bytes: recomputing them through SpliceModelToken*
	// would exercise the same code path as the replay factory and hide a
	// splice bug from this test.
	want := []byte(`{"model":"longer-model-name","input":"hi"}`)

	for attempt := range 2 {
		rc, length, err := factory.NewReader()
		if err != nil {
			t.Fatalf("attempt %d NewReader failed: %v", attempt, err)
		}
		got := readWireReplayAll(t, rc)
		if length != int64(len(want)) {
			t.Fatalf("attempt %d length = %d, want %d", attempt, length, len(want))
		}
		if string(got) != string(want) {
			t.Fatalf("attempt %d bytes = %q, want %q", attempt, got, want)
		}
	}
}

func TestWireReplay_NoProofWithoutReplayableInput(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o"}`)
	src, accepted := testWireReplayAssessment(t, body, NewNoRewrite())

	t.Run("nil_source", func(t *testing.T) {
		t.Parallel()
		factory, ok := ProveWireReplay(nil, accepted, "gpt-4o")
		if ok || factory.Valid() {
			t.Fatal("nil source must not prove replayable")
		}
		if _, _, err := factory.NewReader(); err == nil {
			t.Fatal("invalid factory must fail NewReader")
		}
	})

	t.Run("declined_assessment", func(t *testing.T) {
		t.Parallel()
		declined, err := NewDeclinedAssessment(DeclineReasonProofUncertain)
		if err != nil {
			t.Fatalf("NewDeclinedAssessment failed: %v", err)
		}
		if factory, ok := ProveWireReplay(src, declined, "gpt-4o"); ok || factory.Valid() {
			t.Fatal("declined assessment must not prove replayable")
		}
	})

	t.Run("zero_assessment", func(t *testing.T) {
		t.Parallel()
		if factory, ok := ProveWireReplay(src, Assessment{}, "gpt-4o"); ok || factory.Valid() {
			t.Fatal("zero assessment must not prove replayable")
		}
	})

	t.Run("rewrite_without_candidate_model", func(t *testing.T) {
		t.Parallel()
		rewrite, err := NewModelTokenRewrite(Span{Offset: 9, Length: 4})
		if err != nil {
			t.Fatalf("NewModelTokenRewrite failed: %v", err)
		}
		_, rewriteAccepted := testWireReplayAssessment(t, body, rewrite)
		if factory, ok := ProveWireReplay(src, rewriteAccepted, "  "); ok || factory.Valid() {
			t.Fatal("model rewrite without a candidate model must not prove replayable")
		}
	})
}

func TestWireReplay_BoundedSnapshotReplaysExactBytes(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"snapshot"}`)

	t.Run("over_budget_rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := NewWireReplayFromBytes(body, int64(len(body)-1)); err == nil {
			t.Fatal("over-budget snapshot must be rejected, never buffered unbounded")
		}
	})

	t.Run("exact_bytes_and_defensive_copy", func(t *testing.T) {
		t.Parallel()
		mutable := append([]byte(nil), body...)
		factory, err := NewWireReplayFromBytes(mutable, int64(len(mutable)))
		if err != nil {
			t.Fatalf("NewWireReplayFromBytes failed: %v", err)
		}
		if !factory.Valid() {
			t.Fatal("snapshot factory must be valid")
		}
		// Mutating the caller's slice after the snapshot must not change replays.
		for i := range mutable {
			mutable[i] = 'x'
		}
		for attempt := range 2 {
			rc, length, err := factory.NewReader()
			if err != nil {
				t.Fatalf("attempt %d NewReader failed: %v", attempt, err)
			}
			got := readWireReplayAll(t, rc)
			if length != int64(len(body)) || string(got) != string(body) {
				t.Fatalf("attempt %d = %q len %d, want exact snapshot bytes", attempt, got, length)
			}
		}
	})

	t.Run("invalid_budget_rejected", func(t *testing.T) {
		t.Parallel()
		// An empty body isolates the non-positive-budget rule: with bytes to
		// buffer, the over-budget check rejects a zero budget on its own.
		if _, err := NewWireReplayFromBytes([]byte{}, 0); err == nil {
			t.Fatal("zero snapshot budget must be rejected even for an empty body")
		}
		if _, err := NewWireReplayFromBytes(nil, -1); err == nil {
			t.Fatal("negative snapshot budget must be rejected even for an empty body")
		}
	})
}

func TestWireReplay_InvalidFactoryFailsClosed(t *testing.T) {
	t.Parallel()

	var zero WireReplayFactory
	if zero.Valid() {
		t.Fatal("zero factory must be invalid")
	}
	if _, _, err := zero.NewReader(); err == nil {
		t.Fatal("zero factory NewReader must fail closed")
	}
	if _, ok := ProveWireReplay(nil, Assessment{}, ""); ok {
		t.Fatal("empty proof inputs must not prove replayable")
	}
}

// TestWireReplay_NilConstructorFailsClosed pins the constructor's fail-closed
// rule: a factory built without a replay constructor is invalid and refuses to
// open readers. Constructor delegation itself is exercised end to end by the
// ProveWireReplay tests above.
func TestWireReplay_NilConstructorFailsClosed(t *testing.T) {
	t.Parallel()

	factory := NewWireReplayFactory(nil)
	if factory.Valid() {
		t.Fatal("nil constructor must yield an invalid factory")
	}
	if _, _, err := factory.NewReader(); err == nil {
		t.Fatal("nil-constructor factory must fail closed on NewReader")
	}
}

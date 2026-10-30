package openresponses_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/openresponses"
)

func TestVirtualClock_Determinism(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	clock := openresponses.NewVirtualClock(start)

	if !clock.Now().Equal(start) {
		t.Fatalf("clock.Now() = %v, want %v", clock.Now(), start)
	}

	clock.Advance(5 * time.Minute)
	want := start.Add(5 * time.Minute)
	if !clock.Now().Equal(want) {
		t.Fatalf("after advance: clock.Now() = %v, want %v", clock.Now(), want)
	}

	setT := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	clock.Set(setT)
	if !clock.Now().Equal(setT) {
		t.Fatalf("after set: clock.Now() = %v, want %v", clock.Now(), setT)
	}
}

func TestBoundedCapture_OverflowAndRedaction(t *testing.T) {
	t.Parallel()

	redactHeaders := []string{"Authorization", "X-Api-Key"}
	capture := openresponses.NewBoundedCapture(2, redactHeaders)

	h1 := make(http.Header)
	h1.Set("Authorization", "Bearer secret-token")
	h1.Set("Content-Type", "application/json")

	err := capture.Capture(openresponses.RequestObservation{
		ID:        "req-1",
		Method:    "POST",
		URLPath:   "/openresponses/v1/responses",
		Headers:   h1,
		Body:      []byte(`{"model":"gpt-4o"}`),
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("capture req-1 unexpected error: %v", err)
	}

	obs := capture.Observations()
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	if obs[0].Headers.Get("Authorization") != "[REDACTED]" {
		t.Fatalf("expected Authorization header to be [REDACTED], got %q", obs[0].Headers.Get("Authorization"))
	}
	if !obs[0].Redacted {
		t.Fatalf("expected Redacted flag to be true")
	}
	if obs[0].Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type header modified unexpectedly")
	}

	// Capture second item
	err = capture.Capture(openresponses.RequestObservation{
		ID:      "req-2",
		Method:  "POST",
		URLPath: "/openresponses/v1/responses",
	})
	if err != nil {
		t.Fatalf("capture req-2 unexpected error: %v", err)
	}

	// Capture third item should trigger overflow error
	err = capture.Capture(openresponses.RequestObservation{
		ID:      "req-3",
		Method:  "POST",
		URLPath: "/openresponses/v1/responses",
	})
	if err == nil {
		t.Fatal("expected overflow error on 3rd capture, got nil")
	}
	if capture.OverflowCount() != 1 {
		t.Fatalf("expected overflow count 1, got %d", capture.OverflowCount())
	}
}

func TestCleanup_Idempotence(t *testing.T) {
	t.Parallel()

	calls := 0
	cleanup := openresponses.NewTestCleanup(func() error {
		calls++
		return nil
	})

	if cleanup.IsClosed() {
		t.Fatal("cleanup should not be closed initially")
	}

	err := cleanup.Close()
	if err != nil {
		t.Fatalf("first Close unexpected error: %v", err)
	}
	if !cleanup.IsClosed() {
		t.Fatal("cleanup should be closed after Close()")
	}

	// Second Close call should be idempotent
	err = cleanup.Close()
	if err != nil {
		t.Fatalf("second Close unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected onClose callback executed exactly once, got %d", calls)
	}
}

func TestScriptContract_SequenceAndExhaustion(t *testing.T) {
	t.Parallel()

	steps := []openresponses.ScriptStep{
		{Name: "step-1", MatchMethod: "POST", MatchPath: "/v1/responses", ResponseStatus: 200, ResponseBody: []byte(`{"id":"resp-1"}`)},
		{Name: "step-2", MatchMethod: "POST", MatchPath: "/v1/responses", ResponseStatus: 200, ResponseBody: []byte(`{"id":"resp-2"}`)},
	}

	script := openresponses.NewScriptContract(steps)

	if script.Remaining() != 2 {
		t.Fatalf("expected 2 remaining steps, got %d", script.Remaining())
	}

	s1, err := script.NextStep()
	if err != nil {
		t.Fatalf("unexpected error on step 1: %v", err)
	}
	if s1.Name != "step-1" {
		t.Fatalf("got step name %q, want 'step-1'", s1.Name)
	}

	s2, err := script.NextStep()
	if err != nil {
		t.Fatalf("unexpected error on step 2: %v", err)
	}
	if s2.Name != "step-2" {
		t.Fatalf("got step name %q, want 'step-2'", s2.Name)
	}

	// Next step should fail (script exhausted)
	_, err = script.NextStep()
	if err == nil {
		t.Fatal("expected error on exhausted script step, got nil")
	}
}

func TestEvidence_Validation(t *testing.T) {
	t.Parallel()

	ev := openresponses.TestEvidenceDescriptor{
		ScenarioID:    "openresponses-json-basic-v1",
		Outcome:       "lossless",
		TestArtifacts: []string{"internal/testkit/openresponses/contracts_test.go"},
	}

	if err := ev.Validate(); err != nil {
		t.Fatalf("valid evidence descriptor rejected: %v", err)
	}

	// Missing scenario ID
	invalidEv := ev
	invalidEv.ScenarioID = ""
	if err := invalidEv.Validate(); err == nil {
		t.Fatal("expected error for empty scenario ID, got nil")
	}

	// Invalid outcome
	invalidEv2 := ev
	invalidEv2.Outcome = "unknown_outcome"
	if err := invalidEv2.Validate(); err == nil {
		t.Fatal("expected error for invalid outcome, got nil")
	}

	// Missing artifacts
	invalidEv3 := ev
	invalidEv3.TestArtifacts = nil
	if err := invalidEv3.Validate(); err == nil {
		t.Fatal("expected error for missing artifacts, got nil")
	}
}

func TestBoundedCapture_CaseInsensitiveNonCanonicalHeaderRedaction(t *testing.T) {
	t.Parallel()

	redactHeaders := []string{"authorization", "x-api-key"}
	capture := openresponses.NewBoundedCapture(10, redactHeaders)

	h := make(http.Header)
	// Intentionally use non-canonical / lowercase key in map
	h["authorization"] = []string{"Bearer secret-token-123"}
	h["X-API-KEY"] = []string{"secret-key-456"}
	h.Set("Content-Type", "application/json")

	err := capture.Capture(openresponses.RequestObservation{
		ID:        "req-redact",
		Method:    "POST",
		URLPath:   "/v1/responses",
		Headers:   h,
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obs := capture.Observations()
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}

	// Verify header redaction works regardless of key casing
	if obs[0].Headers.Get("Authorization") != "[REDACTED]" {
		t.Errorf("Authorization header got %q, want [REDACTED]", obs[0].Headers.Get("Authorization"))
	}
	if obs[0].Headers.Get("X-Api-Key") != "[REDACTED]" {
		t.Errorf("X-Api-Key header got %q, want [REDACTED]", obs[0].Headers.Get("X-Api-Key"))
	}
	// Crucially check raw map keys to ensure non-canonical keys carrying secrets were deleted/redacted
	for k, vals := range obs[0].Headers {
		for _, v := range vals {
			if v != "[REDACTED]" && v != "application/json" {
				t.Errorf("leaked secret in unredacted header key %q: %q", k, v)
			}
		}
	}
}

func TestBoundedCapture_NoMutableAliasSharing(t *testing.T) {
	t.Parallel()

	capture := openresponses.NewBoundedCapture(10, nil)

	body := []byte(`{"model":"gpt-4o"}`)
	h := make(http.Header)
	h.Set("X-Custom", "val1")

	err := capture.Capture(openresponses.RequestObservation{
		ID:      "req-immut",
		Body:    body,
		Headers: h,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutate input source buffers after Capture
	body[0] = 'X'
	h.Set("X-Custom", "MUTATED")

	obs1 := capture.Observations()
	if string(obs1[0].Body) != `{"model":"gpt-4o"}` {
		t.Errorf("captured body was mutated by caller: got %q", string(obs1[0].Body))
	}
	if obs1[0].Headers.Get("X-Custom") != "val1" {
		t.Errorf("captured headers was mutated by caller: got %q", obs1[0].Headers.Get("X-Custom"))
	}

	// Mutate returned observation slice
	obs1[0].Body[0] = 'Z'
	obs1[0].Headers.Set("X-Custom", "MUTATED_AGAIN")

	obs2 := capture.Observations()
	if string(obs2[0].Body) != `{"model":"gpt-4o"}` {
		t.Errorf("internal captured body was mutated via returned observation: got %q", string(obs2[0].Body))
	}
	if obs2[0].Headers.Get("X-Custom") != "val1" {
		t.Errorf("internal captured headers was mutated via returned observation: got %q", obs2[0].Headers.Get("X-Custom"))
	}
}

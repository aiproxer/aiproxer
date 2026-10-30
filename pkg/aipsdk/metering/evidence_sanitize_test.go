package metering

import (
	"strings"
	"testing"
	"time"
)

// Task 16.3A RED contract: economic safe-evidence screening. Only canonical
// typed quantities, charges, bounded provenance/identity refs, safe hashes
// and explicitly authorized raw numeric lexemes/paths may persist or be
// returned. Prompts, tool args, headers/cookies/auth, secrets, ciphertext,
// arbitrary provider JSON and request bodies must be rejected at the
// public/normalization boundary and never echoed in diagnostics or errors.

func sanitizeTestField(path, lexeme string) SafeEvidenceField {
	return SafeEvidenceField{
		Path: path, Lexeme: lexeme, Present: lexeme != "",
		Acquisition: AcquisitionProviderResponse,
	}
}

func TestSafeEvidenceRejectsSecretLexemes(t *testing.T) {
	t.Parallel()
	secrets := []struct {
		name   string
		lexeme string
	}{
		{name: "bearer token", lexeme: "Bearer sk-ant-secret-value"},
		{name: "basic auth", lexeme: "Basic dXNlcjpwYXNz"},
		{name: "provider key", lexeme: "sk-ant-api03-s3cr3t-v4lue"},
		{name: "openai key", lexeme: "sk-proj-abcdef123456"},
		{name: "slack token", lexeme: "xoxb-1234-secret-token"},
		{name: "github token", lexeme: "ghp_deadbeefcafe1234567890"},
		{name: "google key", lexeme: "AIzaSyD-secret-value"},
		{name: "aws key id", lexeme: "AKIAIOSFODNN7EXAMPLE"},
		{name: "pem block", lexeme: "-----BEGIN PRIVATE KEY-----"},
		{name: "cookie shape", lexeme: "session=abc123; Path=/; HttpOnly"},
		{name: "password assignment", lexeme: "password=hunter2"},
		{name: "client secret", lexeme: "client_secret=topsecret"},
	}
	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			field := sanitizeTestField("$.usage.input_tokens", tc.lexeme)
			if err := field.Validate(); err == nil {
				t.Fatalf("secret-bearing lexeme %q was accepted", tc.lexeme)
			}
		})
	}
}

func TestSafeEvidenceRejectsNonNumericAmountLexemes(t *testing.T) {
	t.Parallel()
	nonNumeric := []struct {
		name   string
		path   string
		lexeme string
	}{
		{name: "prompt text as tokens", path: "$.usage.input_tokens", lexeme: "Ignore all previous instructions"},
		{name: "tool args as tokens", path: "$.usage.output_tokens", lexeme: `{"tool":"search","query":"x"}`},
		{name: "sentence as amount", path: "$.billing.amount", lexeme: "about twelve dollars"},
		{name: "auth header as usage", path: "x-usage-input-tokens", lexeme: "prompt injection payload"},
		{name: "free text currency", path: "$.billing.currency", lexeme: "dollars"},
		{name: "spaced identifier", path: "$.charge.id", lexeme: "charge 123"},
		{name: "semicolon identifier", path: "x-request-id", lexeme: "req; DROP TABLE"},
	}
	for _, tc := range nonNumeric {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			field := sanitizeTestField(tc.path, tc.lexeme)
			if err := field.Validate(); err == nil {
				t.Fatalf("non-economic lexeme %q at %q was accepted", tc.lexeme, tc.path)
			}
		})
	}
}

func TestSafeEvidenceAcceptsCanonicalEconomicLexemes(t *testing.T) {
	t.Parallel()
	valid := []struct {
		name   string
		path   string
		lexeme string
	}{
		{name: "integer tokens", path: "$.usage.input_tokens", lexeme: "100"},
		{name: "decimal amount", path: "$.billing.amount", lexeme: "1.32"},
		{name: "negative correction", path: "$.charge.amount", lexeme: "-2.50"},
		{name: "scientific amount", path: "$.cost.amount", lexeme: "1.25e3"},
		{name: "currency code", path: "$.billing.currency", lexeme: "USD"},
		{name: "charge id", path: "$.charge.id", lexeme: "charge-abc-123"},
		{name: "request id header", path: "x-request-id", lexeme: "req_01J9Z8X7Y6"},
		{name: "reset epoch", path: "$.quota.reset", lexeme: "2026-09-01T00:00:00Z"},
		{name: "lifetime qualifier", path: "$.cache.lifetime", lexeme: "5m"},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			field := sanitizeTestField(tc.path, tc.lexeme)
			if err := field.Validate(); err != nil {
				t.Fatalf("canonical economic lexeme %q at %q rejected: %v", tc.lexeme, tc.path, err)
			}
		})
	}
}

func TestSafeEvidenceRejectsOverLimitFields(t *testing.T) {
	t.Parallel()
	oversized := sanitizeTestField("$.usage.input_tokens", strings.Repeat("9", MaxSafeEvidenceFieldBytes+1))
	if err := oversized.Validate(); err == nil {
		t.Fatal("over-limit lexeme was accepted")
	}
	longPath := sanitizeTestField("$.usage.input_tokens."+strings.Repeat("a", MaxSafeEvidenceFieldBytes), "100")
	if err := longPath.Validate(); err == nil {
		t.Fatal("over-limit path was accepted")
	}
	longMarker := sanitizeTestField("$.usage.input_tokens", "100")
	longMarker.Sanitizer = "aip-normalizer/" + strings.Repeat("v", MaxSanitizerMarkerBytes)
	if err := longMarker.Validate(); err == nil {
		t.Fatal("over-limit sanitizer marker was accepted")
	}
	now := time.Unix(1_700_040_000, 0).UTC()
	subject := SubjectRef{Kind: SubjectBLeg, StoreID: "store-sanitize", BLegID: "b-1"}
	crowded := Observation{
		Version: ObservationVersionV2, ID: "obs-crowded", SourceEventKey: "event-1",
		Revision: 1, StreamID: "stream-1", Sequence: 1,
		Origin: OriginProvider, Acquisition: AcquisitionProviderResponse, Authority: AuthorityObservedClaim,
		Perspective: PerspectiveOperator, Boundary: BoundaryBackendEgress, Lifecycle: LifecycleBackendAttempt,
		Subject: subject, Correlation: CorrelationV2{StoreID: "store-sanitize", BLegID: "b-1"},
		Semantics: SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "sanitize.test.v1",
		Measures: []Measure{{Key: ComponentKey{
			Direction: DirectionInput, Component: ComponentInputToken, Unit: UnitToken, SchemaID: DefaultInclusionSchemaID,
		}, Value: &Decimal{Coefficient: "1"}, Quality: QualityObserved}},
	}
	for i := 0; i <= MaxObservationEvidence; i++ {
		crowded.Evidence = append(crowded.Evidence, SafeEvidenceField{
			Path: "$.usage.input_tokens", Lexeme: "1", Present: true,
			Acquisition: AcquisitionProviderResponse,
		})
	}
	if err := crowded.Validate(); err == nil {
		t.Fatal("over-count evidence was accepted")
	}
}

func TestSafeEvidenceRejectsTraversalLocations(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"$..usage.input_tokens",
		"$.usage../../etc/passwd",
		"..\\windows\\system32",
		"$.usage.input_tokens\x00",
	} {
		field := sanitizeTestField(path, "100")
		if err := field.Validate(); err == nil {
			t.Fatalf("traversal location %q was accepted", path)
		}
	}
}

func sanitizeTestObservation(t *testing.T, sanitizer, lexeme string) Observation {
	t.Helper()
	now := time.Unix(1_700_040_000, 0).UTC()
	subject := SubjectRef{Kind: SubjectBLeg, StoreID: "store-sanitize", BLegID: "b-1"}
	field := sanitizeTestField("$.usage.input_tokens", lexeme)
	field.Sanitizer = sanitizer
	observation := Observation{
		Version: ObservationVersionV2, ID: "obs-sanitize", SourceEventKey: "event-1",
		Revision: 1, StreamID: "stream-1", Sequence: 1,
		Origin: OriginProvider, Acquisition: AcquisitionProviderResponse, Authority: AuthorityObservedClaim,
		Perspective: PerspectiveOperator, Boundary: BoundaryBackendEgress, Lifecycle: LifecycleBackendAttempt,
		Subject: subject, Correlation: CorrelationV2{StoreID: "store-sanitize", BLegID: "b-1"},
		Semantics: SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "sanitize.test.v1",
		Measures: []Measure{{Key: ComponentKey{
			Direction: DirectionInput, Component: ComponentInputToken, Unit: UnitToken, SchemaID: DefaultInclusionSchemaID,
		}, Value: &Decimal{Coefficient: "100"}, Quality: QualityObserved}},
		Evidence: []SafeEvidenceField{field},
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("sanitize fixture invalid: %v", err)
	}
	return observation
}

func TestSafeEvidenceSanitizerMarker(t *testing.T) {
	t.Parallel()
	marked := sanitizeTestObservation(t, "aip-normalizer/v1", "100")
	unmarked := sanitizeTestObservation(t, "", "100")
	if marked.Fingerprint() == unmarked.Fingerprint() {
		t.Fatal("sanitizer marker must participate in the deterministic content hash")
	}
	for _, marker := range []string{"", " ", "aip normalizer/v1", "aip-normalizer", "/v1", "aip-normalizer/v1/extra", strings.Repeat("a", 129)} {
		bad := sanitizeTestField("$.usage.input_tokens", "100")
		bad.Sanitizer = marker
		if marker == "" {
			continue
		}
		if err := bad.Validate(); err == nil {
			t.Fatalf("malformed sanitizer marker %q was accepted", marker)
		}
	}
}

func TestSafeEvidenceHashBindsSanitizedMaterial(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_040_000, 0).UTC()
	subject := SubjectRef{Kind: SubjectBLeg, StoreID: "store-sanitize", BLegID: "b-1"}
	base := Observation{
		Version: ObservationVersionV2, ID: "obs-sanitize", SourceEventKey: "event-1",
		Revision: 1, StreamID: "stream-1", Sequence: 1,
		Origin: OriginProvider, Acquisition: AcquisitionProviderResponse, Authority: AuthorityObservedClaim,
		Perspective: PerspectiveOperator, Boundary: BoundaryBackendEgress, Lifecycle: LifecycleBackendAttempt,
		Subject: subject, Correlation: CorrelationV2{StoreID: "store-sanitize", BLegID: "b-1"},
		Semantics: SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "sanitize.test.v1",
		Measures: []Measure{{Key: ComponentKey{
			Direction: DirectionInput, Component: ComponentInputToken, Unit: UnitToken, SchemaID: DefaultInclusionSchemaID,
		}, Value: &Decimal{Coefficient: "100"}, Quality: QualityObserved}},
		Evidence: []SafeEvidenceField{sanitizeTestField("$.usage.input_tokens", "100")},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base observation invalid: %v", err)
	}
	first := base.Fingerprint()
	second := base.Fingerprint()
	if first == "" || first != second {
		t.Fatal("fingerprint must be deterministic and non-empty")
	}
	mutated := base.Clone()
	mutated.Evidence[0].Lexeme = "101"
	if mutated.Fingerprint() == first {
		t.Fatal("mutated lexeme must change the content hash (replay conflict detection)")
	}
	if _, err := mutated.Canonical(); err != nil {
		t.Fatalf("mutated observation must stay structurally valid: %v", err)
	}
}

package sessionclassification_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// TestObservationPayloadsCarryOnlyBoundedClosedEnumFields is the structural half
// of requirement 9.4/9.5: an observation value has no field that could hold a
// session ID, an A-leg, a raw User-Agent, a filename, a path, a prompt, client
// metadata, or a remote response body, so none of them can reach a label or a
// log payload.
func TestObservationPayloadsCarryOnlyBoundedClosedEnumFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value any
		want  []struct {
			name   string
			typeOf reflect.Type
		}
	}{
		{
			name:  "EvaluationObservation",
			value: sessionclassification.EvaluationObservation{},
			want: []struct {
				name   string
				typeOf reflect.Type
			}{
				{name: "Mode", typeOf: reflect.TypeFor[sessionclassification.Mode]()},
				{name: "Outcome", typeOf: reflect.TypeFor[sessionclassification.EvaluationOutcome]()},
			},
		},
		{
			name:  "TransitionObservation",
			value: sessionclassification.TransitionObservation{},
			want: []struct {
				name   string
				typeOf reflect.Type
			}{
				{name: "Source", typeOf: reflect.TypeFor[session.ClassificationSource]()},
				{name: "Confidence", typeOf: reflect.TypeFor[session.ConfidenceBand]()},
				{name: "Evidence", typeOf: reflect.TypeFor[session.EvidenceCode]()},
				{name: "Revision", typeOf: reflect.TypeFor[uint64]()},
			},
		},
		{
			name:  "RemoteObservation",
			value: sessionclassification.RemoteObservation{},
			want: []struct {
				name   string
				typeOf reflect.Type
			}{
				{name: "Outcome", typeOf: reflect.TypeFor[sessionclassification.RemoteOutcome]()},
				{name: "Latency", typeOf: reflect.TypeFor[time.Duration]()},
			},
		},
		{
			name:  "StoreObservation",
			value: sessionclassification.StoreObservation{},
			want: []struct {
				name   string
				typeOf reflect.Type
			}{
				{name: "Operation", typeOf: reflect.TypeFor[sessionclassification.StoreOperation]()},
				{name: "Outcome", typeOf: reflect.TypeFor[sessionclassification.StoreOutcome]()},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tc.value)
			if typ.NumField() != len(tc.want) {
				t.Fatalf("%s has %d fields, want exactly %d bounded fields", tc.name, typ.NumField(), len(tc.want))
			}
			for i, want := range tc.want {
				field := typ.Field(i)
				if field.Name != want.name || field.Type != want.typeOf {
					t.Fatalf("%s field %d = %s %s, want %s %s", tc.name, i, field.Name, field.Type, want.name, want.typeOf)
				}
				// No field may itself be a container: a slice, map, or interface
				// could smuggle raw content into a bounded observation.
				switch field.Type.Kind() {
				case reflect.Slice, reflect.Map, reflect.Interface, reflect.Array, reflect.Pointer, reflect.Func:
					t.Fatalf("%s.%s is a %s, which could carry unbounded content", tc.name, field.Name, field.Type.Kind())
				}
				if strings.Contains(strings.ToLower(field.Name), "body") ||
					strings.Contains(strings.ToLower(field.Name), "user") ||
					strings.Contains(strings.ToLower(field.Name), "session") ||
					strings.Contains(strings.ToLower(field.Name), "aleg") ||
					strings.Contains(strings.ToLower(field.Name), "path") ||
					strings.Contains(strings.ToLower(field.Name), "header") ||
					strings.Contains(strings.ToLower(field.Name), "prompt") ||
					strings.Contains(strings.ToLower(field.Name), "message") {
					t.Fatalf("%s.%s is named like a content-bearing field", tc.name, field.Name)
				}
			}
		})
	}
}

// TestClosedObservationVocabulariesAreFrozen pins the exact closed label
// vocabularies. Adding a value is a deliberate review event; every exporter and
// operator dashboard depends on these lists staying finite.
func TestClosedObservationVocabulariesAreFrozen(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		got   []string
		allow func(string) bool
	}{
		{name: "modes", got: sessionclassification.ObservationModes(), allow: func(value string) bool {
			return sessionclassification.ModeAllowed(sessionclassification.Mode(value))
		}},
		{name: "evaluation outcomes", got: sessionclassification.EvaluationOutcomes(), allow: func(value string) bool {
			return sessionclassification.EvaluationOutcomeAllowed(sessionclassification.EvaluationOutcome(value))
		}},
		{name: "remote outcomes", got: sessionclassification.RemoteOutcomes(), allow: func(value string) bool {
			return sessionclassification.RemoteOutcomeAllowed(sessionclassification.RemoteOutcome(value))
		}},
		{name: "store operations", got: sessionclassification.StoreOperations(), allow: func(value string) bool {
			return sessionclassification.StoreOperationAllowed(sessionclassification.StoreOperation(value))
		}},
		{name: "store outcomes", got: sessionclassification.StoreOutcomes(), allow: func(value string) bool {
			return sessionclassification.StoreOutcomeAllowed(sessionclassification.StoreOutcome(value))
		}},
		{name: "evidence codes", got: sessionclassification.BoundedEvidenceCodes(), allow: func(value string) bool {
			return sessionclassification.EvidenceCodeAllowed(session.EvidenceCode(value))
		}},
	}
	want := map[string][]string{
		"modes":               {"heuristic", "hybrid", "jev"},
		"evaluation outcomes": {"excluded", "no_authority", "preserved", "promoted", "remote_error", "remote_skipped", "remote_timeout", "restored", "state_unavailable", "unknown"},
		"remote outcomes":     {"below_threshold", "budget_exhausted", "lease_busy", "malformed", "network_error", "positive", "rate_limited", "server_error", "skipped", "timeout"},
		"store operations":    {"load", "promote", "remote_claim", "remote_complete"},
		"store outcomes":      {"applied", "denied", "error", "hit", "miss", "unchanged"},
		"evidence codes": {
			"client_family.codex", "client_family.droid", "client_family.hermes", "client_family.opencode",
			"client_family.pi", "client_family.roo", "remote.above_threshold",
			"tooling.distinct_coding_cluster", "tooling.project_marker_cluster",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantValues := want[tc.name]
			if len(tc.got) != len(wantValues) {
				t.Fatalf("%s vocabulary = %v, want %v", tc.name, tc.got, wantValues)
			}
			for i, value := range tc.got {
				if value != wantValues[i] {
					t.Fatalf("%s vocabulary[%d] = %q, want %q", tc.name, i, value, wantValues[i])
				}
				if !tc.allow(value) {
					t.Fatalf("%s vocabulary member %q is rejected by its own validator", tc.name, value)
				}
			}
		})
	}
}

// hostileLabelValues are the values that must never be able to reach a metric
// label or a log payload.
var hostileLabelValues = []string{
	"sess-secret-session-id-9f3a",
	"a-leg-secret-aleg-id-9f3a",
	"codex_cli_rs/9.9.9",
	"Mozilla/5.0 (X11; Linux x86_64) OpenCode/2.4.0",
	"/home/user/private/repository/main.go",
	"main.go",
	"please refactor this function to use channels",
	"```go\nfunc main() {}\n```",
	"{\"result\":\"positive\",\"debug\":\"Bearer sk-live-abcdef\"}",
	"vendor-result-{\"model\":\"gpt-9\",\"score\":0.97}",
	"anthropic-beta-token-xyz",
	"",
	"HEURISTIC",
	" local_identity",
	"client_family.codex ",
	strings.Repeat("x", 4096),
}

func TestObservationValidatorsRejectEveryValueOutsideTheClosedEnums(t *testing.T) {
	t.Parallel()

	for _, hostile := range hostileLabelValues {
		if sessionclassification.ModeAllowed(sessionclassification.Mode(hostile)) {
			t.Errorf("ModeAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.EvaluationOutcomeAllowed(sessionclassification.EvaluationOutcome(hostile)) {
			t.Errorf("EvaluationOutcomeAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.RemoteOutcomeAllowed(sessionclassification.RemoteOutcome(hostile)) {
			t.Errorf("RemoteOutcomeAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.StoreOperationAllowed(sessionclassification.StoreOperation(hostile)) {
			t.Errorf("StoreOperationAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.StoreOutcomeAllowed(sessionclassification.StoreOutcome(hostile)) {
			t.Errorf("StoreOutcomeAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.EvidenceCodeAllowed(session.EvidenceCode(hostile)) {
			t.Errorf("EvidenceCodeAllowed accepted hostile value %q", hostile)
		}
		if sessionclassification.ValidEvaluationObservation(sessionclassification.EvaluationObservation{
			Mode:    sessionclassification.ModeHeuristic,
			Outcome: sessionclassification.EvaluationOutcome(hostile),
		}) {
			t.Errorf("ValidEvaluationObservation accepted hostile outcome %q", hostile)
		}
		if sessionclassification.ValidTransitionObservation(sessionclassification.TransitionObservation{
			Source:     session.ClassificationSource(hostile),
			Confidence: session.ConfidenceHigh,
			Evidence:   sessionclassification.EvidenceCodeCodex,
			Revision:   1,
		}) {
			t.Errorf("ValidTransitionObservation accepted hostile source %q", hostile)
		}
		if sessionclassification.ValidRemoteObservation(sessionclassification.RemoteObservation{
			Outcome: sessionclassification.RemoteOutcome(hostile),
		}) {
			t.Errorf("ValidRemoteObservation accepted hostile outcome %q", hostile)
		}
		if sessionclassification.ValidStoreObservation(sessionclassification.StoreObservation{
			Operation: sessionclassification.StoreOperation(hostile),
			Outcome:   sessionclassification.StoreOutcomeMiss,
		}) {
			t.Errorf("ValidStoreObservation accepted hostile operation %q", hostile)
		}
	}
}

// TestTransitionObservationValidationIsTotalOverTheSnapshotVocabulary proves
// requirement 9.1: a transition observation is accepted exactly for the bounded
// positive snapshots the SDK classifies as coding_agent.
func TestTransitionObservationValidationIsTotalOverTheSnapshotVocabulary(t *testing.T) {
	t.Parallel()

	for _, source := range []session.ClassificationSource{
		session.SourceLocalIdentity, session.SourceLocalTooling, session.SourceRemote,
	} {
		for _, raw := range sessionclassification.BoundedEvidenceCodes() {
			code := session.EvidenceCode(raw)
			observation := sessionclassification.TransitionObservation{
				Source:     source,
				Confidence: session.ConfidenceHigh,
				Evidence:   code,
				Revision:   3,
			}
			if !sessionclassification.ValidTransitionObservation(observation) {
				t.Fatalf("bounded transition %s/%s was rejected", source, code)
			}
			snapshot := observation.Snapshot()
			if err := snapshot.Validate(); err != nil {
				t.Fatalf("transition projection %+v is not a valid bounded snapshot: %v", snapshot, err)
			}
			if snapshot.Kind != session.KindCodingAgent || snapshot.Source != source ||
				snapshot.Confidence != session.ConfidenceHigh || snapshot.Evidence != code || snapshot.Revision != 3 {
				t.Fatalf("transition projection = %+v, want the observed bounded snapshot", snapshot)
			}
		}
	}

	invalid := []sessionclassification.TransitionObservation{
		{},
		{Source: session.SourceLocalIdentity, Confidence: session.ConfidenceHigh, Evidence: sessionclassification.EvidenceCodeCodex},
		{Source: session.SourceLocalIdentity, Confidence: session.ConfidenceBand("high "), Evidence: sessionclassification.EvidenceCodeCodex, Revision: 1},
		{Source: session.ClassificationSource("vendor"), Confidence: session.ConfidenceHigh, Evidence: sessionclassification.EvidenceCodeCodex, Revision: 1},
		{Source: session.SourceLocalIdentity, Confidence: session.ConfidenceHigh, Evidence: session.EvidenceCode("vendor.raw_body"), Revision: 1},
		{Source: session.SourceLocalIdentity, Confidence: session.ConfidenceHigh, Evidence: sessionclassification.EvidenceCodeCodex, Revision: sessionclassification.MaxObservedRevision + 1},
	}
	for i, observation := range invalid {
		if sessionclassification.ValidTransitionObservation(observation) {
			t.Errorf("invalid transition[%d] %+v was accepted", i, observation)
		}
	}
}

// TestRemoteObservationValidationBoundsLatency keeps requirement 9.3 honest: a
// bounded latency sample is required alongside the bounded outcome, and an
// unbounded or non-finite sample is rejected instead of exported.
func TestRemoteObservationValidationBoundsLatency(t *testing.T) {
	t.Parallel()

	valid := sessionclassification.RemoteObservation{Outcome: sessionclassification.RemoteTimeout, Latency: 750 * time.Millisecond}
	if !sessionclassification.ValidRemoteObservation(valid) {
		t.Fatalf("bounded remote observation %+v was rejected", valid)
	}
	invalid := []time.Duration{
		-time.Nanosecond,
		time.Duration(1<<63 - 1),
		sessionclassification.MaxRemoteObservationLatency + time.Nanosecond,
	}
	for _, latency := range invalid {
		observation := sessionclassification.RemoteObservation{Outcome: sessionclassification.RemotePositive, Latency: latency}
		if sessionclassification.ValidRemoteObservation(observation) {
			t.Errorf("remote observation with latency %s was accepted", latency)
		}
	}
}

func FuzzObservationValidationIsExactlyTheClosedEnumMembership(f *testing.F) {
	f.Add("heuristic", "unknown", "codex_cli_rs/1.2.3", "load", "miss", "timeout")
	f.Add("jev", "promoted", "remote.above_threshold", "promote", "applied", "positive")
	f.Add("hybrid", "excluded", "tooling.project_marker_cluster", "remote_claim", "denied", "lease_busy")
	f.Add("", "", "", "", "", "")
	f.Add("MODE", "unknown", "client_family.codex", "load", "miss", "malformed")

	f.Fuzz(func(t *testing.T, mode, outcome, evidence, operation, storeOutcome, remoteOutcome string) {
		if len(mode) > 64 || len(outcome) > 256 || len(evidence) > 256 || len(operation) > 64 || len(storeOutcome) > 64 || len(remoteOutcome) > 256 {
			t.Skip()
		}
		gotMode := sessionclassification.ModeAllowed(sessionclassification.Mode(mode))
		if gotMode != slicesContains(sessionclassification.ObservationModes(), mode) {
			t.Fatalf("ModeAllowed(%q) = %t, disagreeing with the published vocabulary", mode, gotMode)
		}
		gotOutcome := sessionclassification.EvaluationOutcomeAllowed(sessionclassification.EvaluationOutcome(outcome))
		if gotOutcome != slicesContains(sessionclassification.EvaluationOutcomes(), outcome) {
			t.Fatalf("EvaluationOutcomeAllowed(%q) = %t, disagreeing with the published vocabulary", outcome, gotOutcome)
		}
		gotCode := sessionclassification.EvidenceCodeAllowed(session.EvidenceCode(evidence))
		if gotCode != slicesContains(sessionclassification.BoundedEvidenceCodes(), evidence) {
			t.Fatalf("EvidenceCodeAllowed(%q) = %t, disagreeing with the published vocabulary", evidence, gotCode)
		}
		gotOperation := sessionclassification.StoreOperationAllowed(sessionclassification.StoreOperation(operation))
		if gotOperation != slicesContains(sessionclassification.StoreOperations(), operation) {
			t.Fatalf("StoreOperationAllowed(%q) = %t, disagreeing with the published vocabulary", operation, gotOperation)
		}
		gotStoreOutcome := sessionclassification.StoreOutcomeAllowed(sessionclassification.StoreOutcome(storeOutcome))
		if gotStoreOutcome != slicesContains(sessionclassification.StoreOutcomes(), storeOutcome) {
			t.Fatalf("StoreOutcomeAllowed(%q) = %t, disagreeing with the published vocabulary", storeOutcome, gotStoreOutcome)
		}
		gotRemote := sessionclassification.RemoteOutcomeAllowed(sessionclassification.RemoteOutcome(remoteOutcome))
		if gotRemote != slicesContains(sessionclassification.RemoteOutcomes(), remoteOutcome) {
			t.Fatalf("RemoteOutcomeAllowed(%q) = %t, disagreeing with the published vocabulary", remoteOutcome, gotRemote)
		}
	})
}

func slicesContains(values []string, want string) bool {
	return slices.Contains(values, want)
}

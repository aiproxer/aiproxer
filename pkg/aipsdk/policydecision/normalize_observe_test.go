package policydecision_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type captureObserver struct {
	records []policydecision.Record
	err     error
}

func (c *captureObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	c.records = append(c.records, record)
	return c.err
}

func TestNoopObserverIsCheap(t *testing.T) {
	t.Parallel()
	if err := (policydecision.NoopObserver{}).OnPolicyDecision(context.Background(), policydecision.Record{}); err != nil {
		t.Fatalf("noop observer must not error: %v", err)
	}
}

func TestChainObserverDeliversCloneToEachChild(t *testing.T) {
	t.Parallel()
	a := &captureObserver{}
	b := &captureObserver{}
	chain := policydecision.NewChainObserver(a, b, nil)
	record := policydecision.Record{
		Stage:       feature.StageIDPreRequest,
		Outcome:     policydecision.OutcomeAllow,
		Effect:      policydecision.EffectNone,
		Annotations: map[string]string{"k": "v"},
		Scope: scope.PrincipalScopeView{
			SafeClaims: map[string]string{"team": "platform"},
		},
	}
	if err := chain.OnPolicyDecision(context.Background(), record); err != nil {
		t.Fatalf("chain error: %v", err)
	}
	if len(a.records) != 1 || len(b.records) != 1 {
		t.Fatalf("each child must receive one record, got %d and %d", len(a.records), len(b.records))
	}
	// Mutating one child's record must not affect the other or the source.
	a.records[0].Annotations["k"] = "mutated"
	a.records[0].Scope.SafeClaims["team"] = "tampered"
	if b.records[0].Annotations["k"] != "v" {
		t.Fatalf("child b saw mutation from child a: %q", b.records[0].Annotations["k"])
	}
	if b.records[0].Scope.SafeClaims["team"] != "platform" {
		t.Fatalf("child b saw scope mutation from child a: %q", b.records[0].Scope.SafeClaims["team"])
	}
	if record.Annotations["k"] != "v" {
		t.Fatalf("source record mutated by observer: %q", record.Annotations["k"])
	}
}

func TestChainObserverIsolatesChildErrors(t *testing.T) {
	t.Parallel()
	failing := &captureObserver{err: errors.New("boom")}
	good := &captureObserver{}
	chain := policydecision.NewChainObserver(failing, good)
	if err := chain.OnPolicyDecision(context.Background(), policydecision.Record{}); err != nil {
		t.Fatalf("chain must not propagate child errors: %v", err)
	}
	if len(good.records) != 1 {
		t.Fatalf("good child must still receive record after failing sibling")
	}
}

func TestChainObserverEmptyIsNoop(t *testing.T) {
	t.Parallel()
	chain := policydecision.NewChainObserver(nil, nil)
	if err := chain.OnPolicyDecision(context.Background(), policydecision.Record{}); err != nil {
		t.Fatalf("empty chain must not error: %v", err)
	}
	if obs := chain.Observers(); len(obs) != 0 {
		t.Fatalf("empty chain observers must be empty, got %d", len(obs))
	}
}

func TestIsNoopObserverDetectsDisabledObservers(t *testing.T) {
	t.Parallel()
	var nilObs policydecision.Observer
	if !policydecision.IsNoopObserver(nilObs) {
		t.Fatalf("nil observer must be treated as no-op")
	}
	if !policydecision.IsNoopObserver(policydecision.NoopObserver{}) {
		t.Fatalf("NoopObserver must be treated as no-op")
	}
	if !policydecision.IsNoopObserver(policydecision.NewChainObserver(nil, nil)) {
		t.Fatalf("empty chain must be treated as no-op")
	}
	capture := &captureObserver{}
	if policydecision.IsNoopObserver(capture) {
		t.Fatalf("real observer must not be treated as no-op")
	}
	if policydecision.IsNoopObserver(policydecision.NewChainObserver(capture)) {
		t.Fatalf("chain with real child must not be treated as no-op")
	}
}

func TestNormalizeRecordBoundsAndTrims(t *testing.T) {
	t.Parallel()
	longID := strings.Repeat("a", policydecision.MaxProviderIDBytes+50)
	r := policydecision.Record{
		Stage:          "   " + feature.StageIDPreRequest + "   ",
		Provider:       policydecision.ProviderRef{ID: longID, Stage: feature.StageIDPreRequest},
		TraceID:        "  trace-1  ",
		Outcome:        policydecision.OutcomeDeny,
		Effect:         policydecision.EffectNone,
		ReasonCode:     "  Policy_Denied.42 ",
		ClientCategory: "Policy Denied!",
		ClientMessage:  "  hello world\n ",
	}
	got := policydecision.NormalizeRecord(r)
	if got.Stage != feature.StageIDPreRequest {
		t.Fatalf("stage not trimmed: %q", got.Stage)
	}
	if got.Provider.ID != longID[:policydecision.MaxProviderIDBytes] {
		t.Fatalf("provider id not bounded: len=%d", len(got.Provider.ID))
	}
	if got.TraceID != "trace-1" {
		t.Fatalf("trace id not trimmed: %q", got.TraceID)
	}
	if got.ReasonCode != "policy_denied.42" {
		t.Fatalf("reason code not normalized: %q", got.ReasonCode)
	}
	if got.ClientCategory != "policydenied" {
		t.Fatalf("client category not normalized: %q", got.ClientCategory)
	}
	if got.ClientMessage != "hello world" {
		t.Fatalf("client message not normalized: %q", got.ClientMessage)
	}
}

func TestNormalizeRecordEmptyProviderBecomesUnknown(t *testing.T) {
	t.Parallel()
	got := policydecision.NormalizeRecord(policydecision.Record{Provider: policydecision.ProviderRef{ID: "   "}})
	if got.Provider.ID != "unknown" {
		t.Fatalf("empty provider id must become %q, got %q", "unknown", got.Provider.ID)
	}
}

func TestNormalizeRecordEmptyReasonBecomesUnspecified(t *testing.T) {
	t.Parallel()
	got := policydecision.NormalizeRecord(policydecision.Record{ReasonCode: "  ", ClientCategory: "!!!"})
	if got.ReasonCode != "unspecified" {
		t.Fatalf("empty reason must become unspecified, got %q", got.ReasonCode)
	}
	if got.ClientCategory != "unspecified" {
		t.Fatalf("invalid client category must become unspecified, got %q", got.ClientCategory)
	}
}

func TestNormalizeRecordDropsInvalidAnnotationKeys(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{
		Annotations: map[string]string{
			"valid_key":    "value",
			"  ":           "empty-key",
			"bad key!":     "ignored",
			"also-valid.1": "ok",
			"":             "empty",
		},
	}
	got := policydecision.NormalizeRecord(r)
	if _, ok := got.Annotations["valid_key"]; !ok {
		t.Fatalf("valid key dropped: %#v", got.Annotations)
	}
	if _, ok := got.Annotations["also-valid.1"]; !ok {
		t.Fatalf("valid dotted key dropped: %#v", got.Annotations)
	}
	for k := range got.Annotations {
		if strings.Contains(k, " ") {
			t.Fatalf("invalid key with space survived: %q", k)
		}
	}
}

func TestNormalizeRecordTruncatesAnnotationValuesAndMarks(t *testing.T) {
	t.Parallel()
	longVal := strings.Repeat("x", policydecision.MaxAnnotationValueBytes+10)
	r := policydecision.Record{
		Annotations: map[string]string{"k": longVal},
	}
	got := policydecision.NormalizeRecord(r)
	v := got.Annotations["k"]
	if utf8.RuneCountInString(v) > policydecision.MaxAnnotationValueBytes {
		t.Fatalf("value not bounded: %d", utf8.RuneCountInString(v))
	}
	if got.Annotations["truncated"] != "true" {
		t.Fatalf("truncation marker missing: %#v", got.Annotations)
	}
}

func TestNormalizeRecordTruncationMarkerDoesNotOverwriteAnnotation(t *testing.T) {
	t.Parallel()
	longVal := strings.Repeat("x", policydecision.MaxAnnotationValueBytes+10)
	got := policydecision.NormalizeRecord(policydecision.Record{
		Annotations: map[string]string{
			"k":         longVal,
			"truncated": "plugin-value",
		},
	})
	if got.Annotations["truncated"] != "plugin-value" {
		t.Fatalf("plugin annotation overwritten: got %q", got.Annotations["truncated"])
	}
	if got.Annotations["truncated.1"] != "true" {
		t.Fatalf("collision-safe truncation marker missing: %#v", got.Annotations)
	}
}

func TestNormalizeRecordCapsAnnotationEntries(t *testing.T) {
	t.Parallel()
	in := make(map[string]string, policydecision.MaxAnnotationEntries+5)
	for i := range policydecision.MaxAnnotationEntries + 5 {
		in[string(rune('a'+i%26))+string(rune('0'+i/26))] = "v"
	}
	got := policydecision.NormalizeRecord(in2record(in))
	if len(got.Annotations) > policydecision.MaxAnnotationEntries {
		t.Fatalf("entries not capped: %d", len(got.Annotations))
	}
	if got.Annotations["truncated"] != "true" {
		t.Fatalf("entry-cap truncation marker missing")
	}
}

func TestNormalizeRecordAnnotationsNeverExceedMaxWithTruncationMarker(t *testing.T) {
	t.Parallel()
	key := func(i int) string {
		return string(rune('a'+i%26)) + string(rune('0'+i/26))
	}

	// Exactly MaxAnnotationEntries valid entries, none oversized: no marker, count == max.
	exact := make(map[string]string, policydecision.MaxAnnotationEntries)
	for i := range policydecision.MaxAnnotationEntries {
		exact[key(i)] = "v"
	}
	gotExact := policydecision.NormalizeRecord(in2record(exact))
	if len(gotExact.Annotations) != policydecision.MaxAnnotationEntries {
		t.Fatalf("exactly-max entries: count = %d, want %d (no marker expected)", len(gotExact.Annotations), policydecision.MaxAnnotationEntries)
	}
	if _, ok := gotExact.Annotations["truncated"]; ok {
		t.Fatalf("no truncation marker expected when nothing truncated")
	}

	// MaxAnnotationEntries valid entries with one oversized value: marker must fit within max.
	oversized := make(map[string]string, policydecision.MaxAnnotationEntries)
	for i := range policydecision.MaxAnnotationEntries {
		v := "v"
		if i == 0 {
			v = strings.Repeat("x", policydecision.MaxAnnotationValueBytes+10)
		}
		oversized[key(i)] = v
	}
	gotOversized := policydecision.NormalizeRecord(in2record(oversized))
	if len(gotOversized.Annotations) > policydecision.MaxAnnotationEntries {
		t.Fatalf("oversized-value case: count = %d, exceeds max %d", len(gotOversized.Annotations), policydecision.MaxAnnotationEntries)
	}
	if gotOversized.Annotations["truncated"] != "true" {
		t.Fatalf("oversized-value case: truncation marker missing")
	}

	// More than max entries: marker must fit within max.
	over := make(map[string]string, policydecision.MaxAnnotationEntries+5)
	for i := range policydecision.MaxAnnotationEntries + 5 {
		over[key(i)] = "v"
	}
	gotOver := policydecision.NormalizeRecord(in2record(over))
	if len(gotOver.Annotations) > policydecision.MaxAnnotationEntries {
		t.Fatalf("over-max case: count = %d, exceeds max %d", len(gotOver.Annotations), policydecision.MaxAnnotationEntries)
	}
	if gotOver.Annotations["truncated"] != "true" {
		t.Fatalf("over-max case: truncation marker missing")
	}
}

func in2record(in map[string]string) policydecision.Record {
	return policydecision.Record{Annotations: in}
}

func TestNormalizeRecordClonesScope(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{
		Scope: scope.PrincipalScopeView{
			SafeClaims: map[string]string{"a": "b"},
		},
	}
	got := policydecision.NormalizeRecord(r)
	got.Scope.SafeClaims["a"] = "tampered"
	if r.Scope.SafeClaims["a"] != "b" {
		t.Fatalf("source scope mutated through normalized copy: %q", r.Scope.SafeClaims["a"])
	}
}

func TestNormalizeRecordStripsControlCharsFromMessage(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{ClientMessage: "safe\u0000text\x07with\x01ctrl"}
	got := policydecision.NormalizeRecord(r)
	if strings.ContainsAny(got.ClientMessage, "\x00\x01\x07") {
		t.Fatalf("control characters survived: %q", got.ClientMessage)
	}
}

func TestNormalizeRecordNilAnnotationsStaysNil(t *testing.T) {
	t.Parallel()
	got := policydecision.NormalizeRecord(policydecision.Record{})
	if got.Annotations != nil {
		t.Fatalf("nil annotations must stay nil, got %#v", got.Annotations)
	}
}

// Exported constants for tests in the same package via the test pseudo-API.
var (
	_ = policydecision.MaxProviderIDBytes
	_ = policydecision.MaxAnnotationValueBytes
	_ = policydecision.MaxAnnotationEntries
)

func TestNormalizeRecordClientMessageParityWithAipapi(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"safe client msg",
		"  hello world\n ",
		"safe\u0000text\x07with\x01ctrl",
		strings.Repeat("a", 600),
		strings.Repeat("a", policydecision.MaxClientMessageBytes-1) + "😀tail",
	}
	for _, in := range cases {
		r := policydecision.Record{
			Stage:         feature.StageIDPreRequest,
			Outcome:       policydecision.OutcomeDeny,
			Effect:        policydecision.EffectNone,
			ClientMessage: in,
		}
		got := policydecision.NormalizeRecord(r).ClientMessage
		want := aipapi.NormalizeClientMessage(in)
		if got != want {
			t.Fatalf("NormalizeRecord ClientMessage parity failed for %q: got %q want %q", in, got, want)
		}
		if len(got) > policydecision.MaxClientMessageBytes {
			t.Fatalf("NormalizeRecord ClientMessage exceeded bound for %q: len=%d", in, len(got))
		}
	}
}

package routing_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

type mapNativeResolver map[string]routing.ModelBinding // key = backend\x00model

func (m mapNativeResolver) ResolveModelBinding(backendID, model string) routing.ModelBinding {
	if b, ok := m[backendID+"\x00"+model]; ok {
		return b
	}
	return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
}

func TestBindNativeModelIDs_preservesLogicalModelSetsNative(t *testing.T) {
	t.Parallel()

	ttft := 50 * time.Millisecond
	handicap := 10 * time.Millisecond
	minTok := int64(1)
	params := url.Values{"temperature": []string{"0.2"}}
	sel := &routing.Selector{
		Affinity: routing.AffinitySession,
		Alternatives: []routing.FailoverAlt{
			{Primary: &routing.Primary{
				Backend: "b1", Model: "openai/gpt-a",
				Params: params, TTFTTimeout: &ttft,
				Size: routing.RequestSizeConstraint{MinContextTokens: &minTok},
			}},
			{Weighted: &routing.Weighted{Branches: []routing.WeightedBranch{
				{Weight: 7, IsFirst: true, Target: routing.Primary{Backend: "b1", Model: "openai/gpt-w"}},
				{Weight: 3, IsThinker: true, Parallel: &routing.Parallel{Branches: []routing.ParallelBranch{
					{Target: routing.Primary{Backend: "b2", Model: "openai/gpt-h"}, Handicap: handicap},
				}}},
			}}},
			{Parallel: &routing.Parallel{Branches: []routing.ParallelBranch{
				{Target: routing.Primary{Backend: "b1", Model: "openai/gpt-p"}, Handicap: handicap},
			}}},
		},
	}
	resolver := mapNativeResolver{
		"b1\x00openai/gpt-a": {Kind: routing.ModelBindingExactCanonical, Native: "native-a"},
		"b1\x00openai/gpt-w": {Kind: routing.ModelBindingExactCanonical, Native: "native-w"},
		"b2\x00openai/gpt-h": {Kind: routing.ModelBindingExactCanonical, Native: "native-h"},
		"b1\x00openai/gpt-p": {Kind: routing.ModelBindingExactCanonical, Native: "native-p"},
	}
	if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
		t.Fatal(err)
	}

	p0 := sel.Alternatives[0].Primary
	if p0.Model != "openai/gpt-a" || p0.NativeModel != "native-a" {
		t.Fatalf("primary = %+v", p0)
	}
	if p0.String() != "b1:openai/gpt-a?temperature=0.2" {
		t.Fatalf("String leaked native: %q", p0.String())
	}
	if p0.Params.Get("temperature") != "0.2" {
		t.Fatal("params mutated")
	}
	if p0.TTFTTimeout == nil || *p0.TTFTTimeout != ttft {
		t.Fatal("TTFT mutated")
	}
	if p0.Size.MinContextTokens == nil || *p0.Size.MinContextTokens != 1 {
		t.Fatal("size mutated")
	}
	w := sel.Alternatives[1].Weighted
	if w.Branches[0].Weight != 7 || !w.Branches[0].IsFirst ||
		w.Branches[0].Target.Model != "openai/gpt-w" || w.Branches[0].Target.NativeModel != "native-w" {
		t.Fatalf("weighted branch = %+v", w.Branches[0])
	}
	h := w.Branches[1].Parallel.Branches[0]
	if !w.Branches[1].IsThinker || h.Target.Model != "openai/gpt-h" || h.Target.NativeModel != "native-h" {
		t.Fatalf("hybrid parallel = %+v", w.Branches[1])
	}
	if h.Handicap != handicap {
		t.Fatal("handicap mutated")
	}
	par := sel.Alternatives[2].Parallel.Branches[0].Target
	if par.Model != "openai/gpt-p" || par.NativeModel != "native-p" {
		t.Fatalf("parallel = %+v", par)
	}
	if sel.Affinity != routing.AffinitySession {
		t.Fatal("affinity mutated")
	}
}

func TestBindNativeModelIDs_unknownPassThrough(t *testing.T) {
	t.Parallel()
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{
		{Primary: &routing.Primary{Backend: "b1", Model: "openai/unknown"}},
	}}
	if err := routing.BindNativeModelIDs(sel, mapNativeResolver{}); err != nil {
		t.Fatal(err)
	}
	p := sel.Alternatives[0].Primary
	if p.Model != "openai/unknown" || p.NativeModel != "" {
		t.Fatalf("unknown must pass through unchanged: %+v", p)
	}
}

func TestBindNativeModelIDs_knownNative(t *testing.T) {
	t.Parallel()
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{
		{Primary: &routing.Primary{Backend: "b1", Model: "native-a"}},
	}}
	resolver := mapNativeResolver{
		"b1\x00native-a": {Kind: routing.ModelBindingKnownNative, Native: "native-a"},
	}
	if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
		t.Fatal(err)
	}
	p := sel.Alternatives[0].Primary
	if p.Model != "native-a" || p.NativeModel != "native-a" {
		t.Fatalf("known native = %+v", p)
	}
}

func TestBindNativeModelIDs_wrongBackendRejects(t *testing.T) {
	t.Parallel()
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{
		{Primary: &routing.Primary{Backend: "b2", Model: "openai/gpt-a"}},
	}}
	resolver := mapNativeResolver{
		"b2\x00openai/gpt-a": {Kind: routing.ModelBindingWrongBackend},
	}
	err := routing.BindNativeModelIDs(sel, resolver)
	if !errors.Is(err, routing.ErrWrongBackendCanonical) {
		t.Fatalf("err = %v", err)
	}
	var wb *routing.WrongBackendCanonicalError
	if !errors.As(err, &wb) || wb.Backend != "b2" || wb.Model != "openai/gpt-a" {
		t.Fatalf("typed err = %#v", err)
	}
	if sel.Alternatives[0].Primary.NativeModel != "" {
		t.Fatal("must not bind native on wrong-backend reject")
	}
}

func TestBindNativeModelIDs_sameCanonicalDifferentNativesTwoBackends(t *testing.T) {
	t.Parallel()
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{
		{Primary: &routing.Primary{Backend: "backend-a", Model: "vendor/shared"}},
		{Primary: &routing.Primary{Backend: "backend-b", Model: "vendor/shared"}},
	}}
	resolver := mapNativeResolver{
		"backend-a\x00vendor/shared": {Kind: routing.ModelBindingExactCanonical, Native: "native-a"},
		"backend-b\x00vendor/shared": {Kind: routing.ModelBindingExactCanonical, Native: "native-b"},
	}
	if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
		t.Fatal(err)
	}
	a := sel.Alternatives[0].Primary
	b := sel.Alternatives[1].Primary
	if a.Model != "vendor/shared" || a.NativeModel != "native-a" {
		t.Fatalf("backend-a = %+v", a)
	}
	if b.Model != "vendor/shared" || b.NativeModel != "native-b" {
		t.Fatalf("backend-b = %+v", b)
	}
	// Same logical AttemptCandidate.String() key shape is fine when natives differ.
	if a.NativeModel == b.NativeModel {
		t.Fatal("exact-backend resolution must not share natives across backends")
	}
}

func TestBackendFacingCandidate_projectsNativeOnlyOnCopy(t *testing.T) {
	t.Parallel()
	logical := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "b1", Model: "openai/gpt-a", NativeModel: "wire-a"},
		Key:     "b1:openai/gpt-a",
	}
	wire := routing.BackendFacingCandidate(logical)
	if wire.Primary.Model != "wire-a" {
		t.Fatalf("wire Model = %q", wire.Primary.Model)
	}
	if logical.Primary.Model != "openai/gpt-a" {
		t.Fatal("logical candidate must remain canonical")
	}
	if wire.Key != "b1:openai/gpt-a" {
		t.Fatalf("Key mutated = %q", wire.Key)
	}
	if logical.Primary.WireModel() != "wire-a" {
		t.Fatalf("WireModel = %q", logical.Primary.WireModel())
	}
}

func TestBindNativeModelIDs_nilSafe(t *testing.T) {
	t.Parallel()
	if err := routing.BindNativeModelIDs(nil, mapNativeResolver{}); err != nil {
		t.Fatal(err)
	}
	if err := routing.BindNativeModelIDs(&routing.Selector{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWithNativeModelResolver_nilContextSafe(t *testing.T) {
	t.Parallel()
	var nilCtx context.Context
	ctx := routing.WithNativeModelResolver(nilCtx, nil)
	if ctx == nil {
		t.Fatal("expected non-nil context when ctx is nil and resolver is nil")
	}
	ctx2 := routing.WithNativeModelResolver(nilCtx, mapNativeResolver{})
	if ctx2 == nil {
		t.Fatal("expected non-nil context when ctx is nil and resolver is non-nil")
	}
	r, ok := routing.NativeModelResolverFromContext(ctx2)
	if !ok || r == nil {
		t.Fatal("expected resolver attached")
	}
}

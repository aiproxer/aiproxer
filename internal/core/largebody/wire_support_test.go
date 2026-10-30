package largebody_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type stubWireBackend struct {
	requestSupport largebody.WireRequestSupport
	domainSupport  largebody.WireDomainSupport
	reqCalls       int
	domainCalls    int
}

func (s *stubWireBackend) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	s.reqCalls++
	return s.requestSupport
}

func (s *stubWireBackend) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	s.domainCalls++
	return s.domainSupport
}

var _ largebody.WireBackend = (*stubWireBackend)(nil)

func TestWireSupportReason_StringsAndUnknown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason largebody.WireSupportReason
		want   string
	}{
		{largebody.WireSupportReasonNone, "none"},
		{largebody.WireSupportReasonUnsupported, "unsupported"},
		{largebody.WireSupportReasonProfileUnsupported, "profile_unsupported"},
		{largebody.WireSupportReasonOperationUnsupported, "operation_unsupported"},
		{largebody.WireSupportReasonDeliveryUnsupported, "delivery_unsupported"},
		{largebody.WireSupportReasonBodyModeUnsupported, "body_mode_unsupported"},
		{largebody.WireSupportReasonModelUnsupported, "model_unsupported"},
		{largebody.WireSupportReasonRewriteUnsupported, "rewrite_unsupported"},
		{largebody.WireSupportReasonClampUnsupported, "clamp_unsupported"},
		{largebody.WireSupportReasonCapabilityUnsupported, "capability_unsupported"},
		{largebody.WireSupportReason(255), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.reason.String(); got != tc.want {
			t.Fatalf("reason %d String() = %q, want %q", uint8(tc.reason), got, tc.want)
		}
	}
}

func TestWireRequestSupport_Validate(t *testing.T) {
	t.Parallel()
	validCompatible := largebody.WireRequestSupport{
		Compatible: true,
		Reason:     largebody.WireSupportReasonNone,
	}
	if err := validCompatible.Validate(); err != nil {
		t.Fatalf("valid compatible support rejected: %v", err)
	}

	validIncompatible := largebody.WireRequestSupport{
		Compatible: false,
		Reason:     largebody.WireSupportReasonUnsupported,
	}
	if err := validIncompatible.Validate(); err != nil {
		t.Fatalf("valid incompatible support rejected: %v", err)
	}

	compatWithReason := largebody.WireRequestSupport{
		Compatible: true,
		Reason:     largebody.WireSupportReasonUnsupported,
	}
	if err := compatWithReason.Validate(); err == nil {
		t.Fatal("compatible support with non-none reason must be rejected")
	}

	incompatWithoutReason := largebody.WireRequestSupport{
		Compatible: false,
		Reason:     largebody.WireSupportReasonNone,
	}
	if err := incompatWithoutReason.Validate(); err == nil {
		t.Fatal("incompatible support with none reason must be rejected")
	}

	unknownReason := largebody.WireRequestSupport{
		Compatible: false,
		Reason:     largebody.WireSupportReason(255),
	}
	if err := unknownReason.Validate(); err == nil {
		t.Fatal("support with unknown reason must be rejected")
	}
}

func TestWireDomainSupport_Validate(t *testing.T) {
	t.Parallel()
	validCompatible := largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: true,
		Reason:           largebody.WireSupportReasonNone,
	}
	if err := validCompatible.Validate(); err != nil {
		t.Fatalf("valid compatible domain support rejected: %v", err)
	}

	validIncompatible := largebody.WireDomainSupport{
		Compatible: false,
		Reason:     largebody.WireSupportReasonModelUnsupported,
	}
	if err := validIncompatible.Validate(); err != nil {
		t.Fatalf("valid incompatible domain support rejected: %v", err)
	}

	compatWithReason := largebody.WireDomainSupport{
		Compatible: true,
		Reason:     largebody.WireSupportReasonModelUnsupported,
	}
	if err := compatWithReason.Validate(); err == nil {
		t.Fatal("compatible domain support with non-none reason must be rejected")
	}

	incompatWithoutReason := largebody.WireDomainSupport{
		Compatible: false,
		Reason:     largebody.WireSupportReasonNone,
	}
	if err := incompatWithoutReason.Validate(); err == nil {
		t.Fatal("incompatible domain support with none reason must be rejected")
	}
}

func TestWireBackend_InterfaceContract(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[largebody.WireBackend]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("largebody.WireBackend is %s, want interface", typ.Kind())
	}
	var names []string
	for method := range typ.Methods() {
		names = append(names, method.Name)
	}
	sort.Strings(names)
	want := []string{"ResolveWireDomain", "ResolveWireRequest"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("WireBackend methods = %v, want %v", names, want)
	}
}

func TestAsWireBackend_Probing(t *testing.T) {
	t.Parallel()
	if got, ok := largebody.AsWireBackend(nil); ok || got != nil {
		t.Fatalf("AsWireBackend(nil) = (%v, %v), want (nil, false)", got, ok)
	}

	stub := &stubWireBackend{
		requestSupport: largebody.WireRequestSupport{Compatible: true},
		domainSupport:  largebody.WireDomainSupport{Compatible: true},
	}
	got, ok := largebody.AsWireBackend(stub)
	if !ok || got == nil {
		t.Fatalf("AsWireBackend(stub) = (%v, %v), want (non-nil, true)", got, ok)
	}

	facts := largebody.WireRequestFacts{
		ProfileID: "openai-responses-v1",
		Operation: aipapi.OperationOpenAIResponses,
		Delivery:  aipapi.DeliveryModeStreaming,
		BodyMode:  largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
	}
	res := got.ResolveWireRequest(context.Background(), facts, routing.AttemptCandidate{})
	if !res.Compatible || stub.reqCalls != 1 {
		t.Fatalf("ResolveWireRequest returned %v, calls = %d", res, stub.reqCalls)
	}
}

func TestSemanticFactBudget(t *testing.T) {
	t.Parallel()
	if largebody.DefaultMaxSemanticFactBytes != 256*1024 {
		t.Fatalf("DefaultMaxSemanticFactBytes = %d, want %d", largebody.DefaultMaxSemanticFactBytes, 256*1024)
	}

	// nil context returns default
	if got := largebody.SemanticFactBudget(nil); got != largebody.DefaultMaxSemanticFactBytes { //nolint:staticcheck // deliberately exercises nil-context default
		t.Fatalf("SemanticFactBudget(nil) = %d, want default %d", got, largebody.DefaultMaxSemanticFactBytes)
	}

	// background context returns default
	if got := largebody.SemanticFactBudget(context.Background()); got != largebody.DefaultMaxSemanticFactBytes {
		t.Fatalf("SemanticFactBudget(Background) = %d, want default %d", got, largebody.DefaultMaxSemanticFactBytes)
	}

	// non-positive budget returns original context without modification
	ctx := context.Background()
	if gotCtx := largebody.WithSemanticFactBudget(ctx, 0); gotCtx != ctx {
		t.Fatal("WithSemanticFactBudget(ctx, 0) must return original ctx")
	}
	if gotCtx := largebody.WithSemanticFactBudget(ctx, -5); gotCtx != ctx {
		t.Fatal("WithSemanticFactBudget(ctx, -5) must return original ctx")
	}

	// positive budget is stored and retrieved
	customCtx := largebody.WithSemanticFactBudget(ctx, 512*1024)
	if got := largebody.SemanticFactBudget(customCtx); got != 512*1024 {
		t.Fatalf("SemanticFactBudget(customCtx) = %d, want 512 KiB", got)
	}
}

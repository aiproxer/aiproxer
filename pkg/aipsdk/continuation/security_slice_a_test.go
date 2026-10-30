package continuation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestResponseIDMaxLengthValidation(t *testing.T) {
	t.Parallel()
	oversized := aipcont.ResponseID("resp_" + strings.Repeat("A", 600))
	if err := oversized.Validate(); err == nil {
		t.Fatal("expected oversized response ID to fail validation")
	}
}

func TestNativeReferenceRedaction(t *testing.T) {
	t.Parallel()
	ref := aipcont.NativeReference{
		Provider: "provider-secret",
		Kind:     "token",
		ID:       "secret-id-123",
		Opaque:   []byte("super-secret-opaque-data"),
	}
	if str := ref.String(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.String() got %q, want %q", str, "[REDACTED_NATIVE_REF]")
	}
	if str := ref.GoString(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.GoString() got %q, want %q", str, "[REDACTED_NATIVE_REF]")
	}
}

func TestLookupUniformErrorClassification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}

	// Test 1: Malformed ID returns ErrPreviousResponseNotFound directly without store lookup
	malformedStore := &failIfCalledStore{t: t}
	_, err := aipcont.Lookup(ctx, malformedStore, scope, aipcont.ResponseID("invalid_id_format"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("malformed ID lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 2: Incomplete ineligible record mapped to ErrPreviousResponseNotFound
	incompleteStore := stubStore{err: aipcont.ErrIncompleteNotEligible}
	_, err = aipcont.Lookup(ctx, incompleteStore, scope, aipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("incomplete store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 3: Record not eligible mapped to ErrPreviousResponseNotFound
	ineligibleStore := stubStore{err: aipcont.ErrRecordNotEligible}
	_, err = aipcont.Lookup(ctx, ineligibleStore, scope, aipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("ineligible store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 4: Cycle detected mapped to ErrPreviousResponseNotFound
	cycleStore := stubStore{err: aipcont.ErrCycleDetected}
	_, err = aipcont.Lookup(ctx, cycleStore, scope, aipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("cycle store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 5: Depth exceeded mapped to ErrPreviousResponseNotFound
	depthStore := stubStore{err: aipcont.ErrChainDepthExceeded}
	_, err = aipcont.Lookup(ctx, depthStore, scope, aipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("depth store lookup got %v, want ErrPreviousResponseNotFound", err)
	}
}

type failIfCalledStore struct {
	aipcont.Store
	t *testing.T
}

func (s *failIfCalledStore) Get(context.Context, aipcont.Scope, aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	s.t.Helper()
	s.t.Fatal("store Get should not have been called for malformed response ID")
	return aipcont.ContinuationRecord{}, nil
}

func FuzzResponseIDValidate(f *testing.F) {
	f.Add("resp_1234567890123456")
	f.Add("")
	f.Add("resp_")
	f.Add("resp_bad_base64_!@#$%^&*")
	f.Add(strings.Repeat("A", 1000))

	f.Fuzz(func(t *testing.T, s string) {
		id := aipcont.ResponseID(s)
		_ = id.Validate()
	})
}

func TestMaterializeAmplificationCheckAtEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "t", PrincipalID: "p"}

	// Huge input that exceeds bounds before store lookup
	hugeInput := make([]aipapi.Item, 0, 100)
	for range 100 {
		hugeInput = append(hugeInput, aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			Role: aipapi.RoleUser,
		})
	}

	// Store should not be called if new input itself exceeds MaxMaterializedItems
	mockStore := &failIfCalledStore{t: t}

	_, err := aipcont.Materialize(ctx, aipcont.MaterializeInput{
		Store:    mockStore,
		Scope:    scope,
		StartID:  aipcont.ResponseID("resp_1234567890123456"),
		NewInput: hugeInput,
		Bounds:   aipcont.Bounds{MaxMaterializedItems: 10},
	})
	if !errors.Is(err, aipcont.ErrMaterializedItemsExceeded) {
		t.Fatalf("expected ErrMaterializedItemsExceeded at entry, got %v", err)
	}
}

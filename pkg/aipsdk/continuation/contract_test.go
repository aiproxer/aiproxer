package continuation_test

import (
	"context"
	"errors"
	"testing"

	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestResponseIDValidate(t *testing.T) {
	t.Parallel()
	if err := aipcont.ResponseID("bad").Validate(); err == nil {
		t.Fatal("expected invalid prefix")
	}
	if err := aipcont.ResponseID(aipcont.ResponseIDPrefix + "short").Validate(); err == nil {
		t.Fatal("expected short entropy rejection")
	}
}

func TestScopeEqual(t *testing.T) {
	t.Parallel()
	a := aipcont.Scope{TenantID: "t1", PrincipalID: "p", SessionID: "s"}
	b := aipcont.Scope{TenantID: "t1", PrincipalID: "p", SessionID: "s", ConnectionID: ""}
	if !a.Equal(b) {
		t.Fatal("expected equal scopes")
	}
	c := aipcont.Scope{TenantID: "t2", PrincipalID: "p", SessionID: "s"}
	if a.Equal(c) {
		t.Fatal("expected scopes with different TenantID to not be equal")
	}
}

func TestLookupMapsRecordNotReady(t *testing.T) {
	t.Parallel()
	store := stubStore{err: aipcont.ErrRecordNotReady}
	_, err := aipcont.Lookup(context.Background(), store, aipcont.Scope{PrincipalID: "p"}, aipcont.ResponseID("resp_x"))
	if !errors.Is(err, aipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("got %v", err)
	}
}

type stubStore struct {
	aipcont.Store
	err error
}

func (s stubStore) Get(context.Context, aipcont.Scope, aipcont.ResponseID) (aipcont.ContinuationRecord, error) {
	return aipcont.ContinuationRecord{}, s.err
}

func TestDefaultBounds(t *testing.T) {
	t.Parallel()
	b := aipcont.DefaultBounds()
	if b.MaxChainDepth != 64 || b.MaxMaterializedBytes <= 0 {
		t.Fatalf("bounds=%+v", b)
	}
}

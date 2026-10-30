package openresponsescompat

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestDefaultDialects_DeclareExactItemReferenceDialect proves that the default
// dialect surface is truthful for the default item_references capability: the
// exact item_reference item dialect is declared so admission and self-admission
// match a canonical item-reference call.
func TestDefaultDialects_DeclareExactItemReferenceDialect(t *testing.T) {
	t.Parallel()
	ds := dialectSupportFromConfig(Config{Dialects: defaultDialects()})
	want := aipapi.DialectRequirement{Kind: "item", Dialect: "item_reference"}
	if !slices.Contains(ds.ItemDialects, want) {
		t.Fatalf("default item dialects = %+v, want exact item_reference dialect %+v", ds.ItemDialects, want)
	}
	if !slices.Contains(ds.ItemDialects, aipapi.DialectRequirement{Kind: "item", Dialect: DefaultItemDialect}) {
		t.Fatalf("default item dialects must keep the pinned profile dialect, got %+v", ds.ItemDialects)
	}
}

// TestBackend_ItemReferenceDefaultSelfAdmissionRoundTrips proves that an
// item-reference call is admitted by the default generic OpenResponses backend
// (truthful item_references capability) and forwarded on the pinned wire.
func TestBackend_ItemReferenceDefaultSelfAdmissionRoundTrips(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := itemAuthorityCreateCall()
	call.Items = append(call.Items, aipapi.Item{
		Kind:      aipapi.ItemKindItemReference,
		ID:        "ref-1",
		Status:    aipapi.ItemStatusCompleted,
		Reference: &aipapi.ItemReference{ID: "msg_prev"},
	})
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing text delta in %+v", events)
	}
	if obs.count() != 1 {
		t.Fatalf("request count = %d, want 1", obs.count())
	}
	body := string(obs.last(t).Body)
	if !strings.Contains(body, `"type":"item_reference"`) || !strings.Contains(body, `"id":"msg_prev"`) {
		t.Fatalf("upstream wire missing the item_reference:\n%s", body)
	}
}

// TestBackend_ItemReferenceRemovedDialectRejectsBeforeNetwork proves that an
// operator who removes the item_reference item dialect makes the capability
// truthful: an item-reference call is rejected before any HTTP round trip.
func TestBackend_ItemReferenceRemovedDialectRejectsBeforeNetwork(t *testing.T) {
	t.Parallel()
	cfg := "dialects:\n  item:\n    - dialect: openresponses.2026-04-24\n"
	be, obs := newObserverBackend(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := itemAuthorityCreateCall()
	call.Items = append(call.Items, aipapi.Item{
		Kind:      aipapi.ItemKindItemReference,
		ID:        "ref-1",
		Status:    aipapi.ItemStatusCompleted,
		Reference: &aipapi.ItemReference{ID: "msg_prev"},
	})
	if _, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}); err == nil {
		t.Fatal("expected rejection when the item_reference dialect is not declared")
	}
	if obs.count() != 0 {
		t.Fatalf("rejection caused %d upstream requests, want 0", obs.count())
	}
}

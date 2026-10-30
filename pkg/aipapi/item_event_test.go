package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func compactionItemEvent(id, encrypted string) aipapi.Event {
	return aipapi.Event{
		Kind: aipapi.EventItem,
		Item: &aipapi.Item{
			Kind:   aipapi.ItemKindCompaction,
			ID:     id,
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncryptedContent: encrypted,
			},
		},
	}
}

func TestValidateEventEnvelope_itemEventRequiresCarriedItem(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventItem}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error for item event without carried item")
	}
}

func TestValidateEventEnvelope_itemEventValidatesCarriedItem(t *testing.T) {
	t.Parallel()
	ev := compactionItemEvent("cmp_1", "enc:cmp_1")
	if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
		t.Fatalf("valid item event rejected: %v", err)
	}
}

func TestValidateEventEnvelope_itemOnlyAllowedOnItemEvent(t *testing.T) {
	t.Parallel()
	ev := compactionItemEvent("cmp_1", "enc")
	ev.Kind = aipapi.EventResponseStarted
	if err := aipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("expected error for carried item on non-item event")
	}
}

func TestValidateEventEnvelope_itemEventRejectsInvalidCarriedItem(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{
		Kind: aipapi.EventItem,
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindCompaction,
			ID:   "cmp_bad",
			Compaction: &aipapi.CompactionItem{
				EncryptedContent: string(make([]byte, aipapi.MaxCompactionEncryptedContentBytes+1)),
			},
		},
	}
	if err := aipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("expected error for oversized compaction encrypted_content")
	}
}

func TestValidateEventSequence_allowsItemEventAfterStart(t *testing.T) {
	t.Parallel()
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		compactionItemEvent("cmp_1", "enc"),
		{Kind: aipapi.EventResponseFinished},
	}
	if err := aipapi.ValidateEventSequence(events); err != nil {
		t.Fatalf("sequence rejected: %v", err)
	}
}

func TestValidateEventSequence_itemEventBeforeStartRejected(t *testing.T) {
	t.Parallel()
	events := []aipapi.Event{
		compactionItemEvent("cmp_1", "enc"),
	}
	if err := aipapi.ValidateEventSequence(events); err == nil {
		t.Fatal("expected error for item event before response started")
	}
}

func TestCompactionItem_RoundTripsEncryptedContent(t *testing.T) {
	t.Parallel()
	item := aipapi.Item{
		Kind:   aipapi.ItemKindCompaction,
		ID:     "cmp_rt",
		Status: aipapi.ItemStatusCompleted,
		Compaction: &aipapi.CompactionItem{
			EncapsulatedID:   "enc_1",
			Dialect:          "openresponses.2026-04-24",
			Implementor:      "provider-x",
			EncryptedContent: "gAAAAABpayload",
		},
	}
	call := aipapi.Call{Items: []aipapi.Item{item}}
	if err := call.Validate(); err != nil {
		t.Fatalf("call with compaction item invalid: %v", err)
	}
}

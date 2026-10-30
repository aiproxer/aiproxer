package journalstore

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestDurableStore_CheckReadiness_NilReceiver(t *testing.T) {
	t.Parallel()
	var store *DurableStore
	err := store.CheckReadiness(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nil store") {
		t.Fatalf("error=%v want nil store", err)
	}
	store = &DurableStore{}
	err = store.CheckReadiness(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nil store") {
		t.Fatalf("nil db error=%v want nil store", err)
	}
}

func TestDurableStore_Append_NilReceiver(t *testing.T) {
	t.Parallel()
	var store *DurableStore
	err := store.Append(context.Background(), metering.Fact{})
	if err == nil || !strings.Contains(err.Error(), "nil store") {
		t.Fatalf("error=%v want nil store", err)
	}
}

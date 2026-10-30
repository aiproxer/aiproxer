package bunstore

import (
	"context"
	"sync"
	"testing"
)

func TestRetirementObserverBindingIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := store.CreateALeg(ctx, "shared"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	createErrors := make(chan error, 20)
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.SetALegRetirementObserver(func(string) {})
		}()
		go func() {
			defer wg.Done()
			_, err := store.CreateALeg(ctx, "shared")
			createErrors <- err
		}()
	}
	wg.Wait()
	close(createErrors)
	for err := range createErrors {
		if err != nil {
			t.Fatalf("concurrent CreateALeg: %v", err)
		}
	}
}

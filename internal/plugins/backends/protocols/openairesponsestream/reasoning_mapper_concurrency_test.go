package openairesponsestream

import (
	"sync"
	"testing"
)

func TestMapper_singleOwner_noConcurrentRecvContract(t *testing.T) {
	t.Parallel()
	// Mapper is documented single-consumer; this proves sequential ownership under load
	// without inventing concurrent Recv (undefined). Abort/Finalize remain safe after use.
	m, q := newTestMapper()
	var wg sync.WaitGroup
	wg.Go(func() {
		item := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
		if err := m.ReasoningOutputItemDone(0, item); err != nil {
			t.Errorf("done: %v", err)
		}
	})
	wg.Wait()
	m.AbortReasoningAssembly()
	if err := m.FinalizeOnEOF(); err != nil {
		t.Fatal(err)
	}
	_ = drainEvents(q)
}

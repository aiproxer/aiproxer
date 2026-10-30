package compactiondetect

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// TestContentFree_stateAndEvents proves the detector retains and emits no
// fixture prompt content: after a full strict transaction over a distinctive
// marker payload, neither the emitted events nor the stored per-A-leg state
// contains any part of the payload text (requirements 5.7, 7.3, 8.6). The
// stored state carries only hashes, counts, timestamps, and transaction
// metadata; events carry correlation/evidence metadata only.
func TestContentFree_stateAndEvents(t *testing.T) {
	t.Parallel()
	const secret = "super-secret-compaction-fixture-9f3c"
	d := testDetector(t)

	call := textCall("CONTEXT CHECKPOINT COMPACTION\n"+secret, 0)
	call.Invocation.Operation = aipapi.OperationContextCompaction
	startEvs := d.RequestOpened(reqMeta("tr-cf"), call)
	if len(startEvs) != 1 {
		t.Fatalf("start events=%v", startEvs)
	}
	// Release a strict post marker over the same secret plus an item so both
	// completion paths run; the protocol item completion itself is strict.
	completionEvs := d.ResponseReleased(resMeta("tr-cf"), assistantItem("[CONTEXT SUMMARY]: "+secret))
	completionEvs = append(completionEvs, d.ResponseReleased(resMeta("tr-cf"), aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind: aipapi.ItemKindCompaction, Status: aipapi.ItemStatusCompleted,
		Compaction: &aipapi.CompactionItem{EncapsulatedID: "enc-" + secret},
	}})...)
	all := append(append([]compaction.Event(nil), startEvs...), completionEvs...)
	if len(all) == 0 {
		t.Fatal("no events observed")
	}
	for _, ev := range all {
		if s := fmt.Sprintf("%+v", ev); strings.Contains(s, secret) {
			t.Fatalf("emitted event leaked fixture text: %s", s)
		}
	}

	// Stored per-A-leg state (fingerprint + transaction) must be content-free.
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.legs) != 1 {
		t.Fatalf("legs=%d want 1", len(d.legs))
	}
	for id, ls := range d.legs {
		if ls.releaseText.Len() != 0 {
			t.Fatalf("stored state for leg %q retained %d release-text bytes", id, ls.releaseText.Len())
		}
		stateBytes := fmt.Appendf(nil, "%+v", ls)
		stateBytes = append(stateBytes, ls.releaseText.String()...)
		for _, hash := range ls.lastFP.TailHashes {
			stateBytes = append(stateBytes, hash[:]...)
		}
		stateBytes = append(stateBytes, ls.lastFP.PrefixHash[:]...)
		if bytes.Contains(stateBytes, []byte(secret)) || strings.Contains(string(stateBytes), secret) {
			t.Fatalf("stored state for leg %q leaked fixture text", id)
		}
	}
}

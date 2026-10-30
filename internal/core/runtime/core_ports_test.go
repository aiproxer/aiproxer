package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type corePortsReader struct{ conversationprojection.Reader }

type corePortsWireDetector struct{ CompactionWireDetector }

func TestCorePorts_OverlayPreservesPresenceAndReaderProvenance(t *testing.T) {
	reader := &corePortsReader{}
	base := CorePorts{ConversationViewReader: reader, ConversationReaderStockOrigin: true}
	var boxedNil *corePortsReader
	merged := base.Overlay(CorePorts{ConversationViewReader: boxedNil})
	if merged.ConversationViewReader != boxedNil || merged.ConversationReaderStockOrigin {
		t.Fatal("boxed nil must replace the reader and clear its stock provenance")
	}
	if empty := base.Overlay(CorePorts{}); empty.ConversationViewReader != reader || !empty.ConversationReaderStockOrigin {
		t.Fatal("absent overlay must retain the reader and provenance")
	}
	if base.ConversationViewReader != reader || !base.ConversationReaderStockOrigin {
		t.Fatal("overlay mutated its input")
	}
	called := false
	bootstrap := ConversationBootstrap(func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
		called = true
		return "", nil
	})
	if cause, err := (CorePorts{}).Overlay(CorePorts{ConversationBootstrap: bootstrap}).ConversationBootstrap(t.Context(), "a", nil, nil); err != nil || !called || cause != "" {
		t.Fatal("supplied callback was not retained")
	}
}

func TestCorePorts_CensusPreservesBootstrapAndTypedNilWireDetector(t *testing.T) {
	bootstrap := ConversationBootstrap(func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error) {
		return "", nil
	})
	var detector *corePortsWireDetector
	p := CorePorts{ConversationBootstrap: bootstrap, CompactionDetector: detector}
	census := largebody.NewStandardDependencyCensus("ports")
	p.AddToCensus(&census, false, false, false)
	if !census.Ports.SteeringWriterFactoryOccupied || !census.Ports.CompactionDetectorOccupied || !census.Ports.CompactionDetectorWireSupported {
		t.Fatal("bootstrap blocks before stored steering; boxed detector retains its wire contract")
	}
	if len(census.ExtraPorts) != 0 {
		t.Fatal("existing classified ports changed the dependency inventory")
	}
}

// TestCorePorts_CensusMarksOnlyStoreBackedLocalTurnAndTerminalDecisionWriters
// pins the census derivations the wire authority gate reads: the conversation-view
// tagger is occupied only by a store-backed local turn, and only a store-backed
// terminal-decision plane occupies the steering writer factory. A census that hid
// either writer would let wire execution run a mutating port under the bounded
// fast path.
func TestCorePorts_CensusMarksOnlyStoreBackedLocalTurnAndTerminalDecisionWriters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                          string
		store, localTurn, terminalDec bool
		wantTagger, wantSteering      bool
	}{
		{"store-backed local turn occupies the tagger only", true, true, false, true, false},
		{"store-backed terminal decision occupies the steering writer only", true, false, true, false, true},
		{"no store leaves both writers unoccupied", false, true, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			census := largebody.NewStandardDependencyCensus("ports")
			CorePorts{}.AddToCensus(&census, tc.store, tc.localTurn, tc.terminalDec)
			if census.Ports.ConversationViewTaggerOccupied != tc.wantTagger {
				t.Errorf("ConversationViewTaggerOccupied=%v want %v", census.Ports.ConversationViewTaggerOccupied, tc.wantTagger)
			}
			if census.Ports.SteeringWriterFactoryOccupied != tc.wantSteering {
				t.Errorf("SteeringWriterFactoryOccupied=%v want %v", census.Ports.SteeringWriterFactoryOccupied, tc.wantSteering)
			}
		})
	}
}

package runtime

import (
	"reflect"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// CorePorts is the generation's fixed, consumer-owned execution port value.
// Copies retain port identity; the value itself is frozen before publication.
// New fields participate in overlay and conservatively block wire execution
// when occupied. Tags retain the existing ports' special composition rules.
type CorePorts struct {
	CompactionDetector            CompactionDetector            `census:"known"`
	ConversationViewReader        conversationprojection.Reader `census:"known"`
	ConversationReaderStockOrigin bool                          `overlay:"ConversationViewReader" census:"known"`
	ConversationBootstrap         ConversationBootstrap         `census:"known"`
	InterleavedProcessor          InterleavedProcessor          `census:"known"`
	PromptCacheMaintenance        PromptCacheMaintenance        `overlay:"keep" census:"known"`
	TerminalPolicyReader          TerminalPolicyReader          `overlay:"keep" census:"known"`
	// AttemptGuard is the optional generic pre-open invariant check the
	// executor consults immediately before the provider open (design §2).
	//
	// It is a known port: it is consulted on the canonical open path only,
	// and the wire fast path carries no canonical call, so occupying it does
	// not change the wire disposition.
	AttemptGuard AttemptGuard `census:"known"`
}

// Overlay replaces supplied ports without mutating either input. Presence is
// tested on the interface slot, so a boxed typed nil still replaces a port.
// Reader provenance follows its reader even when the replacement is false.
// Reflection is confined to generation construction, never request execution.
func (p CorePorts) Overlay(overlay CorePorts) CorePorts {
	dst, src := reflect.ValueOf(&p).Elem(), reflect.ValueOf(overlay)
	for i := range dst.NumField() {
		rule := dst.Type().Field(i).Tag.Get("overlay")
		if rule == "keep" {
			continue
		}
		presence := src.Field(i)
		if rule != "" {
			presence = src.FieldByName(rule)
		}
		if !presence.IsZero() {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return p
}

// AddToCensus derives execution-port facts from the same frozen value the
// executor consumes. Reachability of store-backed writers still depends on
// local-turn and terminal-decision planes, not just a nonnil store.
func (p CorePorts) AddToCensus(c *largebody.DependencyCensus, store, localTurn, terminalDecision bool) {
	c.Ports.ConversationViewReaderOccupied = p.ConversationViewReader != nil
	c.Ports.ConversationReaderFreshALegSupported = p.ConversationViewReader != nil && p.ConversationReaderStockOrigin
	c.Ports.ConversationViewTaggerOccupied = store && localTurn
	c.Ports.SteeringWriterFactoryOccupied = p.ConversationBootstrap != nil || (store && (p.InterleavedProcessor != nil || terminalDecision))
	c.Ports.InterleavedProcessorOccupied = p.InterleavedProcessor != nil
	c.Ports.CompactionDetectorOccupied = p.CompactionDetector != nil
	_, c.Ports.CompactionDetectorWireSupported = p.CompactionDetector.(CompactionWireDetector)
	v := reflect.ValueOf(p)
	for i := range v.NumField() {
		if f := v.Type().Field(i); f.Tag.Get("census") == "" && !v.Field(i).IsZero() {
			c.AddPort("core."+f.Name, true)
		}
	}
}

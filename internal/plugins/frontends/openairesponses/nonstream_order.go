package openairesponses

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type nonstreamOrderKind byte

const (
	nonstreamOrderMessage nonstreamOrderKind = iota
	nonstreamOrderExactReasoning
	nonstreamOrderTool
)

type nonstreamOrderMarker struct {
	kind         nonstreamOrderKind
	exactOrdinal int
	toolCallID   string
}

type nonstreamOutputOrder struct {
	markers       []nonstreamOrderMarker
	messagePlaced bool
	exactCount    int
	seenTools     map[string]struct{}
}

func (o *nonstreamOutputOrder) observe(ev aipapi.Event) {
	switch ev.Kind {
	case aipapi.EventTextDelta, aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
		if o.messagePlaced {
			return
		}
		o.messagePlaced = true
		o.markers = append(o.markers, nonstreamOrderMarker{kind: nonstreamOrderMessage})
	case aipapi.EventReasoningPart:
		if ev.Reasoning == nil {
			return
		}
		if aipapi.NormalizeReasoningDialect(ev.Reasoning.Dialect) != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
			return
		}
		o.markers = append(o.markers, nonstreamOrderMarker{
			kind:         nonstreamOrderExactReasoning,
			exactOrdinal: o.exactCount,
		})
		o.exactCount++
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta:
		id := ev.ToolCallID
		if id == "" {
			return
		}
		if o.seenTools == nil {
			o.seenTools = make(map[string]struct{})
		}
		if _, ok := o.seenTools[id]; ok {
			return
		}
		o.seenTools[id] = struct{}{}
		o.markers = append(o.markers, nonstreamOrderMarker{
			kind:       nonstreamOrderTool,
			toolCallID: id,
		})
	}
}

type orderTeeStream struct {
	inner aipapi.EventStream
	order *nonstreamOutputOrder
}

func (s *orderTeeStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err != nil {
		return ev, err
	}
	s.order.observe(ev)
	return ev, nil
}

func (s *orderTeeStream) Close() error {
	return s.inner.Close()
}

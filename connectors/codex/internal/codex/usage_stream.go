package codex

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type usageEstimatingStream struct {
	base     aipapi.ManagedEventStream
	est      *usageEstimator
	call     aipapi.Call
	model    string
	text     strings.Builder
	textFull bool
	sawUsage bool
	queued   *aipapi.Event
}

const maxUsageEstimateOutputBytes = 1 << 20

func newUsageEstimatingStream(base aipapi.ManagedEventStream, est *usageEstimator, call aipapi.Call, model string) aipapi.ManagedEventStream {
	if est == nil {
		return base
	}
	return &usageEstimatingStream{
		base:  base,
		est:   est,
		call:  call,
		model: strings.TrimSpace(model),
	}
}

func (s *usageEstimatingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.queued != nil {
		ev := *s.queued
		s.queued = nil
		return ev, nil
	}
	ev, err := s.base.Recv(ctx)
	if err != nil {
		return ev, err
	}
	s.observe(ev)
	if ev.Kind != aipapi.EventResponseFinished || s.sawUsage || s.textFull {
		return ev, nil
	}
	usage, err := s.est.estimateUsage(ctx, s.call, s.model, s.text.String())
	if err != nil {
		return ev, nil
	}
	s.queued = &ev
	return usage, nil
}

func (s *usageEstimatingStream) observe(ev aipapi.Event) {
	switch ev.Kind {
	case aipapi.EventTextDelta:
		if s.textFull {
			return
		}
		if s.text.Len()+len(ev.Delta) > maxUsageEstimateOutputBytes {
			s.textFull = true
			return
		}
		s.text.WriteString(ev.Delta)
	case aipapi.EventUsageDelta:
		s.sawUsage = true
	}
}

func (s *usageEstimatingStream) Close() error {
	return s.base.Close()
}

func (s *usageEstimatingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return s.base.Cancel(ctx, cause)
}

func (s *usageEstimatingStream) DrainAccountWindowSnapshots() []AccountWindowSnapshot {
	if source, ok := s.base.(AccountWindowSnapshotSource); ok {
		return source.DrainAccountWindowSnapshots()
	}
	return nil
}

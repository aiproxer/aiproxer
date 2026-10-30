package anthropicmessages

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

type stallDecoderAnthropic struct {
	enteredNext chan struct{}
	release     chan struct{}
	closeOnce   sync.Once
	err         error
}

func (d *stallDecoderAnthropic) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)}
}

func (d *stallDecoderAnthropic) Next() bool {
	if d.enteredNext != nil {
		select {
		case d.enteredNext <- struct{}{}:
		default:
		}
	}
	<-d.release
	return false
}

func (d *stallDecoderAnthropic) Close() error {
	d.closeOnce.Do(func() {
		close(d.release)
	})
	return nil
}

func (d *stallDecoderAnthropic) Err() error {
	return d.err
}

type instantDecoderAnthropic struct{}

func (d *instantDecoderAnthropic) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)}
}

func (d *instantDecoderAnthropic) Next() bool { return false }

func (d *instantDecoderAnthropic) Close() error { return nil }

func (d *instantDecoderAnthropic) Err() error { return nil }

func TestMsgStream_CloseConcurrentWhileRecvBlocked(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	dec := &stallDecoderAnthropic{enteredNext: make(chan struct{}, 1), release: release}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = s.Recv(context.Background())
	})

	waitTimer := time.NewTimer(2 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-dec.enteredNext:
	case <-waitTimer.C:
		t.Fatal("Recv did not reach sdk.Next")
	}

	const n = 32
	var closes sync.WaitGroup
	for range n {
		closes.Go(func() {
			_ = s.Close()
		})
	}
	closes.Wait()
	wg.Wait()
}

func TestMsgStream_CloseConcurrentAfterEOF(t *testing.T) {
	t.Parallel()
	dec := &instantDecoderAnthropic{}
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](dec, nil)
	es := newMessageStream(sdk, "anthropic", 0)
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("recv1: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("recv1 kind: %v", ev.Kind)
	}
	if _, err := s.Recv(context.Background()); err != io.EOF {
		t.Fatalf("recv2: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_ = s.Close()
		})
	}
	wg.Wait()
}

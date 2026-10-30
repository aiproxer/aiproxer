package openailegacy

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

type stallDecoderLegacy struct {
	enteredNext chan struct{}
	release     chan struct{}
	closeOnce   sync.Once
	err         error
}

func (d *stallDecoderLegacy) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[]}`)}
}

func (d *stallDecoderLegacy) Next() bool {
	if d.enteredNext != nil {
		select {
		case d.enteredNext <- struct{}{}:
		default:
		}
	}
	<-d.release
	return false
}

func (d *stallDecoderLegacy) Close() error {
	d.closeOnce.Do(func() {
		close(d.release)
	})
	return nil
}

func (d *stallDecoderLegacy) Err() error {
	return d.err
}

type instantDecoderLegacy struct{}

func (d *instantDecoderLegacy) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[]}`)}
}

func (d *instantDecoderLegacy) Next() bool { return false }

func (d *instantDecoderLegacy) Close() error { return nil }

func (d *instantDecoderLegacy) Err() error { return nil }

func TestChatStream_CloseConcurrentWhileRecvBlocked(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	dec := &stallDecoderLegacy{enteredNext: make(chan struct{}, 1), release: release}
	sdk := ssestream.NewStream[openai.ChatCompletionChunk](dec, nil)
	es := NewChatStream(sdk, 0)
	s, ok := es.(*chatStream)
	if !ok {
		t.Fatalf("newChatStream returned %T", es)
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

func TestChatStream_CloseConcurrentAfterEOF(t *testing.T) {
	t.Parallel()
	dec := &instantDecoderLegacy{}
	sdk := ssestream.NewStream[openai.ChatCompletionChunk](dec, nil)
	es := NewChatStream(sdk, 0)
	s, ok := es.(*chatStream)
	if !ok {
		t.Fatalf("newChatStream returned %T", es)
	}
	if _, err := s.Recv(context.Background()); err != io.EOF {
		t.Fatalf("recv: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_ = s.Close()
		})
	}
	wg.Wait()
}

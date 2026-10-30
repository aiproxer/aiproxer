package openairesponses

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type stallDecoder struct {
	enteredNext chan struct{}
	release     chan struct{}
	closeOnce   sync.Once
	err         error
}

func (d *stallDecoder) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"response.in_progress","sequence_number":0}`)}
}

func (d *stallDecoder) Next() bool {
	if d.enteredNext != nil {
		select {
		case d.enteredNext <- struct{}{}:
		default:
		}
	}
	<-d.release
	return false
}

func (d *stallDecoder) Close() error {
	d.closeOnce.Do(func() {
		close(d.release)
	})
	return nil
}

func (d *stallDecoder) Err() error {
	return d.err
}

type instantDecoder struct{}

func (d *instantDecoder) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"response.in_progress","sequence_number":0}`)}
}

func (d *instantDecoder) Next() bool { return false }

func (d *instantDecoder) Close() error { return nil }

func (d *instantDecoder) Err() error { return nil }

func TestSDKStream_CloseConcurrentWhileRecvBlocked(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	dec := &stallDecoder{enteredNext: make(chan struct{}, 1), release: release}
	sdk := ssestream.NewStream[responses.ResponseStreamEventUnion](dec, nil)
	es := NewSDKStream(sdk, 0)
	s, ok := es.(*sdkStream)
	if !ok {
		t.Fatalf("newSDKStream returned %T", es)
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

func TestSDKStream_CloseConcurrentAfterEOF(t *testing.T) {
	t.Parallel()
	dec := &instantDecoder{}
	sdk := ssestream.NewStream[responses.ResponseStreamEventUnion](dec, nil)
	es := NewSDKStream(sdk, 0)
	s, ok := es.(*sdkStream)
	if !ok {
		t.Fatalf("newSDKStream returned %T", es)
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

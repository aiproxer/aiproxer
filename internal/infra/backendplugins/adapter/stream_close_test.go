package adapter

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestBridgeExecuteStream_CloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()
	stream := &bridgeExecuteStream{ctx: context.Background(), closeCh: make(chan struct{})}
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for range 32 {
		wg.Go(func() {
			if err := stream.Close(); err != nil {
				errCh <- err
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("Close() error = %v", err)
	}
	select {
	case <-stream.closeCh:
	default:
		t.Fatal("Close did not close the unblock channel")
	}
}

func TestBridgeExecuteStream_CloseUnblocksBlockedRecv(t *testing.T) {
	t.Parallel()
	stream := &bridgeExecuteStream{
		ctx:     context.Background(),
		closeCh: make(chan struct{}),
		recv:    make(chan backendplugin.ClientFrame),
	}
	type recvResult struct {
		frame backendplugin.ClientFrame
		err   error
	}
	resCh := make(chan recvResult, 1)
	go func() {
		frame, err := stream.Recv()
		resCh <- recvResult{frame: frame, err: err}
	}()

	if err := stream.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	select {
	case res := <-resCh:
		if !errors.Is(res.err, io.EOF) {
			t.Fatalf("Recv() error = %v, want io.EOF", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv() did not unblock after Close()")
	}
}

func TestBridgeExecuteStream_RecvAfterClose(t *testing.T) {
	t.Parallel()
	stream := &bridgeExecuteStream{
		ctx:     context.Background(),
		closeCh: make(chan struct{}),
		recv:    make(chan backendplugin.ClientFrame),
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	_, err := stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() error = %v, want io.EOF", err)
	}
}

func TestManagedStream_CloseDeliversCloseInputUnderBackpressure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostFrames := make(chan backendplugin.ClientFrame, 1)
	hostFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart}

	done := make(chan struct{})
	s := &managedStream{
		ctx:        ctx,
		cancel:     cancel,
		hostFrames: hostFrames,
		done:       done,
	}

	var (
		receivedCloseInput atomic.Bool
		wg                 sync.WaitGroup
	)
	wg.Go(func() {
		time.Sleep(20 * time.Millisecond)
		for f := range hostFrames {
			if f.Kind == backendplugin.ClientFrameCloseInput {
				receivedCloseInput.Store(true)
				break
			}
		}
	})

	if err := s.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	close(hostFrames)
	wg.Wait()

	if !receivedCloseInput.Load() {
		t.Fatal("managedStream.Close() dropped ClientFrameCloseInput under backpressure instead of waiting for channel capacity")
	}
}

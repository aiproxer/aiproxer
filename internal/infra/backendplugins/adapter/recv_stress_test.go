package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestRecv_StressEventBeforeError(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeCommitThenFail}
	stream := openFakeStream(t, fake, "stress-commit")
	t.Cleanup(func() { _ = stream.Close() })

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("first recv: %v", err)
	}
	if ev.Kind != aipapi.EventTextDelta || !aipapi.OutputCommitted(ev) {
		t.Fatalf("want committed text, got %+v", ev)
	}
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected post-commit error")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) || !ce.OutputCommitted || ce.Retryable {
		t.Fatalf("classified=%+v err=%v", ce, err)
	}
}

func TestRecv_StressErrorOnly(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeProcessExit}
	stream := openFakeStream(t, fake, "stress-err")
	t.Cleanup(func() { _ = stream.Close() })
	_, err := stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected error-only failure")
	}
}

func TestRecv_StressCancel(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeBlockedCancel, SlowWait: time.Hour}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "stress-cancel", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated int
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "stress-cancel",
		InvalidateGeneration: func() { invalidated++ },
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := br.Backend.Open(ctx, testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	cancel()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cancel error")
		default:
		}
		_, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
	}
	if invalidated != 0 {
		t.Fatalf("cancel must not invalidate, got %d", invalidated)
	}
}

func TestRecv_StressChannelCloseEOF(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	stream := openFakeStream(t, fake, "stress-eof")
	t.Cleanup(func() { _ = stream.Close() })
	saw := 0
	for {
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected err before EOF: %v", err)
		}
		saw++
		if saw > 100 {
			t.Fatal("too many events without EOF")
		}
	}
	if saw == 0 {
		t.Fatal("expected at least one event before EOF")
	}
}

func openFakeStream(t *testing.T, fake *testkit.FakeService, id string) aipapi.ManagedEventStream {
	t.Helper()
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: id, FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: id})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

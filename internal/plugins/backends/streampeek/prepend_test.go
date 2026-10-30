package streampeek_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/streampeek"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewPrependFirst_recvOrder(t *testing.T) {
	t.Parallel()
	first := aipapi.Event{Kind: aipapi.EventMessageStarted}
	rest := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventResponseFinished},
	})
	es := streampeek.NewPrependFirst(first, rest)
	ctx := context.Background()
	ev1, err := es.Recv(ctx)
	if err != nil || ev1.Kind != aipapi.EventMessageStarted {
		t.Fatalf("first: %+v err=%v", ev1, err)
	}
	ev2, err := es.Recv(ctx)
	if err != nil || ev2.Delta != "x" {
		t.Fatalf("second: %+v err=%v", ev2, err)
	}
	ev3, err := es.Recv(ctx)
	if err != nil || ev3.Kind != aipapi.EventResponseFinished {
		t.Fatalf("third: %+v err=%v", ev3, err)
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewPrependFirst_nilRest_secondRecvEOF(t *testing.T) {
	t.Parallel()
	first := aipapi.Event{Kind: aipapi.EventMessageStarted}
	es := streampeek.NewPrependFirst(first, nil)
	ctx := context.Background()
	_, err := es.Recv(ctx)
	if err != nil {
		t.Fatalf("first recv: %v", err)
	}
	_, err = es.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second recv: want io.EOF, got %v", err)
	}
}

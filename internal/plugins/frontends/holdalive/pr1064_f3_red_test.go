package holdalive_test

// PR #1064 review finding F3 (RED): a panic from the abandoned-stream
// disposer in the producer goroutine terminates the whole process, because
// no goroutine recovers it. Per testing policy this process-terminating case
// runs as its OWN PROCESS: the parent spawns the child and requires a clean
// exit. On the current code the child dies with a goroutine panic (nonzero
// exit); after the fix it exits 0.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/holdalive"
)

func TestPR1064_F3_ProducerDiscardPanicContained(t *testing.T) {
	if os.Getenv("PR1064_F3_CHILD") == "1" {
		pr1064F3Child()
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestPR1064_F3_ProducerDiscardPanicContained", "-test.count=1")
	cmd.Env = append(os.Environ(), "PR1064_F3_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("producer discard panic was not contained (child exit: %v); output:\n%s", err, out)
	}
	if strings.Contains(string(out), "panic:") {
		t.Fatalf("child logged a panic despite clean exit; output:\n%s", out)
	}
}

func pr1064F3Child() {
	ctx, cancel := context.WithCancel(context.Background())
	sentinel := errors.New("pr1064 f3 discard panic")
	discard := func(v string) {
		panic(sentinel)
	}
	returned := make(chan error, 1)
	go func() {
		_, err := holdalive.Wait(ctx, &recordingWriter{}, holdalive.Config{Enabled: true, Interval: time.Hour},
			func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "late-value", nil
			}, discard)
		returned <- err
	}()
	// Let the producer block inside fn, then cancel: the caller abandons the
	// rendezvous and the producer owns disposal of the late result. The
	// abandoned branch calls discard, which panics. Without containment the
	// process dies here with an unrecovered goroutine panic.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-returned:
	case <-time.After(15 * time.Second):
		println("FATAL: Wait did not return after cancellation")
		os.Exit(3)
	}
	// Give the producer goroutine a moment to reach discard before exiting;
	// an unrecovered panic crashes the process before this returns.
	time.Sleep(500 * time.Millisecond)
}

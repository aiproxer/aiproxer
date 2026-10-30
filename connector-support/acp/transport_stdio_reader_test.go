package acp

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// readDeadline bounds every assertion that waits for a read to complete, so a
// future regression that leaves the reader blocked fails this test instead of
// the package's whole timeout.
const readDeadline = 2 * time.Second

// lineRead is one completed Read result.
type lineRead struct {
	line string
	err  error
}

// windowReader returns a reader whose test seam blocks the first Read between
// the buffered fast path and the blocking receive, and reports when that window
// is reached. The caller releases it by closing the returned channel.
func windowReader(t *testing.T) (*lineChannelReader, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	reader := newLineChannelReader()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	reader.betweenSelects = func() {
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	return reader, reached, release
}

// readAsync starts one Read and returns its result channel.
func readAsync(reader *lineChannelReader) <-chan lineRead {
	read := make(chan lineRead, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := reader.Read(buf)
		read <- lineRead{line: string(buf[:n]), err: err}
	}()
	return read
}

// awaitRead waits for one read result without letting a blocked reader hang the
// test binary.
func awaitRead(t *testing.T, msg string, read <-chan lineRead) lineRead {
	t.Helper()
	select {
	case got := <-read:
		return got
	case <-time.After(readDeadline):
		t.Fatalf("%s: the read did not complete", msg)
		return lineRead{}
	}
}

// Source: #1357 follow-up. finish() closes done after enqueueing the terminal
// line, and that can happen in the window between the buffered fast path and
// the blocking receive. Both cases are then ready, and EOF must not preempt a
// line the caller was promised: the terminal response is what ends the stream.
func TestLineChannelReader_FinishDoesNotPreemptTheBufferedLine(t *testing.T) {
	// Once both cases are ready the runtime picks between them at random, so a
	// single iteration exposes the defect about half the time. A real stream
	// also has to reach this window at all, which is why it loses its terminal
	// response only rarely; the repetition makes the regression reliable
	// without depending on timing.
	for i := range 200 {
		reader, reached, release := windowReader(t)
		read := readAsync(reader)
		<-reached

		// The terminal line and the finish signal become ready together, in the
		// window the reader is paused in.
		reader.enqueue([]byte("terminal"))
		reader.finish()
		close(release)

		got := awaitRead(t, "the buffered line", read)
		if got.err != nil {
			t.Fatalf("iteration %d: Read = %v, want the buffered line before EOF", i, got.err)
		}
		if got.line != "terminal\n" {
			t.Fatalf("iteration %d: line = %q, want %q", i, got.line, "terminal\n")
		}
		// Only after the buffered line does the finished stream report EOF.
		if eof := awaitRead(t, "the finished stream", readAsync(reader)); !errors.Is(eof.err, io.EOF) {
			t.Fatalf("iteration %d: after the terminal line, err = %v, want EOF", i, eof.err)
		}
		_ = reader.Close()
	}
}

// Source: #1357 follow-up. Every line enqueued before finish() survives the
// window, in order, before EOF.
func TestLineChannelReader_FinishDrainsEveryEnqueuedLine(t *testing.T) {
	want := "one\ntwo\nthree\n"
	for i := range 100 {
		reader, reached, release := windowReader(t)
		go func() {
			<-reached
			for _, line := range []string{"one", "two", "three"} {
				reader.enqueue([]byte(line))
			}
			reader.finish()
			close(release)
		}()

		type allRead struct {
			all string
			err error
		}
		drained := make(chan allRead, 1)
		go func() {
			all, err := io.ReadAll(reader)
			drained <- allRead{all: string(all), err: err}
		}()

		select {
		case got := <-drained:
			if got.err != nil {
				t.Fatalf("iteration %d: ReadAll: %v", i, got.err)
			}
			if got.all != want {
				t.Fatalf("iteration %d: stream = %q, want %q", i, got.all, want)
			}
		case <-time.After(readDeadline):
			t.Fatalf("iteration %d: the stream did not reach EOF", i)
		}
		_ = reader.Close()
	}
}

// Source: #1357 follow-up. A finished stream with nothing buffered still
// reports EOF promptly.
func TestLineChannelReader_FinishWithoutLinesReportsEOF(t *testing.T) {
	reader := newLineChannelReader()
	t.Cleanup(func() { _ = reader.Close() })
	reader.finish()

	if got := awaitRead(t, "the finished stream", readAsync(reader)); !errors.Is(got.err, io.EOF) {
		t.Fatalf("Read = %q, %v, want EOF", got.line, got.err)
	}
}

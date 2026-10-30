package runtime

// PR #1064 review finding F2 regression pin: Recv EOF, non-EOF receive
// errors, and receive panics must close the inner stream before releasing the
// replay root (Requirement 1.3). #1067 fixed the teardown ordering, so the
// cases below now hold it: each one requires inner-close to precede root
// release, and an inverted teardown fails it. Close() is the reference
// ordering.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type pr1064F2OrderStream struct {
	mu        sync.Mutex
	events    *[]string
	recvErr   error
	recvPanic any
}

func (s *pr1064F2OrderStream) Recv(context.Context) (aipapi.Event, error) {
	if s.recvPanic != nil {
		panic(s.recvPanic)
	}
	if s.recvErr != nil {
		return aipapi.Event{}, s.recvErr
	}
	return aipapi.Event{}, io.EOF
}

func (s *pr1064F2OrderStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "inner-close")
	return nil
}

func pr1064F2OrderOf(events []string, first, second string) bool {
	fi, si := -1, -1
	for i, ev := range events {
		if fi < 0 && ev == first {
			fi = i
		}
		if ev == second {
			si = i
		}
	}
	return fi >= 0 && si >= 0 && fi < si
}

func TestPR1064_F2_RecvClosesInnerBeforeRootRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		recvErr   error
		recvPanic any
		wantPanic bool
	}{
		{name: "eof", recvErr: io.EOF},
		{name: "error", recvErr: errors.New("pr1064 f2 backend recv failure")},
		{name: "panic", recvPanic: errors.New("pr1064 f2 inner recv panic"), wantPanic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var events []string
			record := func(ev string) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, ev)
			}
			inner := &pr1064F2OrderStream{events: &events, recvErr: tt.recvErr, recvPanic: tt.recvPanic}
			wles := &wireLifecycleEventStream{
				EventStream: inner,
				cleanup: func(err error) {
					record("root-release")
				},
			}
			if tt.wantPanic {
				var panicked any
				func() {
					defer func() { panicked = recover() }()
					_, _ = wles.Recv(context.Background())
				}()
				if panicked == nil {
					t.Fatal("inner recv panic did not propagate")
				}
			} else {
				_, err := wles.Recv(context.Background())
				if !errors.Is(err, tt.recvErr) {
					t.Fatalf("err=%v want %v", err, tt.recvErr)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !pr1064F2OrderOf(events, "inner-close", "root-release") {
				t.Fatalf("%s teardown order=%v want [inner-close root-release]", tt.name, events)
			}
		})
	}
}

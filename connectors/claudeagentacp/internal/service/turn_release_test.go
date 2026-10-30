package service

import (
	"errors"
	"io"
	"testing"
	"testing/synctest"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// drain reads a turn to its end without closing the stream, so the caller can
// observe the window in which the runtime is still claimed.
func drain(t *testing.T, stream aipapi.ManagedEventStream) {
	t.Helper()
	for {
		if _, err := stream.Recv(t.Context()); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatal(err)
		}
	}
}

type turnResult struct {
	stream aipapi.ManagedEventStream
	err    error
}

// openWhileClaimed opens a turn while the previous turn of the same
// conversation still holds its runtime. The core publishes the client-visible
// response before it closes the backend stream, so the next turn of a
// conversation routinely lands in that window.
//
// The opened turn must wait for the release instead of being refused, and it
// must succeed once the release arrives.
func openWhileClaimed(t *testing.T, inst *instance, previous aipapi.ManagedEventStream, next *aipapi.Call) aipapi.ManagedEventStream {
	t.Helper()
	release := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-release
		_ = previous.Close()
	}()

	results := make(chan turnResult, 1)
	go func() {
		opened, err := inst.eng.Open(t.Context(), next)
		results <- turnResult{opened, err}
	}()
	synctest.Wait()

	refused := false
	select {
	case got := <-results:
		refused = true
		t.Errorf("the turn did not wait for the release: %v", got.err)
	default:
	}

	close(release)
	synctest.Wait()
	got := <-results
	<-closed
	if refused {
		t.Fatal("the turn was not served while the previous turn still held the runtime")
	}
	if got.err != nil {
		t.Fatalf("the turn failed: %v", got.err)
	}
	return got.stream
}

// A turn that arrives while the previous turn of the same conversation still
// holds its runtime must wait for that release instead of failing.
func TestService_SecondTurnWaitsForTheFirstTurnToRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		inst := configureFixture(t, svc, "api_key", "turn_wait_seconds: 30\n")
		stream, err := inst.eng.Open(t.Context(), turnCall())
		if err != nil {
			t.Fatal(err)
		}
		drain(t, stream)

		second := turnCall()
		second.Messages = append(second.Messages,
			aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("ok")}},
			aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
		)
		consume(t, openWhileClaimed(t, inst, stream, second))
		if adapter.starts != 1 {
			t.Fatalf("the second turn spawned a replacement runtime: starts=%d", adapter.starts)
		}
	})
}

// An identical retry of a served turn must succeed. The retry lands in the
// window in which the previous turn still holds its runtime, so it proves both
// the bounded wait and that the retry resolves to the same conversation.
func TestService_IdenticalRetrySucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		inst := configureFixture(t, svc, "api_key", "turn_wait_seconds: 30\n")
		call := turnCall()
		stream, err := inst.eng.Open(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		drain(t, stream)

		retry := turnCall()
		retry.ID = call.ID
		consume(t, openWhileClaimed(t, inst, stream, retry))
		if adapter.starts != 1 {
			t.Fatalf("the identical retry spawned a replacement runtime: starts=%d", adapter.starts)
		}
	})
}

// Two concurrent conversations must not share one agent process: the second
// conversation owns its own runtime and is never refused for the first
// conversation's turn.
func TestService_ConcurrentConversationsUseSeparateRuntimes(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key", "turn_wait_seconds: 1\n")
	first := &aipapi.Call{ID: "one", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first question")}}}}
	firstStream, err := inst.eng.Open(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	// The first conversation still holds its runtime while the second one runs.
	second := &aipapi.Call{ID: "two", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second question")}}}}
	secondStream, err := inst.eng.Open(t.Context(), second)
	if err != nil {
		t.Fatalf("the second conversation was refused: %v", err)
	}
	consume(t, secondStream)
	if err := firstStream.Close(); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 2 {
		t.Fatalf("the conversations shared one runtime: starts=%d", adapter.starts)
	}
}

// The proxy-owned session id the host injects is the conversation identity:
// the turns of one session share one agent process, and two sessions never do.
func TestService_ProxyOwnedSessionIDScopesTheAgentRuntime(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key", "turn_wait_seconds: 1\n")
	serve := func(session, request string) {
		inv := backendplugin.Invocation{
			RequestID: request, AttemptID: "attempt", ALegID: "a", BLegID: "b",
			NativeModelID: "future-family-17-3", CanonicalModelID: "anthropic/future-family-17.3",
			ProxyOwnedSessionID: session,
			Messages:            []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("hi " + session)}}},
		}
		wire := &executeCapture{ctx: t.Context(), invocation: &inv}
		if err := inst.Execute(wire); err != nil {
			t.Fatal(err)
		}
	}
	serve("session-one", "turn-1")
	serve("session-one", "turn-2")
	if adapter.starts != 1 {
		t.Fatalf("the turns of one session did not share one agent process: starts=%d", adapter.starts)
	}
	serve("session-two", "turn-3")
	if adapter.starts != 2 {
		t.Fatalf("the sessions shared one agent process: starts=%d", adapter.starts)
	}
}

// The runtime-reuse policy for a client that carries no session authority:
// every request is isolated on its own agent process, because the host gives a
// stateless request a new session. Two requests that open with the same text
// are still two conversations and must never share agent state.
func TestService_StatelessRequestsAreIsolatedPerRequest(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key", "turn_wait_seconds: 1\n")
	open := func(request string) {
		call := &aipapi.Call{ID: request, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
		stream, err := inst.eng.Open(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		consume(t, stream)
	}
	open("same-trace-id")
	open("same-trace-id")
	open("")
	open("")
	if adapter.starts != 4 {
		t.Fatalf("the stateless requests shared one agent process: starts=%d", adapter.starts)
	}
}

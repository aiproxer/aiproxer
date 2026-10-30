package host_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
)

// TestSession_ExecuteSerializesPumpSendAgainstTerminalCloseSend pins the fix
// for the SendMsg-vs-CloseSend data race: the input pump keeps Sending client
// frames while the receive loop CloseSends as soon as the terminal frame
// arrives. It passes non-race locally by construction; under -race (Linux CI)
// it fails if Send and CloseSend ever execute concurrently again.
func TestSession_ExecuteSerializesPumpSendAgainstTerminalCloseSend(t *testing.T) {
	t.Parallel()
	plugin := &publicFake{terminalOnStart: true}
	conn := startPublicFake(t, plugin)
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "send-close-race", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	frames := []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: "send-close-race", Invocation: validInvocation()},
	}
	for range 200 {
		frames = append(frames, backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCancel, InstanceID: "send-close-race", CancelReason: backendplugin.CancelReasonHost})
	}
	stream := &publicStream{ctx: context.Background(), frames: frames}
	if err := sess.Execute(stream); err != nil {
		t.Fatalf("Execute error = %v, want nil after terminal", err)
	}
	if len(stream.out) != 1 || stream.out[0].Kind != backendplugin.ServerFrameTerminal {
		t.Fatalf("forwarded frames = %+v, want exactly one terminal", stream.out)
	}
}

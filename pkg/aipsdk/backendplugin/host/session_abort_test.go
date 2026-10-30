package host

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Close's cancellation path releases the local transport even when there are
// no active Execute calls. It must not require a successful CloseInstance RPC.
func TestSession_CanceledCloseDisposesIdleTransportWithoutRPC(t *testing.T) {
	t.Parallel()
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	client := &canceledCloseClient{}
	s := &Session{client: client, conn: conn, lifecycle: newSessionLifecycle()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close: %v", err)
	}
	if conn.GetState() != connectivity.Shutdown {
		t.Fatal("canceled Close left the idle client connection open")
	}
	if client.calls.Load() != 0 {
		t.Fatal("transport-only cancellation issued a CloseInstance RPC")
	}
}

type canceledCloseClient struct {
	backendpluginv1.BackendPluginClient
	calls atomic.Int64
}

func (c *canceledCloseClient) CloseInstance(ctx context.Context, _ *backendpluginv1.CloseInstanceRequest, _ ...grpc.CallOption) (*backendpluginv1.CloseInstanceResponse, error) {
	c.calls.Add(1)
	return nil, ctx.Err()
}

package backendplugin_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func envOf(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func noPipe(string) (net.Conn, error) { return nil, errors.New("unexpected pipe dial") }

func TestServe_PipeChannelServesHostUntilCanceled(t *testing.T) {
	t.Parallel()
	serverSide, hostSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ServeWithForTest(ctx, &stubService{}, backendplugin.ServeOptions{Name: "aip-backend-stub"},
			envOf(map[string]string{backendplugin.EnvChannelPipe: `\\.\pipe\aip-test`}),
			func(name string) (net.Conn, error) {
				if name != `\\.\pipe\aip-test` {
					t.Errorf("pipe name = %q", name)
				}
				return serverSide, nil
			})
	}()

	conn, err := grpc.NewClient("passthrough:///host",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return hostSide, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	negotiateOK(t, backendpluginv1.NewBackendPluginClient(conn))
	_ = conn.Close()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServe_ChannelErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		svc  backendplugin.Service
		env  map[string]string
		dial func(string) (net.Conn, error)
		want string
	}{
		{"nil service", nil, nil, noPipe, "nil service"},
		{"describe failure", failingDescribeService{}, nil, noPipe, "describe: boom"},
		{
			"pipe dial failure", &stubService{},
			map[string]string{backendplugin.EnvChannelPipe: "p"},
			func(string) (net.Conn, error) { return nil, errors.New("pipe unavailable") }, "pipe dial: pipe unavailable",
		},
		{"invalid fd", &stubService{}, map[string]string{backendplugin.EnvChannelFD: "-3"}, noPipe, "invalid descriptor"},
	}
	for _, tc := range cases {
		err := backendplugin.ServeWithForTest(context.Background(), tc.svc, backendplugin.ServeOptions{}, envOf(tc.env), tc.dial)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestServe_LoopbackAnnouncesAddressAndStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	stderr := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ServeWithForTest(ctx, &stubService{}, backendplugin.ServeOptions{Name: "aip-backend-stub", Stderr: stderr}, envOf(nil), noPipe)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "aip-backend-stub listening on 127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("no loopback announcement; stderr=%q", stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after cancel")
	}
}

type failingDescribeService struct{ backendplugin.Service }

func (failingDescribeService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{}, errors.New("boom")
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A listener failure on a host channel must surface instead of being reported
// as a clean connector shutdown; the loopback TCP path already returns it.
func TestServe_ChannelListenerFailureSurfaces(t *testing.T) {
	t.Parallel()
	serverSide, hostSide := net.Pipe()
	t.Cleanup(func() {
		_ = serverSide.Close()
		_ = hostSide.Close()
	})
	err := backendplugin.ServeWithListenerForTest(
		context.Background(), &stubService{}, backendplugin.ServeOptions{Name: "aip-backend-stub"},
		envOf(map[string]string{backendplugin.EnvChannelPipe: "p"}),
		func(string) (net.Conn, error) { return serverSide, nil },
		func(net.Conn) net.Listener {
			return failingChannelListener{err: errors.New("injected channel listener failure")}
		},
	)
	if err == nil || !strings.Contains(err.Error(), "injected channel listener failure") {
		t.Fatalf("channel listener failure = %v, want surfaced error", err)
	}
}

type failingChannelListener struct{ err error }

func (l failingChannelListener) Accept() (net.Conn, error) { return nil, l.err }
func (l failingChannelListener) Close() error              { return nil }
func (l failingChannelListener) Addr() net.Addr            { return failingChannelAddr{} }

type failingChannelAddr struct{}

func (failingChannelAddr) Network() string { return "stub" }
func (failingChannelAddr) String() string  { return "stub" }

package backendplugin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/grpc"
)

// Channel environment variables the host sets when it spawns a connector.
// Exactly one is present for a host-spawned process; neither is present when an
// operator runs the connector by hand on a loopback address.
const (
	// EnvChannelPipe names a Windows named pipe the connector dials.
	EnvChannelPipe = "AIP_PLUGIN_CHANNEL_PIPE"
	// EnvChannelFD is an inherited socket file descriptor on Unix hosts.
	EnvChannelFD = "AIP_PLUGIN_CHANNEL_FD"
)

// ServeOptions configures [Serve].
type ServeOptions struct {
	// Name labels the loopback startup line, for example "aip-backend-vertex".
	Name string
	// ListenAddr is the TCP address used only when the host supplied no channel.
	// Empty means "127.0.0.1:0".
	ListenAddr string
	// Stderr receives the loopback startup line. Nil means os.Stderr.
	Stderr io.Writer
}

// Serve runs svc as a connector process on the channel the host provided:
// a named pipe ([EnvChannelPipe]), an inherited socket ([EnvChannelFD]), or,
// with neither set, a loopback TCP listener. The gRPC registration advertises
// svc's protocol version and features with transport retries disabled.
//
// Serve returns when ctx is canceled or on a listener error. Process lifetime
// on a pipe or socket channel is owned by the host, which stops the process.
func Serve(ctx context.Context, svc Service, opts ServeOptions) error {
	return serveWith(ctx, svc, opts, os.LookupEnv, dialNamedPipe, channelListener)
}

// channelListener adapts the single-connection listener to the factory type
// shared by Serve and its test seam.
func channelListener(conn net.Conn) net.Listener { return newSingleConnListener(conn) }

// Main is the standard connector entrypoint. It parses the -listen flag, runs
// [Serve], and exits the process with status 1 when Serve fails.
func Main(name string, svc Service) {
	listen := flag.String("listen", "", "loopback listen address (optional; production uses "+EnvChannelPipe+" or "+EnvChannelFD+")")
	flag.Parse()
	if err := Serve(context.Background(), svc, ServeOptions{Name: name, ListenAddr: *listen}); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func serveWith(
	ctx context.Context,
	svc Service,
	opts ServeOptions,
	lookupEnv func(string) (string, bool),
	dialPipe func(string) (net.Conn, error),
	newListener func(net.Conn) net.Listener,
) error {
	if svc == nil {
		return errors.New("backendplugin: serve: nil service")
	}
	desc, err := svc.Describe(ctx)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	offer := ProtocolOffer{
		Major: desc.ProtocolMajor, Minor: desc.ProtocolMinor,
		DisableTransportRetries: true, Features: desc.Features,
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, NewGRPCServer(offer, svc))
	stop := context.AfterFunc(ctx, gs.Stop)
	defer stop()

	if pipe, ok := lookupEnv(EnvChannelPipe); ok && pipe != "" {
		conn, err := dialPipe(pipe)
		if err != nil {
			return fmt.Errorf("pipe dial: %w", err)
		}
		if err := gs.Serve(newListener(conn)); err != nil {
			return fmt.Errorf("serve channel: %w", err)
		}
		return nil
	}
	if fdEnv, ok := lookupEnv(EnvChannelFD); ok && fdEnv != "" {
		conn, err := channelFDConn(fdEnv)
		if err != nil {
			return err
		}
		if err := gs.Serve(newListener(conn)); err != nil {
			return fmt.Errorf("serve channel: %w", err)
		}
		return nil
	}

	addr := opts.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stderr, "%s listening on %s\n", opts.Name, lis.Addr().String())
	if err := gs.Serve(lis); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func channelFDConn(fdEnv string) (net.Conn, error) {
	fd, err := strconv.Atoi(fdEnv)
	if err != nil || fd < 0 {
		return nil, fmt.Errorf("channel fd %q: invalid descriptor", fdEnv)
	}
	f := os.NewFile(uintptr(fd), "aip-plugin-channel")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("channel fd conn: %w", err)
	}
	return conn, nil
}

// singleConnListener hands gRPC the one host channel, then blocks Accept until
// the server stops.
type singleConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	given  bool
	closed chan struct{}
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, closed: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.given {
		l.given = true
		c := l.conn
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.mu.Lock()
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	l.mu.Unlock()
	return l.conn.Close()
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

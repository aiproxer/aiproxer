package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
)

func main() {
	listen := flag.String("listen", "", "loopback listen address (optional)")
	modeFlag := flag.String("mode", "", "fake mode (overrides AIP_BACKENDPLUGIN_FAKE_MODE)")
	flag.Parse()

	mode := fakebp.ModeValid
	if v := os.Getenv("AIP_BACKENDPLUGIN_FAKE_MODE"); v != "" {
		mode = fakebp.Mode(v)
	}
	if *modeFlag != "" {
		mode = fakebp.Mode(*modeFlag)
	}

	svc := &fakebp.FakeService{Mode: mode}
	desc, err := svc.Describe(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "describe: %v\n", err)
		os.Exit(1)
	}
	offer := backendplugin.ProtocolOffer{
		Major: desc.ProtocolMajor, Minor: desc.ProtocolMinor,
		DisableTransportRetries: true, Features: desc.Features,
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, svc))

	if pipe := os.Getenv("AIP_PLUGIN_CHANNEL_PIPE"); pipe != "" {
		if err := serveNamedPipe(gs, pipe, dialNamedPipe); err != nil {
			fmt.Fprintf(os.Stderr, "pipe dial: %v\n", err)
			os.Exit(1)
		}
		return
	}

	addr := *listen
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	if os.Getenv("AIP_BACKENDPLUGIN_FAKE_READY") != "" {
		fmt.Fprintf(os.Stderr, "READY %s\n", lis.Addr().String())
	}
	if err := gs.Serve(lis); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

func serveNamedPipe(server *grpc.Server, name string, dial func(string) (net.Conn, error)) error {
	conn, err := dial(name)
	if err != nil {
		return err
	}
	if os.Getenv("AIP_BACKENDPLUGIN_FAKE_READY") != "" {
		fmt.Fprintf(os.Stderr, "READY pipe %s\n", name)
	}
	_ = server.Serve(&singleConnListener{conn: conn, closed: make(chan struct{})})
	return nil
}

type singleConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	given  bool
	closed chan struct{}
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
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return l.conn.Close()
}
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

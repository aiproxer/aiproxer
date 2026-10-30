package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--runtime-supervisor" {
		if err := product.RunRuntimeSupervisor(os.Args[2:]); err != nil {
			if errors.Is(err, product.ErrRuntimeFailed) {
				// The runtime exited unsuccessfully and its tree is extinct.
				// Report it with its own exit code so the parent can tell the
				// runtime failure from an unconfirmed cleanup.
				fmt.Fprintln(os.Stderr, "Claude runtime exited unsuccessfully")
				os.Exit(product.SupervisorRuntimeFailedExit)
			}
			fmt.Fprintln(os.Stderr, "Claude runtime supervisor failed")
			os.Exit(1)
		}
		return
	}
	listen := flag.String("listen", "", "loopback listen address (optional; production uses AIP_PLUGIN_CHANNEL_PIPE or AIP_PLUGIN_CHANNEL_FD)")
	flag.Parse()

	svc := service.New()
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

	if fdEnv := os.Getenv("AIP_PLUGIN_CHANNEL_FD"); fdEnv != "" {
		fd, err := strconv.Atoi(fdEnv)
		if err != nil || fd < 0 {
			fmt.Fprintf(os.Stderr, "channel fd: %v\n", err)
			os.Exit(1)
		}
		f := os.NewFile(uintptr(fd), "aip-plugin-channel")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "channel fd conn: %v\n", err)
			os.Exit(1)
		}
		_ = gs.Serve(&singleConnListener{conn: c, closed: make(chan struct{})})
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
	fmt.Fprintf(os.Stderr, "aip-backend-claudeagentacp listening on %s\n", lis.Addr().String())
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

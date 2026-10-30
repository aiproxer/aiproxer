package main

import (
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
)

func TestServeNamedPipeClosesConnectionOnStoppedServer(t *testing.T) {
	t.Parallel()
	server := grpc.NewServer()
	server.Stop()
	conn, peer := net.Pipe()
	defer func() {
		_ = conn.Close()
		_ = peer.Close()
	}()
	err := serveNamedPipe(server, "test-pipe", func(name string) (net.Conn, error) {
		if name != "test-pipe" {
			t.Fatalf("pipe name = %q", name)
		}
		return conn, nil
	})
	if err != nil {
		t.Fatalf("serve error = %v", err)
	}
	if _, err := peer.Write([]byte("closed")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("connection not closed by server: %v", err)
	}
}

func TestServeNamedPipeReturnsDialError(t *testing.T) {
	t.Parallel()
	want := errors.New("pipe unavailable")
	err := serveNamedPipe(nil, "test-pipe", func(name string) (net.Conn, error) {
		if name != "test-pipe" {
			t.Fatalf("pipe name = %q", name)
		}
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("dial error = %v, want %v", err, want)
	}
}

// Command stubbackend serves scripted SSE responses over real HTTP for
// spawned-distribution feature smokes (see scripts/feature-stub-smoke.sh).
//
// It serves a sequence of scripted responses loaded from a directory (one
// response per file, sorted by name) or a single file (responses separated by
// a line containing exactly %STUBBACKEND_NEXT%), one per incoming POST request
// in order; once exhausted, the final response repeats. Every POST request
// body is appended to the log file as one `REQ<n> <body>` line. Non-POST
// requests answer 200 without consuming the sequence, so readiness probes
// never shift it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxBodyBytes = 10 << 20
	// responseSeparator splits one response file into a sequence: a line
	// containing exactly this token separates two scripted responses.
	responseSeparator = "%STUBBACKEND_NEXT%"
	// sseContentType is the only response content type: scripted bodies are
	// raw SSE payloads for streaming backends.
	sseContentType = "text/event-stream; charset=utf-8"
)

func main() {
	addr := flag.String("addr", "", "listen address, e.g. 127.0.0.1:18198")
	responsesPath := flag.String("responses", "", "scripted-response directory or file")
	logPath := flag.String("log", "", "request-body log file (appended)")
	freePorts := flag.Int("print-free-ports", 0, "print N free 127.0.0.1 ports on one line and exit")
	flag.Parse()
	if *freePorts != 0 {
		if *freePorts < 1 {
			flag.Usage()
			os.Exit(2)
		}
		ports, err := allocateFreePorts(*freePorts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stubbackend: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(strings.Join(ports, " "))
		return
	}
	if *addr == "" || *responsesPath == "" || *logPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := serve(*addr, *responsesPath, *logPath); err != nil {
		fmt.Fprintf(os.Stderr, "stubbackend: %v\n", err)
		os.Exit(1)
	}
}

// stubServer answers POSTs from the scripted sequence and appends every POST
// body to the log. The mutex serializes sequence selection and log order, so
// the log line order always matches the served order.
type stubServer struct {
	mu        sync.Mutex
	responses [][]byte
	requests  int
	log       *os.File
}

func newStubServer(responses [][]byte, log *os.File) *stubServer {
	return &stubServer{responses: responses, log: log}
}

func (s *stubServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests++
	index := s.requests - 1
	if index >= len(s.responses) {
		index = len(s.responses) - 1
	}
	payload := s.responses[index]
	// A short log write has no request-scoped error channel; the smoke
	// assertions fail closed on a missing log line instead.
	_, _ = fmt.Fprintf(s.log, "REQ%d %s\n", s.requests, body)
	s.mu.Unlock()
	w.Header().Set("Content-Type", sseContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// loadResponses reads the scripted sequence from a directory (one response per
// regular file, sorted by name) or a single file (responses separated by a
// responseSeparator line, or one response when absent).
func loadResponses(path string) ([][]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return splitFileResponses(data), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, data)
	}
	if len(out) == 0 {
		return nil, errors.New("no scripted responses in " + path)
	}
	return out, nil
}

// splitFileResponses splits one file into a response sequence on
// responseSeparator lines. Without the separator the file is one response,
// served for every request.
func splitFileResponses(data []byte) [][]byte {
	var out [][]byte
	var current []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimRight(line, "\r") == responseSeparator {
			out = append(out, []byte(strings.Join(current, "\n")))
			current = nil
			continue
		}
		current = append(current, line)
	}
	return append(out, []byte(strings.Join(current, "\n")))
}

// allocateFreePorts binds n loopback listeners to reserve n free ports, then
// closes them and returns their port numbers. All listeners stay open until
// every port is collected, so one port cannot be handed out twice. The
// close-then-bind race is inherent to free-port selection; the smoke script
// binds the printed ports immediately.
func allocateFreePorts(n int) ([]string, error) {
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	ports := make([]string, 0, n)
	for range n {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		_, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			return nil, err
		}
		ports = append(ports, port)
	}
	return ports, nil
}

func serve(addr, responsesPath, logPath string) error {
	responses, err := loadResponses(responsesPath)
	if err != nil {
		return fmt.Errorf("load responses: %w", err)
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer func() { _ = log.Close() }()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Printf("stubbackend: listening on %s with %d scripted responses\n", listener.Addr(), len(responses))
	server := &http.Server{
		Handler:           newStubServer(responses, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

package processhost_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"go.uber.org/goleak"
)

// TestHost_DrainsConnectorPipesBeforePeerAccept proves that a connector which
// writes to stdout or stderr never stalls. The OS pipe buffer is finite (64 KiB
// on Linux), so a pipe nobody reads blocks the connector's next write and,
// with it, the whole request. The host drains both pipes from the moment the
// process starts, before the peer authenticates.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_DrainsConnectorPipesBeforePeerAccept(t *testing.T) {
	defer goleak.VerifyNone(t)
	assertPipeDrained(t, "stderr", func(l *processhost.TestLauncher, r io.ReadCloser) {
		l.StderrReader = r
	})
	assertPipeDrained(t, "stdout", func(l *processhost.TestLauncher, r io.ReadCloser) {
		l.StdoutReader = r
	})
}

// assertPipeDrained models a connector that logs a full pipe buffer worth of
// diagnostics before it dials the channel. The write must complete even though
// the peer never authenticates, and the activation must still succeed once
// the peer appears.
func assertPipeDrained(t *testing.T, name string, attach func(*processhost.TestLauncher, io.ReadCloser)) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s pipe: %v", name, err)
	}
	// Far more than one pipe buffer, so an undrained pipe blocks the writer.
	payload := bytes.Repeat([]byte("connector diagnostic line\n"), 8192)

	launcher := &processhost.TestLauncher{PID: 9401}
	attach(launcher, reader)
	channel := &blockingAcceptChannel{peerPID: launcher.PID, release: make(chan struct{})}
	written := make(chan error, 1)
	writeDone := make(chan struct{})
	launcher.OnLaunch = func(processhost.LaunchSpec) {
		go func() {
			defer close(writeDone)
			_, werr := writer.Write(payload)
			written <- werr
		}()
	}

	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: channel})
	activated := make(chan error, 1)
	activationDone := make(chan struct{})
	go func() {
		defer close(activationDone)
		_, aerr := h.Activate(context.Background(), processhost.ActivateRequest{
			InstanceID:       "drain-" + name,
			Artifact:         &trust.VerifiedArtifact{DigestHex: "drain-" + name},
			Model:            processhost.ProcessModelPerInstance,
			DialAndConfigure: dialOK,
		})
		activated <- aerr
	}()

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(channel.release) }) }
	defer func() {
		release()
		select {
		case <-activationDone:
		case <-time.After(10 * time.Second):
		}
		_ = h.Close()
		_ = reader.Close()
		_ = writer.Close()
		select {
		case <-writeDone:
		case <-time.After(10 * time.Second):
		}
	}()

	select {
	case werr := <-written:
		if werr != nil {
			t.Errorf("%s write: %v", name, werr)
			return
		}
	case <-time.After(10 * time.Second):
		t.Errorf("%s pipe blocked: the host did not drain the connector pipe", name)
		return
	}

	release()
	if err := <-activated; err != nil {
		t.Errorf("%s activate: %v", name, err)
		return
	}
	if err := h.Close(); err != nil {
		t.Errorf("%s close: %v", name, err)
	}
}

// TestHost_FloodingConnectorIsTerminated proves the diagnostic budget: a
// connector that writes without bound on stdout and stderr at the same time
// is terminated, and the activation fails with the explicit budget reason
// instead of serving a connector that floods the host.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_FloodingConnectorIsTerminated(t *testing.T) {
	defer goleak.VerifyNone(t)

	stdout := newFloodReader()
	stderr := newFloodReader()
	launcher := &processhost.TestLauncher{
		PID:          9501,
		StdoutReader: stdout,
		StderrReader: stderr,
	}
	// The peer never appears, so the activation can only end through the
	// diagnostic budget terminating the connector.
	channel := &blockingAcceptChannel{peerPID: launcher.PID, release: make(chan struct{})}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  channel,
		OutputBudget: processhost.OutputBudget{
			BurstBytes:     64 << 10,
			BytesPerSecond: 1 << 20,
		},
	})
	t.Cleanup(func() { _ = h.Close() })

	// The deadline bounds the failure mode where the budget never trips: the
	// activation then fails on the context instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:       "flood",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "flood"},
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	if !errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("want output limit reason, got %v", err)
	}
	killed, ok := launcher.LastProcess.(interface{ Killed() bool })
	if !ok {
		t.Fatalf("launcher recorded process %T", launcher.LastProcess)
	}
	if !killed.Killed() {
		t.Fatal("flooding connector was not terminated")
	}
}

// TestHost_PeerRejectionReleasesConnectorPipes proves that a rejected peer
// leaves no connector behind: the launched process is terminated and the
// pipes the host drained are released, so the connector's next write fails
// instead of blocking on a pipe nobody reads.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_PeerRejectionReleasesConnectorPipes(t *testing.T) {
	defer goleak.VerifyNone(t)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	launcher := &processhost.TestLauncher{PID: 9601, StderrReader: reader}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{PeerPID: 9999},
	})
	t.Cleanup(func() { _ = h.Close() })

	_, err = h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID:       "peer-reject",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "peer-reject"},
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	if !errors.Is(err, processhost.ReasonPeerRejected) {
		t.Fatalf("want peer rejected, got %v", err)
	}
	killed, ok := launcher.LastProcess.(interface{ Killed() bool })
	if !ok {
		t.Fatalf("launcher recorded process %T", launcher.LastProcess)
	}
	if !killed.Killed() {
		t.Fatal("rejected peer left the connector running")
	}
	// The host released the pipe it drained, so the connector's next write
	// fails instead of blocking forever on a pipe nobody reads.
	if _, werr := writer.Write([]byte("late connector log\n")); werr == nil {
		t.Fatal("connector pipe was not released after the peer was rejected")
	}
}

// TestHost_ReapFollowsDrainRelease proves the documented os/exec pipe
// lifecycle: the host releases the pipes it drains before it reaps the child,
// because Cmd.Wait closes the parent's pipe descriptors.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_ReapFollowsDrainRelease(t *testing.T) {
	defer goleak.VerifyNone(t)

	var mu sync.Mutex
	order := []string{}
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, event)
	}
	launcher := &processhost.TestLauncher{
		PID:          9701,
		StderrReader: &recordingReader{record: record, name: "stderr"},
		OnReap:       func() { record("reaped") },
	}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID:       "reap-order",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "reap-order"},
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	want := []string{"stderr-closed", "reaped"}
	if !slices.Equal(got, want) {
		t.Fatalf("teardown order: got %v, want %v", got, want)
	}
}

// recordingReader records when the host releases the pipe.
type recordingReader struct {
	record func(string)
	name   string
	once   sync.Once
}

func (r *recordingReader) Read([]byte) (int, error) { return 0, io.EOF }

func (r *recordingReader) Close() error {
	r.once.Do(func() { r.record(r.name + "-closed") })
	return nil
}

// floodReader models a connector that logs without bound until the host
// releases the pipe.
type floodReader struct {
	closed chan struct{}
	once   sync.Once
}

func newFloodReader() *floodReader {
	return &floodReader{closed: make(chan struct{})}
}

func (f *floodReader) Read(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, io.EOF
	default:
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (f *floodReader) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// blockingAcceptChannel models a connector that has not dialed the channel
// yet: Accept only returns once the test releases it.
type blockingAcceptChannel struct {
	peerPID int
	release chan struct{}
	gen     uint64
}

func (c *blockingAcceptChannel) Listen(_ context.Context, generation uint64) (processhost.Listener, []*os.File, error) {
	c.gen = generation
	server, peer := net.Pipe()
	return &blockingAcceptListener{parent: c, conn: server, peer: peer}, nil, nil
}

type blockingAcceptListener struct {
	parent *blockingAcceptChannel
	conn   net.Conn
	peer   net.Conn
	once   sync.Once
}

func (l *blockingAcceptListener) Accept(ctx context.Context) (net.Conn, processhost.PeerIdentity, error) {
	select {
	case <-l.parent.release:
	case <-ctx.Done():
		return nil, processhost.PeerIdentity{}, ctx.Err()
	}
	return l.conn, processhost.PeerIdentity{PID: l.parent.peerPID, Generation: l.parent.gen}, nil
}

func (l *blockingAcceptListener) Close() error {
	l.once.Do(func() {
		_ = l.conn.Close()
		_ = l.peer.Close()
	})
	return nil
}

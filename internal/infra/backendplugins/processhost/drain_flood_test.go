//go:build linux || windows

package processhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	backendplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"go.uber.org/goleak"
)

// nativeFloodInstanceID is the instance the native flooding test activates.
const nativeFloodInstanceID = "native-flood"

// TestHost_NativeFloodingConnectorIsTerminated proves the diagnostic budget
// against a real process: a connector that writes to stdout without bound is
// terminated and reaped by the host instead of being allowed to keep the host
// busy.
//
// The test runs on every platform whose launcher can start a connector. The
// fixture floods at a known rate, so the budget is exhausted at a predictable
// moment instead of racing the activation. The activation may still end either
// way: the host can refuse to serve a connector that outruns its budget, or
// serve it and terminate it afterwards. The termination and the reap are
// therefore proven from the launched process, independently of the activation
// outcome.
//
// The fixture survives a closed output pipe (TestFloodFixture_SurvivesClosedOutputPipe
// proves it), so a terminated connector can only have been terminated by the
// host.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_NativeFloodingConnectorIsTerminated(t *testing.T) {
	defer goleak.VerifyNone(t)

	artifact := stageFloodFixture(t)
	launcher := newObservedLauncher(processhost.NewPlatformLauncher())
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
		// The fixture floods 640 KiB/s. The burst plus the sustained
		// allowance are exhausted roughly 1.8 s after the process starts, so
		// the activation settles hundreds of times over before the budget
		// trips. The activation outcome is still not asserted: the
		// termination and the reap must hold either way.
		OutputBudget: processhost.OutputBudget{
			BurstBytes:     1 << 20,
			BytesPerSecond: 64 << 10,
		},
	})
	t.Cleanup(func() { _ = h.Close() })

	// The deadline bounds a host that never settles the activation.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:       nativeFloodInstanceID,
		Artifact:         artifact,
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	switch {
	case err == nil:
		// The connector was served; the budget terminates it below.
	case errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded):
		// The connector outran its budget before it was served, so the host
		// refused to serve it.
	case errors.Is(err, processhost.ReasonGenerationInvalidated):
		// The connector outran its budget right after it was accepted, so
		// the host dropped the generation instead of serving it. The
		// diagnostic budget is the only invalidator in this test.
	default:
		t.Fatalf("activate: %v", err)
	}

	proc := launcher.Process()
	if proc == nil {
		t.Fatal("the host did not launch a child process")
	}
	// The host must have asked for the termination and completed the reap.
	// The observed reap is the proof: a child that was only signalled would
	// still be a zombie, and the fixture itself cannot end on its own.
	select {
	case <-proc.Reaped():
	case <-time.After(30 * time.Second):
		t.Fatal("the host did not reap the flooding connector")
	}
	if !proc.Terminated() {
		t.Fatal("the host reaped the flooding connector without terminating it")
	}

	// A terminated generation must not stay servable. This runs whatever
	// the activation outcome was: the generation identity comes from the
	// launched process, not from a result the activation may not have
	// returned.
	assertTerminatedGenerationNotServed(t, h, artifact, proc.Generation())
}

// assertTerminatedGenerationNotServed proves that a terminated generation is
// not served again: a later activation of the same instance must receive a
// different generation, or fail, but never the terminated one.
//
// A later activation may legitimately fail: the replacement connector floods
// too, so the host can refuse it for the same reason. An unrelated failure
// is a defect in the test or the host and fails it.
func assertTerminatedGenerationNotServed(
	t *testing.T,
	h *processhost.Host,
	artifact *trust.VerifiedArtifact,
	terminated uint64,
) {
	t.Helper()
	// A fresh context: the activation above may have consumed its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	again, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:       nativeFloodInstanceID,
		Artifact:         artifact,
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	switch {
	case err == nil:
		if again.Generation == terminated {
			t.Fatalf("the host served the terminated generation %d again", terminated)
		}
		// The replacement generation owns a live connector: release it
		// before the leak check.
		if cleanupErr := again.Cleanup(); cleanupErr != nil {
			t.Fatalf("cleanup: %v", cleanupErr)
		}
	case errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded):
		// The replacement connector outran its budget as well, so the host
		// refused to serve it. That is not the terminated generation.
	case errors.Is(err, processhost.ReasonGenerationInvalidated):
		// The replacement generation was dropped for the same reason. That
		// is not the terminated generation either.
	default:
		t.Fatalf("reactivating the terminated instance: %v", err)
	}
}

// TestFloodFixture_SurvivesClosedOutputPipe proves the premise of
// TestHost_NativeFloodingConnectorIsTerminated: a closed output pipe must
// not end the fixture. A writer to a broken pipe is killed by SIGPIPE on
// Unix, so without this property a host that only closed the pipe would look
// like it terminated the flooding connector.
//
//nolint:paralleltest // spawns a child process and observes its lifetime
func TestFloodFixture_SurvivesClosedOutputPipe(t *testing.T) {
	bin := buildFlooder(t)
	// The child writes to a pipe the test owns. An explicit pipe keeps the
	// waiter independent of the reads: os/exec closes a StdoutPipe inside
	// Wait, and calling Wait before the reads finish is a contract error.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	// The parent must not hold the write end open, or the reader would
	// never see the end of the child's output.
	_ = writer.Close()
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	// Read one chunk so the fixture is known to be writing.
	buf := make([]byte, 4096)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Closing the read end makes the fixture's next write fail.
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	// The fixture writes every 50 ms, so the failed write happens well
	// inside this window.
	select {
	case <-exited:
		t.Fatalf("the fixture exited when its output pipe was closed: %v", waitErr)
	case <-time.After(time.Second):
	}
}

// TestHost_NativeBothStreamsShareTheBudget proves the host charges stdout
// and stderr against one diagnostic budget.
//
// The fixture writes 60 KiB to each stream and stays alive. Neither stream
// reaches the 100 KiB allowance on its own, so a host that charged the two
// streams separately would never exhaust the allowance and the connector
// would never be terminated.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_NativeBothStreamsShareTheBudget(t *testing.T) {
	defer goleak.VerifyNone(t)

	artifact := stageFixture(t, "twostream")
	launcher := newObservedLauncher(processhost.NewPlatformLauncher())
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
		// The fixture writes 120 KiB in total. The allowance is 100 KiB and
		// the refill is one byte per second, so only the two streams
		// together can exhaust it.
		OutputBudget: processhost.OutputBudget{
			BurstBytes:     100 << 10,
			BytesPerSecond: 1,
		},
	})
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:       "native-both-streams",
		Artifact:         artifact,
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	switch {
	case err == nil:
		// The connector was served; the shared budget terminates it below.
	case errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded):
		// The two streams exhausted the allowance before it was served.
	case errors.Is(err, processhost.ReasonGenerationInvalidated):
		// The allowance was exhausted right after it was accepted.
	default:
		t.Fatalf("activate: %v", err)
	}

	proc := launcher.Process()
	if proc == nil {
		t.Fatal("the host did not launch a child process")
	}
	select {
	case <-proc.Reaped():
	case <-time.After(30 * time.Second):
		t.Fatal("the host did not reap the connector that exhausted the shared budget")
	}
	if !proc.Terminated() {
		t.Fatal("the host reaped the connector without terminating it")
	}
}

// TestHost_NativeProcessTreeIsTerminated proves a connector that leaves a
// descendant holding its output open does not survive the host's
// termination.
//
// The descendant inherits the connector's stdout and stderr, so the host's
// drain stays open after the root process is terminated. A host that
// terminated only the root, or that joined the drain before terminating the
// descendants, would wait for the descendant forever.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_NativeProcessTreeIsTerminated(t *testing.T) {
	defer goleak.VerifyNone(t)

	workDir := t.TempDir()
	artifact := stageFixture(t, "streamtree")
	launcher := newObservedLauncher(processhost.NewPlatformLauncher())
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
		WorkDir:  workDir,
		OutputBudget: processhost.OutputBudget{
			BurstBytes:     1 << 20,
			BytesPerSecond: 64 << 10,
		},
	})
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:       "native-process-tree",
		Artifact:         artifact,
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	switch {
	case err == nil:
	case errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded):
	case errors.Is(err, processhost.ReasonGenerationInvalidated):
	default:
		t.Fatalf("activate: %v", err)
	}

	proc := launcher.Process()
	if proc == nil {
		t.Fatal("the host did not launch a child process")
	}
	// The termination must complete: a descendant holding the output open
	// must not keep the host waiting.
	select {
	case <-proc.Reaped():
	case <-time.After(30 * time.Second):
		t.Fatal("the host did not reap the connector that left a descendant holding its output")
	}
	if !proc.Terminated() {
		t.Fatal("the host reaped the connector without terminating it")
	}

	// The descendant must be gone too: it holds the connector's output
	// handles open.
	descendant := readDescendantPID(t, workDir)
	assertProcessExited(t, descendant, 30*time.Second)
}

// readDescendantPID reads the descendant process id the tree fixture
// recorded in its working directory.
func readDescendantPID(t *testing.T, workDir string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(workDir, "descendant.pid"))
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr != nil {
				t.Fatalf("parse descendant pid %q: %v", raw, convErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the connector never recorded its descendant process id")
	return 0
}

// echoerStdoutMarker and echoerStderrMarker are the markers the echoer
// fixture writes. Keep them in sync with testdata/echoer/main.go.
const (
	echoerStdoutMarker = "aip-processhost-echoer-stdout"
	echoerStderrMarker = "aip-processhost-echoer-stderr"
)

// TestPlatformLauncher_CapturesChildOutput proves that the platform launcher
// hands the host the child's real standard streams.
//
// The host drains those streams under the diagnostic budget, so a launcher
// that returned empty readers would charge nothing for the connector's
// output: a connector that floods the host would never be terminated. The
// capture must therefore exist on every platform, not only on the one whose
// test happens to run.
//
//nolint:paralleltest // launches a child process and reads its output
func TestPlatformLauncher_CapturesChildOutput(t *testing.T) {
	artifact := stageFixture(t, "echoer")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proc, err := processhost.NewPlatformLauncher().Launch(ctx, processhost.LaunchSpec{
		Artifact:   artifact,
		Generation: 1,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })

	// The read runs before the reap: the launcher closes the parent's copy
	// of the child's output when it reaps the child, so reading after the
	// reap would read from a closed pipe.
	stdoutCh := make(chan capturedStream, 1)
	stderrCh := make(chan capturedStream, 1)
	go func() {
		data, err := io.ReadAll(proc.Stdout())
		stdoutCh <- capturedStream{out: data, err: err}
	}()
	go func() {
		data, err := io.ReadAll(proc.Stderr())
		stderrCh <- capturedStream{out: data, err: err}
	}()
	out := awaitCaptured(t, stdoutCh, "stdout")
	errOut := awaitCaptured(t, stderrCh, "stderr")
	if !strings.Contains(string(out.out), echoerStdoutMarker) {
		t.Fatalf("the launcher did not capture the child's stdout: %q", out.out)
	}
	if !strings.Contains(string(errOut.out), echoerStderrMarker) {
		t.Fatalf("the launcher did not capture the child's stderr: %q", errOut.out)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// capturedStream is one captured child stream.
type capturedStream struct {
	out []byte
	err error
}

// awaitCaptured returns the captured stream, or fails the test when the
// stream never ends.
func awaitCaptured(t *testing.T, ch <-chan capturedStream, stream string) capturedStream {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("read %s: %v", stream, got.err)
		}
		return got
	case <-time.After(30 * time.Second):
		t.Fatalf("the child's %s was never captured", stream)
		return capturedStream{}
	}
}

// buildFlooder compiles the controlled-rate flood fixture. The fixture is a
// plain Go program, so the test does not depend on a coreutils `yes` being
// installed on the host, and the flood rate is identical on every platform.
func buildFlooder(t *testing.T) string {
	t.Helper()
	return buildFixture(t, "flooder")
}

// buildFixture compiles one fixture program from testdata.
func buildFixture(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aip-processhost-"+name+executableSuffix())
	// The test runs in the package directory, so the fixture source is
	// addressed relative to it.
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/"+name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s fixture: %v\n%s", name, err, out)
	}
	return bin
}

// stageFloodFixture stages the flood fixture as a verified artifact the
// platform launcher can start.
func stageFloodFixture(t *testing.T) *trust.VerifiedArtifact {
	t.Helper()
	return stageFixture(t, "flooder")
}

// stageFixture stages a fixture program as a verified artifact the platform
// launcher can start. The launcher binds the verified bytes differently per
// platform (a held descriptor on Linux, protected staging on Windows), so the
// fixture is staged through the shared verification path and every platform
// stages it the same way.
func stageFixture(t *testing.T, name string) *trust.VerifiedArtifact {
	t.Helper()
	data, err := os.ReadFile(buildFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel := "bin/aip-processhost-" + name + executableSuffix()
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	m := sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "com.aiproxer.processhost." + name,
		Version: "0.0.1", BuildID: "test",
		Executable: rel, SHA256: hex.EncodeToString(sum[:]), ProtocolMajor: 1,
		Platforms: []sdkmanifest.Platform{{OS: runtime.GOOS, Arch: runtime.GOARCH}},
		Exports: []sdkmanifest.Export{{
			Kind: name + "-fixture", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
	// The staging directory is required by the Windows binding and ignored by
	// the Linux one.
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("verify: %+v", res)
	}
	t.Cleanup(func() { _ = res.Artifact.Close() })
	return res.Artifact
}

// executableSuffix is the native executable suffix of the host platform.
func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// observedLauncher wraps a launcher and remembers the launched process, so a
// test can observe the child pid, the host's termination request, and the
// completed reap even when the activation fails before it returns a result.
type observedLauncher struct {
	inner processhost.Launcher

	mu   sync.Mutex
	proc *observedProcess
}

func newObservedLauncher(inner processhost.Launcher) *observedLauncher {
	return &observedLauncher{inner: inner}
}

func (l *observedLauncher) Launch(ctx context.Context, spec processhost.LaunchSpec) (processhost.Process, error) {
	proc, err := l.inner.Launch(ctx, spec)
	if proc == nil {
		return proc, err
	}
	observed := &observedProcess{Process: proc, reapedCh: make(chan struct{})}
	l.mu.Lock()
	l.proc = observed
	l.mu.Unlock()
	return observed, err
}

// Process returns the observed child, or nil when nothing was launched.
func (l *observedLauncher) Process() *observedProcess {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.proc
}

// observedProcess records the host's termination request and the completed
// reap of a launched connector.
type observedProcess struct {
	processhost.Process

	mu         sync.Mutex
	terminated bool
	reapedCh   chan struct{}
}

func (p *observedProcess) SignalKill() error {
	p.markTerminated()
	return p.Process.SignalKill()
}

func (p *observedProcess) GracefulStop(timeout time.Duration) error {
	p.markTerminated()
	return p.Process.GracefulStop(timeout)
}

func (p *observedProcess) Wait() error {
	err := p.Process.Wait()
	p.markReaped()
	return err
}

func (p *observedProcess) Close() error {
	err := p.Process.Close()
	p.markReaped()
	return err
}

// Terminated reports whether the host asked for the process to be terminated.
func (p *observedProcess) Terminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminated
}

// Reaped is closed when the host completed the reap of the process.
func (p *observedProcess) Reaped() <-chan struct{} {
	return p.reapedCh
}

func (p *observedProcess) markTerminated() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated = true
}

func (p *observedProcess) markReaped() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.reapedCh:
	default:
		close(p.reapedCh)
	}
}

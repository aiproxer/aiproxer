package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testOptions(t *testing.T, script, mode string) Options {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join("testdata", script))
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	if mode != "" {
		env = append(env, "FAKE_MODE="+mode)
	}
	return Options{
		Node:   node,
		Script: path,
		Env:    env,
		Dir:    t.TempDir(),
	}
}

func TestStartRejectsNonBundledScript(t *testing.T) {
	t.Parallel()
	_, err := Start(context.Background(), Options{Node: "node", Script: "/tmp/anything.js"})
	if err == nil {
		t.Fatal("expected non-mjs rejection")
	}
}

func TestProcess_HelloAndDiscovery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "fake-sidecar.mjs", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	hello, err := proc.AwaitHello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Protocol != ProtocolVersion || hello.SDKVersion != "0.3.316" {
		t.Fatalf("hello=%+v", hello)
	}
	if err := proc.SendContext(ctx, map[string]any{"kind": "discover", "request_id": "d"}); err != nil {
		t.Fatal(err)
	}
	line, err := proc.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Kind != "models" || len(frame.Models) != 2 || frame.Models[0].Value != "sonnet" {
		t.Fatalf("frame=%+v", frame)
	}
}

func TestProcess_NoHelloFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "fake-sidecar.mjs", "no_hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	_, err = proc.AwaitHello(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not greet") {
		t.Fatalf("err=%v", err)
	}
}

func TestProcess_OversizeFrameIsRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "oversize-sidecar.mjs", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	if _, err := proc.AwaitHello(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = proc.Recv(ctx)
	if err == nil || !strings.Contains(err.Error(), "frame exceeds bound") {
		t.Fatalf("err=%v", err)
	}
}

func TestProcess_CloseKillsStubbornChild(t *testing.T) {
	t.Parallel()
	opts := testOptions(t, "fake-sidecar.mjs", "stubborn")
	opts.ShutdownTimeout = 200 * time.Millisecond
	proc, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	if err := proc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.done:
	case <-time.After(time.Until(deadline)):
		t.Fatal("child did not exit after forced close")
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("second close=%v", err)
	}
}

func TestProcess_CloseJoinsBlockedWrite(t *testing.T) {
	t.Parallel()
	opts := testOptions(t, "fake-sidecar.mjs", "no_read")
	opts.ShutdownTimeout = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.AwaitHello(ctx); err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- proc.Send(map[string]any{
			"kind":   "start",
			"prompt": strings.Repeat("x", 1<<20),
		})
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-sendDone:
		t.Fatalf("send completed while the child never read stdin: %v", err)
	default:
	}
	start := time.Now()
	if err := proc.Close(); err != nil {
		t.Fatalf("close=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("close took %s with a blocked writer", elapsed)
	}
	select {
	case err := <-sendDone:
		if err == nil {
			t.Fatal("blocked send reported success after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked send did not join after close")
	}
}

func TestProcess_ConcurrentCloseJoins(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "fake-sidecar.mjs", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.AwaitHello(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = proc.Close()
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent close calls did not join")
	}
	for _, err := range errs {
		if err != nil {
			t.Fatalf("close=%v", err)
		}
	}
}

func TestProcess_DrainsNoisyStderrWithoutClosingPipe(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "fake-sidecar.mjs", "noisy_stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	if _, err := proc.AwaitHello(ctx); err != nil {
		t.Fatal(err)
	}
	line, err := proc.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Kind != "noise_done" {
		t.Fatalf("noisy child observed a closed stderr pipe: %+v", frame)
	}
	diagnostics := proc.Diagnostics()
	if len(diagnostics) > StderrLimit {
		t.Fatalf("stderr diagnostics grew to %d bytes", len(diagnostics))
	}
	if !strings.Contains(diagnostics, "raw content withheld") {
		t.Fatalf("stderr diagnostics did not report a bounded status: %q", diagnostics)
	}
}

func TestProcess_DiagnosticsWithholdsStderrWindow(t *testing.T) {
	t.Parallel()
	const secret = "OpaquePrivateCredentialThatMustNotEscape"
	p := &Process{secrets: []string{secret}, stderr: newStderrBuffer(StderrLimit)}
	// A bounded tail would normally cut through a credential that precedes an
	// ordinary noisy log; no raw window may survive that lossy edge.
	_, _ = p.stderr.Write([]byte(secret + strings.Repeat("x", StderrLimit-12)))
	diagnostics := p.Diagnostics()
	for _, fragment := range []string{secret, secret[:20], secret[len(secret)-12:], strings.Repeat("x", 32)} {
		if strings.Contains(diagnostics, fragment) {
			t.Fatalf("stderr diagnostics exposed %q: %q", fragment, diagnostics)
		}
	}
	if !strings.Contains(diagnostics, "raw content withheld") {
		t.Fatalf("stderr diagnostics did not report a bounded status: %q", diagnostics)
	}
}

func TestProcess_GracefulLeaderExitReapsDescendant(t *testing.T) {
	t.Parallel()
	proc, pid := startDescendantProcess(t, "descendant")
	if err := proc.Close(); err != nil {
		t.Fatalf("close=%v", err)
	}
	assertProcessGone(t, pid)
}

func TestProcess_LeaderCrashReapsDescendant(t *testing.T) {
	t.Parallel()
	proc, pid := startDescendantProcess(t, "descendant_crash")
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("crashed leader did not exit")
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("close=%v", err)
	}
	assertProcessGone(t, pid)
}

func startDescendantProcess(t *testing.T, mode string) (*Process, int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("process-group descendant ownership is certified on linux only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, testOptions(t, "fake-sidecar.mjs", mode))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.AwaitHello(ctx); err != nil {
		_ = proc.Close()
		t.Fatal(err)
	}
	line, err := proc.Recv(ctx)
	if err != nil {
		_ = proc.Close()
		t.Fatal(err)
	}
	var frame Frame
	if err := json.Unmarshal(line, &frame); err != nil {
		_ = proc.Close()
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(frame.Text)
	if frame.Kind != "descendant" || err != nil || pid <= 0 {
		_ = proc.Close()
		t.Fatalf("descendant frame=%+v err=%v", frame, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return proc, pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("descendant %d survived sidecar cleanup", pid)
}

func TestRedact_RemovesSecretsAndBearerShapes(t *testing.T) {
	t.Parallel()
	text := redact("token=super-secret Authorization: Bearer abc.def sk-ant-xyz", "super-secret")
	for _, forbidden := range []string{"super-secret", "abc.def", "sk-ant-xyz"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("redaction left %q in %q", forbidden, text)
		}
	}
}

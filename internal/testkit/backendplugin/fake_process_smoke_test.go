package backendplugin_test

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestFakeExecutable_ProcessSmokeNegotiateConfigureExecuteClose(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "aip-backendplugin-fake")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./internal/testkit/backendplugin/cmd/aip-backendplugin-fake")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-listen", "127.0.0.1:0", "-mode", "valid")
	cmd.Env = append(os.Environ(), "AIP_BACKENDPLUGIN_FAKE_MODE=valid", "AIP_BACKENDPLUGIN_FAKE_READY=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	addr, err := readReadyAddr(stderr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)

	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: "count_tokens"}, {Name: "finalize_billing"},
		},
	})
	if err != nil || !neg.GetCompatible() || neg.GetNegotiationToken() == "" {
		t.Fatalf("negotiate: %+v err=%v", neg, err)
	}
	if _, err := client.Describe(ctx, &backendpluginv1.DescribeRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Configure(ctx, &backendpluginv1.ConfigureRequest{
		InstanceId: "smoke", FactoryKind: "fake", ConfigYaml: []byte("kind: fake\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text := "hi"
	if err := stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind:       backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START,
		InstanceId: "smoke",
		Invocation: &backendpluginv1.Invocation{
			RequestId: "r", AttemptId: "a", ALegId: "a", BLegId: "b", CanonicalModelId: "fake-model",
			Messages: []*backendpluginv1.Message{{
				Role: backendpluginv1.Role_ROLE_USER,
				Parts: []*backendpluginv1.Part{{
					Kind: backendpluginv1.PartKind_PART_KIND_TEXT, Text: &text,
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	sawTerminal := false
	for {
		fr, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if fr.GetKind() == backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("missing terminal")
	}
	if _, err := client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: "smoke"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GracefulShutdown(ctx, &backendpluginv1.GracefulShutdownRequest{}); err != nil {
		t.Fatal(err)
	}
}

func readReadyAddr(r io.Reader, timeout time.Duration) (string, error) {
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if after, ok := strings.CutPrefix(line, "READY "); ok {
				ch <- result{addr: strings.TrimSpace(after)}
				return
			}
		}
		ch <- result{err: sc.Err()}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return "", res.err
		}
		if res.addr == "" {
			return "", io.EOF
		}
		return res.addr, nil
	case <-time.After(timeout):
		return "", context.DeadlineExceeded
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

//go:build windows

package processhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// winio remains the client dialer (overlapped); server uses processhost PlatformChannel.

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_PipeBytePing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lis, _, err := processhost.NewPlatformChannel().Listen(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	pipe := ""
	lep, ok := lis.(processhost.LaunchEnvProvider)
	if !ok {
		t.Fatalf("listener %T missing LaunchEnvProvider", lis)
	}
	for _, e := range lep.LaunchEnv() {
		_, v, _ := cutEnv(e)
		pipe = v
	}
	errCh := make(chan error, 1)
	go func() {
		c, err := dialPipeClient(pipe)
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 8)
		n, err := c.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		if string(buf[:n]) != "ping" {
			errCh <- fmt.Errorf("got %q", buf[:n])
			return
		}
		_, err = c.Write([]byte("pong"))
		errCh <- err
	}()
	conn, _, err := lis.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("got %q", buf[:n])
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_PipeGRPCInProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lis, _, err := processhost.NewPlatformChannel().Listen(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	pipe := ""
	lep, ok := lis.(processhost.LaunchEnvProvider)
	if !ok {
		t.Fatalf("listener %T missing LaunchEnvProvider", lis)
	}
	for _, e := range lep.LaunchEnv() {
		_, pipe, _ = cutEnv(e)
	}
	dialErr := make(chan error, 1)
	go func() {
		c, err := dialPipeClient(pipe)
		if err != nil {
			dialErr <- err
			return
		}
		svc := &testkit.FakeService{Mode: testkit.ModeValid}
		desc, _ := svc.Describe(context.Background())
		offer := backendplugin.ProtocolOffer{
			Major: desc.ProtocolMajor, Minor: desc.ProtocolMinor,
			DisableTransportRetries: true, Features: desc.Features,
		}
		gs := grpc.NewServer()
		backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, svc))
		dialErr <- nil
		_ = gs.Serve(&singleAccept{conn: c, block: make(chan struct{})})
	}()
	conn, _, err := lis.Accept(ctx)
	if err != nil {
		select {
		case e := <-dialErr:
			t.Fatalf("accept=%v dial=%v pipe=%q", err, e, pipe)
		default:
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := negotiateConfigureOverConn(ctx, t, conn); err != nil {
		t.Fatal(err)
	}
}

type singleAccept struct {
	conn  net.Conn
	block chan struct{}
	once  sync.Once
}

func (s *singleAccept) Accept() (net.Conn, error) {
	var c net.Conn
	s.once.Do(func() { c = s.conn })
	if c != nil {
		return c, nil
	}
	<-s.block
	return nil, net.ErrClosed
}
func (s *singleAccept) Close() error   { close(s.block); return s.conn.Close() }
func (s *singleAccept) Addr() net.Addr { return s.conn.LocalAddr() }

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_SecureChannel_LaunchJobPeerConfigure(t *testing.T) {
	art := stageBuiltPlugin(t)
	t.Cleanup(func() { _ = art.Close() })

	h := processhost.NewHost(processhost.Config{
		Launcher: processhost.NewPlatformLauncher(),
		Channel:  processhost.NewPlatformChannel(),
		WorkDir:  t.TempDir(),
	})
	t.Cleanup(func() { _ = h.Close() })

	var launches int
	h.SetLaunchProbe(func() { launches++ })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sid := mustCurrentUserSID(t)
	res, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:  "win-prod",
		Artifact:    art,
		Model:       processhost.ProcessModelPerInstance,
		ExpectedSID: sid,
		ConfigYAML:  []byte("kind: fake\n"),
		DialAndConfigure: func(ctx context.Context, conn net.Conn, peer processhost.PeerIdentity, gen uint64, _ backendplugin.SecretBundle, cfg []byte) error {
			if peer.PID <= 0 || peer.Generation != gen || peer.SID != sid {
				t.Fatalf("peer %+v gen=%d", peer, gen)
			}
			if string(cfg) != "kind: fake\n" {
				t.Fatal("config before/without auth path")
			}
			return negotiateConfigureOverConn(ctx, t, conn)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launches != 1 || res.PID <= 0 {
		t.Fatalf("launches=%d pid=%d", launches, res.PID)
	}
	if err := res.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_SubstitutionDeniedWhileHeld(t *testing.T) {
	art := stageBuiltPlugin(t)
	t.Cleanup(func() { _ = art.Close() })
	staged := art.StagedPath
	if err := os.Rename(staged, staged+".moved"); err == nil {
		t.Fatal("rename must fail while launch handle held")
	}
	if err := os.WriteFile(staged, []byte("tamper"), 0o600); err == nil {
		t.Fatal("overwrite must fail while launch handle held")
	}
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_HeldHandleSurvivesArtifactClose(t *testing.T) {
	art := stageBuiltPlugin(t)
	staged := art.StagedPath
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lis, _, err := processhost.NewPlatformChannel().Listen(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	extras := []string{}
	if ep, ok := lis.(processhost.LaunchEnvProvider); ok {
		extras = ep.LaunchEnv()
	}
	env, err := buildEnvForTest(extras)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := processhost.NewPlatformLauncher().Launch(ctx, processhost.LaunchSpec{
		Artifact: art, WorkDir: t.TempDir(), Env: env, Generation: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := art.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, staged+".moved"); err == nil {
		_ = proc.Close()
		t.Fatal("rename must fail while generation-owned launch handle held")
	}
	if err := os.WriteFile(staged, []byte("tamper"), 0o600); err == nil {
		_ = proc.Close()
		t.Fatal("overwrite must fail while generation-owned launch handle held")
	}
	if err := proc.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		// process may exit non-zero; still require handle release
		_ = err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := os.Rename(staged, staged+".moved")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rename should succeed after generation cleanup: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_PeerRejectWrongPID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lis, _, err := processhost.NewPlatformChannel().Listen(ctx, 99)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	if ul, ok := lis.(interface{ SetExpectedPID(int) }); ok {
		ul.SetExpectedPID(1) // unlikely real connector
	}
	pipeEnv := ""
	if ep, ok := lis.(processhost.LaunchEnvProvider); ok {
		for _, e := range ep.LaunchEnv() {
			pipeEnv = e
		}
	}
	bin := buildFakePlugin(t)
	cmd := exec.Command(bin)
	cmd.Env = []string{pipeEnv, "AIP_BACKENDPLUGIN_FAKE_MODE=valid"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	_, _, err = lis.Accept(ctx)
	if !errors.Is(err, processhost.ReasonPeerRejected) {
		t.Fatalf("want peer rejected, got %v", err)
	}
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_JobKillOnClose(t *testing.T) {
	art := stageBuiltPlugin(t)
	t.Cleanup(func() { _ = art.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lis, _, err := processhost.NewPlatformChannel().Listen(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	extras := []string{}
	if ep, ok := lis.(processhost.LaunchEnvProvider); ok {
		extras = ep.LaunchEnv()
	}
	env, err := buildEnvForTest(extras)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := processhost.NewPlatformLauncher().Launch(ctx, processhost.LaunchSpec{
		Artifact: art, WorkDir: t.TempDir(), Env: env, Generation: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := proc.PID()
	if ul, ok := lis.(interface{ SetExpectedPID(int) }); ok {
		ul.SetExpectedPID(pid)
	}
	if ul, ok := lis.(interface{ SetJobFromProcess(processhost.Process) }); ok {
		ul.SetJobFromProcess(proc)
	}
	conn, peer, err := lis.Accept(ctx)
	if err != nil {
		_ = proc.Close()
		t.Fatal(err)
	}
	_ = conn.Close()
	if peer.PID != pid {
		t.Fatalf("peer pid %d != %d", peer.PID, pid)
	}
	_ = proc.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return
		}
		var code uint32
		_ = windows.GetExitCodeProcess(h, &code)
		_ = windows.CloseHandle(h)
		if code != 259 { // STILL_ACTIVE
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("process still alive after job/process close")
}

//nolint:paralleltest // named-pipe/process host shares OS resources
func TestWindows_NoConfigBeforeAuth(t *testing.T) {
	art := stageBuiltPlugin(t)
	t.Cleanup(func() { _ = art.Close() })
	configured := false
	h := processhost.NewHost(processhost.Config{
		Launcher: processhost.NewPlatformLauncher(),
		Channel:  processhost.NewPlatformChannel(),
	})
	t.Cleanup(func() { _ = h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Force peer rejection by expecting impossible SID.
	_, err := h.Activate(ctx, processhost.ActivateRequest{
		InstanceID:  "no-cfg",
		Artifact:    art,
		Model:       processhost.ProcessModelPerInstance,
		ExpectedSID: "S-1-5-99-999",
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured = true
			return nil
		},
	})
	if err == nil || configured {
		t.Fatalf("must fail before configure; err=%v configured=%v", err, configured)
	}
}

func negotiateConfigureOverConn(ctx context.Context, t *testing.T, conn net.Conn) error {
	t.Helper()
	var once sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var out net.Conn
		err := net.ErrClosed
		once.Do(func() { out = conn; err = nil })
		if out == nil {
			return nil, err
		}
		return out, nil
	}
	gc, err := grpc.NewClient(
		"passthrough:///pipe",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	t.Cleanup(func() { _ = gc.Close() })
	client := backendpluginv1.NewBackendPluginClient(gc)
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: "count_tokens"}, {Name: "finalize_billing"}},
	})
	if err != nil {
		return fmt.Errorf("negotiate rpc: %w", err)
	}
	if !neg.GetCompatible() {
		return fmt.Errorf("negotiate incompatible: %+v", neg)
	}
	_, err = client.Configure(ctx, &backendpluginv1.ConfigureRequest{
		InstanceId: "win-prod", FactoryKind: "fake", ConfigYaml: []byte("kind: fake\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	return nil
}

func stageBuiltPlugin(t *testing.T) *trust.VerifiedArtifact {
	t.Helper()
	bin := buildFakePlugin(t)
	root := t.TempDir()
	rel := "bin/plugin.exe"
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	m := sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "com.aiproxer.fake", Version: "0.0.1", BuildID: "t",
		Executable: rel, SHA256: digest, ProtocolMajor: 1,
		Platforms: []sdkmanifest.Platform{{OS: "windows", Arch: "amd64"}},
		Exports: []sdkmanifest.Export{{
			Kind: "fake", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("%+v", res)
	}
	return res.Artifact
}

var (
	fixtureDir     string
	fakePluginOnce sync.Once
	fakePluginPath string
	errFakePlugin  error
)

// TestMain owns the package-level fixture directory so the fake connector
// binary is compiled once per run (a cold `go build` costs ~30s) and shared
// by every test. No test mutates the shared binary: stageBuiltPlugin copies
// it into per-test staging dirs, other tests only execute it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aip-processhost-fixture-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture dir:", err)
		os.Exit(1)
	}
	fixtureDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func buildFakePlugin(t *testing.T) string {
	t.Helper()
	fakePluginOnce.Do(func() {
		root, err := repoRoot()
		if err != nil {
			errFakePlugin = err
			return
		}
		bin := filepath.Join(fixtureDir, "aip-backendplugin-fake.exe")
		cmd := exec.Command("go", "build", "-o", bin, "./internal/testkit/backendplugin/cmd/aip-backendplugin-fake")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			errFakePlugin = fmt.Errorf("build fake: %v\n%s", err, out)
			return
		}
		fakePluginPath = bin
	})
	if errFakePlugin != nil {
		t.Fatal(errFakePlugin)
	}
	return fakePluginPath
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func mustCurrentUserSID(t *testing.T) string {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = token.Close() })
	tu, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return tu.User.Sid.String()
}

func buildEnvForTest(extras []string) ([]string, error) {
	out := make([]string, 0, len(extras)+1)
	out = append(out, "AIP_BACKENDPLUGIN_FAKE_MODE=valid")
	out = append(out, extras...)
	return out, nil
}

func cutEnv(e string) (string, string, bool) {
	for i := 0; i < len(e); i++ {
		if e[i] == '=' {
			return e[:i], e[i+1:], true
		}
	}
	return e, "", false
}

func dialPipeClient(name string) (net.Conn, error) {
	d := 5 * time.Second
	return winio.DialPipe(name, &d)
}

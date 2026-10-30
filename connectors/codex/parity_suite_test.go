// Codex connector parity and conformance proofs.
package codex_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/appserver"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/service"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/testemu"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestDescribe_BothFactories(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]backendplugin.FactoryDescriptor{}
	for _, f := range d.Factories {
		kinds[f.Kind] = f
	}
	httpF, ok := kinds[service.FactoryKindHTTP]
	if !ok || httpF.CredentialMode != backendplugin.CredentialModeStatic ||
		httpF.AccessScope != backendplugin.AccessScopeLocalOnly ||
		httpF.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("http=%+v", httpF)
	}
	appF, ok := kinds[service.FactoryKindAppServer]
	if !ok || appF.CredentialMode != backendplugin.CredentialModeNone ||
		appF.AccessScope != backendplugin.AccessScopeLocalOnly ||
		appF.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("app=%+v", appF)
	}
}

func TestConfigure_HTTPRequiresAPIToken(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindHTTP,
		"base_url: http://127.0.0.1:9\ncatalog_enabled: false\n"))
	if err == nil || !strings.Contains(err.Error(), "api_token") {
		t.Fatalf("err=%v", err)
	}
}

func TestConfigure_AppServerCatalogFallback(t *testing.T) {
	t.Parallel()
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindAppServer,
		"default_workspace: "+filepath.ToSlash(t.TempDir())+"\ncatalog_enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InventorySource != string(catalog.SourceShippedFallback) {
		t.Fatalf("source=%q", resp.InventorySource)
	}
	foundAuto := false
	for _, m := range resp.Models {
		if m.NativeModelID == "auto" {
			foundAuto = true
		}
	}
	if !foundAuto {
		t.Fatalf("models=%+v", resp.Models)
	}
}

func TestInventory_HTTPShippedFallbackProvenance(t *testing.T) {
	t.Parallel()
	emu := testemu.New(testemu.Config{Token: "tok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), mustCfgSecret(t, service.FactoryKindHTTP,
		"base_url: "+srv.URL+"\ncatalog_enabled: false\n", map[string]string{"api_token": "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InventorySource != string(catalog.SourceShippedFallback) {
		t.Fatalf("source=%q", resp.InventorySource)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected fallback slugs")
	}
}

func TestParity_DualHTTPInstancesNoTokenLeak(t *testing.T) {
	t.Parallel()
	aEmu := testemu.New(testemu.Config{Token: "a-token", OutputText: "a-ok"})
	bEmu := testemu.New(testemu.Config{Token: "b-token", OutputText: "b-ok"})
	aSrv := httptest.NewServer(aEmu.Handler())
	bSrv := httptest.NewServer(bEmu.Handler())
	t.Cleanup(aSrv.Close)
	t.Cleanup(bSrv.Close)
	aInst, err := service.New().Configure(context.Background(), mustCfgSecret(t, service.FactoryKindHTTP,
		"base_url: "+aSrv.URL+"\ncatalog_enabled: false\n", map[string]string{"api_token": "a-token"}))
	if err != nil {
		t.Fatal(err)
	}
	bInst, err := service.New().Configure(context.Background(), mustCfgSecret(t, service.FactoryKindHTTP,
		"base_url: "+bSrv.URL+"\ncatalog_enabled: false\n", map[string]string{"api_token": "b-token"}))
	if err != nil {
		t.Fatal(err)
	}
	slugA := firstNative(t, aInst)
	slugB := firstNative(t, bInst)
	mustExecute(t, aInst, service.FactoryKindHTTP+"/"+slugA, "x")
	mustExecute(t, bInst, service.FactoryKindHTTP+"/"+slugB, "x")
	if aEmu.LatestRequest().Authorization != "Bearer a-token" {
		t.Fatalf("a auth=%q", aEmu.LatestRequest().Authorization)
	}
	if bEmu.LatestRequest().Authorization != "Bearer b-token" {
		t.Fatalf("b auth=%q", bEmu.LatestRequest().Authorization)
	}
}

func TestParity_ConformanceHTTP(t *testing.T) {
	t.Parallel()
	emu := testemu.New(testemu.Config{Token: "tok", OutputText: "conformance-ok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), openaicompat.SecretBackedService{
		Service: service.New(),
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok")}},
	}, conformance.Options{
		FactoryKind:             service.FactoryKindHTTP,
		ConfigYAML:              []byte("base_url: " + srv.URL + "\ncatalog_enabled: false\n"),
		SampleModel:             service.FactoryKindHTTP + "/gpt-5.3-codex-spark",
		DisableUsageRequirement: false,
		VisionInputOnly:         true,
		SkipExecute:             true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_ConformanceAppServer(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	go runAgentSimulator(t, proc, simulatorOptions{})
	rep := conformance.RunWith(context.Background(), &service.Service{Starter: &fakeStarter{proc: proc}}, conformance.Options{
		FactoryKind:             service.FactoryKindAppServer,
		ConfigYAML:              []byte("default_workspace: " + strings.ReplaceAll(ws, `\`, `/`) + "\ncatalog_enabled: false\n"),
		SampleModel:             service.FactoryKindAppServer + "/auto",
		DisableUsageRequirement: false,
		VisionInputOnly:         true,
		SkipExecute:             true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_ExecuteHTTPStreaming(t *testing.T) {
	t.Parallel()
	emu := testemu.New(testemu.Config{Token: "tok", OutputText: "stream-ok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), mustCfgSecret(t, service.FactoryKindHTTP,
		"base_url: "+srv.URL+"\ncatalog_enabled: false\n", map[string]string{"api_token": "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	slug := firstNative(t, inst)
	frames := mustExecute(t, inst, service.FactoryKindHTTP+"/"+slug, "hi")
	if !framesHaveText(frames, "stream-ok") {
		t.Fatalf("frames=%v", frames)
	}
	captured := emu.LatestRequest()
	if captured.Authorization != "Bearer tok" {
		t.Fatalf("auth=%q", captured.Authorization)
	}
}

func TestParity_ExecuteAppServerStreaming(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	go runAgentSimulator(t, proc, simulatorOptions{})
	svc := &service.Service{Starter: &fakeStarter{proc: proc}}
	inst, err := svc.Configure(context.Background(), mustCfg(t, service.FactoryKindAppServer,
		"default_workspace: "+strings.ReplaceAll(ws, `\`, `/`)+"\ncatalog_enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, service.FactoryKindAppServer+"/auto", "hello codex")
	if !framesHaveText(frames, "Codex!") {
		t.Fatalf("frames=%v", frames)
	}
}

func TestParity_AppServerExeCacheIsolation(t *testing.T) {
	t.Parallel()
	dir1, dir2 := t.TempDir(), t.TempDir()
	f1 := filepath.Join(dir1, "codex.exe")
	f2 := filepath.Join(dir2, "codex.exe")
	for _, p := range []string{f1, f2} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c1, c2 := &acp.ExecutableCache{}, &acp.ExecutableCache{}
	e1, err := appserver.New(appserver.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: f1, Model: "auto", DefaultWorkspace: dir1},
		ExeCache:        c1, ModelCatalogSource: catalog.SourceShippedFallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := appserver.New(appserver.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: f2, Model: "auto", DefaultWorkspace: dir2},
		ExeCache:        c2, ModelCatalogSource: catalog.SourceShippedFallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.ExecutableCache() == e2.ExecutableCache() || e1.ResolvedExecutable() != f1 || e2.ResolvedExecutable() != f2 {
		t.Fatal("instance isolation failed")
	}
}

func TestParity_AppServerCancelAndStaleReap(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	go runAgentSimulator(t, proc, simulatorOptions{hangAfterFirstDelta: true})
	svc := &service.Service{Starter: &fakeStarter{proc: proc}}
	inst, err := svc.Configure(context.Background(), mustCfg(t, service.FactoryKindAppServer,
		"default_workspace: "+strings.ReplaceAll(ws, `\`, `/`)+"\ncatalog_enabled: false\nstale_kill_delay_seconds: 0.02\n"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: service.FactoryKindAppServer + "/auto",
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hi")}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: ctx, inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}}}
	done := make(chan error, 1)
	go func() { done <- inst.Execute(ms) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ms.outLen() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled execute error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not return after cancel")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if proc.killCount.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected stale reap Kill after cancel/close, kills=%d", proc.killCount.Load())
}

func mustCfg(t *testing.T, kind, yaml string) backendplugin.ConfigureRequest {
	t.Helper()
	return mustCfgSecret(t, kind, yaml, nil)
}

// mustCfgSecret builds a ConfigureRequest with host-delivered secrets: the
// only credential path for static exports after the static YAML/file
// remediation (task 11). A nil secrets map configures without secrets.
func mustCfgSecret(t *testing.T, kind, yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	t.Helper()
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind: kind, InstanceID: "i1", ConfigYAML: []byte(yaml),
		Secrets:     backendplugin.SecretBundle{Values: values},
		Negotiation: backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	}
}

func firstNative(t *testing.T, inst backendplugin.ConfiguredInstance) string {
	t.Helper()
	resp, err := inst.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) == 0 {
		t.Fatal("no models")
	}
	return resp.Models[0].NativeModelID
}

func mustExecute(t *testing.T, inst backendplugin.ConfiguredInstance, model, text string) []backendplugin.ServerFrame {
	t.Helper()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: model,
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		t.Fatal(err)
	}
	return ms.outbox
}

func framesHaveText(frames []backendplugin.ServerFrame, want string) bool {
	for _, fr := range frames {
		if fr.Kind != backendplugin.ServerFrameEvent || fr.Event == nil {
			continue
		}
		if fr.Event.Kind == backendplugin.EventTextDelta && fr.Event.Delta != nil && strings.Contains(*fr.Event.Delta, want) {
			return true
		}
	}
	return false
}

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	mu     sync.Mutex
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }
func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbox = append(m.outbox, frame)
	return nil
}

func (m *memStream) outLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.outbox)
}

type fakeProcess struct {
	pid       int
	killCount atomic.Int32
	stdinR    *io.PipeReader
	stdinW    *io.PipeWriter
	stdoutR   *io.PipeReader
	stdoutW   *io.PipeWriter
	stderrR   *io.PipeReader
	stderrW   *io.PipeWriter
	stdinOnce sync.Once
	scanner   *bufio.Scanner
}

var nextFakePID atomic.Int64

func newFakeProcess(t *testing.T) *fakeProcess {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &fakeProcess{
		pid: int(nextFakePID.Add(1)), stdinR: stdinR, stdinW: stdinW,
		stdoutR: stdoutR, stdoutW: stdoutW, stderrR: stderrR, stderrW: stderrW,
	}
}

func (p *fakeProcess) PID() int              { return p.pid }
func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProcess) Stdout() io.ReadCloser { return p.stdoutR }
func (p *fakeProcess) Stderr() io.ReadCloser { return p.stderrR }
func (p *fakeProcess) Wait() error           { return nil }
func (p *fakeProcess) Kill() error {
	p.killCount.Add(1)
	_ = p.stdinW.Close()
	_ = p.stdoutW.Close()
	_ = p.stderrW.Close()
	return nil
}
func (p *fakeProcess) writeStdout(line string) { _, _ = p.stdoutW.Write([]byte(line + "\n")) }
func (p *fakeProcess) readStdinLine() (string, error) {
	p.stdinOnce.Do(func() {
		p.scanner = bufio.NewScanner(p.stdinR)
		p.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	})
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return p.scanner.Text(), nil
}

type fakeStarter struct{ proc *fakeProcess }

func (f *fakeStarter) Start([]string, string, []string) (acp.Process, error) { return f.proc, nil }

type simulatorOptions struct {
	threadID            string
	hangAfterFirstDelta bool
}

func runAgentSimulator(t *testing.T, proc *fakeProcess, opts simulatorOptions) {
	t.Helper()
	if opts.threadID == "" {
		opts.threadID = "thread-fake-001"
	}
	method, id, _, err := readRequest(proc)
	if err != nil || method != "initialize" {
		return
	}
	writeResponse(proc, id, map[string]any{"protocolVersion": 1})
	method, _, _, err = readRequest(proc)
	if err != nil || method != "initialized" {
		return
	}
	method, id, _, err = readRequest(proc)
	if err != nil || method != "thread/start" {
		return
	}
	writeResponse(proc, id, map[string]any{"id": opts.threadID})
	method, id, _, err = readRequest(proc)
	if err != nil || method != "turn/start" {
		return
	}
	writeNotification(proc, "item/agentMessage/delta", map[string]any{"delta": "Hello from"})
	if opts.hangAfterFirstDelta {
		_, _ = proc.readStdinLine()
		return
	}
	writeNotification(proc, "item/agentMessage/delta", map[string]any{"delta": " Codex!"})
	writeTerminalResponse(proc, id, "turn-fake-001")
}

//go:fix inline
func strPtr(s string) *string { return new(s) }

func readRequest(proc *fakeProcess) (method string, id json.RawMessage, params json.RawMessage, err error) {
	raw, err := proc.readStdinLine()
	if err != nil {
		return "", nil, nil, err
	}
	var parsed struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", nil, nil, err
	}
	return parsed.Method, parsed.ID, parsed.Params, nil
}

func writeResponse(proc *fakeProcess, id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	proc.writeStdout(string(b))
}

func writeNotification(proc *fakeProcess, method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	proc.writeStdout(string(b))
}

func writeTerminalResponse(proc *fakeProcess, id json.RawMessage, turnID string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"turnId": turnID}})
	proc.writeStdout(string(b))
}

// servingCodexBinary returns an executable path whose `debug models` output is
// the current content of servedPath, so tests can rotate discovery results
// between ListModels calls. Discovery is enabled unless the YAML opts out.
func servingCodexBinary(t *testing.T, servedPath string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ncat \"" + servedPath + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func writeServedCatalog(t *testing.T, servedPath, slug string) {
	t.Helper()
	doc := `{"models":[{"slug":"` + slug + `","supported_reasoning_levels":[{"effort":"medium"}]}]}`
	if err := os.WriteFile(servedPath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativesOf(t *testing.T, inst backendplugin.ConfiguredInstance) map[string]bool {
	t.Helper()
	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range resp.Models {
		out[m.NativeModelID] = true
	}
	return out
}

func TestInventory_HTTPRefreshRediscovers(t *testing.T) {
	t.Parallel()
	emu := testemu.New(testemu.Config{Token: "tok"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)
	served := filepath.Join(t.TempDir(), "models.json")
	writeServedCatalog(t, served, "gpt-9")
	inst, err := service.New().Configure(context.Background(), mustCfgSecret(t, service.FactoryKindHTTP,
		"base_url: "+srv.URL+"\nexecutable: "+servingCodexBinary(t, served)+"\n", map[string]string{"api_token": "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := nativesOf(t, inst); !got["gpt-9"] || len(got) != 1 {
		t.Fatalf("first inventory = %+v, want exactly [gpt-9]", got)
	}
	writeServedCatalog(t, served, "gpt-10")
	if got := nativesOf(t, inst); !got["gpt-10"] || len(got) != 1 {
		t.Fatalf("refreshed inventory = %+v, want exactly [gpt-10]", got)
	}
}

func TestInventory_AppServerRefreshRediscovers(t *testing.T) {
	t.Parallel()
	served := filepath.Join(t.TempDir(), "models.json")
	writeServedCatalog(t, served, "gpt-9")
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindAppServer,
		"default_workspace: "+filepath.ToSlash(t.TempDir())+"\nexecutable: "+servingCodexBinary(t, served)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := nativesOf(t, inst); !got["gpt-9"] || len(got) != 2 {
		t.Fatalf("first inventory = %+v, want [auto gpt-9]", got)
	}
	writeServedCatalog(t, served, "gpt-10")
	if got := nativesOf(t, inst); !got["gpt-10"] || len(got) != 2 {
		t.Fatalf("refreshed inventory = %+v, want [auto gpt-10]", got)
	}
	if got := nativesOf(t, inst); got["gpt-9"] {
		t.Fatalf("removed slug gpt-9 still advertised: %+v", got)
	}
}

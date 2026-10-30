//go:build productsmoke

package productsmoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	// binEnv names the prebuilt aipstd binary. It is required: the smoke never
	// rebuilds and never skips.
	binEnv = "AIP_PRODUCT_SMOKE_BIN"
	// canaryEnvVar is the backend credential root referenced by the rendered
	// config; only its name is written to YAML.
	canaryEnvVar = "PRODUCT_SMOKE_API_KEY"
	// managementAddressEnv enables the optional management reload API. It is the
	// only product override the harness ever forwards, and only for a journey
	// that asks for it; the dedicated bearer token is never forwarded.
	managementAddressEnv = "AIP_RELOAD_MANAGEMENT_ADDRESS"

	sutStartupDeadline = 30 * time.Second
	// startupCleanupReserve keeps part of the owning test deadline for the
	// registered cleanup (Stop and log close) when readiness gives up.
	startupCleanupReserve = 5 * time.Second
	readyProbeTimeout     = 2 * time.Second
	readyProbeInterval    = 50 * time.Millisecond
	readyBodyExcerpt      = 512
	processWaitDelay      = 3 * time.Second
	gracefulStopBound     = 10 * time.Second
	killStopBound         = 5 * time.Second
	clientBodyLimit       = 8 << 20

	readyBackendID = "ready"
	readyCanonical = "ready/model"
	readyNative    = "ready-native"
	readyRoute     = readyBackendID + ":" + readyCanonical
	readyContent   = "product smoke ready"
	// readyProbeMessage is the readiness request's only message; a journey can
	// assert it stays the only one to prove a non-matching model was untouched.
	readyProbeMessage = "product smoke readiness probe"

	kindOpenAILegacyCompatible = "custom-openai-legacy-compatible"
	kindAnthropicCompatible    = "custom-anthropic-compatible"
)

// smokeHTTPClient bounds every client exchange; request contexts still carry
// the per-exchange deadline.
var smokeHTTPClient = &http.Client{Timeout: 60 * time.Second}

// errClientCaptureIncomplete marks a client response that continued past
// clientBodyLimit. The unread suffix was never inspected, so the capture cannot
// support any claim about what the client did or did not receive.
var errClientCaptureIncomplete = errors.New("incomplete client response capture")

// modelWiring is one inline model row in a compatible backend.
type modelWiring struct {
	Canonical string
	Native    string
}

// backendWiring is one linked backend pointed at a fake provider.
type backendWiring struct {
	ID      string
	Kind    string
	BaseURL string
	Models  []modelWiring
}

// modelSystemPromptFixture is one opt-in model-system-prompt rule. The harness
// enables the feature only when a journey supplies it, so every other
// configuration revision stays exactly as it was.
type modelSystemPromptFixture struct {
	// RuleID becomes the rule's id and part of its derived overlay ID.
	RuleID string
	// ModelPattern matches the accepted logical model, not the native id.
	ModelPattern string
	// Append is the standalone system instruction added before the client's
	// own messages.
	Append string
}

// sutConfig describes the proxy configuration a journey needs.
type sutConfig struct {
	// Canary is the backend credential bound to each backend's credential
	// environment root. Required; it never appears in YAML.
	Canary string
	// Frontends default to openai-legacy.
	Frontends []string
	// Backends are the journey backends; a dedicated readiness backend is
	// appended automatically so readiness never pollutes their request log.
	Backends []backendWiring
	// DefaultRoute defaults to the first journey backend row.
	DefaultRoute string
	MaxAttempts  int
	// Management enables the opt-in management reload API on its own loopback
	// port (AIP_RELOAD_MANAGEMENT_ADDRESS). Single-user loopback uses the
	// documented local-trust posture, so no bearer token is configured.
	Management bool
	// ModelSystemPrompt enables the model-system-prompt feature with one rule
	// when set.
	ModelSystemPrompt *modelSystemPromptFixture
}

// sutRender holds everything needed to render one configuration revision, so a
// reload journey can build a candidate from the same shape the proxy started
// with instead of duplicating the YAML.
type sutRender struct {
	addr              string
	defaultRoute      string
	maxAttempts       int
	loggingLevel      string
	frontends         []string
	backends          []backendWiring
	modelSystemPrompt *modelSystemPromptFixture
}

// clone copies the slices a candidate mutation may edit.
func (r sutRender) clone() sutRender {
	out := r
	out.frontends = append([]string(nil), r.frontends...)
	out.backends = make([]backendWiring, len(r.backends))
	for i, backend := range r.backends {
		out.backends[i] = backend
		out.backends[i].Models = append([]modelWiring(nil), backend.Models...)
	}
	if r.modelSystemPrompt != nil {
		fixture := *r.modelSystemPrompt
		out.modelSystemPrompt = &fixture
	}
	return out
}

// render renders the documented YAML shape used by the smoke.
func (r sutRender) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "server:\n  address: %q\n", r.addr)
	b.WriteString("access:\n  mode: single_user\n")
	fmt.Fprintf(&b, "routing:\n  max_attempts: %d\n  default_route: %q\n", r.maxAttempts, r.defaultRoute)
	b.WriteString("continuity:\n  in_memory: true\n  store: memory\n")
	fmt.Fprintf(&b, "logging:\n  level: %s\n  format: text\n", r.loggingLevel)
	b.WriteString("diagnostics:\n  enabled: false\n")
	b.WriteString("plugins:\n  frontends:\n")
	for _, id := range r.frontends {
		fmt.Fprintf(&b, "    - id: %s\n      enabled: true\n      config: {}\n", id)
	}
	if prompt := r.modelSystemPrompt; prompt != nil {
		b.WriteString("  features:\n    - id: model-system-prompt\n      enabled: true\n      config:\n        rules:\n")
		fmt.Fprintf(&b, "          - id: %q\n            model_pattern: %q\n            append: %q\n", prompt.RuleID, prompt.ModelPattern, prompt.Append)
	}
	b.WriteString("  backends:\n")
	for _, backend := range r.backends {
		fmt.Fprintf(&b, "    - id: %s\n      kind: %s\n      enabled: true\n      config:\n", backend.ID, backend.Kind)
		fmt.Fprintf(&b, "        backend_prefix: %s\n", backend.ID)
		fmt.Fprintf(&b, "        base_url: %q\n", backend.BaseURL)
		fmt.Fprintf(&b, "        api_key_env_var_root: %s\n", credentialRoot(backend.ID))
		b.WriteString("        models:\n          source: inline\n          items:\n")
		for _, model := range backend.Models {
			fmt.Fprintf(&b, "            - canonical_id: %q\n              native_id: %q\n", model.Canonical, model.Native)
		}
	}
	return b.String()
}

// setBackendBaseURL points one backend row at another provider. The backend ID,
// credential root and model wiring stay fixed, so a reload that uses this
// changes only where the same route sends its upstream request.
func (r *sutRender) setBackendBaseURL(backendID, baseURL string) error {
	for i := range r.backends {
		if r.backends[i].ID == backendID {
			r.backends[i].BaseURL = baseURL
			return nil
		}
	}
	return fmt.Errorf("rendered configuration has no backend %q", backendID)
}

// sut is the proxy under test: a real aipstd process, its config and log.
type sut struct {
	addr           string
	managementAddr string
	dir            string
	configPath     string
	render         sutRender
	evidence       *evidence
	logs           *logSink

	ready       *fakeProvider
	readyProbes int
	readyWire   *clientWire

	cmd     *exec.Cmd
	done    chan struct{}
	exitErr error

	stopOnce sync.Once
	stopErr  error
}

// startSUT renders a config, starts the real binary and waits until a real
// inference request through the proxy has succeeded.
func startSUT(t *testing.T, cfg sutConfig) *sut {
	t.Helper()
	bin := requireProductSmokeBinary(t)
	if cfg.Canary == "" {
		t.Fatalf("product smoke: sutConfig.Canary is required")
	}
	frontends := append([]string(nil), cfg.Frontends...)
	if len(frontends) == 0 {
		frontends = []string{"openai-legacy"}
	}
	readyProvider := newOpenAILegacyProvider(t, cfg.Canary, fakeScript{
		Model:  readyNative,
		Deltas: []string{readyContent},
		Usage:  fakeUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	})
	backends := append([]backendWiring(nil), cfg.Backends...)
	backends = append(backends, backendWiring{
		ID:      readyBackendID,
		Kind:    kindOpenAILegacyCompatible,
		BaseURL: readyProvider.OpenAILegacyBaseURL(),
		Models:  []modelWiring{{Canonical: readyCanonical, Native: readyNative}},
	})
	defaultRoute := cfg.DefaultRoute
	if defaultRoute == "" {
		defaultRoute = firstRoute(cfg.Backends)
	}
	if defaultRoute == "" {
		defaultRoute = readyRoute
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	addr := freeLoopbackAddr(t)
	managementAddr := ""
	if cfg.Management {
		managementAddr = freeLoopbackAddr(t)
		// The data-plane port was released before the process bound it, so the
		// kernel may hand the same port out twice. Two listeners on one address
		// would make the child fail startup, not exercise the journey.
		for tries := 0; managementAddr == addr; tries++ {
			if tries == 5 {
				t.Fatalf("product smoke: could not reserve a management port distinct from %s", addr)
			}
			managementAddr = freeLoopbackAddr(t)
		}
	}
	render := sutRender{
		addr:              addr,
		defaultRoute:      defaultRoute,
		maxAttempts:       maxAttempts,
		loggingLevel:      "info",
		frontends:         frontends,
		backends:          backends,
		modelSystemPrompt: cfg.ModelSystemPrompt,
	}
	configText := render.render()
	if strings.Contains(configText, cfg.Canary) {
		t.Fatalf("product smoke: the canary must reach aipstd through the backend credential environment, not the config file")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "proxy.yaml")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("product smoke: write config %q: %v", configPath, err)
	}
	ev := newEvidence(t, cfg.Canary)
	ev.write("proxy.yaml", configText)
	sink := newLogSink(t, ev.path("proxy.log"), cfg.Canary)

	cmd := exec.Command(bin, "--config", configPath, "serve")
	cmd.Dir = dir
	cmd.Env = sutEnvironment(backends, cfg.Canary, managementEnvironment(managementAddr)...)
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.WaitDelay = processWaitDelay
	if err := cmd.Start(); err != nil {
		_ = sink.Close()
		t.Fatalf("product smoke: start %s: %v", bin, err)
	}
	h := &sut{
		addr:           addr,
		managementAddr: managementAddr,
		dir:            dir,
		configPath:     configPath,
		render:         render,
		evidence:       ev,
		logs:           sink,
		ready:          readyProvider,
		cmd:            cmd,
		done:           make(chan struct{}),
	}
	go func() {
		h.exitErr = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		if err := h.Stop(); err != nil {
			t.Errorf("product smoke: stop proxy: %v", err)
		}
		if err := sink.Close(); err != nil {
			t.Errorf("product smoke: close proxy log: %v", err)
		}
	})
	if err := h.waitReady(t.Context()); err != nil {
		failf(t, failureContext{sut: h, provider: h.ReadyProvider(), wire: h.LastReadyWire()}, "startup: %v", err)
	}
	return h
}

// sutEnvPassthrough lists the non-product environment variables the child needs
// to launch and to find its temporary directory on POSIX and Windows. Every
// inherited product override (AIP_*) and every other ambient setting is
// deliberately not passed: a journey must see only the configuration it
// rendered plus the canary roots it owns.
var sutEnvPassthrough = []string{
	"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE",
	"SystemRoot", "windir", "SystemDrive", "ComSpec", "PATHEXT",
	"TEMP", "TMP", "USERPROFILE", "USERNAME", "NUMBER_OF_PROCESSORS",
}

// sutEnvironment builds the child environment explicitly. extra carries the
// harness-owned product overrides this journey asked for; nothing else is
// inherited.
func sutEnvironment(backends []backendWiring, canary string, extra ...string) []string {
	env := make([]string, 0, len(sutEnvPassthrough)+len(backends)+len(extra))
	for _, name := range sutEnvPassthrough {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	env = append(env, canaryEnvironment(backends, canary)...)
	return append(env, extra...)
}

// managementEnvironment enables the opt-in management reload API for one child.
// Only the loopback address is forwarded: the dedicated bearer token is never
// inherited, so the journey exercises the documented local-trust posture for a
// single-user loopback bind.
func managementEnvironment(addr string) []string {
	if strings.TrimSpace(addr) == "" {
		return nil
	}
	return []string{managementAddressEnv + "=" + addr}
}

// BaseURL is the proxy origin clients talk to.
func (h *sut) BaseURL() string { return "http://" + h.addr }

// ManagementURL is the management reload API origin, empty when the journey
// did not opt in.
func (h *sut) ManagementURL() string {
	if h.managementAddr == "" {
		return ""
	}
	return "http://" + h.managementAddr
}

// CandidateConfig renders a configuration revision from the startup shape after
// mutate edits a copy. The server address and everything the mutation leaves
// alone stay exactly as started.
func (h *sut) CandidateConfig(mutate func(*sutRender) error) (string, error) {
	render := h.render.clone()
	if mutate != nil {
		if err := mutate(&render); err != nil {
			return "", err
		}
	}
	return render.render(), nil
}

// ReplaceConfig atomically replaces the fixed configuration path and records the
// candidate in the journey's evidence. The product rejects an in-place rewrite of
// the fixed source, so the candidate is written to a sibling file and renamed
// over the path: same directory, new file identity.
func (h *sut) ReplaceConfig(name, content string) error {
	tmp, err := os.CreateTemp(h.dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create candidate config in %q: %w", h.dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write candidate config %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close candidate config %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, h.configPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomically replace %q with the candidate: %w", h.configPath, err)
	}
	h.evidence.write(name, content)
	return nil
}

// ReadyProbes reports how many inference readiness requests succeeded before
// the harness declared the proxy ready.
func (h *sut) ReadyProbes() int { return h.readyProbes }

// ReadyProvider is the provider that served the readiness probes.
func (h *sut) ReadyProvider() *fakeProvider { return h.ready }

// LastReadyWire is the last readiness exchange, retained so a startup failure
// can report the client-side transcript. It is nil before the first probe.
func (h *sut) LastReadyWire() *clientWire { return h.readyWire }

// Alive reports whether the proxy process is still running.
func (h *sut) Alive() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// Stop stops the proxy within a bound and reports a process that would not go
// away. POSIX sends SIGTERM first; Windows kills. Graceful exit is not part of
// this contract: only the dedicated shutdown journey asserts an exit code and
// port release.
func (h *sut) Stop() error {
	h.stopOnce.Do(func() { h.stopErr = h.stop() })
	return h.stopErr
}

func (h *sut) stop() error {
	if h.cmd == nil || h.cmd.Process == nil {
		return nil
	}
	select {
	case <-h.done:
		return nil
	default:
	}
	if runtime.GOOS == "windows" {
		_ = h.cmd.Process.Kill()
		select {
		case <-h.done:
			return nil
		case <-time.After(killStopBound):
			return fmt.Errorf("proxy process %d survived kill", h.cmd.Process.Pid)
		}
	}
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = h.cmd.Process.Kill()
	}
	select {
	case <-h.done:
		return nil
	case <-time.After(gracefulStopBound):
	}
	// The grace period is a courtesy, not a contract: the signal leads to a
	// graceful exit in the shipped product, but ordinary cleanup only requires
	// the process to be gone. A successful kill followed by an observed exit
	// is cleanup success; a process still alive after the kill bound is not.
	_ = h.cmd.Process.Kill()
	select {
	case <-h.done:
		return nil
	case <-time.After(killStopBound):
		return fmt.Errorf("proxy process %d survived kill", h.cmd.Process.Pid)
	}
}

// waitReady polls until a real inference request through the proxy succeeds or
// the process exits. An open port is not readiness.
func (h *sut) waitReady(ctx context.Context) error {
	return h.waitReadyInference(ctx, sutStartupDeadline, startupCleanupReserve)
}

// waitReadyInference polls until a real inference request through the proxy
// succeeds, which is the only readiness signal the smoke trusts. The poll loop
// is bounded by the smaller of the explicit startup cap and the owning test
// context's deadline minus a cleanup reserve, and it stops as soon as that
// context is cancelled. Every probe is retained as the last readiness exchange
// so a startup failure can report it.
func (h *sut) waitReadyInference(ctx context.Context, cap, reserve time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// The startup cap bounds a healthy poll; the owning test context can only
	// shorten it, and the reserve keeps time for the registered cleanup.
	limit := cap
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) - reserve; remaining < limit {
			limit = remaining
		}
	}
	if limit < 0 {
		limit = 0
	}
	bound := time.Now().Add(limit)
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readiness polling stopped: %v; last probe: %v", err, probeSummary(last))
		}
		select {
		case <-h.done:
			return fmt.Errorf("proxy exited before readiness: %v", h.exitErr)
		default:
		}
		if !time.Now().Before(bound) {
			return fmt.Errorf("proxy did not serve a readiness inference request within %s: %v", limit, probeSummary(last))
		}
		probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
		wire := doJSON(probeCtx, h.BaseURL()+openAIChatPath, readyProbeBody())
		cancel()
		h.readyWire = wire
		if wire.Err != nil {
			last = wire.Err
		} else if wire.Status != http.StatusOK {
			last = fmt.Errorf("readiness request returned status %d: %s", wire.Status, excerpt(h.evidence.redact, string(wire.Body), readyBodyExcerpt))
		} else if content, err := assistantContent(wire.Body); err != nil {
			last = err
		} else if content != readyContent {
			last = fmt.Errorf("readiness response content %q, want %q", h.evidence.redact(content), readyContent)
		} else {
			h.readyProbes++
			return nil
		}
		select {
		case <-ctx.Done():
		case <-h.done:
		case <-time.After(readyProbeInterval):
		}
	}
}

// probeSummary renders the last probe error for a readiness failure message.
func probeSummary(last error) string {
	if last == nil {
		return "no attempt recorded"
	}
	return last.Error()
}

// readyProbeBody routes the probe to the dedicated readiness backend.
func readyProbeBody() []byte {
	raw, err := json.Marshal(map[string]any{
		"model":  readyRoute,
		"stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": readyProbeMessage},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// assistantContent reads choices[0].message.content from a chat completion.
func assistantContent(body []byte) (string, error) {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode chat completion: %v", err)
	}
	if len(payload.Choices) == 0 {
		return "", errors.New("chat completion carried no choices")
	}
	return payload.Choices[0].Message.Content, nil
}

// doJSON posts body to url and records the client wire transcript.
func doJSON(ctx context.Context, url string, body []byte) *clientWire {
	return doRequest(ctx, http.MethodPost, url, body)
}

// doRequest performs one client exchange and records the wire transcript.
func doRequest(ctx context.Context, method, url string, body []byte) *clientWire {
	wire := &clientWire{Method: method, URL: url, RequestBody: body}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		wire.Err = err
		return wire
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := smokeHTTPClient.Do(req)
	if err != nil {
		wire.Err = err
		return wire
	}
	defer func() { _ = resp.Body.Close() }()
	wire.Status = resp.StatusCode
	wire.Header = resp.Header.Clone()
	// Read one byte past the bound: that byte is the only way to distinguish a
	// response that ended exactly at the limit from one that continued past it.
	// A silently truncated capture could hide a credential in the unread
	// suffix, so exhausting the bound is an explicit incomplete capture, never a
	// successful observation. Only the bounded prefix is retained and rendered.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, clientBodyLimit+1))
	if len(raw) > clientBodyLimit {
		raw = raw[:clientBodyLimit]
		wire.Err = fmt.Errorf("%w: the body exceeded the %d-byte capture bound; the unread suffix was never inspected", errClientCaptureIncomplete, clientBodyLimit)
	} else if err != nil {
		wire.Err = fmt.Errorf("read response body: %w", err)
	}
	wire.Body = raw
	return wire
}

// TestDoJSONReportsIncompleteCapture proves a response that continues past the
// client capture bound is reported as incomplete instead of accepted as a
// complete observation. The stream prefix is valid on its own, so only the
// unseen suffix distinguishes the two captures; a credential beyond the bound
// must fail the canary claim rather than pass it.
func TestDoJSONReportsIncompleteCapture(t *testing.T) {
	t.Parallel()
	plain := func(s string) string { return s }
	prefix := scriptedOpenAIStreamBytes(smokeScript())
	control := &clientWire{
		Method: http.MethodPost,
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   prefix,
	}
	if err := checkOpenAIStream(control, smokeScript(), plain); err != nil {
		t.Fatalf("the scripted stream prefix is not a complete stream: %v", err)
	}
	// Short parser-irrelevant comment lines stay under the SSE scanner's token
	// cap while they push the canary past the capture bound.
	filler := bytes.Repeat([]byte(": pad\n"), clientBodyLimit/len(": pad\n")+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(prefix)
		_, _ = w.Write(filler)
		_, _ = fmt.Fprintf(w, ": %s\n", testCanary)
	}))
	defer server.Close()

	wire := doJSON(t.Context(), server.URL+openAIChatPath, []byte(`{"stream":true}`))
	if wire.Err == nil {
		t.Fatalf("doJSON accepted an oversized response: captured %d bytes with no error", len(wire.Body))
	}
	if !strings.Contains(wire.Err.Error(), "capture bound") {
		t.Fatalf("the oversized capture error does not name the capture bound: %v", wire.Err)
	}
	if len(wire.Body) > clientBodyLimit {
		t.Fatalf("the incomplete capture kept %d bytes, want at most the %d-byte bound", len(wire.Body), clientBodyLimit)
	}
	if err := checkOpenAIStream(wire, smokeScript(), plain); err == nil {
		t.Fatal("the stream check accepted an incomplete capture")
	}
	if err := wire.canaryAbsenceError(testCanary); err == nil {
		t.Fatal("the canary check cleared an incomplete capture whose unread suffix was never inspected")
	}
	// The same guard still clears a complete capture and still detects a
	// credential inside one, so it is not merely refusing every capture.
	if err := control.canaryAbsenceError(testCanary); err != nil {
		t.Fatalf("the complete control capture did not clear the absent canary: %v", err)
	}
	leaked := &clientWire{
		Status: http.StatusOK,
		Header: http.Header{},
		Body:   append(append([]byte(nil), prefix...), testCanary...),
	}
	if err := leaked.canaryAbsenceError(testCanary); err == nil {
		t.Fatal("the canary check cleared a complete capture that carried the canary in its body")
	}
}

// credentialRoot is the per-backend environment root that carries the canary.
// Distinct instances need distinct roots: the host rejects a root shared by two
// configured backends.
func credentialRoot(backendID string) string {
	return canaryEnvVar + "_" + strings.ToUpper(strings.ReplaceAll(backendID, "-", "_"))
}

// canaryEnvironment binds the canary to every backend's credential root.
func canaryEnvironment(backends []backendWiring, canary string) []string {
	env := make([]string, 0, len(backends))
	for _, backend := range backends {
		env = append(env, credentialRoot(backend.ID)+"="+canary)
	}
	return env
}

// requireProductSmokeBinary resolves the prebuilt binary or fails the journey.
func requireProductSmokeBinary(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(binEnv))
	if path == "" {
		t.Fatalf("%s is not set: build ./cmd/aipstd and pass its path; the product smoke fails instead of skipping", binEnv)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s=%q: %v", binEnv, path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s=%q is a directory, want the aipstd binary", binEnv, path)
	}
	return path
}

func firstRoute(backends []backendWiring) string {
	for _, backend := range backends {
		if backend.ID != "" && len(backend.Models) > 0 && backend.Models[0].Canonical != "" {
			return backend.ID + ":" + backend.Models[0].Canonical
		}
	}
	return ""
}

// smokeLoopbackWindowSize is how many candidate ports the harness owns. The
// window is placed below the kernel's ephemeral range so that a concurrent test
// process reserving a port with net.Listen("tcp", "127.0.0.1:0") can never be
// handed one of our ports, keep the address after releasing it, and deliver its
// own traffic to our listener. That cross-process mix-up failed a journey's
// "exactly one upstream request" claim for a reason the product never caused.
const smokeLoopbackWindowSize = 10000

var (
	smokeLoopbackWindowOnce sync.Once
	smokeLoopbackWindowBase int
	smokeLoopbackWindowSpan int
)

// smokeLoopbackWindow reports the port window harness-owned listeners bind from.
func smokeLoopbackWindow() (base, span int) {
	smokeLoopbackWindowOnce.Do(func() {
		smokeLoopbackWindowBase, smokeLoopbackWindowSpan = 20000, smokeLoopbackWindowSize
		low, high, ok := kernelEphemeralPortRange()
		if !ok {
			return
		}
		switch {
		case low-smokeLoopbackWindowSize >= 1024:
			smokeLoopbackWindowBase = low - smokeLoopbackWindowSize
		case 65535-high >= smokeLoopbackWindowSize:
			smokeLoopbackWindowBase = high + 1
		}
	})
	return smokeLoopbackWindowBase, smokeLoopbackWindowSpan
}

// kernelEphemeralPortRange reports the ports the kernel hands to a
// net.Listen("tcp", "127.0.0.1:0") caller. Only Linux publishes the range.
func kernelEphemeralPortRange() (low, high int, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, 0, false
	}
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, 0, false
	}
	low, lowErr := strconv.Atoi(fields[0])
	high, highErr := strconv.Atoi(fields[1])
	if lowErr != nil || highErr != nil || low <= 1024 || low >= high || high > 65535 {
		return 0, 0, false
	}
	return low, high, true
}

var smokeLoopbackCursor atomic.Uint32

// listenSmokeLoopback binds the next free harness-owned loopback port and keeps
// the listener open, so its owner holds the port from bind to close.
func listenSmokeLoopback(t *testing.T) net.Listener {
	t.Helper()
	base, span := smokeLoopbackWindow()
	start := int(smokeLoopbackCursor.Add(1)-1) % span
	for offset := 0; offset < span; offset++ {
		port := base + (start+offset)%span
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener
		}
	}
	t.Fatalf("product smoke: no free harness-owned loopback port in %d-%d", base, base+span-1)
	return nil
}

// freeLoopbackAddr reserves and releases a harness-owned loopback port for
// aipstd. The window sits outside the kernel's ephemeral range, so a concurrent
// test process reserving with ":0" can never be handed this port.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener := listenSmokeLoopback(t)
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("product smoke: release loopback port: %v", err)
	}
	return addr
}

// TestSmokeLoopbackAddressesStayOutsideTheEphemeralRange proves the harness
// never competes for a port the kernel hands out with ":0". A concurrent test
// process that reserves one of those ports, releases it and keeps the address
// would otherwise deliver its own traffic to a journey's fake provider and fail
// the journey's "exactly one upstream request" claim.
func TestSmokeLoopbackAddressesStayOutsideTheEphemeralRange(t *testing.T) {
	t.Parallel()
	low, high, ok := kernelEphemeralPortRange()
	if !ok {
		t.Skip("kernel ephemeral port range is not published on this platform")
	}
	seen := make(map[string]bool, 12)
	check := func(kind, addr string) {
		t.Helper()
		if seen[addr] {
			t.Fatalf("%s address %s handed out twice", kind, addr)
		}
		seen[addr] = true
		_, portText, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("%s address %q: %v", kind, addr, err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatalf("%s address %q: %v", kind, addr, err)
		}
		if port >= low && port <= high {
			t.Fatalf("%s address %s is inside the kernel ephemeral range %d-%d", kind, addr, low, high)
		}
	}
	for i := 0; i < 8; i++ {
		check("proxy", freeLoopbackAddr(t))
	}
	for i := 0; i < 4; i++ {
		provider := newOpenAILegacyProvider(t, "smoke-canary", fakeScript{Model: "smoke-model"})
		check("provider", strings.TrimPrefix(provider.BaseURL(), "http://"))
	}
}

// TestSUTEnvironmentIsExplicit proves the child environment carries only the
// allowlisted OS/runtime variables and the harness-owned canary roots: an
// inherited product override never reaches the proxy. It also proves the
// management address reaches the child only when the journey asks for it, and
// that the dedicated management bearer token is never forwarded.
func TestSUTEnvironmentIsExplicit(t *testing.T) {
	t.Setenv("AIP_RELOAD_MANAGEMENT_ADDRESS", "127.0.0.1:9999")
	t.Setenv("AIP_RELOAD_MANAGEMENT_TOKEN", "inherited-management-bearer-value")
	t.Setenv(canaryEnvVar+"_LEAKED", "inherited")
	backends := []backendWiring{{ID: "smoke"}}
	env := sutEnvironment(backends, testCanary)

	allowed := map[string]bool{credentialRoot("smoke"): true}
	for _, name := range sutEnvPassthrough {
		allowed[name] = true
	}
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed child environment entry %q", entry)
		}
		if !allowed[name] {
			t.Fatalf("child environment carries %q, which is neither an OS/runtime variable nor a harness-owned canary root", name)
		}
	}
	if want := credentialRoot("smoke") + "=" + testCanary; !slices.Contains(env, want) {
		t.Fatalf("the canary root %q is missing from the child environment: %v", want, env)
	}
	// An ambient management override and its bearer never leak into a journey
	// that did not ask for the management API.
	for _, name := range []string{managementAddressEnv, "AIP_RELOAD_MANAGEMENT_TOKEN"} {
		for _, entry := range env {
			if got, _, ok := strings.Cut(entry, "="); ok && got == name {
				t.Fatalf("the inherited %s override reached the child environment", name)
			}
		}
	}
	// An explicit opt-in forwards exactly the loopback address, still never the
	// bearer token.
	management := sutEnvironment(backends, testCanary, managementEnvironment("127.0.0.1:19090")...)
	if want := managementAddressEnv + "=127.0.0.1:19090"; !slices.Contains(management, want) {
		t.Fatalf("the opted-in management address %q is missing from the child environment: %v", want, management)
	}
	for _, entry := range management {
		if name, _, ok := strings.Cut(entry, "="); ok && name == "AIP_RELOAD_MANAGEMENT_TOKEN" {
			t.Fatal("the management bearer token reached the child environment")
		}
	}
	if extra := managementEnvironment(""); extra != nil {
		t.Fatalf("a disabled management API rendered environment overrides: %v", extra)
	}
	// The invalid-config start has no backends, so its explicit environment must
	// carry no canary root at all rather than an unused credential value.
	for _, entry := range sutEnvironment(nil, "") {
		if name, _, ok := strings.Cut(entry, "="); ok && strings.HasPrefix(name, canaryEnvVar) {
			t.Fatalf("the backend-free child environment carries the canary root %q", name)
		}
	}
}

// TestReadinessStopsOnCancellation proves the readiness poll derives from the
// owning test context: a cancelled or nearly expired context stops polling
// before it burns the time the journey needs for cleanup.
func TestReadinessStopsOnCancellation(t *testing.T) {
	t.Parallel()

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		h := &sut{addr: "127.0.0.1:1", evidence: &evidence{t: t}, done: make(chan struct{})}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		start := time.Now()
		err := h.waitReadyInference(ctx, sutStartupDeadline, startupCleanupReserve)
		if err == nil {
			t.Fatal("a cancelled context did not stop readiness polling")
		}
		if !strings.Contains(err.Error(), "readiness polling stopped") {
			t.Fatalf("unexpected readiness error: %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("cancelled readiness polling took %v, want an immediate stop", elapsed)
		}
	})

	t.Run("cleanup_reserve", func(t *testing.T) {
		t.Parallel()
		const (
			cap      = time.Second
			reserve  = 100 * time.Millisecond
			ctxBound = 400 * time.Millisecond
		)
		h := &sut{addr: "127.0.0.1:1", evidence: &evidence{t: t}, done: make(chan struct{})}
		ctx, cancel := context.WithTimeout(t.Context(), ctxBound)
		defer cancel()
		start := time.Now()
		err := h.waitReadyInference(ctx, cap, reserve)
		if err == nil {
			t.Fatal("an exhausted context budget did not stop readiness polling")
		}
		if elapsed := time.Since(start); elapsed > ctxBound-reserve+100*time.Millisecond {
			t.Fatalf("readiness polling ignored the cleanup reserve: %v", elapsed)
		}
	})
}

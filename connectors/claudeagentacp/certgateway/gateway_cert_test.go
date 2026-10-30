package certgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
)

// gatewayRow is one recorded request in the stub gateway's log. Header values
// are asserted exactly here: a regression that sends the wrong gateway header
// value, drops the configured header, or injects a first-party credential
// fails this harness instead of passing silently.
type gatewayRow struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Model   string            `json:"model"`
}

// TestCertGateway_NdjsonParser keeps the certification driver's framing logic
// regression-covered even on hosts without the pinned runtime.
func TestCertGateway_NdjsonParser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("node test runner invocation differs")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command("node", "--test", "ndjson.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("ndjson parser tests failed: %v\n%s", err, out)
	}
}

// TestCertGateway_RealRuntime reproduces the recorded real-runtime gateway
// certification against the exact pinned dependency closure when the
// environment provides it:
//
//	AIP_GATEWAY_CERT_RUNTIME root of an installed pinned node_modules tree
//	                         containing node_modules/@agentclientprotocol/claude-agent-acp
//
// It never contacts any third-party network: the stub gateway is
// project-owned and the driver talks stdio to the shipped sidecar. The exact
// wire guarantees asserted here: every model request the sidecar sends to
// the configured gateway carries the configured header with its provisioned
// value and the pinned CLI bypass authorization, no first-party credential
// heads any request (probe included), the streamed turn carries the stub's
// content, and the secret appears in exactly one artifact (the stub's own
// request log) plus nowhere in captured process output.
func TestCertGateway_RealRuntime(t *testing.T) {
	runtimeDir := os.Getenv("AIP_GATEWAY_CERT_RUNTIME")
	if runtimeDir == "" {
		t.Skip("AIP_GATEWAY_CERT_RUNTIME not set; ordinary repositories skip this certification")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the certified gateway profile is Linux/amd64-only")
	}
	profile := verifyPinnedClosure(t, runtimeDir)
	sidecarDir, err := filepath.Abs(filepath.Join("..", "adapter-extension"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		if _, err := os.Stat(filepath.Join(sidecarDir, name)); err != nil {
			t.Fatalf("shipped sidecar incomplete: %v", err)
		}
	}
	// Exercise the production Go admission gate, not just package version
	// strings. Stage the shipped artifact layout so the same sidecar digest
	// check used at connector startup binds this certification run's bytes.
	artifactDir := t.TempDir()
	installedSidecar := filepath.Join(artifactDir, "claudeagentacp-adapter-extension")
	if err := os.CopyFS(installedSidecar, os.DirFS(sidecarDir)); err != nil {
		t.Fatal(err)
	}
	resolver := product.AdapterResolver{Cache: acp.NewExecutableCache(nil)}
	extension, err := resolver.ResolveIdentityExtension(t.Context(), profile,
		filepath.Join(artifactDir, "connector"), profile.NodeExecutable,
		artifactDir, certificationEnv(artifactDir), "honor")
	if err != nil {
		t.Fatalf("production Go sidecar admission: %v", err)
	}
	// Exercise the real short-lived process-tree runner's joined result,
	// not only injected ExitErrors: the certified logged-out CLI exits 1.
	if _, err := (product.IdentityGate{}).Check(t.Context(), profile.CLIExecutable,
		artifactDir, certificationEnv(artifactDir), "gateway", "honor"); err != nil {
		t.Fatalf("production Go gateway identity admission: %v", err)
	}

	workdir := t.TempDir()
	logPath := filepath.Join(workdir, "gateway-requests.log")
	port := freePort(t)
	stubCommand, err := filepath.Abs("stub-gateway.mjs")
	if err != nil {
		t.Fatal(err)
	}
	stub := exec.Command(extension.NodeExecutable, stubCommand)
	stub.Env, stub.Dir = append(os.Environ(),
		fmt.Sprintf("GW_PORT=%d", port),
		"GW_LOG_FILE="+logPath,
	), workdir
	if err := stub.Start(); err != nil {
		t.Fatalf("start stub gateway: %v", err)
	}
	defer func() {
		if stub.Process != nil {
			_ = stub.Process.Kill()
			_ = stub.Wait()
		}
	}()
	t.Logf("stub pid=%d port=%d log=%s", stub.Process.Pid, port, logPath)
	if !waitForPort(t, port) {
		t.Fatal("stub gateway never listened")
	}

	driverErr := runDriver(t, map[string]string{
		"GW_ADAPTER_ROOT": filepath.Join(runtimeDir, "node_modules", "@agentclientprotocol/claude-agent-acp"),
		"GW_SIDECAR":      installedSidecar,
		"GW_LOG_FILE":     logPath,
		"GW_PORT":         fmt.Sprintf("%d", port),
	}, workdir, extension.NodeExecutable)
	// Scan before printing failed-driver diagnostics, which could themselves
	// contain the secret under the regression this harness must catch.
	assertSecretLeakage(t, workdir, logPath, driverErr.output)
	if driverErr.err != nil {
		t.Fatalf("driver failed: %v\nstderr:\n%s", driverErr.err, driverErr.output)
	}

	required := []string{
		"initialize_advertised_gateway",
		"authenticate_accepted_exact_payload",
		"session_created_gateway_session",
		"admission_ticket_gateway_issued",
		"gateway_routed_streamed_turn",
		"streamed_text_delta_with_stub_content",
		"replayed_ticket_refused",
		"post_refusal_observation_refused",
	}
	certRows := filepath.Join(workdir, "cert-state", "cert-rows.txt")
	data, err := os.ReadFile(certRows)
	if err != nil {
		t.Fatalf("cert rows not recorded: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != len(required) {
		t.Fatalf("got %d certification rows, want %d", len(rows), len(required))
	}
	for _, row := range required {
		found := false
		for _, actual := range rows {
			found = found || actual == row
		}
		if !found {
			t.Fatalf("certification row %q missing", row)
		}
	}

	assertGatewayRequests(t, logPath)
}

// verifyPinnedClosure uses the production Go resolver: exact complete
// node_modules and Node executable bytes, plus adapter and CLI version probes.
func verifyPinnedClosure(t *testing.T, runtimeDir string) product.RuntimeProfile {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(runtimeDir, "node_modules", "@agentclientprotocol", "claude-agent-acp", "dist", "index.js")
	resolver := product.AdapterResolver{Cache: acp.NewExecutableCache(nil)}
	profile, err := resolver.Resolve(t.Context(), executable, nil, dir, certificationEnv(dir), "honor")
	if err != nil {
		t.Fatalf("production Go runtime admission: %v", err)
	}
	return profile
}

func certificationEnv(dir string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CLAUDE_CONFIG_DIR=" + dir}
}

// runDriver runs driver-gw.mjs with the wired environment and captures its
// stderr for the leakage scan.
type driverResult struct {
	err    error
	output string
}

func runDriver(t *testing.T, env map[string]string, workdir, nodeExecutable string) driverResult {
	t.Helper()
	driverCommand, err := filepath.Abs("driver-gw.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	driver := exec.CommandContext(ctx, nodeExecutable, driverCommand)
	driver.Dir = workdir
	driver.Env = os.Environ()
	for key, value := range env {
		driver.Env = append(driver.Env, key+"="+value)
	}
	output, err := certificationOutput(driver, workdir)
	return driverResult{err: err, output: string(output)}
}

// assertGatewayRequests parses the stub's request log and asserts the exact
// gateway wire contract of the certification run. Under the pinned closure
// the CLI routes the driver's user turn and additionally issues its own
// internal auxiliary model request (session title generation) to the same
// endpoint; a reachability HEAD probe precedes them. The certified invariant
// is per request, not per count: every model request carries the configured
// header with its provisioned value and the pinned CLI bypass authorization,
// and no first-party credential appears on any recorded request.
func assertGatewayRequests(t *testing.T, logPath string) {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gateway request log: %v", err)
	}
	var posted []*gatewayRow
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row gatewayRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("gateway log line is not parseable: %v (%.120s)", err, line)
		}
		for header := range row.Headers {
			if strings.EqualFold(header, "X-Api-Key") {
				t.Fatalf("first-party credential header X-Api-Key on a %s %q request", row.Method, row.URL)
			}
		}
		parsed, err := url.Parse(row.URL)
		if err != nil {
			t.Fatalf("gateway log URL is not parseable: %v (%q)", err, row.URL)
		}
		if row.Method != "POST" {
			if row.Method != "HEAD" || parsed.Path != "/api/hello" {
				t.Fatalf("unexpected auxiliary gateway request: %s %q", row.Method, row.URL)
			}
			// The certified reachability probe carries neither header, even
			// with an empty value. Model POSTs have a separate exact policy.
			for header := range row.Headers {
				if strings.EqualFold(header, "X-Route") || strings.EqualFold(header, "Authorization") {
					t.Fatalf("credential header %s on gateway preflight", header)
				}
			}
			continue
		}
		if parsed.Path != "/v1/messages" {
			t.Fatalf("unexpected gateway POST endpoint: %q", row.URL)
		}
		posted = append(posted, &row)
	}
	if len(posted) == 0 {
		t.Fatalf("no gateway-routed POST recorded: %.200s", raw)
	}
	// Case-insensitive canonical lookup, exact value assertion, on every
	// routed request including any aux model call and its potential retries.
	for _, post := range posted {
		value := func(name string) (string, bool) {
			for header, v := range post.Headers {
				if strings.EqualFold(header, name) {
					return v, true
				}
			}
			return "", false
		}
		route, ok := value("X-Route")
		if !ok {
			t.Fatalf("configured gateway header X-Route is missing from a routed POST: %.200s", raw)
		}
		if route != "cert-route-value" {
			t.Fatalf("X-Route does not carry the provisioned secret: %q", route)
		}
		bypass, ok := value("Authorization")
		if !ok || bypass != "Bearer acp-proxy" {
			t.Fatalf("pinned CLI bypass authorization missing or changed on a routed POST: %q", bypass)
		}
	}
}

// assertSecretLeakage walks every file of the certification run dir and
// scans the driver's captured stderr: the provisioned header value may
// appear only in the stub's own request log (which the stub itself writes),
// never in driver rows, connector output, argv echoes, diagnostics or
// evidence.
func assertSecretLeakage(t *testing.T, workdir, logPath, driverStderr string) {
	t.Helper()
	secret := "cert-route-value"
	if strings.Contains(driverStderr, secret) {
		t.Fatalf("provisioned gateway secret leaked into driver stderr")
	}
	allowed := map[string]bool{
		filepath.Clean(logPath): true,
	}
	err := filepath.WalkDir(workdir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if allowed[filepath.Clean(path)] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("provisioned gateway secret leaked into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("leakage scan: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatal("loopback listener address")
	}
	port := addr.Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForPort(t *testing.T, port int) bool {
	t.Helper()
	for range 50 {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

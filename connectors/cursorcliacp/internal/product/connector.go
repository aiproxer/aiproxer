package product

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the Cursor CLI ACP backend.
const ID = "cursorcliacp"

// vendorPrefix is the model prefix for Cursor CLI models.
const vendorPrefix = "cursor"

// ansiEscapeRe matches ANSI escape sequences for stripping from CLI output.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// Config configures the Cursor CLI ACP backend. The shared
// acp.ConnectorConfig embeds the local-agent fields common to every ACP CLI
// connector; Cursor-specific fields stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// AutoAccept controls permission responses (true = allow-always, false = reject-once).
	AutoAccept bool
	// TrustWorkspace adds --trust to the command.
	TrustWorkspace bool
	// CursorAPIEndpoint is an optional -e flag value.
	CursorAPIEndpoint string
	// MCPServers is the JSON array for session/new mcpServers.
	MCPServers json.RawMessage
	// Inventory overrides ModelInventory when non-nil (tests / NewWithStarter).
	// Production New wires a live `agent --list-models` provider (or ErrorProvider
	// on resolve failure).
	Inventory modelinventory.Provider
	// ExeCache is instance-owned LookPath cache. When nil, New allocates one.
	ExeCache *acp.ExecutableCache
}

// cursorSpec implements acp.SubprocessConnectorSpec for the Cursor CLI.
type cursorSpec struct {
	cfg         Config
	index       *acp.ModelIndex
	exe         string // resolved once at construction; BuildCommand never re-resolves
	prefixSlash string // vendorPrefix + "/"
}

// VendorID returns the backend identifier.
func (s *cursorSpec) VendorID() string { return ID }

// VendorPrefix returns the model prefix.
func (s *cursorSpec) VendorPrefix() string { return vendorPrefix }

// BuildCommand returns the subprocess command for the Cursor CLI agent.
func (s *cursorSpec) BuildCommand(model string, workspace string) ([]string, string, []string, error) {
	model = strings.TrimSpace(model)
	if s == nil {
		return nil, "", nil, ErrUnknownModel
	}
	if model == "" || !s.index.IsKnownNative(model) {
		return nil, "", nil, ErrUnknownModel
	}
	if s.exe == "" {
		return nil, "", nil, fmt.Errorf("cursorcliacp: executable not resolved")
	}
	cmd := []string{s.exe}
	if s.cfg.CursorAPIEndpoint != "" {
		cmd = append(cmd, "-e", s.cfg.CursorAPIEndpoint)
	}
	cmd = append(cmd, "--model", model)
	if s.cfg.TrustWorkspace {
		cmd = append(cmd, "--trust")
	}
	if len(s.cfg.ExtraArgs) > 0 {
		cmd = append(cmd, s.cfg.ExtraArgs...)
	}
	cmd = append(cmd, "acp")
	return cmd, workspace, nil, nil
}

// HandshakeProfile returns the Cursor-specific handshake configuration.
func (s *cursorSpec) HandshakeProfile() acp.HandshakeProfile {
	mcp := json.RawMessage("[]")
	if len(s.cfg.MCPServers) > 0 {
		mcp = s.cfg.MCPServers
	}
	return acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     false,
		AuthenticateParams:   json.RawMessage(`{"methodId":"cursor_login"}`),
		SessionNewCwd:        s.cfg.DefaultWorkspace,
		SessionNewMCPServers: mcp,
		ClientCapabilities:   json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`),
		ClientInfo:           json.RawMessage(`{"name":"aiproxer","version":"1"}`),
	}
}

// CancelProfile returns the default ACP cancel methods.
func (s *cursorSpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{
		Methods:          []string{"session/cancel", "session/stop", "session/end"},
		IncludeRequestID: true,
		IncludeMessageID: true,
	}
}

// ServerRequestHandler returns the Cursor-specific handler for inbound JSON-RPC.
func (s *cursorSpec) ServerRequestHandler() acp.ServerRequestHandler {
	return &cursorServerRequestHandler{autoAccept: s.cfg.AutoAccept}
}

// RequiresExplicitWorkspace returns true — Cursor CLI requires an explicit workspace.
func (s *cursorSpec) RequiresExplicitWorkspace() bool { return true }

// ResolveModel strips the route-level "cursor:" / "cursor/" prefix, applies
// configured or package defaults for empty/auto, and maps known canonical IDs
// to the exact Cursor CLI NativeID required by `--model`. Unknown identities
// resolve to "" so Open/BuildCommand can reject them explicitly without
// substituting a default.
func (s *cursorSpec) ResolveModel(effectiveModel string) string {
	native, err := s.resolveNativeModel(effectiveModel)
	if err != nil {
		return ""
	}
	return native
}

// cursorServerRequestHandler handles inbound JSON-RPC requests from the Cursor CLI.
type cursorServerRequestHandler struct {
	autoAccept bool
}

func (h *cursorServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	switch method {
	case "session/request_permission":
		optionID := "reject-once"
		if h.autoAccept {
			optionID = "allow-always"
		}
		return map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": optionID,
			},
		}, nil

	case "cursor/ask_question":
		return map[string]any{
			"outcome": map[string]any{
				"outcome": "skipped",
				"reason":  "proxy_auto_skip",
			},
		}, nil

	case "cursor/create_plan":
		return map[string]any{
			"outcome": map[string]any{
				"outcome": "rejected",
				"reason":  "proxy_auto_reject",
			},
		}, nil

	default:
		if strings.HasPrefix(method, "cursor/") {
			// Unhandled Cursor extension — reply with empty result (headless proxy).
			return map[string]any{}, nil
		}
		// Unknown method — return JSON-RPC method-not-found error.
		return nil, fmt.Errorf("method not handled: %s", method)
	}
}

// resolveExecutable finds the Cursor CLI agent binary using an instance-owned cache.
func resolveExecutable(cache *acp.ExecutableCache, configured string) (string, error) {
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := cache.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("CURSOR_AGENT_BIN")); env != "" {
		if resolved, ok := cache.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	if resolved, err := cache.LookPath("agent"); err == nil {
		return resolved, nil
	}
	if isWindows() {
		if resolved, err := cache.LookPath("agent.cmd"); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("cursor CLI (agent) executable not found; install Cursor CLI and ensure `agent` is on PATH or set CURSOR_AGENT_BIN")
}

// parseAgentModelsListing parses `agent --list-models` human-readable output into model IDs.
func parseAgentModelsListing(stdout string) []string {
	var models []string
	seen := make(map[string]struct{})
	for rawLine := range strings.SplitSeq(stdout, "\n") {
		line := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(rawLine, ""))
		if line == "" || strings.HasPrefix(strings.ToLower(line), "loading") {
			continue
		}
		before, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		modelID := strings.TrimSpace(before)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		if strings.Contains(modelID, " ") {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	return models
}

// isWindows returns true on Windows.
func isWindows() bool {
	return runtime.GOOS == "windows"
}

// New returns a runtime backend that invokes the Cursor CLI via ACP stdio.
func New(cfg Config) (*Engine, error) {
	if cfg.Model == "" {
		cfg.Model = defaultConfiguredModel
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	spec := &cursorSpec{cfg: cfg, prefixSlash: vendorPrefix + "/"}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	exe, exeErr := resolveExecutable(cache, cfg.Executable)
	spec.exe = exe
	inv := cfg.Inventory
	if inv == nil {
		if exeErr != nil {
			inv = modelinventory.ErrorProvider{Err: exeErr}
		} else {
			inv = newModelsProvider(exe, cfg.CursorAPIEndpoint)
		}
	}
	return newCursorBackend(spec, workspace, cfg, nil, exeErr, inv, cache, exe), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the Cursor CLI agent without spawning a real binary. It skips
// executable resolution and live `agent --list-models` discovery unless
// Config.Inventory is set.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) *Engine {
	if cfg.Model == "" {
		cfg.Model = defaultConfiguredModel
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	exe := strings.TrimSpace(cfg.Executable)
	if exe == "" {
		exe = "agent"
	}
	spec := &cursorSpec{cfg: cfg, exe: exe, prefixSlash: vendorPrefix + "/"}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	inv := cfg.Inventory
	if inv == nil {
		inv = modelinventory.ErrorProvider{}
	}
	return newCursorBackend(spec, workspace, cfg, starter, nil, inv, cache, exe)
}

// newCursorBackend assembles the execbackend.Backend from a normalized spec,
// workspace, and config. starter, if non-nil, overrides the default OS process
// starter (used by tests). exeErr is surfaced from the Open closure so callers
// see startup errors early. inventory is wrapped in a tracking provider whose
// ModelIndex is updated by AcceptInventory after registry publish (not by
// LoadModels).
func newCursorBackend(spec *cursorSpec, workspace acp.WorkspacePolicy, cfg Config, starter acp.ProcessStarter, exeErr error, inventory modelinventory.Provider, cache *acp.ExecutableCache, exePath string) *Engine {
	index := acp.NewModelIndex(cursorCanonicalFallback)
	spec.index = index
	tracking := acp.NewTrackingInventory(inventory, index, ID)
	backendCfg := acp.SubprocessBackendConfig{
		Protocol:  acp.NewACPProtocol(spec, nil),
		Workspace: workspace,
		Pool: acp.RuntimePoolConfig{
			IdleTimeout:    cfg.IdleTimeout,
			StaleKillDelay: cfg.StaleKillDelay,
		},
	}
	if starter != nil {
		backendCfg.ProcessStarter = starter
	}
	backend := acp.NewSubprocessBackend(backendCfg)
	return &Engine{
		Inventory: tracking,
		Caps:      defaultBackendCaps(),
		exeCache:  cache,
		exePath:   exePath,
		closeFn:   backend.Close,
		open: func(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
			if exeErr != nil {
				return nil, exeErr
			}
			call = withDefaultWorkspace(call, cfg.DefaultWorkspace)
			if _, err := spec.resolveNativeModel(acp.CallRouteModel(call, "acp.model")); err != nil {
				return nil, err
			}
			return backend.Open(ctx, call)
		},
	}
}

func withDefaultWorkspace(call *aipapi.Call, workspace string) *aipapi.Call {
	workspace = strings.TrimSpace(workspace)
	if call == nil || workspace == "" {
		return call
	}
	cp := *call
	if cp.Extensions == nil {
		cp.Extensions = map[string]json.RawMessage{}
	} else {
		ext := make(map[string]json.RawMessage, len(cp.Extensions)+1)
		maps.Copy(ext, cp.Extensions)
		cp.Extensions = ext
	}
	if _, ok := cp.Extensions["acp.cwd"]; ok {
		return &cp
	}
	raw, _ := json.Marshal(workspace)
	cp.Extensions["acp.cwd"] = raw
	return &cp
}

func defaultBackendCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityVision,
		aipapi.CapabilityReasoning,
	)
}

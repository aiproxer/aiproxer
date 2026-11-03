package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

const (
	pinnedSDKVersion = "0.3.316"
	minNodeVersion   = "18.14.0"
	minCLIVersion    = "2.1.248"
	modelsTTL        = 5 * time.Minute
	maxSystemBytes   = 256 << 10
	maxPromptBytes   = 4 << 20
)

// sidecarLocation is the resolved connector-owned private bundle.
type sidecarLocation struct {
	NodePath   string
	Script     string
	SDKVersion string
}

type runtimeIdentity struct {
	NodeVersion string
	CLIVersion  string
	SDKVersion  string
}

type runtimeProbe func(context.Context, Config, sidecarLocation) (runtimeIdentity, error)

type engineDeps struct {
	host    []string
	probe   runtimeProbe
	sidecar sidecarLocation
	now     func() time.Time
}

func productionDeps() engineDeps {
	return engineDeps{host: os.Environ(), probe: probeRuntime, now: time.Now}
}

type engine struct {
	cfg       Config
	workspace string
	env       []string
	secrets   []string
	location  sidecarLocation
	identity  runtimeIdentity
	now       func() time.Time
	sessions  *bridge.Manager

	mu         sync.Mutex
	discovery  bridge.Discovery
	discovered time.Time

	// admit bounds live sidecars for this configured instance. Every discovery
	// probe and every session holds one slot until its process group is
	// retired. The limit is read from cfg on each admission so the operator
	// bound and the executed bound cannot drift.
	admitMu  sync.Mutex
	live     int
	admitted chan struct{}
}

func newEngine(ctx context.Context, cfg Config, secrets map[string]string, deps engineDeps) (*engine, error) {
	if cfg.AdminPolicy != "honor" {
		return nil, errors.New("claudeagentmodel: potential organisation/server-delivered policy; select admin_policy: honor to honor its authority and enable this local connector")
	}
	if cfg.MaxRuntimes < 1 || cfg.MaxRuntimes > MaxRuntimesLimit {
		return nil, errors.New("claudeagentmodel: max_runtimes must be between 1 and 64")
	}
	apiKey := ""
	if cfg.CredentialSource == "api_key" {
		apiKey = strings.TrimSpace(secrets["api_key"])
		if apiKey == "" {
			return nil, errors.New("claudeagentmodel: api_key secret required for credential_source api_key")
		}
		if strings.ContainsAny(apiKey, "\x00\r\n") {
			return nil, errors.New("claudeagentmodel: invalid credential encoding")
		}
	}
	workspace := strings.TrimSpace(cfg.DefaultWorkspace)
	if workspace == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, errors.New("claudeagentmodel: default_workspace is required when the host home is unavailable")
		}
		workspace = home
	}
	if !filepath.IsAbs(workspace) {
		return nil, errors.New("claudeagentmodel: default_workspace must be absolute")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil, errors.New("claudeagentmodel: default_workspace must be an existing directory")
	}
	env, err := buildEnv(deps.host, apiKey, cfg.CredentialSource)
	if err != nil {
		return nil, err
	}
	location := deps.sidecar
	if location.Script == "" {
		location, err = resolveSidecar()
		if err != nil {
			return nil, err
		}
	}
	probe := deps.probe
	if probe == nil {
		probe = probeRuntime
	}
	identity, err := probe(ctx, cfg, location)
	if err != nil {
		return nil, err
	}
	now := deps.now
	if now == nil {
		now = time.Now
	}
	turnWait, pendingTTL, idleTimeout := cfg.durations()
	eng := &engine{
		cfg:       cfg,
		workspace: workspace,
		env:       env,
		secrets:   secretList(apiKey),
		location:  location,
		identity:  identity,
		now:       now,
		admitted:  make(chan struct{}),
	}
	eng.sessions = bridge.NewManager(bridge.AdmissionFunc(func(ctx context.Context, wait time.Duration) (bridge.Release, error) {
		slot, err := eng.acquire(ctx, wait)
		if err != nil {
			return nil, err
		}
		return slot, nil
	}), pendingTTL, idleTimeout, turnWait, now)
	return eng, nil
}

// lease is one runtime admission slot. Release is idempotent so every terminal,
// error and cancellation path can call it without double-counting.
type lease struct {
	once    sync.Once
	release func()
}

func (l *lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(l.release)
}

// acquire admits one live sidecar. It waits at most the configured turn wait,
// then reports a typed capacity failure rather than spawning beyond the bound.
func (e *engine) acquire(ctx context.Context, wait time.Duration) (*lease, error) {
	if wait <= 0 {
		wait = e.turnWait()
	}
	deadline := time.Now().Add(wait)
	for {
		e.admitMu.Lock()
		limit := e.cfg.MaxRuntimes
		limit = max(limit, 1)
		if e.live < limit {
			e.live++
			e.admitMu.Unlock()
			return &lease{release: e.releaseRuntime}, nil
		}
		waitCh := e.admitted
		e.admitMu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, e.capacityError(limit)
		}
		timer := time.NewTimer(remaining)
		select {
		case <-waitCh:
			timer.Stop()
		case <-timer.C:
			return nil, e.capacityError(limit)
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

func (e *engine) capacityError(limit int) error {
	return &classifiedError{
		code:    backendplugin.ErrorCodeResourceExhausted,
		message: fmt.Sprintf("claudeagentmodel: all %d live runtime slots are in use", limit),
	}
}

func (e *engine) releaseRuntime() {
	e.admitMu.Lock()
	if e.live > 0 {
		e.live--
	}
	close(e.admitted)
	e.admitted = make(chan struct{})
	e.admitMu.Unlock()
}

func (e *engine) turnWait() time.Duration {
	if e.cfg.TurnWaitSeconds <= 0 {
		return time.Duration(DefaultTurnWaitSeconds) * time.Second
	}
	return time.Duration(e.cfg.TurnWaitSeconds * float64(time.Second))
}

func secretList(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// buildEnv constructs the entire child environment. The SDK replaces, not
// merges, the subprocess environment, so no unintended host variable (including
// ANTHROPIC_BASE_URL or NODE_OPTIONS) can reach the sidecar or its CLI child.
func buildEnv(host []string, apiKey, source string) ([]string, error) {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true, "APPDATA": true,
		"LOCALAPPDATA": true, "SYSTEMROOT": true, "TEMP": true, "TMP": true,
		"TMPDIR": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"TERM": true, "TZ": true, "CLAUDE_CONFIG_DIR": true,
	}
	out := make([]string, 0, len(allowed)+8)
	seen := map[string]bool{}
	hasHome := false
	for _, entry := range host {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !allowed[name] || seen[name] || strings.ContainsRune(value, 0) {
			continue
		}
		if name == "HOME" && strings.TrimSpace(value) != "" {
			hasHome = true
		}
		out = append(out, name+"="+value)
		seen[name] = true
	}
	if !seen["PATH"] {
		return nil, errors.New("claudeagentmodel: host PATH is required to launch the sidecar")
	}
	if source == "host_login" && !hasHome {
		return nil, errors.New("claudeagentmodel: host_login requires HOME or USERPROFILE")
	}
	if apiKey != "" {
		out = append(out, "ANTHROPIC_API_KEY="+apiKey)
	}
	out = append(out,
		"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
		"DISABLE_AUTO_COMPACT=1",
		"ENABLE_TOOL_SEARCH=false",
		"CLAUDE_AGENT_SDK_CLIENT_APP=aiproxer/claudeagentmodel",
	)
	return out, nil
}

// resolveSidecar locates the bundled Node bridge next to the installed plugin
// executable. The script path, Node flags and SDK are never operator-configurable.
func resolveSidecar() (sidecarLocation, error) {
	exe, err := os.Executable()
	if err != nil {
		return sidecarLocation{}, errors.New("claudeagentmodel: cannot locate the connector executable")
	}
	return resolveSidecarBeside(exe)
}

// resolveSidecarBeside applies the installed-bundle layout to one connector
// executable path. Certification drives this same production resolver with the
// packaged binary instead of the test process's own executable.
func resolveSidecarBeside(exe string) (sidecarLocation, error) {
	if strings.TrimSpace(exe) == "" {
		return sidecarLocation{}, errors.New("claudeagentmodel: cannot locate the connector executable")
	}
	base := filepath.Join(filepath.Dir(exe), "..", "sidecar")
	script := filepath.Join(base, "bridge.mjs")
	// Every runtime companion is mandatory: a partially staged bundle must fail
	// before a process starts rather than at the first deferred tool call.
	// package.json is read by session.mjs at module load; the lock file is a
	// packaging input, not a runtime dependency.
	for _, name := range []string{"bridge.mjs", "session.mjs", "tool-server.mjs", "package.json"} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil || !info.Mode().IsRegular() {
			return sidecarLocation{}, errors.New("claudeagentmodel: sidecar bundle is incomplete; reinstall the certified connector")
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return sidecarLocation{}, errors.New("claudeagentmodel: Node 18.14 or newer is required but node was not found")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		return sidecarLocation{}, errors.New("claudeagentmodel: node path is unavailable")
	}
	return sidecarLocation{NodePath: node, Script: script, SDKVersion: pinnedSDKVersion}, nil
}

func probeRuntime(ctx context.Context, cfg Config, loc sidecarLocation) (runtimeIdentity, error) {
	info, err := os.Stat(cfg.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return runtimeIdentity{}, errors.New("claudeagentmodel: executable must be an executable Claude Code CLI file")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodeOut, err := runVersion(ctx, loc.NodePath)
	if err != nil {
		return runtimeIdentity{}, errors.New("claudeagentmodel: Node identity is unavailable")
	}
	cliOut, err := runVersion(ctx, cfg.Executable)
	if err != nil {
		return runtimeIdentity{}, errors.New("claudeagentmodel: Claude Code CLI identity is unavailable; verify the executable and launch environment")
	}
	nodeVersion, ok := parseVersion(nodeOut)
	if !ok || compareVersion(nodeVersion, mustVersion(minNodeVersion)) < 0 {
		return runtimeIdentity{}, errors.New("claudeagentmodel: Node 18.14 or newer is required")
	}
	cliVersion, ok := parseVersion(cliOut)
	if !ok || compareVersion(cliVersion, mustVersion(minCLIVersion)) < 0 {
		return runtimeIdentity{}, errors.New("claudeagentmodel: Claude Code 2.1.248 or newer is required")
	}
	if loc.SDKVersion != pinnedSDKVersion {
		return runtimeIdentity{}, errors.New("claudeagentmodel: sidecar SDK identity mismatch")
	}
	return runtimeIdentity{NodeVersion: formatVersion(nodeVersion), CLIVersion: formatVersion(cliVersion), SDKVersion: loc.SDKVersion}, nil
}

func runVersion(ctx context.Context, executable string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if out.Len() == 0 || out.Len() > 4<<10 {
		return "", errors.New("empty or oversized version output")
	}
	return strings.TrimSpace(out.String()), nil
}

type version [3]int

func mustVersion(raw string) version {
	v, _ := parseVersion(raw)
	return v
}

func parseVersion(raw string) (version, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "v"))
	var v version
	n, err := fmt.Sscanf(raw, "%d.%d.%d", &v[0], &v[1], &v[2])
	if err != nil || n < 2 {
		return version{}, false
	}
	return v, true
}

func formatVersion(v version) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func compareVersion(a, b version) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func (e *engine) discoverySnapshot(ctx context.Context) (bridge.Discovery, error) {
	e.mu.Lock()
	if !e.discovered.IsZero() && e.now().Sub(e.discovered) < modelsTTL && len(e.discovery.Models) > 0 {
		out := e.discovery
		e.mu.Unlock()
		return out, nil
	}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	admission, err := e.acquire(ctx, e.turnWait())
	if err != nil {
		return bridge.Discovery{}, err
	}
	defer admission.Release()
	found, err := bridge.Discover(ctx, e.bridgeLocation(), e.bridgeRuntime())
	if err != nil {
		return bridge.Discovery{}, err
	}
	if found.SDKVersion != e.identity.SDKVersion || found.CLIVersion != e.identity.CLIVersion {
		return bridge.Discovery{}, errors.New("claudeagentmodel: runtime identity changed after configuration")
	}
	if err := validateModels(found.Models); err != nil {
		return bridge.Discovery{}, err
	}
	e.mu.Lock()
	e.discovery = found
	e.discovered = e.now()
	e.mu.Unlock()
	return found, nil
}

func validateModels(models []bridge.Model) error {
	if len(models) == 0 || len(models) > 256 {
		return errors.New("claudeagentmodel: runtime model inventory is empty or oversized")
	}
	seen := map[string]bool{}
	for _, model := range models {
		value := strings.TrimSpace(model.Value)
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("claudeagentmodel: runtime model inventory contains an invalid model")
		}
		if seen[value] {
			return errors.New("claudeagentmodel: runtime model inventory contains duplicates")
		}
		seen[value] = true
	}
	return nil
}

func (e *engine) bridgeLocation() bridge.Location {
	return bridge.Location{Node: e.location.NodePath, Script: e.location.Script, PinnedSDKVersion: e.location.SDKVersion}
}

func (e *engine) bridgeRuntime() bridge.Runtime {
	return bridge.Runtime{CLI: e.cfg.Executable, Workspace: e.workspace, Env: e.env, Secrets: e.secrets}
}

func (e *engine) listModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	found, err := e.discoverySnapshot(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	response := backendplugin.ListModelsResponse{
		InventorySource: "claude-agent-sdk",
		FetchedUnixMS:   e.fetchedAt().UnixMilli(),
	}
	refresh := e.fetchedAt().Add(modelsTTL).UnixMilli()
	response.RefreshAfterUnixMS = &refresh
	for _, model := range found.Models {
		if max > 0 && uint32(len(response.Models)) >= max {
			break
		}
		response.Models = append(response.Models, backendplugin.ModelDescriptor{
			CanonicalModelID: ModelNamespace + model.Value,
			NativeModelID:    model.Value,
			DisplayName:      model.DisplayName,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true, Tools: true},
		})
	}
	return response, nil
}

func (e *engine) fetchedAt() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discovered.IsZero() {
		return e.now()
	}
	return e.discovered
}

func (e *engine) profile(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	profile := backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, Tools: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           "claude-agent-sdk",
		ProfileVersion:           "claude-agent-sdk/" + e.identity.SDKVersion,
	}
	if modelID == nil || strings.TrimSpace(*modelID) == "" || strings.TrimSpace(*modelID) == FactoryKind {
		return profile, nil
	}
	native, row, err := e.resolveModel(ctx, *modelID)
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	profile.ProfileVersion = "claude-agent-sdk/" + e.identity.SDKVersion + "/" + native
	profile.EvidenceSource = "claude-agent-sdk model=" + native
	if len(row.SupportedEffortLevels) > 0 {
		declaration := &backendplugin.ReasoningEffortValuesV1{
			ModelID:    strings.TrimSpace(*modelID),
			Supported:  append([]string(nil), row.SupportedEffortLevels...),
			Exhaustive: false,
		}
		if err := declaration.Validate(); err == nil {
			profile.ReasoningEffortValues = declaration
		}
	}
	return profile, nil
}

func (e *engine) resolveModel(ctx context.Context, requested string) (string, bridge.Model, error) {
	found, err := e.discoverySnapshot(ctx)
	if err != nil {
		return "", bridge.Model{}, err
	}
	native := strings.TrimSpace(requested)
	native = strings.TrimPrefix(native, ModelNamespace)
	if native == "" || native == FactoryKind {
		return "", bridge.Model{}, nil
	}
	for i := range found.Models {
		if found.Models[i].Value != native {
			continue
		}
		if native == "default" {
			return "", found.Models[i], nil
		}
		return found.Models[i].Value, found.Models[i], nil
	}
	// A canonical resolved id is unambiguous even when several aliases resolve
	// to it. Pass the exact id through; the SDK accepts full model ids.
	for i := range found.Models {
		if found.Models[i].ResolvedModel == native {
			return native, found.Models[i], nil
		}
	}
	return "", bridge.Model{}, fmt.Errorf("claudeagentmodel: model %q is not in the current runtime inventory", native)
}

// Open starts one canonical step. A request without a matching pending tool
// result opens a fresh native turn; a request that returns the result of a
// committed native tool call releases exactly that parked invocation.
func (e *engine) Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
	if call == nil {
		return nil, errors.New("claudeagentmodel: call is required")
	}
	if call.Decision != nil {
		return nil, capabilityReject("decision requests are not supported by this model connector")
	}
	catalog, warnings, err := bridge.BuildCatalog(call.Tools, call.ToolChoice, e.cfg.StrictSemantics)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	model := ""
	var modelRow bridge.Model
	if selector := strings.TrimSpace(call.Route.Selector); selector != "" {
		model, modelRow, err = e.resolveModel(ctx, selector)
		if err != nil {
			return nil, e.classifyOpenError(err)
		}
	}
	effort, controlWarnings := generationControls(call.Options, modelRow)
	warnings = append(warnings, controlWarnings...)
	if strings.TrimSpace(call.PromptCacheKey) != "" {
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "prompt_cache_key_unsupported",
			WarningMessage: "the SDK controls its own prompt cache; the client cache hint cannot be forwarded",
		})
	}
	if e.cfg.StrictSemantics && len(warnings) > 0 {
		return nil, capabilityReject("requested semantics cannot be represented faithfully in strict mode")
	}
	if e.cfg.StrictSemantics && effort != "" {
		// Strict mode opted into an explicit audit of every generation control:
		// a forwarded control is not a degradation, but the SDK/model may still
		// apply a different effective level, so it is visible rather than silent.
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "reasoning_effort_forwarded",
			WarningMessage: fmt.Sprintf("strict_semantics: reasoning effort %q was forwarded to the SDK; the selected model may apply a different level", effort),
		})
	}
	requestID := strings.TrimSpace(call.ID)
	if requestID == "" {
		requestID = fmt.Sprintf("turn-%d", e.now().UnixNano())
	}
	if previousID := strings.TrimSpace(call.PreviousResponseID); previousID != "" {
		if !aipcont.Materialized(*call) {
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeHistoryUnavailable,
				Detail: "previous_response_id was not materialized into a complete item trajectory",
			})
		}
	}
	// Only the final result group can be the new continuation. Historical
	// completed results are replayed history, so a sequential tool loop with two
	// or more results in the transcript is supported.
	trailing, hasTrailing := bridge.TrailingResults(*call)
	switch {
	case len(trailing) > 1:
		return nil, classifyEngineError(&bridge.ContinuationError{
			Code:   bridge.CodeUnsupportedParallelToolCalls,
			Detail: "one request may return at most one new tool result in this connector slice",
		})
	case len(trailing) == 1 && hasTrailing:
		nameOnly := bridge.IsNameOnlyResult(trailing[0])
		shape := bridge.ClassifyContinuation(*call, trailing[0].CallID)
		if nameOnly {
			_, shape, err = bridge.NameContinuation(*call, trailing[0].Name)
			if err != nil {
				return nil, classifyEngineError(err)
			}
		}
		switch shape {
		case bridge.ContinuationReady, bridge.ContinuationNoCall, bridge.ContinuationCallThenText:
			if strings.TrimSpace(call.PreviousResponseID) != "" && shape == bridge.ContinuationNoCall {
				return nil, classifyEngineError(&bridge.ContinuationError{
					Code:   bridge.CodeHistoryUnavailable,
					Detail: "previous_response_id does not carry the committed assistant tool call",
				})
			}
			stream, err := e.continueTurn(ctx, call, model, effort, catalog, warnings, requestID, trailing[0])
			if err == nil {
				return stream, nil
			}
			if !errors.Is(err, bridge.ErrNoPending) {
				return nil, err
			}
			// No live parked conversation exists, so the request is a new turn
			// whose history contains an already completed tool loop. It is served
			// as an explicitly labeled history replay, never as a native
			// continuation.
		case bridge.ContinuationDuplicate:
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeContinuationMismatch,
				Detail: "the request history contains more than one occurrence of the committed tool call or its result",
			})
		case bridge.ContinuationHistoryUnavailable:
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeHistoryUnavailable,
				Detail: "the request history cannot prove the visible prefix",
			})
		case bridge.ContinuationAmbiguous:
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeAmbiguousContinuation,
				Detail: "more than one visible tool call could be the committed one",
			})
		case bridge.ContinuationRoleMismatch:
			if e.hasLivePending(trailing[0], call) {
				return nil, classifyEngineError(&bridge.ContinuationError{
					Code:   bridge.CodeContinuationMismatch,
					Detail: "the committed tool call is not an assistant call in the request history",
				})
			}
		case bridge.ContinuationMixed:
			if call.HasItemAuthority() {
				// An ordered-item trajectory is explicit. Replaying a reordered or
				// padded item history would present a conversation that did not
				// happen, so the connector refuses instead of reordering it.
				return nil, classifyEngineError(&bridge.ContinuationError{
					Code:   bridge.CodeContinuationMismatch,
					Detail: "the ordered item suffix contains content between the committed tool call and its result",
				})
			}
			// The trailing result is accompanied by content (before it, in the same
			// message, or after it). Replaying the visible history in source order
			// preserves every byte; releasing would drop it. The degradation is
			// always named, and strict semantics refuses when a native turn is
			// actually parked.
			warnings = append(warnings, aipapi.Event{
				Kind:           aipapi.EventWarning,
				WarningCode:    "continuation_replayed",
				WarningMessage: "the request carried content alongside the tool result, so the connector replayed the client-visible history in source order instead of resuming the parked native turn",
			})
			if e.cfg.StrictSemantics && e.hasLivePending(trailing[0], call) {
				return nil, capabilityReject("a tool result with additional content cannot resume the parked native turn faithfully in strict mode")
			}
		}
	}
	return e.startTurn(ctx, call, model, effort, catalog, warnings, requestID)
}

func (e *engine) startTurn(ctx context.Context, call *aipapi.Call, model, effort string, catalog *bridge.Catalog, warnings []aipapi.Event, requestID string) (aipapi.ManagedEventStream, error) {
	system, prompt, promptWarnings, err := buildTextPrompt(call)
	if err != nil {
		return nil, err
	}
	allWarnings := append(append([]aipapi.Event(nil), warnings...), promptWarnings...)
	if e.cfg.StrictSemantics && hasBlockingWarning(allWarnings) {
		return nil, capabilityReject("requested semantics cannot be represented faithfully in strict mode")
	}
	prefix, err := bridge.VisiblePrefixDigest(*call)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	_, pendingTTL, _ := e.cfg.durations()
	turn, err := e.sessions.Start(ctx, e.bridgeLocation(), e.bridgeRuntime(), bridge.TurnRequest{
		RequestID:     requestID,
		Model:         model,
		System:        system,
		Prompt:        prompt,
		Catalog:       catalog,
		SessionKey:    strings.TrimSpace(call.Session.AuthoritativeSessionID),
		Prefix:        prefix,
		ItemAuthority: call.HasItemAuthority(),
		Effort:        effort,
		PendingTTL:    pendingTTL,
	})
	if err != nil {
		return nil, e.classifyOpenError(err)
	}
	return wrapStream(turn, allWarnings), nil
}

func (e *engine) continueTurn(ctx context.Context, call *aipapi.Call, model, effort string, catalog *bridge.Catalog, warnings []aipapi.Event, requestID string, result bridge.ToolResult) (aipapi.ManagedEventStream, error) {
	sessionKey := strings.TrimSpace(call.Session.AuthoritativeSessionID)
	nameOnly := bridge.IsNameOnlyResult(result)
	var (
		session *bridge.Session
		pending bridge.PendingCall
		proof   bridge.ContinuationProof
		err     error
	)
	if nameOnly {
		nameProof, shape, nameErr := bridge.NameContinuation(*call, result.Name)
		if nameErr != nil {
			return nil, classifyEngineError(nameErr)
		}
		switch shape {
		case bridge.ContinuationReady, bridge.ContinuationNoCall:
		case bridge.ContinuationAmbiguous:
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeAmbiguousContinuation,
				Detail: "more than one visible name-only tool call could be the committed one",
			})
		default:
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeContinuationMismatch,
				Detail: "the request history does not select one committed name-only tool call",
			})
		}
		session, err = e.sessions.LookupName(nameProof, sessionKey)
		if errors.Is(err, bridge.ErrNoPending) {
			return nil, bridge.ErrNoPending
		}
		if err != nil {
			return nil, classifyEngineError(err)
		}
		snapshot, ok := session.PendingSnapshot()
		if !ok {
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeContinuationMismatch,
				Detail: "the conversation has no pending tool call",
			})
		}
		pending = snapshot
		if err := bridge.MatchNamePending(&pending, nameProof); err != nil {
			return nil, classifyEngineError(err)
		}
		proof = bridge.ContinuationProof{Prefix: nameProof.Prefix, ArgsDigest: nameProof.ArgsDigest}
	} else {
		session, err = e.sessions.Lookup(result.CallID, sessionKey)
		if errors.Is(err, bridge.ErrNoPending) {
			return nil, bridge.ErrNoPending
		}
		if err != nil {
			return nil, classifyEngineError(err)
		}
		snapshot, ok := session.PendingSnapshot()
		if !ok {
			return nil, classifyEngineError(&bridge.ContinuationError{
				Code:   bridge.CodeContinuationMismatch,
				Detail: "the conversation has no pending tool call",
			})
		}
		pending = snapshot
		proof, err = bridge.MatchPendingContinuation(*call, &pending, result)
		if err != nil {
			return nil, classifyEngineError(err)
		}
	}
	// An explicit prohibition outranks interoperability: the parked native
	// session keeps its MCP tool catalogue attached, and the sidecar cannot
	// unregister tools mid-query. Refuse before Resume so the pending call stays
	// releasable by an honest continuation.
	if call.ToolChoice.Mode == aipapi.ToolChoiceNone && pending.ToolCount > 0 {
		return nil, capabilityReject("tool_choice none cannot be enforced on an already-parked native tool session; the continuation was refused without consuming the pending call")
	}
	retainCatalog := catalog.Len() == 0 && pending.ToolCount > 0 && catalog.Digest() != pending.CatalogDigest
	if retainCatalog {
		if e.cfg.StrictSemantics {
			return nil, capabilityReject("a continuation that omits the parked tool catalogue cannot be served faithfully in strict mode")
		}
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "tool_snapshot_retained",
			WarningMessage: "the continuation request omitted its tool definitions; the parked tool snapshot was retained so the native turn can finish",
		})
	}
	transcript, err := bridge.VisiblePrefixDigest(*call)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	turn, err := session.Resume(ctx, bridge.ResumeSpec{
		CallID:           pending.CallID,
		NameOnly:         nameOnly,
		Model:            model,
		CatalogDigest:    catalog.Digest(),
		RetainCatalog:    retainCatalog,
		Prefix:           proof.Prefix,
		ArgsDigest:       proof.ArgsDigest,
		Effort:           effort,
		Result:           result,
		ItemAuthority:    call.HasItemAuthority(),
		TranscriptPrefix: transcript,
	}, requestID)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	return wrapStream(turn, warnings), nil
}

// hasLivePending reports whether the request names a live parked conversation,
// which decides whether a mixed or role-mismatched suffix is a replay or a typed
// refusal.
func (e *engine) hasLivePending(result bridge.ToolResult, call *aipapi.Call) bool {
	if bridge.IsNameOnlyResult(result) {
		return e.sessions.HasLiveName(result.Name)
	}
	_, err := e.sessions.Lookup(result.CallID, strings.TrimSpace(call.Session.AuthoritativeSessionID))
	return err == nil
}

func wrapStream(turn *bridge.Turn, warnings []aipapi.Event) aipapi.ManagedEventStream {
	var stream aipapi.ManagedEventStream = turn
	stream = &classifiedStream{inner: stream}
	if len(warnings) > 0 {
		stream = &prefixedStream{events: warnings, inner: stream}
	}
	return stream
}

// hasBlockingWarning reports whether strict semantics must reject. A forwarded
// control that the SDK honors is disclosed, not degraded.
func hasBlockingWarning(events []aipapi.Event) bool {
	for _, event := range events {
		if event.Kind != aipapi.EventWarning {
			continue
		}
		if event.WarningCode == "reasoning_effort_forwarded" {
			continue
		}
		return true
	}
	return false
}

// classifyOpenError preserves caller cancellation and already-classified
// capacity failures instead of relabeling them as generic unavailability.
func (e *engine) classifyOpenError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, ok := errors.AsType[*classifiedError](err); ok {
		return err
	}
	return classifyEngineError(err)
}

// generationControls returns the SDK effort to forward plus explicit warnings
// for admitted controls this text slice cannot enforce. Effort is forwarded only
// when the discovered model advertises that exact level, so the profile facts
// and execution stay consistent.
func generationControls(options aipapi.GenerationOptions, row bridge.Model) (string, []aipapi.Event) {
	var warnings []aipapi.Event
	effort := ""
	requestedEffort := strings.TrimSpace(options.ReasoningEffort)
	if requestedEffort != "" {
		for _, level := range row.SupportedEffortLevels {
			if strings.EqualFold(strings.TrimSpace(level), requestedEffort) {
				effort = strings.ToLower(strings.TrimSpace(level))
				break
			}
		}
		if effort == "" {
			warnings = append(warnings, aipapi.Event{
				Kind:           aipapi.EventWarning,
				WarningCode:    "reasoning_effort_degraded",
				WarningMessage: fmt.Sprintf("reasoning effort %q is not supported by the selected model; the SDK ran at its default effort", safeOptionLabel(requestedEffort)),
			})
		}
	}
	var unsupported []string
	if options.MaxOutputTokens != nil {
		unsupported = append(unsupported, "max_output_tokens")
	}
	if options.Temperature != nil {
		unsupported = append(unsupported, "temperature")
	}
	if mime := strings.TrimSpace(options.ResponseMIMEType); mime != "" && !strings.EqualFold(mime, "text/plain") {
		unsupported = append(unsupported, "response_mime_type")
	}
	if len(unsupported) > 0 {
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "generation_controls_degraded",
			WarningMessage: "these admitted generation controls cannot be enforced by the SDK text path and were not applied: " + strings.Join(unsupported, ", "),
		})
	}
	return effort, warnings
}

// safeOptionLabel bounds and strips control characters from one client-supplied
// option value before it appears in a visible warning.
func safeOptionLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func capabilityReject(message string) error {
	return fmt.Errorf("%w: %s", aipapi.ErrCapabilityReject, message)
}

type classifiedError struct {
	code    backendplugin.ErrorCode
	message string
}

func (e *classifiedError) Error() string { return e.message }

func (e *classifiedError) TerminalPluginError() *backendplugin.PluginError {
	message := strings.ReplaceAll(strings.TrimSpace(e.message), "\n", " ")
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = "claude agent model execution failed"
	}
	return &backendplugin.PluginError{Code: e.code, Message: message, Retryable: false}
}

func classifyEngineError(err error) error {
	if errors.Is(err, aipapi.ErrCapabilityReject) {
		return err
	}
	if continuation, ok := errors.AsType[*bridge.ContinuationError](err); ok {
		return &classifiedError{code: backendplugin.ErrorCodeFailedPrecondition, message: continuation.Error()}
	}
	if _, ok := errors.AsType[*bridge.SidecarError](err); ok {
		return classifySidecarError(err)
	}
	return &classifiedError{code: backendplugin.ErrorCodeFailedPrecondition, message: err.Error()}
}

func classifySidecarError(err error) error {
	if sidecar, ok := errors.AsType[*bridge.SidecarError](err); ok {
		code := backendplugin.ErrorCodeInternal
		switch sidecar.Code {
		case "authentication_failed", "auth_required", "oauth_org_not_allowed", "account_on_hold", "verification_required", "cloud_credential_error":
			code = backendplugin.ErrorCodeUnauthenticated
		case "rate_limited", "plan_exhausted":
			code = backendplugin.ErrorCodeResourceExhausted
		case "permission_denied", "third_party_extra_usage":
			// Both are non-retryable provider permission/entitlement decisions.
			code = backendplugin.ErrorCodePermissionDenied
		case "protocol_error", "protocol_mismatch", "tool_correlation_failed", "empty_response":
			code = backendplugin.ErrorCodeProtocolViolation
		case "unavailable", "sdk_unavailable", "mcp_unavailable":
			code = backendplugin.ErrorCodeUnavailable
		case "native_tool_use", "native_tools_exposed", "native_mcp_exposed", "unsupported_parallel_tool_calls", "unsupported_post_tool_text", "tool_abandoned", "superseded_output_committed":
			code = backendplugin.ErrorCodeFailedPrecondition
		}
		return &classifiedError{code: code, message: sidecar.Error()}
	}
	return &classifiedError{code: backendplugin.ErrorCodeUnavailable, message: "claude agent model sidecar is unavailable"}
}

type classifiedStream struct {
	inner    aipapi.ManagedEventStream
	terminal bool
	failed   bool
}

func (s *classifiedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.failed {
		return aipapi.Event{}, io.EOF
	}
	event, err := s.inner.Recv(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if !s.terminal {
				// Only a stream that actually delivered response_finished may end
				// in a normal EOF; the shared forwarder turns EOF into success.
				s.failed = true
				return aipapi.Event{}, classifyEngineError(&bridge.SidecarError{
					Code:    "protocol_error",
					Message: "the sidecar stream ended before a terminal response event",
				})
			}
			return event, err
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.failed = true
			return aipapi.Event{}, classifyEngineError(err)
		}
		return event, err
	}
	if event.Kind == aipapi.EventResponseFinished {
		s.terminal = true
	}
	return event, nil
}

func (s *classifiedStream) Close() error { return s.inner.Close() }

func (s *classifiedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}

// prefixedStream surfaces connector-owned warnings on a served response. The
// canonical sequence allows warnings only after response_started, and the
// unchanged Anthropic encoder rejects a warning before it, so the warnings are
// held until the first response_started event. A failed open has no valid place
// for them: the typed terminal already carries the failure.
type prefixedStream struct {
	events    []aipapi.Event
	inner     aipapi.ManagedEventStream
	delivered bool
}

func (s *prefixedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.delivered && len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	event, err := s.inner.Recv(ctx)
	if err != nil {
		return event, err
	}
	if event.Kind == aipapi.EventResponseStarted && !s.delivered {
		s.delivered = true
	}
	return event, nil
}

func (s *prefixedStream) Close() error { return s.inner.Close() }

func (s *prefixedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}

func (e *engine) Close(context.Context) error {
	if e.sessions != nil {
		return e.sessions.Close()
	}
	return nil
}

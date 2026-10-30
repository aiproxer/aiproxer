package conformance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	frontanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	frontgemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	testkitopenresponses "github.com/aiproxer/aiproxer/internal/testkit/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

// Authoritative harness frontend identities. The set is a superset of the locked
// baseline matrix (BundledFrontendIDs) and adds the OpenResponses frontend.
const (
	FrontendOpenAIResponses = "openai-responses"
	FrontendOpenAILegacy    = "openai-legacy"
	FrontendAnthropic       = "anthropic"
	FrontendGemini          = "gemini"
	FrontendOpenResponses   = "openresponses"
)

// Authoritative harness backend identities. openrouter and nvidia are provider
// connector columns that are not constructible in the base essential bundle;
// the generic selector fails them closed and the OpenResponses row (Task 8.3)
// proves their actual route through the real connector executables
// (DeployConnectorColumnFor / connector_host.go) without promoting the
// connectors to essential status.
const (
	BackendOpenAIResponses = "openai-responses"
	BackendOpenAILegacy    = "openai-legacy"
	BackendAnthropic       = "anthropic"
	BackendGemini          = "gemini"
	BackendBedrock         = "bedrock"
	BackendACP             = "acp"
	BackendOpenRouter      = "openrouter"
	BackendNVIDIA          = "nvidia"
	// BackendLocalStub is the self-contained executable stub column used for
	// staged-executable terminal proof (issue #1104). It is deliberately not
	// in HarnessBackendIDs: that list drives the deployment matrix, while the
	// stub column exists only for the connector terminal-semantics matrix.
	BackendLocalStub = "local-stub"
	// BackendLlamaCpp is the local OpenAI-compatible server column used for
	// staged-executable terminal proof of the shared openaicompat chat
	// mapping (issue #1155). Like the stub it stays out of
	// HarnessBackendIDs; terminal cells point it at scripted origins.
	BackendLlamaCpp = "llamacpp"
	// BackendLmstudio and BackendVllm extend the staged chat-family proof
	// to the identical-shape local/server connectors (issue #1155).
	BackendLmstudio = "lmstudio"
	BackendVllm     = "vllm"
	// BackendOllama, BackendHuggingFace, BackendInfomaniak and
	// BackendCommandCodeOpenAI extend the staged proof to base_url-direct
	// dual-flavor openaicompat connectors (issue #1155 sweep): scripted
	// origins serve both flavors, sk-test credentials satisfy Configure,
	// and cells drive both operations explicitly.
	BackendOllama            = "ollama"
	BackendHuggingFace       = "huggingface"
	BackendInfomaniak        = "infomaniak-ai"
	BackendCommandCodeOpenAI = "commandcode-openai"
	// Static-token OAuth columns (issue #1155 custom batch): chat-only
	// openaicompat mapping with inference_url overrides steering the wire
	// at scripted origins; synthetic api_key secrets take the static-token
	// path so no OAuth issuer is involved.
	BackendNousPortal = "nous-portal"
	BackendQwenOAuth  = "qwen-oauth"
	BackendXaiOAuth   = "xai-oauth"
	// Vendor-endpoint staged columns (issue #1155 remnant sweep): same
	// shared mapping, bespoke config shapes (api_origin overrides keep the
	// standard tails the scripted origins serve). sapaicore stays out:
	// its OAuth token provider needs a fake OAuth issuer (follow-up).
	BackendAzureOpenAI      = "azure-openai"
	BackendCloudflare       = "cloudflare"
	BackendDatabricks       = "databricks-ai"
	BackendSnowflake        = "snowflake-cortex"
	BackendOpenResponses    = "openresponses"
	BackendCompatibleOpenAI = standardplugins.CustomOpenAIResponsesCompatibleID
)

// HarnessFrontendIDs returns the deterministic authoritative harness frontend
// list (5 bundled real frontends including OpenResponses).
func HarnessFrontendIDs() []string {
	return []string{
		FrontendOpenAIResponses,
		FrontendOpenAILegacy,
		FrontendAnthropic,
		FrontendGemini,
		FrontendOpenResponses,
	}
}

// HarnessBackendIDs returns the deterministic authoritative harness backend
// list (the 6 essential backends, the OpenResponses backend, and the
// provider-connector columns openrouter/nvidia).
func HarnessBackendIDs() []string {
	return []string{
		BackendOpenAIResponses,
		BackendOpenAILegacy,
		BackendAnthropic,
		BackendGemini,
		BackendBedrock,
		BackendACP,
		BackendOpenRouter,
		BackendNVIDIA,
		BackendOpenResponses,
	}
}

// ClientTransport selects the client entrypoint used to drive one deployment.
type ClientTransport string

const (
	TransportJSON      ClientTransport = "json"
	TransportSSE       ClientTransport = "sse"
	TransportCompact   ClientTransport = "compact"
	TransportWebSocket ClientTransport = "websocket"
)

// Mutually exclusive Agent Loop Guard strategy selectors a deployment may
// compose. They mirror the feature's own stable configuration vocabulary, so a
// cell selects a strategy the production factory actually accepts.
const (
	AgentLoopGuardStrategyAttemptCompletion = "attempt_completion"
	AgentLoopGuardStrategySemanticVerifier  = "semantic_verifier"
)

// Candidate is one additional real backend candidate in the deployment's
// failover chain, with its own injectable contract-fake origin.
type Candidate struct {
	// Backend is an authoritative backend ID.
	Backend string
	// ProfileID selects a validated profile when Backend is a compatible family.
	ProfileID string
	// OriginFail selects a deterministic failure mode for the candidate origin.
	OriginFail OriginFailMode
	// ProviderOrigin injects an external reference-provider origin base URL.
	ProviderOrigin string
	// OriginHandler injects a custom reference-provider origin responder for this
	// candidate, exactly as DeploymentSpec.OriginHandler does for the primary
	// backend, while the observing proxy still counts, captures, and redacts every
	// request. nil keeps the default family responder. It is what lets a failover
	// or parallel-race cell script one candidate leg deterministically.
	OriginHandler http.Handler
}

// DeploymentSpec is the generic cell selector: one spec resolves the entire
// deployment — client entrypoint, real frontend, core executor, real backend(s),
// and injectable reference-provider origin(s) — with no bespoke pairwise wiring.
//
// This is the Phase 7 SMOKE SCAFFOLDING selector. It proves the reusable harness
// can compose any authoritative cell with contract-fake origins and injectable
// failure modes; it is NOT Phase 8 compatibility evidence. Phase 8 injects the
// independent OpenResponses refbackend/refclient emulators and certifies each
// cell with official-wire scenarios (tasks 8.1–8.5).
type DeploymentSpec struct {
	// Frontend is an authoritative frontend ID (HarnessFrontendIDs).
	Frontend string
	// Backend is an authoritative backend ID (HarnessBackendIDs).
	Backend string
	// Model is the canonical model used by the client; empty uses the harness
	// default for Backend.
	Model string
	// Transport selects the client entrypoint (json/sse/compact/websocket).
	// Empty defaults to json.
	Transport ClientTransport
	// ProviderOrigin injects an external reference-provider origin base URL for
	// the primary backend (existing reference families, Phase 8 refbackend).
	// Empty deploys a harness contract-fake origin.
	ProviderOrigin string
	// ProfileID selects a validated provider profile for a compatible family.
	ProfileID string
	// ProviderClient is the HTTP client used by the real backend to reach the
	// origin; empty uses the origin loopback client.
	ProviderClient *http.Client
	// OriginFail selects a deterministic contract-fake origin failure mode for
	// the primary backend.
	OriginFail OriginFailMode
	// Candidates appends additional real backend candidates to the failover
	// chain, each with an independent injectable origin.
	Candidates []Candidate
	// Clock injects a virtual clock into the harness origins.
	Clock testkitopenresponses.VirtualClock
	// ArtifactLimit bounds the redacted request-capture artifact list per origin.
	ArtifactLimit int
	// OriginHandler injects a custom reference-provider origin responder for the
	// primary backend. When set, it replaces the harness contract-fake family
	// responder while the observing proxy still counts, captures, and redacts
	// every request. nil keeps the default family responder. This is the seam
	// Task 7.4 adversarial origins use (event injection, native ID/native
	// opaque evidence, abrupt mid-stream death).
	OriginHandler http.Handler
	// ContinuationMaxChainDepth overrides the OpenResponses frontend
	// continuation max_chain_depth when greater than zero, so amplification
	// proofs can exercise a short chain instead of the production default of 64.
	ContinuationMaxChainDepth int
	// AgentLoopGuardStrategy composes one real Agent Loop Guard feature
	// generation for the selected mutually exclusive strategy and installs it on
	// the deployment executor's request runtime snapshot, through the production
	// feature registry, enabled-surface merge, and production snapshot builder.
	// Empty (the default) composes no feature generation at all, so every existing
	// cell keeps its current no-provider terminal behavior. Preferred-strategy
	// cells get exactly the control provider and terminal provider the factory
	// composed; no provider is hand-assigned here or by any caller.
	AgentLoopGuardStrategy string
}

// Validate returns a non-nil error for cells the generic selector must not
// deploy: unknown/empty identities, unknown transports, an unknown Agent Loop
// Guard strategy, or provider-connector backends that are not constructible in
// the base harness.
func (s DeploymentSpec) Validate() error {
	switch s.Transport {
	case "", TransportJSON, TransportSSE, TransportCompact, TransportWebSocket:
	default:
		return fmt.Errorf("harness: unknown client transport %q", s.Transport)
	}
	if !containsString(HarnessFrontendIDs(), s.Frontend) {
		return fmt.Errorf("harness: unknown frontend %q", s.Frontend)
	}
	if !containsString(HarnessBackendIDs(), s.Backend) && s.Backend != BackendCompatibleOpenAI {
		return fmt.Errorf("harness: unknown backend %q", s.Backend)
	}
	switch s.Backend {
	case BackendOpenRouter, BackendNVIDIA:
		return fmt.Errorf("harness: backend %q is a provider-connector column not constructible in the base essential bundle (Phase 8.5)", s.Backend)
	}
	switch s.AgentLoopGuardStrategy {
	case "", AgentLoopGuardStrategyAttemptCompletion, AgentLoopGuardStrategySemanticVerifier:
	default:
		return fmt.Errorf("harness: unknown agent loop guard strategy %q", s.AgentLoopGuardStrategy)
	}
	return nil
}

func containsString(xs []string, v string) bool {
	return slices.Contains(xs, v)
}

// Deployment is one deterministic full-path deployment: a configurable client
// entrypoint in front of a real frontend handler, the real core executor, real
// backend adapter(s), and injectable reference-provider origins.
type Deployment struct {
	Spec DeploymentSpec
	// Exec is the wired real core executor.
	Exec *runtime.Executor
	// RouteSelector is the primary route selector the frontend default routes to.
	RouteSelector string
	// Mux is the composed frontend mux.
	Mux *http.ServeMux
	// Server is the full-path frontend origin.
	Server *httptest.Server
	// Client is the transport-selected client entrypoint.
	Client ClientEntrypoint
	// Clock is the injected virtual clock (nil when not injected).
	Clock testkitopenresponses.VirtualClock

	backends         map[string]execbackend.Backend
	origins          map[string]*Origin
	candidateOrigins []*Origin

	genCancel context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

// Deploy composes one full-path deployment from a single generic cell selector.
// It returns nil for invalid or not-yet-constructible cells without starting
// any server, origin, or port.
func Deploy(tb testing.TB, spec DeploymentSpec) *Deployment {
	tb.Helper()
	if err := spec.Validate(); err != nil {
		return nil
	}
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		model = harnessDefaultModel(spec.Backend)
	}

	d := &Deployment{
		Spec:     spec,
		Clock:    spec.Clock,
		origins:  map[string]*Origin{},
		backends: map[string]execbackend.Backend{},
	}

	primaryOrigin := newHarnessOrigin(tb, spec.Backend, spec.OriginFail, spec.Clock, spec.ArtifactLimit, spec.ProviderOrigin, spec.ProviderClient, spec.OriginHandler)
	d.origins[spec.Backend] = primaryOrigin
	d.backends[spec.Backend] = harnessBackendForProfile(tb, spec.Backend, primaryOrigin.URL(), primaryOrigin.Client(), spec.ProfileID)

	var route strings.Builder
	route.WriteString(RouteSelector(spec.Backend, model))
	for i, cand := range spec.Candidates {
		if !containsString(HarnessBackendIDs(), cand.Backend) || cand.Backend == BackendOpenRouter || cand.Backend == BackendNVIDIA {
			tb.Fatalf("harness: invalid candidate %q", cand.Backend)
		}
		candOrigin := newHarnessOrigin(tb, cand.Backend, cand.OriginFail, spec.Clock, spec.ArtifactLimit, cand.ProviderOrigin, nil, cand.OriginHandler)
		candKey := candidateBackendKey(spec.Backend, i)
		d.origins[candKey] = candOrigin
		d.candidateOrigins = append(d.candidateOrigins, candOrigin)
		d.backends[candKey] = harnessBackendFor(tb, cand.Backend, candOrigin.URL(), candOrigin.Client())
		route.WriteString("|" + RouteSelector(candKey, model))
	}
	d.RouteSelector = route.String()

	d.Exec = harnessExecutor(tb, d.backends, spec.Backend)
	if spec.AgentLoopGuardStrategy != "" {
		planes, err := AgentLoopGuardFeaturePlanes(tb, spec.AgentLoopGuardStrategy)
		if err != nil {
			_ = d.Close()
			tb.Fatalf("harness: compose agent loop guard generation %q: %v", spec.AgentLoopGuardStrategy, err)
		}
		d.Exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(d.Exec.Bus, extensions.SnapshotOptions{FeaturePlanes: planes})
	}

	d.Mux = http.NewServeMux()
	genCtx, genCancel := context.WithCancel(context.Background())
	d.genCancel = genCancel
	if err := mountHarnessFrontend(genCtx, d.Mux, spec.Frontend, d.Exec, d.RouteSelector, spec.ContinuationMaxChainDepth); err != nil {
		_ = d.Close()
		tb.Fatalf("harness: mount frontend %q: %v", spec.Frontend, err)
	}
	serverMux := d.Mux
	if spec.Frontend == FrontendOpenResponses {
		// The OpenResponses frontend defaults store:true and reserves a scoped
		// proxy response id, exactly like the production composition whose
		// session/auth middleware attaches an authoritative session identity to
		// every request. The harness mirrors that seam: anonymous requests that
		// carry no proxy session id fall back to one stable harness session so
		// the default store:true path stays exercisable (and store:false and
		// connection-local WebSocket behavior are unaffected).
		serverMux = withHarnessSession(d.Mux)
	}
	d.Server = httptest.NewServer(serverMux)

	d.Client = harnessClientFor(tb, spec.Frontend, d)

	tb.Cleanup(func() { _ = d.Close() })
	return d
}

// Close releases every owned resource deterministically: the frontend listener,
// all reference-provider origins, and the frontend generation context. It is
// idempotent.
func (d *Deployment) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.genCancel != nil {
			d.genCancel()
		}
		for _, o := range d.origins {
			_ = o.Close()
		}
		for _, o := range d.candidateOrigins {
			_ = o.Close()
		}
		if d.Server != nil {
			d.Server.Close()
		}
	})
	return d.closeErr
}

// BaseURL returns the full-path frontend origin base URL.
func (d *Deployment) BaseURL() string {
	if d == nil || d.Server == nil {
		return ""
	}
	return d.Server.URL
}

// FrontendAddr returns the frontend origin listen address (host:port). Prefer
// [Deployment.Server]'s listener for lifecycle assertions: a freed ephemeral port
// can be taken by another listener, so the address alone does not identify this
// frontend after release.
func (d *Deployment) FrontendAddr() string {
	if d == nil || d.Server == nil {
		return ""
	}
	raw := strings.TrimPrefix(d.Server.URL, "http://")
	return strings.TrimSuffix(raw, "/")
}

// OriginFor returns the primary contract-fake origin for backendID.
func (d *Deployment) OriginFor(backendID string) *Origin {
	if d == nil {
		return nil
	}
	return d.origins[backendID]
}

// Backend returns the constructed backend for a backend slot. It exposes the
// host-built connector backend so evidence can drive connector-specific surfaces
// (for example dynamic model inventory) that only the real connector exposes.
func (d *Deployment) Backend(backendID string) execbackend.Backend {
	if d == nil {
		return execbackend.Backend{}
	}
	return d.backends[backendID]
}

// CandidateOrigin returns the i-th candidate origin.
func (d *Deployment) CandidateOrigin(i int) *Origin {
	if d == nil || i < 0 || i >= len(d.candidateOrigins) {
		return nil
	}
	return d.candidateOrigins[i]
}

// RequestCount returns the number of upstream requests observed by the primary
// contract-fake origin for backendID.
func (d *Deployment) RequestCount(backendID string) int {
	o := d.OriginFor(backendID)
	if o == nil {
		return 0
	}
	return o.Count()
}

// RoundTripModel performs one JSON create round trip with an explicit model,
// for pre-network rejection and unroutable-model proofs.
func (d *Deployment) RoundTripModel(ctx context.Context, model, prompt string) error {
	if d == nil || d.Server == nil {
		return fmt.Errorf("harness: deployment is closed or nil")
	}
	client := newOpenResponsesHTTPClient(d.Server.URL, d.Server.Client(), TransportJSON, model)
	res, err := client.RoundTrip(ctx, prompt)
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Text) == "" {
		return fmt.Errorf("harness: empty assistant text for model %q", model)
	}
	return nil
}

// SendRawCreate posts an arbitrary JSON create body to the OpenResponses
// frontend and returns an error when the frontend does not accept it.
func (d *Deployment) SendRawCreate(ctx context.Context, rawBody string) error {
	return rawOpenResponsesPost(ctx, d.Server.Client(), d.Server.URL+"/openresponses/v1/responses", rawBody)
}

// SendRawCompact posts an arbitrary JSON compact body to the OpenResponses
// frontend and returns an error when the frontend does not accept it.
func (d *Deployment) SendRawCompact(ctx context.Context, rawBody string) error {
	return rawOpenResponsesPost(ctx, d.Server.Client(), d.Server.URL+"/openresponses/v1/responses/compact", rawBody)
}

// SendRawWSTurn dials one WebSocket connection, sends an arbitrary turn frame,
// and returns an error when the session rejects the turn (classified error).
func (d *Deployment) SendRawWSTurn(ctx context.Context, rawTurn string) error {
	client, err := newOpenResponsesWSClient(d.Server.URL, "unused", d.Server.Client())
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return client.sendRaw(ctx, rawTurn)
}

// RawFrontendPost posts rawBody to an arbitrary frontend path and returns the
// HTTP status without fataling, so fail-closed cells can be asserted cleanly.
func (d *Deployment) RawFrontendPost(ctx context.Context, path, rawBody string) (int, error) {
	if d == nil || d.Server == nil {
		return 0, fmt.Errorf("harness: deployment is closed or nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Server.URL+path, strings.NewReader(rawBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// withHarnessSession wraps mux so every request lacking a proxy-owned session
// id receives one stable synthetic session header before the OpenResponses
// frontend decodes it. This mirrors the production auth/session middleware that
// attaches an authoritative session identity; it never overrides a header the
// test already set.
func withHarnessSession(next *http.ServeMux) *http.ServeMux {
	wrapped := http.NewServeMux()
	wrapped.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-AIP-Session-Id")) == "" {
			r.Header.Set("X-AIP-Session-Id", "sess-harness-anon")
		}
		next.ServeHTTP(w, r)
	}))
	return wrapped
}

// harnessDefaultModel returns the canonical model the harness client uses when
// the spec does not pin one.
func harnessDefaultModel(backendID string) string {
	switch backendID {
	case BackendOpenResponses, BackendCompatibleOpenAI:
		return "gpt-4o-mini"
	default:
		return DefaultModel(backendID)
	}
}

// candidateBackendKey names the executor backend slot and route leaf of
// candidate i behind primary backendID so candidates never collide with the
// primary backend in routing or origin slots.
func candidateBackendKey(primary string, i int) string {
	return fmt.Sprintf("%s-c%d", primary, i+2)
}

// harnessBackendFor constructs the real backend adapter for an authoritative
// backend ID against an origin base URL. OpenResponses uses the generic
// compatible factory; the essential families reuse [BackendFor].
func harnessBackendFor(tb testing.TB, backendID, originURL string, httpClient *http.Client) execbackend.Backend {
	tb.Helper()
	return harnessBackendForProfile(tb, backendID, originURL, httpClient, "")
}

func harnessBackendForProfile(tb testing.TB, backendID, originURL string, httpClient *http.Client, profileID string) execbackend.Backend {
	tb.Helper()
	if backendID == BackendCompatibleOpenAI {
		profile, err := providerprofiles.EmbeddedProfile(profileID)
		if err != nil {
			tb.Fatalf("harness: profile %q: %v", profileID, err)
		}
		if _, err := providerprofiles.CompileProfile(profile); err != nil {
			tb.Fatalf("harness: compile profile %q: %v", profileID, err)
		}
		// Compile the actual catalog profile, then override only its endpoint for
		// the local observing provider. The backend is built through the standard
		// family compiler, not through a profile-shaped metadata-only fixture.
		profile.Endpoint.BaseURL = originURL + "/v1"
		profile.Auth.EnvVar = "PROFILE_SENTINEL_KEY"
		tb.Setenv("PROFILE_SENTINEL_KEY", "sk-profile-sentinel")
		compiled, err := providerprofiles.CompileProfile(profile)
		if err != nil {
			tb.Fatalf("harness: compile profile %q: %v", profileID, err)
		}
		backend, err := standardplugins.BuildProviderProfileBackend(compiled, "profile-sentinel", httpClient, pluginreg.BackendFactoryDeps{})
		if err != nil {
			tb.Fatalf("harness: build compatible profile family: %v", err)
		}
		return backend
	}
	if backendID == BackendOpenResponses {
		raw := "backend_prefix: harness-or\nbase_url: " + originURL + "\n"
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			tb.Fatalf("harness: openresponses config: %v", err)
		}
		be, err := openresponsescompat.Build("harness-or", n, httpClient)
		if err != nil {
			tb.Fatalf("harness: openresponses backend: %v", err)
		}
		return be
	}
	return BackendFor(tb, backendID, originURL, httpClient)
}

// harnessExecutor wires the real core executor with the real backend set and a
// deterministic routing RNG, plus the standard conformance secure-session seam.
func harnessExecutor(tb testing.TB, backends map[string]execbackend.Backend, defaultBackend string) *runtime.Executor {
	tb.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatalf("harness: memory store: %v", err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = backends
	ex.DefaultBackend = defaultBackend
	testkit.WireConformanceExecutorSecureSession(tb, ex)
	return ex
}

// AgentLoopGuardFeaturePlanes compiles one real Agent Loop Guard feature
// generation for the selected mutually exclusive strategy and returns its frozen
// plane set.
//
// The generation is produced by the production composition path — a standard
// plugin registry, the enabled feature registrations, the enabled-surface merge,
// and the generated plane merge — so the returned planes carry the providers the
// factory actually composed. It never constructs a provider by hand and never
// substitutes a fake: a cell that needs extra observation planes replays these
// planes into its own contribution set instead.
//
// An unknown strategy or a rejected generation is reported as an error, because
// both are seam defects rather than behavior any cell is testing; [Deploy] turns
// that error into a test failure.
func AgentLoopGuardFeaturePlanes(tb testing.TB, strategy string) (aipfeature.FrozenPlaneSet, error) {
	tb.Helper()
	return AgentLoopGuardFeaturePlanesWithConfig(tb, strategy, "")
}

// AgentLoopGuardFeaturePlanesWithConfig compiles one real Agent Loop Guard
// feature generation exactly like AgentLoopGuardFeaturePlanes, with extraConfig
// appended as additional YAML lines of the feature block (for example, protocol
// limits). An empty extraConfig composes the generation defaults.
func AgentLoopGuardFeaturePlanesWithConfig(tb testing.TB, strategy, extraConfig string) (aipfeature.FrozenPlaneSet, error) {
	tb.Helper()

	switch strategy {
	case AgentLoopGuardStrategyAttemptCompletion, AgentLoopGuardStrategySemanticVerifier:
	default:
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("harness: unknown agent loop guard strategy %q", strategy)
	}
	raw := "enabled: true\nstrategy: " + strategy + "\n" + extraConfig
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("harness: agent loop guard config: %w", err)
	}
	registry := pluginreg.NewRegistry()
	if err := standardplugins.InstallBundleOn(registry, standardplugins.StandardBundle()); err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("harness: install standard bundle: %w", err)
	}
	registrations := []aipsdk.Registration{{
		ID:      agentloopguard.ID,
		Kind:    aipsdk.PluginKindFeature,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(registry, registrations, featurebundle.HostContributions{})
	if err != nil {
		return aipfeature.FrozenPlaneSet{}, fmt.Errorf("harness: merge enabled feature surface: %w", err)
	}
	return merged.Frozen, nil
}

// mountHarnessFrontend mounts the real frontend handler for an authoritative
// frontend ID on mux, bound to the executor and the primary route selector.
// continuationDepth, when greater than zero, overrides the OpenResponses
// frontend continuation max_chain_depth for amplification proofs.
func mountHarnessFrontend(genCtx context.Context, mux *http.ServeMux, frontendID string, exec *runtime.Executor, routeSelector string, continuationDepth int) error {
	switch frontendID {
	case FrontendOpenAIResponses:
		mux.Handle("POST /v1/responses", &frontopenairesponses.Handler{Exec: exec, DefaultRouteSelector: routeSelector})
	case FrontendOpenAILegacy:
		mux.Handle("POST /v1/chat/completions", &frontopenailegacy.Handler{Exec: exec, DefaultRouteSelector: routeSelector})
	case FrontendAnthropic:
		mux.Handle("POST /v1/messages", &frontanthropic.Handler{Exec: exec, DefaultRouteSelector: routeSelector})
	case FrontendGemini:
		h := &frontgemini.Handler{Exec: exec, DefaultRouteSelector: routeSelector}
		mux.Handle("/v1beta/", h)
		mux.Handle("/v1beta1/", h)
	case FrontendOpenResponses:
		cfgYAML := "{}"
		if continuationDepth > 0 {
			cfgYAML = fmt.Sprintf("continuation:\n  max_chain_depth: %d\n", continuationDepth)
		}
		var cfg yaml.Node
		if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
			return fmt.Errorf("openresponses config: %w", err)
		}
		opts := aipsdk.FrontendMountOptions{
			PluginCfg:            cfg,
			Exec:                 exec,
			DefaultRoute:         routeSelector,
			GenerationContext:    genCtx,
			AllowUnauthenticated: true, // isolated conformance deployment has no outer auth middleware
		}
		return frontopenresponses.Mount(mux, opts)
	default:
		return fmt.Errorf("harness: unknown frontend %q", frontendID)
	}
	return nil
}

// harnessClientFor builds the transport-selected client entrypoint for a
// frontend family. OpenResponses uses the harness raw wire client (JSON/SSE/
// compact/WS); existing families reuse the repository independent reference
// clients through the existing family client.
func harnessClientFor(tb testing.TB, frontendID string, d *Deployment) ClientEntrypoint {
	tb.Helper()
	if frontendID == FrontendOpenResponses {
		return newOpenResponsesClient(d)
	}
	return &existingFamilyClient{tb: tb, frontendID: frontendID, deployment: d}
}

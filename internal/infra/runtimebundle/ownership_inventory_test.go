package runtimebundle_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ownershipClass classifies an assembled resource for reload migration
// (versioned-runtime-reloadable-proxy-configuration requirements 6.1–6.10).
type ownershipClass string

const (
	ownershipProcess      ownershipClass = "process"
	ownershipGeneration   ownershipClass = "generation"
	ownershipRequestAsync ownershipClass = "request_async"
)

func (c ownershipClass) valid() bool {
	switch c {
	case ownershipProcess, ownershipGeneration, ownershipRequestAsync:
		return true
	default:
		return false
	}
}

// ownershipKind marks non-resource aggregates/containers that must not be
// published as process/generation/request-async resources (req 4.9).
type ownershipKind string

const (
	ownershipKindMixedAggregate         ownershipKind = "mixed_aggregate"
	ownershipKindMixedContainer         ownershipKind = "mixed_container"
	ownershipKindMixedConfigAggregate   ownershipKind = "mixed_config_aggregate"
	ownershipKindMixedTeardownAggregate ownershipKind = "mixed_teardown_aggregate"
)

func (k ownershipKind) validMixed() bool {
	switch k {
	case ownershipKindMixedAggregate, ownershipKindMixedContainer,
		ownershipKindMixedConfigAggregate, ownershipKindMixedTeardownAggregate:
		return true
	default:
		return false
	}
}

// ownershipEntry records one classified resource with source context.
// Architecture-only ledger kept in tests (task 1.1 finding 7).
type ownershipEntry struct {
	Symbol        string
	Class         ownershipClass
	Kind          ownershipKind
	Source        string
	ConstructorID string
	Notes         string
}

// candidateAssemblyInternalFields are candidateAssembly lifecycle-bookkeeping
// fields (sync primitives / transfer flags), not resources; they are excluded
// from ownership classification (PR B2 grouped assembly).
var candidateAssemblyInternalFields = map[string]bool{
	"lifeMu": true, "lifeClaimed": true, "ledgerTransferred": true,
}

// builtFieldOwnership classifies every ownership-relevant field of
// candidateAssembly (package-private grouped successor to CandidateRuntime).
var builtFieldOwnership = []ownershipEntry{
	{Symbol: "execution", Kind: ownershipKindMixedAggregate, Source: "candidate_http.go → candidateExecutionGroup", Notes: "Generation execution/routing outputs grouped; not a flat resource bag."},
	{Symbol: "security", Kind: ownershipKindMixedAggregate, Source: "candidate_http.go → candidateSecurityGroup", Notes: "Generation auth/session/runtime-snapshot outputs grouped."},
	{Symbol: "models", Kind: ownershipKindMixedAggregate, Source: "candidate_http.go → candidateModelGroup", Notes: "Generation catalog/registry outputs grouped."},
	{Symbol: "operations", Kind: ownershipKindMixedAggregate, Source: "candidate_http.go → candidateOperationsGroup", Notes: "Generation control/readiness/terminal projection outputs grouped."},
	{Symbol: "process", Kind: ownershipKindMixedAggregate, Source: "candidate_http.go → candidateProcessRefs", Notes: "Explicitly non-owning process references; Close must never tear these down."},
	{Symbol: "ledger", Class: ownershipGeneration, Source: "runtimebundle.compileCandidate → NewResourceLedger", Notes: "Sole generation-owned resource lifecycle owner: rollback/quiesce/close (req 2.8, 3.8, 8.3-8.4)."},
	{Symbol: "terminalWorkReady", Class: ownershipProcess, Source: "runtimebundle.terminalWorkRuntime.checkReady", Notes: "Composition-root readiness hook."},
	{Symbol: "terminalWorkRT", Class: ownershipProcess, Source: "runtimebundle.buildTerminalWorkWithSetReconcile", Notes: "Internal process ownership handle."},
}

// bootstrapResourceOwnership classifies BuildHost resources beyond Built fields.
var bootstrapResourceOwnership = []ownershipEntry{
	{Symbol: "Host.Process", Class: ownershipProcess, Source: "runtimebundle.BuildHost → NewProcessServices", Notes: "Process-owned shared services for the standard Host (single-snapshot startup transaction)."},
	{Symbol: "Host.Manager", Class: ownershipProcess, Source: "runtimebundle.BuildHost → runtimehost.NewManager", Notes: "Process-owned generation publication manager."},
	{Symbol: "Host.Manager.Active", Class: ownershipGeneration, Source: "runtimebundle.BuildHost → Manager.Publish", Notes: "Generation 1 publication handle for the standard Host."},
	{Symbol: "Host.Config", Kind: ownershipKindMixedConfigAggregate, Source: "runtimebundle.BuildHost → config.LoadFile", Notes: "Mutable *config.Config mixes process-topology and generation settings; unsuitable for generation publication (design: no mutable *config.Config in published generations)."},
	{Symbol: "Logger", Class: ownershipProcess, Source: "runtimebundle.BuildHost → logging.NewLogger", Notes: "Process logger sink (req 6.4)."},
	{Symbol: "Registry", Class: ownershipProcess, Source: "runtimebundle.BuildHost → pluginreg.NewRegistry", Notes: "Same process factory catalog as Built.PluginRegistry."},
	{Symbol: "Registrations", Class: ownershipGeneration, Source: "runtimebundle.BuildHost → config.RegistrationsFromConfig", Notes: "Feature/plugin registration view for the candidate (transient; not retained on Host)."},
	{Symbol: "ShutdownTracing", Class: ownershipProcess, Source: "runtimebundle.BuildHost → tracing.Init", ConstructorID: "tracing.Init.Result.Shutdown", Notes: "Projection of process tracing shutdown; underlying provider/exporter owned below."},
	{Symbol: "OutboundTracing", Class: ownershipProcess, Source: "runtimebundle.BuildHost → tracing.Init", ConstructorID: "tracing.Init.Result.Active", Notes: "Process flag that outbound HTTP propagation is active."},
	{Symbol: "tracing.Exporter", Class: ownershipProcess, Source: "internal/infra/tracing.Init → otlptracehttp.New", ConstructorID: "otlptracehttp.New", Notes: "OTLP HTTP exporter created by tracing.Init (req 6.4)."},
	{Symbol: "tracing.Provider", Class: ownershipProcess, Source: "internal/infra/tracing.Init → sdktrace.NewTracerProvider", ConstructorID: "sdktrace.NewTracerProvider", Notes: "Global tracer provider registered once (req 6.4)."},
	{Symbol: "tracing.Propagator", Class: ownershipProcess, Source: "internal/infra/tracing.Init → otel.SetTextMapPropagator", ConstructorID: "propagation.NewCompositeTextMapPropagator", Notes: "Global text-map propagator installed by tracing.Init."},
	{Symbol: "tracing.ShutdownLifecycle", Class: ownershipProcess, Source: "internal/infra/tracing.Init shutdown closure", ConstructorID: "tracing.Init.shutdown", Notes: "Process shutdown closes tracer provider/exporter."},
	{Symbol: "metrics.Registry", Class: ownershipProcess, Source: "internal/infra/metrics.NewRegistry → prometheus.NewRegistry + collectors", ConstructorID: "prometheus.NewRegistry", Notes: "Dedicated process Prometheus registry with Go/process collectors (req 6.4)."},
}

// compositionResourceOwnership classifies stdhttp/server and nested construction resources.
var compositionResourceOwnership = []ownershipEntry{
	// stdhttp handler / server
	{Symbol: "stdhttp.http.ServeMux", Class: ownershipGeneration, Source: "stdhttp.prepareStandardHandler", Notes: "Mux rebuilt with generation handler graph."},
	{Symbol: "stdhttp.composedHandler", Class: ownershipGeneration, Source: "stdhttp.prepareStandardHandler → stackHTTPHandler", Notes: "Middleware + mux handler graph is generation-owned."},
	{Symbol: "stdhttp.mountedFrontends", Class: ownershipGeneration, Source: "stdhttp.MountBundledFrontends", Notes: "Frontend instances/handlers mounted per generation."},
	{Symbol: "stdhttp.metricsHTTPInstrumentation", Class: ownershipProcess, Source: "stdhttp.mountMetrics", Notes: "Uses process Prometheus registry; instrumentation wiring follows generation mount."},
	{Symbol: "stdhttp.modelRegistryHandler", Class: ownershipGeneration, Source: "stdhttp.prepareStandardHandler → NewModelRegistryHandler", Notes: "Bound to candidate ModelRegistryRuntime."},
	{Symbol: "stdhttp.TraceIDGenerator", Class: ownershipProcess, Source: "stdhttp.prepareStandardHandler → diag.NewTraceIDGenerator", Notes: "Process-scoped ID generator."},
	{Symbol: "stdhttp.http.Server", Class: ownershipProcess, Source: "stdhttp.RunWithGenerationHost", Notes: "Data-plane listener/server never restarts for reload (req 6.4)."},
	{Symbol: "stdhttp.serveWorker", Class: ownershipProcess, Source: "stdhttp.RunWithGenerationHost listenAndServe goroutine + errCh", Notes: "Serve worker and error channel are process-owned with server."},
	{Symbol: "stdhttp.serverShutdownLifecycle", Class: ownershipProcess, Source: "stdhttp.RunWithGenerationHost → runtimebundle.Host.Close", Notes: "Trigger rejection → HTTP drain → management → Host.Close (idle, retire, process, tracing)."},

	// executor nested mutable services (process identity per req 6.6)
	{Symbol: "executor.ALegLifecycle", Class: ownershipProcess, Source: "process_services.go → buildSharedMutableRuntime → leglifecycle.NewCoordinator", Notes: "A-leg lifecycle/cancellation identity is process-owned (req 6.6)."},
	{Symbol: "executor.AffinityStore", Class: ownershipProcess, Source: "process_services.go → buildSharedMutableRuntime → affinity registry + candidate views", Notes: "Mutable affinity store identity is process-owned; views key reuse by BackendStateIdentity (req 6.6–6.8)."},
	{Symbol: "executor.CandidateHealth", Class: ownershipProcess, Source: "process_services.go → buildSharedMutableRuntime → routinghealth.CandidateHealthFromConfig", Notes: "Routing-health observation identity is process-owned; views namespace by BackendStateIdentity (req 6.6–6.8)."},
	{Symbol: "executor.AuxiliaryExecutors", Class: ownershipGeneration, Source: "build_extension.go / auxreq wiring", Notes: "Auxiliary executor runners bind generation request-plane."},
	{Symbol: "executor.toolCallFinalizers", Class: ownershipGeneration, Source: "runtime.NewExecutor ExtensionRuntime", Notes: "Tool-call finalizer set projected with generation."},
	{Symbol: "executor.preRequestHeartbeatConfig", Class: ownershipGeneration, Source: "stdhttp MountBundledFrontends PreRequestKeepalive", Notes: "Pre-request heartbeat config follows generation server settings."},

	// model/catalog startup
	{Symbol: "catalog.cacheAndRefresh", Class: ownershipGeneration, Source: "startModelCatalog / runModelCatalogRefreshLoop", Notes: "Catalog cache/view/refresh loop owned with candidate CatalogRuntime."},
	{Symbol: "catalog.closers", Class: ownershipGeneration, Source: "startModelCatalog / buildModelRuntime startedCatalog.closers + quiesceClosers", Notes: "Candidate catalog PhaseClose runtime/client cleanup and PhaseQuiesce refresh cancel/wait."},
	{Symbol: "modelRegistry.cacheAndRefresh", Class: ownershipGeneration, Source: "startModelRegistryRuntime / runModelRegistryRefreshLoop", Notes: "Registry cache/snapshot/refresh owned with candidate ModelRegistryRuntime."},
	{Symbol: "modelRegistry.closers", Class: ownershipGeneration, Source: "startModelRegistryRuntime", Notes: "Candidate registry refresh cleanup; see closerAcquisitionOwnership expression IDs."},
	{Symbol: "backend.instances", Class: ownershipGeneration, Source: "buildBackends / ResourceLedger.AddClose", Notes: "Backend instances and rollback closers are generation-owned."},

	// extension / secure-session / terminal-work / metering nested constructions
	{Symbol: "extension.State:corestate.NewMem", Class: ownershipProcess, Source: "process_services.go → buildSharedMutableRuntime → corestate.NewMem", ConstructorID: "corestate.NewMem", Notes: "Mutable extension state identity is process-owned; must not silently reset across generations (req 6.6). Distinguish from generation RuntimeSnapshot projection."},
	{Symbol: "secureSession.Generator:app.NewRandGenerator", Class: ownershipProcess, Source: "secure_session.go → app.NewRandGenerator", ConstructorID: "app.NewRandGenerator", Notes: "Secure-session token generator (process)."},
	{Symbol: "secureSession.Lineage:b2bualineage.New", Class: ownershipProcess, Source: "secure_session.go → b2bualineage.New", ConstructorID: "b2bualineage.New", Notes: "Secure-session lineage adapter over process B2BUA store."},
	{Symbol: "secureSession.Manager", Class: ownershipProcess, Source: "secure_session.go → app.NewManager", ConstructorID: "app.NewManager", Notes: "Secure-session manager identity is process-owned (req 6.2, 6.6)."},
	{Symbol: "secureSession.Recorder:app.NewRecorder", Class: ownershipProcess, Source: "secure_session.go → app.NewRecorder", ConstructorID: "app.NewRecorder", Notes: "Secure-session gate recorder is process-owned."},
	{Symbol: "secureSession.EphemeralFingerprintKey", Class: ownershipProcess, Source: "secure_session.go memory store ephemeral key", ConstructorID: "crypto/rand.Read→token_fingerprint_key", Notes: "In-memory ephemeral fingerprint-key ownership is process-local."},
	{Symbol: "terminalWork.IntentService:terminalworkapp.NewIntentService", Class: ownershipProcess, Source: "terminal_work.go → terminalworkapp.NewIntentService", ConstructorID: "terminalworkapp.NewIntentService", Notes: "Nested IntentService assembled with process terminal-work runtime."},
	{Symbol: "terminalWork.QueryService:terminalworkapp.NewQueryService", Class: ownershipProcess, Source: "terminal_work.go → terminalworkapp.NewQueryService", ConstructorID: "terminalworkapp.NewQueryService", Notes: "Nested query service over process terminal-work store."},
	{Symbol: "metering.nestedStores", Class: ownershipProcess, Source: "process_services.go → buildMeteringRuntime", ConstructorID: "journalstore.NewMemoryStore|openDurableMeteringJournal", Notes: "Metering journal/store and readiness lifecycle resources are process-owned (req 6.2)."},
	{Symbol: "accounting.nestedStores", Class: ownershipProcess, Source: "process_services.go → buildProcessAccountingStores", ConstructorID: "accountingobs.NewStats", Notes: "Token-accounting observability is process-owned; the financial token ledger is retired (Phase 8). Provider counters bind per candidate (req 6.2)."},

	// terminal work / Build subcomponents
	{Symbol: "terminalWork.store", Class: ownershipProcess, Source: "buildTerminalWorkWithSetReconcile", ConstructorID: "buildTerminalWorkWithSetReconcile.Store", Notes: "Durable terminal-work store (req 6.2–6.3)."},
	{Symbol: "terminalWork.workerGoroutines", Class: ownershipProcess, Source: "terminalworkapp.Processor tick/renew", ConstructorID: "terminalworkapp.Processor.Start", Notes: "Process worker goroutine/lifecycle resources."},
	{Symbol: "postgresPools", Class: ownershipProcess, Source: "runtimebundle.NewProcessServices → db.NewPoolRegistry", ConstructorID: "db.NewPoolRegistry", Notes: "Database pool registry is process-owned (req 6.2)."},
	{Symbol: "controlPlane.store", Class: ownershipProcess, Source: "runtimebundle.buildControlPlaneRuntime", ConstructorID: "buildControlPlaneRuntime.store", Notes: "Durable control-plane store."},

	// stdhttp remaining middleware resources
	{Symbol: "stdhttp.authMiddleware", Class: ownershipGeneration, Source: "stdhttp/auth middleware stack", ConstructorID: "stdhttp/auth.Middleware", Notes: "Auth middleware instances follow generation auth providers."},
	{Symbol: "stdhttp.accessLogMiddleware", Class: ownershipGeneration, Source: "stdhttp.stackHTTPHandler access log", ConstructorID: "stdhttp.accessLog", Notes: "Access-log middleware wiring follows generation handler stack."},
	{Symbol: "stdhttp.recoveryMiddleware", Class: ownershipGeneration, Source: "stdhttp.stackHTTPHandler recovery", ConstructorID: "stdhttp.recovery", Notes: "Recovery middleware is part of generation handler graph."},

	// request/async pins
	{Symbol: "request.stream", Class: ownershipRequestAsync, Source: "runtime.Executor.Execute / stdhttp frontend handlers", Notes: "Canonical stream pins a generation lease."},
	{Symbol: "request.preRequestHeartbeat", Class: ownershipRequestAsync, Source: "aipsdk FrontendKeepalive / frontend handlers", Notes: "Pre-request heartbeats pin the generation."},
	{Symbol: "request.terminalOrDelayedFinalizer", Class: ownershipRequestAsync, Source: "toolcall finalizers / delayed finalization", Notes: "Terminal/delayed finalizers retain generation pins."},
	{Symbol: "request.auxiliaryWork", Class: ownershipRequestAsync, Source: "auxreq ExecutorRunner", Notes: "Auxiliary work pins the generation."},
	{Symbol: "terminalwork.pendingProvider", Class: ownershipRequestAsync, Source: "terminalworkapp.Processor / provider registry", Notes: "Pending provider intents retain generation or provider pins."},
}

func TestOwnershipInventoryCoversEveryBuiltField(t *testing.T) {
	t.Parallel()

	fields := builtStructFieldNames(t)
	classified := make(map[string]ownershipEntry, len(builtFieldOwnership))
	for _, e := range builtFieldOwnership {
		if e.Symbol == "" {
			t.Fatal("builtFieldOwnership entry with empty Symbol")
		}
		if e.Kind != "" {
			if !e.Kind.validMixed() {
				t.Fatalf("builtFieldOwnership[%q]: invalid mixed kind %q", e.Symbol, e.Kind)
			}
			if e.Class.valid() {
				t.Fatalf("builtFieldOwnership[%q]: mixed kind %q must not also carry resource class %q", e.Symbol, e.Kind, e.Class)
			}
		} else if !e.Class.valid() {
			t.Fatalf("builtFieldOwnership[%q]: invalid class %q", e.Symbol, e.Class)
		}
		if e.Source == "" {
			t.Fatalf("builtFieldOwnership[%q]: empty Source", e.Symbol)
		}
		if _, dup := classified[e.Symbol]; dup {
			t.Fatalf("builtFieldOwnership duplicate symbol %q", e.Symbol)
		}
		classified[e.Symbol] = e
	}

	var missing []string
	for _, name := range fields {
		if _, ok := classified[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("Built fields missing ownership classification: %v", missing)
	}

	var extra []string
	for sym := range classified {
		found := slices.Contains(fields, sym)
		if !found {
			extra = append(extra, sym)
		}
	}
	if len(extra) != 0 {
		t.Fatalf("builtFieldOwnership symbols not present on Built: %v", extra)
	}
}

func TestOwnershipInventoryBootstrapAndCompositionEntriesAreValid(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, e := range bootstrapResourceOwnership {
		assertOwnershipEntry(t, "bootstrap", e, seen)
	}
	if len(bootstrapResourceOwnership) == 0 {
		t.Fatal("bootstrapResourceOwnership must not be empty")
	}
	for _, e := range compositionResourceOwnership {
		assertOwnershipEntry(t, "composition", e, seen)
	}
	if len(compositionResourceOwnership) == 0 {
		t.Fatal("compositionResourceOwnership must not be empty")
	}
}

func TestOwnershipInventory_CatalogAndModelRegistryAreGeneration(t *testing.T) {
	t.Parallel()
	// Leaf catalog/registry ownership lives under the models group and is
	// classified via compositionResourceOwnership after PR B2 grouping.
	for _, e := range compositionResourceOwnership {
		switch e.Symbol {
		case "catalog.cacheAndRefresh", "modelRegistry.cacheAndRefresh", "catalog.closers", "modelRegistry.closers":
			if e.Class != ownershipGeneration {
				t.Fatalf("%s must be generation-owned, got %s", e.Symbol, e.Class)
			}
		}
	}
	foundModels := false
	for _, e := range builtFieldOwnership {
		if e.Symbol == "models" {
			foundModels = true
			if e.Kind != ownershipKindMixedAggregate {
				t.Fatalf("models group must be mixed_aggregate, got class=%q kind=%q", e.Class, e.Kind)
			}
		}
	}
	if !foundModels {
		t.Fatal("candidateAssembly.models missing from inventory")
	}
}

func TestOwnershipInventory_MixedAggregatesAreNotResourceClasses(t *testing.T) {
	t.Parallel()
	builtIdx := map[string]ownershipEntry{}
	for _, e := range builtFieldOwnership {
		builtIdx[e.Symbol] = e
	}
	if _, ok := builtIdx["Closers"]; ok {
		t.Fatal("candidateAssembly.Closers must be removed from inventory after Task 4.2 deletion")
	}
	ledger, ok := builtIdx["ledger"]
	if !ok {
		t.Fatal("candidateAssembly.ledger missing from inventory")
	}
	if ledger.Class != ownershipGeneration {
		t.Fatalf("candidateAssembly.ledger must be generation-owned, got class=%q kind=%q", ledger.Class, ledger.Kind)
	}

	bootIdx := map[string]ownershipEntry{}
	for _, e := range bootstrapResourceOwnership {
		bootIdx[e.Symbol] = e
	}
	ps, ok := bootIdx["Host.Process"]
	if !ok {
		t.Fatal("Host.Process missing from inventory")
	}
	if ps.Class != ownershipProcess {
		t.Fatalf("Host.Process must be process-owned, got class=%q kind=%q", ps.Class, ps.Kind)
	}
	if _, ok := bootIdx["Host.Built"]; ok {
		t.Fatal("Host.Built must be removed from inventory after Task 4.1")
	}

	cfg, ok := bootIdx["Host.Config"]
	if !ok {
		t.Fatal("Host.Config missing from inventory")
	}
	if cfg.Kind != ownershipKindMixedConfigAggregate {
		t.Fatalf("Host.Config must be mixed_config_aggregate (mutable *config.Config mixes process-topology and generation settings; unsuitable for generation publication), got class=%q kind=%q", cfg.Class, cfg.Kind)
	}
	if cfg.Class.valid() {
		t.Fatalf("Host.Config must not carry a resource class, got %q", cfg.Class)
	}

	compIdx := map[string]ownershipEntry{}
	for _, e := range compositionResourceOwnership {
		compIdx[e.Symbol] = e
	}
	if _, ok := compIdx["stdhttp.cleanupClosure"]; ok {
		t.Fatal("stdhttp.cleanupClosure must be removed from inventory after Task 4.2 deleted NewStandardHandler/RunWithRuntime")
	}

	all := append(append(append([]ownershipEntry{}, builtFieldOwnership...), bootstrapResourceOwnership...), compositionResourceOwnership...)
	for _, e := range all {
		if e.Kind != "" {
			if !e.Kind.validMixed() {
				t.Fatalf("%s: unknown mixed kind %q", e.Symbol, e.Kind)
			}
			if e.Class.valid() {
				t.Fatalf("%s: mixed entry must have no resource class, got %q", e.Symbol, e.Class)
			}
			continue
		}
		if !e.Class.valid() {
			t.Fatalf("%s: resource entry must have exactly one valid ownership class, got %q", e.Symbol, e.Class)
		}
	}

	readinessGroup, ok := builtIdx["operations"]
	if !ok {
		t.Fatal("candidateAssembly.operations missing")
	}
	if readinessGroup.Kind != ownershipKindMixedAggregate {
		t.Fatalf("operations group must be mixed_aggregate (includes generation readiness projection), got class=%q kind=%q", readinessGroup.Class, readinessGroup.Kind)
	}
}

func TestOwnershipInventory_RequiredNestedConstructionSites(t *testing.T) {
	t.Parallel()
	required := []string{
		"extension.State:corestate.NewMem",
		"secureSession.Generator:app.NewRandGenerator",
		"secureSession.Lineage:b2bualineage.New",
		"secureSession.Manager",
		"secureSession.Recorder:app.NewRecorder",
		"secureSession.EphemeralFingerprintKey",
		"tracing.Exporter",
		"tracing.Provider",
		"tracing.Propagator",
		"tracing.ShutdownLifecycle",
		"terminalWork.IntentService:terminalworkapp.NewIntentService",
		"metering.nestedStores",
		"accounting.nestedStores",
	}
	index := map[string]ownershipEntry{}
	for _, e := range append(append([]ownershipEntry{}, bootstrapResourceOwnership...), compositionResourceOwnership...) {
		index[e.Symbol] = e
	}
	var missing []string
	for _, sym := range required {
		e, ok := index[sym]
		if !ok {
			missing = append(missing, sym)
			continue
		}
		if e.ConstructorID == "" {
			missing = append(missing, sym+" (empty ConstructorID)")
		}
		if !e.Class.valid() && e.Kind == "" {
			missing = append(missing, sym+" (no class/kind)")
		}
	}
	if len(missing) != 0 {
		t.Fatalf("required nested construction sites missing/incomplete: %v", missing)
	}
}

func TestOwnershipInventory_ProcessOwnedContinuityServices(t *testing.T) {
	t.Parallel()
	wantProcess := []string{
		"executor.ALegLifecycle",
		"executor.AffinityStore",
		"executor.CandidateHealth",
		"stdhttp.http.Server",
		"metrics.Registry",
		"terminalWork.store",
	}
	index := map[string]ownershipClass{}
	for _, e := range append(append([]ownershipEntry{}, bootstrapResourceOwnership...), compositionResourceOwnership...) {
		index[e.Symbol] = e.Class
	}
	for _, sym := range wantProcess {
		if index[sym] != ownershipProcess {
			t.Fatalf("%s must be process-owned, got %q", sym, index[sym])
		}
	}
}

func assertOwnershipEntry(t *testing.T, label string, e ownershipEntry, seen map[string]bool) {
	t.Helper()
	if e.Symbol == "" || e.Source == "" {
		t.Fatalf("%s incomplete entry: %+v", label, e)
	}
	if e.Kind != "" {
		if !e.Kind.validMixed() {
			t.Fatalf("%s[%q]: invalid kind %q (only intentional mixed kinds allowed)", label, e.Symbol, e.Kind)
		}
		if e.Class.valid() {
			t.Fatalf("%s[%q]: mixed kind must not carry resource class %q", label, e.Symbol, e.Class)
		}
	} else if !e.Class.valid() {
		t.Fatalf("%s[%q]: invalid class %q", label, e.Symbol, e.Class)
	}
	if seen[e.Symbol] {
		t.Fatalf("%s duplicate symbol %q", label, e.Symbol)
	}
	seen[e.Symbol] = true
}

func TestOwnershipInventory_ConstructorIDDriftCheck(t *testing.T) {
	t.Parallel()
	// One-to-one drift check: audited nested construction sites must appear as
	// exact call selectors in the named production sources.
	checks := []struct {
		Symbol        string
		File          string
		ConstructorID string
	}{
		{"extension.State:corestate.NewMem", "shared_mutable.go", "corestate.NewMem"},
		{"secureSession.Generator:app.NewRandGenerator", "secure_session.go", "app.NewRandGenerator"},
		{"secureSession.Lineage:b2bualineage.New", "secure_session.go", "b2bualineage.New"},
		{"secureSession.Manager", "secure_session.go", "app.NewManager"},
		{"secureSession.Recorder:app.NewRecorder", "secure_session.go", "app.NewRecorder"},
		{"terminalWork.IntentService:terminalworkapp.NewIntentService", "terminal_work.go", "terminalworkapp.NewIntentService"},
		{"tracing.Exporter", "../tracing/tracing.go", "otlptracehttp.New"},
		{"tracing.Provider", "../tracing/tracing.go", "sdktrace.NewTracerProvider"},
	}
	index := map[string]ownershipEntry{}
	for _, e := range append(append([]ownershipEntry{}, bootstrapResourceOwnership...), compositionResourceOwnership...) {
		index[e.Symbol] = e
	}
	for _, c := range checks {
		e, ok := index[c.Symbol]
		if !ok {
			t.Fatalf("missing inventory entry %s", c.Symbol)
		}
		if e.ConstructorID == "" || !strings.Contains(e.ConstructorID, strings.Split(c.ConstructorID, ".")[len(strings.Split(c.ConstructorID, "."))-1]) && e.ConstructorID != c.ConstructorID {
			// Require ConstructorID to equal or contain the audited constructor selector.
			if e.ConstructorID != c.ConstructorID && !strings.Contains(e.ConstructorID, c.ConstructorID) {
				t.Fatalf("%s ConstructorID=%q want contain %q", c.Symbol, e.ConstructorID, c.ConstructorID)
			}
		}
		src, err := os.ReadFile(c.File)
		if err != nil {
			t.Fatalf("read %s: %v", c.File, err)
		}
		if !strings.Contains(string(src), c.ConstructorID) {
			t.Fatalf("constructor drift: %s not found in %s", c.ConstructorID, c.File)
		}
	}
}

// builtStructFieldNames returns candidateAssembly's ownership-relevant field
// names. Internal lifecycle-bookkeeping fields (sync primitives / transfer
// flags) are excluded because they are not resources requiring classification.
func builtStructFieldNames(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("candidate_http.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read candidate_http.go: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse candidate_http.go: %v", err)
	}
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "candidateAssembly" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				t.Fatal("candidateAssembly is not a struct")
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if candidateAssemblyInternalFields[name.Name] {
						continue
					}
					names = append(names, name.Name)
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no candidateAssembly fields found")
	}
	return names
}

package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CoreOwnershipCategory classifies why a top-level internal/core package may
// remain in the kernel after feature-ownership full closure (Req 12.3/13.2).
type CoreOwnershipCategory string

const (
	// CoreOwnershipKernelInvariant is required for correct base proxy behavior
	// with all optional standard features disabled, or is a universal invariant.
	CoreOwnershipKernelInvariant CoreOwnershipCategory = "kernel invariant"
	// CoreOwnershipGenericExtension is a feature-neutral extension or
	// orchestration mechanism with recorded independent consumers.
	CoreOwnershipGenericExtension CoreOwnershipCategory = "generic extension mechanism"
)

// CoreOwnershipEntry justifies one surviving top-level internal/core package.
// Consumers is required for generic extension mechanisms and names at least
// two independent consumers/producers or a universal invariant.
type CoreOwnershipEntry struct {
	Package   string
	Category  CoreOwnershipCategory
	Reason    string
	Consumers string
}

// CoreOwnershipManifest is the durable core admission table (Task 11.1). A new
// top-level internal/core package fails the admission test until it gains an
// entry here through explicit architecture review.
var CoreOwnershipManifest = []CoreOwnershipEntry{
	{Package: "accessmode", Category: CoreOwnershipKernelInvariant, Reason: "Base proxy execution-mode vocabulary independent of features."},
	{Package: "accounting", Category: CoreOwnershipKernelInvariant, Reason: "Generic token accounting types and ledger contracts."},
	{Package: "admin", Category: CoreOwnershipKernelInvariant, Reason: "Minimal admin command port definitions."},
	{Package: "affinity", Category: CoreOwnershipKernelInvariant, Reason: "Backend connection affinity keys and routing selectors."},
	{Package: "auth", Category: CoreOwnershipKernelInvariant, Reason: "Client authentication, credential verification, principal mapping."},
	{Package: "authorityattribution", Category: CoreOwnershipKernelInvariant, Reason: "Attribution metadata for usage and concurrency leases."},
	{Package: "authoritycoord", Category: CoreOwnershipKernelInvariant, Reason: "Distributed lease and quota coordination across nodes."},
	{Package: "auxreq", Category: CoreOwnershipKernelInvariant, Reason: "Auxiliary request lifecycle, sub-request pipeline, cancellation."},
	{Package: "b2bua", Category: CoreOwnershipKernelInvariant, Reason: "Back-to-back user agent state machine, attempt loop, upstream I/O."},
	{Package: "billingport", Category: CoreOwnershipGenericExtension, Reason: "Logic-free payloads for optional money ports; no validation or financial decisions.", Consumers: "runtime terminal/exposure ports via billing forwarders, billing admission and persistence"},
	{Package: "capabilities", Category: CoreOwnershipKernelInvariant, Reason: "Model capability bitflags consumed by routing and backends."},
	{Package: "concurrencyauthority", Category: CoreOwnershipKernelInvariant, Reason: "Concurrency lease allocation and enforcement."},
	{Package: "config", Category: CoreOwnershipKernelInvariant, Reason: "Typed base proxy configuration; feature payloads stay opaque subtrees."},
	{Package: "configreload", Category: CoreOwnershipKernelInvariant, Reason: "Dynamic configuration reload and generation swap coordination."},
	{Package: "continuation", Category: CoreOwnershipKernelInvariant, Reason: "Request continuation token domain types."},
	{Package: "continuity", Category: CoreOwnershipKernelInvariant, Reason: "Session continuity contracts and replay markers."},
	{Package: "controlplane", Category: CoreOwnershipKernelInvariant, Reason: "Control-plane state management and cluster observers."},
	{Package: "conversationprojection", Category: CoreOwnershipKernelInvariant, Reason: "Pure backend-effective projection, never_backend exclusion, anchors."},
	{Package: "diag", Category: CoreOwnershipKernelInvariant, Reason: "Diagnostic telemetry, trace attributes, logging context."},
	{Package: "execbackend", Category: CoreOwnershipKernelInvariant, Reason: "Backend executor abstraction and invocation port."},
	{Package: "execctx", Category: CoreOwnershipKernelInvariant, Reason: "Execution context: deadlines, trace identity, cancellation."},
	{Package: "extensions", Category: CoreOwnershipGenericExtension, Reason: "Closed standard extension planes and completion gates.", Consumers: "runtime, traffic, diag, testkit conformance"},
	{Package: "geoip", Category: CoreOwnershipKernelInvariant, Reason: "Client IP geolocation lookup for regional policy routing."},
	{Package: "hooks", Category: CoreOwnershipGenericExtension, Reason: "Extension hook pipeline execution and phase ordering.", Consumers: "extensions, runtime, testkit conformance"},
	{Package: "http", Category: CoreOwnershipKernelInvariant, Reason: "HTTP protocol utilities, header normalization, status mapping."},
	{Package: "identity", Category: CoreOwnershipKernelInvariant, Reason: "Tenant, organization, user identity representation."},
	{Package: "ingressdefense", Category: CoreOwnershipKernelInvariant, Reason: "Default provider-neutral ingress self-defense policy and bounded exact-address hostile source state, required with optional features absent."},
	{Package: "interleavedstate", Category: CoreOwnershipKernelInvariant, Reason: "Routing-required thinker cycle state for route selection and continuity."},
	{Package: "jsonpresence", Category: CoreOwnershipKernelInvariant, Reason: "JSON empty-vs-null presence semantics."},
	{Package: "jsonshape", Category: CoreOwnershipKernelInvariant, Reason: "Structural JSON validation for streams and frontends."},
	{Package: "largebody", Category: CoreOwnershipGenericExtension, Reason: "Provider-neutral large-payload wire DTOs shared by frontend proof, core assessment, and backend wire open.", Consumers: "frontend profiles, core assessment, backend wire open"},
	{Package: "leglifecycle", Category: CoreOwnershipKernelInvariant, Reason: "Upstream attempt leg lifecycle tracking and abort handling."},
	{Package: "lineage", Category: CoreOwnershipKernelInvariant, Reason: "Request/response message causal lineage tracking."},
	{Package: "localstream", Category: CoreOwnershipKernelInvariant, Reason: "In-memory canonical stream for local execution loops."},
	{Package: "metering", Category: CoreOwnershipKernelInvariant, Reason: "Raw usage event metering ports and aggregation."},
	{Package: "modelcatalog", Category: CoreOwnershipKernelInvariant, Reason: "Static model definitions, tokenizer mappings, context limits."},
	{Package: "modelregistry", Category: CoreOwnershipKernelInvariant, Reason: "Dynamic runtime model registration and lookup."},
	{Package: "modelview", Category: CoreOwnershipKernelInvariant, Reason: "Filtered model visibility views for tenants."},
	{Package: "policy", Category: CoreOwnershipKernelInvariant, Reason: "Rate limit and tier policy definitions."},
	{Package: "routeoverride", Category: CoreOwnershipKernelInvariant, Reason: "Operator A-leg routing-override rules and latest-wins state."},
	{Package: "routing", Category: CoreOwnershipKernelInvariant, Reason: "Backend selection, fallback sequences, weighted health routing."},
	{Package: "runtime", Category: CoreOwnershipKernelInvariant, Reason: "Request/attempt orchestration, output commitment, immutable generations."},
	{Package: "safety", Category: CoreOwnershipKernelInvariant, Reason: "Prompt safety boundary assertions and stream guard invariants."},
	{Package: "securesession", Category: CoreOwnershipKernelInvariant, Reason: "Secure-session authority: creation, verification, rotation, stores."},
	{Package: "snapshotgen", Category: CoreOwnershipKernelInvariant, Reason: "Immutable runtime state snapshot generation."},
	{Package: "state", Category: CoreOwnershipKernelInvariant, Reason: "Core state container and epoch tracking."},
	{Package: "stream", Category: CoreOwnershipKernelInvariant, Reason: "Canonical streaming abstractions, backpressure, chunk multiplexing."},
	{Package: "streamrecovery", Category: CoreOwnershipKernelInvariant, Reason: "Mid-stream disconnect recovery and idempotent retry markers."},
	{Package: "terminal", Category: CoreOwnershipKernelInvariant, Reason: "Final generation completion state evaluation."},
	{Package: "terminalwork", Category: CoreOwnershipKernelInvariant, Reason: "Post-response async terminal work pipeline."},
	{Package: "tokenaccounting", Category: CoreOwnershipKernelInvariant, Reason: "Token counting, budget enforcement, ledger, preflight."},
	{Package: "traffic", Category: CoreOwnershipKernelInvariant, Reason: "Traffic shaping and admission tokens."},
	{Package: "usageevidence", Category: CoreOwnershipKernelInvariant, Reason: "Immutable usage records, evidence and correlation identities required without optional billing; no pricing or monetary decisions."},
	{Package: "usagelimit", Category: CoreOwnershipGenericExtension, Reason: "Usage-limit protocol, value contracts and neutral default without rule enforcement.", Consumers: "runtime admission/completion through the usage-limits port; the usage-authority implementation lives in the Enterprise tree since task 6.1 Slice B"},
	{Package: "workspace", Category: CoreOwnershipKernelInvariant, Reason: "Workspace isolation boundary contracts."},
}

func coreOwnershipByPackage() map[string]CoreOwnershipEntry {
	out := make(map[string]CoreOwnershipEntry, len(CoreOwnershipManifest))
	for _, e := range CoreOwnershipManifest {
		out[e.Package] = e
	}
	return out
}

// dirHasProductionGo reports whether dir holds any production (non-test) Go
// file anywhere beneath it, skipping testdata subtrees. Admission is
// recursive: production code in a nested directory still requires a
// manifest entry for the top-level package.
func dirHasProductionGo(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			found = true
		}
		return nil
	})
	return found
}

// missingCoreOwnershipEntries reports top-level core packages holding
// production code without a manifest entry. coreDir is the live
// internal/core directory or a synthetic fixture root with the same layout.
func missingCoreOwnershipEntries(coreDir string, byPackage map[string]CoreOwnershipEntry) []string {
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		return []string{"ReadDir " + coreDir + ": " + err.Error()}
	}
	var missing []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		if !dirHasProductionGo(filepath.Join(coreDir, e.Name())) {
			continue
		}
		if _, ok := byPackage[e.Name()]; !ok {
			missing = append(missing, "internal/core/"+e.Name())
		}
	}
	sort.Strings(missing)
	return missing
}

// staleCoreOwnershipEntries reports manifest entries whose top-level
// directory exists but holds zero production Go: the package was hollowed
// out and the entry must be removed or re-justified. Entries without a
// matching directory are reported by validateCoreOwnershipEntries instead.
func staleCoreOwnershipEntries(root string, manifest []CoreOwnershipEntry) []string {
	var stale []string
	for _, e := range manifest {
		dir := filepath.Join(root, "internal", "core", e.Package)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if !dirHasProductionGo(dir) {
			stale = append(stale, e.Package)
		}
	}
	sort.Strings(stale)
	return stale
}

// validateCoreOwnershipEntries locks the manifest shape: known categories
// only, concise reasons, generic mechanisms carry independent consumer
// evidence, entries match live top-level directories, and no stale entries
// for packages without production code.
func validateCoreOwnershipEntries(root string, manifest []CoreOwnershipEntry) []string {
	var problems []string
	seen := map[string]bool{}
	for _, e := range manifest {
		if e.Package == "" {
			problems = append(problems, "manifest entry with empty package name")
		}
		if seen[e.Package] {
			problems = append(problems, "duplicate manifest entry for core package "+strconv.Quote(e.Package))
		}
		seen[e.Package] = true
		switch e.Category {
		case CoreOwnershipKernelInvariant, CoreOwnershipGenericExtension:
		default:
			problems = append(problems, "package "+strconv.Quote(e.Package)+" has unknown ownership category "+strconv.Quote(string(e.Category)))
		}
		if strings.TrimSpace(e.Reason) == "" {
			problems = append(problems, "package "+strconv.Quote(e.Package)+" has empty ownership reason")
		}
		if e.Category == CoreOwnershipGenericExtension && strings.TrimSpace(e.Consumers) == "" {
			problems = append(problems, "generic extension mechanism "+strconv.Quote(e.Package)+" must record independent consumer evidence")
		}
		dir := filepath.Join(root, "internal", "core", e.Package)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			problems = append(problems, "manifest entry "+strconv.Quote(e.Package)+" does not match a top-level internal/core directory")
		}
	}
	for _, stale := range staleCoreOwnershipEntries(root, manifest) {
		problems = append(problems, "stale manifest entry "+strconv.Quote(stale)+" has no production Go anywhere beneath internal/core/"+stale)
	}
	return problems
}

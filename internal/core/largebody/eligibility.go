package largebody

import (
	"fmt"
	"strings"
)

// Generation-frozen wire eligibility (Task 3.5; Requirements 5, 6, 22;
// design section 7).
//
// WireEligibilitySummary is the bounded, deterministic, generation-pinned
// compilation of every production authority that can invalidate raw
// forwarding: typed extension planes (Tasks 3.1/3.2, evidence 1.9),
// hook-bus occupancy (Task 3.3), and narrow/non-plane ports (Task 3.4,
// evidence 1.8). Composition builds it once per generation; the request path
// only reads it (Task 3.6 static disposition, Task 11.3 assessment re-check).
//
// The summary carries fixed bitsets/enums plus one bounded generation
// binding. It carries no request-sized data: no body bytes, no prompt text,
// no spool paths, no maps, no slices, no plugin values, no aipapi.Call.
// Unknown input fails closed: the compiler rejects it and the zero summary
// reports a static blocker.
//
// Leaf-pure: stdlib only, no I/O, no goroutines, no reflection, no time,
// no context. Deterministic across input order.

const (
	// maxEligibilityIDChars bounds unfamiliar IDs echoed in diagnostics so
	// error text stays bounded (Requirement 22.3).
	maxEligibilityIDChars = 64
)

// PlaneAccess mirrors pkg/aipsdk/feature.RequestBodyAccess without importing
// the plane registry: the compiler stays leaf-pure and the composition
// mapper converts explicitly. Numeric values and static labels match exactly;
// TestWireEligibility_PlaneAccessParityWithFeature pins the parity.
type PlaneAccess uint8

const (
	// PlaneAccessUnclassified is the zero value: no wire classification.
	// It fails compilation so a new or unannotated plane fails closed.
	PlaneAccessUnclassified PlaneAccess = iota
	// PlaneAccessCanonicalRequired marks planes receiving or mutating the
	// full canonical request: occupied blocks the wire lane in V1.
	PlaneAccessCanonicalRequired
	// PlaneAccessMetadataOnly marks planes carrying only bounded
	// session/workspace/int metadata.
	PlaneAccessMetadataOnly
	// PlaneAccessResponseOnly marks planes operating on canonical output
	// events only.
	PlaneAccessResponseOnly
	// PlaneAccessWireContract marks planes carrying an explicit certified
	// wire contract. None exists in V1; the value is accepted so a future
	// certified contract does not read as unknown.
	PlaneAccessWireContract
)

// String returns a bounded static label for metrics/diagnostics.
func (a PlaneAccess) String() string {
	switch a {
	case PlaneAccessUnclassified:
		return "unclassified"
	case PlaneAccessCanonicalRequired:
		return "canonical_required"
	case PlaneAccessMetadataOnly:
		return "metadata_only"
	case PlaneAccessResponseOnly:
		return "response_only"
	case PlaneAccessWireContract:
		return "wire_contract"
	default:
		return fmt.Sprintf("PlaneAccess(%d)", uint8(a))
	}
}

// WireEligibilityPlaneID resolves a fixed plane index to its stable ID.
func WireEligibilityPlaneID(index int) (string, bool) {
	if index < 0 || index >= WireEligibilityPlaneCount {
		return "", false
	}
	return wireEligibilityPlaneOrder[index], true
}

// WireEligibilityPlaneIndex resolves a stable plane ID to its fixed index.
// Unknown IDs fail closed (ok=false); composition-time linear scan only.
func WireEligibilityPlaneIndex(id string) (int, bool) {
	for i, known := range wireEligibilityPlaneOrder {
		if known == id {
			return i, true
		}
	}
	return 0, false
}

// HookChain is a fixed bitset over the four hook-bus chains (Task 3.3).
// The bus owns exactly these chains, separate from the plane manifest.
type HookChain uint8

const (
	// HookChainSubmit names the submit chain (canonical request pre-route).
	HookChainSubmit HookChain = 1 << iota
	// HookChainRequestPart names the request-part chain (request mutation).
	HookChainRequestPart
	// HookChainResponsePart names the response-part chain (events only).
	HookChainResponsePart
	// HookChainTool names the tool-reactor chain (tool lifecycle).
	HookChainTool
)

// WirePortBlocker is a fixed bitset over V1 static narrow-port blockers
// (Task 3.4). Each bit names one unconditional composition-time blocker;
// present-but-model-dependent backend facts stay dynamic (Task 11)
// and never set a bit here.
type WirePortBlocker uint32

const (
	// WirePortBackendsEmpty names an empty backend map: no candidate exists
	// for any request (1.8 section 3.1; 3.4 core.backends nil state).
	WirePortBackendsEmpty WirePortBlocker = 1 << iota
	// WirePortConversationViewReader names occupied trajectory reads without
	// a source-backed contract (3.4 core.conversation_view_reader).
	WirePortConversationViewReader
	// WirePortConversationViewTagger names occupied local-turn tagging with
	// content-shaped identity derivation (3.4 core.conversation_view_tagger).
	WirePortConversationViewTagger
	// WirePortSteeringWriterFactory names a Call-returning steering resolver
	// (3.4 core.steering_writer_factory).
	WirePortSteeringWriterFactory
	// WirePortExposureAdmission names the Call-embedding exposure input
	// pending the Task 10.5 bounded-facts refactor (3.4 billing).
	WirePortExposureAdmission
	// WirePortBillingIdentityCallbacks names custom func(ctx, Call) billing
	// resolvers without a bounded fact contract (3.4 billing, req 15.6).
	WirePortBillingIdentityCallbacks
	// WirePortCapsResolver names an occupied full-Call capability resolver
	// (3.4 routing; Task 12.2).
	WirePortCapsResolver
	// WirePortCatalogResolver names an occupied full-Call catalog resolver
	// (3.4 routing; Task 12.2).
	WirePortCatalogResolver
	// WirePortEligibilityResolver names an occupied full-Call eligibility
	// check (3.4 routing; Task 12.2).
	WirePortEligibilityResolver
	// WirePortRequestTokenEstimator names an occupied full-Call token
	// estimator without an exact source contract (3.4 routing; Task 10.4).
	WirePortRequestTokenEstimator
	// WirePortPreflightCountOnly names enabled CountCall-only preflight
	// without an exact wire counter (3.4 accounting; Task 10.4).
	WirePortPreflightCountOnly
	// WirePortStreamUsage names occupied stream usage with a Call-backed
	// input-count leg (3.4 accounting).
	WirePortStreamUsage
	// WirePortAdminCountService names occupied token-dependent counting
	// (3.4 accounting; Tasks 10.4/12.2).
	WirePortAdminCountService
	// WirePortInterleavedProcessor names occupied thinker/executor shaping
	// or continuation re-projection (3.4 interleaved; Task 12.3).
	WirePortInterleavedProcessor
	// WirePortCompactionDetector names the Call-shaped RequestOpened leg
	// (3.4 compaction; Task 12.3).
	WirePortCompactionDetector
	// WirePortTrafficCapturing names a non-no-op traffic bundle: capturing,
	// observing, or redacting legs (3.4 traffic; req 13.1).
	WirePortTrafficCapturing
	// WirePortTokenCounting names required input-token counting without an
	// exact wire counter; body bytes are never tokens (3.4 counting).
	WirePortTokenCounting
	// WirePortCustomCallCallbacks names any other Call-shaped callback
	// without an explicit bounded fact contract (3.4 custom; req 5.6/5.12).
	WirePortCustomCallCallbacks
	// WirePortTwoPhaseExecutorMissing names an executor without the internal
	// two-phase capability (design section 4).
	WirePortTwoPhaseExecutorMissing
)

// PlaneEligibilityInput is one frozen plane fact: stable ID, V1 access
// class, and generation occupancy. The slice must cover exactly the 28 known
// planes in any order; unknown, duplicate, missing, or unclassified entries
// fail compilation.
type PlaneEligibilityInput struct {
	ID       string
	Access   PlaneAccess
	Occupied bool
}

// HookEligibilityInput is the frozen hook-bus occupancy: one bit per chain.
// Every occupied chain (submit, request-part, response-part, tool) blocks
// without a wire contract: response hooks are bypassed by wire streaming,
// so under the Blocker 2 conservative fail-safe the occupied response-only
// chain also blocks statically (Task 3.3).
type HookEligibilityInput struct {
	SubmitOccupied       bool
	RequestPartOccupied  bool
	ResponsePartOccupied bool
	ToolOccupied         bool
}

// NarrowPortEligibilityInput is the frozen narrow-port occupancy/mode record
// (Task 3.4). Fields that can never block (bounded knobs, observational
// sinks, nil-safe coordinators) are omitted: they contribute no eligibility
// fact. Present-but-model-dependent backend compatibility stays dynamic and
// is not represented here.
type NarrowPortEligibilityInput struct {
	BackendsEmpty, ConversationViewReaderOccupied, ConversationReaderFreshALegSupported               bool
	ConversationViewTaggerOccupied, SteeringWriterFactoryOccupied, ExposureAdmissionOccupied          bool
	BillingIdentityCustomCallbacks, CapsResolverOccupied, CapsResolverWireProofSubsumed               bool
	CatalogResolverOccupied, EligibilityResolverOccupied, RequestTokenEstimatorOccupied               bool
	PreflightEnabled, PreflightHasExactCounter, StreamUsageOccupied, AdminCountServiceOccupied        bool
	InterleavedProcessorOccupied, CompactionDetectorOccupied, CompactionDetectorWireSupported         bool
	TrafficCapturing, TokenCountingRequired, TokenCountingHasExactCounter, CustomCallCallbacksPresent bool
}

// WireEligibilityInput is the complete frozen generation fact set compiled
// once at composition time. It carries bounded static facts only: stable
// plane IDs, access enums, occupancy bools, and a bounded generation
// binding. No request data can be expressed in it.
type WireEligibilityInput struct {
	GenerationID              string
	Planes                    []PlaneEligibilityInput
	Hooks                     HookEligibilityInput
	Ports                     NarrowPortEligibilityInput
	TwoPhaseExecutorAvailable bool
}

// WireEligibilitySummary is the published bounded summary. The zero value is
// fail-closed: unsealed and reporting a static blocker. Only the compiler
// produces sealed values; fields are unexported so published summaries are
// immutable and cannot alias caller memory.
type WireEligibilitySummary struct {
	generationID  string
	sealed        bool
	planeBlockers uint32
	hookOccupancy HookChain
	hookBlockers  HookChain
	portBlockers  WirePortBlocker
}

// GenerationID returns the bound generation identity (Requirement 6.7).
func (s WireEligibilitySummary) GenerationID() string { return s.generationID }

// Sealed reports whether the summary is a complete valid compilation.
// Only sealed summaries are usable; the zero value is unsealed.
func (s WireEligibilitySummary) Sealed() bool { return s.sealed }

// HasStaticBlocker reports whether the generation is definitely canonical:
// unsealed summaries and any recorded plane/hook/port blocker block.
// Allocation-free; suitable for the Task 3.6 hot projection.
func (s WireEligibilitySummary) HasStaticBlocker() bool {
	return !s.sealed || s.planeBlockers != 0 || s.hookBlockers != 0 || s.portBlockers != 0
}

// PinnedTo reports whether the summary is sealed and bound to the given
// generation. A reload publishes a new generation ID, so a mismatch detects
// staleness without reading any other generation state.
func (s WireEligibilitySummary) PinnedTo(generationID string) bool {
	return s.sealed && s.generationID != "" && s.generationID == generationID
}

// PlaneBlockers returns the fixed plane blocker mask: bit i (see
// WireEligibilityPlaneID) names an occupied canonical-required plane.
func (s WireEligibilitySummary) PlaneBlockers() uint32 { return s.planeBlockers }

// HookOccupancy returns the frozen hook-bus occupancy bits.
func (s WireEligibilitySummary) HookOccupancy() HookChain { return s.hookOccupancy }

// HookBlockers returns the occupied blocking-chain subset: every occupied
// chain blocks, including the response-only chain (Blocker 2).
func (s WireEligibilitySummary) HookBlockers() HookChain { return s.hookBlockers }

// PortBlockers returns the fixed narrow-port blocker mask.
func (s WireEligibilitySummary) PortBlockers() WirePortBlocker { return s.portBlockers }

// Validate enforces the generation binding under the semantic-fact budget.
func (s WireEligibilitySummary) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if !s.sealed {
		return fmt.Errorf("largebody: wire eligibility summary is not sealed")
	}
	if strings.TrimSpace(s.generationID) == "" {
		return fmt.Errorf("largebody: wire eligibility generation binding must not be empty")
	}
	if int64(len(s.generationID)) > maxFactBytes {
		return fmt.Errorf("largebody: wire eligibility generation binding exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// String renders bounded static labels only: generation binding plus fixed
// masks. No backend/model/user/session content can appear.
func (s WireEligibilitySummary) String() string {
	return fmt.Sprintf("WireEligibilitySummary{gen:%q sealed:%t plane_blockers:%#x hook_occupancy:%#x hook_blockers:%#x port_blockers:%#x}",
		s.generationID, s.sealed, s.planeBlockers, uint8(s.hookOccupancy), uint8(s.hookBlockers), uint32(s.portBlockers))
}

// CompileWireEligibilitySummary compiles frozen generation facts into a
// sealed summary. It is deterministic across plane input order, copies all
// inputs (published summaries never alias caller memory), and fails closed:
// malformed input returns an error with an unsealed blocking summary.
func CompileWireEligibilitySummary(in WireEligibilityInput, maxFactBytes int64) (WireEligibilitySummary, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return WireEligibilitySummary{}, err
	}
	if strings.TrimSpace(in.GenerationID) == "" {
		return WireEligibilitySummary{}, fmt.Errorf("largebody: wire eligibility generation binding must not be empty")
	}
	if int64(len(in.GenerationID)) > maxFactBytes {
		return WireEligibilitySummary{}, fmt.Errorf("largebody: wire eligibility generation binding exceeds %d bytes", maxFactBytes)
	}
	planeBlockers, err := compilePlaneBlockers(in.Planes)
	if err != nil {
		return WireEligibilitySummary{}, err
	}
	hookOccupancy, hookBlockers := compileHookChains(in.Hooks)
	return WireEligibilitySummary{
		generationID:  in.GenerationID,
		sealed:        true,
		planeBlockers: planeBlockers,
		hookOccupancy: hookOccupancy,
		hookBlockers:  hookBlockers,
		portBlockers:  compilePortBlockers(in.Ports, in.TwoPhaseExecutorAvailable),
	}, nil
}

// compilePlaneBlockers maps plane facts onto the fixed census. Unknown,
// duplicate, missing, unclassified, or out-of-range entries fail closed;
// occupied canonical-required planes set their fixed bit.
func compilePlaneBlockers(planes []PlaneEligibilityInput) (uint32, error) {
	if len(planes) != WireEligibilityPlaneCount {
		return 0, fmt.Errorf("largebody: wire eligibility needs %d planes, got %d",
			WireEligibilityPlaneCount, len(planes))
	}
	var seen [WireEligibilityPlaneCount]bool
	var blockers uint32
	for _, p := range planes {
		idx, ok := WireEligibilityPlaneIndex(p.ID)
		if !ok {
			return 0, fmt.Errorf("largebody: wire eligibility unknown plane %q", truncateEligibilityID(p.ID))
		}
		if seen[idx] {
			return 0, fmt.Errorf("largebody: wire eligibility duplicate plane %q", truncateEligibilityID(p.ID))
		}
		seen[idx] = true
		switch p.Access {
		case PlaneAccessUnclassified:
			return 0, fmt.Errorf("largebody: wire eligibility plane %q is unclassified", truncateEligibilityID(p.ID))
		case PlaneAccessCanonicalRequired, PlaneAccessResponseOnly:
			// Blocker 2: Response-only planes (completion_gates, stream_observer_factories,
			// usage_observers, response_part_hooks) are bypassed by wire streaming.
			// Conservative fail-safe: any occupied canonical-required or response-only plane
			// statically blocks wire eligibility until deferred pipeline reuse is implemented.
			if p.Occupied {
				blockers |= 1 << uint(idx)
			}
		case PlaneAccessMetadataOnly, PlaneAccessWireContract:
			// Task 12.4: Local Turn and Secret Guard occupied planes are non-negotiable static
			// canonical blockers in V1 (Requirements 5.4, 13.4, 19.4). Attempting to weaken their access
			// while occupied must never bypass the static blocker bit.
			if p.Occupied && isV1NonNegotiableCanonicalPlane(p.ID) {
				blockers |= 1 << uint(idx)
			}
			// Occupied metadata-only and explicitly wire-contracted planes stay eligible
			// for dynamic assessment; they never block statically.
		default:
			return 0, fmt.Errorf("largebody: wire eligibility plane %q has unknown access %d",
				truncateEligibilityID(p.ID), uint8(p.Access))
		}
	}
	for idx, ok := range seen {
		if !ok {
			id, _ := WireEligibilityPlaneID(idx)
			return 0, fmt.Errorf("largebody: wire eligibility missing plane %q", id)
		}
	}
	return blockers, nil
}

// isV1NonNegotiableCanonicalPlane reports whether plane id names a non-negotiable
// canonical plane in V1 (Requirements 5.4, 13.4, 13.5, 19.4; Tasks 12.4, 12.5).
func isV1NonNegotiableCanonicalPlane(id string) bool {
	return id == "local_turn_handlers" || id == "secret_guards" || id == "secret_guard_execution" ||
		id == "terminal_decision_provider" || id == "control_tool_provider"
}

// compileHookChains records frozen bus occupancy and the occupied
// mutating-chain subset. Under Blocker 2 conservative fail-safe, the
// response-only chain also blocks when occupied because wire execution
// bypasses response hooks.
func compileHookChains(hooks HookEligibilityInput) (occupancy, blockers HookChain) {
	if hooks.SubmitOccupied {
		occupancy |= HookChainSubmit
		blockers |= HookChainSubmit
	}
	if hooks.RequestPartOccupied {
		occupancy |= HookChainRequestPart
		blockers |= HookChainRequestPart
	}
	if hooks.ResponsePartOccupied {
		occupancy |= HookChainResponsePart
		// Blocker 2: Response part hooks are bypassed during wire streaming.
		// Conservative fail-safe: occupied response part hooks block wire fast-path.
		blockers |= HookChainResponsePart
	}
	if hooks.ToolOccupied {
		occupancy |= HookChainTool
		blockers |= HookChainTool
	}
	return occupancy, blockers
}

// compilePortBlockers applies the frozen V1 static policy (Tasks 3.2/3.4,
// design section 4): unconditional content authorities block; wire-capable
// modes (disabled preflight/counting, exact counters, present-but-dynamic
// backends) set no bit.
func compilePortBlockers(ports NarrowPortEligibilityInput, twoPhaseAvailable bool) WirePortBlocker {
	var blockers WirePortBlocker
	set := func(cond bool, bit WirePortBlocker) {
		if cond {
			blockers |= bit
		}
	}
	set(ports.BackendsEmpty, WirePortBackendsEmpty)
	set(ports.ConversationViewReaderOccupied && !ports.ConversationReaderFreshALegSupported, WirePortConversationViewReader)
	set(ports.ConversationViewTaggerOccupied, WirePortConversationViewTagger)
	set(ports.SteeringWriterFactoryOccupied, WirePortSteeringWriterFactory)
	set(ports.ExposureAdmissionOccupied, WirePortExposureAdmission)
	set(ports.BillingIdentityCustomCallbacks, WirePortBillingIdentityCallbacks)
	set(ports.CapsResolverOccupied && !ports.CapsResolverWireProofSubsumed, WirePortCapsResolver)
	set(ports.CatalogResolverOccupied, WirePortCatalogResolver)
	set(ports.EligibilityResolverOccupied, WirePortEligibilityResolver)
	set(ports.RequestTokenEstimatorOccupied, WirePortRequestTokenEstimator)
	set(ports.PreflightEnabled && !ports.PreflightHasExactCounter, WirePortPreflightCountOnly)
	set(ports.StreamUsageOccupied, WirePortStreamUsage)
	set(ports.AdminCountServiceOccupied, WirePortAdminCountService)
	set(ports.InterleavedProcessorOccupied, WirePortInterleavedProcessor)
	set(ports.CompactionDetectorOccupied && !ports.CompactionDetectorWireSupported, WirePortCompactionDetector)
	set(ports.TrafficCapturing, WirePortTrafficCapturing)
	set(ports.TokenCountingRequired && !ports.TokenCountingHasExactCounter, WirePortTokenCounting)
	set(ports.CustomCallCallbacksPresent, WirePortCustomCallCallbacks)
	set(!twoPhaseAvailable, WirePortTwoPhaseExecutorMissing)
	return blockers
}

// truncateEligibilityID bounds unfamiliar IDs echoed in diagnostics.
func truncateEligibilityID(id string) string {
	if len(id) <= maxEligibilityIDChars {
		return id
	}
	return id[:maxEligibilityIDChars] + "..."
}

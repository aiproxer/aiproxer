package largebody_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// Task 3.5 RED: bounded generation-frozen WireEligibilitySummary.
// Composition-time only, deterministic, generation-pinned (Requirements 5,
// 6, 22; design section 7). The compiler is a pure function of frozen
// generation inputs: fixed plane/hook/narrow-port facts plus a bounded
// generation binding. No I/O, no request path, no goroutines, no reflection.
//
// WireEligibilityInput.
const testEligibilityBudget = 256

func featureAccessToEligibility(a feature.RequestBodyAccess) largebody.PlaneAccess {
	switch a {
	case feature.RequestBodyAccessUnclassified:
		return largebody.PlaneAccessUnclassified
	case feature.RequestBodyCanonicalRequired:
		return largebody.PlaneAccessCanonicalRequired
	case feature.RequestBodyMetadataOnly:
		return largebody.PlaneAccessMetadataOnly
	case feature.RequestBodyResponseOnly:
		return largebody.PlaneAccessResponseOnly
	case feature.RequestBodyWireContract:
		return largebody.PlaneAccessWireContract
	default:
		return largebody.PlaneAccessUnclassified
	}
}

// census35Access pins the Task 3.1 initial truthful request-body access
// classification (pkg/aipsdk/feature/plane_request_access_test.go). The
// compiler input carries access explicitly so largebody stays leaf-pure;
// eligible35Input cross-checks every live StandardPlanes ID against this map
// so a new manifest plane fails here until classified.
var census35Access = map[string]largebody.PlaneAccess{
	"submit_hooks":                          largebody.PlaneAccessCanonicalRequired,
	"request_part_hooks":                    largebody.PlaneAccessCanonicalRequired,
	"response_part_hooks":                   largebody.PlaneAccessResponseOnly,
	"tool_reactors":                         largebody.PlaneAccessCanonicalRequired,
	"session_openers":                       largebody.PlaneAccessMetadataOnly,
	"workspace_resolvers":                   largebody.PlaneAccessMetadataOnly,
	"tool_catalog_filters":                  largebody.PlaneAccessCanonicalRequired,
	"tool_call_policies":                    largebody.PlaneAccessCanonicalRequired,
	"tool_call_finalizers":                  largebody.PlaneAccessCanonicalRequired,
	"tool_call_finalization_max_args_bytes": largebody.PlaneAccessMetadataOnly,
	"request_transforms":                    largebody.PlaneAccessCanonicalRequired,
	"pre_request_handlers":                  largebody.PlaneAccessCanonicalRequired,
	"route_hint_providers":                  largebody.PlaneAccessCanonicalRequired,
	"completion_gates":                      largebody.PlaneAccessResponseOnly,
	"attempt_transforms":                    largebody.PlaneAccessCanonicalRequired,
	"stream_observer_factories":             largebody.PlaneAccessResponseOnly,
	"traffic_observers":                     largebody.PlaneAccessCanonicalRequired,
	"usage_observers":                       largebody.PlaneAccessResponseOnly,
	"raw_capture_sinks":                     largebody.PlaneAccessCanonicalRequired,
	"traffic_redactors":                     largebody.PlaneAccessCanonicalRequired,
	"compaction_observers":                  largebody.PlaneAccessCanonicalRequired,
	"compaction_preservers":                 largebody.PlaneAccessCanonicalRequired,
	"secret_guards":                         largebody.PlaneAccessCanonicalRequired,
	"secret_guard_execution":                largebody.PlaneAccessCanonicalRequired,
	"local_turn_handlers":                   largebody.PlaneAccessCanonicalRequired,
	"terminal_decision_provider":            largebody.PlaneAccessCanonicalRequired,
	"session_classifier":                    largebody.PlaneAccessMetadataOnly,
	"control_tool_provider":                 largebody.PlaneAccessCanonicalRequired,
}

// eligible35Input builds the "normal potentially eligible generation" shape
// from the live StandardPlanes manifest (IDs) plus the pinned census access:
// every plane unoccupied, an empty hook bus, clear narrow ports, backends
// present, and a wire-capable executor. Static disposition must not reject it.
func eligible35Input(genID string) largebody.WireEligibilityInput {
	planes := make([]largebody.PlaneEligibilityInput, 0, len(feature.StandardPlanes))
	for _, decl := range feature.StandardPlanes {
		access, ok := census35Access[decl.PlaneID()]
		if !ok {
			panic("eligible35Input: manifest plane " + decl.PlaneID() + " has no compiler classification: classify it first")
		}
		planes = append(planes, largebody.PlaneEligibilityInput{
			ID:       decl.PlaneID(),
			Access:   access,
			Occupied: false,
		})
	}
	return largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}
}

func occupy35Plane(in largebody.WireEligibilityInput, id string) largebody.WireEligibilityInput {
	out := in
	out.Planes = append([]largebody.PlaneEligibilityInput(nil), in.Planes...)
	found := false
	for i := range out.Planes {
		if out.Planes[i].ID == id {
			out.Planes[i].Occupied = true
			found = true
		}
	}
	if !found {
		panic("occupy35Plane: unknown plane " + id)
	}
	return out
}

func compile35(t *testing.T, in largebody.WireEligibilityInput) largebody.WireEligibilitySummary {
	t.Helper()
	sum, err := largebody.CompileWireEligibilitySummary(in, testEligibilityBudget)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return sum
}

// TestWireEligibility_EligibleGenerationCompilesClean pins the baseline: a
// normal generation with no occupied content authorities compiles without
// error and reports no static blocker.
func TestWireEligibility_EligibleGenerationCompilesClean(t *testing.T) {
	t.Parallel()

	sum := compile35(t, eligible35Input("gen-7"))
	if !sum.Sealed() {
		t.Fatal("eligible generation summary must be sealed")
	}
	if sum.HasStaticBlocker() {
		t.Fatalf("eligible generation must report no static blocker, got %v", sum)
	}
	if err := sum.Validate(testEligibilityBudget); err != nil {
		t.Fatalf("eligible summary must validate: %v", err)
	}
}

// TestWireEligibility_DeterministicSameGenerationEquality pins composition
// determinism: identical frozen facts compile to equal summaries regardless
// of plane input order.
func TestWireEligibility_DeterministicSameGenerationEquality(t *testing.T) {
	t.Parallel()

	first := compile35(t, eligible35Input("gen-7"))
	in := eligible35Input("gen-7")
	for i, j := 0, len(in.Planes)-1; i < j; i, j = i+1, j-1 {
		in.Planes[i], in.Planes[j] = in.Planes[j], in.Planes[i]
	}
	reordered := compile35(t, in)
	if first != reordered {
		t.Fatalf("same generation facts must compile to equal summaries:\n%v\n%v", first, reordered)
	}
	if first.String() != reordered.String() {
		t.Fatal("equal summaries must render identically")
	}
	blocked := compile35(t, occupy35Plane(eligible35Input("gen-7"), "local_turn_handlers"))
	again := compile35(t, occupy35Plane(eligible35Input("gen-7"), "local_turn_handlers"))
	if blocked != again {
		t.Fatal("blocked generation must also compile deterministically")
	}
}

// TestWireEligibility_GenerationPinnedStaleness pins the Requirement 6.7
// binding: the summary carries its generation, equal authorities under a
// different generation compare unequal, and PinnedTo detects reload drift.
func TestWireEligibility_GenerationPinnedStaleness(t *testing.T) {
	t.Parallel()

	oldGen := compile35(t, eligible35Input("gen-7"))
	newGen := compile35(t, eligible35Input("gen-8"))
	if oldGen == newGen {
		t.Fatal("same authorities under different generations must not compare equal")
	}
	if oldGen.GenerationID() != "gen-7" || newGen.GenerationID() != "gen-8" {
		t.Fatalf("summary must carry its generation binding, got %q vs %q", oldGen.GenerationID(), newGen.GenerationID())
	}
	if !oldGen.PinnedTo("gen-7") || oldGen.PinnedTo("gen-8") {
		t.Fatal("PinnedTo must report the exact bound generation only")
	}
	if !newGen.PinnedTo("gen-8") || newGen.PinnedTo("gen-7") {
		t.Fatal("PinnedTo must report the exact bound generation only")
	}
	var zero largebody.WireEligibilitySummary
	if zero.PinnedTo("gen-7") {
		t.Fatal("zero summary must not claim any generation pin")
	}
}

// TestWireEligibility_BoundedSize pins the "no request-sized data" contract:
// fixed-size struct, bounded generation binding, bounded diagnostics.
func TestWireEligibility_BoundedSize(t *testing.T) {
	t.Parallel()

	sum := compile35(t, eligible35Input("gen-7"))
	if size := reflect.TypeFor[largebody.WireEligibilitySummary]().Size(); size > 64 {
		t.Fatalf("summary must stay a fixed small struct, got %d bytes", size)
	}
	if got := len(sum.GenerationID()); got > testEligibilityBudget {
		t.Fatalf("generation binding exceeds budget: %d > %d", got, testEligibilityBudget)
	}
	tooLong := eligible35Input(strings.Repeat("g", testEligibilityBudget+1))
	if _, err := largebody.CompileWireEligibilitySummary(tooLong, testEligibilityBudget); err == nil {
		t.Fatal("over-budget generation binding must fail closed")
	}
	hugeGarbage := eligible35Input("gen-7")
	hugeGarbage.Planes = append(hugeGarbage.Planes, largebody.PlaneEligibilityInput{
		ID:       strings.Repeat("x", 1<<20),
		Access:   largebody.PlaneAccessCanonicalRequired,
		Occupied: true,
	})
	_, err := largebody.CompileWireEligibilitySummary(hugeGarbage, testEligibilityBudget)
	if err == nil {
		t.Fatal("megabyte garbage plane ID must fail closed")
	}
	if len(err.Error()) > 1024 {
		t.Fatalf("diagnostics must stay bounded, got %d bytes: %.80q...", len(err.Error()), err.Error())
	}
}

// TestWireEligibility_UnknownFailsClosed pins fail-closed composition: every
// malformed input returns an error AND a fail-closed (blocking, unsealed)
// summary so ignoring the error still selects the canonical path.
func TestWireEligibility_UnknownFailsClosed(t *testing.T) {
	t.Parallel()

	cases := map[string]func(largebody.WireEligibilityInput) largebody.WireEligibilityInput{
		"unknown_plane": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.Planes = append(in.Planes, largebody.PlaneEligibilityInput{
				ID:       "core.some_future_plane",
				Access:   largebody.PlaneAccessCanonicalRequired,
				Occupied: true,
			})
			return in
		},
		"duplicate_plane": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.Planes = append(in.Planes, in.Planes[0])
			return in
		},
		"missing_plane": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.Planes = in.Planes[:len(in.Planes)-1]
			return in
		},
		"unclassified_plane": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.Planes = append([]largebody.PlaneEligibilityInput(nil), in.Planes...)
			in.Planes[0].Access = largebody.PlaneAccessUnclassified
			return in
		},
		"out_of_range_access": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.Planes = append([]largebody.PlaneEligibilityInput(nil), in.Planes...)
			in.Planes[0].Access = largebody.PlaneAccess(99)
			return in
		},
		"empty_generation": func(in largebody.WireEligibilityInput) largebody.WireEligibilityInput {
			in.GenerationID = ""
			return in
		},
	}
	for name, mutate := range cases {
		sum, err := largebody.CompileWireEligibilitySummary(mutate(eligible35Input("gen-7")), testEligibilityBudget)
		if err == nil {
			t.Fatalf("%s: must fail closed with an error", name)
		}
		if sum.Sealed() {
			t.Fatalf("%s: fail-closed summary must not be sealed", name)
		}
		if !sum.HasStaticBlocker() {
			t.Fatalf("%s: fail-closed summary must report a static blocker", name)
		}
		if err := sum.Validate(testEligibilityBudget); err == nil {
			t.Fatalf("%s: fail-closed summary must not validate", name)
		}
	}
	if _, err := largebody.CompileWireEligibilitySummary(eligible35Input("gen-7"), 0); err == nil {
		t.Fatal("non-positive fact budget must fail closed")
	}
}

// TestWireEligibility_NoRequestDataRetention pins that the summary copies its
// bounded inputs: mutating caller memory after compilation cannot change a
// published summary, and summaries never alias caller slices.
func TestWireEligibility_NoRequestDataRetention(t *testing.T) {
	t.Parallel()

	in := eligible35Input("gen-7")
	sum := compile35(t, in)
	in.GenerationID = "gen-8"
	in.Planes[0].Occupied = true
	in.Planes = append(in.Planes, largebody.PlaneEligibilityInput{
		ID:       "core.some_future_plane",
		Access:   largebody.PlaneAccessCanonicalRequired,
		Occupied: true,
	})
	in.Hooks.SubmitOccupied = true
	in.Ports.TrafficCapturing = true
	in.TwoPhaseExecutorAvailable = false
	if sum.GenerationID() != "gen-7" {
		t.Fatalf("summary must pin the compiled generation, got %q", sum.GenerationID())
	}
	if sum.HasStaticBlocker() {
		t.Fatalf("published summary must be unaffected by caller mutation, got %v", sum)
	}
	pristine := compile35(t, eligible35Input("gen-7"))
	if sum != pristine {
		t.Fatal("published summary must equal a fresh compile of the same facts")
	}
}

// TestWireEligibility_ZeroValueFailsClosed pins that the zero summary is a
// safe default: unsealed, blocking, bound to nothing.
func TestWireEligibility_ZeroValueFailsClosed(t *testing.T) {
	t.Parallel()

	var zero largebody.WireEligibilitySummary
	if zero.Sealed() {
		t.Fatal("zero summary must not be sealed")
	}
	if !zero.HasStaticBlocker() {
		t.Fatal("zero summary must report a static blocker")
	}
	if zero.GenerationID() != "" {
		t.Fatalf("zero summary must carry no generation binding, got %q", zero.GenerationID())
	}
	if err := zero.Validate(testEligibilityBudget); err == nil {
		t.Fatal("zero summary must not validate")
	}
}

// TestWireEligibility_PlaneAccessParityWithFeature pins the V1 contract
// between the leaf-pure compiler enum and the sole plane declaration source
// (pkg/aipsdk/feature): numeric values and static labels match exactly so the
// composition mapper cannot silently skew a classification.
func TestWireEligibility_PlaneAccessParityWithFeature(t *testing.T) {
	t.Parallel()

	pairs := []struct {
		feat feature.RequestBodyAccess
		want largebody.PlaneAccess
	}{
		{feature.RequestBodyAccessUnclassified, largebody.PlaneAccessUnclassified},
		{feature.RequestBodyCanonicalRequired, largebody.PlaneAccessCanonicalRequired},
		{feature.RequestBodyMetadataOnly, largebody.PlaneAccessMetadataOnly},
		{feature.RequestBodyResponseOnly, largebody.PlaneAccessResponseOnly},
		{feature.RequestBodyWireContract, largebody.PlaneAccessWireContract},
	}
	for _, p := range pairs {
		if uint8(p.feat) != uint8(p.want) {
			t.Fatalf("access parity: feature %v (%d) != eligibility %v (%d)",
				p.feat, uint8(p.feat), p.want, uint8(p.want))
		}
		if p.feat.String() != p.want.String() {
			t.Fatalf("access label parity: %q != %q", p.feat.String(), p.want.String())
		}
	}
}

// TestWireEligibility_KnownPlanesMatchStandardPlanes pins the closed-world
// ratchet: the compiler knows exactly the current StandardPlanes manifest.
// A new production plane fails here until it is classified in the compiler
// table (Requirement 5.12).
func TestWireEligibility_KnownPlanesMatchStandardPlanes(t *testing.T) {
	t.Parallel()

	if largebody.WireEligibilityPlaneCount != len(feature.StandardPlanes) {
		t.Fatalf("compiler knows %d planes but manifest declares %d: classify the new plane first",
			largebody.WireEligibilityPlaneCount, len(feature.StandardPlanes))
	}
	seen := make(map[string]struct{}, len(feature.StandardPlanes))
	for _, decl := range feature.StandardPlanes {
		id := decl.PlaneID()
		if _, dup := seen[id]; dup {
			t.Fatalf("manifest declares duplicate plane %q", id)
		}
		seen[id] = struct{}{}
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		if !ok {
			t.Fatalf("compiler does not know manifest plane %q: classify it first", id)
		}
		back, ok := largebody.WireEligibilityPlaneID(idx)
		if !ok || back != id {
			t.Fatalf("plane index round-trip failed for %q (idx %d -> %q)", id, idx, back)
		}
		access, classified := census35Access[id]
		if !classified {
			t.Fatalf("manifest plane %q has no compiler classification: classify it first", id)
		}
		if access == largebody.PlaneAccessUnclassified {
			t.Fatalf("manifest plane %q is unclassified: fails closed", id)
		}
	}
	// The census-derived input set compiles: manifest and compiler agree.
	compile35(t, eligible35Input("gen-7"))
}

// TestWireEligibility_NonNegotiablePlaneBlockers pins the Task 3.2 starting
// points inside the summary: occupied Local Turn / Secret Guard / Terminal
// Decision (and every other occupied canonical-required plane) blocks, while
// occupied metadata-only and response-only planes stay eligible for dynamic
// assessment.
func TestWireEligibility_NonNegotiablePlaneBlockers(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"local_turn_handlers",
		"secret_guards",
		"secret_guard_execution",
		"terminal_decision_provider",
	} {
		sum := compile35(t, occupy35Plane(eligible35Input("gen-7"), id))
		if !sum.HasStaticBlocker() {
			t.Fatalf("occupied %s must be a static blocker", id)
		}
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		if !ok {
			t.Fatalf("plane index missing for %q", id)
		}
		if sum.PlaneBlockers()&(1<<uint(idx)) == 0 {
			t.Fatalf("plane blocker mask must name %s (idx %d), got %#x", id, idx, sum.PlaneBlockers())
		}
	}
	// Blocker 2: Response-only planes are static blockers under the conservative fail-safe.
	for _, id := range []string{
		"response_part_hooks",
		"completion_gates",
		"stream_observer_factories",
		"usage_observers",
	} {
		sum := compile35(t, occupy35Plane(eligible35Input("gen-7"), id))
		if !sum.HasStaticBlocker() {
			t.Fatalf("occupied response-only plane %s must statically block under Blocker 2, got %v", id, sum)
		}
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		if !ok {
			t.Fatalf("plane index missing for %q", id)
		}
		if sum.PlaneBlockers()&(1<<uint(idx)) == 0 {
			t.Fatalf("plane blocker mask must name %s (idx %d), got %#x", id, idx, sum.PlaneBlockers())
		}
	}
	// Metadata-only planes stay eligible for dynamic assessment without static blocking.
	for _, id := range []string{
		"session_openers",
		"workspace_resolvers",
		"tool_call_finalization_max_args_bytes",
		"session_classifier",
	} {
		sum := compile35(t, occupy35Plane(eligible35Input("gen-7"), id))
		if sum.HasStaticBlocker() {
			t.Fatalf("occupied metadata-only plane %s must not statically block, got %v", id, sum)
		}
	}
}

func downgrade35Plane(in largebody.WireEligibilityInput, id string, access largebody.PlaneAccess) largebody.WireEligibilityInput {
	out := in
	out.Planes = append([]largebody.PlaneEligibilityInput(nil), in.Planes...)
	found := false
	for i := range out.Planes {
		if out.Planes[i].ID == id {
			out.Planes[i].Access = access
			found = true
		}
	}
	if !found {
		panic("downgrade35Plane: unknown plane " + id)
	}
	return out
}

// TestWireEligibility_NonNegotiablePlaneAccessDowngrade pins that an occupied
// non-negotiable canonical plane cannot be weakened to metadata-only or to an
// explicit wire contract: the static blocker bit survives the downgrade, while
// an absent plane at the same weakened access stays eligible.
func TestWireEligibility_NonNegotiablePlaneAccessDowngrade(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"local_turn_handlers",
		"secret_guards",
		"secret_guard_execution",
		"terminal_decision_provider",
		"control_tool_provider",
	} {
		for _, access := range []largebody.PlaneAccess{
			largebody.PlaneAccessMetadataOnly,
			largebody.PlaneAccessWireContract,
		} {
			t.Run(id+"/"+access.String(), func(t *testing.T) {
				t.Parallel()

				occupied := compile35(t, downgrade35Plane(occupy35Plane(eligible35Input("gen-7"), id), id, access))
				if !occupied.HasStaticBlocker() {
					t.Fatalf("occupied %s downgraded to %s must remain a static blocker, got %v",
						id, access, occupied)
				}
				idx, ok := largebody.WireEligibilityPlaneIndex(id)
				if !ok {
					t.Fatalf("plane index missing for %q", id)
				}
				if occupied.PlaneBlockers()&(1<<uint(idx)) == 0 {
					t.Fatalf("plane blocker mask must name downgraded occupied %s (idx %d), got %#x",
						id, idx, occupied.PlaneBlockers())
				}

				// An absent provider at the same weakened access stays eligible.
				absent := compile35(t, downgrade35Plane(eligible35Input("gen-7"), id, access))
				if absent.HasStaticBlocker() {
					t.Fatalf("absent %s at %s must stay eligible, got %v", id, access, absent)
				}
			})
		}
	}
}

// TestWireEligibility_HookBusBlockers pins the verdicts inside the summary:
// occupied submit/request-part/tool chains and (under Blocker 2 conservative fail-safe)
// response-part chains statically block wire eligibility.
func TestWireEligibility_HookBusBlockers(t *testing.T) {
	t.Parallel()

	blocking := []struct {
		name string
		set  func(*largebody.HookEligibilityInput)
		want largebody.HookChain
	}{
		{"submit", func(h *largebody.HookEligibilityInput) { h.SubmitOccupied = true }, largebody.HookChainSubmit},
		{"requestParts", func(h *largebody.HookEligibilityInput) { h.RequestPartOccupied = true }, largebody.HookChainRequestPart},
		{"responseParts", func(h *largebody.HookEligibilityInput) { h.ResponsePartOccupied = true }, largebody.HookChainResponsePart},
		{"tools", func(h *largebody.HookEligibilityInput) { h.ToolOccupied = true }, largebody.HookChainTool},
	}
	for _, tc := range blocking {
		in := eligible35Input("gen-7")
		tc.set(&in.Hooks)
		sum := compile35(t, in)
		if !sum.HasStaticBlocker() {
			t.Fatalf("occupied %s hook chain must be a static blocker", tc.name)
		}
		if largebody.HookChain(sum.HookBlockers())&tc.want == 0 {
			t.Fatalf("hook blocker mask must name %s, got %#x", tc.name, sum.HookBlockers())
		}
	}
	in := eligible35Input("gen-7")
	in.Hooks.ResponsePartOccupied = true
	sum := compile35(t, in)
	if !sum.HasStaticBlocker() {
		t.Fatalf("occupied response-only hook chain must statically block under Blocker 2, got %v", sum)
	}
	if largebody.HookChain(sum.HookOccupancy())&largebody.HookChainResponsePart == 0 {
		t.Fatalf("hook occupancy must record the response chain, got %#x", sum.HookOccupancy())
	}
	if largebody.HookChain(sum.HookBlockers())&largebody.HookChainResponsePart == 0 {
		t.Fatalf("hook blockers must name the response chain under Blocker 2, got %#x", sum.HookBlockers())
	}
}

// TestWireEligibility_NarrowPortBlockers pins the Task 3.4 compilation inside
// the summary: every occupied port whose frozen state is Blocker blocks with
// its named bit, and the all-clear composition stays eligible.
func TestWireEligibility_NarrowPortBlockers(t *testing.T) {
	t.Parallel()

	blocking := []struct {
		name string
		set  func(*largebody.NarrowPortEligibilityInput)
		want largebody.WirePortBlocker
	}{
		{"core.backends empty", func(p *largebody.NarrowPortEligibilityInput) { p.BackendsEmpty = true }, largebody.WirePortBackendsEmpty},
		{"core.conversation_view_reader", func(p *largebody.NarrowPortEligibilityInput) { p.ConversationViewReaderOccupied = true }, largebody.WirePortConversationViewReader},
		{"core.conversation_view_tagger", func(p *largebody.NarrowPortEligibilityInput) { p.ConversationViewTaggerOccupied = true }, largebody.WirePortConversationViewTagger},
		{"core.steering_writer_factory", func(p *largebody.NarrowPortEligibilityInput) { p.SteeringWriterFactoryOccupied = true }, largebody.WirePortSteeringWriterFactory},
		{"billing.exposure_admission", func(p *largebody.NarrowPortEligibilityInput) { p.ExposureAdmissionOccupied = true }, largebody.WirePortExposureAdmission},
		{"billing.identity_call_callbacks", func(p *largebody.NarrowPortEligibilityInput) { p.BillingIdentityCustomCallbacks = true }, largebody.WirePortBillingIdentityCallbacks},
		{"routing.caps_resolver", func(p *largebody.NarrowPortEligibilityInput) { p.CapsResolverOccupied = true }, largebody.WirePortCapsResolver},
		{"routing.catalog_resolver", func(p *largebody.NarrowPortEligibilityInput) { p.CatalogResolverOccupied = true }, largebody.WirePortCatalogResolver},
		{"routing.eligibility_resolver", func(p *largebody.NarrowPortEligibilityInput) { p.EligibilityResolverOccupied = true }, largebody.WirePortEligibilityResolver},
		{"routing.request_token_estimator", func(p *largebody.NarrowPortEligibilityInput) { p.RequestTokenEstimatorOccupied = true }, largebody.WirePortRequestTokenEstimator},
		{"accounting.preflight count-only", func(p *largebody.NarrowPortEligibilityInput) { p.PreflightEnabled = true }, largebody.WirePortPreflightCountOnly},
		{"accounting.stream_usage", func(p *largebody.NarrowPortEligibilityInput) { p.StreamUsageOccupied = true }, largebody.WirePortStreamUsage},
		{"accounting.admin_count_service", func(p *largebody.NarrowPortEligibilityInput) { p.AdminCountServiceOccupied = true }, largebody.WirePortAdminCountService},
		{"interleaved.processor", func(p *largebody.NarrowPortEligibilityInput) { p.InterleavedProcessorOccupied = true }, largebody.WirePortInterleavedProcessor},
		{"compaction.detector", func(p *largebody.NarrowPortEligibilityInput) { p.CompactionDetectorOccupied = true }, largebody.WirePortCompactionDetector},
		{"traffic.port_bundle capturing", func(p *largebody.NarrowPortEligibilityInput) { p.TrafficCapturing = true }, largebody.WirePortTrafficCapturing},
		{"counting.token_rule", func(p *largebody.NarrowPortEligibilityInput) { p.TokenCountingRequired = true }, largebody.WirePortTokenCounting},
		{"custom.call_callbacks", func(p *largebody.NarrowPortEligibilityInput) { p.CustomCallCallbacksPresent = true }, largebody.WirePortCustomCallCallbacks},
	}
	for _, tc := range blocking {
		in := eligible35Input("gen-7")
		tc.set(&in.Ports)
		sum := compile35(t, in)
		if !sum.HasStaticBlocker() {
			t.Fatalf("%s must be a static blocker", tc.name)
		}
		if sum.PortBlockers()&tc.want == 0 {
			t.Fatalf("port blocker mask must name %s, got %#x", tc.name, sum.PortBlockers())
		}
	}
	// Disabled counting/preflight stays wire-capable; an exact wire counter
	// clears the counting/preflight blockers (Task 10.4 contract shape).
	for _, tc := range []struct {
		name string
		set  func(*largebody.NarrowPortEligibilityInput)
	}{
		{"preflight disabled", func(p *largebody.NarrowPortEligibilityInput) {}},
		{"preflight with exact counter", func(p *largebody.NarrowPortEligibilityInput) {
			p.PreflightEnabled = true
			p.PreflightHasExactCounter = true
		}},
		{"counting with exact counter", func(p *largebody.NarrowPortEligibilityInput) {
			p.TokenCountingRequired = true
			p.TokenCountingHasExactCounter = true
		}},
		{"caps resolver with wire proof subsumption", func(p *largebody.NarrowPortEligibilityInput) {
			p.CapsResolverOccupied = true
			p.CapsResolverWireProofSubsumed = true
		}},
		{"conversation view reader fresh aleg supported", func(p *largebody.NarrowPortEligibilityInput) {
			p.ConversationViewReaderOccupied = true
			p.ConversationReaderFreshALegSupported = true
		}},
		{"compaction detector wire supported", func(p *largebody.NarrowPortEligibilityInput) {
			p.CompactionDetectorOccupied = true
			p.CompactionDetectorWireSupported = true
		}},
	} {
		in := eligible35Input("gen-7")
		tc.set(&in.Ports)
		sum := compile35(t, in)
		if sum.HasStaticBlocker() {
			t.Fatalf("%s must stay eligible for assessment, got %v", tc.name, sum)
		}
	}
	// A present but model-dependent backend map is dynamic, never a static
	// blocker: only an empty map (no candidate for any request) blocks.
	in := eligible35Input("gen-7")
	in.Ports.BackendsEmpty = false
	if sum := compile35(t, in); sum.HasStaticBlocker() {
		t.Fatalf("present backends must defer to dynamic assessment, got %v", sum)
	}
	// No two-phase executor capability blocks statically (design section 4).
	noExec := eligible35Input("gen-7")
	noExec.TwoPhaseExecutorAvailable = false
	sum := compile35(t, noExec)
	if !sum.HasStaticBlocker() {
		t.Fatal("missing two-phase executor must be a static blocker")
	}
	if sum.PortBlockers()&largebody.WirePortTwoPhaseExecutorMissing == 0 {
		t.Fatalf("port blocker mask must name the executor capability, got %#x", sum.PortBlockers())
	}
}

// TestWireEligibility_InputModelStaysFixed pins the closed-world input shape:
// the narrow-port struct carries exactly the Task 3.4-derived fields, so a
// new authority cannot slip in unclassified. Adding a field requires
// updating the compiler policy and this tripwire together. The plane table
// grows append-only within the 32-bit blocker capacity: the original 28
// entries stay stable while validated appends (up to 32) are permitted; the
// tripwire still fires on a shrink below 28 or an overflow past 32.
func TestWireEligibility_InputModelStaysFixed(t *testing.T) {
	t.Parallel()

	if got := reflect.TypeFor[largebody.NarrowPortEligibilityInput]().NumField(); got != 23 {
		t.Fatalf("narrow-port input model changed (%d fields): extend the compiler policy first", got)
	}
	if largebody.WireEligibilityPlaneCount < 28 || largebody.WireEligibilityPlaneCount > 32 {
		t.Fatalf("plane table changed (%d planes): extend the compiler policy first (append-only within [28,32])", largebody.WireEligibilityPlaneCount)
	}
}

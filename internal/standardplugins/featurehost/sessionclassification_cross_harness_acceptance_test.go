package featurehost

// Task 10.1 cross-harness acceptance certification.
//
// The frozen evidence matrix from task 1.2 is executed here through the REAL
// composed surfaces: a real standard featurehost process, real compiled
// generations whose classifier is the production one, a real generic-core
// executor, and the production durable SQLite store. Nothing in this file
// re-implements a classification rule, a tool category, or a store, so a row can
// only pass because the production pipeline really produced that outcome
// (requirements 3.1-3.4, 12.1, 12.3, 12.7).
//
// Two independent positive paths are certified, and they are deliberately kept
// separable so neither can mask the other's failure:
//
//   - identity positives (Rule A, requirement 3.1): a recognized high-confidence
//     coding-harness User-Agent family, with NO tool evidence at all;
//   - tool-structure positives (Rules B/C, requirement 3.3): a coding harness
//     with a generic or absent User-Agent that is still classifiable from the
//     canonical tool-name categories plus, where needed, a recognized project
//     marker.
//
// Each positive group is additionally replayed with the other group's only
// evidence removed, so a group that could only pass because of the other group
// would fail. The task's status report records the mutation proofs that break
// Rule A and Rules B/C separately and shows which group fails in each case.
//
// The wire lane runs every row too. Per task 7.3 the wire stage deliberately
// discards its same-turn projection because every requirement-4.2 consumer is
// canonical-required, so wire parity is asserted on the DURABLE, observable
// outcome rather than on a same-turn wire view (requirements 5.3, 12.7).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification/testfixtures"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// crossHarnessGroup names the load-bearing path a matrix row exercises.
type crossHarnessGroup string

const (
	// crossHarnessIdentity is the stable-identity positive path: Rule A alone.
	crossHarnessIdentity crossHarnessGroup = "identity"
	// crossHarnessTooling is the tool-structure positive path: Rules B/C with a
	// generic or absent User-Agent.
	crossHarnessTooling crossHarnessGroup = "tooling"
	// crossHarnessExcluded covers rows carrying a configured exclusion prefix.
	// It is a separate group because those rows are served by a second compiled
	// generation, not by the stock one.
	crossHarnessExcluded crossHarnessGroup = "excluded"
	// crossHarnessNegative is a row whose expected classification is unknown.
	crossHarnessNegative crossHarnessGroup = "negative"
)

// crossHarnessExpectedCode is the SINGLE place a row's expected decisive evidence
// code is read from the frozen matrix. Keeping it in one function makes a row's
// expectation one data read, so a wrong expectation fails exactly that row and
// names its fixture ID (see the status report's non-vacuity proof).
func crossHarnessExpectedCode(fixture testfixtures.EvidenceFixture) session.EvidenceCode {
	switch len(fixture.ExpectedEvidenceCodes) {
	case 0:
		return ""
	case 1:
		return session.EvidenceCode(fixture.ExpectedEvidenceCodes[0])
	default:
		return session.EvidenceCode(strings.Join(fixture.ExpectedEvidenceCodes, "+"))
	}
}

// crossHarnessExpectedSource is the bounded classification source the decisive
// evidence code implies: an identity code came from local identity evidence, a
// tooling code from local tool-category evidence.
func crossHarnessExpectedSource(code session.EvidenceCode) session.ClassificationSource {
	switch {
	case strings.HasPrefix(string(code), "client_family."):
		return session.SourceLocalIdentity
	case strings.HasPrefix(string(code), "tooling."):
		return session.SourceLocalTooling
	default:
		return ""
	}
}

// crossHarnessGroupOf classifies a matrix row by the path that decides it. A
// positive row with no decisive code, with several, or with a code outside the
// two supported positive paths is a matrix defect: the classifier can only ever
// report one decisive evidence code.
func crossHarnessGroupOf(fixture testfixtures.EvidenceFixture) (crossHarnessGroup, error) {
	if len(fixture.IgnoredUserAgentPrefixes) > 0 {
		return crossHarnessExcluded, nil
	}
	if fixture.ExpectedClassification != testfixtures.ClassificationCodingAgent {
		if len(fixture.ExpectedEvidenceCodes) != 0 {
			return "", fmt.Errorf("unknown row %q declares %d decisive evidence codes",
				fixture.ID, len(fixture.ExpectedEvidenceCodes))
		}
		return crossHarnessNegative, nil
	}
	if len(fixture.ExpectedEvidenceCodes) != 1 {
		return "", fmt.Errorf("positive row %q declares %d decisive evidence codes, want exactly 1",
			fixture.ID, len(fixture.ExpectedEvidenceCodes))
	}
	code := fixture.ExpectedEvidenceCodes[0]
	switch {
	case strings.HasPrefix(code, "client_family."):
		return crossHarnessIdentity, nil
	case strings.HasPrefix(code, "tooling."):
		return crossHarnessTooling, nil
	default:
		return "", fmt.Errorf("positive row %q declares evidence code %q outside the two supported positive paths",
			fixture.ID, code)
	}
}

// crossHarnessOutcome is what one lane actually produced for one row.
type crossHarnessOutcome struct {
	// Projected is the same-turn classification a downstream consumer received.
	// The wire lane deliberately has no same-turn consumer (task 7.3), so it is
	// left zero there and wire parity is asserted on Durable instead.
	Projected session.Classification
	// Durable is the committed store value, read back through an INDEPENDENT
	// store instance so a process cache cannot stand in for committed state.
	Durable    session.Classification
	DurableSet bool
}

// crossHarnessResult is one matrix row's full acceptance result.
type crossHarnessResult struct {
	Fixture   testfixtures.EvidenceFixture
	Group     crossHarnessGroup
	Canonical crossHarnessOutcome
	Wire      crossHarnessOutcome
	Err       error
}

func (r crossHarnessResult) verdict() string {
	if r.Err != nil {
		return "FAIL: " + r.Err.Error()
	}
	return "PASS"
}

// crossHarnessKind names a classification kind for the result table without
// printing a whole struct.
func crossHarnessKind(c session.Classification) string {
	if c.IsCodingAgent() {
		return "coding_agent"
	}
	return "unknown"
}

// crossHarnessSupport finds a harness's documented identity support row.
func crossHarnessSupport(matrix testfixtures.Matrix, harness string) *testfixtures.HarnessIdentity {
	for index := range matrix.HarnessIdentitySupport {
		if matrix.HarnessIdentitySupport[index].Harness == harness {
			return &matrix.HarnessIdentitySupport[index]
		}
	}
	return nil
}

// crossHarnessMatrix loads the frozen matrix and enforces this task's own hard
// invariants on it before any turn runs: every SourceRefs entry must resolve to a
// real Source ID, every positive row must name exactly one decisive code from one
// of the two supported positive paths, and every group must be populated. A
// matrix that violates any of these is reported as a fixture-artifact defect
// rather than papered over by weakening an expectation.
func crossHarnessMatrix(tb testing.TB) testfixtures.Matrix {
	tb.Helper()
	matrix, err := testfixtures.Load()
	if err != nil {
		tb.Fatalf("load evidence matrix: %v", err)
	}

	sources := make(map[string]struct{}, len(matrix.Sources))
	for _, source := range matrix.Sources {
		if source.ID == "" {
			tb.Error("matrix declares a source with an empty ID")
			continue
		}
		if _, duplicate := sources[source.ID]; duplicate {
			tb.Errorf("matrix declares duplicate source ID %q", source.ID)
		}
		sources[source.ID] = struct{}{}
	}

	groups := map[crossHarnessGroup]int{}
	identityHarnesses := map[string]struct{}{}
	for _, fixture := range matrix.Fixtures {
		for _, ref := range fixture.SourceRefs {
			if _, ok := sources[ref]; !ok {
				tb.Errorf("fixture %q references unknown evidence source ID %q", fixture.ID, ref)
			}
		}
		group, err := crossHarnessGroupOf(fixture)
		if err != nil {
			tb.Errorf("fixture %q: %v", fixture.ID, err)
			continue
		}
		groups[group]++
		if group == crossHarnessIdentity && fixture.Harness != "" {
			identityHarnesses[fixture.Harness] = struct{}{}
		}
	}

	// Requirements 3.1 and 3.3 must each be carried by at least one row. If either
	// group were empty the composed suite would silently certify only one path.
	for _, group := range []crossHarnessGroup{
		crossHarnessIdentity, crossHarnessTooling, crossHarnessNegative, crossHarnessExcluded,
	} {
		if groups[group] == 0 {
			tb.Errorf("matrix has no %q rows; the composed acceptance suite cannot certify that path", group)
		}
	}
	// Requirement 3.1 must be carried by more than one distinct harness family, so
	// a single family's accidental match cannot stand in for the identity rule.
	if len(identityHarnesses) < 2 {
		tb.Errorf("identity rows cover %d harness(es), want at least 2 so one family cannot carry requirement 3.1 alone",
			len(identityHarnesses))
	}
	return matrix
}

// TestCrossHarnessMatrixEvidenceIsTraceable proves requirement 12.1's per-fixture
// "documented evidence source" independently of any classification outcome, and
// proves no positive row invents identity for a harness the matrix cannot
// distinguish. It needs no process, so a fixture-artifact defect is reported even
// when the composed harness cannot be built.
func TestCrossHarnessMatrixEvidenceIsTraceable(t *testing.T) {
	t.Parallel()

	matrix := crossHarnessMatrix(t)
	sources := make(map[string]testfixtures.Source, len(matrix.Sources))
	for _, source := range matrix.Sources {
		sources[source.ID] = source
	}
	for _, support := range matrix.HarnessIdentitySupport {
		if len(support.SourceRefs) == 0 {
			t.Errorf("harness %q records no evidence source", support.Harness)
		}
		for _, ref := range support.SourceRefs {
			source, ok := sources[ref]
			if !ok {
				t.Errorf("harness %q references unknown source ID %q", support.Harness, ref)
				continue
			}
			if source.Reference == "" {
				t.Errorf("harness %q source %q carries no reference", support.Harness, ref)
			}
		}
	}
	for _, fixture := range matrix.Fixtures {
		if len(fixture.SourceRefs) == 0 {
			t.Errorf("fixture %q records no evidence source", fixture.ID)
			continue
		}
		for _, ref := range fixture.SourceRefs {
			if _, ok := sources[ref]; !ok {
				t.Errorf("fixture %q references unknown evidence source ID %q", fixture.ID, ref)
			}
		}
		// Requirement 3.2 / task 1.2: identity must not be invented for a harness
		// the matrix cannot distinguish. That constraint applies to the IDENTITY
		// path only: a tool-structure positive deliberately carries a generic or
		// absent SDK identity and is expected to stay classifiable anyway.
		group, groupErr := crossHarnessGroupOf(fixture)
		if groupErr != nil || group != crossHarnessIdentity || fixture.Harness == "" {
			continue
		}
		support := crossHarnessSupport(matrix, fixture.Harness)
		if support == nil {
			t.Errorf("fixture %q promotes harness %q by identity with no identity-support row", fixture.ID, fixture.Harness)
			continue
		}
		if support.Status != testfixtures.IdentityHighConfidence {
			t.Errorf("fixture %q promotes harness %q by identity although its documented status is %q, want %q: identity must not be invented for an indistinguishable harness",
				fixture.ID, fixture.Harness, support.Status, testfixtures.IdentityHighConfidence)
		}
	}
}

// ---------------------------------------------------------------------------
// Composed harness
// ---------------------------------------------------------------------------

// crossHarnessWorkspaceResolver is the resolved workspace for one certified
// process. The matrix supplies project markers per row and Rule C reads the
// resolved workspace, so markers are set immediately before each turn. Turns are
// sequential by construction (the census needs a total order); the mutex keeps the
// resolver safe if a future caller serves a turn concurrently.
type crossHarnessWorkspaceResolver struct {
	mu      sync.Mutex
	markers []string
}

func (r *crossHarnessWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return aipworkspace.WorkspaceView{
		ID:      certificationWorkspaceID,
		Markers: append([]string(nil), r.markers...),
	}, nil
}

func (r *crossHarnessWorkspaceResolver) set(markers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markers = append([]string(nil), markers...)
}

// crossHarnessCensusConsumer is a test-only downstream consumer. It records the
// classification each authoritative session actually received on its own turn,
// which is how the canonical lane's same-turn projection (requirement 4.2) is
// observed without trusting the classifier's own return value. It is not a
// production consumer: session classification gates no bundled feature until a
// feature opts in.
type crossHarnessCensusConsumer struct {
	id string

	mu        sync.Mutex
	bySession map[string]session.Classification
	runs      int
}

var _ prerequest.Handler = (*crossHarnessCensusConsumer)(nil)

func newCrossHarnessCensusConsumer(id string) *crossHarnessCensusConsumer {
	return &crossHarnessCensusConsumer{id: id, bySession: map[string]session.Classification{}}
}

func (c *crossHarnessCensusConsumer) ID() string                        { return c.id }
func (c *crossHarnessCensusConsumer) Order() int                        { return 0 }
func (c *crossHarnessCensusConsumer) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (c *crossHarnessCensusConsumer) Handle(
	_ context.Context,
	_ *aipapi.Call,
	meta prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bySession[meta.Session.AuthoritativeSessionID] = meta.Session.Classification
	c.runs++
	return prerequest.Allow(), nil
}

func (c *crossHarnessCensusConsumer) observed(sessionID string) (session.Classification, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	got, ok := c.bySession[sessionID]
	return got, ok
}

func (c *crossHarnessCensusConsumer) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// crossHarnessRig is one compiled generation's serving surfaces over the shared
// process state: the canonical executor (with the observing consumer) and the
// wire executor, both bound to the same durable database and the same
// process-owned classification state.
type crossHarnessRig struct {
	database  *bun.DB
	workspace *crossHarnessWorkspaceResolver
	consumer  *crossHarnessCensusConsumer

	canonical     *runtime.Executor
	canonicalOpen *atomic.Int32
	wire          *runtime.Executor
	wireOpen      *atomic.Int32
}

// crossHarnessHarness is a real featurehost process with TWO started compiled
// generations over one shared process state: the stock heuristic generation and a
// generation carrying the matrix's exclusion prefixes. Both are started, which is
// the overlapping-generation posture the holder reports SafeUnderCandidateOverlap
// for, so the exclusion rows exercise a real reloaded policy against a real shared
// durable store.
type crossHarnessHarness struct {
	database  *bun.DB
	process   *Runtime
	workspace *crossHarnessWorkspaceResolver
	consumer  *crossHarnessCensusConsumer

	stock   *crossHarnessRig
	exclude *crossHarnessRig
}

func newCrossHarnessHarness(tb testing.TB) *crossHarnessHarness {
	tb.Helper()

	database := certificationOpenSQLite(tb,
		certificationSQLiteDSN(filepath.Join(tb.TempDir(), "cross-harness.db")))

	process, err := NewProcess(context.Background(), ProcessInput{
		Logger: slog.Default(), BunDB: database, MetricsRegistry: prometheus.NewRegistry(),
	})
	if err != nil {
		tb.Fatalf("NewProcess: %v", err)
	}
	tb.Cleanup(func() {
		if err := process.Close(); err != nil {
			tb.Errorf("close cross-harness process: %v", err)
		}
	})

	workspace := &crossHarnessWorkspaceResolver{}
	consumer := newCrossHarnessCensusConsumer("cross-harness-census")
	prefixes := crossHarnessExclusionPrefixes(tb)
	if len(prefixes) == 0 {
		tb.Fatal("the matrix declares no ignored_user_agent_prefixes, so no exclusion generation can be compiled")
	}

	return &crossHarnessHarness{
		database:  database,
		process:   process,
		workspace: workspace,
		consumer:  consumer,
		stock: newCrossHarnessRig(tb, process, database, workspace, consumer,
			certificationCompile(tb, process, true, "")),
		exclude: newCrossHarnessRig(tb, process, database, workspace, consumer,
			certificationCompile(tb, process, true, crossHarnessExclusionYAML(tb, prefixes))),
	}
}

// crossHarnessExclusionPrefixes returns the single exclusion prefix set the matrix
// declares. Every exclusion row must agree on it, because one compiled generation
// serves them all.
func crossHarnessExclusionPrefixes(tb testing.TB) []string {
	tb.Helper()
	matrix, err := testfixtures.Load()
	if err != nil {
		tb.Fatalf("load evidence matrix: %v", err)
	}
	var prefixes []string
	for _, fixture := range matrix.Fixtures {
		if len(fixture.IgnoredUserAgentPrefixes) == 0 {
			continue
		}
		if prefixes == nil {
			prefixes = append([]string(nil), fixture.IgnoredUserAgentPrefixes...)
			continue
		}
		if !slices.Equal(prefixes, fixture.IgnoredUserAgentPrefixes) {
			tb.Fatalf("exclusion rows declare different prefix sets (%v and %q); one compiled generation cannot serve them honestly",
				prefixes, fixture.IgnoredUserAgentPrefixes)
		}
	}
	return prefixes
}

func crossHarnessExclusionYAML(tb testing.TB, prefixes []string) string {
	tb.Helper()
	quoted := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		quoted = append(quoted, "'"+prefix+"'")
	}
	return "heuristic:\n  ignored_user_agent_prefixes: [" + strings.Join(quoted, ", ") + "]\n"
}

func newCrossHarnessRig(
	tb testing.TB,
	process *Runtime,
	database *bun.DB,
	workspace *crossHarnessWorkspaceResolver,
	consumer *crossHarnessCensusConsumer,
	generation certificationGeneration,
) *crossHarnessRig {
	tb.Helper()
	if generation.classifier == nil {
		tb.Fatal("enabled generation published no classifier")
	}
	generation.start(tb)
	canonical, canonicalOpen := certificationExecutorWithWorkspace(tb, database,
		certificationPlanesWithConsumer(tb, generation.planes, consumer), workspace)
	// The wire lane gets the unmodified plane set: adding the observing consumer
	// would occupy a canonical-required plane and statically block the wire lane,
	// which is exactly the condition task 7.3 recorded.
	wire, wireOpen := certificationExecutorWithWorkspace(tb, database, generation.planes, workspace)
	return &crossHarnessRig{
		database:      database,
		workspace:     workspace,
		consumer:      consumer,
		canonical:     canonical,
		canonicalOpen: canonicalOpen,
		wire:          wire,
		wireOpen:      wireOpen,
	}
}

// rigFor selects the generation whose policy owns a row. A row with configured
// exclusion prefixes is served by the reloaded exclusion generation; every other
// row is served by the stock heuristic generation.
func (h *crossHarnessHarness) rigFor(fixture testfixtures.EvidenceFixture) *crossHarnessRig {
	if len(fixture.IgnoredUserAgentPrefixes) > 0 {
		return h.exclude
	}
	return h.stock
}

// crossHarnessToolDefs builds the canonical request's tool definitions from a
// row's tool evidence. Names AND descriptions are carried, because requirements
// 3.9/11.2 require canonical name-only classification: the
// assistant_action_description_names_unknown row is only meaningful when its
// description reaches the frontend and still produces no category.
func crossHarnessToolDefs(fixture testfixtures.EvidenceFixture) []aipapi.ToolDef {
	defs := make([]aipapi.ToolDef, 0, len(fixture.ToolEvidence))
	for _, tool := range fixture.ToolEvidence {
		defs = append(defs, aipapi.ToolDef{Name: tool.Name, Description: tool.Description})
	}
	return defs
}

// crossHarnessWireEvidence compiles the bounded evidence a certified frontend
// proof carries for the same request: the accepted client identity plus the fixed
// tool-category bits. It deliberately holds no tool list, description or prompt
// material (requirements 5.2, 5.5).
func crossHarnessWireEvidence(fixture testfixtures.EvidenceFixture) sdkclassification.Evidence {
	categories := sdkclassification.ToolCategorySet(0)
	for _, tool := range fixture.ToolEvidence {
		categories = categories.AddToolName(tool.Name)
	}
	return sdkclassification.Evidence{
		Operation:       aipapi.OperationOpenAIChatCompletions,
		ClientUserAgent: fixture.IdentityValue,
		ToolCategories:  categories,
	}
}

const crossHarnessWireBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"continue"}]}`

// crossHarnessDefaultPrompt is delivered only when a matrix row declares no
// weakPrompt. A row with a declared prompt always gets that prompt, so a
// technical-chat negative is never served with generic filler.
const crossHarnessDefaultPrompt = "summarize the diff"

// crossHarnessPromptFor is the single source of truth for which prompt a row
// delivers, so the census cannot disagree with the turn.
func crossHarnessPromptFor(fixture testfixtures.EvidenceFixture) string {
	if fixture.WeakPrompt != "" {
		return fixture.WeakPrompt
	}
	return crossHarnessDefaultPrompt
}

// serveCanonical runs one canonical client turn for a row and reports what the
// lane produced: the same-turn classification a downstream consumer received, and
// the committed durable row.
func (rig *crossHarnessRig) serveCanonical(
	t *testing.T,
	clientSessionID, sessionID, resumeToken string,
	fixture testfixtures.EvidenceFixture,
	identityValue string,
	toolDefs []aipapi.ToolDef,
) crossHarnessOutcome {
	t.Helper()
	rig.workspace.set(fixture.ProjectMarkers)

	// The row's declared weak evidence must actually reach the turn. Delivering a
	// generic prompt to a technical-chat negative would make the row vacuous: a
	// classifier that promoted on "main.go" or a code fence could never be observed
	// promoting because the signal was never sent. Note the SDK classifier input has
	// no model field by design, so the matrix's modelName is delivered through the
	// prompt exactly as the frozen weakPrompt carries it.
	call := certificationCallWith(clientSessionID, resumeToken, identityValue,
		crossHarnessPromptFor(fixture), fixture.ModelName)
	call.Tools = toolDefs
	before := rig.canonicalOpen.Load()
	certificationServe(t, rig.canonical, call)
	if rig.canonicalOpen.Load() <= before {
		t.Fatal("the canonical turn never opened the backend, so no classification outcome was served")
	}

	projected, ok := rig.consumer.observed(sessionID)
	if !ok {
		t.Fatalf("the downstream consumer never ran for authoritative session %q, so the same-turn projection was not observable", sessionID)
	}
	record, found := certificationLoadRow(t, rig.database, sessionID)
	return crossHarnessOutcome{Projected: projected, Durable: record.Classification, DurableSet: found}
}

// serveWireTurn runs one committed large-payload wire turn carrying the given
// bounded proof evidence and returns the proxy-owned authority carrier the lane
// bound.
func (rig *crossHarnessRig) serveWireTurn(
	t *testing.T,
	sessionInput largebody.SessionInput,
	evidence sdkclassification.Evidence,
	requestID string,
) largebody.SessionResponseCarrier {
	t.Helper()
	source := certificationNewWireSource(t, crossHarnessWireBody)
	assessment := certificationWireAssessment(t, source)
	proof := largebody.Proof{
		ProfileID:              "openai-chat",
		Operation:              aipapi.OperationOpenAIChatCompletions,
		Delivery:               aipapi.DeliveryModeStreaming,
		Identity:               largebody.NewIdentityDigest([32]byte{0x64, 0x64, 0x64, 0x64}),
		Session:                sessionInput,
		ClassificationEvidence: evidence,
	}
	ctx := largebody.ContextWithWireProof(context.Background(), proof, requestID)
	result, err := rig.wire.ExecuteLargeBody(ctx, assessment, source)
	if err != nil {
		t.Fatalf("ExecuteLargeBody for %q: %v", requestID, err)
	}
	if _, err := aipapi.Collect(context.Background(), result.Stream); err != nil {
		t.Fatalf("collect wire turn %q: %v", requestID, err)
	}
	if result.Session.AuthoritativeSessionID == "" {
		t.Fatalf("wire turn %q committed without a proxy-owned authoritative session", requestID)
	}
	if rig.wireOpen.Load() == 0 {
		t.Fatalf("wire turn %q never opened the backend, so no classification outcome was served", requestID)
	}
	return result.Session
}

// serveWire runs one wire turn for a brand-new wire session and reports the
// committed durable outcome plus the authority carrier the lane bound. The wire
// lane has no same-turn SessionView consumer, so only the durable outcome is
// observable there (task 7.3).
func (rig *crossHarnessRig) serveWire(
	t *testing.T,
	clientSessionID string,
	fixture testfixtures.EvidenceFixture,
	evidence sdkclassification.Evidence,
) (crossHarnessOutcome, largebody.SessionResponseCarrier) {
	t.Helper()
	rig.workspace.set(fixture.ProjectMarkers)
	carrier := rig.serveWireTurn(t, certificationNewWireSession(clientSessionID), evidence, "req-"+clientSessionID)
	record, found := certificationLoadRow(t, rig.database, carrier.AuthoritativeSessionID)
	return crossHarnessOutcome{Durable: record.Classification, DurableSet: found}, carrier
}

// ---------------------------------------------------------------------------
// Runner, census and per-row acceptance
// ---------------------------------------------------------------------------

// crossHarnessCensusError is the pure completeness check over the rows a run
// actually executed. It is deliberately a pure function of the census so the
// control tests can prove it REPORTS a skipped row instead of trusting it.
// crossHarnessDeliveredSignature is exactly the evidence a row puts in front of
// the classifier: the accepted user agent, the tool names, the project markers and
// the prompt. Two distinct rows may share a signature only when the matrix itself
// declares them indistinguishable.
func crossHarnessDeliveredSignature(fixture testfixtures.EvidenceFixture, deliveredPrompt string) string {
	names := make([]string, 0, len(fixture.ToolEvidence))
	for _, tool := range fixture.ToolEvidence {
		names = append(names, tool.Name)
	}
	// The ignored-prefix list and the prior classification change how the SAME
	// evidence is evaluated (a prefixed user agent is withdrawn; a prior positive
	// is preserved), so they are part of the delivered signature even though they
	// are configuration rather than turn content.
	return fmt.Sprintf("ua=%q ignored=%v prior=%q tools=%v markers=%v prompt=%q",
		fixture.IdentityValue, fixture.IgnoredUserAgentPrefixes, fixture.PriorClassification,
		names, fixture.ProjectMarkers, deliveredPrompt)
}

// crossHarnessDeliveredEvidenceError rejects a run in which two matrix rows that
// the matrix distinguishes were delivered identical evidence. Without this, a row
// whose weakPrompt/modelName was never sent silently collapses onto an empty-evidence
// baseline and its PASS is meaningless.
func crossHarnessDeliveredEvidenceError(delivered map[string]string, expected []testfixtures.EvidenceFixture) error {
	bySignature := map[string][]string{}
	for _, fixture := range expected {
		signature, ok := delivered[fixture.ID]
		if !ok {
			return fmt.Errorf("fixture %q recorded no delivered evidence", fixture.ID)
		}
		bySignature[signature] = append(bySignature[signature], fixture.ID)
	}
	var problems []string
	signatures := make([]string, 0, len(bySignature))
	for signature := range bySignature {
		signatures = append(signatures, signature)
	}
	sort.Strings(signatures)
	for _, signature := range signatures {
		ids := bySignature[signature]
		if len(ids) < 2 {
			continue
		}
		slices.Sort(ids)
		problems = append(problems, fmt.Sprintf(
			"fixtures %v were delivered identical evidence (%s); the matrix distinguishes them, "+
				"so at least one row's declared signal was not delivered and its result proves nothing",
			ids, signature))
	}
	if len(problems) > 0 {
		return fmt.Errorf("delivered evidence census collapsed distinct fixtures: %s", strings.Join(problems, "; "))
	}
	return nil
}

func crossHarnessCensusError(executed map[string]int, expected []testfixtures.EvidenceFixture) error {
	var problems []string
	seen := make(map[string]struct{}, len(expected))
	for _, fixture := range expected {
		count := executed[fixture.ID]
		switch {
		case count == 0:
			problems = append(problems, fmt.Sprintf("fixture %q was never executed", fixture.ID))
		case count > 1:
			problems = append(problems, fmt.Sprintf("fixture %q was executed %d times", fixture.ID, count))
		}
		seen[fixture.ID] = struct{}{}
	}
	extra := make([]string, 0)
	for id := range executed {
		if _, ok := seen[id]; !ok {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	for _, id := range extra {
		problems = append(problems, fmt.Sprintf("executed fixture %q is not in the matrix", id))
	}
	if len(problems) > 0 {
		return fmt.Errorf("fixture census does not cover the matrix: %s", strings.Join(problems, "; "))
	}
	return nil
}

// crossHarnessRun is one pass of the matrix plus its completeness verdict.
type crossHarnessRun struct {
	Results []crossHarnessResult
	Exec    map[string]int
	// Delivered records the evidence each row actually put on the wire (user
	// agent, tool names, markers, prompt digest). A row census alone cannot see a
	// row whose declared weak evidence was silently dropped, which would leave two
	// distinct rows sharing one byte-identical signature and certify nothing.
	Delivered map[string]string
	Err       error
}

// crossHarnessCheckRow asserts one row's composed outcome against the frozen
// expectation. Every channel the row can be observed on is checked: the canonical
// same-turn projection, the canonical durable row, and the wire durable row.
func crossHarnessCheckRow(result *crossHarnessResult) {
	// A lane that could not even establish the row's premise (a prior-positive
	// row's establishing turn, for example) reports that specific failure; the
	// outcome check must not overwrite it with a downstream symptom.
	if result.Err != nil {
		return
	}
	fixture := result.Fixture
	code := crossHarnessExpectedCode(fixture)
	canonical, wire := result.Canonical, result.Wire

	if fixture.ExpectedClassification != testfixtures.ClassificationCodingAgent {
		if canonical.Projected.IsCodingAgent() {
			result.Err = fmt.Errorf("canonical lane projected %+v, want unknown", canonical.Projected)
			return
		}
		if canonical.DurableSet {
			result.Err = fmt.Errorf("unknown row persisted a durable classification %+v", canonical.Durable)
			return
		}
		if wire.DurableSet {
			result.Err = fmt.Errorf("wire lane persisted a durable classification %+v for an unknown row", wire.Durable)
			return
		}
		return
	}

	if !canonical.Projected.IsCodingAgent() {
		result.Err = fmt.Errorf("canonical same-turn projection = %+v, want coding_agent with evidence %q",
			canonical.Projected, code)
		return
	}
	if canonical.Projected.Evidence != code {
		result.Err = fmt.Errorf("canonical evidence code = %q, want %q", canonical.Projected.Evidence, code)
		return
	}
	if want := crossHarnessExpectedSource(code); want != "" && canonical.Projected.Source != want {
		result.Err = fmt.Errorf("canonical source = %q, want %q for evidence %q",
			canonical.Projected.Source, want, code)
		return
	}
	if canonical.Projected.Confidence != session.ConfidenceHigh {
		result.Err = fmt.Errorf("canonical confidence = %q, want %q",
			canonical.Projected.Confidence, session.ConfidenceHigh)
		return
	}
	if !canonical.DurableSet || canonical.Durable != canonical.Projected {
		result.Err = fmt.Errorf("canonical durable row = %+v (set=%t), want the projected %+v",
			canonical.Durable, canonical.DurableSet, canonical.Projected)
		return
	}
	// A row that arrives without a prior positive was promoted by THIS turn, so the
	// store assigns revision 1. A row that arrives with an established positive must
	// come back at that same revision: requirement 3.7 makes a configured exclusion
	// prospective only, so the excluded turn can neither revoke it nor re-promote it
	// (which would advance the revision).
	if canonical.Projected.Revision != 1 {
		result.Err = fmt.Errorf("canonical revision = %d, want the store-assigned first revision 1",
			canonical.Projected.Revision)
		return
	}
	// Requirement 12.7: the same bounded evidence must produce the same outcome on
	// both lanes. The wire lane publishes no same-turn view, so the comparison is
	// against committed durable state, the outcome it does own.
	if !wire.DurableSet || wire.Durable != canonical.Projected {
		result.Err = fmt.Errorf("wire durable row = %+v (set=%t), want the canonical outcome %+v",
			wire.Durable, wire.DurableSet, canonical.Projected)
	}
}

// runMatrix serves every selected row through both lanes and returns the results
// plus the completeness verdict. Per-row problems are collected rather than fatal
// so the whole matrix still runs and the full result table can be reported.
func (h *crossHarnessHarness) runMatrix(
	t *testing.T,
	matrix testfixtures.Matrix,
	selected func(testfixtures.EvidenceFixture) bool,
) crossHarnessRun {
	t.Helper()
	run := crossHarnessRun{Exec: map[string]int{}, Delivered: map[string]string{}}
	var problems []string
	for _, fixture := range matrix.Fixtures {
		if selected != nil && !selected(fixture) {
			continue
		}
		group, err := crossHarnessGroupOf(fixture)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		result := crossHarnessResult{Fixture: fixture, Group: group}
		if group == crossHarnessExcluded && fixture.PriorClassification == testfixtures.ClassificationCodingAgent {
			h.servePriorPositive(t, &result)
		} else {
			rig := h.rigFor(fixture)
			clientSessionID := "xhr-" + fixture.ID
			sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)
			result.Canonical = rig.serveCanonical(t, clientSessionID, sessionID, resumeToken, fixture,
				fixture.IdentityValue, crossHarnessToolDefs(fixture))
			result.Wire, _ = rig.serveWire(t, clientSessionID+"-wire", fixture, crossHarnessWireEvidence(fixture))
		}
		crossHarnessCheckRow(&result)
		if result.Err != nil {
			problems = append(problems, fmt.Sprintf("fixture %q [%s]: %v", fixture.ID, group, result.Err))
		}
		run.Exec[fixture.ID]++
		run.Delivered[fixture.ID] = crossHarnessDeliveredSignature(fixture, crossHarnessPromptFor(fixture))
		run.Results = append(run.Results, result)
	}
	if err := crossHarnessCensusError(run.Exec, matrix.Fixtures); err != nil {
		problems = append(problems, err.Error())
	}
	if err := crossHarnessDeliveredEvidenceError(run.Delivered, matrix.Fixtures); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		run.Err = fmt.Errorf("cross-harness acceptance failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return run
}

// servePriorPositive serves the one row whose whole expectation is that an
// already-established positive survives a reloaded exclusion policy. The positive
// is established for real on the STOCK generation by a first turn, and only the
// second turn runs under the exclusion policy; nothing is seeded into the store.
func (h *crossHarnessHarness) servePriorPositive(t *testing.T, result *crossHarnessResult) {
	t.Helper()
	fixture := result.Fixture
	clientSessionID := "xhr-" + fixture.ID + "-prior"

	// Canonical lane: establish the positive on the stock generation, then resume
	// that very authoritative session under the exclusion generation.
	sessionID, resumeToken := certificationResumableSession(t, h.stock.canonical, clientSessionID)
	established := h.stock.serveCanonical(t, clientSessionID, sessionID, resumeToken, fixture,
		fixture.IdentityValue, crossHarnessToolDefs(fixture))
	if !established.DurableSet || !established.Durable.IsCodingAgent() {
		result.Err = fmt.Errorf("could not establish the row's prior positive: durable %+v (set=%t)",
			established.Durable, established.DurableSet)
		return
	}
	result.Canonical = h.exclude.serveCanonical(t, clientSessionID, sessionID, resumeToken, fixture,
		fixture.IdentityValue, crossHarnessToolDefs(fixture))
	// Requirement 3.7/8.7: the exclusion is prospective, so the excluded turn must
	// neither revoke the positive nor rewrite the durable row it came from.
	if result.Canonical.Durable != established.Durable {
		result.Err = fmt.Errorf("excluded turn rewrote the established durable row: %+v, want it unchanged at %+v",
			result.Canonical.Durable, established.Durable)
		return
	}

	// Wire lane: the same two-turn shape on the wire executors, so the lane's
	// durable outcome is a preserved positive too and parity stays meaningful.
	wireClientSessionID := clientSessionID + "-wire"
	evidence := crossHarnessWireEvidence(fixture)
	establishedWire, carrier := h.stock.serveWire(t, wireClientSessionID+"-establish", fixture, evidence)
	if !establishedWire.DurableSet || !establishedWire.Durable.IsCodingAgent() {
		result.Err = fmt.Errorf("wire lane could not establish the row's prior positive: durable %+v (set=%t)",
			establishedWire.Durable, establishedWire.DurableSet)
		return
	}
	wireResumeToken := carrier.ResumeToken.Reveal()
	if wireResumeToken == "" {
		result.Err = errors.New("the establishing wire turn issued no resume token, so no turn could continue its session")
		return
	}
	h.exclude.workspace.set(fixture.ProjectMarkers)
	preserved := h.exclude.serveWireTurn(t, largebody.SessionInput{
		AuthoritativeSessionID: carrier.AuthoritativeSessionID,
		ClientSessionID:        wireClientSessionID + "-establish",
		ResumeToken:            largebody.NewSensitiveString(wireResumeToken),
	}, evidence, "req-"+wireClientSessionID+"-preserve")
	record, found := certificationLoadRow(t, h.exclude.database, preserved.AuthoritativeSessionID)
	result.Wire = crossHarnessOutcome{Durable: record.Classification, DurableSet: found}
}

// ---------------------------------------------------------------------------
// Certification tests
// ---------------------------------------------------------------------------

// TestCrossHarnessEvidenceMatrixCertification is the load-bearing 10.1 suite:
// every row of the frozen matrix is served through the real canonical stage and
// the real wire stage, and each row's composed outcome is compared with the
// frozen expectation. The census proves every row ran exactly once, so a row can
// neither be skipped nor silently duplicated.
func TestCrossHarnessEvidenceMatrixCertification(t *testing.T) {
	t.Parallel()

	matrix := crossHarnessMatrix(t)
	harness := newCrossHarnessHarness(t)
	run := harness.runMatrix(t, matrix, nil)

	t.Logf("cross-harness acceptance over %d matrix rows (%d identity, %d tool-structure, %d excluded, %d unknown)",
		len(matrix.Fixtures), crossHarnessGroupSize(matrix, crossHarnessIdentity),
		crossHarnessGroupSize(matrix, crossHarnessTooling), crossHarnessGroupSize(matrix, crossHarnessExcluded),
		crossHarnessGroupSize(matrix, crossHarnessNegative))
	for _, result := range run.Results {
		code := crossHarnessExpectedCode(result.Fixture)
		t.Logf("  %-46s %-9s want=%-13s/%-30q canonical=%-13s/%-30q wire=%-13s/%-30q %s",
			result.Fixture.ID,
			result.Group,
			result.Fixture.ExpectedClassification, string(code),
			crossHarnessKind(result.Canonical.Projected), string(result.Canonical.Projected.Evidence),
			crossHarnessKind(result.Wire.Durable), string(result.Wire.Durable.Evidence),
			result.verdict())
	}
	if run.Err != nil {
		t.Fatal(run.Err)
	}
	// The consumer census is the SUITE census seen from the production side. A row
	// that arrives with a prior positive is deliberately served twice (establish,
	// then preserve under the reloaded exclusion policy), so the expected turn
	// count is one per row plus one per prior-positive row.
	wantTurns := len(matrix.Fixtures) + crossHarnessPriorPositiveRows(matrix)
	if got := harness.consumer.total(); got != wantTurns {
		t.Fatalf("downstream consumer ran %d times over %d matrix rows plus %d prior-positive rows, want %d",
			got, len(matrix.Fixtures), wantTurns-len(matrix.Fixtures), wantTurns)
	}
}

// crossHarnessPriorPositiveRows counts the rows served as two canonical turns.
func crossHarnessPriorPositiveRows(matrix testfixtures.Matrix) int {
	count := 0
	for _, fixture := range matrix.Fixtures {
		if fixture.PriorClassification == testfixtures.ClassificationCodingAgent {
			count++
		}
	}
	return count
}

func crossHarnessGroupSize(matrix testfixtures.Matrix, want crossHarnessGroup) int {
	count := 0
	for _, fixture := range matrix.Fixtures {
		if group, err := crossHarnessGroupOf(fixture); err == nil && group == want {
			count++
		}
	}
	return count
}

// TestCrossHarnessPositivePathsAreIndependentlyLoadBearing is the requirement
// 3.1/3.3 separation proof. Each positive group is replayed with the other
// group's ONLY evidence removed: the identity group with no tool definitions at
// all, and the tool-structure group with no client identity at all. A group that
// could pass only because of the other group's evidence fails here, so neither
// path can mask the other.
func TestCrossHarnessPositivePathsAreIndependentlyLoadBearing(t *testing.T) {
	t.Parallel()

	matrix := crossHarnessMatrix(t)
	harness := newCrossHarnessHarness(t)
	rig := harness.stock

	identityRows, toolingRows := 0, 0
	for _, fixture := range matrix.Fixtures {
		group, err := crossHarnessGroupOf(fixture)
		if err != nil {
			t.Fatalf("fixture %q: %v", fixture.ID, err)
		}
		if group != crossHarnessIdentity && group != crossHarnessTooling {
			continue
		}
		code := crossHarnessExpectedCode(fixture)
		clientSessionID := "xhr-isolated-" + fixture.ID
		sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)

		switch group {
		case crossHarnessIdentity:
			identityRows++
			// Requirement 3.1: a bounded identity value alone, with no prompt-text
			// inference and no tool evidence whatsoever.
			outcome := rig.serveCanonical(t, clientSessionID, sessionID, resumeToken, fixture,
				fixture.IdentityValue, nil)
			if !outcome.Projected.IsCodingAgent() || outcome.Projected.Evidence != code {
				t.Errorf("identity fixture %q with NO tool evidence = %+v, want coding_agent/%q",
					fixture.ID, outcome.Projected, code)
			}
			if outcome.Projected.Source != session.SourceLocalIdentity {
				t.Errorf("identity fixture %q source = %q, want %q",
					fixture.ID, outcome.Projected.Source, session.SourceLocalIdentity)
			}
			// Structural half: the row must be unable to reach the identity code
			// through tools at all.
			if len(fixture.ToolEvidence) != 0 {
				t.Errorf("identity fixture %q carries %d tool evidence entries; a row whose positive could come from tools cannot certify requirement 3.1 alone",
					fixture.ID, len(fixture.ToolEvidence))
			}

		case crossHarnessTooling:
			toolingRows++
			// Requirement 3.3: canonical tool categories alone, with no client
			// identity at all.
			outcome := rig.serveCanonical(t, clientSessionID, sessionID, resumeToken, fixture,
				"", crossHarnessToolDefs(fixture))
			if !outcome.Projected.IsCodingAgent() || outcome.Projected.Evidence != code {
				t.Errorf("tool-structure fixture %q with NO client identity = %+v, want coding_agent/%q",
					fixture.ID, outcome.Projected, code)
			}
			if outcome.Projected.Source != session.SourceLocalTooling {
				t.Errorf("tool-structure fixture %q source = %q, want %q",
					fixture.ID, outcome.Projected.Source, session.SourceLocalTooling)
			}
			// Requirement 12.1: the row's own identity must not be a recognized
			// coding-harness family, or its positive would not be tool-derived. A
			// generic SDK or absent User-Agent is exactly the requirement 3.2 case.
			if fixture.IdentityValue != "" {
				if match, ok := agentfacts.MatchIdentity(fixture.IdentityValue); ok {
					t.Errorf("tool-structure fixture %q carries identity %q matching family %q at %q confidence; its positive is not independent of Rule A",
						fixture.ID, fixture.IdentityValue, match.Family, match.Confidence)
				}
			}
		}
	}
	if identityRows == 0 || toolingRows == 0 {
		t.Fatalf("matrix carries identity=%d tool-structure=%d positive rows, want both non-empty", identityRows, toolingRows)
	}
	t.Logf("isolated positives: %d identity rows classified with no tools, %d tool-structure rows classified with no client identity",
		identityRows, toolingRows)
}

// TestCrossHarnessCensusRejectsSkippedAndFilteredFixtures is the non-vacuity
// control for the census every acceptance assertion depends on. It runs the same
// runner with filters that would silently drop rows - exactly one row, and an
// "only harnesses the matrix documents as high confidence" filter that would
// discard every tool-structure and every ambiguous/unsupported row - and requires
// the census to REJECT each run while naming the rows it never executed. A census
// that accepted a filtered run would let a skipped row certify nothing while the
// suite still passed.
func TestCrossHarnessCensusRejectsSkippedAndFilteredFixtures(t *testing.T) {
	t.Parallel()

	matrix := crossHarnessMatrix(t)
	if len(matrix.Fixtures) < 3 {
		t.Fatalf("matrix has %d rows, want at least 3 to exercise a filter", len(matrix.Fixtures))
	}
	skippedID := matrix.Fixtures[0].ID
	recognizeOnly := crossHarnessHighConfidenceHarnessFilter(matrix)

	cases := []struct {
		name      string
		selected  func(testfixtures.EvidenceFixture) bool
		wantNamed string
		wantKept  int
	}{
		{
			name: "skip exactly one row",
			selected: func(fixture testfixtures.EvidenceFixture) bool {
				return fixture.ID != skippedID
			},
			wantNamed: skippedID,
			wantKept:  len(matrix.Fixtures) - 1,
		},
		{
			name:      "keep only harnesses documented as high confidence",
			selected:  recognizeOnly,
			wantNamed: crossHarnessFirstDroppedID(matrix, recognizeOnly),
		},
	}

	harness := newCrossHarnessHarness(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := harness.runMatrix(t, matrix, tc.selected)
			if run.Err == nil {
				t.Fatal("the census accepted a run that skipped matrix rows; skipped fixtures would certify nothing while passing")
			}
			if !strings.Contains(run.Err.Error(), tc.wantNamed) {
				t.Fatalf("census failure does not name the skipped fixture %q: %v", tc.wantNamed, run.Err)
			}
			if tc.wantKept > 0 {
				if got := len(run.Exec); got != tc.wantKept {
					t.Fatalf("filtered run executed %d rows, want %d", got, tc.wantKept)
				}
				for id := range run.Exec {
					if id == tc.wantNamed {
						t.Fatalf("filtered run executed the row it was supposed to skip: %q", id)
					}
				}
			}
			t.Logf("census rejected the filtered run: %v", run.Err)
		})
	}
}

// crossHarnessHighConfidenceHarnessFilter is the tempting shortcut that would
// silently drop everything the matrix does not document as a stable identity: all
// tool-structure rows and every ambiguous or unsupported harness. The census must
// refuse it.
func crossHarnessHighConfidenceHarnessFilter(matrix testfixtures.Matrix) func(testfixtures.EvidenceFixture) bool {
	return func(fixture testfixtures.EvidenceFixture) bool {
		if fixture.Harness == "" {
			return false
		}
		support := crossHarnessSupport(matrix, fixture.Harness)
		return support != nil && support.Status == testfixtures.IdentityHighConfidence
	}
}

// crossHarnessFirstDroppedID returns a row the given filter drops, so the census
// failure can be shown to name a genuinely skipped row.
func crossHarnessFirstDroppedID(
	matrix testfixtures.Matrix,
	filter func(testfixtures.EvidenceFixture) bool,
) string {
	for _, fixture := range matrix.Fixtures {
		if !filter(fixture) {
			return fixture.ID
		}
	}
	return ""
}

// TestCrossHarnessCensusErrorDetectsSkippedRowWithoutRunningAnything proves the
// census check itself can fail, independent of the composed harness: a zero-count
// census over a non-empty matrix, a repeated row, and an executed row that is not
// in the matrix are all reported rather than accepted.
func TestCrossHarnessCensusErrorDetectsSkippedRowWithoutRunningAnything(t *testing.T) {
	t.Parallel()

	matrix := crossHarnessMatrix(t)
	if err := crossHarnessCensusError(map[string]int{}, matrix.Fixtures); err == nil {
		t.Fatal("an empty census was accepted over a non-empty matrix")
	} else if !strings.Contains(err.Error(), matrix.Fixtures[0].ID) {
		t.Fatalf("an empty census failure does not name the first fixture: %v", err)
	}

	executed := make(map[string]int, len(matrix.Fixtures)+1)
	for _, fixture := range matrix.Fixtures {
		executed[fixture.ID] = 1
	}
	if err := crossHarnessCensusError(executed, matrix.Fixtures); err != nil {
		t.Fatalf("a complete census was rejected: %v", err)
	}

	executed[matrix.Fixtures[1].ID] = 2
	err := crossHarnessCensusError(executed, matrix.Fixtures)
	if err == nil {
		t.Fatal("a census that executed one row twice was accepted")
	}
	if !strings.Contains(err.Error(), matrix.Fixtures[1].ID) {
		t.Fatalf("a repeated-row census failure does not name the row: %v", err)
	}

	delete(executed, matrix.Fixtures[1].ID)
	executed["not_in_matrix"] = 1
	err = crossHarnessCensusError(executed, matrix.Fixtures)
	if err == nil {
		t.Fatal("a census that skipped a row and invented one was accepted")
	}
	for _, want := range []string{matrix.Fixtures[1].ID, "not_in_matrix"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("census failure does not name %q: %v", want, err)
		}
	}
}

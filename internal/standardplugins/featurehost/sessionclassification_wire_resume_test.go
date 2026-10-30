package featurehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// certificationWireSource is a bounded large-body capture. The wire lane never
// materializes a canonical Call, so the request body is reachable only through
// this source (requirements 5.2, 5.5).
type certificationWireSource struct {
	digest largebody.SourceDigest
	size   int64
	data   []byte
}

func (s *certificationWireSource) Size() int64                          { return s.size }
func (s *certificationWireSource) Digest() largebody.SourceDigest       { return s.digest }
func (s *certificationWireSource) SourceDigest() largebody.SourceDigest { return s.digest }
func (s *certificationWireSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *certificationWireSource) Close() error { return nil }

func certificationNewWireSource(t *testing.T, body string) *certificationWireSource {
	t.Helper()
	data := []byte(body)
	return &certificationWireSource{
		digest: largebody.NewSourceDigest(sha256.Sum256(data)),
		size:   int64(len(data)),
		data:   data,
	}
}

// certificationWireAssessment builds the accepted assessment a certified frontend
// profile hands to the wire lane. It carries no canonical Call anywhere.
func certificationWireAssessment(t *testing.T, source *certificationWireSource) largebody.Assessment {
	t.Helper()
	stamp, err := largebody.NewAssessmentStamp(
		"certification-generation-1",
		"openai-chat",
		source.digest,
		source.size,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(sha256.Sum256([]byte("certification-wire-identity"))),
		"certification-domain-generation-1",
	)
	if err != nil {
		t.Fatalf("new assessment stamp: %v", err)
	}
	wireFacts := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 2048,
	}
	domainFacts := largebody.WireDomainFacts{
		ProfileID:      "openai-chat",
		Operation:      aipapi.OperationOpenAIChatCompletions,
		Delivery:       aipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		Rewrite:        largebody.NewNoRewrite(),
		UniversalModel: true,
	}
	assessment, err := largebody.NewAcceptedAssessment(stamp, wireFacts, domainFacts)
	if err != nil {
		t.Fatalf("new accepted assessment: %v", err)
	}
	return assessment
}

// certificationServeWire runs one committed wire turn and returns the
// proxy-owned authority carrier the wire lane bound. Only bounded proof facts are
// handed to the executor: session input plus classification evidence, no header
// bag, no tool list, no transcript.
func certificationServeWire(
	t *testing.T,
	executor *runtime.Executor,
	sessionInput largebody.SessionInput,
	userAgent string,
	body string,
	requestID string,
) largebody.SessionResponseCarrier {
	t.Helper()
	source := certificationNewWireSource(t, body)
	assessment := certificationWireAssessment(t, source)
	proof := largebody.Proof{
		ProfileID: "openai-chat",
		Operation: aipapi.OperationOpenAIChatCompletions,
		Delivery:  aipapi.DeliveryModeStreaming,
		Identity:  largebody.NewIdentityDigest([32]byte{9, 9, 9, 9}),
		Session:   sessionInput,
		ClassificationEvidence: sdkclassification.Evidence{
			Operation:       aipapi.OperationOpenAIChatCompletions,
			ClientUserAgent: userAgent,
		},
	}
	ctx := largebody.ContextWithWireProof(context.Background(), proof, requestID)
	result, err := executor.ExecuteLargeBody(ctx, assessment, source)
	if err != nil {
		t.Fatalf("ExecuteLargeBody: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), result.Stream); err != nil {
		t.Fatalf("collect wire turn: %v", err)
	}
	if result.Session.AuthoritativeSessionID == "" {
		t.Fatal("the wire turn committed without a proxy-owned authoritative session")
	}
	return result.Session
}

// certificationNewWireSession is the bounded session input for a brand-new wire
// turn.
func certificationNewWireSession(clientSessionID string) largebody.SessionInput {
	return largebody.SessionInput{
		ClientSessionID:     clientSessionID,
		NewSessionRequested: true,
	}
}

// certificationResumedWireSession rebuilds the resume input from a previous
// committed wire turn, so the next turn continues the same proxy-owned session.
func certificationResumedWireSession(t *testing.T, carrier largebody.SessionResponseCarrier, clientSessionID string) largebody.SessionInput {
	t.Helper()
	token := carrier.ResumeToken.Reveal()
	if token == "" {
		t.Fatal("the first wire turn issued no resume token, so no turn could continue its session")
	}
	return largebody.SessionInput{
		AuthoritativeSessionID: carrier.AuthoritativeSessionID,
		ClientSessionID:        clientSessionID,
		ResumeToken:            largebody.NewSensitiveString(token),
	}
}

// TestSessionClassificationWireLaneSharesDurableResumeAndReloadState certifies
// the wire half of task 9.1: the metadata-only wire classification stage writes
// through the same shared, durable, process-owned state as the canonical lane, so
// a wire-promoted session survives a real close/reopen of that state and is
// restored by the restarted generation.
//
// The wire lane has no same-turn SessionView consumer (every requirement-4.2
// consumer is canonical-required and statically blocks the wire lane), so the
// ordering half of requirement 2.7 is not observable there. What is observable,
// and what this test certifies, is that the wire lane neither loses nor
// re-promotes the shared durable classification across a restart: the restored
// value comes from durable state (the production observation says "restored", not
// "promoted"), an unrelated wire session with the same weak evidence stays
// unknown, and the durable row is never rewritten.
func TestSessionClassificationWireLaneSharesDurableResumeAndReloadState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-wire-resume.db"))
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"refactor the repository"}]}`

	// ---- first process: the wire lane promotes a durable positive ----
	firstDB := certificationOpenSQLite(t, dsn)
	firstProcess, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: firstDB})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	first := certificationCompile(t, firstProcess, true, "")
	if first.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	first.start(t)
	wireExecutor, wireOpens := certificationExecutor(t, firstDB, first.planes)

	firstWireCarrier := certificationServeWire(t, wireExecutor,
		certificationNewWireSession("client-wire-resume"),
		certificationPositiveUserAgent, body, "req-wire-resume")
	wireSessionID := firstWireCarrier.AuthoritativeSessionID
	if got := wireOpens.Load(); got != 1 {
		t.Fatalf("wire turn opened the backend %d times, want exactly 1 committed execution", got)
	}
	persisted, found := certificationLoadRow(t, firstDB, wireSessionID)
	if !found || !persisted.Classification.IsCodingAgent() {
		t.Fatalf("wire durable row = %+v (found=%t), want the wire stage's persisted positive", persisted.Classification, found)
	}
	persistedPositive := persisted.Classification
	if persistedPositive.Evidence != "client_family.codex" || persistedPositive.Revision != 1 {
		t.Fatalf("wire classification = %+v, want the single local identity promotion", persistedPositive)
	}

	// ---- restart: stop the generation, close the process, close the database ----
	first.stop(t)
	if err := firstProcess.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	// ---- second process: the wire lane restores it from durable state ----
	secondDB := certificationOpenSQLite(t, dsn)
	secondRegistry := prometheus.NewRegistry()
	secondProcess, err := NewProcess(ctx, ProcessInput{
		Logger:          slog.Default(),
		BunDB:           secondDB,
		MetricsRegistry: secondRegistry,
	})
	if err != nil {
		t.Fatalf("NewProcess after restart: %v", err)
	}
	t.Cleanup(func() {
		if err := secondProcess.Close(); err != nil {
			t.Errorf("close second process: %v", err)
		}
	})
	second := certificationCompile(t, secondProcess, true, "")
	if second.classifier == nil {
		t.Fatal("restarted generation published no classifier")
	}
	second.start(t)
	restoredExecutor, restoredOpens := certificationExecutor(t, secondDB, second.planes)

	resumedWireCarrier := certificationServeWire(t, restoredExecutor,
		certificationResumedWireSession(t, firstWireCarrier, "client-wire-resume"),
		certificationWeakUserAgent, body, "req-wire-restore")
	if resumedWireCarrier.AuthoritativeSessionID != wireSessionID {
		t.Fatalf("wire lane rebound the session to %q, want the promoted authority %q",
			resumedWireCarrier.AuthoritativeSessionID, wireSessionID)
	}
	if got := restoredOpens.Load(); got != 1 {
		t.Fatalf("restored wire turn opened the backend %d times, want exactly 1", got)
	}
	// The production observation is the load-bearing part: the restarted wire turn
	// restored the persisted positive from durable state instead of promoting it
	// again, which is what proves the wire lane shares the durable store.
	if got := certificationEvaluations(t, secondRegistry); got["heuristic/restored"] != 1 || len(got) != 1 {
		t.Fatalf("wire evaluations after the restart = %v, want exactly one heuristic/restored", got)
	}
	afterRestore, found := certificationLoadRow(t, secondDB, wireSessionID)
	if !found || afterRestore != persisted {
		t.Fatalf("wire durable row after the restore = %+v (found=%t), want it unchanged at %+v", afterRestore.Classification, found, persistedPositive)
	}

	// Negative control: an unrelated wire session with the same weak evidence must
	// stay unknown, so the restore above cannot have come from wire-local state.
	controlCarrier := certificationServeWire(t, restoredExecutor,
		certificationNewWireSession("client-wire-control"),
		certificationWeakUserAgent, body, "req-wire-control")
	controlSessionID := controlCarrier.AuthoritativeSessionID
	if controlSessionID == wireSessionID {
		t.Fatal("the wire control turn reused the promoted session's authority")
	}
	if got := certificationEvaluations(t, secondRegistry); got["heuristic/restored"] != 1 || got["heuristic/unknown"] != 1 {
		t.Fatalf("wire evaluations after the control turn = %v, want one restore and one unknown", got)
	}
	if row, found := certificationLoadRow(t, secondDB, controlSessionID); found && row.Classification != (session.Classification{}) {
		t.Fatalf("the wire control session persisted %+v, want no durable classification", row.Classification)
	}

	// The wire lane shares the canonical lane's durable store, and only the
	// promoted authority owns a row: the weak-evidence control session must have
	// left no durable classification behind (requirement 1.5, 6.9).
	assertOnlyAuthorityHasDurableRow(t, secondDB, wireSessionID)
}

// assertOnlyAuthorityHasDurableRow is a census rather than a spot check: the
// feature table must contain exactly one row, for the promoted authority.
func assertOnlyAuthorityHasDurableRow(t *testing.T, database *bun.DB, scopeID string) {
	t.Helper()
	var scopeIDs []string
	if err := database.NewRaw("SELECT scope_id FROM session_classification").Scan(context.Background(), &scopeIDs); err != nil {
		t.Fatalf("census durable classification rows: %v", err)
	}
	if len(scopeIDs) != 1 || scopeIDs[0] != scopeID {
		t.Fatalf("durable classification rows = %v, want exactly [%s]", scopeIDs, scopeID)
	}
}

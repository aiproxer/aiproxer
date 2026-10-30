package proxymemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// Capture fixtures. The principal, A-leg, project and content values are
// distinctive so the log-hygiene case can prove none of them reaches a log.
const (
	captureTestTenant    = "tenant-capture"
	captureTestPrincipal = "alice-capture-principal"
	captureTestALeg      = "aleg-capture-1"
	captureTestProject   = "/w/app"
	captureTestUserText  = "question sk-ABCDEFGHIJKLMNOPQRSTUVWX"
	captureTestOutput    = "answer token=supersecretvalue123"
)

// captureFakeClock is a deterministic, mutex-guarded clock.
type captureFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newCaptureFakeClock() *captureFakeClock {
	return &captureFakeClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *captureFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *captureFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// captureStoreStub records UpsertSession calls and answers with a fixed
// outcome or error. The embedded Store stays nil: capture must never call any
// other method, so an unexpected call panics loudly.
type captureStoreStub struct {
	Store
	mu      sync.Mutex
	calls   []SessionCapture
	outcome UpsertOutcome
	err     error
	seq     int64
}

func (s *captureStoreStub) ReadAdmission(ctx context.Context, q AdmissionQuery) (AdmissionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	project := Project{Key: ProjectKey(captureTestProject), Label: captureTestProject}
	if q.CachedProject != nil {
		project = *q.CachedProject
	}
	if q.AuthoritativeFirstTurn {
		project = q.Candidate
		if project.Key == "" {
			return AdmissionSnapshot{NextBSeq: s.seq - 1}, nil
		}
		return AdmissionSnapshot{Project: project, NextBSeq: s.seq - 1}, nil
	}
	return AdmissionSnapshot{
		Bound:    true,
		Project:  project,
		Decision: &FirstTurnDecision{Outcome: FirstTurnNoSummary},
		NextBSeq: s.seq - 1,
	}, nil
}

func (*captureStoreStub) CommitFirstTurn(context.Context, FirstTurnJob) (bool, error) {
	return true, nil
}

func (s *captureStoreStub) UpsertSession(_ context.Context, c SessionCapture) (UpsertOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	return s.outcome, s.err
}

func (s *captureStoreStub) upserts() []SessionCapture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionCapture(nil), s.calls...)
}

// captureDeadlineStore is a context-cooperative store stub: every write blocks
// until its context is done. The first write hands its context to the test, so
// the test can prove which deadline cancelled the in-flight write.
type captureDeadlineStore struct {
	Store
	mu    sync.Mutex
	calls int
	first chan context.Context
}

func newCaptureDeadlineStore() *captureDeadlineStore {
	return &captureDeadlineStore{first: make(chan context.Context, 1)}
}

func (*captureDeadlineStore) ReadAdmission(ctx context.Context, _ AdmissionQuery) (AdmissionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionSnapshot{}, err
	}
	return AdmissionSnapshot{
		Bound:    true,
		Project:  Project{Key: ProjectKey(captureTestProject), Label: captureTestProject},
		Decision: &FirstTurnDecision{Outcome: FirstTurnNoSummary},
		NextBSeq: 0,
	}, nil
}

func (s *captureDeadlineStore) UpsertSession(ctx context.Context, _ SessionCapture) (UpsertOutcome, error) {
	s.mu.Lock()
	s.calls++
	if s.calls == 1 {
		s.first <- ctx
	}
	s.mu.Unlock()
	<-ctx.Done()
	return UpsertStored, ctx.Err()
}

func (s *captureDeadlineStore) upsertCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// captureLogCapture is a slog handler that retains every record.
type captureLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureLogCapture) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *captureLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLogCapture) WithGroup(string) slog.Handler      { return h }

func (h *captureLogCapture) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func (h *captureLogCapture) warnRecords() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, record := range h.records {
		if record.Level >= slog.LevelWarn {
			out = append(out, record)
		}
	}
	return out
}

// captureRecordText flattens a record's message and attribute values into one
// string for content scans; nested groups are walked.
func captureRecordText(record slog.Record) string {
	var b strings.Builder
	b.WriteString(record.Message)
	record.Attrs(func(a slog.Attr) bool {
		captureAppendAttr(&b, a)
		return true
	})
	return b.String()
}

func captureAppendAttr(b *strings.Builder, a slog.Attr) {
	if a.Value.Kind() == slog.KindGroup {
		for _, inner := range a.Value.Group() {
			captureAppendAttr(b, inner)
		}
		return
	}
	b.WriteString(" ")
	b.WriteString(a.Key)
	b.WriteString("=")
	b.WriteString(a.Value.String())
}

// captureTestHarness wires one capture over stubs and a capturing log handler.
type captureTestHarness struct {
	capture *Capture
	store   *captureStoreStub
	clock   *captureFakeClock
	metrics *Metrics
	logs    *captureLogCapture
}

func newCaptureTestHarness(t *testing.T, cfg Config) *captureTestHarness {
	t.Helper()
	h := &captureTestHarness{
		store:   &captureStoreStub{},
		clock:   newCaptureFakeClock(),
		metrics: NewPrometheusCollector(),
		logs:    &captureLogCapture{},
	}
	h.capture = NewCapture(cfg, h.store, h.metrics, h.clock.Now, slog.New(h.logs), nil)
	return h
}

// captureTestConfig enables capture for the capture principal.
func captureTestConfig() Config {
	cfg := DefaultConfig()
	cfg.Principals = []string{captureTestPrincipal}
	cfg.ExtractRoute = "route-extract"
	cfg.ConsolidateRoute = "route-consolidate"
	return cfg
}

// captureTestScope is an eligible human scope for the capture principal.
func captureTestScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(captureTestPrincipal),
		TenantID:    scope.Known(captureTestTenant),
		Origin:      scope.OriginClient,
	}
}

// captureTestMeta builds eligible request metadata for one trace and A-leg.
func captureTestMeta(traceID, aLegID string) request.RequestMeta {
	return request.RequestMeta{
		TraceID: traceID,
		Scope:   captureTestScope(),
		Session: session.SessionView{AuthoritativeSessionID: "sess-capture", ALegID: aLegID},
	}
}

// captureTestStreamMeta builds the observer metadata for one trace and A-leg.
func captureTestStreamMeta(traceID, aLegID string) response.StreamMeta {
	return response.StreamMeta{TraceID: traceID, ALegID: aLegID}
}

// captureTestCall is one eligible request: a user question carrying a secret,
// behind a workspace extension hint. System content is excluded by design.
func captureTestCall(t *testing.T) *aipapi.Call {
	t.Helper()
	return &aipapi.Call{
		Extensions: map[string]json.RawMessage{"workspace_path": rawExtension(t, captureTestProject)},
		Messages: []aipapi.Message{
			textMessage(aipapi.RoleSystem, "system text"),
			textMessage(aipapi.RoleUser, captureTestUserText),
		},
	}
}

// captureTestHistoryEntries is the redacted history of captureTestCall.
func captureTestHistoryEntries() []Entry {
	return []Entry{{Type: "message", Role: "user", Text: "question [REDACTED_SECRET]"}}
}

// captureCounter reads one bounded event counter.
func captureCounter(t *testing.T, m *Metrics, event string) float64 {
	t.Helper()
	return testutil.ToFloat64(m.events.WithLabelValues(event))
}

// captureTranscriptEntries validates and decodes one stored transcript.
func captureTranscriptEntries(t *testing.T, transcript string) []Entry {
	t.Helper()
	if !json.Valid([]byte(transcript)) {
		t.Fatalf("Transcript is not valid JSON: %q", transcript)
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(transcript), &entries); err != nil {
		t.Fatalf("decode Transcript: %v", err)
	}
	return entries
}

// captureAssertAssistantTranscript compares one stored transcript with the
// expected history followed by the expected assistant output, and fails when
// the raw text of that output is still present.
func captureAssertAssistantTranscript(t *testing.T, transcript, assistantText string) {
	t.Helper()
	entries := captureTranscriptEntries(t, transcript)
	wantEntries := append(captureTestHistoryEntries(), Entry{Type: "message", Role: "assistant", Text: assistantText})
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("Transcript entries mismatch\n got: %#v\nwant: %#v", entries, wantEntries)
	}
}

// TestCapturePlaneIdentity pins the design §9 plane identity: the transform and
// observer factory share ID "proxy-memory.capture", order 1000 and FailOpen,
// and the capture constants match the §9 numbers.
func TestCapturePlaneIdentity(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	transform := h.capture.Transform()
	factory := h.capture.ObserverFactory()
	if transform.ID() != CaptureHandlerID || transform.Order() != 1000 || transform.FailureMode() != sdkhooks.FailOpen {
		t.Fatalf("transform = (%q, %d, %v), want (%q, 1000, FailOpen)",
			transform.ID(), transform.Order(), transform.FailureMode(), CaptureHandlerID)
	}
	if factory.ID() != CaptureHandlerID || factory.Order() != 1000 || factory.FailureMode() != sdkhooks.FailOpen {
		t.Fatalf("observer factory = (%q, %d, %v), want (%q, 1000, FailOpen)",
			factory.ID(), factory.Order(), factory.FailureMode(), CaptureHandlerID)
	}
	if capturePendingCap != 1024 || captureQueueCap != 256 || capturePendingTTL != 10*time.Minute || captureWriteTimeout != 5*time.Second {
		t.Fatalf("capture constants = (pending %d, queue %d, ttl %v, write %v), want (1024, 256, 10m, 5s)",
			capturePendingCap, captureQueueCap, capturePendingTTL, captureWriteTimeout)
	}
}

// capturePendingCount reports the pending-map size under its mutex.
func capturePendingCount(c *Capture) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// captureRunTurn drives one transform, observer and finish of the capture
// harness, returning the observer for extra observations.
func captureRunTurn(t *testing.T, h *captureTestHarness, traceID, aLegID string) response.StreamObserver {
	t.Helper()
	ctx := context.Background()
	if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta(traceID, aLegID), request.Services{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	obs, err := h.capture.Open(ctx, captureTestStreamMeta(traceID, aLegID), response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return obs
}

func TestCaptureEligibleTurnRecordsRedactedTranscript(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer "}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "token=supersecretvalue123"}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	h.clock.Advance(2 * time.Second)
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	upserts := h.store.upserts()
	if len(upserts) != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1", len(upserts))
	}
	got := upserts[0]
	wantOwner := Owner{TenantID: captureTestTenant, PrincipalID: captureTestPrincipal, ProjectKey: ProjectKey(captureTestProject)}
	if got.Owner != wantOwner {
		t.Fatalf("Owner = %+v, want %+v", got.Owner, wantOwner)
	}
	if got.ProjectLabel != captureTestProject {
		t.Fatalf("ProjectLabel = %q, want %q", got.ProjectLabel, captureTestProject)
	}
	if got.ALegID != captureTestALeg {
		t.Fatalf("ALegID = %q, want %q", got.ALegID, captureTestALeg)
	}
	if want := h.clock.Now().UnixMilli(); got.ActivityMS != want {
		t.Fatalf("ActivityMS = %d, want %d", got.ActivityMS, want)
	}
	captureAssertAssistantTranscript(t, got.Transcript, "answer token=[REDACTED_SECRET]")
	if strings.Contains(got.Transcript, "sk-ABCDEFGHIJKLMNOPQRSTUVWX") || strings.Contains(got.Transcript, "supersecretvalue123") {
		t.Fatalf("Transcript retained a raw secret: %q", got.Transcript)
	}
	if want := 1.0; captureCounter(t, h.metrics, EventCaptureRecorded) != want {
		t.Fatalf("%s = %v, want %v", EventCaptureRecorded, captureCounter(t, h.metrics, EventCaptureRecorded), want)
	}
	if records := h.logs.snapshot(); len(records) != 0 {
		t.Fatalf("success path logged %d records, want none: %v", len(records), records)
	}
}

func TestCaptureIneligibleOrProjectlessLeavesNoTrace(t *testing.T) {
	t.Parallel()

	ineligible := captureTestScope()
	ineligible.PrincipalID = scope.Known("eve-outsider")
	ineligibleMeta := captureTestMeta("trace-1", captureTestALeg)
	ineligibleMeta.Scope = ineligible
	projectless := &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleUser, "no project here")}}
	projectlessMeta := captureTestMeta("trace-1", captureTestALeg)
	projectlessMeta.Session.AuthoritativeFirstTurn = true

	cases := []struct {
		name string
		meta request.RequestMeta
		call *aipapi.Call
	}{
		{name: "ineligible scope", meta: ineligibleMeta, call: captureTestCall(t)},
		{name: "no project", meta: projectlessMeta, call: projectless},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newCaptureTestHarness(t, captureTestConfig())
			ctx := context.Background()
			if err := h.capture.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := h.capture.Handle(ctx, tc.call, tc.meta, request.Services{}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := capturePendingCount(h.capture); got != 0 {
				t.Fatalf("pending entries = %d, want 0", got)
			}
			obs, err := h.capture.Open(ctx, captureTestStreamMeta(tc.meta.TraceID, tc.meta.Session.ALegID), response.Services{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "output"}); err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			if err := h.capture.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if got := len(h.store.upserts()); got != 0 {
				t.Fatalf("UpsertSession calls = %d, want 0", got)
			}
			if got := captureCounter(t, h.metrics, EventCaptureDroppedQueue); got != 0 {
				t.Fatalf("%s = %v, want 0", EventCaptureDroppedQueue, got)
			}
		})
	}
}

func TestCaptureNonReleasedOutcomesRecordNothing(t *testing.T) {
	t.Parallel()

	outcomes := []response.StreamOutcome{
		response.OutcomeFailed,
		response.OutcomeCancelled,
		response.OutcomeClosed,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	}
	for _, outcome := range outcomes {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			h := newCaptureTestHarness(t, captureTestConfig())
			ctx := context.Background()
			if err := h.capture.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
			if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "failed attempt output"}); err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if err := obs.Finish(ctx, outcome); err != nil {
				t.Fatalf("Finish(%s): %v", outcome, err)
			}
			if err := h.capture.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if got := len(h.store.upserts()); got != 0 {
				t.Fatalf("Outcome %s recorded %d upserts, want 0", outcome, got)
			}
			if got := capturePendingCount(h.capture); got != 1 {
				t.Fatalf("pending entries = %d, want 1: a failed attempt must not discard the turn for a later retry", got)
			}
		})
	}
}

func TestCaptureFailoverRecordsOnlyTheReleasedAttempt(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := first.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "first attempt output"}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := first.Finish(ctx, response.OutcomeFailed); err != nil {
		t.Fatalf("Finish(first): %v", err)
	}
	second := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := second.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "second attempt output"}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := second.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish(second): %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	upserts := h.store.upserts()
	if len(upserts) != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1", len(upserts))
	}
	captureAssertAssistantTranscript(t, upserts[0].Transcript, "second attempt output")
	if strings.Contains(upserts[0].Transcript, "first attempt output") {
		t.Fatalf("Transcript kept a failed attempt's output: %q", upserts[0].Transcript)
	}
}

// TestCaptureFailoverReusesSingleTransformSnapshot pins that every observer of
// one turn opens over the snapshot of one transform (design §9) and that a
// failed attempt must not remove the pending state the released attempt still
// needs: unlike the failover fixture above, exactly one Handle feeds both
// Opens.
func TestCaptureFailoverReusesSingleTransformSnapshot(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta("trace-1", captureTestALeg), request.Services{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	failed, err := h.capture.Open(ctx, captureTestStreamMeta("trace-1", captureTestALeg), response.Services{})
	if err != nil {
		t.Fatalf("Open(failed attempt): %v", err)
	}
	if err := failed.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "first attempt output"}); err != nil {
		t.Fatalf("Observe(failed attempt): %v", err)
	}
	if err := failed.Finish(ctx, response.OutcomeFailed); err != nil {
		t.Fatalf("Finish(failed attempt): %v", err)
	}
	released, err := h.capture.Open(ctx, captureTestStreamMeta("trace-1", captureTestALeg), response.Services{})
	if err != nil {
		t.Fatalf("Open(released attempt): %v", err)
	}
	if err := released.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "second attempt output"}); err != nil {
		t.Fatalf("Observe(released attempt): %v", err)
	}
	if err := released.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish(released attempt): %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	upserts := h.store.upserts()
	if len(upserts) != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1", len(upserts))
	}
	captureAssertAssistantTranscript(t, upserts[0].Transcript, "second attempt output")
	if strings.Contains(upserts[0].Transcript, "first attempt output") {
		t.Fatalf("Transcript kept the failed attempt's output: %q", upserts[0].Transcript)
	}
	if got := capturePendingCount(h.capture); got != 0 {
		t.Fatalf("pending entries after the released finish = %d, want 0", got)
	}
}

func TestCaptureParallelBranchesRecordOneWinner(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	loser := captureRunTurn(t, h, "trace-1", captureTestALeg)
	winner := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := loser.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "loser output"}); err != nil {
		t.Fatalf("observe loser: %v", err)
	}
	if err := winner.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "winner output"}); err != nil {
		t.Fatalf("observe winner: %v", err)
	}
	if err := loser.Finish(ctx, response.OutcomeFailed); err != nil {
		t.Fatalf("finish loser: %v", err)
	}
	if err := winner.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("finish winner: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	upserts := h.store.upserts()
	if len(upserts) != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1", len(upserts))
	}
	captureAssertAssistantTranscript(t, upserts[0].Transcript, "winner output")
	if strings.Contains(upserts[0].Transcript, "loser output") {
		t.Fatalf("Transcript kept the losing branch's output: %q", upserts[0].Transcript)
	}
}

func TestCaptureWithoutEstablishedSessionIsSkipped(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	meta := captureTestMeta("trace-1", captureTestALeg)
	meta.Session = session.SessionView{ALegID: captureTestALeg}
	if err := h.capture.Handle(ctx, captureTestCall(t), meta, request.Services{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := capturePendingCount(h.capture); got != 0 {
		t.Fatalf("pending entries = %d, want 0", got)
	}
	if got := captureCounter(t, h.metrics, EventCaptureNoSession); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureNoSession, got)
	}
	obs, err := h.capture.Open(ctx, captureTestStreamMeta("trace-1", captureTestALeg), response.Services{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := len(h.store.upserts()); got != 0 {
		t.Fatalf("UpsertSession calls = %d, want 0", got)
	}
}

func TestCaptureFinishAfterStopReturnsNil(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish after Stop = %v, want nil", err)
	}
	if got := capturePendingCount(h.capture); got != 0 {
		t.Fatalf("pending entries = %d, want 0 after a released finish", got)
	}
}

func TestCaptureQueueFullDropsWithoutBlocking(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	for i := range captureQueueCap {
		traceID := fmt.Sprintf("trace-fill-%04d", i)
		aLegID := fmt.Sprintf("aleg-fill-%04d", i)
		obs := captureRunTurn(t, h, traceID, aLegID)
		if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
			t.Fatalf("Finish(%s): %v", traceID, err)
		}
	}
	if got := len(h.capture.queue); got != captureQueueCap {
		t.Fatalf("queue length = %d, want %d", got, captureQueueCap)
	}
	overflow := captureRunTurn(t, h, "trace-overflow", "aleg-overflow")
	if err := overflow.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish(overflow) = %v, want nil (must not block or fail)", err)
	}
	if got := captureCounter(t, h.metrics, EventCaptureDroppedQueue); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureDroppedQueue, got)
	}
	if got := len(h.capture.queue); got != captureQueueCap {
		t.Fatalf("queue length after drop = %d, want %d", got, captureQueueCap)
	}
	if got := len(h.store.upserts()); got != 0 {
		t.Fatalf("UpsertSession calls = %d, want 0 (writer not started)", got)
	}
	if warns := h.logs.warnRecords(); len(warns) != 1 {
		t.Fatalf("warn records = %d, want 1: %v", len(warns), warns)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop before Start = %v, want nil", err)
	}
}

func TestCaptureTruncatesAndLogsWithoutContent(t *testing.T) {
	t.Parallel()

	cfg := captureTestConfig()
	cfg.MaxTranscriptBytes = 512
	h := newCaptureTestHarness(t, cfg)
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	const sentinel = "TRUNCATION-CONTENT-SENTINEL"
	obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat(sentinel+" ", 200)}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := captureCounter(t, h.metrics, EventCaptureTruncated); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureTruncated, got)
	}
	if got := captureCounter(t, h.metrics, EventCaptureRecorded); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureRecorded, got)
	}
	upserts := h.store.upserts()
	if len(upserts) != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1", len(upserts))
	}
	if !json.Valid([]byte(upserts[0].Transcript)) {
		t.Fatalf("truncated Transcript is not valid JSON: %q", upserts[0].Transcript)
	}
	if got := len(upserts[0].Transcript); got > cfg.MaxTranscriptBytes {
		t.Fatalf("Transcript is %d bytes, want <= %d", got, cfg.MaxTranscriptBytes)
	}
	warns := h.logs.warnRecords()
	if len(warns) != 1 {
		t.Fatalf("warn records = %d, want 1: %v", len(warns), warns)
	}
	if text := captureRecordText(warns[0]); strings.Contains(text, sentinel) {
		t.Fatalf("truncation log record carries content: %q", text)
	}
}

func TestCapturePendingCapEvictsOldest(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	for i := range capturePendingCap {
		if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta(fmt.Sprintf("trace-cap-%04d", i), fmt.Sprintf("aleg-cap-%04d", i)), request.Services{}); err != nil {
			t.Fatalf("Handle(%d): %v", i, err)
		}
		h.clock.Advance(time.Millisecond)
	}
	if got := capturePendingCount(h.capture); got != capturePendingCap {
		t.Fatalf("pending entries = %d, want %d", got, capturePendingCap)
	}
	if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta("trace-cap-overflow", "aleg-cap-overflow"), request.Services{}); err != nil {
		t.Fatalf("Handle(overflow): %v", err)
	}
	if got := captureCounter(t, h.metrics, EventCaptureDroppedPending); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureDroppedPending, got)
	}
	if got := capturePendingCount(h.capture); got != capturePendingCap {
		t.Fatalf("pending entries after eviction = %d, want %d", got, capturePendingCap)
	}

	// The oldest turn (trace-cap-0000) is gone; the next-oldest survives. A
	// released finish only enqueues a job when an observer found the turn.
	evicted, err := h.capture.Open(ctx, captureTestStreamMeta("trace-cap-0000", "aleg-cap-0000"), response.Services{})
	if err != nil {
		t.Fatalf("Open(evicted): %v", err)
	}
	if err := evicted.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("finish evicted: %v", err)
	}
	if got := len(h.capture.queue); got != 0 {
		t.Fatalf("queue length after finishing the evicted turn = %d, want 0", got)
	}
	kept, err := h.capture.Open(ctx, captureTestStreamMeta("trace-cap-0001", "aleg-cap-0001"), response.Services{})
	if err != nil {
		t.Fatalf("Open(kept): %v", err)
	}
	if err := kept.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("finish kept: %v", err)
	}
	if got := len(h.capture.queue); got != 1 {
		t.Fatalf("queue length after finishing a kept turn = %d, want 1", got)
	}
}

func TestCapturePendingTTLEvictsOnInsert(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta("trace-old", "aleg-old"), request.Services{}); err != nil {
		t.Fatalf("Handle(old): %v", err)
	}
	h.clock.Advance(capturePendingTTL + time.Second)
	if err := h.capture.Handle(ctx, captureTestCall(t), captureTestMeta("trace-new", "aleg-new"), request.Services{}); err != nil {
		t.Fatalf("Handle(new): %v", err)
	}
	if got := captureCounter(t, h.metrics, EventCaptureDroppedPending); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureDroppedPending, got)
	}
	if got := capturePendingCount(h.capture); got != 1 {
		t.Fatalf("pending entries = %d, want 1", got)
	}
	expired, err := h.capture.Open(ctx, captureTestStreamMeta("trace-old", "aleg-old"), response.Services{})
	if err != nil {
		t.Fatalf("Open(expired): %v", err)
	}
	if err := expired.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("finish expired: %v", err)
	}
	if got := len(h.capture.queue); got != 0 {
		t.Fatalf("queue length after finishing the expired turn = %d, want 0", got)
	}
}

func TestCaptureStoreOutcomesCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		outcome UpsertOutcome
		event   string
	}{
		{name: "owner mismatch", outcome: UpsertOwnerMismatch, event: EventCaptureOwnerMismatch},
		{name: "forgotten", outcome: UpsertForgotten, event: EventCaptureForgotten},
		{name: "stale", outcome: UpsertStale, event: "capture_stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newCaptureTestHarness(t, captureTestConfig())
			h.store.outcome = tc.outcome
			ctx := context.Background()
			if err := h.capture.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
			if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			if err := h.capture.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if got := captureCounter(t, h.metrics, tc.event); got != 1 {
				t.Fatalf("%s = %v, want 1", tc.event, got)
			}
			if got := captureCounter(t, h.metrics, EventCaptureRecorded); got != 0 {
				t.Fatalf("%s = %v, want 0", EventCaptureRecorded, got)
			}
			if tc.outcome == UpsertStale {
				found := false
				for _, record := range h.logs.snapshot() {
					text := captureRecordText(record)
					found = found || strings.Contains(text, "capture_stale")
					if strings.Contains(text, captureTestALeg) || strings.Contains(text, captureTestUserText) {
						t.Fatalf("stale capture diagnostic leaked content: %q", text)
					}
				}
				if !found {
					t.Fatal("stale outcome lacks a bounded diagnostic")
				}
			}
		})
	}
}

func TestCaptureTransformDoesNotMutateTheCall(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	call := captureTestCall(t)
	before := aipapi.CloneCall(*call)
	if err := h.capture.Handle(context.Background(), call, captureTestMeta("trace-1", captureTestALeg), request.Services{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !reflect.DeepEqual(*call, before) {
		t.Fatalf("Handle mutated the call\n got: %#v\nwant: %#v", *call, before)
	}
}

// TestCaptureStopDeadlineStopsDrainAndInFlightWrite is the shutdown-budget
// regression for design §9: "drain what is already queued until the stop ctx
// expires". The store write in flight when the stop deadline expires must be
// cancelled by that deadline, not by its own per-write limit, and the job
// still queued behind it must never start.
func TestCaptureStopDeadlineStopsDrainAndInFlightWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCaptureTestHarness(t, captureTestConfig())
		store := newCaptureDeadlineStore()
		h.capture.store = store
		// Two jobs are queued before the writer starts: one write is in
		// flight and one job remains queued when the deadline expires.
		for _, traceID := range []string{"trace-deadline-1", "trace-deadline-2"} {
			obs := captureRunTurn(t, h, traceID, captureTestALeg)
			if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish(%s): %v", traceID, err)
			}
		}
		if err := h.capture.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		inFlight := <-store.first

		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := h.capture.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want %v", err, context.DeadlineExceeded)
		}
		synctest.Wait()

		if inFlight.Err() == nil {
			t.Error("in-flight store write still live after the stop budget expired")
		}
		select {
		case <-h.capture.writerDone:
		default:
			t.Fatal("capture writer still running after the stop budget expired")
		}
		if got := store.upsertCalls(); got != 1 {
			t.Fatalf("UpsertSession calls = %d, want 1: no queued write may start after the stop deadline", got)
		}

		// Far beyond any per-write limit, with the budget long expired: the
		// drain must stay down and start no further write.
		time.Sleep(2 * captureWriteTimeout)
		synctest.Wait()
		if got := store.upsertCalls(); got != 1 {
			t.Fatalf("UpsertSession calls after the stop deadline = %d, want 1", got)
		}
		if err := h.capture.Stop(context.Background()); err != nil {
			t.Fatalf("Stop after the deadline = %v, want nil", err)
		}
	})
}

// captureHeldStore holds the first write behind a channel, then makes every
// later write wait for its context. It counts store entries so a regression
// can prove that no post-budget write ever reaches the store.
type captureHeldStore struct {
	captureStoreStub
	mu      sync.Mutex
	starts  int
	entered chan struct{}
	release chan struct{}
	next    chan context.Context
}

func (s *captureHeldStore) UpsertSession(ctx context.Context, _ SessionCapture) (UpsertOutcome, error) {
	s.mu.Lock()
	s.starts++
	call := s.starts
	s.mu.Unlock()
	switch call {
	case 1:
		close(s.entered)
		<-s.release
		return UpsertStored, nil
	case 2:
		s.next <- ctx
	}
	<-ctx.Done()
	return UpsertStored, ctx.Err()
}

func (s *captureHeldStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// TestCaptureStopDrainAdmitsNoJobAfterExhaustedBudget pins the drain admission
// rule against the exhausted absolute stop deadline: while one drained write
// is still in flight, the job behind it must not be admitted — not even at the
// instant the write context wakes on the deadline before the stop ctx makes
// its own cancellation observable — and the store must never see a post-budget
// write. All waits are virtualized by testing/synctest; the first write is
// held by a channel until Stop has published its three-second budget.
func TestCaptureStopDrainAdmitsNoJobAfterExhaustedBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCaptureTestHarness(t, captureTestConfig())
		store := &captureHeldStore{entered: make(chan struct{}), release: make(chan struct{}), next: make(chan context.Context, 1)}
		h.capture.store = store
		for _, traceID := range []string{"held", "drained", "behind"} {
			obs := captureRunTurn(t, h, traceID, traceID)
			if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish(%s): %v", traceID, err)
			}
		}
		if err := h.capture.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		<-store.entered

		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stopResult := make(chan error, 1)
		go func() { stopResult <- h.capture.Stop(stopCtx) }()
		<-h.capture.done
		close(store.release)

		drained := <-store.next
		deadline, ok := drained.Deadline()
		if !ok || time.Until(deadline) != 3*time.Second {
			t.Fatalf("drained write deadline = %v (ok=%v), want the stop budget's remaining 3s", deadline, ok)
		}
		if err := <-stopResult; err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want nil or %v", err, context.DeadlineExceeded)
		}
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()

		if got := store.calls(); got != 2 {
			t.Fatalf("UpsertSession calls = %d, want 2: the store must never see a post-budget drain write", got)
		}
		if got := len(h.capture.queue); got != 1 {
			t.Fatalf("queue length after the stop deadline = %d, want 1: the job behind the drained write must not be admitted past the deadline", got)
		}
		if err := h.capture.Stop(context.Background()); err != nil {
			t.Fatalf("Stop after the deadline = %v, want nil", err)
		}
	})
}

// TestCaptureStartCancellationStopsWriter pins the lifecycle contract of
// Start: cancelling its context ends the writer — while idle and while a
// cooperative store write is in flight — and closes writerDone, so a retired
// generation owns no goroutine. The budgeted Stop drain path is a separate
// shutdown path and is unchanged.
func TestCaptureStartCancellationStopsWriter(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newCaptureTestHarness(t, captureTestConfig())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := h.capture.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			synctest.Wait()
			cancel()
			synctest.Wait()
			select {
			case <-h.capture.writerDone:
			default:
				t.Fatal("writer still live after its Start context was cancelled")
			}
			if err := h.capture.Stop(context.Background()); err != nil {
				t.Fatalf("Stop after lifecycle cancel = %v, want nil", err)
			}
		})
	})
	t.Run("mid-write", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newCaptureTestHarness(t, captureTestConfig())
			store := newCaptureDeadlineStore()
			h.capture.store = store
			obs := captureRunTurn(t, h, "trace-lifecycle", captureTestALeg)
			if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := h.capture.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			inFlight := <-store.first
			cancel()
			synctest.Wait()
			if !errors.Is(inFlight.Err(), context.Canceled) {
				t.Fatalf("in-flight write ctx = %v, want %v", inFlight.Err(), context.Canceled)
			}
			select {
			case <-h.capture.writerDone:
			default:
				t.Fatal("writer still live after its Start context was cancelled during a write")
			}
			if err := h.capture.Stop(context.Background()); err != nil {
				t.Fatalf("Stop after lifecycle cancel = %v, want nil", err)
			}
		})
	})
}

func TestCaptureStopDrainsAndLeavesNoGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newCaptureTestHarness(t, captureTestConfig())
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "drained output"}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := len(h.store.upserts()); got != 1 {
		t.Fatalf("UpsertSession calls = %d, want 1: Stop must return after the writer drains", got)
	}
	select {
	case <-h.capture.writerDone:
	default:
		t.Fatal("capture writer still running after Stop")
	}
}

func TestCaptureLogsCarryNoContent(t *testing.T) {
	t.Parallel()

	h := newCaptureTestHarness(t, captureTestConfig())
	h.store.err = errors.New("store failure for " + captureTestProject + " " + captureTestPrincipal + " " +
		captureTestALeg + " " + captureTestUserText + " " + captureTestOutput)
	ctx := context.Background()
	if err := h.capture.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	obs := captureRunTurn(t, h, "trace-1", captureTestALeg)
	if err := obs.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: captureTestOutput}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Finish(ctx, response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := h.capture.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := captureCounter(t, h.metrics, EventCaptureError); got != 1 {
		t.Fatalf("%s = %v, want 1", EventCaptureError, got)
	}
	records := h.logs.snapshot()
	if len(records) == 0 {
		t.Fatal("capture_error emitted no log record")
	}
	forbidden := []string{
		captureTestProject,
		captureTestPrincipal,
		captureTestALeg,
		captureTestUserText,
		"sk-ABCDEFGHIJKLMNOPQRSTUVWX",
		captureTestOutput,
	}
	for _, record := range records {
		text := captureRecordText(record)
		for _, value := range forbidden {
			if strings.Contains(text, value) {
				t.Fatalf("log record carries %q: %q", value, text)
			}
		}
	}
}

// Claim: a stalled first-turn write cannot hold the request transform.
// Source: owner-approved asynchronous-write boundary and Requirement 3.5.
// Mutant: persist the binding synchronously from Handle.
type stalledAdmissionStore struct {
	captureStoreStub
	commitStarted chan struct{}
}

func (s *stalledAdmissionStore) ReadAdmission(_ context.Context, q AdmissionQuery) (AdmissionSnapshot, error) {
	return AdmissionSnapshot{
		Project:      q.Candidate,
		NextBSeq:     0,
		ErasureEpoch: 0,
	}, nil
}

func (s *stalledAdmissionStore) CommitFirstTurn(ctx context.Context, _ FirstTurnJob) (bool, error) {
	select {
	case s.commitStarted <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return false, ctx.Err()
}

func TestCapture_StalledAdmissionCannotHoldForeground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &stalledAdmissionStore{commitStarted: make(chan struct{}, 1)}
		lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
		defer cancelLifecycle()
		c := NewCapture(captureTestConfig(), store, nil, nil, nil, nil)
		if err := c.Start(lifecycleCtx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		call := captureTestCall(t)
		meta := captureTestMeta("stalled-admission", captureTestALeg)
		meta.Session.AuthoritativeFirstTurn = true
		requestCtx, cancelRequest := context.WithCancel(context.Background())
		defer cancelRequest()
		done := make(chan error, 1)
		go func() { done <- c.Handle(requestCtx, call, meta, request.Services{}) }()
		synctest.Wait()
		foregroundBlocked := false
		var handleErr error
		select {
		case handleErr = <-done:
		default:
			foregroundBlocked = true
		}
		commitReachedWriter := false
		select {
		case <-store.commitStarted:
			commitReachedWriter = true
		default:
		}

		// Release both intentionally stalled paths before asserting. A failed
		// foreground assertion must not strand a goroutine inside this synctest
		// bubble and turn the expected RED into a deadlock panic.
		cancelRequest()
		if foregroundBlocked {
			synctest.Wait()
			handleErr = <-done
		}
		cancelLifecycle()
		synctest.Wait()
		if handleErr != nil {
			t.Errorf("foreground request failed: %v", handleErr)
		}

		if foregroundBlocked {
			t.Error("foreground transform is blocked on the memory admission write")
		}
		if !commitReachedWriter {
			t.Error("asynchronous first-turn commit did not reach the stalled writer")
		}
		select {
		case <-c.writerDone:
		default:
			t.Fatal("capture writer did not stop after lifecycle cancellation")
		}
	})
}

type serviceForgetOrderStore struct {
	*serviceStoreStub
	beforeForget func()
}

func (s *serviceForgetOrderStore) Forget(ctx context.Context, owner Owner, nowMS int64) error {
	if s.beforeForget != nil {
		s.beforeForget()
	}
	return s.serviceStoreStub.Forget(ctx, owner, nowMS)
}

func TestServiceForgetEvictsCachedDecisionBeforeStoreCall(t *testing.T) {
	cache := NewDecisionCache(2)
	job := FirstTurnJob{
		Principal: serviceTestCaller(),
		ALegID:    "leg-forgotten",
		Project:   Project{Key: serviceTestScope},
		Decision:  FirstTurnDecision{Outcome: FirstTurnNoSummary},
	}
	cache.Put(job)
	store := &serviceForgetOrderStore{serviceStoreStub: newServiceStoreStub()}
	store.beforeForget = func() {
		if _, status := cache.GetStatus(job.Principal, job.ALegID); status != DecisionAbsent {
			t.Error("Forget reached the durable store before evicting the cached decision")
		}
	}
	svc := NewServiceWithDecisionCache(serviceTestConfig(), store, nil, serviceTestNow, cache)

	if err := svc.Forget(context.Background(), serviceTestCaller(), serviceTestScope); err != nil {
		t.Fatalf("Forget error = %v, want nil", err)
	}
	if _, status := cache.GetStatus(job.Principal, job.ALegID); status != DecisionAbsent {
		t.Fatal("Forget retained the cached first-turn decision")
	}
}

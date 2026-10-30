package reasoningpreservation_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/require"
)

// pollTestPoller stands in for auxreq.BackgroundScheduler, whose Poll,
// Forget and SubmitCollect are all safe for concurrent use. Tests that share
// one poller across concurrent HandleAttempt calls therefore require this
// double to be concurrency-safe as well: every field it mutates per call is
// guarded, never left as a bare write.
type pollTestPoller struct {
	pollCalls   atomic.Int32
	result      auxiliary.PollResult
	err         error
	lastIDMu    sync.Mutex
	lastID      auxiliary.JobID
	forgetCalls atomic.Int32
}

func (f *pollTestPoller) Poll(_ context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	f.pollCalls.Add(1)
	f.setLastID(id)
	if f.err != nil {
		return auxiliary.PollResult{}, f.err
	}
	return f.result, nil
}

func (f *pollTestPoller) setLastID(id auxiliary.JobID) {
	f.lastIDMu.Lock()
	defer f.lastIDMu.Unlock()
	f.lastID = id
}

// LastID reports the job id the poll path observed most recently. Concurrent
// adoption tests read it only after the attempts have joined, so the observed
// value stays deterministic while the write itself stays race-free.
func (f *pollTestPoller) LastID() auxiliary.JobID {
	f.lastIDMu.Lock()
	defer f.lastIDMu.Unlock()
	return f.lastID
}

func (f *pollTestPoller) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "job-1", nil
}

func (f *pollTestPoller) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *pollTestPoller) Forget(id auxiliary.JobID) { f.forgetCalls.Add(1) }
func (f *pollTestPoller) PollCount() int            { return int(f.pollCalls.Load()) }
func (f *pollTestPoller) ForgetCount() int          { return int(f.forgetCalls.Load()) }

type pollTestEgress struct{}

func (pollTestEgress) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

type pollTestSan struct{}

func (pollTestSan) SanitizeText(_ context.Context, t string) (string, error) { return t, nil }

func pollTestConfig(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, `
action: restore
use_builtin_catalog: false
rules:
  - id: be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
compression:
  enabled: true
  mode: shadow
  route: comp-route
  timeout: 5s
  max_input_tokens: 1000
  max_input_bytes: 10000
  max_output_tokens: 1000
  max_output_bytes: 4096
  max_surrogate_bytes: 1024
  min_source_bytes: 10
  min_saved_bytes: 5
  min_savings_ratio: 0.1
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 32768
  max_pending_total: 100
  max_surrogate_bytes_total: 262144
  egress_policy_ref: v1
`)
}

func setupPollPendingForFixture(t *testing.T, cs reasoningpreservation.CompressionStore, p reasoningpreservation.SessionPartition, art reasoningpreservation.TurnArtifact) (auxiliary.JobID, string) {
	t.Helper()
	_, err := cs.Append(context.Background(), p, art)
	require.NoError(t, err)
	digest := art.Anchor
	semDigest := sha256.Sum256([]byte("semantic-" + art.ID))
	egHash := sha256.Sum256([]byte("v1"))
	claim, err := cs.ReserveCompression(context.Background(), p, art.ID, digest, "v1", semDigest, egHash)
	require.NoError(t, err)
	newHash := sha256.Sum256([]byte("v1-route-purpose"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim, egHash, semDigest, newHash, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	jobID := auxiliary.JobID("job-" + art.ID)
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim, jobID))
	return jobID, claim.ReservationID
}

var pollTestSupport = aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}}

// Direct poll helper tests — verify one-shot Poll semantics via restorableArtifacts.

func TestPollOnce_Pending(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-pending")
	call, arts := missingRestoreFixture(t)
	jobID, _ := setupPollPendingForFixture(t, cs, p, arts[0])
	snap, err := cs.Snapshot(context.Background(), p)
	require.NoError(t, err)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindPending, res.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, auxiliary.PollPending, res.State)
	require.Equal(t, 0, poller.ForgetCount())
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending)
	require.Equal(t, jobID, st.Pending.JobID)
}

func TestPollOnce_Failed(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-failed")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollFailed, Err: errors.New("provider boom")}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindFailed, res.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, 1, poller.ForgetCount())
	st, found, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	if found && st.Pending != nil {
		t.Fatalf("pending should be cleared after failed")
	}
}

func TestPollOnce_NotFound(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-notfound")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollNotFound}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindNotFound, res.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, 1, poller.ForgetCount())
	st, found, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	if found && st.Pending != nil {
		t.Fatalf("pending should be cleared after not_found")
	}
}

func TestPollOnce_Unavailable(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-unavail")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	svc := reasoningpreservation.CompressionServices{Client: nil, Poller: nil, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindUnavailable, res.Kind)
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending)
}

func TestPollOnce_PollError(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-err")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{err: errors.New("poll operational error")}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindPollError, res.Kind)
	require.NotNil(t, res.Err)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, 0, poller.ForgetCount())
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending, "poll error must leave pending")
}

func TestPollOnce_Completed(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-poll-completed")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	var c aipapi.Collected
	c.Text.WriteString(`{"schema_version":1,"segments":[{"index":0,"text":"compressed"}]}`)
	c.FinishReceived = true
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindCompleted, res.Kind)
	require.NotNil(t, res.Candidate)
	require.Equal(t, arts[0].ID, res.Candidate.ArtifactID)
	require.Equal(t, auxiliary.PollCompleted, res.Candidate.PollState)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, 0, poller.ForgetCount())
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending, "completed must not clear pending yet (5.2)")
	// Defensive clone: mutate original poller result should not affect candidate
	c.Text.WriteString("mutate")
	require.NotContains(t, res.Candidate.Collected.Text.String(), "mutate")
}

func TestPollOnce_PollCountExactlyOne(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-multi")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	secondArt := arts[0]
	secondArt.ID = "art-2"
	secondArt.Anchor = sha256.Sum256([]byte("art-2-anchor"))
	_, err := cs.Append(context.Background(), p, secondArt)
	require.NoError(t, err)
	sem2 := sha256.Sum256([]byte("semantic-art-2"))
	eg2 := sha256.Sum256([]byte("v1"))
	claim2, err := cs.ReserveCompression(context.Background(), p, "art-2", secondArt.Anchor, "v1", sem2, eg2)
	require.NoError(t, err)
	newHash2 := sha256.Sum256([]byte("v1-route-purpose"))
	require.NoError(t, cs.UpdateReservationPolicyHash(context.Background(), claim2, eg2, sem2, newHash2, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))))
	require.NoError(t, cs.BindCompressionJob(context.Background(), claim2, "job-art-2"))
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	// call only missing first artifact (art-1); second artifact pending but not restorable for this call
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindPending, res.Kind)
	require.Equal(t, 1, poller.PollCount(), "must poll exactly once even with multiple pending")
}

func TestPollOnce_PreservedSkipped_SecondMissingPicked(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-preserved-skip")
	// Build 2-turn fixture: first preserved, second missing
	visible1 := []aipapi.Part{aipapi.TextPart("visible1")}
	visible2 := []aipapi.Part{aipapi.TextPart("visible2")}
	anchor1 := anchorFor(t, visible1...)
	anchor2 := anchorFor(t, visible2...)
	stored1 := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "thought1", "", nil)
	stored2 := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "thought2", "", nil)
	art1 := turnArtifact("art-a", anchor1, placedReasoning(0, stored1))
	art2 := turnArtifact("art-b", anchor2, placedReasoning(0, stored2))
	art1.CreatedAt = time.Now().UTC()
	art2.CreatedAt = time.Now().UTC()
	// call: first has reasoning preserved, second missing
	call := aipapi.Call{Messages: []aipapi.Message{
		assistantMsg(stored1, aipapi.TextPart("visible1")),
		assistantMsg(visible2...),
	}}
	setupPollPendingForFixture(t, cs, p, art1)
	setupPollPendingForFixture(t, cs, p, art2)
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindPending, res.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, auxiliary.JobID("job-art-b"), poller.LastID(), "must poll job-B, first is preserved not missing")
}

func TestPollOnce_UnsupportedDialectSkipped(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-unsupported-skip")
	visible1 := []aipapi.Part{aipapi.TextPart("visible1")}
	visible2 := []aipapi.Part{aipapi.TextPart("visible2")}
	anchor1 := anchorFor(t, visible1...)
	anchor2 := anchorFor(t, visible2...)
	// art-a uses unsupported dialect (Anthropic thinking with signature, not in support)
	unsupported := reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "thought1", "sig", nil)
	supported := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "thought2", "", nil)
	artA := turnArtifact("art-a2", anchor1, placedReasoning(0, unsupported))
	artB := turnArtifact("art-b2", anchor2, placedReasoning(0, supported))
	artA.CreatedAt = time.Now().UTC()
	artB.CreatedAt = time.Now().UTC()
	call := aipapi.Call{Messages: []aipapi.Message{
		assistantMsg(visible1...),
		assistantMsg(visible2...),
	}}
	setupPollPendingForFixture(t, cs, p, artA)
	setupPollPendingForFixture(t, cs, p, artB)
	snap, _ := cs.Snapshot(context.Background(), p)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	// support only OpenAIChatTextV1, so art-a unsupported should be skipped
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindPending, res.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, auxiliary.JobID("job-art-b2"), poller.LastID(), "must skip unsupported first missing, pick supported second")
}

// Transform integration tests — verify chain without active replay.

func TestTransform_PollPending_OriginalRemains(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-t-pending")
	call, arts := missingRestoreFixture(t)
	jobID, _ := setupPollPendingForFixture(t, cs, p, arts[0])
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svc, reasoningpreservation.CompanionPolicy{}, nil)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-t-pending"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.True(t, callHasReasoning(call), "original restoration must be unchanged")
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending)
	require.Equal(t, jobID, st.Pending.JobID)
}

func TestTransform_PollFailed_ClearsAndForgets(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-t-failed")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollFailed, Err: errors.New("boom")}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svc, reasoningpreservation.CompanionPolicy{}, nil)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-t-failed"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.Equal(t, 1, poller.ForgetCount())
	require.True(t, callHasReasoning(call))
}

func TestTransform_PollCompleted_ShadowOriginalRemains(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-t-completed")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	var c aipapi.Collected
	c.Text.WriteString(`{"schema_version":1,"segments":[{"index":0,"text":"compressed"}]}`)
	c.FinishReceived = true
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svc, reasoningpreservation.CompanionPolicy{}, nil)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-t-completed"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.Equal(t, 1, poller.PollCount())
	require.True(t, callHasReasoning(call), "shadow: original must remain even when completed candidate exists")
	st, _, _ := cs.GetCompressionState(context.Background(), p, arts[0].ID)
	require.NotNil(t, st.Pending)
	require.Equal(t, 0, poller.ForgetCount())
}

func TestTransform_PollErrorDistinctFromStateError(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-t-err")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	pollerErr := &pollTestPoller{err: errors.New("poll operational error")}
	svcErr := reasoningpreservation.CompressionServices{Client: pollerErr, Poller: pollerErr, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svcErr, reasoningpreservation.CompanionPolicy{}, nil)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-t-err"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind, "poll error must be compression-local")
	require.True(t, callHasReasoning(call))
	failStore := &pollSnapshotFailStore{}
	xform2 := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, failStore, svcErr, reasoningpreservation.CompanionPolicy{}, nil)
	call2, _ := missingRestoreFixture(t)
	dec2, err := xform2.HandleAttempt(context.Background(), &call2, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "any"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptExcludeCandidate, dec2.Kind)
	require.Equal(t, "state_error", dec2.ReasonCode)
}

type pollSnapshotFailStore struct{}

func (s *pollSnapshotFailStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, nil
}

func (s *pollSnapshotFailStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return nil, errors.New("snapshot boom")
}

func (s *pollSnapshotFailStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	return nil
}

func TestTransform_UnmatchedDoesNotPoll(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-t-unmatched")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, cs, svc, reasoningpreservation.CompanionPolicy{}, nil)
	dec, err := xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "unmatched-be", Model: "unknown-model", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-t-unmatched"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.Equal(t, 0, poller.PollCount(), "unmatched candidate must not poll")
}

func TestTransform_BundleWiring_UsesCompositionServices(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	var c aipapi.Collected
	c.Text.WriteString(`{"schema_version":1,"segments":[{"index":0,"text":"compressed"}]}`)
	c.FinishReceived = true
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	parts, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(cfg, svc, reasoningpreservation.CompanionPolicy{})
	require.NoError(t, err)
	call, arts := missingRestoreFixture(t)
	arts[0].CreatedAt = time.Now().UTC()
	partition := reasoningpreservation.NewSessionPartition("sess-bundle")
	cs, ok := parts.Store.(reasoningpreservation.CompressionStore)
	require.True(t, ok)
	setupPollPendingForFixture(t, cs, partition, arts[0])
	dec, err := parts.Transform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-bundle"},
	}, request.Services{})
	require.NoError(t, err)
	require.Equal(t, request.AttemptContinue, dec.Kind)
	require.Equal(t, 1, poller.PollCount(), "bundle-wired transform must poll exactly once via InstanceParts.CompressionServices")
	require.True(t, callHasReasoning(call))
}

// Regression: restore preserves exact state-error and unrepresentable semantics via shared helper.

func TestRestore_StateError_InvalidArtifact(t *testing.T) {
	t.Parallel()
	call, _ := missingRestoreFixture(t)
	corrupt := reasoningpreservation.TurnArtifact{ID: "corrupt", ReasoningBytes: -1}
	res, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:            reasoningpreservation.ActionRestore,
		OnStateError:      reasoningpreservation.PolicyReject,
		OnUnrepresentable: reasoningpreservation.PolicyReject,
		Call:              &call,
		Artifacts:         []reasoningpreservation.TurnArtifact{corrupt},
		ReplaySupport:     pollTestSupport,
		Eligible:          true,
	})
	require.NoError(t, err)
	require.True(t, res.Exclude)
	require.Equal(t, "state_error", res.ReasonCode)
}

func TestRestore_Unrepresentable_UnsupportedDialect(t *testing.T) {
	t.Parallel()
	call, arts := missingRestoreFixture(t)
	// Make artifact dialect unsupported: only OpenAIChatTextV1 is supported
	arts[0].Reasoning[0].Part.Reasoning.Dialect = aipapi.ReasoningDialectAnthropicThinkingV1
	arts[0].Reasoning[0].Part.Reasoning.Signature = "sig"
	res, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:            reasoningpreservation.ActionRestore,
		OnStateError:      reasoningpreservation.PolicyReject,
		OnUnrepresentable: reasoningpreservation.PolicyReject,
		Call:              &call,
		Artifacts:         arts,
		ReplaySupport:     pollTestSupport,
		Eligible:          true,
	})
	require.NoError(t, err)
	require.True(t, res.Exclude)
	require.Equal(t, "unrepresentable_replay", res.ReasonCode)
}

func TestPollAndRestore_UnaffectedByStateError_UnrepresentableSplit(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-split")
	// Artifact with unsupported dialect -> restore should be unrepresentable, poll should skip it
	call, arts := missingRestoreFixture(t)
	arts[0].Reasoning[0].Part.Reasoning.Dialect = aipapi.ReasoningDialectAnthropicThinkingV1
	arts[0].Reasoning[0].Part.Reasoning.Signature = "sig"
	arts[0].CreatedAt = time.Now().UTC()
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	// Poll should skip unsupported and return NoPending (since only unsupported candidate)
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollPending}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	resPoll := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindNoPending, resPoll.Kind)
	require.Equal(t, 0, poller.PollCount(), "unsupported missing must be skipped, no poll")
	// Restore must still be unrepresentable
	resRestore, _ := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:            reasoningpreservation.ActionRestore,
		OnStateError:      reasoningpreservation.PolicyReject,
		OnUnrepresentable: reasoningpreservation.PolicyReject,
		Call:              &call,
		Artifacts:         snap,
		ReplaySupport:     pollTestSupport,
		Eligible:          true,
	})
	require.True(t, resRestore.Exclude)
	require.Equal(t, "unrepresentable_replay", resRestore.ReasonCode)
}

func TestPollOnce_CompletedCarriesPollerPayload(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	p := reasoningpreservation.NewSessionPartition("sess-clone-all")
	call, arts := missingRestoreFixture(t)
	setupPollPendingForFixture(t, cs, p, arts[0])
	snap, _ := cs.Snapshot(context.Background(), p)
	// Build Collected with every mutable field including TerminalError deep fields
	var c aipapi.Collected
	c.Text.WriteString("orig text")
	c.Reasoning.WriteString("orig reasoning")
	c.ToolArgs = map[string]*strings.Builder{"tool-1": func() *strings.Builder { b := &strings.Builder{}; b.WriteString("args1"); return b }()}
	c.ToolNames = map[string]string{"tool-1": "fn1"}
	c.ToolCallOrder = []string{"tool-1"}
	c.Warnings = []string{"warn1"}
	c.InputTokens = 1
	c.OutputTokens = 2
	c.FinishReceived = true
	c.FinishReason = "stop"
	c.AssistantMedia = []aipapi.Part{{Kind: aipapi.PartText, Text: "media"}}
	c.ReasoningParts = []aipapi.ReasoningPart{{
		Dialect:                 aipapi.ReasoningDialectOpenAIChatTextV1,
		Text:                    "rt",
		Signature:               "sig",
		Opaque:                  []byte(`{"opaque":1}`),
		Summary:                 []byte(`{"summary":1}`),
		SummaryPresent:          true,
		Content:                 []byte(`{"content":1}`),
		ContentPresent:          true,
		EncryptedContent:        []byte(`null`),
		EncryptedContentPresent: true,
	}}
	c.TerminalError = &aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    "orig-code",
		ErrorMessage: "orig msg",
		Opaque:       []byte("opaque-terminal"),
		Reasoning: &aipapi.ReasoningPart{
			Dialect:                 aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:                    "term-rt",
			Signature:               "term-sig",
			Opaque:                  []byte(`{"term-opaque":1}`),
			Summary:                 []byte(`{"term-summary":1}`),
			SummaryPresent:          true,
			Content:                 []byte(`{"term-content":1}`),
			ContentPresent:          true,
			EncryptedContent:        []byte(`{"term-enc":1}`),
			EncryptedContentPresent: true,
		},
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{
				Kind:       aipapi.ContentPartText,
				Text:       "item-text",
				Annotation: &aipapi.AnnotationPart{Type: "ann", Data: []byte(`{"ann":1}`)},
			}},
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call-1",
				Arguments: []byte(`{"arg":1}`),
			},
		},
		UsageScopes: []aipapi.ScopedUsageDelta{{InputTokens: 5, OutputTokens: 10}},
	}
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: c}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	res := reasoningpreservation.PollOnceForMatchingArtifact(context.Background(), &call, cs, p, snap, pollTestSupport, svc)
	require.Equal(t, reasoningpreservation.PollKindCompleted, res.Kind)
	require.NotNil(t, res.Candidate)
	// Candidate carries the poller's Collected as returned. The feature re-clones it
	// defensively at the adoption boundary (cloneCollected), so candidate reflects
	// the original payload independently of the poller implementation.
	require.Equal(t, "orig text", res.Candidate.Collected.Text.String())
	require.Equal(t, "orig reasoning", res.Candidate.Collected.Reasoning.String())
	require.Equal(t, "args1", res.Candidate.Collected.ToolArgs["tool-1"].String())
	require.Equal(t, "fn1", res.Candidate.Collected.ToolNames["tool-1"])
	require.Equal(t, "tool-1", res.Candidate.Collected.ToolCallOrder[0])
	require.Equal(t, "warn1", res.Candidate.Collected.Warnings[0])
	require.Equal(t, "media", res.Candidate.Collected.AssistantMedia[0].Text)
	require.Equal(t, "rt", res.Candidate.Collected.ReasoningParts[0].Text)
	require.Equal(t, "sig", res.Candidate.Collected.ReasoningParts[0].Signature)
	require.Equal(t, `{"opaque":1}`, string(res.Candidate.Collected.ReasoningParts[0].Opaque))
	require.Equal(t, `{"summary":1}`, string(res.Candidate.Collected.ReasoningParts[0].Summary))
	require.True(t, res.Candidate.Collected.ReasoningParts[0].SummaryPresent)
	require.Equal(t, `{"content":1}`, string(res.Candidate.Collected.ReasoningParts[0].Content))
	require.True(t, res.Candidate.Collected.ReasoningParts[0].ContentPresent)
	require.Equal(t, `null`, string(res.Candidate.Collected.ReasoningParts[0].EncryptedContent))
	require.True(t, res.Candidate.Collected.ReasoningParts[0].EncryptedContentPresent)
	require.Equal(t, aipapi.ReasoningDialectOpenAIChatTextV1, res.Candidate.Collected.ReasoningParts[0].Dialect)
	require.NotNil(t, res.Candidate.Collected.TerminalError)
	require.Equal(t, "orig-code", res.Candidate.Collected.TerminalError.ErrorCode)
	require.Equal(t, "orig msg", res.Candidate.Collected.TerminalError.ErrorMessage)
	require.Equal(t, "opaque-terminal", string(res.Candidate.Collected.TerminalError.Opaque))
	require.Equal(t, "term-rt", res.Candidate.Collected.TerminalError.Reasoning.Text)
	require.Equal(t, "term-sig", res.Candidate.Collected.TerminalError.Reasoning.Signature)
	require.Equal(t, `{"term-opaque":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Opaque))
	require.Equal(t, `{"term-summary":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Summary))
	require.True(t, res.Candidate.Collected.TerminalError.Reasoning.SummaryPresent)
	require.Equal(t, `{"term-content":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.Content))
	require.True(t, res.Candidate.Collected.TerminalError.Reasoning.ContentPresent)
	require.Equal(t, `{"term-enc":1}`, string(res.Candidate.Collected.TerminalError.Reasoning.EncryptedContent))
	require.True(t, res.Candidate.Collected.TerminalError.Reasoning.EncryptedContentPresent)
	require.Equal(t, "item-text", res.Candidate.Collected.TerminalError.Item.Content[0].Text)
	require.Equal(t, `{"ann":1}`, string(res.Candidate.Collected.TerminalError.Item.Content[0].Annotation.Data))
	require.Equal(t, `{"arg":1}`, string(res.Candidate.Collected.TerminalError.Item.ToolCall.Arguments))
	require.Equal(t, 5, res.Candidate.Collected.TerminalError.UsageScopes[0].InputTokens)
	require.Equal(t, 10, res.Candidate.Collected.TerminalError.UsageScopes[0].OutputTokens)
}

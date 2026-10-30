package diag_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corediag "github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/db"
)

func seedSession(ctx context.Context, t *testing.T, st *memory.Store, sid domain.SessionID, owner string, aLeg string, pol domain.PolicyMetadata) {
	t.Helper()
	fp := domain.TokenFingerprint{}
	fp[3] = byte(sid[0] % 255)
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: owner}, Workspace: domain.WorkspaceRef{ID: "ws1"},
		Policy: pol, ALegID: aLeg, ResumeEligible: true, CreatedAt: time.Unix(10, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.TouchActivity(ctx, sid, time.Unix(20, 0), domain.ActivityClientRequest)
	_ = st.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: sid, TurnID: "t1", ALegID: aLeg, BLegID: "b1", AttemptSeq: 1,
		RequestedModel: "gpt", ResolvedBackend: "be", ResolvedModel: "gpt-4",
		RouteSource: "default", StartedAt: time.Unix(11, 0),
	})
	_ = st.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: sid, TurnID: "t1", BLegID: "b1", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(12, 0),
	})
	_ = st.AddUsage(ctx, domain.UsageDelta{SessionID: sid, TurnID: "t1", BLegID: "b1", InputTokens: 3, OutputTokens: 5})
}

func testDiagHandler(t *testing.T, st *memory.Store) http.Handler {
	t.Helper()
	h, err := diag.NewHandler("/debug/sessions", st, "standard", nil, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestNewHandler_nilStore(t *testing.T) {
	t.Parallel()
	_, err := diag.NewHandler("/debug/sessions", nil, "standard", nil, nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestHandler_listAndDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-a", "alice", "aleg-1", domain.PolicyMetadata{
		TranscriptEnabled: true, PolicyVersion: "pv1", RedactionProfile: "standard", AuditMode: "best_effort",
	})
	h := testDiagHandler(t, st)

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/debug/sessions?owner=alice", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		sessions, ok := env["sessions"].([]any)
		if !ok || len(sessions) != 1 {
			t.Fatalf("sessions %#v", env)
		}
		row, ok := sessions[0].(map[string]any)
		if !ok {
			t.Fatalf("session row %#v", sessions[0])
		}
		if row["session_id"] != "sess-a" || row["owner_id"] != "alice" {
			t.Fatalf("ids %#v", row)
		}
		if re, ok := row["resume_eligible"].(bool); !ok || !re {
			t.Fatalf("resume_eligible %#v", row["resume_eligible"])
		}
		if row["a_leg_id"] != "aleg-1" {
			t.Fatalf("a_leg_id %#v", row)
		}
		if row["policy_version"] != "pv1" {
			t.Fatalf("policy_version %#v", row)
		}
		uin, _ := row["usage_input_tokens"].(float64)
		uout, _ := row["usage_output_tokens"].(float64)
		if int64(uin) != 3 || int64(uout) != 5 {
			t.Fatalf("usage tokens in=%v out=%v", uin, uout)
		}
	})

	t.Run("detail", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-a", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		sess, ok := env["session"].(map[string]any)
		if !ok {
			t.Fatalf("session %#v", env)
		}
		if sess["owner_id"] != "alice" || sess["a_leg_id"] != "aleg-1" {
			t.Fatalf("session %#v", sess)
		}
		uin, ok := sess["usage_input_tokens"].(float64)
		if !ok || int64(uin) != 3 {
			t.Fatalf("usage in %#v", sess)
		}
		atts, ok := env["attempts"].([]any)
		if !ok || len(atts) != 1 {
			t.Fatalf("attempts %#v", env["attempts"])
		}
		att, ok := atts[0].(map[string]any)
		if !ok || att["b_leg_id"] != "b1" {
			t.Fatalf("attempt row %#v", atts[0])
		}
	})
}

func TestHandler_byALeg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-b", "alice", "aleg-99", domain.PolicyMetadata{TranscriptEnabled: false})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/by-a-leg/aleg-99", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	sess, ok := env["session"].(map[string]any)
	if !ok {
		t.Fatalf("session %#v", env)
	}
	if sess["session_id"] != "sess-b" {
		t.Fatalf("got %#v", sess)
	}
}

func TestHandler_transcriptPagingAndRedaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	pol := domain.PolicyMetadata{TranscriptEnabled: true, RedactionProfile: "strict"}
	seedSession(ctx, t, st, "sess-tx", "u1", "a1", pol)
	seq, _ := st.NextTranscriptSeq(ctx, "sess-tx")
	_ = st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: "sess-tx", TurnID: "t1", Seq: seq, EventKind: "x",
		PayloadRef: `{"secret":"nope"}`, CreatedAt: time.Unix(30, 0),
	})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-tx/transcript?limit=10", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	items, ok := env["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items %#v", env)
	}
	it, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item 0 %#v", items[0])
	}
	pr, ok := it["payload_ref"].(string)
	if !ok {
		t.Fatalf("payload_ref %#v", it["payload_ref"])
	}
	if strings.Contains(pr, "secret") || strings.Contains(pr, "nope") {
		t.Fatalf("leaked payload: %s", pr)
	}
}

func TestHandler_auditStripsRawEventWhenNotFull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	pol := domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "best_effort"}
	seedSession(ctx, t, st, "sess-aud", "u1", "a1", pol)
	seq, _ := st.NextAuditSeq(ctx, "sess-aud")
	_ = st.AppendAudit(ctx, domain.AuditItem{
		SessionID: "sess-aud", TurnID: "t1", Seq: seq, Action: "test",
		Result:    `{"event":{"x":"y"},"ok":true}`,
		CreatedAt: time.Unix(40, 0),
	})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-aud/audit", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	items, ok := env["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("items %#v", env)
	}
	it, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item 0 %#v", items[0])
	}
	resB, err := json.Marshal(it["result"])
	if err != nil {
		t.Fatal(err)
	}
	res := string(resB)
	if strings.Contains(res, `"x":"y"`) {
		t.Fatalf("raw event leaked: %s", res)
	}
	if !strings.Contains(res, "event_digest") {
		t.Fatalf("want digest: %s", res)
	}
}

func TestHandler_auditMalformedResultNotRawWhenNotFull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	pol := domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "best_effort"}
	seedSession(ctx, t, st, "sess-aud-bad", "u1", "a1", pol)
	seq, _ := st.NextAuditSeq(ctx, "sess-aud-bad")
	_ = st.AppendAudit(ctx, domain.AuditItem{
		SessionID: "sess-aud-bad", TurnID: "t1", Seq: seq, Action: "test",
		Result:    `not-json{SECRET_LEAK}`,
		CreatedAt: time.Unix(40, 0),
	})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-aud-bad/audit", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	items, ok := env["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("items %#v", env)
	}
	it, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item 0 %#v", items[0])
	}
	resB, err := json.Marshal(it["result"])
	if err != nil {
		t.Fatal(err)
	}
	res := string(resB)
	if strings.Contains(res, "SECRET_LEAK") {
		t.Fatalf("malformed audit must not echo raw body: %s", res)
	}
	if !strings.Contains(res, "event_digest") {
		t.Fatalf("want digest wrapper: %s", res)
	}
}

func TestHandler_auditPreservesEventWhenFullPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	pol := domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "full"}
	seedSession(ctx, t, st, "sess-full", "u1", "a1", pol)
	seq, _ := st.NextAuditSeq(ctx, "sess-full")
	_ = st.AppendAudit(ctx, domain.AuditItem{
		SessionID: "sess-full", TurnID: "t1", Seq: seq, Action: "test",
		Result:    `{"event":{"keep":true}}`,
		CreatedAt: time.Unix(41, 0),
	})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-full/audit", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	items, ok := env["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("items %#v", env)
	}
	it, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item 0 %#v", items[0])
	}
	resB, err := json.Marshal(it["result"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resB), "keep") {
		t.Fatalf("expected raw event: %s", resB)
	}
}

func TestHandler_wrapDiagnosticsProtect_requiresSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-p", "u1", "a1", domain.PolicyMetadata{})
	h := testDiagHandler(t, st)
	wrapped := corediag.WrapDiagnosticsProtect("s3cr3t-s3cr3t-s3cr3t-s3cr3t", h)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-p", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", rec.Code)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-p", nil)
	req2.Header.Set(corediag.HeaderDiagnosticsSecret, "s3cr3t-s3cr3t-s3cr3t-s3cr3t")
	rec2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("want 200 got %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandler_unknownSession_nonEnumerating(t *testing.T) {
	t.Parallel()
	st := memory.New(memory.Options{})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not_found") {
		t.Fatalf("body %q", body)
	}
}

func TestHandler_wrongOwnerScope_matchesUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-own", "alice", "a-leg-x", domain.PolicyMetadata{})
	h := testDiagHandler(t, st)

	unknown := httptest.NewRequest(http.MethodGet, "/debug/sessions/nope", nil)
	recU := httptest.NewRecorder()
	h.ServeHTTP(recU, unknown)

	wrong := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-own", nil)
	wrong.Header.Set(diag.HeaderOwnerScope, "bob")
	recW := httptest.NewRecorder()
	h.ServeHTTP(recW, wrong)

	if recU.Code != recW.Code || recU.Body.String() != recW.Body.String() {
		t.Fatalf("mismatch unknown=%d %q wrong=%d %q", recU.Code, recU.Body.String(), recW.Code, recW.Body.String())
	}
}

func TestHandler_unknownALeg_nonEnumerating(t *testing.T) {
	t.Parallel()
	st := memory.New(memory.Options{})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/by-a-leg/unknown-leg", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandler_resumeIneligibleExposedToAuthorizedOperator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	fp := domain.TokenFingerprint{}
	fp[1] = 1
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: "sess-exp", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "u"}, Policy: domain.PolicyMetadata{},
		ALegID: "leg1", ResumeEligible: false, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-exp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	sess, ok := env["session"].(map[string]any)
	if !ok {
		t.Fatalf("session %#v", env)
	}
	re, ok := sess["resume_eligible"].(bool)
	if !ok {
		t.Fatalf("resume_eligible type %#v", sess["resume_eligible"])
	}
	if re {
		t.Fatal("expected resume_eligible false")
	}
}

func TestHandler_listEmptyOnScopeConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "s1", "alice", "l1", domain.PolicyMetadata{})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions?owner=bob", nil)
	req.Header.Set(diag.HeaderOwnerScope, "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if arr, ok := env["sessions"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("want empty sessions %#v", env)
	}
}

func TestHandler_methodNotAllowed(t *testing.T) {
	t.Parallel()
	st := memory.New(memory.Options{})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodPost, "/debug/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestHandler_transcript_afterSeq(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	pol := domain.PolicyMetadata{TranscriptEnabled: true}
	seedSession(ctx, t, st, "sess-seq", "u1", "a1", pol)
	for i := range 3 {
		seq, _ := st.NextTranscriptSeq(ctx, "sess-seq")
		_ = st.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID: "sess-seq", TurnID: "t1", Seq: seq, EventKind: "e",
			PayloadRef: "{}", CreatedAt: time.Unix(int64(50+i), 0),
		})
	}
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-seq/transcript?after_seq=2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	items, ok := env["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("want 1 item after seq 2, got %d", len(items))
	}
}

func TestHandler_bodyReadableOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-br", "u1", "a1", domain.PolicyMetadata{})
	h := testDiagHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-br", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "sess-br") {
		t.Fatalf("body %s", b)
	}
}

type diagStore interface {
	app.Store
	diag.Store
}

// TestHandler_latestAttemptSurvivesOutOfOrderCompletion pins that the session's
// latest attempt is the newest B-leg with its own persisted outcome, even when
// an older attempt finishes after the newer failed open recorded its failure.
func TestHandler_latestAttemptSurvivesOutOfOrderCompletion(t *testing.T) {
	t.Parallel()
	stores := map[string]func(*testing.T) diagStore{
		"memory": func(*testing.T) diagStore { return memory.New(memory.Options{}) },
		"sqlite": func(t *testing.T) diagStore {
			t.Helper()
			ctx := context.Background()
			bunDB, err := db.OpenSQLiteBun(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			s, err := bunstore.NewWithContext(ctx, bunDB)
			if err != nil {
				_ = bunDB.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
	for name, newStore := range stores {
		for _, profile := range []string{"standard", "strict"} {
			t.Run(name+"/"+profile, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				st := newStore(t)
				fp := domain.TokenFingerprint{}
				fp[0] = 7
				rec, err := st.Create(ctx, domain.CreateRecord{
					SessionID: "sess-ooo", ResumeFingerprint: fp,
					Owner: domain.PrincipalRef{ID: "alice"}, Workspace: domain.WorkspaceRef{ID: "ws1"},
					Policy: domain.PolicyMetadata{RedactionProfile: profile, AuditMode: "best_effort"},
					ALegID: "aleg-1", ResumeEligible: true, CreatedAt: time.Unix(10, 0),
				})
				if err != nil {
					t.Fatal(err)
				}
				sid := rec.SessionID
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				must(st.AppendAttemptTrace(ctx, domain.AttemptTrace{SessionID: sid, TurnID: "t1", ALegID: "aleg-1", BLegID: "A", AttemptSeq: 1, ResolvedBackend: "be-a", StartedAt: time.Unix(11, 0)}))
				must(st.AppendAttemptTrace(ctx, domain.AttemptTrace{SessionID: sid, TurnID: "t1", ALegID: "aleg-1", BLegID: "B", AttemptSeq: 2, ResolvedBackend: "be-b", StartedAt: time.Unix(12, 0), OpenFailed: true}))
				must(st.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{SessionID: sid, TurnID: "t1", BLegID: "B", SurfaceState: domain.SurfaceSwallowed, ProviderStatus: "pre_output", ErrorCode: "backend_open_nil_stream", DebugReason: "secret detail", EndedAt: time.Unix(13, 0)}))
				must(st.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{SessionID: sid, TurnID: "t1", BLegID: "A", Success: true, SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(14, 0)}))

				h, err := diag.NewHandler("/debug/sessions", st, "standard", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-ooo", nil))
				if w.Code != http.StatusOK {
					t.Fatalf("status %d", w.Code)
				}
				if strings.Contains(w.Body.String(), "secret detail") {
					t.Fatalf("raw failure detail leaked: %s", w.Body.String())
				}
				var env struct {
					Session struct {
						LatestAttempt map[string]any `json:"latest_attempt"`
					} `json:"session"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
					t.Fatal(err)
				}
				la := env.Session.LatestAttempt
				if la["b_leg_id"] != "B" || la["open_failed"] != true || la["success"] != false ||
					la["provider_status"] != "pre_output" || la["error_code"] != "backend_open_nil_stream" ||
					la["surface_state"] != "swallowed" {
					t.Fatalf("latest attempt lost B's recorded failure: %#v", la)
				}
			})
		}
	}
}

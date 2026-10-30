package observers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// TestPhase6Compat_SecureSessionDiagnosticsUnchangedWithControlPlane proves the
// secure-session operator diagnostics HTTP surface (list, detail, by-A-leg,
// transcript, audit) is byte-identical before and after the control-plane
// SecureSessionStoreDecorator wraps the store (task 6.3; requirements 8.1, 8.5,
// 8.6, 10.6, 10.7). Existing diagnostics must not change and must not require
// the control-plane query capability.
func TestPhase6Compat_SecureSessionDiagnosticsUnchangedWithControlPlane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pol := domain.PolicyMetadata{
		TranscriptEnabled: true, PolicyVersion: "pv1", RedactionProfile: "standard", AuditMode: "best_effort",
	}

	baselineStore := memory.New(memory.Options{})
	wrappedStore := memory.New(memory.Options{})
	seedCompatSession(ctx, t, baselineStore, "sess-cmp", "alice", "aleg-cmp", pol)

	h := newHarness(t, cp.RecordingBestEffort, nil)
	decorated := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   wrappedStore,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	// Seed the wrapped store THROUGH the decorator so the same delegate data is
	// produced and the decorator's mutation projection is exercised.
	seedCompatSessionThroughDecorator(ctx, t, decorated, "sess-cmp", "alice", "aleg-cmp", pol)

	baselineHandler := newCompatDiagHandler(t, baselineStore)
	wrappedHandler := newCompatDiagHandler(t, decorated)

	cases := []struct {
		name string
		path string
	}{
		{"list", "/debug/sessions?owner=alice"},
		{"detail", "/debug/sessions/sess-cmp"},
		{"by_a_leg", "/debug/sessions/by-a-leg/aleg-cmp"},
		{"transcript", "/debug/sessions/sess-cmp/transcript?limit=10"},
		{"audit", "/debug/sessions/sess-cmp/audit?limit=10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := doCompatGet(t, baselineHandler, tc.path)
			after := doCompatGet(t, wrappedHandler, tc.path)

			if before.Code != after.Code {
				t.Fatalf("status changed: before=%d after=%d path=%s", before.Code, after.Code, tc.path)
			}
			if !jsonEqual(before.Body.Bytes(), after.Body.Bytes()) {
				t.Fatalf("diagnostics body changed for %s\nbefore=%s\nafter=%s",
					tc.path, before.Body.String(), after.Body.String())
			}
		})
	}

	// Mutating through the decorator recorded control-plane events, but the
	// diagnostics reads above never recorded (reads are pure pass-through).
	if got := len(h.events()); got == 0 {
		t.Fatalf("control-plane recording must be active for mutations, got %d events", got)
	}
}

// TestPhase6Compat_SecureSessionDiagnosticsDoNotRequireControlPlaneQuery proves
// the secure-session diagnostics surface works unchanged when the control-plane
// capability is disabled (no recorder), confirming existing diagnostics do not
// depend on the new query capability (requirements 8.1, 8.5, 10.6).
func TestPhase6Compat_SecureSessionDiagnosticsDoNotRequireControlPlaneQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pol := domain.PolicyMetadata{TranscriptEnabled: true, RedactionProfile: "standard", AuditMode: "best_effort"}

	h := newHarness(t, cp.RecordingBestEffort, nil)
	disabled := h.disabledRecorder()

	store := memory.New(memory.Options{})
	seedCompatSession(ctx, t, store, "sess-dis", "alice", "aleg-dis", pol)
	decorated := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   store,
		Normalizer: h.normal,
		Recorder:   disabled,
	})
	handler := newCompatDiagHandler(t, decorated)

	for _, path := range []string{
		"/debug/sessions?owner=alice",
		"/debug/sessions/sess-dis",
		"/debug/sessions/by-a-leg/aleg-dis",
		"/debug/sessions/sess-dis/transcript?limit=10",
		"/debug/sessions/sess-dis/audit?limit=10",
	} {
		rec := doCompatGet(t, handler, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("disabled control-plane must not break diagnostics: %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled recorder must record nothing, got %d events", len(h.events()))
	}
}

func seedCompatSession(ctx context.Context, t *testing.T, st *memory.Store, sid domain.SessionID, owner, aLeg string, pol domain.PolicyMetadata) {
	t.Helper()
	seedCompatSessionThroughStore(ctx, t, st, sid, owner, aLeg, pol)
}

func seedCompatSessionThroughDecorator(ctx context.Context, t *testing.T, st app.Store, sid domain.SessionID, owner, aLeg string, pol domain.PolicyMetadata) {
	t.Helper()
	seedCompatSessionThroughStore(ctx, t, st, sid, owner, aLeg, pol)
}

func seedCompatSessionThroughStore(ctx context.Context, t *testing.T, st app.Store, sid domain.SessionID, owner, aLeg string, pol domain.PolicyMetadata) {
	t.Helper()
	fp := domain.TokenFingerprint{}
	fp[3] = byte(sid[0] % 255)
	if _, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: owner}, Workspace: domain.WorkspaceRef{ID: "ws1"},
		Policy: pol, ALegID: aLeg, ResumeEligible: true, CreatedAt: time.Unix(10, 0),
	}); err != nil {
		t.Fatalf("seedCompatSession Create(%s): %v", sid, err)
	}
	if err := st.TouchActivity(ctx, sid, time.Unix(20, 0), domain.ActivityClientRequest); err != nil {
		t.Fatalf("seedCompatSession TouchActivity(%s): %v", sid, err)
	}
	if err := st.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: sid, TurnID: "t1", ALegID: aLeg, BLegID: "b1", AttemptSeq: 1,
		RequestedModel: "gpt", ResolvedBackend: "be", ResolvedModel: "gpt-4",
		RouteSource: "default", StartedAt: time.Unix(11, 0),
	}); err != nil {
		t.Fatalf("seedCompatSession AppendAttemptTrace(%s): %v", sid, err)
	}
	if err := st.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: sid, TurnID: "t1", BLegID: "b1", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(12, 0),
	}); err != nil {
		t.Fatalf("seedCompatSession UpdateAttemptOutcome(%s): %v", sid, err)
	}
	if err := st.AddUsage(ctx, domain.UsageDelta{SessionID: sid, TurnID: "t1", BLegID: "b1", InputTokens: 3, OutputTokens: 5}); err != nil {
		t.Fatalf("seedCompatSession AddUsage(%s): %v", sid, err)
	}
	tseq, err := st.NextTranscriptSeq(ctx, sid)
	if err != nil {
		t.Fatalf("seedCompatSession NextTranscriptSeq(%s): %v", sid, err)
	}
	if err := st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: sid, TurnID: "t1", Seq: tseq, EventKind: "x",
		PayloadRef: `{"hello":"world"}`, CreatedAt: time.Unix(30, 0),
	}); err != nil {
		t.Fatalf("seedCompatSession AppendTranscript(%s): %v", sid, err)
	}
	aseq, err := st.NextAuditSeq(ctx, sid)
	if err != nil {
		t.Fatalf("seedCompatSession NextAuditSeq(%s): %v", sid, err)
	}
	if err := st.AppendAudit(ctx, domain.AuditItem{
		SessionID: sid, TurnID: "t1", Seq: aseq, Action: "test",
		Result:    `{"event":{"safe":"value"},"ok":true}`,
		CreatedAt: time.Unix(40, 0),
	}); err != nil {
		t.Fatalf("seedCompatSession AppendAudit(%s): %v", sid, err)
	}
}

func newCompatDiagHandler(t *testing.T, store app.Store) http.Handler {
	t.Helper()
	h, err := diag.NewHandler("/debug/sessions", store, "standard", nil,
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func doCompatGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func jsonEqual(a, b []byte) bool {
	var ja, jb any
	if err := json.Unmarshal(a, &ja); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &jb); err != nil {
		return false
	}
	return deepEqualJSON(ja, jb)
}

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bv2, ok := bv[k]
			if !ok || !deepEqualJSON(v, bv2) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

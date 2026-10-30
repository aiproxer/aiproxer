package frontendpipe_test

// Task 5 (large-payload-lifecycle-correctness): bind pathless cleanup notices
// to production capture. Production CaptureCandidateBody must install the
// existing source-owned OnRemovalFailure notification on the frontend logger
// fallback with a fixed failure reason, independently of ignored Close
// errors. The notice must carry no path, prompt bytes, digest, model, or raw
// filesystem error, and must not revise the stream outcome or retain the
// response writer (Requirements 4.1, 4.2, 4.3, 4.4, 3.4).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

const (
	lplcTask5CanaryPrompt = "LPLC_TASK5_CANARY_PROMPT_ALPHA"
	lplcTask5CanaryModel  = "lplc-task5-canary-model"
	lplcTask5NoticeReason = "spill_remove_failed"
)

// lplcTask5SyncBuffer is a goroutine-safe slog sink so the observed notice
// text is complete without sleeps once the triggering Close returns.
type lplcTask5SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lplcTask5SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lplcTask5SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lplcTask5CountingWriter observes response-writer use so the notice path can
// prove it never retains or touches the abandoned writer.
type lplcTask5CountingWriter struct {
	http.ResponseWriter
	mu      sync.Mutex
	writes  int
	headers int
	codes   []int
}

func (w *lplcTask5CountingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	return w.ResponseWriter.Write(p)
}

func (w *lplcTask5CountingWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.headers++
	w.codes = append(w.codes, code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *lplcTask5CountingWriter) counts() (writes, headers int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes, w.headers
}

// lplcTask5Payload builds a valid JSON body carrying the canary prompt and
// model through the production failure path.
func lplcTask5Payload() []byte {
	return []byte(`{"model":"` + lplcTask5CanaryModel + `","messages":[{"role":"user","content":"` +
		lplcTask5CanaryPrompt + strings.Repeat("B", 8*1024) + `"}]}`)
}

// lplcTask5BreakUnlink fault-injects a deferred unlink failure that survives
// process privileges: the spill file becomes a non-empty directory, so the
// lifecycle's single os.Remove fails with a path-carrying raw error.
func lplcTask5BreakUnlink(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove spilled file for fault injection: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir blocking spill path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking dir child: %v", err)
	}
}

func lplcTask5CleanupBreak(t *testing.T, path string) {
	t.Helper()
	_ = os.Remove(filepath.Join(path, "child"))
	_ = os.Remove(path)
}

// lplcTask5AssertPathlessNotice checks exact-once delivery of the fixed reason
// and omission of every forbidden value. Each forbidden value is genuinely
// present in the failure path (real spill path, real prompt/model bytes, real
// digest, real raw error text), so a notice that logged any of them fails.
func lplcTask5AssertPathlessNotice(t *testing.T, logText, spillPath, digestHex, rawErr string) {
	t.Helper()
	if got := strings.Count(logText, lplcTask5NoticeReason); got != 1 {
		t.Fatalf("notice reason %q appears %d times, want exactly once; log=%q", lplcTask5NoticeReason, got, logText)
	}
	forbidden := map[string]string{
		"spill path":       spillPath,
		"spool dir":        filepath.Dir(spillPath),
		"prompt bytes":     lplcTask5CanaryPrompt,
		"model":            lplcTask5CanaryModel,
		"source digest":    digestHex,
		"raw unlink error": rawErr,
	}
	for name, v := range forbidden {
		if v == "" {
			t.Fatalf("forbidden %s value is empty: non-vacuous assertion needs a live value", name)
		}
		if strings.Contains(logText, v) {
			t.Fatalf("notice leaks %s %q; log=%q", name, v, logText)
		}
	}
}

func lplcTask5DigestHex(payload []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func lplcTask5CaptureSpec(logBuf *lplcTask5SyncBuffer, dir string, ledger *largebody.SpoolLedger) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Log:        slog.New(slog.NewTextHandler(logBuf, nil)),
			FrontendID: "lplc_task5_test",
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:          true,
				ThresholdBytes:   1024,
				MemorySpoolBytes: 64,
				SpoolDir:         dir,
				SpoolLedger:      ledger,
			},
		},
	}
}

// lplcTask5CaptureSpilled runs production capture over payload and returns
// the spilled completed source plus the artifacts every capture-based notice
// test needs: the counting writer (untouched by the notice path), the log
// sink, the ledger, the spill path, and the payload digest. Each test keeps
// its own fault injection and assertions.
func lplcTask5CaptureSpilled(t *testing.T, payload []byte) (completed *largebody.CompletedSource, cw *lplcTask5CountingWriter, logBuf *lplcTask5SyncBuffer, ledger *largebody.SpoolLedger, spillPath, digestHex string) {
	t.Helper()
	dir := t.TempDir()
	logBuf = &lplcTask5SyncBuffer{}
	ledger = lplcTask5Ledger(t)
	spec := lplcTask5CaptureSpec(logBuf, dir, ledger)
	cw = &lplcTask5CountingWriter{ResponseWriter: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	capRes, _, err := frontendpipe.CaptureCandidateBody(context.Background(), &spec, cw, req, 4<<20)
	if err != nil {
		t.Fatalf("CaptureCandidateBody: %v", err)
	}
	if capRes.Outcome != largebody.CaptureOutcomeCompleted || capRes.Completed == nil {
		t.Fatalf("outcome=%v completed=%v, want completed source", capRes.Outcome, capRes.Completed != nil)
	}
	if !capRes.Completed.HasSpilled() {
		t.Fatal("capture did not spill: failure path needs a spilled source")
	}
	digestHex = lplcTask5DigestHex(payload)
	if capRes.Digest.String() != digestHex {
		t.Fatalf("digest=%q want %q (digest must exist in the lifecycle for omission to mean anything)", capRes.Digest.String(), digestHex)
	}
	spillPath = capRes.Completed.FilePath()
	if spillPath == "" {
		t.Fatal("empty spill path before fault injection")
	}
	return capRes.Completed, cw, logBuf, ledger, spillPath, digestHex
}

// lplcTask5ServeDeclined serves payload through a declined assessment with a
// spilling ledger and returns the captured completed source. When
// breakUnlinkInDecode the decode hook faults the unlink, so the serving path
// below observes an immediate cleanup failure without revising the response.
func lplcTask5ServeDeclined(t *testing.T, payload []byte, logBuf *lplcTask5SyncBuffer, w http.ResponseWriter, breakUnlinkInDecode bool) (completed *largebody.CompletedSource, ledger *largebody.SpoolLedger) {
	t.Helper()
	dir := t.TempDir()
	ledger = lplcTask5Ledger(t)
	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
	}

	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:          true,
			ThresholdBytes:   1024,
			MemorySpoolBytes: 64,
			SpoolDir:         dir,
			SpoolLedger:      ledger,
		},
		limiter,
		nil,
		nil,
		func(frontendpipe.DecodeContext) {
			// The replay body is already read; only the deferred root Close
			// remains, so breaking the unlink here cannot change the response.
			if breakUnlinkInDecode && completed != nil {
				lplcTask5BreakUnlink(t, completed.FilePath())
			}
		},
	)
	spec.Log = slog.New(slog.NewTextHandler(logBuf, nil))
	spec.OnCandidateCapture = func(r *http.Request, res frontendpipe.CandidateCaptureResult) {
		completed = res.Completed
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	frontendpipe.ServeHTTP(&spec, w, req)

	if completed == nil {
		t.Fatal("no completed source captured: serving path did not run through production capture")
	}
	return completed, ledger
}

func lplcTask5Ledger(t *testing.T) *largebody.SpoolLedger {
	t.Helper()
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}
	return ledger
}

// TestLargePayloadLifecycle_ProductionSpillNoticeImmediateOnIgnoredRootClose
// proves 4.3/4.2/4.4 through production capture: an immediate unlink failure
// produces exactly one pathless notice even when the root Close error is
// ignored, failure state stays published, and the reservation still releases.
func TestLargePayloadLifecycle_ProductionSpillNoticeImmediateOnIgnoredRootClose(t *testing.T) {
	t.Parallel()

	payload := lplcTask5Payload()
	completed, cw, logBuf, ledger, spillPath, digestHex := lplcTask5CaptureSpilled(t, payload)

	lplcTask5BreakUnlink(t, spillPath)
	defer lplcTask5CleanupBreak(t, spillPath)

	// Production call sites ignore this Close error; the notice is the only signal.
	_ = completed.Close()

	rawProbe := ""
	if perr := os.Remove(spillPath); perr != nil {
		rawProbe = perr.Error()
	} else {
		t.Fatal("probe unlink unexpectedly succeeded: fault injection did not hold")
	}
	lplcTask5AssertPathlessNotice(t, logBuf.String(), spillPath, digestHex, rawProbe)

	// Truthful state publication survives the notice: failure retained, no retry.
	if got := completed.FilePath(); got != spillPath {
		t.Fatalf("FilePath=%q want retained %q", got, spillPath)
	}
	if err := completed.Close(); err != nil {
		t.Fatalf("repeated Close=%v want nil (no retry)", err)
	}
	if got := strings.Count(logBuf.String(), lplcTask5NoticeReason); got != 1 {
		t.Fatalf("notice appears %d times after repeat, want still 1", got)
	}
	if got := ledger.InflightBytes(); got != 0 {
		t.Fatalf("InflightBytes=%d want 0 (reservation released despite failure)", got)
	}
	if w, h := cw.counts(); w != 0 || h != 0 {
		t.Fatalf("writer touched by notice path: writes=%d headers=%d, want 0/0", w, h)
	}
}

// TestLargePayloadLifecycle_ProductionSpillNoticeDeferredOnIgnoredReaderClose
// proves 4.3/4.1 through production capture: with an active reader the root
// Close stays silent, and the final reader Close (error ignored) produces
// exactly one pathless notice.
func TestLargePayloadLifecycle_ProductionSpillNoticeDeferredOnIgnoredReaderClose(t *testing.T) {
	t.Parallel()

	payload := lplcTask5Payload()
	completed, cw, logBuf, ledger, spillPath, digestHex := lplcTask5CaptureSpilled(t, payload)

	rc, err := completed.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	lplcTask5BreakUnlink(t, spillPath)
	defer lplcTask5CleanupBreak(t, spillPath)

	_ = completed.Close()
	if got := logBuf.String(); strings.Contains(got, lplcTask5NoticeReason) {
		t.Fatalf("notice fired at root close with active reader, want deferred; log=%q", got)
	}

	// Final reader Close error is ignored at production call sites as well.
	_ = rc.Close()

	rawProbe := ""
	if perr := os.Remove(spillPath); perr != nil {
		rawProbe = perr.Error()
	} else {
		t.Fatal("probe unlink unexpectedly succeeded: fault injection did not hold")
	}
	lplcTask5AssertPathlessNotice(t, logBuf.String(), spillPath, digestHex, rawProbe)

	if got := completed.FilePath(); got != spillPath {
		t.Fatalf("FilePath=%q want retained %q", got, spillPath)
	}
	if got := ledger.InflightBytes(); got != 0 {
		t.Fatalf("InflightBytes=%d want 0 (reservation released despite deferred failure)", got)
	}
	if w, h := cw.counts(); w != 0 || h != 0 {
		t.Fatalf("writer touched by notice path: writes=%d headers=%d, want 0/0", w, h)
	}
}

// TestLargePayloadLifecycle_ProductionSpillNoticePreservesServingOutput proves
// 4.4/3.4 end to end: an immediate unlink failure during serving still yields
// the exact successful response (notice cannot revise the stream outcome) with
// exactly one pathless notice and no extra writer use.
func TestLargePayloadLifecycle_ProductionSpillNoticePreservesServingOutput(t *testing.T) {
	t.Parallel()

	payload := lplcTask5Payload()
	logBuf := &lplcTask5SyncBuffer{}
	cw := &lplcTask5CountingWriter{ResponseWriter: httptest.NewRecorder()}
	completed, ledger := lplcTask5ServeDeclined(t, payload, logBuf, cw, true)

	rec, ok := cw.ResponseWriter.(*httptest.ResponseRecorder)
	if !ok {
		t.Fatal("counting writer does not wrap *httptest.ResponseRecorder")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (cleanup failure must not revise outcome); body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("body=%q want exact success output (no notice contamination)", got)
	}
	if w, h := cw.counts(); w != 1 || h != 1 {
		t.Fatalf("writer calls writes=%d headers=%d, want 1/1 (notice must not use the writer)", w, h)
	}

	cs := completed
	if cs == nil {
		t.Fatal("no completed source captured: serving path did not run through production capture")
	}
	spillPath := cs.FilePath()
	if spillPath == "" {
		t.Fatal("empty spill path: failure state was not retained")
	}
	defer lplcTask5CleanupBreak(t, spillPath)
	rawProbe := ""
	if perr := os.Remove(spillPath); perr != nil {
		rawProbe = perr.Error()
	} else {
		t.Fatal("probe unlink unexpectedly succeeded: fault injection did not hold")
	}
	lplcTask5AssertPathlessNotice(t, logBuf.String(), spillPath, lplcTask5DigestHex(payload), rawProbe)

	if err := cs.Close(); err != nil {
		t.Fatalf("repeated Close=%v want nil (single unlink attempt, no retry)", err)
	}
	if got := strings.Count(logBuf.String(), lplcTask5NoticeReason); got != 1 {
		t.Fatalf("notice appears %d times after repeat, want still 1", got)
	}
	if got := ledger.InflightBytes(); got != 0 {
		t.Fatalf("InflightBytes=%d want 0", got)
	}
}

// TestLargePayloadLifecycle_ProductionSpillNoticeSilentOnSuccess proves the
// negative: clean serving emits no notice, reclaims the spill, and releases
// the reservation.
func TestLargePayloadLifecycle_ProductionSpillNoticeSilentOnSuccess(t *testing.T) {
	t.Parallel()

	payload := lplcTask5Payload()
	logBuf := &lplcTask5SyncBuffer{}
	rec := httptest.NewRecorder()
	completed, ledger := lplcTask5ServeDeclined(t, payload, logBuf, rec, false)

	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%q, want 200 + exact success output", rec.Code, rec.Body.String())
	}
	if got := logBuf.String(); strings.Contains(got, lplcTask5NoticeReason) {
		t.Fatalf("notice fired on successful cleanup; log=%q", got)
	}
	cs := completed
	if cs == nil {
		t.Fatal("no completed source captured")
	}
	if got := cs.FilePath(); got != "" {
		t.Fatalf("FilePath=%q want empty (spill reclaimed)", got)
	}
	if got := ledger.InflightBytes(); got != 0 {
		t.Fatalf("InflightBytes=%d want 0", got)
	}
}

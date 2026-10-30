package frontendpipe_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 7.4: Capture to EOF while running shared scanner —
// preserve body limit/error parity and Task 4 continuation on recoverable decline;
// unknown/chunked final size below threshold => canonical from source.
// Requirements 1, 2, 3, 20; design target-flow + replay sections.

func newCandidateCaptureSpec(
	exec *candidateGatesExec,
	prof frontendpipe.FrontendProfile,
	cfg frontendpipe.LargePayloadConfig,
	captureRecord *[]frontendpipe.CandidateCaptureResult,
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:         exec,
			FrontendID:   "candidate_capture_test",
			LargePayload: cfg,
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{
					ID: "call_capture_test",
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart(string(dctx.Body))},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return nil
		},
		OnCandidateCapture: func(r *http.Request, res frontendpipe.CandidateCaptureResult) {
			if captureRecord != nil {
				*captureRecord = append(*captureRecord, res)
			}
		},
	}
}

// buildJSONPayload builds a valid JSON body of approximately the requested size.
func buildJSONPayload(targetBytes int) []byte {
	padLen := max(0, targetBytes-100)
	padding := strings.Repeat("A", padLen)
	payload := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "user", "content": padding},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func TestCandidateCapture_CompletedSource_Success(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}
	payload := buildJSONPayload(1200 * 1024) // 1.2 MiB (> 1 MiB threshold)
	expectedHash := sha256.Sum256(payload)

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20, // 1 MiB
	}, &captureRecord)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	capRes := captureRecord[0]
	if capRes.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected OutcomeCompleted, got %v", capRes.Outcome)
	}
	if capRes.Completed == nil {
		t.Fatal("expected non-nil CompletedSource")
	}
	if capRes.Completed.Size() != int64(len(payload)) {
		t.Fatalf("CompletedSource size: got %d, want %d", capRes.Completed.Size(), len(payload))
	}
	if capRes.Digest.String() != fmt.Sprintf("%x", expectedHash) {
		t.Fatalf("digest mismatch: got %s, want %x", capRes.Digest.String(), expectedHash)
	}
	if capRes.ScannerErr != nil {
		t.Fatalf("unexpected scanner error: %v", capRes.ScannerErr)
	}
	if capRes.ScannerResult.Bytes != len(payload) {
		t.Fatalf("scanner bytes: got %d, want %d", capRes.ScannerResult.Bytes, len(payload))
	}
}

func TestCandidateCapture_ConcurrentScanner_MalformedJSON(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Malformed JSON over 1 MiB threshold
	prefix := []byte(`{"model":"gpt-4o","content":"`)
	badSuffix := []byte(`"not-closed-properly...`)
	pad := bytes.Repeat([]byte("X"), 1100*1024)
	malformed := append(prefix, pad...)
	malformed = append(malformed, badSuffix...)

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20, // 1 MiB
	}, &captureRecord)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(malformed))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Parity with canonical jsonguard: malformed JSON returns 400 Invalid JSON
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for malformed JSON, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") && !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("expected error JSON body, got: %s", rec.Body.String())
	}
}

func TestCandidateCapture_BodyLimitExceeded_413Parity(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Body ceiling 1.5 MiB, request sends 1.6 MiB
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20, // 1 MiB
	}, nil)
	spec.MaxRequestBodyBytes = 1500 * 1024 // 1.5 MiB

	payload := buildJSONPayload(1600 * 1024) // 1.6 MiB > limit
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Parity with canonical reqbody: returns HTTP 413
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected HTTP 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

type errReader struct {
	data   []byte
	errAt  int
	read   int
	errVal error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.read >= r.errAt {
		return 0, r.errVal
	}
	available := r.errAt - r.read
	toRead := min(len(p), available)
	copy(p, r.data[r.read:r.read+toRead])
	r.read += toRead
	if r.read >= r.errAt {
		return toRead, r.errVal
	}
	return toRead, nil
}

func (r *errReader) Close() error {
	return nil
}

func TestCandidateCapture_ClientReadError_Parity(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20,
	}, nil)

	payload := buildJSONPayload(1200 * 1024)
	reader := &errReader{
		data:   payload,
		errAt:  50 * 1024,
		errVal: errors.New("simulated client socket reset"),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", reader)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Parity with canonical reqbody.ReadAll: returns HTTP 400 read failed
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for socket read error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCandidateCapture_EarlySpoolBudgetExhaustion_LosslessContinuation(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Ledger budget = 500 KiB, but request is 1.2 MiB (> threshold)
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 500 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20, // 1 MiB
		SpoolLedger:    ledger,
	}, &captureRecord)

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Requirement 1.5 & 20.5: Budget exhaustion is optimization decline, NOT 413.
	// Request continues losslessly through canonical path and succeeds!
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on spool budget exhaustion, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	if captureRecord[0].Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("expected OutcomeDeclined, got %v", captureRecord[0].Outcome)
	}
	if !largebody.IsSpoolBudgetExhausted(captureRecord[0].Err) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got %v", captureRecord[0].Err)
	}
}

func TestCandidateCapture_IncrementalSpoolBudgetExhaustion_LosslessContinuation(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Ledger with 300 KiB budget; request is chunked stream of 600 KiB
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 300 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 256 * 1024, // 256 KiB threshold
		SpoolLedger:    ledger,
	}, &captureRecord)

	payload := buildJSONPayload(600 * 1024)
	// Chunked request: ContentLength = -1
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Recoverable decline via CaptureReader: serves 200 OK without client error
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on incremental spool budget exhaustion, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	if captureRecord[0].Outcome != largebody.CaptureOutcomeDeclined {
		t.Fatalf("expected OutcomeDeclined, got %v", captureRecord[0].Outcome)
	}
}

func TestCandidateCapture_ChunkedFinalBelowThreshold_CanonicalFromSource(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20, // 1 MiB
	}, &captureRecord)

	// Payload is only 10 KiB (far below 1 MiB threshold)
	payload := buildJSONPayload(10 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // Chunked/unknown
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	capRes := captureRecord[0]
	if capRes.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected OutcomeCompleted on EOF, got %v", capRes.Outcome)
	}
	if capRes.Completed == nil {
		t.Fatal("expected non-nil CompletedSource")
	}
	if capRes.Completed.Size() != int64(len(payload)) {
		t.Fatalf("size mismatch: got %d, want %d", capRes.Completed.Size(), len(payload))
	}
}

func TestCandidateCapture_SpillToDisk_LargePayload(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Memory spool bytes 64 KiB, payload 1.5 MiB => forces spill to temporary file
	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   1 << 20, // 1 MiB
		MemorySpoolBytes: 64 * 1024,
	}, &captureRecord)

	payload := buildJSONPayload(1500 * 1024)
	expectedHash := sha256.Sum256(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	capRes := captureRecord[0]
	if capRes.Outcome != largebody.CaptureOutcomeCompleted {
		t.Fatalf("expected OutcomeCompleted, got %v", capRes.Outcome)
	}
	if capRes.Completed.Size() != int64(len(payload)) {
		t.Fatalf("size mismatch: got %d, want %d", capRes.Completed.Size(), len(payload))
	}
	if capRes.Digest.String() != fmt.Sprintf("%x", expectedHash) {
		t.Fatalf("digest mismatch: got %s, want %x", capRes.Digest.String(), expectedHash)
	}
}

func TestCandidateCapture_TerminalCaptureError_Returns500Never400(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{profileID: "test_profile_v1"}

	// Ledger with 100 KiB budget so 300 KiB Reserve fails with ErrSpoolBudgetExhausted
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 100 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	var captureRecord []frontendpipe.CandidateCaptureResult
	spec := newCandidateCaptureSpec(exec, prof, frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 256 * 1024,
		SpoolLedger:    ledger,
	}, &captureRecord)

	// Request with ContentLength > 0 to trigger Reserve, but nil Body to force NewCaptureReader failure
	req := httptest.NewRequest(http.MethodPost, "/v1/create", nil)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 300 * 1024
	req.Body = nil
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// M5 requirement: terminal capture errors map to HTTP 500, never 400
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 for terminal capture failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(captureRecord) != 1 {
		t.Fatalf("expected 1 capture record, got %d", len(captureRecord))
	}
	if captureRecord[0].Outcome != largebody.CaptureOutcomeTerminalError {
		t.Fatalf("expected OutcomeTerminalError, got %v", captureRecord[0].Outcome)
	}
}

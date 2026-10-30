package service

// Usage exhaustion is TERMINAL for the selected registration (requirements
// 2.13, 8.3 and 8.6).
//
// These proofs are negative proofs: they exist to fail when any substitution
// path is added. A second provider request, a different registration's
// credential, or a fallback model id on the exhaustion answer must each break
// one of them, and the failure must name that reason.
//
// The reference fixtures answer the FIRST and only request with the documented
// plan-limit outcome, so a one-attempt proof cannot be satisfied by sampling a
// cached answer or another profile's state: the attempt being proven IS the
// attempt that was answered.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	// documentedUsageLimitCode is the provider's NAMED plan-limit outcome.
	// Only this code makes an answer non-retryable usage exhaustion; a bare
	// 429 stays temporary throttling.
	documentedUsageLimitCode = "subscription_sharing_usage_limit_exceeded"
	// documentedUsageUnavailableCode is the documented usage-unavailable
	// outcome. It terminates the selected-profile attempt as well, but its
	// class stays retryable temporary: a later attempt may succeed with the
	// same registration, and it never authorizes another subscriber.
	documentedUsageUnavailableCode = "subscription_sharing_usage_unavailable"
	// planLimitRequestID is the provider correlation id of the exhaustion
	// answer. The typed failure must keep it.
	planLimitRequestID = "req-plan-limit"

	// referenceOtherProfileID and referenceOtherAccessToken belong to a
	// SECOND saved registration that is never selected. They make a profile
	// rotation observable: the other registration exists and could be used,
	// so any request carrying its credential is substitution.
	referenceOtherProfileID    = "acct-other"
	referenceOtherAccessToken  = "other-registration-token-not-a-secret"
	referenceOtherRefreshToken = "other-registration-refresh-token-not-a-secret"
)

// usageExhaustionScript is a stream that opens and then reports the documented
// plan-limit failure as its terminal. No visible content is delivered before
// the failure, so the terminal is the first and only downstream verdict.
var usageExhaustionScript = []string{
	sseFrame("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`),
	sseFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"`+documentedUsageLimitCode+`","message":"plan usage limit reached"}}}`),
}

// newUsageOutcomeReference returns a reference server that answers every
// request with the named provider outcome. The FIRST request is already the
// exhausted one, so no earlier success can be mistaken for the attempt under
// proof.
func newUsageOutcomeReference(t *testing.T, status int, code string) *planReference {
	t.Helper()
	ref := newPlanReference(t, nil)
	ref.status = status
	ref.requestID = planLimitRequestID
	ref.errorBody = `{"error":{"message":"the plan allowance for this account is exhausted","type":"usage_limit","code":"` + code + `"}}`
	return ref
}

// stageOtherRegistration saves a second, never-selected registration in the
// same store. It is the potential rotation target, not a selected profile.
func stageOtherRegistration(t *testing.T, storePath string) siwc.ProfileRecord {
	t.Helper()
	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	record := siwc.ProfileRecord{
		Version:        1,
		ID:             referenceOtherProfileID,
		Subject:        "subject-other",
		IssuedClientID: "client-other",
		HostID:         "host-reference",
		GrantedScopes:  []string{siwc.PlanUseScope},
		AccessToken:    referenceOtherAccessToken,
		RefreshToken:   referenceOtherRefreshToken,
		Expiry:         time.Now().Add(time.Hour).UTC(),
	}
	if err := store.SaveNew(record); err != nil {
		t.Fatalf("save the second registration: %v", err)
	}
	return record
}

// TestExecute_UsageExhaustionIsTerminalOnTheSelectedProfile proves the
// observable completion of task 6.2: the documented plan-limit answer ends the
// Execute call with one terminal, TYPED failure that names the selected local
// registration and the provider request id.
//
// A silent empty result would satisfy "no substitution" while hiding the
// degradation, so the failure itself is asserted: class, phase, source,
// status, code, request id, non-retryability, and profile provenance.
func TestExecute_UsageExhaustionIsTerminalOnTheSelectedProfile(t *testing.T) {
	t.Parallel()
	provider := newUsageOutcomeReference(t, http.StatusTooManyRequests, documentedUsageLimitCode)
	storePath, selected := stageProfileStore(t)
	other := stageOtherRegistration(t, storePath)
	selectProfile(t, storePath, selected.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("usage exhaustion must fail the attempt explicitly, not end it silently")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("Execute error %T is not the connector's typed classification: %v", err, err)
	}
	if classified.Class != responses.ErrorClassUsageExhausted {
		t.Fatalf("exhaustion class = %q, want %q", classified.Class, responses.ErrorClassUsageExhausted)
	}
	if classified.Retryable {
		t.Fatal("the named plan-limit outcome is terminal for the selected profile; it must not be retryable")
	}
	if classified.Phase != responses.ErrorPhaseAdmission {
		t.Fatalf("exhaustion phase = %q, want %q", classified.Phase, responses.ErrorPhaseAdmission)
	}
	if classified.Source != responses.ErrorSourceProvider {
		t.Fatalf("exhaustion source = %q, want %q", classified.Source, responses.ErrorSourceProvider)
	}
	if classified.Status != http.StatusTooManyRequests {
		t.Fatalf("exhaustion status = %d, want the provider's %d", classified.Status, http.StatusTooManyRequests)
	}
	if classified.Code != documentedUsageLimitCode {
		t.Fatalf("exhaustion code = %q, want the documented %q", classified.Code, documentedUsageLimitCode)
	}
	if classified.RequestID != planLimitRequestID {
		t.Fatalf("exhaustion request id = %q, want %q", classified.RequestID, planLimitRequestID)
	}
	if classified.ProfileID != selected.ID {
		t.Fatalf("exhaustion profile provenance = %q, want the selected %q", classified.ProfileID, selected.ID)
	}

	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("the exhaustion answer must end the selected profile's ONE attempt, got %d provider requests", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer "+selected.AccessToken {
		t.Fatalf("the only request carried %q, want the selected registration's credential", got)
	}

	events, evidence, terminals, failures := countFrames(frames)
	if events != 0 {
		t.Fatalf("usage exhaustion delivered %d content event(s) before the terminal; the fixture is not a first-request exhaustion", events)
	}
	if evidence != 0 {
		t.Fatalf("a failed attempt emitted %d accounting evidence record(s)", evidence)
	}
	if terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
	for _, secret := range []string{selected.AccessToken, other.AccessToken, selected.RefreshToken, other.RefreshToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("the typed exhaustion failure rendered credential material")
		}
	}
}

// TestExecute_UsageExhaustionMakesOneSelectedProfileAttempt is the one-attempt
// proof: exactly ONE provider request is made, with the SELECTED registration's
// credential, and the never-selected registration's credential is never sent.
//
// A second attempt, a profile rotation, or a retry loop must fail here for
// that reason. The second registration exists on disk and could be selected,
// so the proof is not vacuous.
func TestExecute_UsageExhaustionMakesOneSelectedProfileAttempt(t *testing.T) {
	t.Parallel()
	provider := newUsageOutcomeReference(t, http.StatusTooManyRequests, documentedUsageLimitCode)
	storePath, selected := stageProfileStore(t)
	other := stageOtherRegistration(t, storePath)
	selectProfile(t, storePath, selected.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("usage exhaustion must fail the attempt")
	}

	requests := provider.captured()
	for index, request := range requests {
		if strings.Contains(request.Header.Get("Authorization"), other.AccessToken) {
			t.Fatalf("provider request %d carried the never-selected registration's credential; usage exhaustion must not rotate profiles", index+1)
		}
	}
	if len(requests) != 1 {
		t.Fatalf("usage exhaustion must stop after the selected profile's single attempt, got %d provider requests", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer "+selected.AccessToken {
		t.Fatalf("the only request carried %q, want the selected registration's credential", got)
	}
	if _, _, terminals, failures := countFrames(frames); terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
}

// TestExecute_UsageExhaustionDoesNotSubstituteTheModel proves no silent model
// substitution: every provider request carries EXACTLY the model the caller
// asked for, and no fallback/downgraded model id appears on the wire.
//
// The proof does not assert the request count; a substitution mutant that
// retries with a fallback model fails on the model, which is the reason this
// test names.
func TestExecute_UsageExhaustionDoesNotSubstituteTheModel(t *testing.T) {
	t.Parallel()
	provider := newUsageOutcomeReference(t, http.StatusTooManyRequests, documentedUsageLimitCode)
	storePath, selected := stageProfileStore(t)
	selectProfile(t, storePath, selected.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	inv := invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{})
	_, err := runExecute(context.Background(), inst, inv)
	if err == nil {
		t.Fatal("usage exhaustion must fail the attempt")
	}

	requests := provider.captured()
	if len(requests) == 0 {
		t.Fatal("the fixture observed no provider request, so it cannot prove the model that was sent")
	}
	for index, request := range requests {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatalf("decode provider request %d: %v", index+1, err)
		}
		if body.Model != inv.NativeModelID {
			t.Fatalf("provider request %d carried model %q, want the selected model %q; usage exhaustion must not substitute a fallback model", index+1, body.Model, inv.NativeModelID)
		}
	}
}

// TestExecute_StreamTerminalUsageExhaustionIsTerminalAndNotRetried proves the
// same terminal rule when the plan-limit outcome arrives as the stream's own
// `response.failed` terminal instead of an admission status. The attempt was
// already open, so the failure is a stream-phase typed failure, and it still
// does not restart or substitute.
func TestExecute_StreamTerminalUsageExhaustionIsTerminalAndNotRetried(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, usageExhaustionScript)
	storePath, selected := stageProfileStore(t)
	other := stageOtherRegistration(t, storePath)
	selectProfile(t, storePath, selected.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("a stream-terminal plan-limit failure must fail the attempt")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("Execute error %T is not the connector's typed classification: %v", err, err)
	}
	if classified.Class != responses.ErrorClassUsageExhausted {
		t.Fatalf("stream exhaustion class = %q, want %q", classified.Class, responses.ErrorClassUsageExhausted)
	}
	if classified.Retryable {
		t.Fatal("a stream-terminal plan-limit failure is terminal; it must not be retryable")
	}
	if classified.Phase != responses.ErrorPhaseStream {
		t.Fatalf("stream exhaustion phase = %q, want %q", classified.Phase, responses.ErrorPhaseStream)
	}
	if classified.Source != responses.ErrorSourceProvider {
		t.Fatalf("stream exhaustion source = %q, want %q", classified.Source, responses.ErrorSourceProvider)
	}
	if classified.Code != documentedUsageLimitCode {
		t.Fatalf("stream exhaustion code = %q, want %q", classified.Code, documentedUsageLimitCode)
	}
	if classified.RequestID != "req_reference" {
		t.Fatalf("stream exhaustion request id = %q, want the transport correlation id %q", classified.RequestID, "req_reference")
	}
	if classified.ProfileID != selected.ID {
		t.Fatalf("stream exhaustion profile provenance = %q, want the selected %q", classified.ProfileID, selected.ID)
	}

	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("a stream-terminal plan-limit failure must not restart the attempt, got %d provider requests", len(requests))
	}
	for index, request := range requests {
		if strings.Contains(request.Header.Get("Authorization"), other.AccessToken) {
			t.Fatalf("provider request %d carried the never-selected registration's credential; a stream-terminal plan-limit failure must not rotate profiles", index+1)
		}
	}
	if _, evidence, terminals, failures := countFrames(frames); evidence != 0 || terminals != 1 || failures != 1 {
		t.Fatalf("evidence=%d terminals=%d failures=%d, want no evidence and one failure terminal", evidence, terminals, failures)
	}
}

// TestExecute_UsageUnavailableIsRetryableButStillOneSelectedProfileAttempt
// pins the 5.3 r1-F3 boundary: usage-unavailable keeps the retryable temporary
// class (a LATER attempt on the same registration may succeed), while THIS
// attempt still ends after one request on the selected registration and never
// authorizes another subscriber.
func TestExecute_UsageUnavailableIsRetryableButStillOneSelectedProfileAttempt(t *testing.T) {
	t.Parallel()
	provider := newUsageOutcomeReference(t, http.StatusTooManyRequests, documentedUsageUnavailableCode)
	storePath, selected := stageProfileStore(t)
	other := stageOtherRegistration(t, storePath)
	selectProfile(t, storePath, selected.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	_, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("usage unavailable must fail the attempt")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("Execute error %T is not the connector's typed classification: %v", err, err)
	}
	if classified.Class != responses.ErrorClassTemporaryUnavailable {
		t.Fatalf("usage-unavailable class = %q, want %q", classified.Class, responses.ErrorClassTemporaryUnavailable)
	}
	if !classified.Retryable {
		t.Fatal("usage_unavailable is a temporary condition; the classification must stay retryable for a later attempt")
	}
	if classified.Status != http.StatusTooManyRequests {
		t.Fatalf("usage-unavailable status = %d, want the provider's %d", classified.Status, http.StatusTooManyRequests)
	}
	if classified.Code != documentedUsageUnavailableCode {
		t.Fatalf("usage-unavailable code = %q, want the documented %q", classified.Code, documentedUsageUnavailableCode)
	}
	if classified.RequestID != planLimitRequestID {
		t.Fatalf("usage-unavailable request id = %q, want %q", classified.RequestID, planLimitRequestID)
	}
	if classified.ProfileID != selected.ID {
		t.Fatalf("usage-unavailable profile provenance = %q, want the selected %q", classified.ProfileID, selected.ID)
	}

	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("usage unavailable must end this attempt after the selected profile's single request, got %d", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer "+selected.AccessToken {
		t.Fatalf("the only request carried %q, want the selected registration's credential", got)
	}
	for index, request := range requests {
		if strings.Contains(request.Header.Get("Authorization"), other.AccessToken) {
			t.Fatalf("provider request %d carried the never-selected registration's credential; usage unavailable must not rotate profiles", index+1)
		}
	}
}

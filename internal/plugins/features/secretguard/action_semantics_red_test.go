package secretguard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func newBetterLeaksActionGuard(t *testing.T, action string, scanMaxBytes, maxFindings int) (sdk.Guard, sdk.Services) {
	t.Helper()
	if maxFindings == 0 {
		maxFindings = DefaultBetterLeaksMaxFindings
	}
	services, err := BuildGenerationServices(DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       maxFindings,
		},
	}, engine.NewDisabledSource())
	if err != nil {
		t.Fatal("compose BetterLeaks generation services")
	}
	return NewGuard(Config{Action: action, ScanMaxBytes: scanMaxBytes}), sdk.Services{
		// Keep the exact source empty while passing the real generation service
		// through the existing opaque capability seam.
		MatcherResolver: engine.NewStaticMatcherResolver(nil, engine.MatcherOptions{}),
		Capability:      services,
	}
}

func betterLeaksTextCall(text string) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		}},
	}
}

func betterLeaksJSONCall(raw []byte) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: append([]byte(nil), raw...)}},
		}},
	}
}

func assertBetterLeaksDecisionSafe(t *testing.T, decision sdk.Decision) {
	t.Helper()
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal("marshal decision")
	}
	if bytes.Contains(raw, []byte(adapterGitHubToken)) {
		t.Fatal("decision contains detector source material")
	}
}

func fatalDecisionSummary(t *testing.T, decision sdk.Decision) {
	t.Helper()
	t.Fatalf("unexpected decision: outcome=%q findings=%d mutations=%d scan_limit=%t failure_kind_set=%t", decision.Outcome, len(decision.Findings), decision.MutationCount, decision.ScanLimitHit, decision.FailureKind != "")
}

func assertBetterLeaksFinding(t *testing.T, decision sdk.Decision) {
	t.Helper()
	if len(decision.Findings) == 0 {
		t.Fatal("BetterLeaks finding was not surfaced to Guard")
	}
	for _, finding := range decision.Findings {
		if finding.DetectorID != sdk.DetectorIDBetterLeaks {
			t.Fatalf("finding detector provenance = %q", finding.DetectorID)
		}
	}
	assertBetterLeaksDecisionSafe(t, decision)
}

func TestGuard_BetterLeaksActionSemanticsUseComposedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
	}{
		{name: "block", action: ActionBlock},
		{name: "log", action: ActionLog},
		{name: "redact", action: ActionRedact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, services := newBetterLeaksActionGuard(t, tc.action, 0, 0)
			call := betterLeaksTextCall("GITHUB_TOKEN=" + adapterGitHubToken)
			before := aipapi.CloneCall(call)

			decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
			if err != nil {
				t.Fatal("BetterLeaks action evaluation failed")
			}
			assertBetterLeaksFinding(t, decision)
			switch tc.action {
			case ActionBlock:
				if decision.Outcome != sdk.OutcomeBlock {
					t.Fatalf("outcome = %q", decision.Outcome)
				}
				if !reflect.DeepEqual(call, before) {
					t.Fatal("block mutated the canonical call")
				}
			case ActionLog:
				if decision.Outcome != sdk.OutcomeLog {
					t.Fatalf("outcome = %q", decision.Outcome)
				}
				if decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
					t.Fatal("log mutated the canonical call")
				}
			case ActionRedact:
				if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount == 0 {
					fatalDecisionSummary(t, decision)
				}
				got := call.Messages[0].Parts[0].Text
				if strings.Contains(got, adapterGitHubToken) {
					t.Fatal("redacted text still contains the detected token")
				}
				if len(got) != len(before.Messages[0].Parts[0].Text) {
					t.Fatal("text redaction changed byte length")
				}
				if !strings.Contains(got, "ghp_") {
					t.Fatal("text redaction did not preserve the declared public prefix")
				}
			}
		})
	}
}

func TestGuard_BetterLeaksRedactLiteralJSONKeepsCanonicalValidity(t *testing.T) {
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)
	raw := []byte(`{"credential":"` + adapterGitHubToken + `","count":7,"enabled":true}`)
	call := betterLeaksJSONCall(raw)

	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal("literal JSON redaction failed")
	}
	assertBetterLeaksFinding(t, decision)
	if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount == 0 {
		fatalDecisionSummary(t, decision)
	}
	if !json.Valid(call.Messages[0].Parts[0].Content) {
		t.Fatal("redaction produced invalid canonical JSON")
	}
	if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
		t.Fatal("redacted JSON still contains the detected token")
	}
	var decoded map[string]any
	if err := json.Unmarshal(call.Messages[0].Parts[0].Content, &decoded); err != nil {
		t.Fatal("parse redacted JSON")
	}
	if decoded["count"] != float64(7) || decoded["enabled"] != true {
		t.Fatal("redaction changed unrelated JSON values")
	}
	credential, ok := decoded["credential"].(string)
	if !ok || len(credential) != len(adapterGitHubToken) || !strings.HasPrefix(credential, "ghp_") {
		t.Fatal("JSON redaction did not preserve the declared public prefix and byte length")
	}
}

func TestGuard_BetterLeaksDecodedFindingActionMatrix(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(adapterGitHubToken))
	raw := []byte(`{"credential":"` + encoded + `"}`)
	for _, tc := range []struct {
		name        string
		action      string
		wantOutcome sdk.Outcome
		wantFailure string
		wantErr     bool
	}{
		{name: "block", action: ActionBlock, wantOutcome: sdk.OutcomeBlock},
		{name: "log", action: ActionLog, wantOutcome: sdk.OutcomeLog},
		{name: "redact", action: ActionRedact, wantOutcome: sdk.OutcomeBlock, wantFailure: "unrewritable_detected_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, services := newBetterLeaksActionGuard(t, tc.action, 0, 0)
			call := betterLeaksJSONCall(raw)
			before := aipapi.CloneCall(call)
			decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected bounded detector error")
				}
				return
			}
			if err != nil {
				t.Fatal("decoded finding evaluation failed")
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("decoded finding decision is not valid: %v", err)
			}
			assertBetterLeaksFinding(t, decision)
			if decision.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q", decision.Outcome)
			}
			if tc.wantFailure != "" && decision.FailureKind != tc.wantFailure {
				t.Fatalf("failure kind = %q", decision.FailureKind)
			}
			if !reflect.DeepEqual(call, before) {
				t.Fatal("decoded/unrewritable action mutated the canonical call")
			}
		})
	}
}

func TestGuard_BetterLeaksRedactMixedLiteralAndDecodedNeverCommitsPartialClone(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(adapterGitHubToken))
	call := betterLeaksTextCall("literal=" + adapterGitHubToken)
	call.Messages = append(call.Messages, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: []byte(`{"credential":"` + encoded + `"}`)}},
	})
	before := aipapi.CloneCall(call)
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)

	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal("mixed redaction should return bounded fail-closed decision")
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("mixed redaction decision is not valid: %v", err)
	}
	assertBetterLeaksFinding(t, decision)
	if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != "unrewritable_detected_secret" {
		fatalDecisionSummary(t, decision)
	}
	if decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
		t.Fatal("failed redaction committed a partial working clone")
	}
}

func TestGuard_BetterLeaksUnsupportedJSONKeyFailsClosedWithoutMutation(t *testing.T) {
	raw := []byte(`{"` + adapterGitHubToken + `":"safe"}`)
	call := betterLeaksJSONCall(raw)
	before := aipapi.CloneCall(call)
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)

	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal("unsupported JSON key should return bounded fail-closed decision")
	}
	assertBetterLeaksFinding(t, decision)
	if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != "unsupported_json_token" {
		fatalDecisionSummary(t, decision)
	}
	if decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
		t.Fatal("unsupported JSON key changed the canonical call")
	}
}

func TestGuard_BetterLeaksExistingNonStringJSONTokenFailsClosedWithoutMutation(t *testing.T) {
	call := betterLeaksJSONCall([]byte(`{"credential":123456789,"later":"safe"}`))
	before := aipapi.CloneCall(call)
	guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)
	// The exact matcher remains authoritative for existing unsupported-token
	// behavior even while the real BetterLeaks capability is present.
	services.MatcherResolver = staticResolver{m: newExactStub("123456789", "NUMERIC_TOKEN", sdk.SourceCategoryProxyEnv)}

	decision, err := guard.Evaluate(context.Background(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal("unsupported JSON scalar should return bounded fail-closed decision")
	}
	if len(decision.Findings) == 0 || decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != "unsupported_json_token" {
		fatalDecisionSummary(t, decision)
	}
	assertBetterLeaksDecisionSafe(t, decision)
	if decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
		t.Fatal("unsupported JSON scalar changed the canonical call")
	}
}

func TestGuard_BetterLeaksScanFailureActionMatrix(t *testing.T) {
	for _, action := range []string{ActionBlock, ActionRedact, ActionLog} {
		t.Run(action, func(t *testing.T) {
			guard, services := newBetterLeaksActionGuard(t, action, 0, 0)
			call := betterLeaksTextCall("GITHUB_TOKEN=" + adapterGitHubToken)
			before := aipapi.CloneCall(call)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			decision, err := guard.Evaluate(ctx, &call, sdk.Meta{}, services)
			if !errors.Is(err, context.Canceled) {
				t.Fatal("canceled BetterLeaks scan did not propagate cancellation")
			}
			if strings.Contains(err.Error(), adapterGitHubToken) {
				t.Fatal("canceled scan error exposed detector source material")
			}
			assertBetterLeaksDecisionSafe(t, decision)
			if !reflect.DeepEqual(call, before) {
				t.Fatal("canceled scan mutated the canonical call")
			}
		})
	}
}

func TestGuard_BetterLeaksFindingCapActionMatrix(t *testing.T) {
	call := aipapi.Call{}
	for range DefaultBetterLeaksMaxFindings + 1 {
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("GITHUB_TOKEN=" + adapterGitHubToken)},
		})
	}
	for _, tc := range []struct {
		name        string
		action      string
		wantOutcome sdk.Outcome
		wantErr     bool
	}{
		{name: "block", action: ActionBlock, wantErr: true},
		{name: "redact", action: ActionRedact, wantErr: true},
		{name: "log", action: ActionLog, wantOutcome: sdk.OutcomeLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, services := newBetterLeaksActionGuard(t, tc.action, 0, DefaultBetterLeaksMaxFindings)
			candidate := aipapi.CloneCall(call)
			before := aipapi.CloneCall(candidate)
			decision, err := guard.Evaluate(context.Background(), &candidate, sdk.Meta{}, services)
			if tc.wantErr {
				if err == nil {
					t.Fatal("finding cap did not fail closed")
				}
				if strings.Contains(err.Error(), adapterGitHubToken) {
					t.Fatal("finding-cap error exposed detector source material")
				}
				if !reflect.DeepEqual(candidate, before) {
					t.Fatal("finding-cap failure mutated the canonical call")
				}
				return
			}
			if err != nil || decision.Outcome != tc.wantOutcome || decision.FailureKind == "" {
				fatalDecisionSummary(t, decision)
			}
			assertBetterLeaksDecisionSafe(t, decision)
			if !reflect.DeepEqual(candidate, before) {
				t.Fatal("log finding-cap path mutated the canonical call")
			}
		})
	}
}

func TestGuard_BetterLeaksPreservesExistingScanLimitSemantics(t *testing.T) {
	first := "GITHUB_TOKEN=" + adapterGitHubToken
	call := betterLeaksTextCall(first)
	call.Messages = append(call.Messages, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("z", 32))},
	})
	for _, tc := range []struct {
		name        string
		action      string
		wantOutcome sdk.Outcome
	}{
		{name: "block", action: ActionBlock, wantOutcome: sdk.OutcomeBlock},
		{name: "redact", action: ActionRedact, wantOutcome: sdk.OutcomeBlock},
		{name: "log", action: ActionLog, wantOutcome: sdk.OutcomeLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, services := newBetterLeaksActionGuard(t, tc.action, len(first)+1, 0)
			candidate := aipapi.CloneCall(call)
			before := aipapi.CloneCall(candidate)
			decision, err := guard.Evaluate(context.Background(), &candidate, sdk.Meta{}, services)
			if err != nil || decision.Outcome != tc.wantOutcome || !decision.ScanLimitHit {
				fatalDecisionSummary(t, decision)
			}
			if decision.FailureKind != FailureKindScanLimit {
				t.Fatalf("failure kind = %q", decision.FailureKind)
			}
			assertBetterLeaksDecisionSafe(t, decision)
			if !reflect.DeepEqual(candidate, before) {
				t.Fatal("scan-limit path mutated the canonical call")
			}
		})
	}
}

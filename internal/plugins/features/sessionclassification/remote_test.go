package sessionclassification_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// remoteTestInput builds the bounded snapshot a remote input is derived from. It
// deliberately carries every fact the remote contract must not forward: an
// authoritative session ID, a client-controlled hint, a workspace root, the raw
// client identity, and session labels.
func remoteTestInput(clientUserAgent string, markers []string) sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{
			AuthoritativeSessionID: "secure-session-id",
			ClientSessionHint:      "client-controlled-hint",
			Labels:                 map[string]string{"origin": "operator-console"},
		},
		Workspace: workspace.WorkspaceView{
			ProjectRoot: "/workspace/private/repository",
			Markers:     markers,
		},
		Evidence: sdkclassification.Evidence{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: clientUserAgent,
			ToolCategories: sdkclassification.ToolCategoryFileRead |
				sdkclassification.ToolCategoryFileEdit |
				sdkclassification.ToolCategoryOSCommand,
		},
	}
}

func TestBuildRemoteInputReducesEvidenceToBoundedNormalizedFacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		userAgent string
		markers   []string
		want      sessionclassification.RemoteInput
	}{
		{
			name:      "recognized coding family",
			userAgent: "  CoDeX_Cli_Rs/1.2.3  ",
			markers:   []string{"go.mod"},
			want: sessionclassification.RemoteInput{
				Operation:      aipapi.OperationOpenAIResponses,
				ClientFamily:   agentfacts.FamilyCodex,
				ToolCategories: sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryOSCommand,
				WorkspaceClass: sessionclassification.WorkspaceClassProjectMarker,
			},
		},
		{
			name:      "generic underlying sdk identity stays ambiguous",
			userAgent: "Anthropic/JS",
			markers:   []string{"README.md"},
			want: sessionclassification.RemoteInput{
				Operation:          aipapi.OperationOpenAIResponses,
				HasAmbiguousClient: true,
				ToolCategories:     sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryOSCommand,
				WorkspaceClass:     sessionclassification.WorkspaceClassUnmarked,
			},
		},
		{
			name:      "absent identity is not ambiguous",
			userAgent: "",
			markers:   nil,
			want: sessionclassification.RemoteInput{
				Operation:          aipapi.OperationOpenAIResponses,
				HasAmbiguousClient: false,
				ToolCategories:     sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryOSCommand,
				WorkspaceClass:     sessionclassification.WorkspaceClassUnmarked,
			},
		},
		{
			name:      "project marker by suffix and case",
			userAgent: "factory_cli/2.0",
			markers:   []string{"notes.txt", "Widget.CSPROJ"},
			want: sessionclassification.RemoteInput{
				Operation:      aipapi.OperationOpenAIResponses,
				ClientFamily:   agentfacts.FamilyDroid,
				ToolCategories: sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryOSCommand,
				WorkspaceClass: sessionclassification.WorkspaceClassProjectMarker,
			},
		},
		{
			name:      "conflicting identities yield no family",
			userAgent: "codex_cli_rs/1.2.3 hermes-agent/3",
			markers:   []string{"go.mod"},
			want: sessionclassification.RemoteInput{
				Operation:          aipapi.OperationOpenAIResponses,
				HasAmbiguousClient: true,
				ToolCategories:     sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryOSCommand,
				WorkspaceClass:     sessionclassification.WorkspaceClassProjectMarker,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := sessionclassification.BuildRemoteInput(remoteTestInput(tc.userAgent, tc.markers), sessionclassification.LocalDecision{})
			if got != tc.want {
				t.Fatalf("BuildRemoteInput = %+v, want %+v", got, tc.want)
			}
			if err := sessionclassification.ValidateRemoteInput(got); err != nil {
				t.Fatalf("normalized input %+v failed its own contract: %v", got, err)
			}
		})
	}
}

func TestBuildRemoteInputCarriesTheDecisiveLocalEvidenceCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		decision sessionclassification.LocalDecision
		want     session.EvidenceCode
	}{
		{
			name:     "no decisive local evidence",
			decision: sessionclassification.LocalDecision{Promotes: true, Source: session.SourceLocalTooling},
			want:     "",
		},
		{
			name:     "tooling cluster code is forwarded",
			decision: sessionclassification.LocalDecision{EvidenceCode: sessionclassification.EvidenceCodeDistinctCluster},
			want:     sessionclassification.EvidenceCodeDistinctCluster,
		},
		{
			name:     "identity code is forwarded",
			decision: sessionclassification.LocalDecision{EvidenceCode: sessionclassification.EvidenceCodeCodex},
			want:     sessionclassification.EvidenceCodeCodex,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := sessionclassification.BuildRemoteInput(remoteTestInput("Anthropic/JS", nil), tc.decision)
			if got.LocalEvidenceCode != tc.want {
				t.Fatalf("LocalEvidenceCode = %q, want %q", got.LocalEvidenceCode, tc.want)
			}
			if err := sessionclassification.ValidateRemoteInput(got); err != nil {
				t.Fatalf("normalized input %+v failed its own contract: %v", got, err)
			}
		})
	}
}

// TestBuildRemoteInputNormalizesUnrecognisedFacts proves normalization reduces a
// fact outside a closed vocabulary to that vocabulary's absent value instead of
// forwarding it. The tool-bit mask is the one exception: defined bits survive and
// undefined carrier bits are dropped.
func TestBuildRemoteInputNormalizesUnrecognisedFacts(t *testing.T) {
	t.Parallel()

	undefinedBits := sdkclassification.ToolCategorySet(1) << 15
	cases := []struct {
		name       string
		categories sdkclassification.ToolCategorySet
		code       session.EvidenceCode
		wantCode   session.EvidenceCode
	}{
		{
			name:       "undefined tool carrier bits are dropped",
			categories: sdkclassification.ToolCategoryFileRead | undefinedBits,
		},
		{
			name:     "remote evidence code is not a local fact",
			code:     sessionclassification.EvidenceCodeRemoteAboveThreshold,
			wantCode: "",
		},
		{
			name:     "arbitrary evidence code is dropped",
			code:     "client_family.vscode",
			wantCode: "",
		},
		{
			name:     "credential-shaped evidence code is dropped",
			code:     "sk-live-9f3a2b",
			wantCode: "",
		},
		{
			name:     "a reviewed local code is forwarded",
			code:     sessionclassification.EvidenceCodeProjectMarkerCluster,
			wantCode: sessionclassification.EvidenceCodeProjectMarkerCluster,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input := remoteTestInput("Anthropic/JS", nil)
			input.Evidence.ToolCategories = tc.categories
			got := sessionclassification.BuildRemoteInput(input, sessionclassification.LocalDecision{EvidenceCode: tc.code})

			if got.ToolCategories != tc.categories&sdkclassification.DefinedToolCategoryBits {
				t.Fatalf("ToolCategories = %#x, want %#x", got.ToolCategories, tc.categories&sdkclassification.DefinedToolCategoryBits)
			}
			if got.LocalEvidenceCode != tc.wantCode {
				t.Fatalf("LocalEvidenceCode = %q, want %q", got.LocalEvidenceCode, tc.wantCode)
			}
			if err := sessionclassification.ValidateRemoteInput(got); err != nil {
				t.Fatalf("normalized input %+v must stay servable: %v", got, err)
			}
		})
	}
}

// TestBuildRemoteInputForwardsTheOperationUnchanged pins the one value the builder
// deliberately does not normalize: an operation outside the closed vocabulary has
// no safe absent value, so it is forwarded unchanged and ValidateRemoteInput
// refuses the attempt rather than the builder hiding a carrier defect.
func TestBuildRemoteInputForwardsTheOperationUnchanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		operation  aipapi.Operation
		wantServed bool
	}{
		{name: "recognized operation", operation: aipapi.OperationOpenAIResponses, wantServed: true},
		{name: "compaction operation", operation: aipapi.OperationContextCompaction, wantServed: true},
		{name: "unrecognized operation", operation: "openai.responses.v2"},
		{name: "absent operation", operation: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input := remoteTestInput("Anthropic/JS", nil)
			input.Evidence.Operation = tc.operation
			got := sessionclassification.BuildRemoteInput(input, sessionclassification.LocalDecision{})
			if got.Operation != tc.operation {
				t.Fatalf("Operation = %q, want the carrier value %q forwarded unchanged", got.Operation, tc.operation)
			}
			err := sessionclassification.ValidateRemoteInput(got)
			if tc.wantServed {
				if err != nil {
					t.Fatalf("ValidateRemoteInput(%+v) = %v, want accepted", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateRemoteInput(%+v) accepted an operation outside the closed vocabulary", got)
			}
			if !errors.Is(err, sessionclassification.ErrInvalidRemoteInput) {
				t.Fatalf("error %v does not report ErrInvalidRemoteInput", err)
			}
		})
	}
}

// TestBuildRemoteInputNeverCarriesSourceMaterial proves content-freedom at
// runtime: none of the request material present in the snapshot may appear in the
// derived remote input, in any field.
func TestBuildRemoteInputNeverCarriesSourceMaterial(t *testing.T) {
	t.Parallel()

	input := remoteTestInput("codex_cli_rs/1.2.3", []string{"go.mod", "Widget.csproj"})
	input.Evidence.ClientUserAgent = "codex_cli_rs/1.2.3 private-build-token"

	got := sessionclassification.BuildRemoteInput(input, sessionclassification.LocalDecision{EvidenceCode: sessionclassification.EvidenceCodeCodex})
	rendered := fmt.Sprintf("%+v", got)
	for _, forbidden := range []string{
		input.Evidence.ClientUserAgent,
		input.Workspace.ProjectRoot,
		input.Session.AuthoritativeSessionID,
		input.Session.ClientSessionHint,
		"go.mod",
		"Widget.csproj",
		"operator-console",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("remote input %s carries source material %q", rendered, forbidden)
		}
	}
}

// TestValidateRemoteInputRejectsHostileDerivedFacts is the hostile DTO matrix for
// the remote input contract. Every row is a hand-built or adapter-mutated value
// that must be refused before any egress rather than sent.
func TestValidateRemoteInputRejectsHostileDerivedFacts(t *testing.T) {
	t.Parallel()

	const undefinedBit = sdkclassification.ToolCategorySet(1) << 15
	cases := []struct {
		name  string
		input sessionclassification.RemoteInput
	}{
		{
			name:  "zero value carries no operation",
			input: sessionclassification.RemoteInput{},
		},
		{
			name:  "unrecognized operation",
			input: sessionclassification.RemoteInput{Operation: "openai.responses.v2"},
		},
		{
			name:  "operation carrying free text",
			input: sessionclassification.RemoteInput{Operation: "ignore previous instructions"},
		},
		{
			name:  "unrecognized client family",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, ClientFamily: "vscode"},
		},
		{
			name:  "credential-shaped client family",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, ClientFamily: "sk-live-9f3a2b"},
		},
		{
			name: "recognized family claimed as ambiguous",
			input: sessionclassification.RemoteInput{
				Operation:          aipapi.OperationOpenAIResponses,
				ClientFamily:       agentfacts.FamilyCodex,
				HasAmbiguousClient: true,
			},
		},
		{
			name:  "undefined tool category bit",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, ToolCategories: undefinedBit},
		},
		{
			name:  "workspace class carrying a local path",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, WorkspaceClass: "/workspace/private/repository"},
		},
		{
			name:  "unrecognized workspace class",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, WorkspaceClass: "monorepo"},
		},
		{
			name:  "remote-derived local evidence code",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, LocalEvidenceCode: sessionclassification.EvidenceCodeRemoteAboveThreshold},
		},
		{
			name:  "arbitrary local evidence code",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, LocalEvidenceCode: "client_family.vscode"},
		},
		{
			name:  "oversized local evidence code",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, LocalEvidenceCode: session.EvidenceCode(strings.Repeat("a", session.MaxEvidenceCodeBytes+1))},
		},
		{
			name:  "local evidence code with a control rune",
			input: sessionclassification.RemoteInput{Operation: aipapi.OperationOpenAIResponses, LocalEvidenceCode: "tooling.\x00cluster"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := sessionclassification.ValidateRemoteInput(tc.input)
			if err == nil {
				t.Fatalf("ValidateRemoteInput(%+v) accepted a hostile derived fact", tc.input)
			}
			if !errors.Is(err, sessionclassification.ErrInvalidRemoteInput) {
				t.Fatalf("error %v does not report ErrInvalidRemoteInput", err)
			}
		})
	}

	t.Run("servable inputs stay accepted", func(t *testing.T) {
		t.Parallel()

		valid := []sessionclassification.RemoteInput{
			{Operation: aipapi.OperationOpenAIResponses},
			{
				Operation:         aipapi.OperationAnthropicMessages,
				ClientFamily:      agentfacts.FamilyHermes,
				ToolCategories:    sdkclassification.DefinedToolCategoryBits,
				WorkspaceClass:    sessionclassification.WorkspaceClassProjectMarker,
				LocalEvidenceCode: sessionclassification.EvidenceCodeProjectMarkerCluster,
			},
			{
				Operation:          aipapi.OperationContextCompaction,
				HasAmbiguousClient: true,
				ToolCategories:     sdkclassification.ToolCategoryUnknownSeen,
			},
		}
		for _, in := range valid {
			if err := sessionclassification.ValidateRemoteInput(in); err != nil {
				t.Fatalf("ValidateRemoteInput(%+v) = %v, want accepted", in, err)
			}
		}
	})
}

// TestValidateRemoteDecisionRejectsHostileProbabilities is the hostile matrix for
// the bounded remote decision: every non-finite or out-of-range number is a
// bounded remote error, and the zero decision is a valid below-threshold answer
// rather than a promotion.
func TestValidateRemoteDecisionRejectsHostileProbabilities(t *testing.T) {
	t.Parallel()

	hostile := []struct {
		name     string
		decision sessionclassification.RemoteDecision
	}{
		{name: "nan probability", decision: sessionclassification.RemoteDecision{CodingProbability: math.NaN()}},
		{name: "positive infinity probability", decision: sessionclassification.RemoteDecision{CodingProbability: math.Inf(1)}},
		{name: "negative infinity probability", decision: sessionclassification.RemoteDecision{CodingProbability: math.Inf(-1)}},
		{name: "negative probability", decision: sessionclassification.RemoteDecision{CodingProbability: -0.0001}},
		{name: "probability above one", decision: sessionclassification.RemoteDecision{CodingProbability: 1.0000001}},
		{name: "nan confidence", decision: sessionclassification.RemoteDecision{CodingProbability: 0.5, Confidence: math.NaN()}},
		{name: "positive infinity confidence", decision: sessionclassification.RemoteDecision{CodingProbability: 0.5, Confidence: math.Inf(1)}},
		{name: "negative infinity confidence", decision: sessionclassification.RemoteDecision{CodingProbability: 0.5, Confidence: math.Inf(-1)}},
		{name: "negative confidence", decision: sessionclassification.RemoteDecision{CodingProbability: 0.5, Confidence: -1}},
		{name: "confidence above one", decision: sessionclassification.RemoteDecision{CodingProbability: 0.5, Confidence: 2}},
		{name: "largest finite probability", decision: sessionclassification.RemoteDecision{CodingProbability: math.MaxFloat64}},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := sessionclassification.ValidateRemoteDecision(tc.decision)
			if err == nil {
				t.Fatalf("ValidateRemoteDecision(%+v) accepted a hostile number", tc.decision)
			}
			if !errors.Is(err, sessionclassification.ErrInvalidRemoteDecision) {
				t.Fatalf("error %v does not report ErrInvalidRemoteDecision", err)
			}
		})
	}

	t.Run("bounded decisions stay accepted", func(t *testing.T) {
		t.Parallel()

		accepted := []sessionclassification.RemoteDecision{
			{},
			{CodingProbability: 1},
			{CodingProbability: 0.5, Confidence: 0.5},
			{CodingProbability: 1, Confidence: 1},
			{CodingProbability: 0.9, Confidence: 0.95},
		}
		for _, decision := range accepted {
			if err := sessionclassification.ValidateRemoteDecision(decision); err != nil {
				t.Fatalf("ValidateRemoteDecision(%+v) = %v, want accepted", decision, err)
			}
		}
	})
}

// TestRemoteDecisionPositiveAppliesConfiguredThresholdOnly proves the promotion
// predicate is driven solely by the configured threshold: a hostile threshold or a
// non-finite decision can never yield a positive, and a below-threshold answer is
// an unknown session rather than a negative classification.
func TestRemoteDecisionPositiveAppliesConfiguredThresholdOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		decision  sessionclassification.RemoteDecision
		threshold float64
		want      bool
	}{
		{name: "exactly at the threshold", decision: sessionclassification.RemoteDecision{CodingProbability: 0.90, Confidence: 0.9}, threshold: 0.90, want: true},
		{name: "above the threshold", decision: sessionclassification.RemoteDecision{CodingProbability: 0.95, Confidence: 0.5}, threshold: 0.90, want: true},
		{name: "below the threshold", decision: sessionclassification.RemoteDecision{CodingProbability: 0.8999999, Confidence: 1}, threshold: 0.90, want: false},
		{name: "zero decision never promotes", decision: sessionclassification.RemoteDecision{}, threshold: 0.01, want: false},
		{name: "lowest legal threshold", decision: sessionclassification.RemoteDecision{CodingProbability: 0.000001}, threshold: 0.000001, want: true},
		{name: "highest legal threshold", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: 1, want: true},
		{name: "nan probability never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: math.NaN(), Confidence: 1}, threshold: 0.5, want: false},
		{name: "infinite confidence never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1, Confidence: math.Inf(1)}, threshold: 0.5, want: false},
		{name: "zero threshold never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: 0, want: false},
		{name: "negative threshold never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: -1, want: false},
		{name: "nan threshold never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: math.NaN(), want: false},
		{name: "infinite threshold never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: math.Inf(1), want: false},
		{name: "threshold above one never promotes", decision: sessionclassification.RemoteDecision{CodingProbability: 1}, threshold: 1.0001, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.decision.Positive(tc.threshold); got != tc.want {
				t.Fatalf("Positive(threshold=%v) = %t, want %t for %+v", tc.threshold, got, tc.want, tc.decision)
			}
		})
	}
}

func TestNewRemotePolicyRejectsNonRemoteAndUnsafeConfigurations(t *testing.T) {
	t.Parallel()

	unsafeRemoteConfig := func(mutate func(*sessionclassification.RemoteConfig)) sessionclassification.Config {
		remote := validTestRemoteConfig()
		mutate(remote)
		return sessionclassification.Config{Mode: sessionclassification.ModeJev, Remote: remote}
	}
	cases := []struct {
		name         string
		cfg          sessionclassification.Config
		wantContains string
	}{
		{name: "heuristic mode", cfg: sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, wantContains: "never constructs or calls a remote decider"},
		{name: "omitted mode", cfg: sessionclassification.Config{}, wantContains: "never constructs or calls a remote decider"},
		{name: "unknown mode", cfg: sessionclassification.Config{Mode: "automatic"}, wantContains: "never constructs or calls a remote decider"},
		{name: "jev without remote settings", cfg: sessionclassification.Config{Mode: sessionclassification.ModeJev}, wantContains: "requires explicit remote settings"},
		{name: "hybrid without remote settings", cfg: sessionclassification.Config{Mode: sessionclassification.ModeHybrid}, wantContains: "requires explicit remote settings"},
		{name: "wrong provider", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.Provider = "openai" })},
		{name: "nan threshold", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.PositiveThreshold = math.NaN() })},
		{name: "positive infinity threshold", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.PositiveThreshold = math.Inf(1) })},
		{name: "negative threshold", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.PositiveThreshold = -0.5 })},
		{name: "zero threshold", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.PositiveThreshold = 0 })},
		{name: "threshold above one", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.PositiveThreshold = 1.5 })},
		{name: "zero timeout", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.Timeout = 0 })},
		{name: "timeout below the minimum", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.Timeout = time.Nanosecond })},
		{name: "timeout above the maximum", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
			r.Timeout = sessionclassification.MaxRemoteTimeout + time.Millisecond
		})},
		{name: "zero attempts", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.MaxAttemptsPerSession = 0 })},
		{name: "negative attempts", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.MaxAttemptsPerSession = -1 })},
		{name: "attempts above the maximum", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
			r.MaxAttemptsPerSession = sessionclassification.MaxRemoteAttemptsPerSession + 1
		})},
		{name: "lease ttl not beyond the timeout margin", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
			r.LeaseTTL = r.Timeout + sessionclassification.RemoteLeaseSafetyMargin
		})},
		{name: "lease ttl below the timeout", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.LeaseTTL = time.Millisecond })},
		{name: "lease ttl above the maximum", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
			r.LeaseTTL = sessionclassification.MaxRemoteLeaseTTL + time.Millisecond
		})},
		{name: "negative retry backoff", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.RetryBackoff = -time.Second })},
		{name: "retry backoff above the maximum", cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
			r.RetryBackoff = sessionclassification.MaxRemoteRetryBackoff + time.Millisecond
		})},
		{
			name: "embedded credential instead of a reference",
			cfg:  unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.APIKeyEnv = "sk-live-9f3a2b-private-value" }),
		},
		{
			name: "inline assignment instead of a reference",
			cfg:  unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.APIKeyEnv = "TYPESAFE_API_KEY=private-value" }),
		},
		{
			name: "bearer header instead of a reference",
			cfg:  unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.APIKeyEnv = "Bearer private-value" }),
		},
		{
			name: "filesystem path instead of a reference",
			cfg:  unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.APIKeyEnv = "/etc/aipstd/credentials" }),
		},
		{
			name: "blank reference",
			cfg:  unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) { r.APIKeyEnv = "   " }),
		},
		{
			name: "oversized reference",
			cfg: unsafeRemoteConfig(func(r *sessionclassification.RemoteConfig) {
				// One byte over the accepted environment-reference length.
				r.APIKeyEnv = strings.Repeat("A", 129)
			}),
		},
		{
			name: "unusable heuristic exclusions alongside valid remote settings",
			cfg: sessionclassification.Config{
				Mode:      sessionclassification.ModeHybrid,
				Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: make([]string, sessionclassification.MaxIgnoredUserAgentPrefixes+1)},
				Remote:    validTestRemoteConfig(),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := sessionclassification.NewRemotePolicy(tc.cfg)
			if err == nil {
				t.Fatalf("NewRemotePolicy(%+v) = %+v, want rejection", tc.cfg, policy)
			}
			if !errors.Is(err, sessionclassification.ErrRemoteNotConfigured) {
				t.Fatalf("error %v does not report ErrRemoteNotConfigured", err)
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("error %v does not report %q", err, tc.wantContains)
			}
			for _, secret := range []string{"sk-live-9f3a2b", "private-value", "/etc/aipstd/credentials"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q exposed a credential-shaped value", err)
				}
			}
		})
	}
}

func TestNewRemotePolicyExposesOnlyAValidatedRemotePosture(t *testing.T) {
	t.Parallel()

	t.Run("jev mode", func(t *testing.T) {
		t.Parallel()

		policy, err := sessionclassification.NewRemotePolicy(sessionclassification.Config{
			Mode:   sessionclassification.ModeJev,
			Remote: validTestRemoteConfig(),
		})
		if err != nil {
			t.Fatalf("NewRemotePolicy: %v", err)
		}
		if policy.Mode() != sessionclassification.ModeJev {
			t.Fatalf("Mode = %q, want jev", policy.Mode())
		}
		if policy.Threshold() != 0.90 {
			t.Fatalf("Threshold = %v, want the configured 0.90", policy.Threshold())
		}
		if policy.CredentialReference() != "TYPESAFE_API_KEY" {
			t.Fatalf("CredentialReference = %q, want the referenced environment name", policy.CredentialReference())
		}
		if got := policy.Config(); !reflect.DeepEqual(got, *validTestRemoteConfig()) {
			t.Fatalf("Config = %+v, want the validated remote settings", got)
		}
	})

	t.Run("hybrid mode", func(t *testing.T) {
		t.Parallel()

		policy, err := sessionclassification.NewRemotePolicy(sessionclassification.Config{
			Mode:   sessionclassification.ModeHybrid,
			Remote: validTestRemoteConfig(),
		})
		if err != nil {
			t.Fatalf("NewRemotePolicy: %v", err)
		}
		if policy.Mode() != sessionclassification.ModeHybrid {
			t.Fatalf("Mode = %q, want hybrid", policy.Mode())
		}
	})

	t.Run("the validated posture is immune to later configuration edits", func(t *testing.T) {
		t.Parallel()

		cfg := sessionclassification.Config{Mode: sessionclassification.ModeJev, Remote: validTestRemoteConfig()}
		policy, err := sessionclassification.NewRemotePolicy(cfg)
		if err != nil {
			t.Fatalf("NewRemotePolicy: %v", err)
		}
		cfg.Remote.PositiveThreshold = 0.01
		cfg.Remote.Timeout = time.Millisecond
		cfg.Remote.APIKeyEnv = "OTHER_KEY"

		if policy.Threshold() != 0.90 {
			t.Fatalf("Threshold = %v, want the validated 0.90", policy.Threshold())
		}
		if policy.Config().Timeout != 750*time.Millisecond {
			t.Fatalf("Timeout = %v, want the validated 750ms", policy.Config().Timeout)
		}
		if policy.CredentialReference() != "TYPESAFE_API_KEY" {
			t.Fatalf("CredentialReference = %q, want the validated reference", policy.CredentialReference())
		}
	})

	t.Run("the zero posture is inert", func(t *testing.T) {
		t.Parallel()

		var policy sessionclassification.RemotePolicy
		if policy.Mode() != "" || policy.Threshold() != 0 || policy.CredentialReference() != "" {
			t.Fatalf("zero policy = %+v, want no mode, threshold, or reference", policy)
		}
		if policy.Config() != (sessionclassification.RemoteConfig{}) {
			t.Fatalf("zero policy config = %+v, want the zero remote settings", policy.Config())
		}
		certain := sessionclassification.RemoteDecision{CodingProbability: 1, Confidence: 1}
		if certain.Positive(policy.Threshold()) {
			t.Fatal("zero policy authorised a remote promotion")
		}
	})
}

// remoteContractExpectedShapes pins the shipped remote contract. Both values must
// stay a flat set of closed enumerations and fixed-width scalars: adding,
// removing, or retyping a field is a deliberate review decision, because every
// field is data an adapter may send to a remote service.
var remoteContractExpectedShapes = map[string][]struct {
	name   string
	typeOf reflect.Type
}{
	"RemoteInput": {
		{name: "Operation", typeOf: reflect.TypeFor[aipapi.Operation]()},
		{name: "ClientFamily", typeOf: reflect.TypeFor[agentfacts.Family]()},
		{name: "HasAmbiguousClient", typeOf: reflect.TypeFor[bool]()},
		{name: "ToolCategories", typeOf: reflect.TypeFor[sdkclassification.ToolCategorySet]()},
		{name: "WorkspaceClass", typeOf: reflect.TypeFor[sessionclassification.WorkspaceClass]()},
		{name: "LocalEvidenceCode", typeOf: reflect.TypeFor[session.EvidenceCode]()},
	},
	"RemoteDecision": {
		{name: "CodingProbability", typeOf: reflect.TypeFor[float64]()},
		{name: "Confidence", typeOf: reflect.TypeFor[float64]()},
	},
}

// remoteContentBearingNameFragments name request content, unbounded request
// material, or a credential. No remote contract field may hold data under one of
// them (requirements 7.1, 7.2, 7.5).
var remoteContentBearingNameFragments = []string{
	"prompt", "message", "transcript", "content", "header", "argument", "toolarg",
	"body", "payload", "path", "projectroot", "filename", "history", "instruction",
	"secret", "credential", "token", "apikey", "authorization", "bearer",
}

// remoteVendorNameFragments name a vendor wire detail. A guessed endpoint, schema,
// or DTO is frozen into this contract only by naming it (research.md 258,
// design.md 40).
var remoteVendorNameFragments = []string{
	"jev", "typesafe", "vendor", "endpoint", "baseurl", "url", "wire", "schema",
	"dto", "requestbody", "responsebody", "request", "response",
}

// TestRemoteContractValuesCarryNoContentOrVendorShape is the structural half of
// the content-freedom guarantee: the reflection guard below must find nothing in
// the shipped contract.
func TestRemoteContractValuesCarryNoContentOrVendorShape(t *testing.T) {
	t.Parallel()

	shapes := map[string]reflect.Type{
		"RemoteInput":    reflect.TypeFor[sessionclassification.RemoteInput](),
		"RemoteDecision": reflect.TypeFor[sessionclassification.RemoteDecision](),
		"WorkspaceClass": reflect.TypeFor[sessionclassification.WorkspaceClass](),
	}
	for name, typeOf := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if violations := remoteContractViolations(typeOf, name, map[reflect.Type]bool{}); len(violations) != 0 {
				t.Fatalf("%s can carry unbounded, dynamic, content-bearing, or vendor-named data: %s",
					name, strings.Join(violations, "; "))
			}
		})
	}
}

func TestRemoteContractShapesArePinned(t *testing.T) {
	t.Parallel()

	shapes := map[string]reflect.Type{
		"RemoteInput":    reflect.TypeFor[sessionclassification.RemoteInput](),
		"RemoteDecision": reflect.TypeFor[sessionclassification.RemoteDecision](),
	}
	for name, wantFields := range remoteContractExpectedShapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typeOf := shapes[name]
			if typeOf.Kind() != reflect.Struct {
				t.Fatalf("%s is not a struct", name)
			}
			if typeOf.NumField() != len(wantFields) {
				t.Fatalf("%s has %d fields, want exactly %d", name, typeOf.NumField(), len(wantFields))
			}
			for i, want := range wantFields {
				field := typeOf.Field(i)
				if field.Name != want.name || field.Type != want.typeOf {
					t.Fatalf("%s field %d = %s %s, want %s %s", name, i, field.Name, field.Type, want.name, want.typeOf)
				}
			}
		})
	}
}

// TestRemoteContractGuardRejectsSmugglingShapes is the load-bearing self-test for
// the guard above. Every fixture is an otherwise legitimate remote input plus one
// smuggling shape, so each case proves a specific rule fires instead of proving
// that a broadly failing predicate rejects everything.
func TestRemoteContractGuardRejectsSmugglingShapes(t *testing.T) {
	t.Parallel()

	type funcFixture func()
	type vendorDTO struct {
		Endpoint string
		Headers  map[string]string
	}
	type deepSliceFixture struct {
		Inner struct{ Values []string }
	}
	type deepPointerFixture struct {
		Inner *vendorDTO
	}

	tests := []struct {
		name  string
		field reflect.StructField
	}{
		{name: "transcript slice", field: remoteField("Transcript", reflect.TypeFor[[]string]())},
		{name: "nested message slice", field: remoteField("Extra", reflect.TypeFor[deepSliceFixture]())},
		{name: "prompt string", field: remoteField("Prompt", reflect.TypeFor[string]())},
		{name: "raw header map", field: remoteField("RawHeaders", reflect.TypeFor[map[string][]string]())},
		{name: "tool name slice", field: remoteField("ToolNames", reflect.TypeFor[[]string]())},
		{name: "workspace path", field: remoteField("WorkspacePath", reflect.TypeFor[string]())},
		{name: "credential field", field: remoteField("APIToken", reflect.TypeFor[string]())},
		{name: "vendor struct", field: remoteField("Vendor", reflect.TypeFor[vendorDTO]())},
		{name: "vendor named field", field: remoteField("JevRequest", reflect.TypeFor[string]())},
		{name: "vendor dto name", field: remoteField("DecisionDTO", reflect.TypeFor[string]())},
		{name: "vendor endpoint name", field: remoteField("EndpointURL", reflect.TypeFor[string]())},
		{name: "vendor response name", field: remoteField("RemoteResponse", reflect.TypeFor[string]())},
		{name: "pointer field", field: remoteField("Deferred", reflect.TypeFor[*string]())},
		{name: "nested pointer field", field: remoteField("Extra", reflect.TypeFor[deepPointerFixture]())},
		{name: "interface field", field: remoteField("Carrier", reflect.TypeFor[any]())},
		{name: "func field", field: remoteField("Compile", reflect.TypeFor[funcFixture]())},
		{name: "fixed array of strings", field: remoteField("History", reflect.TypeFor[[8]string]())},
		{name: "raw uintptr handle", field: remoteField("Handle", reflect.TypeFor[uintptr]())},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := remoteContractFixtureWith(tc.field)
			violations := remoteContractViolations(fixture, "RemoteInput", map[reflect.Type]bool{})
			if len(violations) == 0 {
				t.Fatalf("the guard accepted a contract field smuggling %s %s", tc.field.Name, tc.field.Type)
			}
			if !slices.ContainsFunc(violations, func(v string) bool {
				return strings.Contains(v, "."+tc.field.Name)
			}) {
				t.Fatalf("violations must name the offending field %s; got: %s", tc.field.Name, strings.Join(violations, "; "))
			}
		})
	}

	t.Run("bounded scalar additions stay accepted", func(t *testing.T) {
		t.Parallel()

		fixture := remoteContractFixtureWith(
			remoteField("AttemptHint", reflect.TypeFor[uint8]()),
			remoteField("Label", reflect.TypeFor[string]()),
			remoteField("Eligible", reflect.TypeFor[bool]()),
			remoteField("Bits", reflect.TypeFor[sdkclassification.ToolCategorySet]()),
		)
		if violations := remoteContractViolations(fixture, "RemoteInput", map[reflect.Type]bool{}); len(violations) != 0 {
			t.Fatalf("a contract of fixed-shape scalars must stay acceptable: %s", strings.Join(violations, "; "))
		}
	})
}

// remoteContractFixtureWith builds a complete, otherwise-legitimate remote input
// type plus one extra field, so each hostile fixture isolates exactly one rule.
func remoteContractFixtureWith(extra ...reflect.StructField) reflect.Type {
	fields := make([]reflect.StructField, 0, len(extra)+2)
	for _, want := range remoteContractExpectedShapes["RemoteInput"] {
		fields = append(fields, remoteField(want.name, want.typeOf))
	}
	return reflect.StructOf(append(fields, extra...))
}

func remoteField(name string, typ reflect.Type) reflect.StructField {
	return reflect.StructField{Name: name, Type: typ}
}

// remoteContractViolations reports every way a remote contract value could carry
// unbounded, dynamic, content-bearing, or vendor-named data, at any depth.
//
// Rules, in order:
//
//  1. every field holds a bounded scalar (bool, string, or a fixed-width numeric):
//     a slice, array, map, pointer, interface, func, chan, or raw handle is a
//     violation, because a bounded remote payload has no container;
//  2. no field name carries request content, a credential, or a vendor wire detail
//     under a name matching one of the reviewed fragments.
//
// Residual limitation, stated honestly: this guard rejects storage shapes and
// names. A fixed-width string added under a benign name cannot be detected by any
// reflection rule, so reviewing a newly added field remains a human responsibility.
// The runtime guards are the closed-vocabulary checks in ValidateRemoteInput and
// ValidateRemoteDecision.
func remoteContractViolations(typ reflect.Type, path string, visited map[reflect.Type]bool) []string {
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map, reflect.Interface,
		reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
		// A reference or container edge is rejected on sight rather than
		// dereferenced: dereferencing first would erase the very edge being
		// judged. uintptr is included because unsafe.Pointer(uintptr) converts
		// back into a live pointer.
		return []string{fmt.Sprintf("%s has an unbounded or reference shape (%s)", path, typ.Kind())}
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return nil
	}
	visited[typ] = true
	defer delete(visited, typ)

	var violations []string
	for field := range typ.Fields() {
		fieldPath := path + "." + field.Name
		lowered := strings.ToLower(field.Name)
		if slices.ContainsFunc(remoteContentBearingNameFragments, func(fragment string) bool {
			return strings.Contains(lowered, fragment)
		}) {
			violations = append(violations, fieldPath+" has a content-bearing or credential-bearing name")
		}
		if slices.ContainsFunc(remoteVendorNameFragments, func(fragment string) bool {
			return strings.Contains(lowered, fragment)
		}) {
			violations = append(violations, fieldPath+" has a vendor wire-detail name")
		}
		violations = append(violations, remoteContractViolations(field.Type, fieldPath, visited)...)
	}
	return violations
}

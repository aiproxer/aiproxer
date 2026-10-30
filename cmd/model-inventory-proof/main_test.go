package main

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestCredentialedBackendCandidates_selectsOnlyConfiguredRemoteBackends(t *testing.T) {
	t.Parallel()

	candidates, skipped := credentialedBackendCandidates(
		standardplugins.UpstreamAPIKeys{
			OpenAI: []string{"openai-key"},
		},
		awsEnvironment{Region: "us-east-1", AccessKeyID: true, SecretAccessKey: true},
	)

	want := []string{"openai-responses", "openai-legacy", "bedrock"}
	if len(candidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %+v", len(candidates), len(want), candidates)
	}
	for i, id := range want {
		if candidates[i].ID != id {
			t.Fatalf("candidate[%d].ID = %q, want %q", i, candidates[i].ID, id)
		}
	}

	skipReasons := map[string]string{}
	for _, row := range skipped {
		skipReasons[row.ID] = row.Reason
	}
	if got := skipReasons["anthropic"]; got != "ANTHROPIC_API_KEY is not set" {
		t.Fatalf("anthropic skip reason = %q", got)
	}
	if got := skipReasons["gemini"]; got != "GEMINI_API_KEY is not set" {
		t.Fatalf("gemini skip reason = %q", got)
	}
}

func TestCredentialedBackendCandidates_skipsBedrockWithoutCompleteAWSCredentialSignal(t *testing.T) {
	t.Parallel()

	candidates, skipped := credentialedBackendCandidates(
		standardplugins.UpstreamAPIKeys{},
		awsEnvironment{Region: "us-east-1", AccessKeyID: true},
	)

	for _, candidate := range candidates {
		if candidate.ID == "bedrock" {
			t.Fatal("bedrock should not be selected without a complete AWS credential signal")
		}
	}
	skipReasons := map[string]string{}
	for _, row := range skipped {
		skipReasons[row.ID] = row.Reason
	}
	if got := skipReasons["bedrock"]; got != "AWS credential signal is incomplete" {
		t.Fatalf("bedrock skip reason = %q", got)
	}
}

func TestBackendReportFromSnapshot_sortsModelsAndDoesNotExposeSecrets(t *testing.T) {
	t.Parallel()

	loadedAt := time.Date(2026, 6, 18, 9, 30, 0, 0, time.UTC)
	report := backendReportFromSnapshot("openai-responses", modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: loadedAt,
		Models: []modelinventory.Model{
			{CanonicalID: "z/vendor-model", NativeID: "vendor-model-z", DisplayName: "Z"},
			{CanonicalID: "a/vendor-model", NativeID: "vendor-model-a", DisplayName: "A"},
		},
	})

	if report.Status != "ok" {
		t.Fatalf("Status = %q", report.Status)
	}
	if report.ModelCount != 2 {
		t.Fatalf("ModelCount = %d", report.ModelCount)
	}
	if report.LoadedAt != loadedAt.Format(time.RFC3339) {
		t.Fatalf("LoadedAt = %q", report.LoadedAt)
	}
	if report.Models[0].CanonicalID != "a/vendor-model" || report.Models[1].CanonicalID != "z/vendor-model" {
		t.Fatalf("models not sorted: %+v", report.Models)
	}
}

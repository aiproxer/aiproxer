package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type smokeRecord struct {
	OS                 string   `json:"os"`
	Arch               string   `json:"arch"`
	NodeVersion        string   `json:"node_version"`
	CLIVersion         string   `json:"cli_version"`
	SDKVersion         string   `json:"sdk_version"`
	ModelCount         int      `json:"model_count"`
	ModelIDs           []string `json:"model_ids"`
	TextTurnDurationMS int64    `json:"text_turn_duration_ms"`
	EventCount         int      `json:"event_count"`
	FinishReason       string   `json:"finish_reason"`
	UsagePresent       bool     `json:"usage_present"`
	ErrorClass         string   `json:"error_class,omitempty"`
}

// TestPrivateSmoke_OwnPlan is the opt-in private real-account smoke. It uses the
// operator's own native login and a single non-sensitive text prompt; it records
// only versions, discovered model IDs, timings, event counts, finish reason and
// a typed error class. Prompts, arguments, results, headers and credentials are
// never written. Ordinary checkouts and CI skip it.
func TestPrivateSmoke_OwnPlan(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_SMOKE")) != "1" {
		t.Skip("AIP_CLAUDE_MODEL_SMOKE=1 is required for the private own-plan smoke")
	}
	runtimeRoot := strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_SMOKE_RUNTIME"))
	cliPath := strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_SMOKE_CLI"))
	if runtimeRoot == "" || cliPath == "" {
		t.Fatal("AIP_CLAUDE_MODEL_SMOKE_RUNTIME and AIP_CLAUDE_MODEL_SMOKE_CLI are required")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatalf("the private smoke profile is Linux/amd64; host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !filepath.IsAbs(runtimeRoot) || !filepath.IsAbs(cliPath) {
		t.Fatal("private smoke paths must be absolute")
	}
	sidecar, err := resolveSidecarBeside(filepath.Join(runtimeRoot, "bin", "aip-backend-claudeagentmodel"))
	if err != nil {
		t.Fatalf("installed bundle resolver: %v", err)
	}
	identity, err := probeRuntime(context.Background(), Config{Executable: cliPath}, sidecar)
	if err != nil {
		t.Fatalf("installed runtime probe: %v", err)
	}
	location := bridge.Location{Node: sidecar.NodePath, Script: sidecar.Script, PinnedSDKVersion: sidecar.SDKVersion}
	record := smokeRecord{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		NodeVersion: identity.NodeVersion,
		CLIVersion:  identity.CLIVersion,
		SDKVersion:  identity.SDKVersion,
	}
	defer writeSmokeRecord(t, &record)

	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		home = t.TempDir()
	}
	env, err := buildEnv([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "host_login")
	if err != nil {
		t.Fatal(err)
	}
	workspace := strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_SMOKE_WORKSPACE"))
	if workspace == "" {
		workspace = t.TempDir()
	}
	if !filepath.IsAbs(workspace) {
		t.Fatal("AIP_CLAUDE_MODEL_SMOKE_WORKSPACE must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	found, err := bridge.Discover(ctx, location, bridge.Runtime{CLI: cliPath, Workspace: workspace, Env: env})
	if err != nil {
		record.ErrorClass = smokeErrorClass(err)
		t.Fatalf("private smoke discovery failed: %v", err)
	}
	record.ModelCount = len(found.Models)
	for _, model := range found.Models {
		record.ModelIDs = append(record.ModelIDs, model.Value)
	}
	if len(found.Models) == 0 {
		record.ErrorClass = "discovery_empty"
		t.Fatal("private smoke discovered no models")
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Reply with the single word: ok.")}}}}
	prefix, err := bridge.VisiblePrefixDigest(call)
	if err != nil {
		t.Fatal(err)
	}
	manager := bridge.NewManager(nil, time.Minute, time.Minute, 30*time.Second, time.Now)
	defer func() { _ = manager.Close() }()
	started := time.Now()
	turn, err := manager.Start(ctx, location, bridge.Runtime{CLI: cliPath, Workspace: workspace, Env: env}, bridge.TurnRequest{
		RequestID:  "private-smoke-text",
		Model:      found.Models[0].Value,
		Prompt:     "Reply with the single word: ok.",
		Prefix:     prefix,
		PendingTTL: time.Minute,
	})
	if err != nil {
		record.ErrorClass = smokeErrorClass(err)
		t.Fatalf("private smoke turn did not start: %v", err)
	}
	defer func() { _ = turn.Close() }()
	for {
		event, recvErr := turn.Recv(ctx)
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			record.TextTurnDurationMS = time.Since(started).Milliseconds()
			record.ErrorClass = smokeErrorClass(recvErr)
			t.Fatalf("private smoke turn failed: %v", recvErr)
		}
		record.EventCount++
		if event.Kind == aipapi.EventUsageDelta {
			record.UsagePresent = true
		}
		if event.Kind == aipapi.EventResponseFinished {
			record.FinishReason = event.FinishReason
		}
	}
	record.TextTurnDurationMS = time.Since(started).Milliseconds()
	if record.EventCount == 0 {
		record.ErrorClass = "empty_response"
		t.Fatal("private smoke turn produced no events")
	}
}

func smokeErrorClass(err error) string {
	var sidecar *bridge.SidecarError
	if errors.As(err, &sidecar) && sidecar.Code != "" {
		return sidecar.Code
	}
	var continuation *bridge.ContinuationError
	if errors.As(err, &continuation) && continuation.Code != "" {
		return continuation.Code
	}
	return "error"
}

func writeSmokeRecord(t *testing.T, record *smokeRecord) {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if out := strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_SMOKE_OUT")); out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("AIP_CLAUDE_MODEL_SMOKE_OUT must be absolute")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(body, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("private smoke record: %s", body)
}

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func repoRootForControlPlaneExamples(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestControlPlaneConfigExamples_ValidateAndExposePosture(t *testing.T) {
	t.Parallel()
	root := repoRootForControlPlaneExamples(t)
	dir := filepath.Join(root, "config", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		enabled        bool
		queryExposed   bool
		retentionOn    bool
		requirePreWork bool
	}{
		"control-plane-memory.yaml":       {enabled: true, queryExposed: false, retentionOn: false, requirePreWork: false},
		"control-plane-sqlite-query.yaml": {enabled: true, queryExposed: true, retentionOn: true, requirePreWork: false},
		"control-plane-retention.yaml":    {enabled: true, queryExposed: false, retentionOn: true, requirePreWork: true},
	}
	for _, e := range entries {
		name := e.Name()
		expected, ok := want[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, name)
			cfg, err := config.LoadFile(path)
			if err != nil {
				t.Fatalf("LoadFile: %v", err)
			}
			if cfg.ControlPlane.Enabled != expected.enabled {
				t.Fatalf("enabled: got %v, want %v", cfg.ControlPlane.Enabled, expected.enabled)
			}
			if got := config.ControlPlaneQueryEffectivelyExposed(cfg); got != expected.queryExposed {
				t.Fatalf("query exposed: got %v, want %v", got, expected.queryExposed)
			}
			if cfg.ControlPlane.Retention.Enabled != expected.retentionOn {
				t.Fatalf("retention: got %v, want %v", cfg.ControlPlane.Retention.Enabled, expected.retentionOn)
			}
			if expected.requirePreWork && !strings.EqualFold(cfg.ControlPlane.RecordingPolicy, "required_pre_work") {
				t.Fatalf("expected required_pre_work policy, got %q", cfg.ControlPlane.RecordingPolicy)
			}
			if expected.queryExposed {
				if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
					t.Fatalf("protected posture for query example: %v", err)
				}
			}
		})
	}
	if len(want) == 0 {
		t.Fatal("expected at least one control-plane example to assert")
	}
}

func TestControlPlaneConfigExamples_NoEnterpriseFeatureSurface(t *testing.T) {
	t.Parallel()
	root := repoRootForControlPlaneExamples(t)
	dir := filepath.Join(root, "config", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"billing", "invoice", "quota", "marketplace", "oauth", "saml", "scim", "user_directory", "rate_limit"}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "control-plane-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ToLower(string(data))
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Fatalf("%s introduces excluded enterprise feature %q", e.Name(), bad)
			}
		}
	}
}

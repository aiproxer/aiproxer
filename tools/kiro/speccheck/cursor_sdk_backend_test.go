package speccheck_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiroSpec(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	filter := strings.TrimSpace(os.Getenv("KIRO_SPEC"))
	specs := registeredSpecs()
	if filter != "" {
		for _, spec := range specs {
			if spec.name == filter {
				spec.check(t, root)
				return
			}
		}
		t.Fatalf("unknown KIRO_SPEC=%q", filter)
	}
	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			spec.check(t, root)
		})
	}
}

type kiroSpec struct {
	name  string
	check func(t *testing.T, root string)
}

type specMetadata struct {
	Phase                  string `json:"phase"`
	ReadyForImplementation bool   `json:"ready_for_implementation"`
	Completed              bool   `json:"completed"`
	Approvals              struct {
		Requirements approval `json:"requirements"`
		Design       approval `json:"design"`
		Tasks        approval `json:"tasks"`
	} `json:"approvals"`
}

type approval struct {
	Generated bool `json:"generated"`
	Approved  bool `json:"approved"`
}

// registeredSpecs lists active spec development gates. Completed specs are
// archived under .kiro/specs/archive/ and removed here; active specs must stay
// registered so `make kiro-spec-check SPEC=...` cannot silently validate zero
// specifications.
func registeredSpecs() []kiroSpec {
	return []kiroSpec{
		{name: "prompt-cache-residency-contract", check: checkResidencyContract},
		{name: "prompt-cache-keepwarm-orchestration", check: checkKeepwarmOrchestration},
		{name: "pre-oss-core-slimming", check: checkPreOssCoreSlimming},
	}
}

func checkResidencyContract(t *testing.T, root string) {
	t.Helper()
	checkMetadata(t, root, "prompt-cache-residency-contract")
	checkTasks(t, root, "prompt-cache-residency-contract", nil)
	checkRequiredFiles(t, root, "prompt-cache-residency-contract", []string{
		"pkg/aipsdk/promptcache/types.go",
		"pkg/aipsdk/backendplugin/promptcache.go",
		"internal/testkit/contract/backend/promptcache.go",
		"internal/testkit/contract/backend/promptcache_test.go",
	})
}

func checkKeepwarmOrchestration(t *testing.T, root string) {
	t.Helper()
	checkMetadata(t, root, "prompt-cache-keepwarm-orchestration")
	checkTasks(t, root, "prompt-cache-keepwarm-orchestration", nil)
	checkRequiredFiles(t, root, "prompt-cache-keepwarm-orchestration", []string{
		"internal/plugins/features/keepwarm/manager.go",
		"internal/plugins/features/keepwarm/scheduler.go",
		"internal/plugins/features/keepwarm/lifecycle.go",
		"internal/core/runtime/prompt_cache_maintenance.go",
		"internal/plugins/backends/anthropic/promptcache_live_test.go",
	})
	liveTest := readSpecFile(t, root, "internal/plugins/backends/anthropic/promptcache_live_test.go")
	for _, marker := range []string{"AIP_ANTHROPIC_CACHE_LIVE", "ANTHROPIC_API_KEY", "t.Skip"} {
		if !strings.Contains(liveTest, marker) {
			t.Errorf("live Anthropic gate is missing explicit marker %q", marker)
		}
	}
}

func checkMetadata(t *testing.T, root, name string) {
	t.Helper()
	var metadata specMetadata
	path := filepath.Join(root, ".kiro", "specs", "archive", name, "spec.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if metadata.Phase != "completed" {
		t.Errorf("phase = %q, want completed", metadata.Phase)
	}
	if metadata.ReadyForImplementation {
		t.Error("ready_for_implementation is true for completed spec")
	}
	if !metadata.Completed {
		t.Error("completed is false")
	}
	for label, approval := range map[string]approval{
		"requirements": metadata.Approvals.Requirements,
		"design":       metadata.Approvals.Design,
		"tasks":        metadata.Approvals.Tasks,
	} {
		if !approval.Generated || !approval.Approved {
			t.Errorf("%s approval is not generated and approved", label)
		}
	}
}

func checkTasks(t *testing.T, root, name string, allowedUnchecked []string) {
	t.Helper()
	content := readSpecFile(t, root, filepath.Join(".kiro", "specs", "archive", name, "tasks.md"))
	allowed := make(map[string]struct{}, len(allowedUnchecked))
	for _, line := range allowedUnchecked {
		allowed[line] = struct{}{}
	}
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [ ]") {
			continue
		}
		if _, ok := allowed[line]; !ok {
			t.Errorf("unchecked task: %s", line)
		}
	}
}

func checkRequiredFiles(t *testing.T, root, name string, files []string) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, file)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s requires implementation file %s: %v", name, file, err)
		}
	}
}

func readSpecFile(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "aipstd")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

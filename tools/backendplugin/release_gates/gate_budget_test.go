package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	releaseWorkflow      = ".github/workflows/release.yml"
	releaseGatesWorkflow = ".github/workflows/backend-plugin-release-gates.yml"
)

// parseWorkflow decodes a workflow document into its root mapping node.
// Workflows are read as yaml.Node rather than decoded into typed structs:
// GitHub Actions accepts `${{ ... }}` expressions anywhere a scalar is legal,
// so a single expression-valued field (for example a per-matrix-job
// timeout-minutes) must not abort the decode of the whole document and hide
// every other job from the checks below.
func parseWorkflow(t *testing.T, rel string) *yaml.Node {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s: workflow has no root mapping", rel)
	}
	return doc.Content[0]
}

// workflowNode walks a mapping path, returning nil as soon as a key is absent.
func workflowNode(t *testing.T, root *yaml.Node, path ...string) *yaml.Node {
	t.Helper()
	current := root
	for i, key := range path {
		if current == nil {
			return nil
		}
		if current.Kind != yaml.MappingNode {
			t.Fatalf("workflow path %v: %q is not a mapping", path[:i], path[i-1])
		}
		found := false
		for n := 0; n+1 < len(current.Content); n += 2 {
			if current.Content[n].Value == key {
				current = current.Content[n+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return current
}

// workflowJobTimeout is one job's declared `timeout-minutes`. GitHub Actions
// accepts a literal minute count or a workflow expression; exactly one of
// Minutes and Expr is populated when Declared is true.
type workflowJobTimeout struct {
	Declared bool
	Minutes  int
	Expr     string
}

func (w workflowJobTimeout) String() string {
	if !w.Declared {
		return "unset"
	}
	if w.Expr != "" {
		return w.Expr
	}
	return (time.Duration(w.Minutes) * time.Minute).String()
}

func workflowJobTimeoutMinutes(t *testing.T, rel string, job string) workflowJobTimeout {
	t.Helper()
	node := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "timeout-minutes")
	if node == nil {
		return workflowJobTimeout{}
	}
	if node.Kind != yaml.ScalarNode {
		t.Fatalf("%s: job %q timeout-minutes is not a scalar", rel, job)
	}
	if node.Tag != "!!int" {
		return workflowJobTimeout{Declared: true, Expr: node.Value}
	}
	var minutes int
	if err := node.Decode(&minutes); err != nil {
		t.Fatalf("%s: job %q timeout-minutes: %v", rel, job, err)
	}
	return workflowJobTimeout{Declared: true, Minutes: minutes}
}

// workflowMatrixLanes reads a job's literal `strategy.matrix.lane` list.
func workflowMatrixLanes(t *testing.T, rel string, job string) []string {
	t.Helper()
	node := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "strategy", "matrix", "lane")
	if node == nil || node.Kind != yaml.SequenceNode {
		t.Fatalf("%s: job %q declares no strategy.matrix.lane sequence", rel, job)
		return nil
	}
	lanes := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		lanes = append(lanes, item.Value)
	}
	return lanes
}

func TestMakeGateBudgets_RaceScanIsWorkflowOwned(t *testing.T) {
	t.Parallel()
	cat := catalogByName()
	race, ok := cat["race_scan"]
	if !ok {
		t.Fatal("race_scan gate missing from the catalog")
	}
	if race.Kind != "external" {
		t.Fatalf("race_scan kind = %q, want external workflow evidence", race.Kind)
	}
	if race.Timeout != 0 {
		t.Fatalf("race_scan declares an unused timeout %s", race.Timeout)
	}
	if strings.TrimSpace(race.Notes) == "" {
		t.Fatal("race_scan must explain which workflow owns its evidence")
	}

	for _, g := range rootGateCatalog() {
		if g.Kind != "make" {
			continue
		}
		if got := g.makeTimeout(); got != defaultMakeGateTimeout {
			t.Fatalf("gate %q budget = %s, want the default %s", g.Name, got, defaultMakeGateTimeout)
		}
	}
}

func TestRaceScanWorkflows_RequireExhaustiveMatrixBeforeAggregate(t *testing.T) {
	t.Parallel()

	// scripts/race-check.sh isolates the billing and billing-store sweeps into
	// their own lanes with 60m package budgets, so the exhaustive partition is
	// six lanes and both release matrices must select all of them.
	want := []string{"broad", "billing", "billingstore", "support", "runtime", "architecture"}
	for _, workflow := range []struct {
		path       string
		consumerID string
	}{
		{path: releaseWorkflow, consumerID: "verify"},
		{path: releaseGatesWorkflow, consumerID: "release-gates"},
	} {
		lanes := workflowMatrixLanes(t, workflow.path, "race")
		if len(lanes) != len(want) {
			t.Fatalf("%s race matrix has lane(s) %v, want the exhaustive partition %v", workflow.path, lanes, want)
		}
		for i, lane := range lanes {
			if lane != want[i] {
				t.Fatalf("%s race matrix lane %d = %q, want %q", workflow.path, i, lane, want[i])
			}
		}
		if !slices.Contains(workflowNeeds(t, workflow.path, workflow.consumerID), "race") {
			t.Fatalf("%s job %q must depend on every race matrix lane", workflow.path, workflow.consumerID)
		}
		if got := workflowJobTimeoutMinutes(t, workflow.path, "race"); got.Expr != "${{ (matrix.lane == 'billing' || matrix.lane == 'billingstore') && 75 || 40 }}" {
			t.Fatalf("%s race lane budget = %s, want billing and billing-store lanes 75m and other lanes 40m", workflow.path, got)
		}
		if got := workflowStepRun(t, workflow.path, "race", "Strict Linux race detector"); got != `bash scripts/race-check.sh --strict --lane "$RACE_LANE"` {
			t.Fatalf("%s strict race command = %q", workflow.path, got)
		}
		laneEnv := workflowNode(t, workflowStep(t, workflow.path, "race", "Strict Linux race detector"), "env", "RACE_LANE")
		if laneEnv == nil || laneEnv.Value != "${{ matrix.lane }}" {
			t.Fatalf("%s strict race step must select its matrix lane", workflow.path)
		}
	}
	if !slices.Contains(workflowNeeds(t, releaseWorkflow, "release"), "verify") {
		t.Fatalf("%s release job must retain the verify dependency that follows all race lanes", releaseWorkflow)
	}

	continueOnError := workflowNode(t, parseWorkflow(t, releaseGatesWorkflow), "jobs", "race", "continue-on-error")
	if continueOnError != nil && continueOnError.Value != "false" {
		t.Fatalf("%s race job must fail when a lane fails", releaseGatesWorkflow)
	}
	if condition := workflowNode(t, parseWorkflow(t, releaseGatesWorkflow), "jobs", "release-gates", "if"); condition != nil {
		t.Fatalf("%s aggregate job must use the default success condition after needs: race, got %q", releaseGatesWorkflow, condition.Value)
	}

	record := workflowStepRun(t, releaseGatesWorkflow, "race", "Record tested revision")
	for _, evidence := range []string{`tested_sha="$(git rev-parse HEAD)"`, "tested_sha=%s", "command=bash scripts/race-check.sh --strict --lane %s"} {
		if !strings.Contains(record, evidence) {
			t.Fatalf("%s race evidence step is missing %q", releaseGatesWorkflow, evidence)
		}
	}
	artifact := workflowStep(t, releaseGatesWorkflow, "race", "Upload strict race evidence")
	artifactName := workflowNode(t, artifact, "with", "name")
	artifactPath := workflowNode(t, artifact, "with", "path")
	if artifactName == nil || !strings.Contains(artifactName.Value, "steps.revision.outputs.sha") {
		t.Fatalf("%s race artifact name must include the tested SHA", releaseGatesWorkflow)
	}
	if artifactPath == nil || !strings.Contains(artifactPath.Value, ".tmp/race-evidence.txt") {
		t.Fatalf("%s race artifact must contain the tested-SHA evidence file", releaseGatesWorkflow)
	}
}

func workflowNeeds(t *testing.T, rel, job string) []string {
	t.Helper()
	node := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "needs")
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			out = append(out, item.Value)
		}
		return out
	default:
		t.Fatalf("%s job %q needs is not a scalar or sequence", rel, job)
		return nil
	}
}

func workflowStep(t *testing.T, rel, job, name string) *yaml.Node {
	t.Helper()
	steps := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		t.Fatalf("%s job %q has no steps sequence", rel, job)
	}
	for _, step := range steps.Content {
		stepName := workflowNode(t, step, "name")
		if stepName != nil && stepName.Value == name {
			return step
		}
	}
	t.Fatalf("%s job %q is missing step %q", rel, job, name)
	return nil
}

func workflowStepRun(t *testing.T, rel, job, name string) string {
	t.Helper()
	node := workflowNode(t, workflowStep(t, rel, job, name), "run")
	if node == nil {
		t.Fatalf("%s job %q step %q has no run command", rel, job, name)
	}
	return node.Value
}

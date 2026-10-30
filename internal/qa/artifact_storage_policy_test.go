package qa

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// GitHub Actions artifact storage is a shared, capped allowance: 500 MB on the
// Free plan, pooled with Packages storage. The allowance is billed per hour of
// occupancy, so a single large upload keeps consuming it for its whole
// retention window, and when it is exhausted every later workflow is blocked
// before it starts. In October 2026 eight Windows cost artifacts of about
// 149 MB each took 98.8% of the account's allowance and blocked remote CI.
//
// Two rules keep that from recurring. Every upload declares an explicit
// retention window, so GitHub removes it automatically; an upload cannot rely
// on the 90-day default. And diagnostic evidence is retained for at most two
// weeks, long enough to compare two weekly runs. Release qualification keeps
// 30 days because it is the audit trail for a shipped version.
const (
	diagnosticArtifactRetentionDays = 14
	releaseArtifactRetentionDays    = 30
)

// releaseArtifactWorkflows may retain artifacts for the release window. They
// upload strict race evidence for a release candidate, not routine diagnostics.
var releaseArtifactWorkflows = map[string]struct{}{
	"release.yml":                      {},
	"backend-plugin-release-gates.yml": {},
}

type artifactUpload struct {
	workflow  string
	job       string
	step      string
	retention string
	paths     []string
}

// TestWorkflowArtifacts_DeclareBoundedRetention enforces explicit, bounded
// retention on every actions/upload-artifact step under .github/workflows.
func TestWorkflowArtifacts_DeclareBoundedRetention(t *testing.T) {
	t.Parallel()
	uploads := collectArtifactUploads(t)
	if len(uploads) == 0 {
		t.Fatal("no upload-artifact step was found; the policy check did not run")
	}
	for _, upload := range uploads {
		t.Run(upload.workflow+"/"+upload.step, func(t *testing.T) {
			t.Parallel()
			if upload.retention == "" {
				t.Fatalf("%s/%s: upload-artifact must declare retention-days; the 90-day default outlives the evidence and consumes the storage allowance", upload.workflow, upload.step)
			}
			days, err := strconv.Atoi(upload.retention)
			if err != nil {
				t.Fatalf("%s/%s: retention-days %q is not an integer: %v", upload.workflow, upload.step, upload.retention, err)
			}
			limit := diagnosticArtifactRetentionDays
			if _, release := releaseArtifactWorkflows[upload.workflow]; release {
				limit = releaseArtifactRetentionDays
			}
			if days < 1 || days > limit {
				t.Fatalf("%s/%s: retention-days %d is outside 1..%d; longer retention keeps consuming the account artifact allowance", upload.workflow, upload.step, days, limit)
			}
		})
	}
}

// TestWorkflowArtifacts_RejectWholeWorkspaceUploads rejects a path that names
// the checkout or runner temp root. Such a path uploads binaries, caches and
// per-run scratch trees next to the report that was meant to be retained.
func TestWorkflowArtifacts_RejectWholeWorkspaceUploads(t *testing.T) {
	t.Parallel()
	uploads := collectArtifactUploads(t)
	if len(uploads) == 0 {
		t.Fatal("no upload-artifact step was found; the policy check did not run")
	}
	roots := map[string]struct{}{
		"":                        {},
		".":                       {},
		"./":                      {},
		"${{ github.workspace }}": {},
		"${{ runner.temp }}":      {},
		"${{ runner.temp }}/":     {},
		"${{ runner.temp }}/*":    {},
	}
	for _, upload := range uploads {
		t.Run(upload.workflow+"/"+upload.step, func(t *testing.T) {
			t.Parallel()
			if len(upload.paths) == 0 {
				t.Fatalf("%s/%s: upload-artifact must declare a bounded path", upload.workflow, upload.step)
			}
			for _, path := range upload.paths {
				if _, root := roots[path]; root {
					t.Fatalf("%s/%s: path %q uploads the whole checkout or runner temp root; name the report directory instead", upload.workflow, upload.step, path)
				}
			}
		})
	}
}

// TestWorkflowArtifacts_DevelopmentCostRetainsReportsOnly pins the exact upload
// layout of the Windows cost watchdog. Its OutputRoot also owns the
// aip-testcost/aipstd binaries and per-run scratch trees; uploading the root
// produced the eight 149 MB artifacts that exhausted the account allowance.
// The job is paused until historical comparisons are affordable again, so the
// regression must not wait for a live run to surface.
func TestWorkflowArtifacts_DevelopmentCostRetainsReportsOnly(t *testing.T) {
	t.Parallel()
	const workflow = "development-cost-weekly.yml"
	uploads := collectArtifactUploads(t)
	var found *artifactUpload
	for i := range uploads {
		if uploads[i].workflow == workflow {
			found = &uploads[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("%s: no upload-artifact step was found; the policy check did not run", workflow)
	}
	want := []string{
		"${{ runner.temp }}/development-cost/measurements",
		"${{ runner.temp }}/development-cost/reports",
	}
	if strings.Join(found.paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s: upload paths = %q, want %q; binaries and scratch trees stay out of artifact storage", workflow, found.paths, want)
	}
}

func collectArtifactUploads(t *testing.T) []artifactUpload {
	t.Helper()
	entries, err := os.ReadDir(repositoryFile(t, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	var uploads []artifactUpload
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", entry.Name())), &doc); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if len(doc.Content) == 0 {
			t.Fatalf("%s: empty workflow document", entry.Name())
		}
		jobs := workflowMappingValue(doc.Content[0], "jobs")
		if jobs == nil || jobs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			jobName := jobs.Content[i].Value
			steps := workflowMappingValue(jobs.Content[i+1], "steps")
			if steps == nil || steps.Kind != yaml.SequenceNode {
				continue
			}
			for _, step := range steps.Content {
				uses := workflowMappingValue(step, "uses")
				if uses == nil || !strings.HasPrefix(uses.Value, "actions/upload-artifact@") {
					continue
				}
				with := workflowMappingValue(step, "with")
				upload := artifactUpload{workflow: entry.Name(), job: jobName}
				if name := workflowMappingValue(step, "name"); name != nil {
					upload.step = name.Value
				}
				if retention := workflowMappingValue(with, "retention-days"); retention != nil {
					upload.retention = strings.TrimSpace(retention.Value)
				}
				upload.paths = workflowArtifactPaths(workflowMappingValue(with, "path"))
				uploads = append(uploads, upload)
			}
		}
	}
	return uploads
}

// workflowArtifactPaths normalizes the scalar, block-scalar and sequence forms
// accepted by upload-artifact into one trimmed path per entry.
func workflowArtifactPaths(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	var values []string
	switch node.Kind {
	case yaml.ScalarNode:
		values = strings.Split(node.Value, "\n")
	case yaml.SequenceNode:
		for _, entry := range node.Content {
			values = append(values, entry.Value)
		}
	}
	paths := make([]string, 0, len(values))
	for _, value := range values {
		if path := strings.TrimSpace(value); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

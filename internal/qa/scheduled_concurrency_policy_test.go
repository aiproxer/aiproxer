package qa

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A workflow triggered by both a main push and a schedule gives the two events
// the same concurrency identity: github.head_ref is empty and github.ref_name is
// "main" for both. When the push run is created while the scheduled run is in
// flight, the concurrency group replaces the scheduled run and cancels it
// mid-task, so the only scheduled evidence for that period is lost and the CI
// watchdog sees a red scheduled run.
//
// A scheduled run is protected in one of two ways: the group names the event that
// owns it (the ci.yml rule from #790), or the workflow never cancels in-progress
// runs (auto-update-prs.yml serializes instead). Neither is optional: the rule
// recurred on the 2026-10-10 backend-plugin-cross-platform nightly, cancelled 8
// minutes in by the push run for a tools/backendplugin test refactor that matched
// the workflow's paths.
func TestWorkflowConcurrency_ScheduledRunKeepsItsOwnGroup(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(repositoryFile(t, ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
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
		root := doc.Content[0]
		on := workflowMappingValue(root, "on")
		if on == nil || workflowMappingValue(on, "schedule") == nil {
			continue
		}
		push := workflowMappingValue(on, "push")
		if push == nil || !workflowPushTargetsMain(push) {
			continue
		}
		checked++
		concurrency := workflowMappingValue(root, "concurrency")
		group := workflowMappingValue(concurrency, "group")
		if group != nil && strings.Contains(group.Value, "github.event_name") {
			continue
		}
		if !workflowCancelsInProgress(concurrency) {
			continue
		}
		value := ""
		if group != nil {
			value = group.Value
		}
		t.Errorf("%s: a scheduled run shares concurrency group %q with a main push and the workflow cancels in-progress runs; the next matching main push cancels the scheduled run", entry.Name(), value)
	}
	if checked == 0 {
		t.Fatal("no workflow with both a schedule and a main push was found; the policy check did not run")
	}
}

// workflowMappingValue returns the value node for key in a YAML mapping node.
// It reads yaml.Node values instead of decoding into structs because the trigger
// key "on" resolves to a boolean when a document is unmarshalled normally.
func workflowMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// workflowPushTargetsMain reports whether a push trigger can fire on main. A
// bare push trigger and a branch list that omits "branches" both match main.
func workflowPushTargetsMain(push *yaml.Node) bool {
	if push.Kind != yaml.MappingNode {
		return true
	}
	branches := workflowMappingValue(push, "branches")
	if branches == nil || branches.Kind != yaml.SequenceNode {
		return true
	}
	for _, branch := range branches.Content {
		if branch.Value == "main" {
			return true
		}
	}
	return false
}

// workflowCancelsInProgress reports whether the workflow unconditionally cancels
// an in-progress run in its group. An expression is not evaluated here: only the
// literal true is treated as cancelling, so a group that does not name the event
// must either omit the key or set it to false.
func workflowCancelsInProgress(concurrency *yaml.Node) bool {
	cancel := workflowMappingValue(concurrency, "cancel-in-progress")
	return cancel != nil && strings.EqualFold(cancel.Value, "true")
}

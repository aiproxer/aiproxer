package carriers

import (
	"embed"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

//go:embed testdata/*.json
var fixtureFiles embed.FS

func TestFixtureFiles(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name string
		rule string
	}{
		{"testdata/codex_update_plan_v1.json", CodexUpdatePlanV1},
		{"testdata/opencode_todo_v1.json", OpenCodeTodoV1},
		{"testdata/cline_task_progress_v1.json", ClineTaskProgressV1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			data, err := fixtureFiles.ReadFile(fixture.name)
			if err != nil {
				t.Fatal(err)
			}
			got, matched, err := Extract(data)
			if err != nil || !matched || got.RuleID != fixture.rule {
				t.Fatalf("rule=%q matched=%v err=%v", got.RuleID, matched, err)
			}
		})
	}
	data, err := fixtureFiles.ReadFile("testdata/near_miss_markdown.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, matched, err := Extract(data); matched || err != nil {
		t.Fatalf("near miss matched=%v err=%v", matched, err)
	}
}

func TestApplyPreservesParentBinding(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "principal")
	if err != nil {
		t.Fatal(err)
	}
	base, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fixtureFiles.ReadFile("testdata/codex_update_plan_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	update, matched, err := Extract(data)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
	got, err := Apply(base, update)
	if err != nil {
		t.Fatal(err)
	}
	if got.BranchBinding != base.BranchBinding || got.Revision != base.Revision+1 {
		t.Fatalf("binding/revision changed: %#v", got)
	}
}

func TestExtractVersionedCarrierFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, input, rule string }{
		{"codex", `{"type":"function_call","name":"update_plan","arguments":"{\"explanation\":\"start\",\"plan\":[{\"step\":\"inspect\",\"status\":\"in_progress\"},{\"step\":\"test\",\"status\":\"pending\"}]}"}`, CodexUpdatePlanV1},
		{"opencode", `{"name":"todowrite","input":{"todos":[{"content":"inspect","status":"in_progress","priority":"high"},{"content":"test","status":"pending","priority":"medium"}]}}`, OpenCodeTodoV1},
		{"cline", `{"name":"task_progress","input":{"task_progress":[{"content":"inspect","status":"in_progress","activeForm":"Inspecting"},{"content":"test","status":"pending","activeForm":"Testing"}]}}`, ClineTaskProgressV1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, matched, err := Extract([]byte(tt.input))
			if err != nil || !matched {
				t.Fatalf("Extract matched=%v err=%v", matched, err)
			}
			if got.RuleID != tt.rule || len(got.Plan.Steps) != 2 {
				t.Fatalf("update = %#v", got)
			}
			again, againMatched, err := Extract([]byte(tt.input))
			if err != nil || !againMatched || again.Plan.Steps[0].ID != got.Plan.Steps[0].ID {
				t.Fatalf("non-deterministic normalization: %#v %v", again, err)
			}
		})
	}
}

func TestExtractNearMissesDoNotInferBrand(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`{"name":"update-plan","input":{"plan":[{"step":"x","status":"pending"}]}}`,
		`{"name":"other","input":{"plan":[{"step":"x","status":"pending"}]}}`,
		`{"content":"- [ ] inspect"}`,
		`{"name":"todowrite","input":{"todos":[{"content":"x","status":"finished"}]}}`,
	} {
		got, matched, err := Extract([]byte(input))
		if matched && err == nil {
			t.Fatalf("near miss matched: %#v", got)
		}
	}
}

func TestExtractMalformedRecognizedCarrier(t *testing.T) {
	t.Parallel()
	fixture, readErr := fixtureFiles.ReadFile("testdata/malformed_update_plan_v1.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	_, matched, err := Extract(fixture)
	if !matched || !errors.Is(err, ErrMalformedCarrier) {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
	_, matched, err = Extract([]byte(`{"name":"update_plan","input":{"plan":[{"step":"x","status":"in_progress"},{"step":"y","status":"in_progress"}]}}`))
	if !matched || !errors.Is(err, ErrMalformedCarrier) {
		t.Fatalf("multiple progress matched=%v err=%v", matched, err)
	}
	_, matched, err = Extract([]byte(`{"name":"todowrite","input":{"todos":null}}`))
	if !matched || !errors.Is(err, ErrMalformedCarrier) {
		t.Fatalf("null todos matched=%v err=%v", matched, err)
	}
}

func FuzzExtractNeverPanics(f *testing.F) {
	f.Add([]byte(`{"name":"update_plan","input":{"plan":[]}}`))
	f.Add([]byte(`{"name":"todowrite","input":{"todos":[]}}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, data []byte) { _, _, _ = Extract(data) })
}

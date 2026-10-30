package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloseout_ReconcilesEveryObjectiveAndExactRunEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := sourceGit(t.Context(), root, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceGit(t.Context(), root, "add", "tracked"); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceGit(t.Context(), root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(root, "no-hooks"), "commit", "-qm", "base"); err != nil {
		t.Fatal(err)
	}
	state, err := snapshot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	sha := state.Head
	evidence := filepath.Join(root, "race.txt")
	if err := os.WriteFile(evidence, []byte("tested_sha="+sha+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := Session{Version: 1, ID: "session", Objectives: []Objective{{ID: "original", Delivery: "merged", SpecDelivery: "none", PRs: []Delivery{{Number: 1, Head: sha}}, Runs: []RunRequirement{{ID: 2, Head: sha, Steps: []RequiredStep{{Job: "broad", Name: "fuzz"}}, Evidence: []string{evidence}}}}, {ID: "later", Delivery: "merged", SpecDelivery: "none", PRs: []Delivery{{Number: 3, Head: sha}}}}}
	zero, no := 0, false
	proof := filepath.Join(t.TempDir(), "main.json")
	result := Result{Version: 1, Role: "implementer", Task: "main-smoke", Status: "READY_FOR_REVIEW", Behavioral: &no, Source: state, Commands: []Command{{Purpose: "verification", Argv: []string{"go", "test"}, ExitCode: &zero, Evidence: evidence}}, Findings: []Finding{}}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proof, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range session.Objectives {
		session.Objectives[i].MainEvidence = proof
	}
	query := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "pr":
			return []byte(`{"state":"MERGED","headRefOid":"` + sha + `","mergeCommit":{"oid":"` + sha + `"}}`), nil
		case "run":
			return []byte(`{"headSha":"` + sha + `","status":"completed","conclusion":"success","jobs":[{"name":"broad","conclusion":"success","steps":[{"name":"fuzz","conclusion":"success"}]}]}`), nil
		default:
			return nil, errors.New("unexpected query")
		}
	}
	check := func(s Session, phase string, q ghQuery) CloseoutReport {
		t.Helper()
		r, err := checkSession(t.Context(), root, s, phase, q)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := check(session, "complete", query); !r.Complete || len(r.Pending) != 0 {
		t.Fatalf("complete session rejected: %+v", r)
	}
	if r := check(session, "submitted", query); r.Complete {
		t.Fatal("submission reported as full completion")
	}
	missing := session
	missing.Objectives = append([]Objective(nil), session.Objectives...)
	missing.Objectives[0].MainEvidence = ""
	if r := check(missing, "complete", query); r.Complete {
		t.Fatal("merged PR substituted for main verification")
	}
	missing.Objectives[0].MainEvidence = proof
	missing.Objectives[0].Issue = 4
	if r := check(missing, "complete", query); r.Complete {
		t.Fatal("unverified issue closure ignored")
	}
	for _, payload := range []string{
		`{"headSha":"` + sha + `","status":"completed","conclusion":"failure"}`,
		`{"headSha":"` + strings.Repeat("b", 40) + `","status":"completed","conclusion":"success"}`,
		`{"headSha":"` + sha + `","status":"completed","conclusion":"success","jobs":[{"name":"broad","conclusion":"success","steps":[{"name":"fuzz","conclusion":"skipped"}]}]}`,
	} {
		q := func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "run" {
				return []byte(payload), nil
			}
			return query(ctx, args...)
		}
		if r := check(session, "complete", q); r.Complete || len(r.Pending) == 0 {
			t.Fatalf("earlier objective failure lost: %+v", r)
		}
	}
	if err := os.WriteFile(evidence, []byte("tested_sha="+strings.Repeat("b", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := check(session, "complete", query); r.Complete {
		t.Fatal("wrong artifact SHA accepted")
	}
	if _, err := checkSession(t.Context(), root, Session{Version: 1, ID: "empty"}, "complete", query); err == nil {
		t.Fatal("empty scope silently completed")
	}
}

func TestCloseout_ReportsOwnedResourcesAndArchivedSpec(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := sourceGit(t.Context(), root, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "worktrees", "owned")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, ".kiro/specs/archive/example")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "spec.json"), []byte(`{"phase":"completed","completed":true,"ready_for_implementation":false,"approvals":{"requirements":{"approved":true},"design":{"approved":true},"tasks":{"approved":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "tasks.md"), []byte("- [x] shipped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Session{Version: 1, ID: "local", Objectives: []Objective{{ID: "docs", Delivery: "submitted", Spec: ".kiro/specs/archive/example/spec.json", SpecDelivery: "complete"}}, Worktrees: []string{worktree}}
	q := func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("must not infer remote scope")
	}
	r, err := checkSession(t.Context(), root, s, "complete", q)
	if err != nil || r.Complete || len(r.Pending) != 1 {
		t.Fatalf("owned resource not reconciled: %+v %v", r, err)
	}
	if err := os.Remove(worktree); err != nil {
		t.Fatal(err)
	}
	r, err = checkSession(t.Context(), root, s, "complete", q)
	if err != nil || !r.Complete {
		t.Fatalf("local archived objective rejected: %+v %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(archive, "tasks.md"), []byte("- [x] shipped\n- [ ] regression pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := checkSession(t.Context(), root, s, "complete", q); err != nil || r.Complete {
		t.Fatalf("unchecked task accepted: %+v %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(archive, "spec.json"), []byte(`{"phase":"completed","completed":false,"ready_for_implementation":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = checkSession(t.Context(), root, s, "complete", q)
	if err != nil || r.Complete {
		t.Fatalf("unfinished spec accepted: %+v %v", r, err)
	}
}

func TestCloseout_InventoryLinkSurvivesTaskIndexUpdates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inventory := filepath.Join(dir, "session.json")
	s := Session{Version: 1, ID: "whole-scope", Objectives: []Objective{{ID: "original", Delivery: "submitted", Spec: ".kiro/specs/archive/example/spec.json", SpecDelivery: "complete"}}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inventory, data, 0o600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, "execution.json")
	first := Result{Version: 1, Task: "original", Role: "implementer", Status: "BLOCKED"}
	if err := recordResultWithInventory(index, "original.json", first, inventory); err != nil {
		t.Fatal(err)
	}
	first.Task = "later"
	if err := recordResult(index, "later.json", first); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(index)
	if err != nil {
		t.Fatal(err)
	}
	var state executionIndex
	decodeErr := decodeStrict(file, &state)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil || state.Inventory != inventory || len(state.Entries) != 2 {
		t.Fatalf("earlier scope disappeared: %+v %v %v", state, decodeErr, closeErr)
	}
}

func TestCloseout_DispatchedRunBindsWorkflowAndTestedRevisions(t *testing.T) {
	t.Parallel()
	workflow, tested := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, runHead, marker string
		omitEvidence          bool
		wantPending           bool
	}{
		{name: "distinct checkout", runHead: workflow, marker: tested},
		{name: "wrong workflow", runHead: tested, marker: tested, wantPending: true},
		{name: "wrong checkout", runHead: workflow, marker: workflow, wantPending: true},
		{name: "no checkout proof", runHead: workflow, omitEvidence: true, wantPending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := filepath.Join(t.TempDir(), "race.txt")
			if err := os.WriteFile(evidence, []byte("tested_sha="+tc.marker+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			paths := []string{evidence}
			if tc.omitEvidence {
				paths = nil
			}
			encodedPaths, err := json.Marshal(paths)
			if err != nil {
				t.Fatal(err)
			}
			var session Session
			payload := `{"version":1,"id":"dispatch","objectives":[{"id":"race","delivery":"submitted","spec_delivery":"none","runs":[{"id":1,"head":"` + workflow + `","tested_sha":"` + tested + `","steps":[{"job":"broad","name":"race"}],"evidence":` + string(encodedPaths) + `}]}]}`
			if err := decodeStrict(strings.NewReader(payload), &session); err != nil {
				t.Fatal(err)
			}
			query := func(context.Context, ...string) ([]byte, error) {
				return []byte(`{"headSha":"` + tc.runHead + `","status":"completed","conclusion":"success","jobs":[{"name":"broad","conclusion":"success","steps":[{"name":"race","conclusion":"success"}]}]}`), nil
			}
			r, err := checkSession(t.Context(), t.TempDir(), session, "submitted", query)
			if tc.omitEvidence {
				if err == nil {
					t.Fatal("distinct checkout accepted without artifact proof")
				}
				return
			}
			if err != nil || (len(r.Pending) != 0) != tc.wantPending || r.Complete {
				t.Fatalf("revision evidence: %+v, err=%v", r, err)
			}
		})
	}
}

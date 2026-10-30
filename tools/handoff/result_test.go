package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoff_RejectsMissingContradictoryAndFailedEvidence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "check.log")
	if err := os.WriteFile(log, []byte("observed result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero, one := 0, 1
	yes, no := true, false
	result := Result{Version: 1, Role: "implementer", Task: "1.1", Status: "READY_FOR_REVIEW", Source: SourceState{Head: strings.Repeat("a", 40), Fingerprint: strings.Repeat("b", 64)}, Behavioral: &yes, Findings: []Finding{}, Commands: []Command{{Purpose: "red", Argv: []string{"go", "test"}, ExitCode: &one, Evidence: log}, {Purpose: "verification", Argv: []string{"go", "test"}, ExitCode: &zero, Evidence: log}}}
	if err := result.Validate(dir); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Result){
		func(r *Result) { r.Task = "" },
		func(r *Result) { r.Blocker = "cannot proceed" },
		func(r *Result) { r.Commands = r.Commands[:1] },
		func(r *Result) { r.Commands[1].ExitCode = nil },
		func(r *Result) { r.Commands[1].ExitCode = &one },
		func(r *Result) { r.Commands[0].Evidence = "missing.log" },
	} {
		copy := result
		copy.Commands = append([]Command(nil), result.Commands...)
		mutate(&copy)
		if err := copy.Validate(dir); err == nil {
			t.Fatalf("accepted invalid artifact: %+v", copy)
		}
	}
	result.Role, result.Status, result.Behavioral = "reviewer", "APPROVED", &no
	result.Findings = []Finding{{Severity: "Important", Detail: "unfixed boundary leak"}}
	if err := result.Validate(dir); err == nil {
		t.Fatal("approved artifact accepted unresolved blocking finding")
	}
	result.Status = "REJECTED"
	if err := result.Validate(dir); err == nil {
		t.Fatal("rejection without actionable remediation accepted")
	}
	result.Remediation = "repair the boundary and rerun the named test"
	if err := result.Validate(dir); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"version":1,"unknown":true}`, `{} {}`, `{"version":1,"version":2}`} {
		if _, err := decodeResult(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted ambiguous input: %s", input)
		}
	}
}

func TestHandoff_IndexPreservesOtherTasksAndFailsClosedOnCorruption(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "execution.json")
	first := Result{Version: 1, Task: "1.1", Role: "implementer", Status: "BLOCKED"}
	if err := recordResult(file, "first.json", first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Task, second.Role = "1.2", "reviewer"
	if err := recordResult(file, "second.json", second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("first.json")) || !bytes.Contains(data, []byte("second.json")) {
		t.Fatalf("index discarded earlier work: %s", data)
	}
	for _, input := range []string{"corrupted", "null", `{"entries":{}}`, `{"version":1}`} {
		if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := recordResult(file, "third.json", first); err == nil {
			t.Fatalf("corrupt index silently overwritten: %s", input)
		}
	}
}

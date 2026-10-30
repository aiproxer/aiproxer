package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerificationLedger_PassingRetryCannotEraseFailure(t *testing.T) {
	repo, scratch := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=" + scratch, "commit", "--allow-empty", "-qm", "base"}} {
		if _, err := sourceGit(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	index := filepath.Join(scratch, "execution.json")
	log := filepath.Join(scratch, "failed.log")
	if err := runHandoff(t.Context(), []string{"-task=run", "-repo=" + repo, "-index=" + index, "-log=" + log, "--", "git", "rev-parse", "--verify", "missing"}, io.Discard, io.Discard); err == nil {
		t.Fatal("failure swallowed")
	}
	raw, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("failed verification not retained: %v", err)
	}
	if !bytes.Contains(raw, []byte("failed.log.record.json")) {
		t.Fatalf("failure missing: %s", raw)
	}
	inventory := filepath.Join(scratch, "session.json")
	if err := os.WriteFile(inventory, []byte(fmtSpecSession("partial", "Remaining tasks stay active")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordResultWithInventory(index, "result.json", Result{Task: "slice", Role: "implementer", Status: "BLOCKED"}, inventory); err != nil {
		t.Fatal(err)
	}
	if err := runHandoff(t.Context(), []string{"-task=closeout", "-repo=" + repo, "-index=" + index, "-phase=submitted"}, io.Discard, io.Discard); err == nil {
		t.Fatal("index-based closeout ignored a failed attempt")
	}
	if err := runHandoff(t.Context(), []string{"-task=run", "-repo=" + repo, "-index=" + index, "-log=" + filepath.Join(scratch, "passed.log"), "--", "git", "rev-parse", "HEAD"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runHandoff(t.Context(), []string{"-task=run", "-repo=" + repo, "-index=" + index, "-log=" + log, "--", "git", "rev-parse", "HEAD"}, io.Discard, io.Discard); err == nil {
		t.Fatal("recorded log overwritten")
	}
	raw, err = os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	var session Session
	data := `{"version":1,"id":"checks","objectives":[{"id":"scope","delivery":"submitted","spec_delivery":"none","issue":1}],"verification_attempts":` + string(state["verification_attempts"]) + `}`
	if err := decodeStrict(strings.NewReader(data), &session); err != nil {
		t.Fatal(err)
	}
	query := func(context.Context, ...string) ([]byte, error) { return []byte(`{"state":"CLOSED"}`), nil }
	report, err := checkSession(t.Context(), repo, session, "complete", query)
	if err != nil || report.Complete {
		t.Fatalf("undisposed failure accepted: %+v %v", report, err)
	}
	// A later task result must preserve the attempt history too.
	if err := recordResult(index, "result.json", Result{Task: "later", Role: "implementer", Status: "BLOCKED"}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(index)
	if err != nil || !bytes.Contains(raw, []byte("failed.log.record.json")) {
		t.Fatalf("task update erased failure: %s %v", raw, err)
	}
	session.VerificationAttempts[0].Disposition = "Accepted fixture risk by owner; failed log retained; focused repeats passed, root cause not claimed fixed."
	report, err = checkSession(t.Context(), repo, session, "complete", query)
	if err != nil || !report.Complete {
		t.Fatalf("explicit disposition rejected: %+v %v", report, err)
	}
	if err := os.WriteFile(log, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = checkSession(t.Context(), repo, session, "complete", query)
	if err != nil || report.Complete {
		t.Fatalf("disposition waived evidence integrity: %+v %v", report, err)
	}
	if err := os.Remove(log + ".record.json"); err != nil {
		t.Fatal(err)
	}
	report, err = checkSession(t.Context(), repo, session, "complete", query)
	if err != nil || report.Complete {
		t.Fatalf("missing receipt waived: %+v %v", report, err)
	}
}

func TestSpecDelivery_NoSpecMustBeExplicit(t *testing.T) {
	for _, intent := range []string{"", "partial", "complete", "none"} {
		s := Session{Version: 1, ID: "explicit", Objectives: []Objective{{ID: "scope", Delivery: "submitted", Issue: 1, SpecDelivery: intent}}}
		if err := s.validate(); (err == nil) != (intent == "none") {
			t.Errorf("spec_delivery=%q: %v", intent, err)
		}
	}
}

func TestSpecDelivery_ExplicitPartialVersusComplete(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, ".kiro/specs/example")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "spec.json"), []byte(`{"phase":"tasks-generated","ready_for_implementation":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		intent, disposition string
		want                bool
	}{
		{"partial", "Remaining tasks stay active for the next slice", true},
		{"partial", "", false},
		{"complete", "", false},
		{"", "", false},
	} {
		var session Session
		raw := fmtSpecSession(tc.intent, tc.disposition)
		err := decodeStrict(strings.NewReader(raw), &session)
		if err == nil {
			r, checkErr := checkSession(t.Context(), root, session, "complete", func(context.Context, ...string) ([]byte, error) { return nil, errors.New("unexpected remote scope") })
			err = checkErr
			if err == nil && !r.Complete {
				err = errors.New(strings.Join(r.Pending, ";"))
			}
		}
		if (err == nil) != tc.want {
			t.Errorf("intent=%q disposition=%q: %v", tc.intent, tc.disposition, err)
		}
	}
}

func fmtSpecSession(intent, disposition string) string {
	b, _ := json.Marshal(map[string]any{"version": 1, "id": "spec", "objectives": []any{map[string]any{"id": "slice", "delivery": "submitted", "spec": ".kiro/specs/example/spec.json", "spec_delivery": intent, "spec_disposition": disposition}}})
	return string(b)
}

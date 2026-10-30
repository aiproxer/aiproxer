package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Session is an explicit approved-scope inventory, never an inferred ownership
// registry. The read-only checker does not authorize any remote/local mutation.
type Session struct {
	Version              int                   `json:"version"`
	ID                   string                `json:"id"`
	Objectives           []Objective           `json:"objectives"`
	Worktrees            []string              `json:"worktrees"`
	Branches             []string              `json:"branches"`
	VerificationAttempts []VerificationAttempt `json:"verification_attempts,omitempty"`
}

type Objective struct {
	ID              string           `json:"id"`
	Delivery        string           `json:"delivery"`
	PRs             []Delivery       `json:"prs"`
	Issue           int              `json:"issue,omitempty"`
	Spec            string           `json:"spec,omitempty"`
	SpecDelivery    string           `json:"spec_delivery,omitempty"`
	SpecDisposition string           `json:"spec_disposition,omitempty"`
	Runs            []RunRequirement `json:"runs"`
	MainEvidence    string           `json:"main_evidence,omitempty"`
}

type Delivery struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
}
type RequiredStep struct {
	Job  string `json:"job"`
	Name string `json:"name"`
}
type RunRequirement struct {
	ID        int64          `json:"id"`
	Head      string         `json:"head"`                 // GitHub workflow revision (headSha).
	TestedSHA string         `json:"tested_sha,omitempty"` // Checkout revision; defaults to Head.
	Steps     []RequiredStep `json:"steps"`
	Evidence  []string       `json:"evidence"`
}
type CloseoutReport struct {
	Session  string   `json:"session"`
	Phase    string   `json:"phase"`
	Complete bool     `json:"complete"`
	Pending  []string `json:"pending"`
}
type ghQuery func(context.Context, ...string) ([]byte, error)

func queryGH(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = repo
	return cmd.Output()
}

func (s Session) validate() error {
	if s.Version != 1 || strings.TrimSpace(s.ID) == "" || len(s.Objectives) == 0 {
		return errors.New("session requires version=1, id and explicit nonempty objectives")
	}
	seen := map[string]bool{}
	for _, o := range s.Objectives {
		if strings.TrimSpace(o.ID) == "" || seen[o.ID] || (o.Delivery != "submitted" && o.Delivery != "merged") || (len(o.PRs) == 0 && o.Spec == "" && len(o.Runs) == 0 && o.Issue == 0) {
			return fmt.Errorf("invalid/duplicate objective %q or missing delivery/scope", o.ID)
		}
		seen[o.ID] = true
		for _, p := range o.PRs {
			if p.Number < 1 || !validHex(p.Head, 40, 64) {
				return fmt.Errorf("objective %s requires positive PR number and exact head", o.ID)
			}
		}
		for _, r := range o.Runs {
			if r.ID < 1 || !validHex(r.Head, 40, 64) || len(r.Steps) == 0 {
				return fmt.Errorf("objective %s requires run ID, exact head and required executed steps", o.ID)
			}
			if r.TestedSHA != "" && !validHex(r.TestedSHA, 40, 64) {
				return fmt.Errorf("objective %s requires an exact tested_sha", o.ID)
			}
			if r.TestedSHA != "" && r.TestedSHA != r.Head && len(r.Evidence) == 0 {
				return fmt.Errorf("objective %s requires artifact proof for a distinct tested_sha", o.ID)
			}
			for _, step := range r.Steps {
				if step.Job == "" || step.Name == "" {
					return errors.New("required run step needs job and name")
				}
			}
			for _, p := range r.Evidence {
				if !filepath.IsAbs(p) {
					return errors.New("artifact evidence paths must be absolute scratch paths")
				}
			}
		}
		if o.Issue < 0 {
			return errors.New("issue number must be positive when present")
		}
		if o.Spec != "" {
			if !strings.HasPrefix(o.Spec, ".kiro/specs/") || filepath.IsAbs(o.Spec) || strings.Contains(o.Spec, "..") || !strings.HasSuffix(o.Spec, "/spec.json") {
				return errors.New("spec must name a repository-relative spec.json")
			}
			if o.SpecDelivery != "partial" && o.SpecDelivery != "complete" {
				return errors.New("associated spec requires explicit spec_delivery: partial or complete")
			}
			archived := strings.HasPrefix(o.Spec, ".kiro/specs/archive/")
			if o.SpecDelivery == "complete" && !archived {
				return errors.New("complete spec delivery requires an archived spec")
			}
			if o.SpecDelivery == "partial" && (archived || strings.TrimSpace(o.SpecDisposition) == "") {
				return errors.New("partial spec delivery requires an active spec and explicit remaining-scope disposition")
			}
		} else if o.SpecDelivery != "none" || o.SpecDisposition != "" {
			return errors.New("objectives without an associated spec require explicit spec_delivery=none")
		}
		if o.MainEvidence != "" && !filepath.IsAbs(o.MainEvidence) {
			return errors.New("main evidence must be an absolute task artifact path")
		}
	}
	for _, p := range s.Worktrees {
		if !filepath.IsAbs(p) || !strings.Contains(filepath.ToSlash(p), "/worktrees/") {
			return errors.New("owned worktree paths must be explicit absolute paths under worktrees")
		}
	}
	for _, b := range s.Branches {
		if b == "" || b == "main" || b == "dev" || strings.HasPrefix(b, "refs/") || strings.ContainsAny(b, "*?[] ") {
			return errors.New("owned branches must be exact task-local branch names")
		}
	}
	return nil
}

func checkSession(ctx context.Context, repo string, s Session, phase string, query ghQuery) (CloseoutReport, error) {
	report := CloseoutReport{Session: s.ID, Phase: phase, Pending: []string{}}
	if err := s.validate(); err != nil {
		return report, err
	}
	if phase != "complete" && phase != "submitted" {
		return report, errors.New("closeout phase must be submitted or complete")
	}
	for _, attempt := range s.VerificationAttempts {
		if !filepath.IsAbs(attempt.Record) {
			return report, errors.New("verification attempt requires an absolute command receipt")
		}
		record, err := loadCommandRecord(attempt.Record)
		if err != nil || record.Command.ExitCode == nil {
			report.Pending = append(report.Pending, "verification attempt receipt missing or invalid: "+attempt.Record)
			continue
		}
		if attempt.Record != record.Command.Evidence+".record.json" {
			report.Pending = append(report.Pending, "verification receipt must accompany its log: "+attempt.Record)
			continue
		}
		record.Command.Record = attempt.Record
		if err := checkCommandRecord(record.Command.Evidence, record.Command, record.Source); err != nil {
			report.Pending = append(report.Pending, "verification attempt evidence invalid: "+attempt.Record)
			continue
		}
		if record.Command.Purpose != "red" && *record.Command.ExitCode != 0 && strings.TrimSpace(attempt.Disposition) == "" {
			report.Pending = append(report.Pending, "failed verification needs explicit disposition (retry is not a fix): "+attempt.Record)
		}
	}
	for _, o := range s.Objectives {
		add := func(message string) { report.Pending = append(report.Pending, o.ID+": "+message) }
		for _, p := range o.PRs {
			var data struct {
				State, HeadRefOid string
				MergeCommit       *struct{ Oid string }
			}
			raw, err := query(ctx, "pr", "view", strconv.Itoa(p.Number), "--json", "state,headRefOid,mergeCommit")
			if err != nil || json.Unmarshal(raw, &data) != nil {
				add(fmt.Sprintf("PR %d unavailable", p.Number))
				continue
			}
			if data.HeadRefOid != p.Head {
				add(fmt.Sprintf("PR %d head changed; rebind evidence", p.Number))
			}
			if o.Delivery == "merged" && data.State != "MERGED" {
				add(fmt.Sprintf("PR %d not merged", p.Number))
			}
			if data.State != "OPEN" && data.State != "MERGED" {
				add(fmt.Sprintf("PR %d closed without delivery", p.Number))
			}
		}
		for _, r := range o.Runs {
			var data struct {
				HeadSha, Status, Conclusion string
				Jobs                        []struct {
					Name, Conclusion string
					Steps            []struct{ Name, Conclusion string }
				}
			}
			raw, err := query(ctx, "run", "view", strconv.FormatInt(r.ID, 10), "--json", "headSha,status,conclusion,jobs")
			if err != nil || json.Unmarshal(raw, &data) != nil {
				add(fmt.Sprintf("required run %d unavailable", r.ID))
				continue
			}
			if data.HeadSha != r.Head || data.Status != "completed" || data.Conclusion != "success" {
				add(fmt.Sprintf("required run %d pending, failed or stale", r.ID))
			}
			for _, required := range r.Steps {
				passed := false
				for _, job := range data.Jobs {
					if job.Name != required.Job || job.Conclusion != "success" {
						continue
					}
					for _, step := range job.Steps {
						if step.Name == required.Name && step.Conclusion == "success" {
							passed = true
						}
					}
				}
				if !passed {
					add(fmt.Sprintf("run %d required step %s/%s not executed successfully", r.ID, required.Job, required.Name))
				}
			}
			testedSHA := r.Head
			if r.TestedSHA != "" {
				testedSHA = r.TestedSHA
			}
			for _, p := range r.Evidence {
				bytes, err := os.ReadFile(p)
				if err != nil || !artifactSHA(string(bytes), testedSHA) {
					add("required artifact missing or wrong tested_sha: " + p)
				}
			}
		}
		if phase == "submitted" {
			continue
		}
		if o.Delivery == "merged" && len(o.PRs) != 0 && o.MainEvidence == "" {
			add("merged-main verification artifact required")
		}
		if o.Issue != 0 {
			var data struct{ State string }
			raw, err := query(ctx, "issue", "view", strconv.Itoa(o.Issue), "--json", "state")
			if err != nil || json.Unmarshal(raw, &data) != nil || data.State != "CLOSED" {
				add(fmt.Sprintf("issue %d not closed/verified", o.Issue))
			}
		}
		if o.Spec != "" && o.SpecDelivery == "partial" {
			var data struct {
				Completed bool
				Phase     string
			}
			raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(o.Spec)))
			if err != nil || json.Unmarshal(raw, &data) != nil || data.Completed || data.Phase == "completed" || data.Phase == "superseded" {
				add("partial delivery must retain an active unfinished spec")
			}
		}
		if o.Spec != "" && o.SpecDelivery == "complete" {
			var data struct {
				Phase     string
				Completed bool
				Ready     *bool                              `json:"ready_for_implementation"`
				Approvals map[string]struct{ Approved bool } `json:"approvals"`
			}
			raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(o.Spec)))
			if err != nil || json.Unmarshal(raw, &data) != nil || !data.Completed || data.Ready == nil || *data.Ready || (data.Phase != "completed" && data.Phase != "superseded") {
				add("spec is not completely archived: " + o.Spec)
			}
			if data.Phase == "completed" {
				for _, stage := range []string{"requirements", "design", "tasks"} {
					if !data.Approvals[stage].Approved {
						add("completed spec lacks approval: " + stage)
					}
				}
				tasks, err := os.ReadFile(filepath.Join(repo, filepath.Dir(o.Spec), "tasks.md"))
				if err != nil || !strings.Contains(string(tasks), "[x]") || strings.Contains(string(tasks), "[ ]") {
					add("completed spec has missing or unchecked tasks")
				}
			}
			active := strings.Replace(o.Spec, ".kiro/specs/archive/", ".kiro/specs/", 1)
			if _, err := os.Lstat(filepath.Dir(filepath.Join(repo, active))); !errors.Is(err, os.ErrNotExist) {
				add("active spec directory still exists or cannot be checked")
			}
		}
		if o.MainEvidence != "" {
			file, err := os.Open(o.MainEvidence)
			if err != nil {
				add("merged-main evidence unavailable")
				continue
			}
			result, decodeErr := decodeResult(file)
			closeErr := file.Close()
			if errors.Join(decodeErr, closeErr) != nil || result.Validate(filepath.Dir(o.MainEvidence)) != nil || (result.Status != "READY_FOR_REVIEW" && result.Status != "APPROVED") {
				add("merged-main evidence invalid")
			}
			for _, p := range o.PRs {
				var data struct{ MergeCommit *struct{ Oid string } }
				raw, err := query(ctx, "pr", "view", strconv.Itoa(p.Number), "--json", "mergeCommit")
				if err != nil || json.Unmarshal(raw, &data) != nil || data.MergeCommit == nil {
					add("merged-main evidence lacks merge provenance")
					continue
				}
				if _, err := sourceGit(ctx, repo, "merge-base", "--is-ancestor", data.MergeCommit.Oid, result.Source.Head); err != nil {
					add("tested main revision does not contain declared merge")
				}
			}
		}
	}
	if phase == "complete" {
		if len(s.Worktrees) != 0 {
			listing, err := sourceGit(ctx, repo, "worktree", "list", "--porcelain")
			if err != nil {
				return report, err
			}
			for _, p := range s.Worktrees {
				_, err := os.Lstat(p)
				if !errors.Is(err, os.ErrNotExist) || strings.Contains(string(listing), "worktree "+filepath.Clean(p)+"\n") {
					report.Pending = append(report.Pending, "owned worktree remains or cannot be checked: "+p)
				}
			}
		}
		for _, b := range s.Branches {
			out, err := sourceGit(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+b)
			if err != nil {
				return report, err
			}
			for line := range strings.FieldsSeq(string(out)) {
				if line == "refs/heads/"+b {
					report.Pending = append(report.Pending, "owned local branch remains: "+b)
				}
			}
		}
	}
	report.Complete = phase == "complete" && len(report.Pending) == 0
	return report, nil
}

func artifactSHA(text, sha string) bool {
	matched := false
	for line := range strings.SplitSeq(text, "\n") {
		if got, ok := strings.CutPrefix(line, "tested_sha="); ok {
			if got != sha {
				return false
			}
			matched = true
		}
	}
	return matched
}

package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Result is the versioned handoff contract. Validation checks evidence integrity,
// not whether the implementation or reviewer conclusion is correct.
type Result struct {
	Version     int         `json:"version"`
	Role        string      `json:"role"`
	Task        string      `json:"task"`
	Status      string      `json:"status"`
	Source      SourceState `json:"source"`
	Behavioral  *bool       `json:"behavioral"`
	Commands    []Command   `json:"commands"`
	Findings    []Finding   `json:"findings"`
	Blocker     string      `json:"blocker,omitempty"`
	Remediation string      `json:"remediation,omitempty"`
}

type Command struct {
	Purpose  string   `json:"purpose"`
	Argv     []string `json:"argv"`
	ExitCode *int     `json:"exit_code"`
	Evidence string   `json:"evidence"`
	Record   string   `json:"command_record,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Detail   string `json:"detail"`
}

func decodeResult(reader io.Reader) (Result, error) {
	var result Result
	err := decodeStrict(reader, &result)
	return result, err
}

func decodeStrict(reader io.Reader, target any) error {
	const maxArtifactBytes = 2 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxArtifactBytes+1))
	if err != nil || len(data) > maxArtifactBytes {
		return errors.New("artifact unreadable or exceeds 2 MiB")
	}
	if err := uniqueMembers(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON document")
	}
	return nil
}

func uniqueMembers(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("invalid or duplicate JSON member %v", key)
			}
			seen[name] = true
		}
		if err := uniqueMembers(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func (r Result) Validate(dir string) error {
	if r.Version != 1 || strings.TrimSpace(r.Task) == "" || r.Behavioral == nil || r.Commands == nil || r.Findings == nil || !validHex(r.Source.Head, 40, 64) || !validHex(r.Source.Fingerprint, 64) {
		return errors.New("version, task, behavioral and source identity are required")
	}
	switch r.Role {
	case "implementer":
		if !slices.Contains([]string{"READY_FOR_REVIEW", "BLOCKED", "NEEDS_CONTEXT"}, r.Status) {
			return errors.New("invalid implementer status")
		}
	case "reviewer":
		if !slices.Contains([]string{"APPROVED", "REJECTED"}, r.Status) || r.Findings == nil {
			return errors.New("reviewer requires APPROVED/REJECTED and findings (use [] when clean)")
		}
	default:
		return errors.New("role must be implementer or reviewer")
	}
	ready := r.Status == "READY_FOR_REVIEW" || r.Status == "APPROVED"
	if ready && r.Blocker != "" {
		return errors.New("ready/approved result contradicts blocker")
	}
	if (r.Status == "BLOCKED" || r.Status == "NEEDS_CONTEXT") && strings.TrimSpace(r.Blocker) == "" {
		return errors.New("blocked/context-needed result requires an actionable blocker")
	}
	if r.Status == "REJECTED" && (strings.TrimSpace(r.Remediation) == "" || len(r.Findings) == 0) {
		return errors.New("rejection requires findings and actionable remediation")
	}
	for _, finding := range r.Findings {
		if !slices.Contains([]string{"Critical", "Important", "Suggestion", "FYI"}, finding.Severity) || strings.TrimSpace(finding.Detail) == "" {
			return errors.New("finding requires a known severity and concrete detail")
		}
		if ready && (finding.Severity == "Critical" || finding.Severity == "Important") {
			return errors.New("ready/approved result has an unresolved blocking finding")
		}
	}
	red, verified := false, false
	for _, command := range r.Commands {
		if !slices.Contains([]string{"red", "verification", "baseline"}, command.Purpose) || len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" || command.ExitCode == nil || command.Evidence == "" {
			return errors.New("command requires purpose, argv, exit_code and evidence")
		}
		name := command.Evidence
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("command evidence is not a readable regular file: %s", name)
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		recorded := command
		if recorded.Record != "" && !filepath.IsAbs(recorded.Record) {
			recorded.Record = filepath.Join(dir, recorded.Record)
		}
		if err := checkCommandRecord(name, recorded, r.Source); err != nil {
			return err
		}
		if command.Purpose == "red" {
			if *command.ExitCode == 0 || info.Size() == 0 {
				return errors.New("RED evidence requires a nonzero exit and nonempty output")
			}
			red = true
		}
		if command.Purpose == "verification" {
			if ready && *command.ExitCode != 0 {
				return errors.New("ready/approved result has failed verification")
			}
			verified = verified || *command.ExitCode == 0
		}
	}
	if ready && !verified {
		return errors.New("ready/approved result requires successful verification evidence")
	}
	if r.Role == "implementer" && ready && *r.Behavioral && !red {
		return errors.New("behavioral implementation requires RED evidence")
	}
	return nil
}

func validHex(value string, lengths ...int) bool {
	_, err := hex.DecodeString(value)
	return err == nil && slices.Contains(lengths, len(value))
}

package controlplane

import (
	"fmt"
	"strings"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Bounded invariant constants for control-plane evidence. These keep stored
// summaries and scope maps bounded so a malicious or runaway source cannot
// inflate event records (requirement 4.7, 4.8, performance & scalability).
const (
	MaxSummaryBytes    = 4096
	MaxScopeMapEntries = 64
	MaxScopeMapValue   = 512
	MaxSourceNameLen   = 128
)

// unsafeSummarySubstrings marks summary content that must never be stored in
// a control-plane event record (requirement 4.4, 4.5). Matching is
// case-insensitive on the ASCII summary.
var unsafeSummarySubstrings = []string{
	"bearer ",
	"api key",
	"api-key",
	"api_key",
	"apikey:",
	"apikey=",
	"oauth ",
	"authorization:",
	"authorization=",
	"resume token",
	"resume_token",
	"secret:",
	"secret=",
	"password:",
	"password=",
}

// ValidateEvent performs the core-level invariant checks for an Event before
// it can be persisted or returned (requirement 1.7, 1.8, 3.4, 3.5, 3.6, 4.4,
// 4.5, 4.6, 4.7, 9.4). It delegates structural checks to the SDK Event.Validate
// and adds core-owned scope bounding and unsafe-content rejection.
func ValidateEvent(ev cp.Event) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	if err := validateSource(ev.Source); err != nil {
		return err
	}
	if err := validateSummary(ev.Summary); err != nil {
		return err
	}
	if err := validateScopeSnapshot(ev.Scope); err != nil {
		return err
	}
	return nil
}

func validateSource(src cp.SourceRef) error {
	if strings.TrimSpace(src.Name) == "" {
		return fmt.Errorf("controlplane event: source.name is required")
	}
	if len(src.Name) > MaxSourceNameLen {
		return fmt.Errorf("controlplane event: source.name exceeds %d bytes", MaxSourceNameLen)
	}
	return nil
}

func validateSummary(summary string) error {
	if len(summary) > MaxSummaryBytes {
		return fmt.Errorf("controlplane event: summary exceeds %d bytes", MaxSummaryBytes)
	}
	if summary == "" {
		return nil
	}
	low := strings.ToLower(summary)
	for _, bad := range unsafeSummarySubstrings {
		if strings.Contains(low, bad) {
			return fmt.Errorf("controlplane event: summary contains unsafe token-like content")
		}
	}
	return nil
}

func validateScopeSnapshot(snap cp.ScopeSnapshot) error {
	if err := boundedSlice(snap.Principal.Roles, "roles"); err != nil {
		return err
	}
	if err := boundedMap(snap.Principal.SafeClaims, "safe_claims"); err != nil {
		return err
	}
	if err := boundedMap(snap.Principal.PolicyLabels, "policy_labels"); err != nil {
		return err
	}
	return nil
}

func boundedMap(m map[string]string, label string) error {
	if len(m) > MaxScopeMapEntries {
		return fmt.Errorf("controlplane event: scope.%s exceeds %d entries", label, MaxScopeMapEntries)
	}
	for k, v := range m {
		if len(k) > MaxScopeMapValue || len(v) > MaxScopeMapValue {
			return fmt.Errorf("controlplane event: scope.%s entry exceeds %d bytes", label, MaxScopeMapValue)
		}
	}
	return nil
}

func boundedSlice(s []string, label string) error {
	if len(s) > MaxScopeMapEntries {
		return fmt.Errorf("controlplane event: scope.%s exceeds %d entries", label, MaxScopeMapEntries)
	}
	for _, v := range s {
		if len(v) > MaxScopeMapValue {
			return fmt.Errorf("controlplane event: scope.%s entry exceeds %d bytes", label, MaxScopeMapValue)
		}
	}
	return nil
}

package runtimehost

import "fmt"

// RetentionCategoryBudget is the safe retention-pressure category when the
// retained-generation budget would be exceeded (req 10.8-10.10).
const RetentionCategoryBudget = "retained_generation_budget"

// RetentionPressure is a bounded diagnostics snapshot for retention admission
// (req 10.10). It never includes request content, credentials, or paths.
type RetentionPressure struct {
	MaxRetained       int
	Retained          int
	BlockingCategory  string
	WouldBlockPublish bool
}

// RetentionPressure returns the current retained-generation budget posture.
func (m *Manager) RetentionPressure() RetentionPressure {
	if m == nil {
		return RetentionPressure{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	retained := len(m.retained)
	out := RetentionPressure{
		MaxRetained: m.maxRetained,
		Retained:    retained,
	}
	if retained >= m.maxRetained && m.active.Load() != nil {
		out.WouldBlockPublish = true
		out.BlockingCategory = RetentionCategoryBudget
	}
	return out
}

// LifecycleOutcome is a stable post-commit retirement status category
// (design Error Handling; req 10.12, 13.5, 14.1).
type LifecycleOutcome string

const (
	LifecycleOutcomeOK            LifecycleOutcome = ""
	LifecycleOutcomeQuiesceFailed LifecycleOutcome = "quiesce_failed"
	LifecycleOutcomeCleanupFailed LifecycleOutcome = "cleanup_failed"
)

// RetirementStatus is a bounded snapshot of the most recent retirement attempt.
type RetirementStatus struct {
	GenerationID int64
	Outcome      LifecycleOutcome
	Attempts     int
	Err          error
}

// CleanupPolicy bounds post-drain cleanup retries (req 10.12; design Closing→Closing).
// MaxAttempts <= 0 defaults to DefaultCleanupMaxAttempts.
type CleanupPolicy struct {
	MaxAttempts int
}

// DefaultCleanupMaxAttempts is the startup-fixed cleanup retry budget.
const DefaultCleanupMaxAttempts = 3

func (p CleanupPolicy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return DefaultCleanupMaxAttempts
	}
	return p.MaxAttempts
}

func panicError(stage string, recovered any) error {
	return fmt.Errorf("runtimehost: %s panic: %v", stage, recovered)
}

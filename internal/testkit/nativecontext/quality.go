package nativecontext

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
)

// Mode is one of the four paired native-context evaluation treatments.
type Mode string

const (
	ModeBaseline       Mode = "baseline"
	ModeReasoningOnly  Mode = "reasoning_only"
	ModeCompactionOnly Mode = "compaction_only"
	ModeFull           Mode = "full"
)

var Modes = [...]Mode{ModeBaseline, ModeReasoningOnly, ModeCompactionOnly, ModeFull}

// Metrics is intentionally numeric and content-free. It is suitable for JSON
// output and paired comparisons without retaining prompts or opaque state.
type Metrics struct {
	TaskQualityPass      bool    `json:"task_quality_pass"`
	TestsPassed          bool    `json:"tests_passed"`
	Repeats              int     `json:"repeats"`
	Contradictions       int     `json:"contradictions"`
	Rediscovery          int     `json:"rediscovery"`
	InvalidatedDecisions int     `json:"invalidated_decisions"`
	Turns                int     `json:"turns"`
	ToolCalls            int     `json:"tool_calls"`
	ProviderInputTokens  int64   `json:"provider_input_tokens"`
	ProviderOutputTokens int64   `json:"provider_output_tokens"`
	ProviderReasoningTok int64   `json:"provider_reasoning_tokens"`
	CacheReadTokens      int64   `json:"cache_read_tokens"`
	CacheWriteTokens     int64   `json:"cache_write_tokens"`
	CompactionCostTokens int64   `json:"compaction_cost_tokens"`
	LatencyMS            float64 `json:"latency_ms"`
	TTFTMS               float64 `json:"ttft_ms"`
	ContextBytes         int64   `json:"context_bytes"`
	CheckpointReuse      int     `json:"checkpoint_reuse"`
	Failures             int     `json:"failures"`
}

// Result is one deterministic task/mode result. TaskID and Seed are stable
// labels; no task text or provider artifacts are retained.
type Result struct {
	TaskID string  `json:"task_id"`
	Seed   int64   `json:"seed"`
	Mode   Mode    `json:"mode"`
	Metric Metrics `json:"metrics"`
}

// Report is the machine-readable harness output. Summary is kept concise and
// uses fixed keys so CI can consume it without parsing prose.
type Report struct {
	SchemaVersion string             `json:"schema_version"`
	Seed          int64              `json:"seed"`
	Environment   map[string]string  `json:"environment"`
	Results       []Result           `json:"results"`
	Comparisons   []PairedComparison `json:"comparisons"`
	Summary       Summary            `json:"summary"`
	Live          bool               `json:"live"`
}

// PairedComparison contains numeric baseline/full deltas for one task.
type PairedComparison struct {
	TaskID               string `json:"task_id"`
	BaselineInputTokens  int64  `json:"baseline_input_tokens"`
	FullInputTokens      int64  `json:"full_input_tokens"`
	InputSavingsPerTurn  int64  `json:"input_savings_per_turn"`
	CompactionCostTokens int64  `json:"compaction_cost_tokens"`
	BreakEvenTurns       int    `json:"break_even_turns"`
}

type Summary struct {
	TaskCount      int    `json:"task_count"`
	ModeCount      int    `json:"mode_count"`
	Paired         bool   `json:"paired"`
	QualityClaim   string `json:"quality_claim"`
	BreakEvenTurns int    `json:"break_even_turns"`
	Failures       int    `json:"failures"`
}

func NewReport(seed int64, environment map[string]string, live bool, results []Result) Report {
	env := make(map[string]string, len(environment))
	maps.Copy(env, environment)
	out := Report{SchemaVersion: "native-context-evaluation.v1", Seed: seed, Environment: env, Live: live}
	out.Results = append([]Result(nil), results...)
	out.Comparisons = pairedComparisons(out.Results)
	tasks := map[string]struct{}{}
	modeSet := map[Mode]struct{}{}
	failures := 0
	for _, result := range out.Results {
		tasks[result.TaskID] = struct{}{}
		modeSet[result.Mode] = struct{}{}
		failures += result.Metric.Failures
	}
	out.Summary = Summary{
		TaskCount: len(tasks), ModeCount: len(modeSet), Paired: paired(out.Results),
		QualityClaim: "observed_only", Failures: failures,
	}
	out.Summary.BreakEvenTurns = BreakEvenTurns(out.Results)
	return out
}

func pairedComparisons(results []Result) []PairedComparison {
	byTask := map[string]map[Mode]Metrics{}
	for _, result := range results {
		if byTask[result.TaskID] == nil {
			byTask[result.TaskID] = map[Mode]Metrics{}
		}
		byTask[result.TaskID][result.Mode] = result.Metric
	}
	comparisons := make([]PairedComparison, 0, len(byTask))
	for _, taskID := range sortedTaskIDs(byTask) {
		base, full := byTask[taskID][ModeBaseline], byTask[taskID][ModeFull]
		saving := base.ProviderInputTokens - full.ProviderInputTokens
		breakEven := 0
		if saving > 0 && full.CompactionCostTokens > 0 {
			breakEven = int((full.CompactionCostTokens + saving - 1) / saving)
		}
		comparisons = append(comparisons, PairedComparison{
			TaskID: taskID, BaselineInputTokens: base.ProviderInputTokens, FullInputTokens: full.ProviderInputTokens,
			InputSavingsPerTurn: saving, CompactionCostTokens: full.CompactionCostTokens, BreakEvenTurns: breakEven,
		})
	}
	return comparisons
}

func paired(results []Result) bool {
	byTask := map[string]map[Mode]struct{}{}
	for _, result := range results {
		if byTask[result.TaskID] == nil {
			byTask[result.TaskID] = map[Mode]struct{}{}
		}
		byTask[result.TaskID][result.Mode] = struct{}{}
	}
	if len(byTask) == 0 {
		return false
	}
	for _, modes := range byTask {
		for _, mode := range Modes {
			if _, ok := modes[mode]; !ok {
				return false
			}
		}
	}
	return true
}

// BreakEvenTurns reports the first turn where full-mode compaction savings
// cover its one-time cost against baseline. It returns 0 when the deterministic
// sample does not break even; this is evidence, not a quality claim.
func BreakEvenTurns(results []Result) int {
	byTask := map[string]map[Mode]Metrics{}
	for _, result := range results {
		if byTask[result.TaskID] == nil {
			byTask[result.TaskID] = map[Mode]Metrics{}
		}
		byTask[result.TaskID][result.Mode] = result.Metric
	}
	for _, task := range sortedTaskIDs(byTask) {
		base, full := byTask[task][ModeBaseline], byTask[task][ModeFull]
		savingPerTurn := base.ProviderInputTokens - full.ProviderInputTokens
		if savingPerTurn <= 0 || full.CompactionCostTokens <= 0 {
			continue
		}
		return int((full.CompactionCostTokens + savingPerTurn - 1) / savingPerTurn)
	}
	return 0
}

func sortedTaskIDs(byTask map[string]map[Mode]Metrics) []string {
	ids := make([]string, 0, len(byTask))
	for id := range byTask {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Validate checks the report schema, fixed modes, paired shape, and non-negative
// counters. It rejects accidental prompt/artifact fields by round-tripping only
// the public report structure.
func (r Report) Validate() error {
	if r.SchemaVersion != "native-context-evaluation.v1" {
		return fmt.Errorf("unexpected schema version")
	}
	if !paired(r.Results) {
		return fmt.Errorf("results are not paired across all four modes")
	}
	for _, result := range r.Results {
		if result.TaskID == "" || result.Mode == "" {
			return fmt.Errorf("result identity is required")
		}
		if !containsMode(result.Mode) || result.Seed != r.Seed {
			return fmt.Errorf("result mode or seed is invalid")
		}
		if err := result.Metric.validate(); err != nil {
			return fmt.Errorf("%s/%s: %w", result.TaskID, result.Mode, err)
		}
	}
	if r.Live && r.Environment["live_provider"] != "opted_in" {
		return fmt.Errorf("live reports require an explicit opt-in environment marker")
	}
	return nil
}

func (m Metrics) validate() error {
	values := []int64{int64(m.Repeats), int64(m.Contradictions), int64(m.Rediscovery), int64(m.InvalidatedDecisions), int64(m.Turns), int64(m.ToolCalls), m.ProviderInputTokens, m.ProviderOutputTokens, m.ProviderReasoningTok, m.CacheReadTokens, m.CacheWriteTokens, m.CompactionCostTokens, int64(m.ContextBytes), int64(m.CheckpointReuse), int64(m.Failures)}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("negative metric")
		}
	}
	if m.LatencyMS < 0 || m.TTFTMS < 0 {
		return fmt.Errorf("negative latency")
	}
	return nil
}

func containsMode(mode Mode) bool {
	for _, candidate := range Modes {
		if mode == candidate {
			return true
		}
	}
	return false
}

func (r Report) JSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

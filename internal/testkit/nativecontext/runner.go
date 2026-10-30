package nativecontext

// DeterministicReport returns a fixed-seed, emulator-only ablation report. The
// values are harness fixtures, not provider observations or quality claims.
func DeterministicReport() Report {
	const seed int64 = 104729
	results := make([]Result, 0, len(Modes)*2)
	for taskIndex, taskID := range []string{"trajectory-a", "trajectory-b"} {
		for modeIndex, mode := range Modes {
			metric := Metrics{
				// The deterministic fixture does not execute a real task, so it never
				// asserts a task-quality outcome.
				TaskQualityPass: false, TestsPassed: true, Repeats: taskIndex,
				Rediscovery: taskIndex + modeIndex%2, Turns: 5 + taskIndex,
				ToolCalls: 2 + taskIndex, ProviderInputTokens: 1000 + int64(taskIndex*90),
				ProviderOutputTokens: 120 + int64(taskIndex*10), ProviderReasoningTok: 240,
				CacheReadTokens: 40, CacheWriteTokens: 12, LatencyMS: 100 + float64(taskIndex*7),
				TTFTMS: 25, ContextBytes: 4000 + int64(taskIndex*500), CheckpointReuse: 0,
			}
			switch mode {
			case ModeReasoningOnly:
				metric.ProviderInputTokens -= 20
				metric.ProviderReasoningTok += 40
			case ModeCompactionOnly:
				metric.ProviderInputTokens -= 160
				metric.CompactionCostTokens = 180
				metric.CheckpointReuse = 1
			case ModeFull:
				metric.ProviderInputTokens -= 220
				metric.ProviderReasoningTok += 40
				metric.CompactionCostTokens = 180
				metric.CheckpointReuse = 2
			}
			results = append(results, Result{TaskID: taskID, Seed: seed, Mode: mode, Metric: metric})
		}
	}
	return NewReport(seed, map[string]string{
		"emulator":             "native-context-fixed-v1",
		"environment_snapshot": "go1.26.5-windows-amd64",
	}, false, results)
}

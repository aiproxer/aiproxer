package interleavedthinking

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultInstructions is the built-in thinker prompt used when instructions_file is empty.
const DefaultInstructions = `You now become a thinker.

You receive the full conversation history for this turn. Produce a **compact** planning memo which will be provided for the next executor model.

Your memo is injected into the main session model's context, so every word you write competes with the user's actual work for the model's attention. The default policy is to NOT steer. Produce steering only when it is genuinely important and has a real chance of significantly improving the outcome of the main session.

Never produce:
- nitpicks, style opinions, or cosmetic suggestions;
- statements of the obvious, or restatements of what has already happened;
- generic advice the executor would arrive at on its own;
- filler written just to produce a memo.

If nothing qualifies, respond with nothing at all. An empty response is a valid and expected answer: it means no steering is needed. Do not write just for the sake of writing.

When you do steer, reflect on the progress of the session, current status of the task at hand, what was already achieved, what errors were made and need correction and the suggested very next steps.

Do not ask the user questions. This step of session is non interactive. Do not try to call tools. Do not try to execute the task or produce final user-facing work unless a tiny illustrative example is necessary to clarify the plan for the next steps.

Focus on:
- the user's main goal,
- what has already happened,
- current constraints, risks, and open assumptions,
- up to three plausible next actions,
- the single best next action and why it is better.

Please think out loud. This is important to provide the execution model with proper understanding of your reasoning. Provide concise, actionable planning context and a short rationale. Keep it brief with high signal/noise ratio.

When you have enough context to produce the memo, mark your response with a final structured markdown section:

## Session Steering Memo
- **Goal**: {goal_here}
- **Current state**: {current state}
- **Constraints and risks**: {constraints_and_risks}
- **Considered next steps**:
  1. {step_option_no1}
  2. {step_option_no2}
  3. {step_option_no3}
- **Recommended next step**: {recommended_next_step}
- **Reason**: {reason}`

// ResolveInstructions returns the thinker instructions text. When path is non-empty,
// it validates the path and reads the file bounded by maxBytes. When path is empty,
// inline is returned if non-empty, otherwise DefaultInstructions is returned.
func ResolveInstructions(baseDir string, path string, inline string) (string, error) {
	if strings.TrimSpace(inline) != "" {
		return strings.TrimSpace(inline), nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return DefaultInstructions, nil
	}
	resolved, err := resolveInstructionsPath(baseDir, path)
	if err != nil {
		return "", err
	}
	b, err := readInstructionsFile(resolved, DefaultMaxInstructionsBytes)
	if err != nil {
		return "", fmt.Errorf("interleavedthinking: instructions_file: %w", err)
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return "", fmt.Errorf("interleavedthinking: instructions_file is empty")
	}
	return content, nil
}

func resolveInstructionsPath(baseDir string, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("interleavedthinking: instructions_file: empty path")
	}
	if strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("interleavedthinking: instructions_file must not contain NUL")
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("interleavedthinking: instructions_file: getwd: %w", err)
		}
		base = wd
	}
	absBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", fmt.Errorf("interleavedthinking: instructions_file: resolve base: %w", err)
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absBase, candidate)
	}
	absPath, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("interleavedthinking: instructions_file: resolve path: %w", err)
	}
	if err := ensurePathUnderBase(absBase, absPath, raw); err != nil {
		return "", err
	}
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			return absPath, nil
		}
		return "", fmt.Errorf("interleavedthinking: instructions_file: stat: %w", err)
	}
	resolvedBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("interleavedthinking: instructions_file: resolve base directory symlinks: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("interleavedthinking: instructions_file: resolve path symlinks: %w", err)
	}
	if err := ensurePathUnderBase(resolvedBase, resolvedPath, raw); err != nil {
		return "", err
	}
	return resolvedPath, nil
}

func ensurePathUnderBase(absBase, absPath, label string) error {
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return fmt.Errorf("interleavedthinking: instructions_file: path outside base directory")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("interleavedthinking: instructions_file: path %q escapes base directory", label)
	}
	return nil
}

func readInstructionsFile(path string, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxInstructionsBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return b, nil
}

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// ModelInfo is one model advertised by an ACP agent in a session/new response
// (schema: models.availableModels[] with modelId, name, and optional
// description). Agents that do not report models omit the field.
type ModelInfo struct {
	ModelID     string
	Name        string
	Description string
}

// SessionModels is the model state of an ACP session/new response
// (schema: models.availableModels + models.currentModelId).
type SessionModels struct {
	Available []ModelInfo
	CurrentID string
}

// parseSessionModels extracts the models state from a decoded session/new
// result body. Missing or malformed models state yields an empty SessionModels;
// entries without a modelId are skipped.
func parseSessionModels(result json.RawMessage) SessionModels {
	var out struct {
		Models *struct {
			Available []struct {
				ModelID     string `json:"modelId"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableModels"`
			CurrentID string `json:"currentModelId"`
		} `json:"models"`
	}
	if len(result) == 0 || json.Unmarshal(result, &out) != nil || out.Models == nil {
		return SessionModels{}
	}
	models := SessionModels{CurrentID: out.Models.CurrentID}
	for _, m := range out.Models.Available {
		if m.ModelID == "" {
			continue
		}
		models.Available = append(models.Available, ModelInfo{
			ModelID:     m.ModelID,
			Name:        m.Name,
			Description: m.Description,
		})
	}
	if models.Available == nil {
		models.Available = []ModelInfo{}
	}
	return models
}

// DiscoverSessionModels spawns cmd one-shot via starter, runs the
// initialize/session-new handshake, and returns the models advertised by the
// agent. No prompt is sent. A nil starter uses OSProcessStarter.
//
// Successful discovery is authoritative about what the agent reports; agents
// that omit models yield an empty SessionModels without error. All failures
// (spawn, handshake, cancellation) are explicit errors. Callers preserve
// context.Canceled/DeadlineExceeded via errors.Is.
func DiscoverSessionModels(ctx context.Context, cmd []string, cwd string, env []string, starter ProcessStarter, hp HandshakeProfile) (SessionModels, error) {
	if ctx == nil {
		return SessionModels{}, fmt.Errorf("acp: discover session models: nil context")
	}
	if err := ctx.Err(); err != nil {
		return SessionModels{}, err
	}
	if len(cmd) == 0 {
		return SessionModels{}, fmt.Errorf("acp: discover session models: empty command")
	}
	if starter == nil {
		starter = OSProcessStarter{}
	}
	proc, err := starter.Start(cmd, cwd, env)
	if err != nil {
		return SessionModels{}, fmt.Errorf("acp: discover session models: spawn: %w", err)
	}
	t := NewStdioTransport(proc, slog.Default())
	defer func() { _ = t.Close() }()
	cli := newClientFromTransport(t, slog.Default())
	if err := runHandshake(ctx, cli, hp); err != nil {
		if ctx.Err() != nil {
			return SessionModels{}, ctx.Err()
		}
		return SessionModels{}, fmt.Errorf("acp: discover session models: handshake: %w", err)
	}
	_, raw, err := cli.sessionNewResult(ctx, hp)
	if err != nil {
		if ctx.Err() != nil {
			return SessionModels{}, ctx.Err()
		}
		return SessionModels{}, fmt.Errorf("acp: discover session models: session/new: %w", err)
	}
	return parseSessionModels(raw), nil
}

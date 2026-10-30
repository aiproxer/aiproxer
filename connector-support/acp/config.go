package acp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Config configures the ACP HTTP prompt-turn client (JSON-RPC over POST /v1/acp).
// HTTPClient is required; callers own dial/TLS/idle policy.
type Config struct {
	BaseURL    string
	HTTPClient *http.Client

	Handshake     HandshakeProfile
	Cancel        CancelProfile
	SessionUpdate SessionUpdateMapperOptions
	ServerRequest ServerRequestHandler
	Log           *slog.Logger
}

// OpenHTTPPrompt runs handshake/session/prompt against an ACP HTTP endpoint and
// returns a managed NDJSON event stream. It does not depend on host routing types.
func OpenHTTPPrompt(ctx context.Context, cfg Config, call aipapi.Call) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s: %w", ID, aipapi.ErrNilContext)
	}
	if cfg.HTTPClient == nil {
		return nil, fmt.Errorf("%s: HTTPClient is required (caller-owned policy)", ID)
	}
	cli, err := newClient(cfg.BaseURL, cfg.HTTPClient, cfg.Log)
	if err != nil {
		return nil, err
	}
	mapper := mergeMapperOptions(cfg)
	cancelProf := mergeCancelProfile(cfg)
	if err := validateACPCall(&call); err != nil {
		return nil, err
	}
	callPtr := &call
	hp := mergeHandshakeProfile(cfg, callPtr)
	if err := runHandshake(ctx, cli, hp); err != nil {
		return nil, classifyPreOutputError(err)
	}
	sid, err := resolveSessionID(ctx, cli, callPtr, hp)
	if err != nil {
		return nil, classifyPreOutputError(err)
	}
	blocks, err := promptBlocksForCall(callPtr)
	if err != nil {
		return nil, err
	}
	msgID := messageIDForCall(callPtr)
	params := buildPromptParams(sid, blocks, msgID)
	rpcID := cli.rpcID()
	body, err := cli.sessionPrompt(ctx, params, rpcID)
	if err != nil {
		return nil, classifyPreOutputError(err)
	}
	return newPromptNDJSONStream(ctx, body, cli, sid, rpcID, msgID, mapper, cfg.ServerRequest, cancelProf, call.MaxPendingWireEvents), nil
}

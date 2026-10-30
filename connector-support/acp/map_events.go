package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// promptStream maps ACP prompt NDJSON lines to aipapi.EventStream. It embeds
// NDJSONStreamBase for shared scanner/body/pending/framing/EOF mechanics and
// supplies the ACP-specific NDJSONStreamStrategy via receiver methods.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently
// with Recv blocked on scanner.Scan or network I/O; Close cancels the stream
// context and closes the response body so Scan unblocks.
// Context: Scan does not observe the per-Recv ctx; cancellation of that ctx
// alone does not unblock a blocked Scan—use Close (or parent cancellation that
// closes the body). See [aipapi.EventStream] cancellation notes.
//
// Context: ctx/cancel are derived from the Open parent via WithCancel and live
// for the stream lifetime (cancel on Close). Recv passes its per-call ctx to
// inbound server requests and JSON-RPC; line parsing uses the stream ctx so
// trace values from the parent propagate and cancellation aligns with
// Close/parent rather than an arbitrary Recv deadline.
type promptStream struct {
	*NDJSONStreamBase

	cli         *client
	sessionID   string
	promptRPCID int64
	messageID   string

	mapper SessionUpdateMapperOptions
	srv    ServerRequestHandler

	cancelProfile CancelProfile
}

func newPromptNDJSONStream(
	parent context.Context,
	body io.ReadCloser,
	cli *client,
	sessionID string,
	promptRPCID int64,
	messageID string,
	mapper SessionUpdateMapperOptions,
	srv ServerRequestHandler,
	cancelProfile CancelProfile,
	maxPending int,
) *promptStream {
	s := &promptStream{
		cli:           cli,
		sessionID:     sessionID,
		promptRPCID:   promptRPCID,
		messageID:     messageID,
		mapper:        mapper,
		srv:           serverHandlerOrDefault(srv),
		cancelProfile: cancelProfile,
	}
	s.NDJSONStreamBase = NewNDJSONStreamBase(parent, body, maxPending, s)
	return s
}

// Label implements NDJSONStreamStrategy.
func (s *promptStream) Label() string { return "acp" }

// IsServerRequest implements NDJSONStreamStrategy: ACP excludes session/update.
func (s *promptStream) IsServerRequest(probe map[string]any) bool {
	return isInboundServerRequest(probe)
}

// HandleServerRequest implements NDJSONStreamStrategy. On handler failure it
// sends a JSON-RPC -32601 error response to the agent and returns nil so the
// stream continues; send/encode failures return a wrapped error to terminate.
func (s *promptStream) HandleServerRequest(ctx context.Context, probe map[string]any) error {
	if err := s.handleInboundServerRequest(ctx, probe); err != nil {
		return fmt.Errorf("acp: handle inbound server request: %w", err)
	}
	return nil
}

// MapLine implements NDJSONStreamStrategy. ACP lines (session/update
// notifications, terminal prompt results, errors) are mapped via
// parseNDJSONLine using the stream context.
func (s *promptStream) MapLine(ctx context.Context, line string, _ map[string]any) ([]aipapi.Event, error) {
	evs, err := parseNDJSONLine(ctx, s.mapper, line, s.promptRPCID)
	if err != nil {
		return nil, fmt.Errorf("acp: parse NDJSON line: %w", err)
	}
	return evs, nil
}

// OnCancel implements NDJSONStreamStrategy: send a best-effort cancel-session
// RPC. Does not cancel the stream context (Close handles that).
func (s *promptStream) OnCancel() {
	s.signalCancel()
}

func (s *promptStream) handleInboundServerRequest(ctx context.Context, probe map[string]any) error {
	method, idBytes, paramsRaw, dropped, err := ExtractServerRequestProbe("acp", probe)
	if err != nil {
		return err
	}
	if dropped {
		return nil
	}
	res, err := s.srv.HandleServerRequest(ctx, method, idBytes, paramsRaw)
	if err != nil {
		// Send a JSON-RPC -32601 method-not-found response for an unhandled
		// method instead of terminating the stream.
		errBody, encErr := replyServerRequestErrorJSON(idBytes, -32601, err.Error())
		if encErr != nil {
			return fmt.Errorf("acp: encode inbound server error response: %w", encErr)
		}
		if sendErr := s.cli.t.SendJSONRPC(ctx, errBody); sendErr != nil {
			return fmt.Errorf("acp: send inbound server error response: %w", sendErr)
		}
		return nil
	}
	body, err := replyServerRequestJSON(idBytes, res)
	if err != nil {
		return fmt.Errorf("acp: encode inbound server response: %w", err)
	}
	if err := s.cli.t.SendJSONRPC(ctx, body); err != nil {
		return fmt.Errorf("acp: send inbound server response: %w", err)
	}
	return nil
}

func (s *promptStream) signalCancel() {
	// WithoutCancel(s.ctx): the consumer ctx passed to Recv is already canceled when we run;
	// we still need a short cancel RPC to complete even if the stream ctx is canceled later.
	// Values from the stream ctx (e.g. trace IDs) are preserved for the outbound request.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(s.StreamContext()), 2*time.Second)
	defer cancel()
	if err := s.cli.cancelSession(cctx, s.cancelProfile, s.sessionID, s.promptRPCID, s.messageID); err != nil {
		if log := s.cli.log; log != nil {
			log.Debug("acp: cancel session rpc failed", slog.String("error", truncErrDetail(err, 512)))
		}
	}
}

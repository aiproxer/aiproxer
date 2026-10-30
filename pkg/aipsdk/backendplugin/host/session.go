// Package host provides the supported public construction path for executable
// backend-plugin contract clients.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ProtocolViolationError preserves the distinction between malformed protocol
// frames and transport failures for internal host adapters.
type ProtocolViolationError struct{ Err error }

func (e *ProtocolViolationError) Error() string { return e.Err.Error() }
func (e *ProtocolViolationError) Unwrap() error { return e.Err }

// Session is the public host-facing configured connector boundary.
type Session struct {
	client     backendpluginv1.BackendPluginClient
	conn       *grpc.ClientConn
	instanceID string

	negotiation backendplugin.Negotiation
	// execMu serializes Execute calls on one session: a pooled session is
	// shared across retained old-generation and new-generation execution, so
	// a second Execute must not enter while the first is held. It is held
	// across the RPC, but Close never takes it: Close waits for the active
	// stream to drain while observing its own context, so a held Execute can
	// no longer wedge Close past the caller's deadline.
	execMu sync.Mutex
	// lifecycleMu is intentionally short-lived: it only guards the lifecycle
	// registration below, never an in-flight RPC.
	lifecycleMu sync.Mutex
	lifecycle   *sessionLifecycle
	closeMu     sync.Mutex
	closed      bool
	connClosed  bool
}

// sessionLifecycle tracks in-flight Execute calls so Close can wait for the
// active stream to drain without holding a mutex across the RPC. A Close whose
// context expires first cancels the in-flight executes and tears down the
// transport instead of waiting forever; the plugin instance itself is left to
// a retrying Close because the server side keeps CloseInstance retryable.
type sessionLifecycle struct {
	changed   chan struct{}
	inflight  int
	cancels   map[*context.CancelFunc]struct{}
	cancelled bool
}

func newSessionLifecycle() *sessionLifecycle {
	return &sessionLifecycle{changed: make(chan struct{}), cancels: make(map[*context.CancelFunc]struct{})}
}

// ensureLifecycle initializes the execute registry for sessions built outside
// the constructors. Both constructors initialize it eagerly; this only covers
// direct struct literals.
func (s *Session) ensureLifecycle() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycle == nil {
		s.lifecycle = newSessionLifecycle()
	}
}

// awaitDrain blocks until no Execute is in flight, the context expires, or the
// stream's own world ends. It never holds the session mutex while waiting.
func (s *Session) awaitDrain(ctx context.Context) error {
	for {
		// Cancellation is also a local disposal request for an idle session:
		// do not fall through to an RPC against an already-terminated plugin.
		if err := ctx.Err(); err != nil {
			return err
		}
		s.lifecycleMu.Lock()
		empty := s.lifecycle.inflight == 0
		changed := s.lifecycle.changed
		s.lifecycleMu.Unlock()
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// cancelInflight cancels every registered in-flight Execute so a Close whose
// context expired can still release the transport. Later Close calls observe
// the cancelled executes draining and may proceed to CloseInstance.
func (s *Session) cancelInflight() {
	s.ensureLifecycle()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycle.cancelled {
		return
	}
	s.lifecycle.cancelled = true
	for cancel := range s.lifecycle.cancels {
		(*cancel)()
	}
}

// NewSession binds an already-created gRPC client to a host session. It is
// intended for standard-distribution composition tests; connector authors
// should normally use DialConfiguredSession.
func NewSessionForTesting(client backendpluginv1.BackendPluginClient, conn *grpc.ClientConn, instanceID string, negotiation backendplugin.Negotiation) *Session {
	return &Session{
		client: client, conn: conn, instanceID: instanceID,
		negotiation: negotiation,
		lifecycle:   newSessionLifecycle(),
	}
}

// DialConfiguredSession negotiates, configures, and resolves a plugin over an
// already authenticated connection. The caller owns conn until Close.
func DialConfiguredSession(ctx context.Context, conn net.Conn, instanceID, factoryKind string, configYAML []byte, secrets backendplugin.SecretBundle, policy backendplugin.RuntimePolicy) (*Session, backendplugin.ResolvedProfile, error) {
	if conn == nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: nil conn")
	}
	policy.DisableTransportRetries = true
	var once sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var out net.Conn
		err := net.ErrClosed
		once.Do(func() { out, err = conn, nil })
		if out == nil {
			return nil, err
		}
		return out, nil
	}
	gc, err := grpc.NewClient("passthrough:///backendplugin", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: grpc dial: %w", err)
	}
	cleanup := func() { _ = gc.Close() }
	client := backendpluginv1.NewBackendPluginClient(gc)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorReasoningEffortValues,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureAccountingEvidenceV2},
			{Name: backendplugin.FeatureSemanticExtensions},
			{Name: backendplugin.FeaturePromptCacheResidency},
			{Name: backendplugin.FeatureCancellationHandshake},
			{Name: backendplugin.FeatureReasoningEffortValues},
			// The host-bound economic evidence contract is additive and never
			// required: a peer that predates it keeps negotiating the older
			// minor, and only a peer that opts in has its claim bound into the
			// host namespace.
			{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2},
		},
		DisableTransportRetries: true,
	}
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: offer.Minor,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureAccountingEvidenceV2},
			{Name: backendplugin.FeatureSemanticExtensions},
			{Name: backendplugin.FeaturePromptCacheResidency},
			{Name: backendplugin.FeatureCancellationHandshake},
			{Name: backendplugin.FeatureReasoningEffortValues},
			// The requested feature set must mirror the offer exactly: the
			// negotiated feature set is the intersection of the host's and the
			// plugin's features, so a feature the host does not request is
			// never enabled and the contract stays unreachable.
			{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2},
		},
		DisableTransportRetries: true,
	})
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate: %w", err)
	}
	if !neg.GetCompatible() {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate incompatible: %s", neg.GetRejectReason())
	}
	negotiated, err := backendplugin.NegotiationFromNegotiateResponse(neg)
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate response: %w", err)
	}
	if err := backendplugin.ValidateNegotiationResult(offer, negotiated); err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate response validation: %w", err)
	}
	_, err = client.Configure(ctx, backendplugin.ConfigureRequestToProto(backendplugin.ConfigureRequest{
		InstanceID: instanceID, FactoryKind: factoryKind, ConfigYAML: configYAML,
		Secrets: secrets, RuntimePolicy: policy, NegotiationToken: neg.GetNegotiationToken(),
	}))
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: configure: %w", err)
	}
	negotiatedEnabled := append([]string(nil), negotiated.EnabledFeatures...)
	slices.Sort(negotiatedEnabled)
	s := &Session{client: client, conn: gc, instanceID: instanceID, lifecycle: newSessionLifecycle(), negotiation: backendplugin.Negotiation{
		Compatible: negotiated.Compatible, NegotiatedMinor: negotiated.NegotiatedMinor,
		EnabledFeatures: negotiatedEnabled, PluginMajor: negotiated.PluginMajor,
		PluginMinor: negotiated.PluginMinor, PluginFeatures: negotiated.PluginFeatures, TransportPolicy: negotiated.TransportPolicy,
	}}
	profile, err := s.Resolve(ctx, nil)
	if err != nil {
		_ = s.Close(ctx)
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: resolve: %w", err)
	}
	return s, profile, nil
}

// Negotiation returns the protocol outcome bound at dial time.
func (s *Session) Negotiation() backendplugin.Negotiation { return s.negotiation }

// Describe returns the plugin's runtime factory descriptor over the
// configured session. Hosts compare it against the packaged manifest at
// activation so a process whose runtime exports disagree with its admission
// metadata fails closed before publication.
func (s *Session) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	resp, err := s.client.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil {
		return backendplugin.PluginDescriptor{}, err
	}
	descriptor, err := backendplugin.PluginDescriptorFromProto(resp.GetDescriptor_())
	if err != nil {
		return backendplugin.PluginDescriptor{}, err
	}
	return descriptor, nil
}

func (s *Session) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	req := &backendpluginv1.ResolveProfileRequest{InstanceId: s.instanceID}
	if modelID != nil {
		req.ModelId = modelID
	}
	resp, err := s.client.ResolveProfile(ctx, req)
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	// Strip unsolicited facts before conversion: an unnegotiated peer's
	// declaration is unproven and may be malformed. Decoding it first would
	// turn unsolicited bytes into a legacy ResolveProfile failure instead of
	// ignoring the unsolicited declaration. The host is the trust boundary
	// for third-party executable plugins.
	if wire := resp.GetProfile(); wire != nil && !backendplugin.ReasoningEffortValuesNegotiated(s.negotiation) {
		wire.ReasoningEffortValues = nil
	}
	profile, err := backendplugin.ResolvedProfileFromProto(resp.GetProfile())
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	// Defense in depth: the converted profile must never carry unnegotiated
	// facts even if a future conversion path preserves them.
	if !backendplugin.ReasoningEffortValuesNegotiated(s.negotiation) {
		profile.ReasoningEffortValues = nil
	}
	return profile, nil
}

func (s *Session) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	resp, err := s.client.ListModels(ctx, &backendpluginv1.ListModelsRequest{InstanceId: s.instanceID, MaxModels: maxModels})
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	return backendplugin.ListModelsResponseFromProto(resp)
}

// Execute forwards the public DTO stream through the gRPC host session.
//
// Executes serialize on one session and with Close through the session
// lifecycle: Close waits for the active stream to drain before tearing down
// the transport, and each Execute registers a cancellation that a
// deadline-bound Close can trigger. The lifecycle mutex is only held for
// registration, never across the RPC; the execMu serialization below is what
// Close deliberately does not take.
func (s *Session) Execute(stream backendplugin.ExecuteStream) error {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return fmt.Errorf("host: session is closed")
	}

	s.execMu.Lock()
	defer s.execMu.Unlock()

	s.ensureLifecycle()
	ctx, cancel := context.WithCancel(stream.Context())
	s.lifecycleMu.Lock()
	if s.lifecycle.cancelled {
		s.lifecycleMu.Unlock()
		cancel()
		return fmt.Errorf("host: session is closed")
	}
	s.lifecycle.inflight++
	s.lifecycle.cancels[&cancel] = struct{}{}
	s.lifecycleMu.Unlock()
	defer func() {
		cancel()
		s.lifecycleMu.Lock()
		s.lifecycle.inflight--
		delete(s.lifecycle.cancels, &cancel)
		notify := s.lifecycle.changed
		s.lifecycle.changed = make(chan struct{})
		s.lifecycleMu.Unlock()
		close(notify)
	}()

	var (
		streamCloseOnce sync.Once
		closer          backendplugin.OptionalExecuteStreamCloser
	)
	if c, ok := stream.(backendplugin.OptionalExecuteStreamCloser); ok {
		closer = c
	}
	closeStream := func() {
		streamCloseOnce.Do(func() {
			if closer != nil {
				_ = closer.Close()
			}
		})
	}
	defer closeStream()

	gs, err := s.client.Execute(ctx)
	if err != nil {
		return mapSessionError(err, false)
	}
	// The gRPC client stream forbids concurrent SendMsg and CloseSend: the
	// input pump below Sends while the receive loop below CloseSends once the
	// terminal frame arrives. Serialize the two so they never overlap; the
	// Once additionally collapses the pump-deferred and terminal CloseSend
	// into a single call with no change to cancellation or retry semantics.
	var sendMu sync.Mutex
	var closeSendOnce sync.Once
	closeSend := func() {
		closeSendOnce.Do(func() {
			sendMu.Lock()
			defer sendMu.Unlock()
			_ = gs.CloseSend()
		})
	}
	errCh := make(chan error, 1)
	var reportOnce sync.Once
	report := func(e error) {
		if e != nil && !errors.Is(e, io.EOF) && ctx.Err() == nil {
			reportOnce.Do(func() { errCh <- e })
		}
	}
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		defer closeSend()
		for {
			frame, recvErr := stream.Recv()
			if recvErr != nil {
				report(recvErr)
				return
			}
			if recvErr = backendplugin.ValidateClientFrameBounds(frame); recvErr != nil {
				report(recvErr)
				return
			}
			msg, convErr := backendplugin.ClientFrameToProto(frame)
			if convErr != nil {
				report(convErr)
				return
			}
			sendMu.Lock()
			sendErr := gs.Send(msg)
			sendMu.Unlock()
			if sendErr != nil {
				report(sendErr)
				return
			}
		}
	}()
	var closeOnce sync.Once
	closePump := func() {
		closeOnce.Do(func() {
			cancel()
			closeStream()
			// Legacy ExecuteStream implementations must observe their own
			// Context, but the host cannot cancel that caller-owned context.
			// Only wait when the optional closer gives us a reliable unblock
			// seam; otherwise return without turning a non-cooperative stream
			// into a host deadlock.
			if closer != nil {
				<-pumpDone
			}
		})
	}
	defer closePump()

	terminal := false
	for {
		msg, recvErr := gs.Recv()
		if recvErr != nil {
			closePump()
			if ctxErr := stream.Context().Err(); ctxErr != nil {
				return ctxErr
			}
			if peer := firstSessionError(errCh); peer != nil {
				return mapSessionError(peer, terminal)
			}
			return mapSessionError(recvErr, terminal)
		}
		frame, convErr := backendplugin.ServerFrameFromProto(msg)
		if convErr != nil {
			closePump()
			return &ProtocolViolationError{Err: convErr}
		}
		if frame.AccountingV2 != nil && !backendplugin.AccountingEvidenceV2Negotiated(s.negotiation) {
			closePump()
			return &ProtocolViolationError{Err: backendplugin.ErrAccountingEvidenceV2Unsupported}
		}
		if terminal {
			closePump()
			return &ProtocolViolationError{Err: fmt.Errorf("session: unexpected server frame after terminal")}
		}
		if frame.Kind == backendplugin.ServerFrameTerminal {
			terminal = true
		}
		if sendErr := stream.Send(frame); sendErr != nil {
			closePump()
			return sendErr
		}
		if terminal {
			closeSend()
		}
	}
}

func (s *Session) Cancel(ctx context.Context, invocation backendplugin.Invocation) error {
	stream := &cancelStream{ctx: ctx, frames: []backendplugin.ClientFrame{{Kind: backendplugin.ClientFrameStart, InstanceID: s.instanceID, Invocation: &invocation}, {Kind: backendplugin.ClientFrameCancel, InstanceID: s.instanceID, CancelReason: backendplugin.CancelReasonClient}}}
	err := s.Execute(stream)
	if !stream.cancelSent {
		return fmt.Errorf("adapter: cancellation frame was not transmitted")
	}
	return err
}

func (s *Session) Close(ctx context.Context) error {
	// Close serializes with active executes: it waits for the in-flight
	// stream to drain before entering the configured instance, so the
	// transport is never torn down underneath a running Execute. Unlike the
	// previous mutex-held design, the wait observes the caller's context: a
	// Close whose deadline expires first cancels the in-flight executes,
	// releases the transport, and returns the context error without closing
	// the plugin instance. The server keeps CloseInstance retryable, so a
	// later Close can still complete the instance close.
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closeMu.Unlock()

	s.ensureLifecycle()
	if err := s.awaitDrain(ctx); err != nil {
		s.cancelInflight()
		s.closeTransport()
		return err
	}
	if _, err := s.client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: s.instanceID}); err != nil {
		return err
	}
	s.closeMu.Lock()
	s.closed = true
	s.closeMu.Unlock()
	s.closeTransport()
	return nil
}

// closeTransport releases the gRPC client connection exactly once. It is safe
// to call while RPCs are in flight: they fail with cancellation errors that
// the session error mapping already handles.
func (s *Session) closeTransport() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.connClosed || s.conn == nil {
		return
	}
	s.connClosed = true
	_ = s.conn.Close()
}

// CountTokens forwards the optional token-counting operation.
func (s *Session) CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	wire, err := backendplugin.CountTokensRequestToProto(req)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	resp, err := s.client.CountTokens(ctx, wire)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	return backendplugin.CountTokensResponseFromProto(resp)
}

// RenewPromptCache forwards the optional instance-scoped control operation.
func (s *Session) RenewPromptCache(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	if !backendplugin.PromptCacheNegotiated(s.negotiation) {
		return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Unsupported}}, backendplugin.ErrPromptCacheUnsupported
	}
	if err := req.Validate(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	resp, err := s.client.RenewPromptCache(ctx, &backendpluginv1.RenewPromptCacheRequest{InstanceId: s.instanceID, Handle: append([]byte(nil), req.Handle...), OperationId: req.OperationID})
	if err != nil {
		return promptcache.RenewResponse{}, err
	}
	return backendplugin.PromptCacheRenewResponseFromProto(resp)
}

// ReleasePromptCache forwards the optional local-forget operation.
func (s *Session) ReleasePromptCache(ctx context.Context, req promptcache.ReleaseRequest) error {
	if !backendplugin.PromptCacheNegotiated(s.negotiation) {
		return backendplugin.ErrPromptCacheUnsupported
	}
	if err := req.Validate(); err != nil {
		return err
	}
	_, err := s.client.ReleasePromptCache(ctx, &backendpluginv1.ReleasePromptCacheRequest{InstanceId: s.instanceID, Handle: append([]byte(nil), req.Handle...)})
	return err
}

// FinalizeBilling forwards the optional billing-finalization operation.
func (s *Session) FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	resp, err := s.client.FinalizeBilling(ctx, backendplugin.FinalizeBillingRequestToProto(req))
	if err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	out, err := backendplugin.FinalizeBillingResponseFromProto(resp)
	if err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	if len(out.AccountingV2) != 0 && !backendplugin.AccountingEvidenceV2Negotiated(s.negotiation) {
		return backendplugin.FinalizeBillingResponse{}, backendplugin.ErrAccountingEvidenceV2Unsupported
	}
	return out, nil
}

func firstSessionError(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func mapSessionError(err error, terminal bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.Canceled {
			return context.Canceled
		}
		if st.Code() == codes.DeadlineExceeded && !terminal {
			return context.DeadlineExceeded
		}
		if terminal && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded || st.Code() == codes.Unknown || st.Code() == codes.Internal || st.Code() == codes.Aborted) {
			return nil
		}
	}
	if errors.Is(err, io.EOF) {
		if terminal {
			return nil
		}
		return backendplugin.ModeError{Code: "transport_death"}
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown, codes.Internal, codes.Aborted:
			if terminal {
				return nil
			}
			return backendplugin.ModeError{Code: "transport_death"}
		}
	}
	if !terminal {
		return backendplugin.ModeError{Code: "transport_death"}
	}
	return err
}

type cancelStream struct {
	ctx        context.Context
	frames     []backendplugin.ClientFrame
	pos        int
	cancelSent bool
}

func (s *cancelStream) Context() context.Context { return s.ctx }
func (s *cancelStream) Recv() (backendplugin.ClientFrame, error) {
	if s.pos >= len(s.frames) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	frame := s.frames[s.pos]
	s.pos++
	if frame.Kind == backendplugin.ClientFrameCancel {
		s.cancelSent = true
	}
	return frame, nil
}
func (s *cancelStream) Send(backendplugin.ServerFrame) error { return nil }

var _ interface {
	Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error)
	ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error)
	Execute(backendplugin.ExecuteStream) error
	Cancel(context.Context, backendplugin.Invocation) error
	Close(context.Context) error
	Negotiation() backendplugin.Negotiation
} = (*Session)(nil)

var (
	_ backendplugin.TokenCounter          = (*Session)(nil)
	_ backendplugin.BillingFinalizer      = (*Session)(nil)
	_ backendplugin.PromptCacheController = (*Session)(nil)
)

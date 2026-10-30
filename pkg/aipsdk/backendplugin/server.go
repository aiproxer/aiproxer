package backendplugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer adapts a public Service to the generated BackendPlugin gRPC contract.
// It does not launch processes; hosts supply transport separately.
//
// Concurrency/security limitation (Phase 1 helper): negotiation tokens and
// instances are stored in this server's process memory and are not bound to a
// live connection/session. Do not share one GRPCServer across mutually untrusted
// clients without an outer session boundary. Phase 3 replaces this with a
// connection-scoped host.
//
// Negotiate incompatibility (mismatched major/required features/transport policy)
// is returned as a NegotiateResponse with Compatible=false and a nil RPC error.
// Malformed wire/protocol offers return gRPC InvalidArgument.
type GRPCServer struct {
	backendpluginv1.UnimplementedBackendPluginServer

	PluginOffer ProtocolOffer
	Service     Service

	mu           sync.Mutex
	leaseCond    *sync.Cond
	status       HealthResponse
	negotiations map[string]Negotiation
	instances    map[string]*trackedInstance
}

type trackedInstance struct {
	inst          ConfiguredInstance
	negotiation   Negotiation
	leases        int
	isClosing     bool
	closeInFlight bool
}

// NewGRPCServer constructs a registration/dispatch adapter around svc.
func NewGRPCServer(offer ProtocolOffer, svc Service) *GRPCServer {
	s := &GRPCServer{
		PluginOffer:  offer,
		Service:      svc,
		status:       HealthResponse{Serving: true},
		negotiations: make(map[string]Negotiation),
		instances:    make(map[string]*trackedInstance),
	}
	s.leaseCond = sync.NewCond(&s.mu)
	return s
}

// Negotiate validates the host offer, negotiates against the plugin offer, and
// on a compatible outcome mints a negotiation_token bound to the negotiated set.
func (s *GRPCServer) Negotiate(ctx context.Context, req *backendpluginv1.NegotiateRequest) (*backendpluginv1.NegotiateResponse, error) {
	_ = ctx
	host, err := ProtocolOfferFromNegotiateRequest(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	neg, err := Negotiate(host, s.PluginOffer)
	if err != nil {
		if isMalformedNegotiateErr(err) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		// Deterministic incompatibility: wire body carries Compatible=false.
		resp, convErr := NegotiationToNegotiateResponse(neg)
		if convErr != nil {
			return nil, status.Error(codes.InvalidArgument, convErr.Error())
		}
		return resp, nil
	}
	token, err := mintNegotiationToken()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	neg.NegotiationToken = token
	s.mu.Lock()
	s.negotiations[token] = neg
	s.mu.Unlock()
	resp, convErr := NegotiationToNegotiateResponse(neg)
	if convErr != nil {
		return nil, status.Error(codes.InvalidArgument, convErr.Error())
	}
	return resp, nil
}

// Describe returns the plugin descriptor.
func (s *GRPCServer) Describe(ctx context.Context, _ *backendpluginv1.DescribeRequest) (*backendpluginv1.DescribeResponse, error) {
	d, err := s.Service.Describe(ctx)
	if err != nil {
		return nil, err
	}
	pd, err := PluginDescriptorToProto(d)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &backendpluginv1.DescribeResponse{Descriptor_: pd}, nil
}

// Configure creates and stores a configured instance bound to a prior negotiation token.
// The negotiation token is consumed atomically under the server mutex before
// Service.Configure runs. Fail-closed: a consumed token is not restored when
// ConfigureRequestFromProto or Service.Configure fails.
func (s *GRPCServer) Configure(ctx context.Context, req *backendpluginv1.ConfigureRequest) (*backendpluginv1.ConfigureResponse, error) {
	token := ""
	if req != nil {
		token = req.GetNegotiationToken()
	}
	if token == "" {
		return nil, status.Error(codes.FailedPrecondition, ErrNegotiationRequired.Error())
	}
	s.mu.Lock()
	neg, ok := s.negotiations[token]
	if !ok {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, ErrUnknownNegotiationToken.Error())
	}
	instanceID := ""
	if req != nil {
		instanceID = req.GetInstanceId()
	}
	if _, exists := s.instances[instanceID]; exists {
		s.mu.Unlock()
		return nil, status.Error(codes.AlreadyExists, ErrInstanceExists.Error())
	}
	delete(s.negotiations, token)
	s.mu.Unlock()

	cfg, err := ConfigureRequestFromProto(req, neg)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	inst, err := s.Service.Configure(ctx, cfg)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.instances[cfg.InstanceID]; exists {
		_ = inst.Close(ctx)
		return nil, status.Error(codes.AlreadyExists, ErrInstanceExists.Error())
	}
	s.instances[cfg.InstanceID] = &trackedInstance{inst: inst, negotiation: neg}
	return &backendpluginv1.ConfigureResponse{InstanceId: cfg.InstanceID}, nil
}

// CloseInstance closes a configured instance when no execute leases remain.
// The map entry is retained until Close succeeds so a failed Close remains retryable.
// Waiters are woken via leaseCond when leases drop or an in-flight close finishes.
// Context cancellation while waiting for leases clears the closing mark so later Close can retry.
func (s *GRPCServer) CloseInstance(ctx context.Context, req *backendpluginv1.CloseInstanceRequest) (*backendpluginv1.CloseInstanceResponse, error) {
	id := ""
	if req != nil {
		id = req.GetInstanceId()
	}
	stopWait := make(chan struct{})
	defer close(stopWait)
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.leaseCond.Broadcast()
			s.mu.Unlock()
		case <-stopWait:
		}
	}()

	s.mu.Lock()
	for {
		tr, ok := s.instances[id]
		if !ok {
			s.mu.Unlock()
			return nil, status.Error(codes.NotFound, fmt.Sprintf("%v: instance %q", ErrInvalidInvocation, id))
		}
		if ctx.Err() != nil {
			if tr.isClosing && !tr.closeInFlight && tr.leases > 0 {
				tr.isClosing = false
			}
			s.mu.Unlock()
			return nil, status.Error(codes.Canceled, ctx.Err().Error())
		}
		if tr.closeInFlight || tr.leases > 0 {
			tr.isClosing = true
			s.leaseCond.Wait()
			continue
		}
		tr.isClosing = true
		tr.closeInFlight = true
		inst := tr.inst
		s.mu.Unlock()

		err := inst.Close(ctx)
		s.mu.Lock()
		if cur, ok := s.instances[id]; ok && cur == tr {
			cur.closeInFlight = false
			if err != nil {
				cur.isClosing = true
				s.leaseCond.Broadcast()
				s.mu.Unlock()
				return nil, err
			}
			delete(s.instances, id)
			s.leaseCond.Broadcast()
		}
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &backendpluginv1.CloseInstanceResponse{}, nil
	}
}

// ResolveProfile resolves capabilities for an instance.
func (s *GRPCServer) ResolveProfile(ctx context.Context, req *backendpluginv1.ResolveProfileRequest) (*backendpluginv1.ResolveProfileResponse, error) {
	inst, release, neg, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	profile, err := inst.Resolve(ctx, req.ModelId)
	if err != nil {
		return nil, err
	}
	// Model-agnostic queries carry no per-model facts.
	if req.ModelId == nil {
		profile.ReasoningEffortValues = nil
	}
	// Unnegotiated peers never publish graded facts; suppress rather than leak.
	if !ReasoningEffortValuesNegotiated(neg) {
		profile.ReasoningEffortValues = nil
	} else if profile.ReasoningEffortValues != nil {
		if err := profile.ReasoningEffortValues.Validate(); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	// A connector-authored profile the wire cannot represent is a contract
	// violation; report it instead of answering with an empty profile.
	wire, err := resolvedProfileToProto(profile)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("resolved profile: %v", err))
	}
	return &backendpluginv1.ResolveProfileResponse{Profile: wire}, nil
}

// ListModels returns inventory when supported.
func (s *GRPCServer) ListModels(ctx context.Context, req *backendpluginv1.ListModelsRequest) (*backendpluginv1.ListModelsResponse, error) {
	inst, release, _, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := inst.ListModels(ctx, req.GetMaxModels())
	if err != nil {
		return nil, err
	}
	return ListModelsResponseToProto(out), nil
}

// CountTokens delegates when the instance implements TokenCounter.
func (s *GRPCServer) CountTokens(ctx context.Context, req *backendpluginv1.CountTokensRequest) (*backendpluginv1.CountTokensResponse, error) {
	inst, release, _, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	counter, ok := inst.(TokenCounter)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("%v: count_tokens not advertised", ErrInvalidInvocation))
	}
	in, err := CountTokensRequestFromProto(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out, err := counter.CountTokens(ctx, in)
	if err != nil {
		return nil, err
	}
	return CountTokensResponseToProto(out), nil
}

// FinalizeBilling delegates when the instance implements BillingFinalizer.
func (s *GRPCServer) RenewPromptCache(ctx context.Context, req *backendpluginv1.RenewPromptCacheRequest) (*backendpluginv1.RenewPromptCacheResponse, error) {
	inst, release, neg, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	if !PromptCacheNegotiated(neg) {
		return nil, status.Error(codes.FailedPrecondition, ErrPromptCacheUnsupported.Error())
	}
	controller, ok := inst.(PromptCacheController)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, ErrPromptCacheUnsupported.Error())
	}
	in := promptcache.RenewRequest{Handle: promptcache.Handle(append([]byte(nil), req.GetHandle()...)), OperationID: req.GetOperationId()}
	if err := in.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	result, err := controller.RenewPromptCache(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return PromptCacheRenewResponseToProto(result)
}

func (s *GRPCServer) ReleasePromptCache(ctx context.Context, req *backendpluginv1.ReleasePromptCacheRequest) (*backendpluginv1.ReleasePromptCacheResponse, error) {
	inst, release, neg, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	if !PromptCacheNegotiated(neg) {
		return nil, status.Error(codes.FailedPrecondition, ErrPromptCacheUnsupported.Error())
	}
	controller, ok := inst.(PromptCacheController)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, ErrPromptCacheUnsupported.Error())
	}
	in := promptcache.ReleaseRequest{Handle: promptcache.Handle(append([]byte(nil), req.GetHandle()...))}
	if err := in.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := controller.ReleasePromptCache(ctx, in); err != nil {
		return nil, err
	}
	return &backendpluginv1.ReleasePromptCacheResponse{}, nil
}

func (s *GRPCServer) FinalizeBilling(ctx context.Context, req *backendpluginv1.FinalizeBillingRequest) (*backendpluginv1.FinalizeBillingResponse, error) {
	inst, release, neg, err := s.acquire(req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	defer release()
	fin, ok := inst.(BillingFinalizer)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("%v: finalize_billing not advertised", ErrInvalidInvocation))
	}
	in, err := FinalizeBillingRequestFromProto(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out, err := fin.FinalizeBilling(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(out.AccountingV2) != 0 && !AccountingEvidenceV2Negotiated(neg) {
		return nil, status.Error(codes.FailedPrecondition, ErrAccountingEvidenceV2Unsupported.Error())
	}
	return FinalizeBillingResponseToProto(out)
}

// Execute bridges a bidirectional gRPC stream to ConfiguredInstance.Execute.
func (s *GRPCServer) Execute(stream backendpluginv1.BackendPlugin_ExecuteServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	frame, err := ClientFrameFromProto(first)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ValidateClientFrameBounds(frame); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if frame.Kind != ClientFrameStart || frame.Invocation == nil {
		return status.Error(codes.InvalidArgument, fmt.Sprintf("%v: execute requires start frame", ErrInvalidFrame))
	}
	inst, release, neg, err := s.acquire(frame.InstanceID)
	if err != nil {
		return err
	}
	defer release()
	call, err := CallFromInvocation(*frame.Invocation)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if strings.TrimSpace(call.Session.AuthoritativeSessionID) != "" && !ProxyOwnedSessionIDSupported(neg) {
		return status.Error(codes.FailedPrecondition, ErrProxyOwnedSessionUnsupported.Error())
	}
	if err := RequireExactOpenResponsesABISupport(neg, call); err != nil {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	adapter := &grpcExecuteStream{ctx: stream.Context(), stream: stream, pending: &frame, negotiation: neg}
	return inst.Execute(adapter)
}

// Health returns serving status.
func (s *GRPCServer) Health(ctx context.Context, _ *backendpluginv1.HealthRequest) (*backendpluginv1.HealthResponse, error) {
	_ = ctx
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	return HealthResponseToProto(st), nil
}

// GracefulShutdown acknowledges drain requests.
func (s *GRPCServer) GracefulShutdown(ctx context.Context, req *backendpluginv1.GracefulShutdownRequest) (*backendpluginv1.GracefulShutdownResponse, error) {
	_ = ctx
	_ = req
	s.mu.Lock()
	s.status.Serving = false
	s.mu.Unlock()
	return &backendpluginv1.GracefulShutdownResponse{Accepted: true}, nil
}

func (s *GRPCServer) acquire(id string) (ConfiguredInstance, func(), Negotiation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, ok := s.instances[id]
	if !ok {
		return nil, nil, Negotiation{}, status.Error(codes.InvalidArgument, fmt.Sprintf("%v: instance %q", ErrInvalidInvocation, id))
	}
	if tr.isClosing {
		return nil, nil, Negotiation{}, status.Error(codes.FailedPrecondition, ErrInstanceBusy.Error())
	}
	tr.leases++
	return tr.inst, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if cur, ok := s.instances[id]; ok && cur == tr && cur.leases > 0 {
			cur.leases--
			s.leaseCond.Broadcast()
		}
	}, tr.negotiation, nil
}

func mintNegotiationToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func isMalformedNegotiateErr(err error) bool {
	return errors.Is(err, ErrEmptyFeatureName) ||
		errors.Is(err, ErrDuplicateFeature) ||
		errors.Is(err, ErrUnknownEnum) ||
		errors.Is(err, ErrInvalidInvocation)
}

type grpcExecuteStream struct {
	ctx         context.Context
	stream      backendpluginv1.BackendPlugin_ExecuteServer
	pending     *ClientFrame
	negotiation Negotiation
}

func (g *grpcExecuteStream) Context() context.Context { return g.ctx }

func (g *grpcExecuteStream) Recv() (ClientFrame, error) {
	if g.pending != nil {
		f := *g.pending
		g.pending = nil
		return f, nil
	}
	msg, err := g.stream.Recv()
	if err != nil {
		return ClientFrame{}, err
	}
	frame, err := ClientFrameFromProto(msg)
	if err != nil {
		return ClientFrame{}, err
	}
	if err := ValidateClientFrameBounds(frame); err != nil {
		return ClientFrame{}, err
	}
	return frame, nil
}

func (g *grpcExecuteStream) Send(frame ServerFrame) error {
	if frame.Kind == ServerFramePromptCacheObservation && !PromptCacheNegotiated(g.negotiation) {
		return ErrPromptCacheUnsupported
	}
	if frame.AccountingV2 != nil && !AccountingEvidenceV2Negotiated(g.negotiation) {
		return ErrAccountingEvidenceV2Unsupported
	}
	if err := ValidateServerFrameBounds(frame); err != nil {
		return err
	}
	msg, err := ServerFrameToProto(frame)
	if err != nil {
		return err
	}
	return g.stream.Send(msg)
}

func (g *grpcExecuteStream) Negotiation() Negotiation {
	return g.negotiation
}

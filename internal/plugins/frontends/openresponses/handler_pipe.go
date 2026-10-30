package openresponses

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

type authDecisionCtxKey struct{}

func contextWithAuthDecision(ctx context.Context, d sdkauth.Decision) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	if d.SubmissionAuthority != nil {
		ctx = submission.WithAuthority(ctx, d.SubmissionAuthority.Clone())
	}
	return context.WithValue(ctx, authDecisionCtxKey{}, d)
}

func authDecisionFromContext(ctx context.Context) sdkauth.Decision {
	d, _ := ctx.Value(authDecisionCtxKey{}).(sdkauth.Decision)
	return d
}

// CreateEncodeState is per-request OpenResponses create state carried through Extra.
type CreateEncodeState struct {
	decoded    *DecodedCreate
	compact    *DecodedCompact
	responseID string
	store      aipcont.Store
	scope      aipcont.Scope
	isReserved bool
	owner      continuationReservationOwner
	observer   aipcont.StreamObserver
}

type createEncodeState = CreateEncodeState

type executorViewAdapter struct {
	ExecutorView
}

func (a executorViewAdapter) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return errors.New("openresponses: HTTP create adapter does not cancel A-legs")
}

func (a executorViewAdapter) WallClock() func() time.Time { return nil }

func adaptExecutor(ex ExecutorView) aipsdk.ExecutorView {
	if ex == nil {
		return nil
	}
	if v, ok := ex.(aipsdk.ExecutorView); ok {
		return v
	}
	return executorViewAdapter{ExecutorView: ex}
}

func (h *Handler) spec() *frontendpipe.Spec[createEncodeState] {
	h.pipeOnce.Do(func() {
		h.buildPipe()
	})
	return &h.pipe
}

func (h *Handler) buildPipe() {
	h.pipe = frontendpipe.Spec[createEncodeState]{
		Config: frontendpipe.Config{
			Exec:                    adaptExecutor(h.cfg.Executor),
			DefaultRouteSelector:    h.cfg.DefaultRouteSelector,
			RoutePrefixes:           routeselect.NewPrefixSet(h.cfg.RoutePrefixes),
			MaxRequestBodyBytes:     h.cfg.MaxRequestBodyBytes,
			Log:                     diag.LoggerOrDefault(h.cfg.Log),
			TrafficPorts:            h.cfg.TrafficPorts,
			DecodeAdmission:         h.cfg.DecodeAdmission,
			PreRequestKeepalive:     h.cfg.PreRequestKeepalive,
			FrontendID:              ID,
			HTTPHeaders:             h.cfg.HTTPHeaders,
			StreamKeepaliveInterval: h.cfg.StreamKeepaliveInterval,
			LargePayload:            h.cfg.LargePayload,
		},
		Wire:               WireErrors{},
		Profile:            h.cfg.Profile,
		RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if isCompactPath(path) || isCreatePath(path) {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			maxBytes := h.cfg.MaxRequestBodyBytes
			if maxBytes <= 0 {
				maxBytes = proto.MaxRequestBytes
			}
			if isCompactPath(dctx.URLPath) {
				decoded, err := DecodeCompactRequest(dctx.Ctx, dctx.Body, DecodeCompactOptions{
					DefaultRouteSelector: h.cfg.DefaultRouteSelector,
					RoutePrefixes:        h.cfg.RoutePrefixes,
					RouteSelector:        h.cfg.HTTPHeaders.RouteSelector(dctx.Headers),
					Headers:              dctx.Headers,
					Method:               http.MethodPost,
					Path:                 dctx.URLPath,
					MaxBodyBytes:         maxBytes,
					Limits:               h.cfg.ProtocolLimits,
					HTTPHeaders:          h.cfg.HTTPHeaders,
				})
				if err != nil {
					return nil, err
				}
				decoded.AuthDecision = authDecisionFromContext(dctx.Ctx)
				return &frontendpipe.Decoded{
					Call:          decoded.Call,
					Stream:        false,
					RouteSelector: decoded.RouteSelector,
					RouteOrigin:   decoded.RouteOrigin,
					Extra:         decoded,
				}, nil
			}
			decoded, err := AuthenticateAndDecodeCreate(dctx.Ctx, dctx.Body, DecodeCreateOptions{
				DefaultRouteSelector: h.cfg.DefaultRouteSelector,
				RoutePrefixes:        h.cfg.RoutePrefixes,
				RouteSelector:        h.cfg.HTTPHeaders.RouteSelector(dctx.Headers),
				Headers:              dctx.Headers,
				Method:               http.MethodPost,
				Path:                 dctx.URLPath,
				MaxBodyBytes:         maxBytes,
				Limits:               h.cfg.ProtocolLimits,
				HTTPHeaders:          h.cfg.HTTPHeaders,
			})
			if err != nil {
				return nil, err
			}
			decoded.AuthDecision = authDecisionFromContext(dctx.Ctx)
			return &frontendpipe.Decoded{
				Call:          decoded.Call,
				Stream:        decoded.Stream,
				RouteSelector: decoded.RouteSelector,
				RouteOrigin:   decoded.RouteOrigin,
				Extra:         decoded,
			}, nil
		},
		AfterDecode: func(ctx context.Context, decoded *frontendpipe.Decoded) error {
			if decoded.Call != nil && decoded.Call.Invocation.Operation == aipapi.OperationContextCompaction {
				compact, _ := decoded.Extra.(*DecodedCompact)
				if compact == nil {
					return &frontendpipe.StatusError{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "bad_request", Message: "Invalid request"}
				}
				decoded.Call = compact.Call
				decoded.Stream = false
				decoded.Extra = &createEncodeState{compact: compact}
				return nil
			}
			create, _ := decoded.Extra.(*DecodedCreate)
			if create == nil {
				return &frontendpipe.StatusError{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "bad_request", Message: "Invalid request"}
			}
			// A continuation restores its route from the parent lineage, so the
			// route the request ends up with is not the route the proxy chose:
			// the origin becomes the lineage, and the request uses the current
			// request-bound registry.
			routeBeforeLineage := decoded.Call.Route.Selector
			state, err := h.prepareCreateState(ctx, create)
			if err != nil {
				return err
			}
			decoded.Call = create.Call
			decoded.Stream = create.Stream
			decoded.Extra = state
			if routeBeforeLineage == "" && decoded.Call.Route.Selector != "" {
				decoded.RouteOrigin = routing.RouteOriginLineage
			}
			return nil
		},
		WrapStream: func(_ context.Context, decoded *frontendpipe.Decoded, inner aipapi.EventStream) (aipapi.EventStream, error) {
			state, _ := decoded.Extra.(*createEncodeState)
			if state == nil {
				return inner, nil
			}
			if inner == nil {
				if owner, ok := state.observer.(continuationReservationOwner); ok && owner.OwnsContinuationReservation() {
					safeReleaseContinuationReservation(owner)
				}
				safeCloseObserverFrontend(state.observer)
				if state.isReserved && state.store != nil {
					cleanupContinuationReservation(state.store, state.scope, state.responseID)
					state.isReserved = false
				}
				return nil, nil
			}
			stream := newAllowedToolsStream(decoded.Call, inner)
			if state.observer != nil {
				stream = &observedEventStream{EventStream: stream, observer: state.observer}
				if candidate, ok := state.observer.(continuationReservationOwner); ok && candidate.OwnsContinuationReservation() {
					state.owner = candidate
					state.isReserved = false
				}
			}
			return stream, nil
		},
		OnExecuteError: func(_ context.Context, decoded *frontendpipe.Decoded, _ error) {
			state, _ := decoded.Extra.(*createEncodeState)
			if state == nil || !state.isReserved || state.store == nil {
				return
			}
			cleanupContinuationReservation(state.store, state.scope, state.responseID)
			state.isReserved = false
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) createEncodeState {
			if state, ok := decoded.Extra.(*createEncodeState); ok && state != nil {
				return *state
			}
			return createEncodeState{}
		},
		WriteStream: func(ctx context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, opts createEncodeState) error {
			h.serveStreaming(ctx, w, es, opts.decoded, opts.responseID, opts.store, opts.scope, opts.isReserved, opts.owner)
			return nil
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, opts createEncodeState) error {
			if opts.compact != nil {
				return h.writeCompact(ctx, w, es, opts.compact)
			}
			if opts.decoded == nil || opts.decoded.Call == nil {
				writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
				return nil
			}
			if es == nil {
				if opts.isReserved && opts.store != nil {
					cleanupContinuationReservation(opts.store, opts.scope, opts.responseID)
				}
				writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
				return nil
			}
			clock := h.cfg.ResponseClock
			if clock == nil {
				clock = systemResponseClock{}
			}
			resource, collectErr := collectNonStreaming(ctx, es, nonStreamingEnvelope(opts.decoded, opts.responseID, clock.Now()), opts.decoded.Call.Options, h.cfg.ProtocolLimits)
			if collectErr != nil {
				if opts.isReserved && opts.store != nil {
					cleanupContinuationReservation(opts.store, opts.scope, opts.responseID)
				}
				if ctx.Err() != nil {
					return nil
				}
				status, typ, code, message := classifyExecutionError(collectErr)
				writeWireError(w, status, typ, code, message)
				return nil
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(resource)
			return nil
		},
		WireWrapStream: func(_ context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			if extra, ok := rc.Extra().(wireExtraState); ok {
				return newAllowedToolsStreamFromChoice(extra.ToolChoice, inner), nil
			}
			return inner, nil
		},
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			rc.WriteSessionHeaders(w)
			model := rc.ClientModel()
			if model == "" {
				model = rc.EffectiveModel()
			}
			// Frontend-owned response ID: fresh resp_* per wire response via the same
			// ResponseIDSource semantics as canonical no-store. Internal RequestID
			// (rc.CallID(), deterministic call_*) stays a distinct concept and must
			// never surface as response.id. Generated once here and carried
			// consistently through created/deltas/completed by serveStreamingWithModel.
			ids := h.cfg.ResponseIDSource
			if ids == nil {
				ids = systemResponseIDSource{}
			}
			responseID := ids.NewResponseID()
			var opts aipapi.GenerationOptions
			if extra, ok := rc.Extra().(wireExtraState); ok {
				opts = extra.Options
			}
			h.serveStreamingWithModel(ctx, w, es, nil, model, responseID, nil, aipcont.Scope{}, false, nil, opts)
			return nil
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			return h.writeWireNonStream(ctx, w, rc, es)
		},
	}
}

func (h *Handler) writeWireNonStream(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
	rc.WriteSessionHeaders(w)
	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	now := clock.Now()
	storeVal := false
	model := rc.ClientModel()
	if model == "" {
		model = rc.EffectiveModel()
	}
	// Same frontend-owned boundary as WireWriteStream: fresh resp_* per response,
	// never the deterministic internal RequestID.
	ids := h.cfg.ResponseIDSource
	if ids == nil {
		ids = systemResponseIDSource{}
	}
	meta := proto.EnvelopeMetadata{
		ResponseID:  ids.NewResponseID(),
		CreatedAt:   now,
		CompletedAt: &now,
		Model:       model,
		Store:       &storeVal,
	}
	var opts aipapi.GenerationOptions
	if extra, ok := rc.Extra().(wireExtraState); ok {
		opts = extra.Options
	}
	resource, collectErr := collectNonStreaming(ctx, es, meta, opts, h.cfg.ProtocolLimits)
	if collectErr != nil {
		if ctx.Err() != nil {
			return nil
		}
		status, typ, code, message := classifyExecutionError(collectErr)
		writeWireError(w, status, typ, code, message)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource)
	return nil
}

// IsEligibleNoStoreCreate reports whether the decoded request belongs to the
// certified bounded no-store subset (Task 17.1, Requirement 17.4):
//   - HTTP create (not compaction)
//   - explicit store: false (ExplicitStore != nil && !*ExplicitStore)
//   - no previous_response_id (PreviousResponseID == "")
//
// Missing store field returns false because current decode defaults store=true,
// requiring canonical storage/reservation processing.
func IsEligibleNoStoreCreate(decoded *DecodedCreate) bool {
	if decoded == nil {
		return false
	}
	if decoded.ExplicitStore == nil || *decoded.ExplicitStore {
		return false
	}
	if decoded.PreviousResponseID != "" {
		return false
	}
	return true
}

// prepareNoStoreState prepares the bounded, side-effect-free state for an
// explicit store:false create request with no previous_response_id (Task 17.1).
// It performs zero store reservations, zero parent lookups, and attaches zero
// recorder observers, guaranteeing that no AfterDecode side effect or error
// exists that could move after wire commit.
func (h *Handler) prepareNoStoreState(decoded *DecodedCreate) *createEncodeState {
	ids := h.cfg.ResponseIDSource
	if ids == nil {
		ids = systemResponseIDSource{}
	}
	return &createEncodeState{
		decoded:    decoded,
		responseID: ids.NewResponseID(),
		store:      nil,
		scope:      aipcont.Scope{},
		isReserved: false,
		observer:   nil,
	}
}

func (h *Handler) prepareCreateState(ctx context.Context, decoded *DecodedCreate) (*createEncodeState, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if IsEligibleNoStoreCreate(decoded) {
		return h.prepareNoStoreState(decoded), nil
	}

	var (
		store       = h.getStore()
		parent      aipcont.ContinuationRecord
		responseID  string
		isReserved  bool
		recordInput = decoded.Call.Items
		scope       = continuationScope(decoded)
	)
	if decoded.PreviousResponseID != "" {
		resolver := continuationResolverFor(h.cfg.ContinuationResolver, store, aipcont.Bounds{
			MaxChainDepth:        h.cfg.Config.Continuation.MaxChainDepth,
			MaxMaterializedBytes: h.cfg.Config.Continuation.MaxMaterializedBytes,
		})
		if resolver == nil {
			return nil, &frontendpipe.StatusError{
				Status: http.StatusBadRequest, Type: "invalid_request_error",
				Code: "previous_response_not_found", Message: "Previous response was not found",
			}
		}
		materialized, parentRecord, err := resolver.ResolveParent(ctx, scope, decoded.PreviousResponseID, *decoded.Call)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, aipcont.ErrStorageFailure) || errors.Is(err, context.DeadlineExceeded) {
				return nil, &frontendpipe.StatusError{
					Status: http.StatusInternalServerError, Type: "server_error",
					Code: "storage_error", Message: "Continuation storage is unavailable",
				}
			}
			return nil, &frontendpipe.StatusError{
				Status: http.StatusBadRequest, Type: "invalid_request_error",
				Code: "previous_response_not_found", Message: "Previous response was not found",
			}
		}
		parent = parentRecord
		decoded.Call = &materialized
		applyParentLineage(&decoded.Model, decoded.Call, &decoded.PreviousResponseID, parent)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if decoded.Store && store != nil {
		policy := h.storagePolicy()
		reserved, err := store.Reserve(ctx, scope, policy)
		if err != nil {
			return nil, &frontendpipe.StatusError{
				Status: http.StatusInternalServerError, Type: "server_error",
				Code: "storage_error", Message: "Failed to prepare response storage",
			}
		}
		responseID = reserved.String()
		isReserved = true
	}
	if responseID == "" {
		ids := h.cfg.ResponseIDSource
		if ids == nil {
			ids = systemResponseIDSource{}
		}
		responseID = ids.NewResponseID()
	}

	var observer aipcont.StreamObserver
	if decoded.Store && store != nil {
		lineage := aipcont.Lineage{ProfileID: DefaultProfile, Model: decoded.Model, RouteSelector: decoded.Call.Route.Selector}
		previous := aipcont.ResponseID(decoded.PreviousResponseID)
		depth := parent.ChainDepth + 1
		recorderFactory := h.cfg.RecorderFactory
		if recorderFactory == nil {
			recorderFactory = defaultContinuationRecorderFactory()
		}
		observer = recorderFactory.NewRecorder(store, aipcont.ContinuationRecord{
			ID:           aipcont.ResponseID(responseID),
			Scope:        scope,
			PreviousID:   previous,
			ProfileID:    DefaultProfile,
			Lineage:      lineage,
			InputItems:   aipcont.CloneItems(recordInput),
			Requirements: aipapi.DeriveProtocolRequirements(*decoded.Call),
			Policy:       h.storagePolicy(),
			ChainDepth:   depth,
		})
	}
	return &createEncodeState{
		decoded:    decoded,
		responseID: responseID,
		store:      store,
		scope:      scope,
		isReserved: isReserved,
		observer:   observer,
	}, nil
}

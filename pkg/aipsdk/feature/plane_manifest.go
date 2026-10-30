package feature

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type hookParticipant interface {
	ID() string
	Order() int
}

func sortHooks[T hookParticipant](h []T) []T {
	if len(h) == 0 {
		return nil
	}
	nonNil := make([]T, 0, len(h))
	for _, item := range h {
		var anyVal any = item
		if anyVal != nil {
			nonNil = append(nonNil, item)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	idx := make([]int, len(nonNil))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := nonNil[hi], nonNil[hj]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(hi, hj)
	})
	out := make([]T, len(nonNil))
	for k, ii := range idx {
		out[k] = nonNil[ii]
	}
	return out
}

func requireNonNilAttemptTransforms(in []request.AttemptTransform) []request.AttemptTransform {
	for _, t := range in {
		if t == nil {
			panic("extensions: SnapshotOptions.AttemptTransforms contains nil entry")
		}
	}
	return in
}

func requireNonNilStreamObserverFactories(in []response.StreamObserverFactory) []response.StreamObserverFactory {
	for _, f := range in {
		if f == nil {
			panic("extensions: SnapshotOptions.StreamObserverFactories contains nil entry")
		}
	}
	return in
}

func validateNonEmptyCachedIdentity(id string) error {
	if id == "" {
		return fmt.Errorf("identity must not be empty")
	}
	return nil
}

// PlaneSubmitHooks declares the SubmitHooks extension plane.
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{
	ID:            "submit_hooks",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy:  NilNotApplicable,
	HookTarget: HookTargetSubmitHooks,
	Combine: func(source SourceKind, current, incoming []hooks.SubmitHook) ([]hooks.SubmitHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.SubmitHook]{
		StageID: StageIDSubmit,
		Order:   10,
		Materialize: func(v []hooks.SubmitHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: h.ID()})
			}
			return occupants
		},
	},
}

// PlaneRequestPartHooks declares the RequestPartHooks extension plane.
var PlaneRequestPartHooks = Plane[[]hooks.RequestPartHook]{
	ID:            "request_part_hooks",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy:  NilNotApplicable,
	HookTarget: HookTargetRequestPartHooks,
	Combine: func(source SourceKind, current, incoming []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.RequestPartHook]{
		StageID: StageIDRequestWide,
		Order:   60,
		Materialize: func(v []hooks.RequestPartHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "request_part:" + h.ID()})
			}
			return occupants
		},
	},
}

// PlaneResponsePartHooks declares the ResponsePartHooks extension plane.
var PlaneResponsePartHooks = Plane[[]hooks.ResponsePartHook]{
	ID:            "response_part_hooks",
	RequestAccess: RequestBodyResponseOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy:  NilNotApplicable,
	HookTarget: HookTargetResponsePartHooks,
	Combine: func(source SourceKind, current, incoming []hooks.ResponsePartHook) ([]hooks.ResponsePartHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.ResponsePartHook]{
		StageID: StageIDStreamEventMutation,
		Order:   70,
		Materialize: func(v []hooks.ResponsePartHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: h.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolReactors declares the ToolReactors extension plane.
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{
	ID:            "tool_reactors",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy:  NilNotApplicable,
	HookTarget: HookTargetToolReactors,
	Combine: func(source SourceKind, current, incoming []hooks.ToolReactor) ([]hooks.ToolReactor, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.ToolReactor]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Order:         82,
		Materialize: func(v []hooks.ToolReactor) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, r := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: r.ID()})
			}
			return occupants
		},
	},
}

// PlaneSessionOpeners declares the SessionOpeners extension plane.
var PlaneSessionOpeners = Plane[[]session.Opener]{
	ID:            "session_openers",
	RequestAccess: RequestBodyMetadataOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []session.Opener) ([]session.Opener, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: func(v []session.Opener) []session.Opener {
		if len(v) == 0 {
			return nil
		}
		return cloneSlice(v)
	},
	Diagnostics: DiagnosticDescriptor[[]session.Opener]{
		StageID:       StageIDSessionOpen,
		CoalesceGroup: "session_open",
		Order:         90,
		Materialize: func(v []session.Opener) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for _, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "opener:" + o.ID()})
			}
			return occupants
		},
	},
}

// PlaneWorkspaceResolvers declares the WorkspaceResolvers extension plane.
var PlaneWorkspaceResolvers = Plane[[]workspace.Resolver]{
	ID:            "workspace_resolvers",
	RequestAccess: RequestBodyMetadataOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []workspace.Resolver) ([]workspace.Resolver, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]workspace.Resolver]{
		StageID:       StageIDSessionOpen,
		CoalesceGroup: "session_open",
		Order:         91,
		Materialize: func(v []workspace.Resolver) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, r := range v {
				if r == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("workspace_resolver:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneToolCatalogFilters declares the ToolCatalogFilters extension plane.
var PlaneToolCatalogFilters = Plane[[]toolcatalog.Filter]{
	ID:            "tool_catalog_filters",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolcatalog.Filter) ([]toolcatalog.Filter, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]toolcatalog.Filter]{
		StageID: StageIDToolCatalog,
		Order:   20,
		Materialize: func(v []toolcatalog.Filter) []DiagnosticOccupant {
			sorted := toolcatalog.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				if f == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_catalog:" + f.ID()})
			}
			return occupants
		},
		Privileges: func(v []toolcatalog.Filter) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneToolCallPolicies declares the ToolCallPolicies extension plane.
var PlaneToolCallPolicies = Plane[[]toolpolicy.Policy]{
	ID:            "tool_call_policies",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolpolicy.Policy) ([]toolpolicy.Policy, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: toolpolicy.MaterializeSorted,
	RequestBorrow:       true,
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Order:         80,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant {
			sorted := toolpolicy.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, p := range sorted {
				if p == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_policy:" + p.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolCallFinalizers declares the ToolCallFinalizers extension plane.
var PlaneToolCallFinalizers = Plane[[]toolcall.Finalizer]{
	ID:            "tool_call_finalizers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolcall.Finalizer) ([]toolcall.Finalizer, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: toolcall.MaterializeSorted,
	RequestBorrow:       true,
	Diagnostics: DiagnosticDescriptor[[]toolcall.Finalizer]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Order:         81,
		Materialize: func(v []toolcall.Finalizer) []DiagnosticOccupant {
			sorted := toolcall.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				if f == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_finalizer:" + f.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolCallFinalizationMaxArgsBytes declares the ToolCallFinalizationMaxArgsBytes extension plane.
var PlaneToolCallFinalizationMaxArgsBytes = Plane[int]{
	ID:            "tool_call_finalization_max_args_bytes",
	RequestAccess: RequestBodyMetadataOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombReduce,
	},
	NilPolicy: NilNotApplicable,
	Validate: func(v int) error {
		if v < 0 {
			return fmt.Errorf("must be >= 0, got %d", v)
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming int) (int, error) {
		if incoming <= 0 {
			return current, nil
		}
		if current <= 0 || incoming < current {
			return incoming, nil
		}
		return current, nil
	},
}

// PlaneRequestTransforms declares the RequestTransforms extension plane.
var PlaneRequestTransforms = Plane[[]request.Transform]{
	ID:            "request_transforms",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	Combine: func(source SourceKind, current, incoming []request.Transform) ([]request.Transform, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]request.Transform]{
		StageID: StageIDRequestWide,
		Order:   30,
		Materialize: func(v []request.Transform) []DiagnosticOccupant {
			nonNil := make([]request.Transform, 0, len(v))
			for _, tr := range v {
				if !isNilValue(tr) {
					nonNil = append(nonNil, tr)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := request.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, tr := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "request_transform:" + tr.ID()})
			}
			return occupants
		},
		Privileges: func(v []request.Transform) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlanePreRequestHandlers declares the PreRequestHandlers extension plane.
var PlanePreRequestHandlers = Plane[[]prerequest.Handler]{
	ID:            "pre_request_handlers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	Combine: func(source SourceKind, current, incoming []prerequest.Handler) ([]prerequest.Handler, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: prerequest.MaterializeSorted,
	Diagnostics: DiagnosticDescriptor[[]prerequest.Handler]{
		StageID: StageIDPreRequest,
		Order:   40,
		Materialize: func(v []prerequest.Handler) []DiagnosticOccupant {
			nonNil := make([]prerequest.Handler, 0, len(v))
			for _, h := range v {
				if !isNilValue(h) {
					nonNil = append(nonNil, h)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := prerequest.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "pre_request:" + h.ID()})
			}
			return occupants
		},
		Privileges: func(v []prerequest.Handler) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneRouteHintProviders declares the RouteHintProviders extension plane.
var PlaneRouteHintProviders = Plane[[]routehint.Provider]{
	ID:            "route_hint_providers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []routehint.Provider) ([]routehint.Provider, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]routehint.Provider]{
		StageID: StageIDRouteHinting,
		Order:   50,
		Materialize: func(v []routehint.Provider) []DiagnosticOccupant {
			sorted := routehint.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, p := range sorted {
				if p == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "route_hint:" + p.ID()})
			}
			return occupants
		},
	},
}

// PlaneCompletionGates declares the CompletionGates extension plane.
var PlaneCompletionGates = Plane[[]completion.Gate]{
	ID:            "completion_gates",
	RequestAccess: RequestBodyResponseOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []completion.Gate) ([]completion.Gate, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]completion.Gate]{
		StageID: StageIDCompletionGating,
		Order:   110,
		Materialize: func(v []completion.Gate) []DiagnosticOccupant {
			sorted := completion.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, g := range sorted {
				if g == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "completion_gate:" + g.ID()})
			}
			return occupants
		},
		Privileges: func(v []completion.Gate) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests, PrivilegeCompletionGate}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneAttemptTransforms declares the AttemptTransforms extension plane.
var PlaneAttemptTransforms = Plane[[]request.AttemptTransform]{
	ID:            "attempt_transforms",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	Identity: func(v []request.AttemptTransform) (string, bool) {
		if len(v) > 0 && !isNilValue(v[0]) {
			return v[0].ID(), true
		}
		return "", false
	},
	Validate: func(v []request.AttemptTransform) error {
		for i, at := range v {
			if isNilValue(at) {
				return fmt.Errorf("AttemptTransforms[%d] must not be nil", i)
			}
		}
		return nil
	},
	ValidateIdentity: validateNonEmptyCachedIdentity,
	Combine: func(source SourceKind, current, incoming []request.AttemptTransform) ([]request.AttemptTransform, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || isNilValue(incoming[0]) {
				return current, nil
			}
			incID := incoming[0].ID()
			out := make([]request.AttemptTransform, 0, len(current)+len(incoming))
			for _, t := range current {
				if !isNilValue(t) && t.ID() == incID {
					continue
				}
				out = append(out, t)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
	RequestMaterializer: func(v []request.AttemptTransform) []request.AttemptTransform {
		return request.MaterializeAttemptsSorted(requireNonNilAttemptTransforms(v))
	},
	Diagnostics: DiagnosticDescriptor[[]request.AttemptTransform]{
		StageID: StageIDCandidateAttemptTransform,
		Order:   120,
		Materialize: func(v []request.AttemptTransform) []DiagnosticOccupant {
			nonNil := make([]request.AttemptTransform, 0, len(v))
			for _, tr := range v {
				if !isNilValue(tr) {
					nonNil = append(nonNil, tr)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := request.MaterializeAttemptsSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, tr := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "attempt_transform:" + tr.ID()})
			}
			return occupants
		},
		Privileges: func(v []request.AttemptTransform) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneStreamObserverFactories declares the StreamObserverFactories extension plane.
var PlaneStreamObserverFactories = Plane[[]response.StreamObserverFactory]{
	ID:            "stream_observer_factories",
	RequestAccess: RequestBodyResponseOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	Identity: func(v []response.StreamObserverFactory) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			return v[0].ID(), true
		}
		return "", false
	},
	Validate: func(v []response.StreamObserverFactory) error {
		for i, f := range v {
			if f == nil {
				return fmt.Errorf("StreamObserverFactories[%d] must not be nil", i)
			}
		}
		return nil
	},
	ValidateIdentity: validateNonEmptyCachedIdentity,
	Combine: func(source SourceKind, current, incoming []response.StreamObserverFactory) ([]response.StreamObserverFactory, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || incoming[0] == nil {
				return current, nil
			}
			incID := incoming[0].ID()
			out := make([]response.StreamObserverFactory, 0, len(current)+len(incoming))
			for _, f := range current {
				if f != nil && f.ID() == incID {
					continue
				}
				out = append(out, f)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
	RequestMaterializer: func(v []response.StreamObserverFactory) []response.StreamObserverFactory {
		return response.MaterializeSorted(requireNonNilStreamObserverFactories(v))
	},
	Diagnostics: DiagnosticDescriptor[[]response.StreamObserverFactory]{
		StageID: StageIDFinalStreamObservation,
		Order:   130,
		Materialize: func(v []response.StreamObserverFactory) []DiagnosticOccupant {
			nonNil := make([]response.StreamObserverFactory, 0, len(v))
			for _, f := range v {
				if f != nil {
					nonNil = append(nonNil, f)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := response.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "stream_observer:" + f.ID()})
			}
			return occupants
		},
	},
}

// PlaneTrafficObservers declares the TrafficObservers extension plane.
var PlaneTrafficObservers = Plane[[]traffic.Observer]{
	ID:            "traffic_observers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		Host:    CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.Observer) ([]traffic.Observer, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]traffic.Observer]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Order:         140,
		Materialize: func(v []traffic.Observer) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("traffic_observer:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneUsageObservers declares the UsageObservers extension plane.
var PlaneUsageObservers = Plane[[]usage.Observer]{
	ID:            "usage_observers",
	RequestAccess: RequestBodyResponseOnly,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		Host:    CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []usage.Observer) ([]usage.Observer, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]usage.Observer]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Order:         141,
		Materialize: func(v []usage.Observer) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("usage_observer:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneRawCaptureSinks declares the RawCaptureSinks extension plane.
var PlaneRawCaptureSinks = Plane[[]traffic.RawCaptureSink]{
	ID:            "raw_capture_sinks",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.RawCaptureSink) ([]traffic.RawCaptureSink, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]traffic.RawCaptureSink]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Order:         142,
		Materialize: func(v []traffic.RawCaptureSink) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, s := range v {
				if s == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("raw_capture:%d", i)})
			}
			return occupants
		},
		Privileges: func(v []traffic.RawCaptureSink) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneTrafficRedactors declares the TrafficRedactors extension plane.
var PlaneTrafficRedactors = Plane[[]traffic.Redactor]{
	ID:            "traffic_redactors",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.Redactor) ([]traffic.Redactor, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: traffic.MaterializeSortedRedactors,
	Diagnostics: DiagnosticDescriptor[[]traffic.Redactor]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Order:         143,
		Materialize: func(v []traffic.Redactor) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for _, r := range v {
				if r == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "traffic_redactor:" + r.ID()})
			}
			return occupants
		},
	},
}

// PlaneCompactionObservers declares the CompactionObservers extension plane.
var PlaneCompactionObservers = Plane[[]compaction.Observer]{
	ID:            "compaction_observers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []compaction.Observer) ([]compaction.Observer, error) {
		return append(current, incoming...), nil
	},
}

func safePreserverID(p compaction.Preserver) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	if p != nil {
		return p.ID()
	}
	return ""
}

// PlaneCompactionPreservers declares the CompactionPreservers extension plane.
var PlaneCompactionPreservers = Plane[[]compaction.Preserver]{
	ID:            "compaction_preservers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilNotApplicable,
	Identity: func(v []compaction.Preserver) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			id := safePreserverID(v[0])
			if id != "" {
				return id, true
			}
		}
		return "", false
	},
	Validate: func(v []compaction.Preserver) error {
		for i, p := range v {
			if p == nil {
				return fmt.Errorf("CompactionPreservers[%d] must not be nil", i)
			}
		}
		return nil
	},
	ValidateIdentity: validateNonEmptyCachedIdentity,
	Combine: func(source SourceKind, current, incoming []compaction.Preserver) ([]compaction.Preserver, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || incoming[0] == nil {
				return current, nil
			}
			incID := safePreserverID(incoming[0])
			if incID == "" {
				return append(current, incoming...), nil
			}
			out := make([]compaction.Preserver, 0, len(current)+len(incoming))
			for _, p := range current {
				if p != nil && safePreserverID(p) == incID {
					continue
				}
				out = append(out, p)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
}

// PlaneSecretGuards declares the SecretGuards extension plane.
var PlaneSecretGuards = Plane[[]secretguard.Guard]{
	ID:            "secret_guards",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []secretguard.Guard) ([]secretguard.Guard, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: secretguard.MaterializeSorted,
	RequestBorrow:       true,
	Diagnostics: DiagnosticDescriptor[[]secretguard.Guard]{
		StageID: StageIDSecretGuard,
		Order:   100,
		Materialize: func(v []secretguard.Guard) []DiagnosticOccupant {
			sorted := secretguard.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, g := range sorted {
				if g == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "secret_guard:" + g.ID()})
			}
			return occupants
		},
	},
}

// PlaneSecretGuardExecution declares the generation-composed secret-guard
// execution configuration plane. Guards travel on PlaneSecretGuards; this
// exclusive generation-binder plane carries the engine-composed matcher,
// audit, policy, and diagnostics posture bound by the standard distribution
// for one generation. It is never candidate-overlaid.
var PlaneSecretGuardExecution = Plane[*secretguard.ExecutionConfig]{
	ID:            "secret_guard_execution",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultExclusive,
	Rules: SourceRules{
		// Binder-only admission: only the standard-distribution generation
		// binder may publish composed engine posture. Feature and host
		// sources are explicitly unsupported so no plugin can supply
		// execution posture when none was composed, nor conflict with the
		// standard binder's exclusive slot.
		Feature:          CombUnsupported,
		Host:             CombUnsupported,
		GenerationBinder: CombExclusive,
	},
	NilPolicy: NilSkip,
	// RequestMaterializer isolates frozen generations: every copy boundary
	// (clone, freeze, thaw, request freeze, read) deep-copies the container
	// and categories while preserving shared engine capabilities.
	RequestMaterializer: secretguard.CloneExecutionConfig,
	Identity: func(v *secretguard.ExecutionConfig) (string, bool) {
		if v == nil {
			return "", false
		}
		return "secret-guard-execution", true
	},
	ValidateIdentity: validateNonEmptyCachedIdentity,
	Combine: func(source SourceKind, current, incoming *secretguard.ExecutionConfig) (*secretguard.ExecutionConfig, error) {
		if incoming == nil {
			return incoming, nil
		}
		// Defensive copy: the frozen set must never alias contributor memory,
		// so later mutation by the contributor cannot alter a frozen
		// generation's configuration (frozen-value isolation).
		return secretguard.CloneExecutionConfig(incoming), nil
	},
}

// PlaneLocalTurnHandlers declares the LocalTurnHandlers extension plane.
var PlaneLocalTurnHandlers = Plane[[]localturn.Handler]{
	ID:            "local_turn_handlers",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilReject,
	Validate: func(v []localturn.Handler) error {
		for i, h := range v {
			if localturn.IsNilHandler(h) {
				return fmt.Errorf("LocalTurnHandlers[%d] must not be nil", i)
			}
			if err := localturn.ValidateHandlerID(h.ID()); err != nil {
				return fmt.Errorf("LocalTurnHandlers[%d] invalid id: %w", i, err)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []localturn.Handler) ([]localturn.Handler, error) {
		return append(current, incoming...), nil
	},
	RequestMaterializer: localturn.MaterializeSorted,
	RequestBorrow:       true,
	Diagnostics: DiagnosticDescriptor[[]localturn.Handler]{
		StageID: StageIDPreRequest,
		Order:   45,
		Materialize: func(v []localturn.Handler) []DiagnosticOccupant {
			sorted := localturn.MaterializeSorted(v)
			if len(sorted) == 0 {
				return nil
			}
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				if h == nil || localturn.IsNilHandler(h) {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "local_turn:" + h.ID()})
			}
			return occupants
		},
	},
}

// PlaneTerminalDecisionProvider declares the TerminalDecisionProvider extension plane.
var PlaneTerminalDecisionProvider = Plane[terminaldecision.Provider]{
	ID:            "terminal_decision_provider",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultExclusive,
	Rules: SourceRules{
		Feature: CombExclusive,
	},
	NilPolicy: NilReject,
	Identity: func(v terminaldecision.Provider) (string, bool) {
		id, err := terminaldecision.ProviderIdentity(v)
		if err != nil {
			return "", false
		}
		return id, true
	},
	Validate: func(v terminaldecision.Provider) error {
		_, err := terminaldecision.ProviderIdentity(v)
		return err
	},
	ValidateIdentity: terminaldecision.ValidateProviderID,
	Combine: func(source SourceKind, current, incoming terminaldecision.Provider) (terminaldecision.Provider, error) {
		return incoming, nil
	},
	ExclusiveConflictError: ErrTerminalDecisionProviderConflict,
}

// PlaneSessionClassifier declares the exclusive metadata-only session-classification plane.
var PlaneSessionClassifier = Plane[sessionclassification.Classifier]{
	ID:            "session_classifier",
	RequestAccess: RequestBodyMetadataOnly,
	Multiplicity:  MultExclusive,
	Rules: SourceRules{
		Feature: CombExclusive,
	},
	NilPolicy: NilReject,
	Identity: func(v sessionclassification.Classifier) (string, bool) {
		id, err := sessionclassification.ClassifierIdentity(v)
		if err != nil {
			return "", false
		}
		return id, true
	},
	Validate: func(v sessionclassification.Classifier) error {
		_, err := sessionclassification.ClassifierIdentity(v)
		return err
	},
	ValidateIdentity: sessionclassification.ValidateClassifierID,
	Combine: func(_ SourceKind, _ sessionclassification.Classifier, incoming sessionclassification.Classifier) (sessionclassification.Classifier, error) {
		return incoming, nil
	},
	Diagnostics: DiagnosticDescriptor[sessionclassification.Classifier]{
		StageID: StageIDSessionClassification,
		Order:   101,
		Materialize: func(v sessionclassification.Classifier) []DiagnosticOccupant {
			// Composition has already validated and cached the classifier identity.
			// Diagnostics expose only the fixed occupant label and never re-read ID.
			if v == nil {
				return nil
			}
			return []DiagnosticOccupant{{Label: "classifier"}}
		},
	},
}

// PlaneControlToolProvider declares the ControlToolProvider extension plane: at
// most one optional proxy-owned model control tool owner per generation. It
// mirrors PlaneTerminalDecisionProvider: feature-only exclusive admission,
// typed-nil rejection before publication, and provider validation delegated to
// the generic controltool contract.
var PlaneControlToolProvider = Plane[controltool.Provider]{
	ID:            "control_tool_provider",
	RequestAccess: RequestBodyCanonicalRequired,
	Multiplicity:  MultExclusive,
	Rules: SourceRules{
		Feature: CombExclusive,
	},
	NilPolicy: NilReject,
	Identity: func(v controltool.Provider) (string, bool) {
		id, err := controltool.ProviderIdentity(v)
		if err != nil {
			return "", false
		}
		return id, true
	},
	Validate: func(v controltool.Provider) error {
		return controltool.ValidateProvider(v)
	},
	ValidateIdentity: controltool.ValidateProviderID,
	Combine: func(source SourceKind, current, incoming controltool.Provider) (controltool.Provider, error) {
		return incoming, nil
	},
	ExclusiveConflictError: ErrControlToolProviderConflict,
}

// StandardPlanes is the ordered slice of all standard feature planes.
var StandardPlanes = []PlaneDeclaration{
	PlaneSubmitHooks,
	PlaneRequestPartHooks,
	PlaneResponsePartHooks,
	PlaneToolReactors,
	PlaneSessionOpeners,
	PlaneWorkspaceResolvers,
	PlaneToolCatalogFilters,
	PlaneToolCallPolicies,
	PlaneToolCallFinalizers,
	PlaneToolCallFinalizationMaxArgsBytes,
	PlaneRequestTransforms,
	PlanePreRequestHandlers,
	PlaneRouteHintProviders,
	PlaneCompletionGates,
	PlaneAttemptTransforms,
	PlaneStreamObserverFactories,
	PlaneTrafficObservers,
	PlaneUsageObservers,
	PlaneRawCaptureSinks,
	PlaneTrafficRedactors,
	PlaneCompactionObservers,
	PlaneCompactionPreservers,
	PlaneSecretGuards,
	PlaneSecretGuardExecution,
	PlaneLocalTurnHandlers,
	PlaneTerminalDecisionProvider,
	PlaneSessionClassifier,
	PlaneControlToolProvider,
}

// StandardCandidatePlanes defines the canonical list of plane IDs allowed in candidate overlay contribution.
var StandardCandidatePlanes = []string{
	"session_openers",
	"workspace_resolvers",
	"tool_catalog_filters",
	"tool_call_policies",
	"tool_call_finalizers",
	"tool_call_finalization_max_args_bytes",
	"request_transforms",
	"pre_request_handlers",
	"route_hint_providers",
	"completion_gates",
	"attempt_transforms",
	"secret_guards",
	"compaction_observers",
	"compaction_preservers",
	"local_turn_handlers",
	"terminal_decision_provider",
}

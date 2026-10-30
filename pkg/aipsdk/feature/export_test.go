package feature

import (
	"reflect"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// GeneratedContributionsForTest is a type alias to the internal generatedContributions type for testing.
type GeneratedContributionsForTest = generatedContributions

// GeneratedFrozenForTest is a type alias to the internal generatedFrozen type for testing.
type GeneratedFrozenForTest = generatedFrozen

var (
	testStorageMu      sync.Mutex
	testContribStorage = make(map[*generatedContributions]map[string]any)
	testFrozenStorage  = make(map[*generatedFrozen]map[string]any)
)

func init() {
	onFreezeGenerated = func(gc *generatedContributions, gf *generatedFrozen) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testContribStorage[gc]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testFrozenStorage[gf] = mCopy
		}
	}
	onCloneGenerated = func(src, dst *generatedContributions) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testContribStorage[src]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testContribStorage[dst] = mCopy
		}
	}
	onThawGenerated = func(gf *generatedFrozen, gc *generatedContributions) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testFrozenStorage[gf]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testContribStorage[gc] = mCopy
		}
	}
	onCloneFrozen = func(src, dst *generatedFrozen) {
		testStorageMu.Lock()
		defer testStorageMu.Unlock()
		if m, ok := testFrozenStorage[src]; ok {
			mCopy := make(map[string]any, len(m))
			for k, v := range m {
				mCopy[k] = cloneValue(v)
			}
			testFrozenStorage[dst] = mCopy
		}
	}
}

// BindGeneratedAccessForTest attaches generated access closures and canonical policy to a Plane[T] for testing.
func BindGeneratedAccessForTest[T any](
	p Plane[T],
	contribute func(*generatedContributions, SourceKind, string, T) error,
	get func(*generatedFrozen) T,
	identity func(*generatedFrozen) (string, bool),
) Plane[T] {
	p.generated = generatedAccess[T]{
		contribute: contribute,
		get:        get,
		identity:   identity,
		policy: &generatedPolicy[T]{
			planeID:                p.ID,
			rules:                  p.Rules,
			nilPolicy:              p.NilPolicy,
			isNil:                  p.IsNil,
			validate:               p.Validate,
			validateIdentity:       p.ValidateIdentity,
			combine:                p.Combine,
			identity:               p.Identity,
			exclusiveConflictError: p.ExclusiveConflictError,
			requestAccess:          p.RequestAccess,
		},
	}
	return p
}

// SetCanonicalValidateIdentityForTest temporarily overrides validateIdentity on a plane's canonical policy.
func SetCanonicalValidateIdentityForTest[T any](p Plane[T], fn func(string) error) func() {
	if p.generated.policy == nil {
		return func() {}
	}
	orig := p.generated.policy.validateIdentity
	p.generated.policy.validateIdentity = fn
	return func() {
		p.generated.policy.validateIdentity = orig
	}
}

// BindGeneratedTestPlane attaches canonical policy and test eligibility/storage to a Plane[T] for testing.
func BindGeneratedTestPlane[T any](p Plane[T]) Plane[T] {
	return BindGeneratedAccessForTest(
		p,
		func(gc *generatedContributions, source SourceKind, pluginID string, v T) error {
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			m := testContribStorage[gc]
			if m == nil {
				m = make(map[string]any)
				testContribStorage[gc] = m
			}
			var current T
			if curVal, ok := m[p.ID]; ok {
				if typed, ok := curVal.(T); ok {
					current = typed
				}
			}
			currentCopy := cloneValue(current)
			incoming := cloneValue(v)
			combined, err := p.Combine(source, currentCopy, incoming)
			if err != nil {
				return err
			}
			if anyVal := any(v); anyVal != nil {
				rv := reflect.ValueOf(anyVal)
				if rv.Kind() == reflect.Slice && !rv.IsNil() {
					if anyComb := any(combined); anyComb == nil || isReflectNil(reflect.ValueOf(anyComb)) {
						if typedEmpty, ok := reflect.TypeAssert[T](reflect.MakeSlice(rv.Type(), 0, 0)); ok {
							combined = typedEmpty
						}
					}
				}
			}
			m[p.ID] = cloneValue(combined)
			return nil
		},
		func(gf *generatedFrozen) T {
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			if m, ok := testFrozenStorage[gf]; ok {
				if val, ok := m[p.ID]; ok {
					if typed, ok := val.(T); ok {
						return cloneValue(typed)
					}
				}
			}
			var zero T
			return zero
		},
		func(gf *generatedFrozen) (string, bool) {
			if p.Identity == nil {
				return "", false
			}
			testStorageMu.Lock()
			defer testStorageMu.Unlock()
			if m, ok := testFrozenStorage[gf]; ok {
				if val, ok := m[p.ID]; ok {
					if typed, ok := val.(T); ok {
						return p.Identity(typed)
					}
				}
			}
			return "", false
		},
	)
}

// NewContributionSetWithGeneratedForTest creates a ContributionSet wrapping a generatedContributions pointer for testing.
func NewContributionSetWithGeneratedForTest(gen *generatedContributions) *ContributionSet {
	s := NewContributionSet()
	s.generated = gen
	return s
}

// NewFrozenPlaneSetWithGeneratedForTest creates a FrozenPlaneSet wrapping a generatedFrozen pointer for testing.
func NewFrozenPlaneSetWithGeneratedForTest(frozen *generatedFrozen) FrozenPlaneSet {
	return FrozenPlaneSet{
		frozen: frozen,
	}
}

// NewGeneratedContributionsForTest creates a new generatedContributions for testing.
func NewGeneratedContributionsForTest() *generatedContributions {
	return newGeneratedContributions()
}

// NewMalformedGeneratedFrozenCandidateForTest constructs a test-only generated FrozenPlaneSet
// with specific candidate request transforms and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenCandidateForTest(
	reqTr []request.Transform,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		requestTransforms: cloneSlice(reqTr),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenSessionWorkspaceCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate session openers, workspace resolvers, and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenSessionWorkspaceCandidateForTest(
	so []session.Opener,
	wr []workspace.Resolver,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		sessionOpeners:     cloneSlice(so),
		workspaceResolvers: cloneSlice(wr),
		attemptTransforms:  cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenSecretGuardsCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate secret guards and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenSecretGuardsCandidateForTest(
	sg []secretguard.Guard,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		secretGuards:      cloneSlice(sg),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenCompactionCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate compaction observers and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenCompactionCandidateForTest(
	obs []compaction.Observer,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		compactionObservers: cloneSlice(obs),
		attemptTransforms:   cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenLocalTurnCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate local turn handlers and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenLocalTurnCandidateForTest(
	lt []localturn.Handler,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		localTurnHandlers: cloneSlice(lt),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate terminal decision provider and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenTerminalDecisionCandidateForTest(
	provider terminaldecision.Provider,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider: provider,
		attemptTransforms:        cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest constructs a test-only generated FrozenPlaneSet
// with candidate terminal decision provider but missing frozen identity.
func NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(
	provider terminaldecision.Provider,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider: provider,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenTerminalDecisionForTest constructs a test-only generated FrozenPlaneSet
// with specific terminal decision provider and cached identity metadata.
func NewMalformedGeneratedFrozenTerminalDecisionForTest(
	provider terminaldecision.Provider,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		terminalDecisionProvider:      provider,
		terminalDecisionProviderID:    id,
		terminalDecisionProviderHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenStreamObserversCandidateForTest constructs a test-only generated FrozenPlaneSet
// with candidate stream observer factories for testing.
func NewMalformedGeneratedFrozenStreamObserversCandidateForTest(
	so []response.StreamObserverFactory,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		streamObserverFactories: cloneSlice(so),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenAttemptTransformsForTest constructs a test-only generated FrozenPlaneSet
// with specific attempt transforms and cached identity metadata.
func NewMalformedGeneratedFrozenAttemptTransformsForTest(
	tr []request.AttemptTransform,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		attemptTransforms:      cloneSlice(tr),
		attemptTransformsID:    id,
		attemptTransformsHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenStreamObserverFactoriesForTest constructs a test-only generated FrozenPlaneSet
// with specific stream observer factories and cached identity metadata.
func NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(
	sof []response.StreamObserverFactory,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		streamObserverFactories:      cloneSlice(sof),
		streamObserverFactoriesID:    id,
		streamObserverFactoriesHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenCompactionPreserversForTest constructs a test-only generated FrozenPlaneSet
// with specific compaction preservers and cached identity metadata.
func NewMalformedGeneratedFrozenCompactionPreserversForTest(
	cp []compaction.Preserver,
	id string,
	hasID bool,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		compactionPreservers:      cloneSlice(cp),
		compactionPreserversID:    id,
		compactionPreserversHasID: hasID,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenToolCallFinalizationCandidateForTest constructs a test-only generated FrozenPlaneSet
// with finalizers and max args bytes.
func NewMalformedGeneratedFrozenToolCallFinalizationCandidateForTest(
	finalizers []toolcall.Finalizer,
	maxArgs int,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		toolCallFinalizers:               cloneSlice(finalizers),
		toolCallFinalizationMaxArgsBytes: maxArgs,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

// NewMalformedGeneratedFrozenToolCallFinalizationMaxArgsBytesForTest constructs a test-only generated FrozenPlaneSet
// with specific tool call finalization max args bytes.
func NewMalformedGeneratedFrozenToolCallFinalizationMaxArgsBytesForTest(maxArgs int) FrozenPlaneSet {
	gf := &generatedFrozen{
		toolCallFinalizationMaxArgsBytes: maxArgs,
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}

func cloneValue[T any](v T) T {
	var anyVal any = v
	if anyVal == nil {
		return v
	}
	cloned := cloneAny(anyVal)
	if typed, ok := cloned.(T); ok {
		return typed
	}
	return v
}

func cloneAny(v any) any {
	if v == nil {
		return nil
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Slice:
		if val.IsNil() {
			return v
		}
		l := val.Len()
		c := val.Cap()
		out := reflect.MakeSlice(val.Type(), l, c)
		reflect.Copy(out, val)
		return out.Interface()
	case reflect.Map:
		if val.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(val.Type(), val.Len())
		iter := val.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), iter.Value())
		}
		return out.Interface()
	default:
		return v
	}
}

// DeclaredHookTargetForTest returns the declared hook target of a PlaneDeclaration if implemented.
func DeclaredHookTargetForTest(decl PlaneDeclaration) HookTarget {
	if htp, ok := decl.(hookTargetProvider); ok {
		return htp.declaredHookTarget()
	}
	return ""
}

// DeclaredRequestAccessForTest returns the declared request-body access class
// of a PlaneDeclaration, or Unclassified when the declaration does not carry
// access metadata.
func DeclaredRequestAccessForTest(decl PlaneDeclaration) RequestBodyAccess {
	if rap, ok := decl.(requestAccessProvider); ok {
		return rap.declaredRequestAccess()
	}
	return RequestBodyAccessUnclassified
}

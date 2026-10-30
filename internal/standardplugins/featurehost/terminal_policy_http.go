package featurehost

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	policyhttp "github.com/aiproxer/aiproxer/internal/stdhttp/terminalpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

const terminalDecisionPolicyFeatureID = "terminal-decision"

// TerminalDecisionPolicyHTTPProjection binds the process-owned policy and
// secure-session services to one immutable generation's HTTP composition.
// The callbacks capture only request authority and the process store; they do
// not perform live provider lookup or mutate generation state.
func (r *Runtime) TerminalDecisionPolicyHTTPProjection(
	snapshot *extensions.RequestRuntimeSnapshot,
	headers aipsdk.HTTPHeaders,
	maxBodyBytes int64,
	store ssessionapp.Store,
) httpcontract.TerminalDecisionPolicyInput {
	var policyStore *sessionpolicy.Store
	if r != nil {
		policyStore = r.terminalPolicy
	}
	available := snapshot != nil && snapshot.TerminalDecisionProvider() != nil
	return httpcontract.TerminalDecisionPolicyInput{
		Store: policyStore,
		FeatureStatus: func(_ context.Context, featureID string) (bool, bool, error) {
			return featureID == terminalDecisionPolicyFeatureID, available, nil
		},
		ResolveClientScope: func(ctx context.Context, req *http.Request, featureID string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
			return resolveTerminalDecisionClientScope(ctx, req, featureID, store, headers)
		},
		AuthorizeOperatorTarget: func(ctx context.Context, req *http.Request, sessionID, featureID string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
			return authorizeTerminalDecisionOperatorTarget(ctx, req, sessionID, featureID, store)
		},
		GenerationDefault: func(featureID string) bool {
			return featureID == terminalDecisionPolicyFeatureID && available
		},
		MaxBodyBytes: maxBodyBytes,
	}
}

// TerminalPolicyProjectionFunc builds one generation's terminal decision policy
// HTTP input from request-time composition state. It is a fixed consumer port:
// generic runtimebundle invokes it without referencing concrete feature symbols.
type TerminalPolicyProjectionFunc func(snapshot *extensions.RequestRuntimeSnapshot, headers aipsdk.HTTPHeaders, maxBodyBytes int64, store ssessionapp.Store) httpcontract.TerminalDecisionPolicyInput

// TerminalPolicyProjection returns the opaque generation-scoped factory for the
// terminal decision policy HTTP projection bound to this process Runtime.
func (r *Runtime) TerminalPolicyProjection() TerminalPolicyProjectionFunc {
	return func(snapshot *extensions.RequestRuntimeSnapshot, headers aipsdk.HTTPHeaders, maxBodyBytes int64, store ssessionapp.Store) httpcontract.TerminalDecisionPolicyInput {
		return r.TerminalDecisionPolicyHTTPProjection(snapshot, headers, maxBodyBytes, store)
	}
}

// TerminalDecisionPolicyHTTPProjection provides a package-level helper that safely
// handles nil Runtime.
func TerminalDecisionPolicyHTTPProjection(
	r *Runtime,
	snapshot *extensions.RequestRuntimeSnapshot,
	headers aipsdk.HTTPHeaders,
	maxBodyBytes int64,
	store ssessionapp.Store,
) httpcontract.TerminalDecisionPolicyInput {
	if r == nil {
		return (&Runtime{}).TerminalDecisionPolicyHTTPProjection(snapshot, headers, maxBodyBytes, store)
	}
	return r.TerminalDecisionPolicyHTTPProjection(snapshot, headers, maxBodyBytes, store)
}

func resolveTerminalDecisionClientScope(
	ctx context.Context,
	r *http.Request,
	featureID string,
	store ssessionapp.Store,
	headers aipsdk.HTTPHeaders,
) (sessionpolicy.Key, sessionpolicy.Authority, error) {
	sc, ok := httpauth.ScopeFromContext(ctx)
	if !ok || !sc.PrincipalID.IsKnown() || strings.TrimSpace(sc.PrincipalID.String()) == "" {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrUnauthenticated
	}
	if r == nil {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	sessionID := strings.TrimSpace(headers.SessionIDValue(r.Header))
	aLegID := strings.TrimSpace(headers.ALegIDValue(r.Header))
	if sessionID == "" || aLegID == "" || store == nil {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
		}
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, err
	}
	if !rec.Status.IsActive() || strings.TrimSpace(rec.ALegID) != aLegID {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	if !principalOwnsSession(rec.Owner, sc) {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrForbidden
	}
	key := sessionpolicy.Key{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		FeatureID:                featureID,
	}
	return key, sessionpolicy.Authority{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		Authorized:               true,
	}, nil
}

func authorizeTerminalDecisionOperatorTarget(
	ctx context.Context,
	r *http.Request,
	sessionID, featureID string,
	store ssessionapp.Store,
) (sessionpolicy.Key, sessionpolicy.Authority, error) {
	if _, ok := httpauth.ScopeFromContext(ctx); !ok {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrUnauthenticated
	}
	if r == nil || store == nil {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSessionNotFound
		}
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, err
	}
	if !rec.Status.IsActive() || strings.TrimSpace(rec.ALegID) == "" {
		return sessionpolicy.Key{}, sessionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	key := sessionpolicy.Key{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		FeatureID:                featureID,
	}
	return key, sessionpolicy.Authority{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		Authorized:               true,
	}, nil
}

func principalOwnsSession(owner domain.PrincipalRef, sc scope.PrincipalScopeView) bool {
	if strings.TrimSpace(owner.ID) == "" || strings.TrimSpace(owner.ID) != strings.TrimSpace(sc.PrincipalID.String()) {
		return false
	}
	if strings.TrimSpace(owner.Tenant) != "" && strings.TrimSpace(owner.Tenant) != strings.TrimSpace(sc.TenantID.String()) {
		return false
	}
	return true
}

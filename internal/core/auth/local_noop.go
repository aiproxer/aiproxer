package auth

import (
	"context"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// LocalNoOpAuthenticator grants credential-free access with an explicit non-anonymous
// principal derived from [OSIdentityProvider]. It must only be wired when access posture
// validation permits local no-op.
type LocalNoOpAuthenticator struct {
	OS OSIdentityProvider
	// OnOSIdentityFallback, if set, is called when the OS provider is nil or [OSIdentityProvider.Current] fails,
	// before a fallback principal ([LocalUnknownOSPrincipalID]) is used. err is non-nil only when
	// Current was invoked; hadProvider is false when OS is nil.
	OnOSIdentityFallback func(ctx context.Context, err error, hadProvider bool)
}

// Authenticate implements [Authenticator].
func (a LocalNoOpAuthenticator) Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = req
	var snap OSIdentitySnapshot
	var err error
	if a.OS != nil {
		snap, err = a.OS.Current(ctx)
	}
	if err != nil || a.OS == nil {
		if a.OnOSIdentityFallback != nil {
			a.OnOSIdentityFallback(ctx, err, a.OS != nil)
		}
		snap = OSIdentitySnapshot{
			PrincipalID:  LocalUnknownOSPrincipalID,
			DisplayName:  "",
			FallbackUsed: true,
		}
	}
	id := strings.TrimSpace(snap.PrincipalID)
	if id == "" {
		id = LocalUnknownOSPrincipalID
		snap.FallbackUsed = true
	}
	displayName := strings.TrimSpace(snap.DisplayName)
	principal := execview.PrincipalView{ID: id, DisplayName: displayName}
	return sdkauth.Decision{
		Outcome:        sdkauth.OutcomeAllow,
		Principal:      principal,
		SatisfiedLevel: sdkauth.LevelNone,
		Scope:          scopeFromLocalNoOp(id, displayName),
	}, nil
}

// scopeFromLocalNoOp marks allowed local no-auth requests as local single-user scope without
// inventing tenant, project, department, or cost-center values (requirements 1.4, 2.4, 3.5).
func scopeFromLocalNoOp(principalID, displayName string) *scope.PrincipalScopeView {
	return &scope.PrincipalScopeView{
		SubjectKind: scope.SubjectLocal,
		Origin:      scope.OriginClient,
		PrincipalID: scope.Known(principalID),
		AuthMethod:  scope.Known("local_noop"),
		DisplayName: knownOrUnknown(displayName),
	}
}

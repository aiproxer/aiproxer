package runtime

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// SecureSessionRecorder is an alias for the secure-session gate recording port implemented by [app.Recorder].
type SecureSessionRecorder = app.GateRecording

func principalRefFromView(p execview.PrincipalView) domain.PrincipalRef {
	r := domain.PrincipalRef{ID: strings.TrimSpace(p.ID)}
	if len(p.Claims) == 0 {
		return r
	}
	if v := strings.TrimSpace(p.Claims["issuer"]); v != "" {
		r.Issuer = v
	}
	if v := strings.TrimSpace(p.Claims["tenant"]); v != "" {
		r.Tenant = v
	}
	return r
}

func principalRefFromScope(p execview.PrincipalView, s scope.PrincipalScopeView) domain.PrincipalRef {
	r := principalRefFromView(p)
	if s.TenantID.IsKnown() {
		r.Tenant = strings.TrimSpace(s.TenantID.String())
	}
	return r
}

func (e *Executor) secureSessionForAttempt() *app.Manager {
	if e == nil || e.SecureSession == nil {
		return nil
	}
	return e.SecureSession
}

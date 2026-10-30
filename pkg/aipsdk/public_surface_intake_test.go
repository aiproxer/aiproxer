package aipsdk

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// This test exercises the public auth and backend security surface (no internal packages)
// to catch accidental un-export or compile breaks for SDK consumers.
func TestPublicAuthAndBackendSecuritySurface(t *testing.T) {
	t.Parallel()
	_ = auth.HandlerLocalNoop
	_ = auth.OutcomeAllow
	_ = auth.Challenge{Kind: auth.ChallengeSSORequired}
	_ = auth.InboundCallMeta{TraceID: "t1"}
	_ = auth.Decision{Principal: execview.PrincipalView{ID: "u"}}
	_ = auth.AuthDecisionEvent{Time: time.Now(), TraceID: "t1"}
	_ = auth.SessionStartEvent{SessionID: "s1", Certainty: auth.SessionCertaintyUnknown}
	_ = AuthErrorRenderResult{Status: 401, ContentType: "application/json; charset=utf-8"}
	_ = AuthErrorRenderInput{Decision: auth.Decision{Outcome: auth.OutcomeDeny}}
	_ = BackendSecurityProfile{CredentialMode: CredentialStatic}
	_ = BackendSecurityProfile{CredentialMode: CredentialNone}
	_ = BackendExecutionProfile{Class: BackendExecutionInference}
	_ = BackendExecutionProfile{Class: BackendExecutionAgentRuntime}
	_ = ErrInvalidBackendExecutionClass
}

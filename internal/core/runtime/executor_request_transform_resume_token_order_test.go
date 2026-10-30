package runtime_test

// Task 3.2 runtime-level pin (spec .kiro/specs/proxy-memory-commercial,
// Requirement 3.3, design §3): the request-transform stage runs BEFORE the
// resume token is attached to the CLIENT call.
//
// The closure of the first-turn pending-decision rule rests on this ordering.
// The producer's put-to-admit interval closes INSIDE the request transform, so
// a resume token — the only binder that can point a second request at the same
// A-leg — cannot exist before that interval closed. A later turn therefore can
// never observe a pending (cached, not published, not durable) decision.
//
// WHICH OBJECT IS OBSERVED. The request transform receives the runtime's
// WORKING CLONE of the call, not the client call the caller passed to Execute.
// The real resume-token attachment writes the CLIENT call's session. Only the
// original client pointer can therefore observe the client-binding order this
// case pins, so the transform records the original client's token and, in
// addition, the working clone's token, so the two are never confused again.
// Both are read at transform time, on the SAME goroutine that runs Execute, so
// no shared-goroutine unsafety is involved.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// ftTokenTransform records the resume token of the ORIGINAL client call, and
// of the working clone the transform receives, at the moment the
// request-transform stage runs.
type ftTokenTransform struct {
	// client is the original call the caller passed to Execute.
	client *aipapi.Call

	mu sync.Mutex
	// seen reports that the transform ran.
	seen bool
	// clientToken is the original client's resume token at transform time.
	clientToken string
	// workingToken is the working clone's resume token at transform time.
	workingToken string
	// sameObject reports that the transform received the original call
	// itself instead of a distinct working clone.
	sameObject bool
}

func (t *ftTokenTransform) ID() string                        { return "ft-token-transform" }
func (t *ftTokenTransform) Order() int                        { return 0 }
func (t *ftTokenTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (t *ftTokenTransform) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen = true
	t.clientToken = t.client.Session.ResumeToken
	t.workingToken = call.Session.ResumeToken
	t.sameObject = call == t.client
	return nil
}

func (t *ftTokenTransform) observed() (seen bool, clientToken, workingToken string, sameObject bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen, t.clientToken, t.workingToken, t.sameObject
}

// TestRequestTransformRunsBeforeTheResumeTokenIsAttached pins the ordering the
// first-turn decision closure depends on.
func TestRequestTransformRunsBeforeTheResumeTokenIsAttached(t *testing.T) {
	t.Parallel()
	transform := &ftTokenTransform{}
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return pdFinishingStream(), nil
			},
		},
	}, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{transform},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	transform.client = call
	stream, err := ex.Execute(principalCtx("user-ft-order"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}

	seen, clientToken, workingToken, sameObject := transform.observed()
	if !seen {
		t.Fatal("the request-transform stage did not run")
	}
	if sameObject {
		t.Fatal("the request-transform stage must receive the runtime's working clone, not the client call itself")
	}
	if clientToken != "" {
		t.Fatalf("the resume token must not be attached to the client call before the request-transform stage, got %q", clientToken)
	}
	if workingToken != "" {
		t.Fatalf("the working clone must not carry a resume token at transform time, got %q", workingToken)
	}
	// The live post-turn difference: the client call carries the token the
	// real manager issued, so the empty observation above is a real "not yet
	// attached" and not a constant unset value.
	if strings.TrimSpace(call.Session.ResumeToken) == "" {
		t.Fatal("the executor must attach the resume token to the client call after the transform")
	}
}

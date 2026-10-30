package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type sequenceRng struct {
	mu   sync.Mutex
	vals []int
	idx  int
}

func (r *sequenceRng) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || len(r.vals) == 0 {
		return 0
	}
	v := r.vals[r.idx%len(r.vals)]
	r.idx++
	if v < 0 {
		v = -v
	}
	return v % n
}

func TestExecutorSessionAffinityBindsAfterOutputCommitAndReusesBackend(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	opens := map[string]int{"a": 0, "b": 0}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = &sequenceRng{vals: []int{1, 0}}
	ex.AffinityStore = affStore
	ex.Backends = affinityTestBackends(opens, nil, "a")
	clientID := uniqueSessionID(t)
	call := affinityTestCall("{session_sticky}[weight=1]a:m^[weight=1]b:m", clientID)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	resumeToken := call.Session.ResumeToken
	if opens["b"] != 1 || opens["a"] != 0 {
		t.Fatalf("first opens: %+v", opens)
	}
	next := affinityTestCall("{session_sticky}[weight=1]a:m^[weight=1]b:m", clientID)
	next.Session.AuthoritativeSessionID = call.Session.AuthoritativeSessionID
	next.Session.ResumeToken = resumeToken
	stream, err = ex.Execute(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opens["b"] != 2 || opens["a"] != 0 {
		t.Fatalf("sticky opens: %+v", opens)
	}
}

func TestExecutorClientAffinitySpansDistinctSessionsForPrincipal(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	opens := map[string]int{"a": 0, "b": 0}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = &sequenceRng{vals: []int{1, 0}}
	ex.AffinityStore = affStore
	ex.Backends = affinityTestBackends(opens, nil, "a")
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: uniqueSessionID(t)})
	for _, sessionID := range []string{"s1", "s2"} {
		stream, err := ex.Execute(ctx, affinityTestCall("{client_sticky}[weight=1]a:m^[weight=1]b:m", sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := aipapi.Collect(context.Background(), stream); err != nil {
			t.Fatal(err)
		}
	}
	if opens["b"] != 2 || opens["a"] != 0 {
		t.Fatalf("opens: %+v", opens)
	}
}

func TestExecutorAffinityResetsUnhealthyBindingAndRebindsReplacement(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	opens := map[string]int{"a": 0, "b": 0}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(0)
	ex.AffinityStore = affStore
	ex.CandidateHealth = policy.StaticUnhealthy{"b:m": {}}
	ex.Backends = affinityTestBackends(opens, nil, "a")
	clientID := uniqueSessionID(t)
	resume := prepareSessionResume(t, ex, clientID)
	key := affinity.Key{Scope: affinity.ScopeSession, ID: resume.authoritativeID}
	opens["a"] = 0
	if err := affStore.Set(context.Background(), affinity.Binding{Key: key, BackendID: "b", CandidateKey: "b:m"}); err != nil {
		t.Fatal(err)
	}
	call := affinityTestCall("{session_sticky}a:m|b:m", clientID)
	call.Session.AuthoritativeSessionID = key.ID
	call.Session.ResumeToken = resume.token
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opens["a"] != 1 || opens["b"] != 0 {
		t.Fatalf("opens: %+v", opens)
	}
	b, ok, err := affStore.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || b.BackendID != "a" {
		t.Fatalf("binding got (%+v,%v), want backend a", b, ok)
	}
}

func TestExecutorAffinityResetsContextIneligibleBinding(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	opens := map[string]int{"small": 0, "large": 0}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(0)
	ex.AffinityStore = affStore
	ex.RequestTokenEstimator = fixedRequestTokenEstimator{available: true, tokens: 11}
	ex.Backends = affinityTestBackends(opens, nil, "small", "a")
	clientID := uniqueSessionID(t)
	resume := prepareSessionResume(t, ex, clientID)
	key := affinity.Key{Scope: affinity.ScopeSession, ID: resume.authoritativeID}
	opens["small"] = 0
	if err := affStore.Set(context.Background(), affinity.Binding{Key: key, BackendID: "small", CandidateKey: "small:m"}); err != nil {
		t.Fatal(err)
	}
	call := affinityTestCall("{session_sticky}[max_context=10]small:m|large:m", clientID)
	call.Session.AuthoritativeSessionID = key.ID
	call.Session.ResumeToken = resume.token
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opens["large"] != 1 || opens["small"] != 0 {
		t.Fatalf("opens: %+v", opens)
	}
	b, ok, err := affStore.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || b.BackendID != "large" {
		t.Fatalf("binding got (%+v,%v), want backend large", b, ok)
	}
}

func TestExecutorAffinityDoesNotBindPreOutputFailures(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	temp := errors.New("temporary")
	opens := map[string]int{"bad": 0, "ok": 0}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(0)
	ex.AffinityStore = affStore
	ex.Backends = affinityTestBackends(opens, map[string]error{
		"bad": aipapi.RecoverablePreOutputError(temp),
	}, "a")
	clientID := uniqueSessionID(t)
	call := affinityTestCall("{session_sticky}bad:m|ok:m", clientID)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	key := affinity.Key{Scope: affinity.ScopeSession, ID: call.Session.AuthoritativeSessionID}
	b, ok, err := affStore.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || b.BackendID != "ok" {
		t.Fatalf("binding got (%+v,%v), want backend ok", b, ok)
	}
}

func TestExecutorAffinityRecordsRouteTrace(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	affStore := memorystore.New()
	opens := map[string]int{"a": 0, "b": 0}
	trace := diag.NewRouteTraceBuffer(8)
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = &sequenceRng{vals: []int{1}}
	ex.AffinityStore = affStore
	ex.RouteTrace = trace
	ex.Backends = affinityTestBackends(opens, nil, "a")
	stream, err := ex.Execute(context.Background(), affinityTestCall("{session_sticky}[weight=1]a:m^[weight=1]b:m", uniqueSessionID(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	entries := trace.Snapshot()
	foundBind := false
	foundTraceID := false
	for _, entry := range entries {
		if entry.Decision == "affinity_bind" && entry.Detail == "b" {
			foundBind = true
		}
		if entry.Decision == "affinity_bind" && entry.TraceID != "" {
			foundTraceID = true
		}
	}
	if !foundBind {
		t.Fatalf("missing affinity_bind route trace in %#v", entries)
	}
	if !foundTraceID {
		t.Fatalf("missing trace id on affinity route trace in %#v", entries)
	}
}

func TestExecutorAffinityMissingIdentityPolicy(t *testing.T) {
	t.Parallel()
	ex := runtime.TestExecutor()
	ex.AffinityMissingIdentity = affinity.MissingIdentityFailClosed
	_, _, err := ex.ResolveAffinityKeyForTest(routing.AffinitySession, execctx.Views{}, true)
	if !errors.Is(err, affinity.ErrIdentityRequired) {
		t.Fatalf("got %v want ErrIdentityRequired", err)
	}
	ex.AffinityMissingIdentity = affinity.MissingIdentityIgnore
	_, ok, err := ex.ResolveAffinityKeyForTest(routing.AffinitySession, execctx.Views{}, true)
	if err != nil || ok {
		t.Fatalf("ignore policy got ok=%v err=%v", ok, err)
	}
}

func affinityTestCall(route, sessionID string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: sessionID},
		Route:   aipapi.RouteIntent{Selector: route},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func affinityTestBackends(opens map[string]int, openErrors map[string]error, extra ...string) map[string]execbackend.Backend {
	for _, id := range extra {
		if _, ok := opens[id]; !ok {
			opens[id] = 0
		}
	}
	out := make(map[string]execbackend.Backend, len(opens))
	for id := range opens {
		out[id] = execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens[id]++
				if err := openErrors[id]; err != nil {
					return nil, err
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: id},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}
	}
	return out
}

func uniqueSessionID(t *testing.T) string {
	t.Helper()
	return strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
}

type sessionResume struct {
	authoritativeID string
	token           string
}

func prepareSessionResume(t *testing.T, ex *runtime.Executor, clientSessionID string) sessionResume {
	t.Helper()
	call := affinityTestCall("a:m", clientSessionID)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if call.Session.AuthoritativeSessionID == "" {
		t.Fatal("expected authoritative session id")
	}
	if call.Session.ResumeToken == "" {
		t.Fatal("expected resume token")
	}
	return sessionResume{authoritativeID: call.Session.AuthoritativeSessionID, token: call.Session.ResumeToken}
}

var _ runtime.RequestTokenEstimator = fixedRequestTokenEstimator{}

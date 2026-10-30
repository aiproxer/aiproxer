package geoip

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

type middlewareLookup struct {
	err error
}

func (l middlewareLookup) LookupCountry(netip.Addr) (string, bool, error) {
	if l.err != nil {
		return "", false, l.err
	}
	return "RU", true, nil
}

type middlewareObserver struct {
	reason coregeoip.Reason
	allow  bool
	calls  int
}

func (o *middlewareObserver) Decision(reason coregeoip.Reason, allow bool) {
	o.reason, o.allow, o.calls = reason, allow, o.calls+1
}

func TestMiddlewareDeniesBeforeDownstreamWithGeneric403(t *testing.T) {
	t.Parallel()

	policy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{Countries: []string{"RU"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := new(middlewareObserver)
	downstreamCalls := 0
	h := Middleware(Input{
		Policy: policy,
		Lookup: middlewareLookup{},
		Resolver: ResolverConfig{
			Source: SourceDirect,
		},
		Observer: observer,
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { downstreamCalls++ }))

	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "198.51.100.10:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Body.String() != "Forbidden\n" {
		t.Fatalf("response = %d %q, want generic 403", rec.Code, rec.Body.String())
	}
	if downstreamCalls != 0 {
		t.Fatalf("downstream calls = %d, want 0", downstreamCalls)
	}
	if observer.calls != 1 || observer.allow || observer.reason != coregeoip.ReasonCountryDeny {
		t.Fatalf("observer = %+v, want country deny", *observer)
	}
}

func TestMiddlewareAllowsAndPreservesDownstreamResponse(t *testing.T) {
	t.Parallel()

	policy, err := coregeoip.Compile(coregeoip.CompileInput{Order: coregeoip.OrderDenyAllow})
	if err != nil {
		t.Fatal(err)
	}
	downstreamCalls := 0
	h := Middleware(Input{Policy: policy, Resolver: ResolverConfig{Source: SourceDirect}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls++
		w.Header().Set("X-Downstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "198.51.100.10:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || rec.Body.String() != "ok" || rec.Header().Get("X-Downstream") != "yes" {
		t.Fatalf("response = %d %q headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
	if downstreamCalls != 1 {
		t.Fatalf("downstream calls = %d, want 1", downstreamCalls)
	}
}

func TestMiddlewareClientIPAndLookupFailuresDeny(t *testing.T) {
	t.Parallel()

	policy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{Countries: []string{"RU"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		remote string
		lookup coregeoip.CountryLookup
		want   coregeoip.Reason
	}{
		{name: "client ip", remote: "not-an-ip:443", lookup: middlewareLookup{}, want: coregeoip.ReasonClientIPError},
		{name: "lookup", remote: "198.51.100.10:443", lookup: middlewareLookup{err: errors.New("decode")}, want: coregeoip.ReasonLookupError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observer := new(middlewareObserver)
			h := Middleware(Input{Policy: policy, Lookup: tc.lookup, Resolver: ResolverConfig{Source: SourceDirect}, Observer: observer}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("downstream was called")
			}))
			req := httptest.NewRequest("GET", "http://example.test", nil)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || observer.reason != tc.want {
				t.Fatalf("response=%d observer=%+v, want 403/%s", rec.Code, *observer, tc.want)
			}
		})
	}
}

func TestMiddlewareNilPolicyIsDisabledFastPath(t *testing.T) {
	t.Parallel()

	called := false
	h := Middleware(Input{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "invalid hostname"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("disabled middleware did not delegate")
	}
}

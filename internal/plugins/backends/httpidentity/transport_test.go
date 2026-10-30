package httpidentity_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/httpidentity"
)

func TestTransport_modesOnWire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		policy     identity.FieldPolicy
		callUA     *string // nil = background; non-nil = call path (may be empty)
		wantHeader bool
		wantValue  string
	}{
		{
			name:       "ID-001_proxy_default_literal",
			policy:     identity.FieldPolicy{Mode: identity.ModeProxy},
			wantHeader: true,
			wantValue:  "aiproxer",
		},
		{
			name:       "empty_mode_means_proxy_literal",
			policy:     identity.FieldPolicy{},
			wantHeader: true,
			wantValue:  "aiproxer",
		},
		{
			name:       "ID-010_passthrough_exact",
			policy:     identity.FieldPolicy{Mode: identity.ModePassthrough},
			callUA:     new("Cursor/1.2"),
			wantHeader: true,
			wantValue:  "Cursor/1.2",
		},
		{
			name:       "passthrough_missing_on_call_omits",
			policy:     identity.FieldPolicy{Mode: identity.ModePassthrough},
			callUA:     new(""),
			wantHeader: false,
		},
		{
			name:       "passthrough_background_uses_proxy_identity",
			policy:     identity.FieldPolicy{Mode: identity.ModePassthrough},
			wantHeader: true,
			wantValue:  "aiproxer",
		},
		{
			name:       "custom_exact",
			policy:     identity.FieldPolicy{Mode: identity.ModeCustom, Value: "DiagAgent/9"},
			wantHeader: true,
			wantValue:  "DiagAgent/9",
		},
		{
			name:       "ID-030_drop_omits_including_go_default",
			policy:     identity.FieldPolicy{Mode: identity.ModeDrop},
			wantHeader: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sawUA string
			var sawPresent bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, sawPresent = r.Header["User-Agent"]
				sawUA = r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			client := httpidentity.WrapClient(srv.Client(), tc.policy)
			ctx := context.Background()
			if tc.callUA != nil {
				ctx = identity.WithClientUserAgent(ctx, *tc.callUA)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate SDK pre-setting a User-Agent that must be replaced or dropped.
			req.Header.Set("User-Agent", "sdk-default/0")
			req.Header.Set("Authorization", "Bearer secret-token")
			req.Header.Set("X-Extra", "keep-me")
			origUA := req.Header.Get("User-Agent")
			origAuth := req.Header.Get("Authorization")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			if req.Header.Get("User-Agent") != origUA {
				t.Fatalf("original request User-Agent mutated: %q", req.Header.Get("User-Agent"))
			}
			if req.Header.Get("Authorization") != origAuth {
				t.Fatal("original Authorization mutated")
			}
			if sawPresent != tc.wantHeader {
				t.Fatalf("User-Agent present=%v want %v (value=%q)", sawPresent, tc.wantHeader, sawUA)
			}
			if tc.wantHeader && sawUA != tc.wantValue {
				t.Fatalf("User-Agent=%q want %q", sawUA, tc.wantValue)
			}
			if !tc.wantHeader && sawUA != "" {
				t.Fatalf("expected omitted User-Agent, got %q", sawUA)
			}
		})
	}
}

func TestTransport_dropSuppressesBareGoDefault(t *testing.T) {
	t.Parallel()
	var sawPresent bool
	var sawUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawPresent = r.Header["User-Agent"]
		sawUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModeDrop})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if sawPresent {
		t.Fatalf("drop must suppress User-Agent including Go-http-client/1.1, got %q", sawUA)
	}
}

func TestTransport_preservesNonIdentityHeadersAndNotCredentials(t *testing.T) {
	t.Parallel()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModeProxy})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-secret")
	req.Header.Set("X-Api-Key", "key-secret")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("X-Request-Id", "req-1")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got.Get("Authorization") != "Bearer sk-secret" {
		t.Fatalf("Authorization: %q", got.Get("Authorization"))
	}
	if got.Get("X-Api-Key") != "key-secret" {
		t.Fatalf("X-Api-Key: %q", got.Get("X-Api-Key"))
	}
	if got.Get("Cookie") != "session=abc" {
		t.Fatalf("Cookie: %q", got.Get("Cookie"))
	}
	if got.Get("X-Request-Id") != "req-1" {
		t.Fatalf("X-Request-Id: %q", got.Get("X-Request-Id"))
	}
	if got.Get("User-Agent") != "aiproxer" {
		t.Fatalf("User-Agent: %q", got.Get("User-Agent"))
	}
}

func TestTransport_doesNotMutateSharedClient(t *testing.T) {
	t.Parallel()
	base := &http.Client{Timeout: 12 * time.Second, Transport: http.DefaultTransport}
	origTimeout := base.Timeout
	origRT := base.Transport
	wrapped := httpidentity.WrapClient(base, identity.FieldPolicy{Mode: identity.ModeProxy})
	if base.Transport != origRT {
		t.Fatal("WrapClient mutated shared client Transport")
	}
	if base.Timeout != origTimeout {
		t.Fatal("WrapClient mutated shared client Timeout")
	}
	if wrapped == base {
		t.Fatal("WrapClient must return a distinct client")
	}
	if wrapped.Transport == base.Transport {
		t.Fatal("wrapped transport must differ from base")
	}
}

func TestTransport_cancellationPropagates(t *testing.T) {
	t.Parallel()
	const hangLimit = 5 * time.Second
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModeProxy})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			errCh <- err
			return
		}
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(hangLimit):
		t.Fatal("timed out waiting for handler to start")
	}
	cancel()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(hangLimit):
		t.Fatal("timed out waiting for cancelled Do to return")
	}
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want context.Canceled", err)
	}
}

func TestTransport_underlyingErrorIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("upstream boom")
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	})
	client := httpidentity.WrapClient(&http.Client{Transport: rt}, identity.FieldPolicy{Mode: identity.ModeProxy})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v want sentinel", err)
	}
}

func TestTransport_concurrencyIsolation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("User-Agent")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModePassthrough})
	const n = 32
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			ua := "Agent-" + string(rune('A'+i%26))
			ctx := identity.WithClientUserAgent(context.Background(), ua)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				errCh <- err
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected multiple distinct UAs, got %#v", seen)
	}
	for ua, count := range seen {
		if ua == "" || count < 1 {
			t.Fatalf("bad entry %#v", seen)
		}
	}
}

func TestTransport_sharedTransportReuse(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       http.NoBody,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	shared := &http.Client{Transport: base}
	a := httpidentity.WrapClient(shared, identity.FieldPolicy{Mode: identity.ModeProxy})
	b := httpidentity.WrapClient(shared, identity.FieldPolicy{Mode: identity.ModeCustom, Value: "Other/1"})
	for _, c := range []*http.Client{a, b, a} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if hits.Load() != 3 {
		t.Fatalf("hits=%d want 3", hits.Load())
	}
}

func TestWrapClient_nilSafe(t *testing.T) {
	t.Parallel()
	if httpidentity.WrapClient(nil, identity.FieldPolicy{Mode: identity.ModeProxy}) != nil {
		t.Fatal("nil client should stay nil")
	}
}

func TestTransport_passthroughRevalidatesClientUserAgent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ua   string
	}{
		{name: "crlf", ua: "bad\r\nagent"},
		{name: "nul", ua: "bad\x00agent"},
		{name: "control", ua: "bad\x01agent"},
		{name: "overlong", ua: strings.Repeat("a", identity.MaxUserAgentBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sawPresent bool
			var sawUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, sawPresent = r.Header["User-Agent"]
				sawUA = r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModePassthrough})
			ctx := identity.WithClientUserAgent(context.Background(), tc.ua)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if sawPresent {
				t.Fatalf("invalid call UA must omit User-Agent, got present value %q", sawUA)
			}
		})
	}
}

func TestTransport_backgroundIdentityOverridesCallPassthrough(t *testing.T) {
	t.Parallel()
	var sawUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModePassthrough})
	ctx := identity.WithClientUserAgent(context.Background(), "ClientMustNotAppear/1")
	ctx = identity.WithBackgroundIdentity(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if sawUA != "aiproxer" {
		t.Fatalf("User-Agent=%q want proxy identity", sawUA)
	}
}

func TestTransport_dropCustomProxyIgnorePurpose(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		policy     identity.FieldPolicy
		wantHeader bool
		wantUA     string
	}{
		{name: "proxy", policy: identity.FieldPolicy{Mode: identity.ModeProxy}, wantHeader: true, wantUA: "aiproxer"},
		{name: "custom", policy: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "Fixed/1"}, wantHeader: true, wantUA: "Fixed/1"},
		{name: "drop", policy: identity.FieldPolicy{Mode: identity.ModeDrop}, wantHeader: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sawPresent bool
			var sawUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, sawPresent = r.Header["User-Agent"]
				sawUA = r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			client := httpidentity.WrapClient(srv.Client(), tc.policy)
			ctx := identity.WithClientUserAgent(context.Background(), "IgnoredClient/1")
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if sawPresent != tc.wantHeader {
				t.Fatalf("present=%v want %v value=%q", sawPresent, tc.wantHeader, sawUA)
			}
			if tc.wantHeader && sawUA != tc.wantUA {
				t.Fatalf("User-Agent=%q want %q", sawUA, tc.wantUA)
			}
		})
	}
}

// Mutation-kill: call-path passthrough with empty UA must omit (not emit product proxy identity).
func TestTransport_mutationKill_callPathPassthroughOmitsEmpty(t *testing.T) {
	t.Parallel()
	var sawPresent bool
	var sawUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawPresent = r.Header["User-Agent"]
		sawUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := httpidentity.WrapClient(srv.Client(), identity.FieldPolicy{Mode: identity.ModePassthrough})
	ctx := identity.WithClientUserAgent(context.Background(), "")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if sawPresent {
		t.Fatalf("empty call-path passthrough must omit User-Agent, got %q (must not become proxy default)", sawUA)
	}
	if sawUA == "aiproxer" {
		t.Fatal("mutation: missing passthrough collapsed into proxy identity")
	}
}

// ID-147: redirect follow must not restore SDK/Go User-Agent after identity transport applies policy.
func TestTransport_ID147_redirectKeepsPolicyIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		policy      identity.FieldPolicy
		wantPresent bool
		wantUA      string
	}{
		{
			name:        "proxy",
			policy:      identity.FieldPolicy{Mode: identity.ModeProxy},
			wantPresent: true,
			wantUA:      "aiproxer",
		},
		{
			name:        "custom",
			policy:      identity.FieldPolicy{Mode: identity.ModeCustom, Value: "RedirectAgent/9"},
			wantPresent: true,
			wantUA:      "RedirectAgent/9",
		},
		{
			name:        "drop",
			policy:      identity.FieldPolicy{Mode: identity.ModeDrop},
			wantPresent: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var (
				finalPresent bool
				finalUA      string
				hopCount     int
			)
			mux := http.NewServeMux()
			mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
				hopCount++
				_, finalPresent = r.Header["User-Agent"]
				finalUA = r.Header.Get("User-Agent")
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/final", http.StatusFound)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			base := srv.Client()
			client := httpidentity.WrapClient(base, tc.policy)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("User-Agent", "sdk-default/0")
			req.Header.Set("Authorization", "Bearer secret-token")
			origUA := req.Header.Get("User-Agent")
			origAuth := req.Header.Get("Authorization")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if hopCount != 1 {
				t.Fatalf("final handler hops=%d want 1", hopCount)
			}
			if req.Header.Get("User-Agent") != origUA {
				t.Fatalf("original request User-Agent mutated: %q", req.Header.Get("User-Agent"))
			}
			if req.Header.Get("Authorization") != origAuth {
				t.Fatal("original Authorization mutated")
			}
			if finalPresent != tc.wantPresent {
				t.Fatalf("final User-Agent present=%v want %v (value=%q)", finalPresent, tc.wantPresent, finalUA)
			}
			if tc.wantPresent && finalUA != tc.wantUA {
				t.Fatalf("final User-Agent=%q want %q", finalUA, tc.wantUA)
			}
			if !tc.wantPresent && (finalUA != "" || strings.HasPrefix(finalUA, "Go-http-client/")) {
				t.Fatalf("drop after redirect must suppress Go default, got present=%v value=%q", finalPresent, finalUA)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

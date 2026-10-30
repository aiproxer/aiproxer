package modelsdev_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
)

func TestHTTPSource_fetchSuccess(t *testing.T) {
	t.Parallel()
	body := `{"z":{"id":"z","models":[{"id":"a","tool_call":true}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	src := modelsdev.NewHTTPSource(srv.Client(), srv.URL, true, 0)
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Index == nil {
		t.Fatal("nil index")
	}
	f, ok := snap.Index.FactsByCatalogModelID("z/a")
	if !ok || f.Tools != modelcatalog.CapabilitySupported {
		t.Fatalf("facts: ok=%v tools=%v", ok, f.Tools)
	}
}

func TestHTTPSource_nilResponseBody(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: nilBodyOKTransport{}}
	src := modelsdev.NewHTTPSource(client, "http://example.invalid/catalog", true, 0)
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for nil response body")
	}
}

type nilBodyOKTransport struct{}

func (nilBodyOKTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       nil,
	}, nil
}

func TestHTTPSource_httpError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	src := modelsdev.NewHTTPSource(srv.Client(), srv.URL, true, 0)
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPSource_fetchTimeout_appliesWhenNoParentDeadline(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: blockUntilCancelTransport{}}
	src := modelsdev.NewHTTPSource(client, "http://example.invalid/catalog.json", true, 40*time.Millisecond)
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected timeout or cancel error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

type blockUntilCancelTransport struct{}

func (blockUntilCancelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestHTTPSource_bodyTooLarge(t *testing.T) {
	t.Parallel()
	// Fetch caps response bytes (64 MiB + 1 triggers rejection); stream junk without allocating it.
	junk := io.LimitReader(infiniteJunkReader{}, (64<<20)+256)
	client := &http.Client{Transport: oversizeCatalogTransport{body: io.NopCloser(junk)}}
	src := modelsdev.NewHTTPSource(client, "http://example.invalid/catalog.json", true, 0)
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want exceeds error, got %v", err)
	}
}

type infiniteJunkReader struct{}

func (infiniteJunkReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

type oversizeCatalogTransport struct{ body io.ReadCloser }

func (t oversizeCatalogTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       t.body,
	}, nil
}

func TestHTTPSource_disabledNoRoundTrip(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be called")
	}))
	t.Cleanup(srv.Close)

	rt := &failingRoundTripper{t: t}
	client := &http.Client{Transport: rt}
	src := modelsdev.NewHTTPSource(client, srv.URL, false, 0)
	_, err := src.Fetch(context.Background())
	if !errors.Is(err, modelsdev.ErrExternalUpdatesDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestHTTPSource_noAuthorizationHeader(t *testing.T) {
	t.Parallel()
	body := `{"z":{"id":"z","models":[{"id":"a"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected Authorization header")
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Fatal("unexpected X-Api-Key header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: &captureAuthTransport{inner: http.DefaultTransport, t: t}}
	src := modelsdev.NewHTTPSource(client, srv.URL, true, 0)
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type failingRoundTripper struct{ t *testing.T }

func (f *failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("RoundTrip should not be invoked when updates disabled")
	return nil, nil
}

type captureAuthTransport struct {
	inner http.RoundTripper
	t     *testing.T
}

func (c *captureAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		c.t.Fatal("client added Authorization")
	}
	if c.inner == nil {
		c.inner = http.DefaultTransport
	}
	return c.inner.RoundTrip(req)
}

package bedrock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewRuntimeClient_staticCredsAndEndpoint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	}
	cli, err := newRuntimeClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewRuntimeClient_nilContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	}
	_, err := newRuntimeClient(nil, cfg) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestBackend_Open_nilContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), DefaultLoadConfigTimeout)
	defer cancel()
	b := NewWithContext(ctx, Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: ID, Model: "m"},
	}
	_, err := b.Open(nil, call, cand)
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestNewWithContext_nilAppliesDefaultLoadDeadline(t *testing.T) {
	t.Parallel()
	b := NewWithContext(nil, Config{ //nolint:staticcheck // nil ctx: load uses DefaultLoadConfigTimeout
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
	})
	if b.Open == nil {
		t.Fatal("expected backend with Open")
	}
}

func TestNewWithContext_parentWithDeadlineSkipsChildTimeout(t *testing.T) {
	t.Parallel()
	// httptest + static creds so config load stays local; use generous parent deadline
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := NewWithContext(ctx, Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	})
	if b.Open == nil {
		t.Fatal("expected backend with Open")
	}
}

package anthropic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNew_rejectsEmptyBaseURL(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "", APIKey: "k"})
	_, err := be.Open(context.Background(), aipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "https://example.com", APIKey: "k"})
	_, err := be.Open(nil, aipapi.Call{}, routing.AttemptCandidate{})
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

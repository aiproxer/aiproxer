package gemini

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "https://example.com", APIKey: "k"})
	_, err := be.Open(nil, aipapi.Call{}, routing.AttemptCandidate{})
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

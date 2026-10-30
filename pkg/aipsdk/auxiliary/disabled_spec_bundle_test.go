package auxiliary_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestDisabledClient_errorsNotConfigured(t *testing.T) {
	t.Parallel()
	var c auxiliary.Client = auxiliary.DisabledClient{}
	_, err := c.Collect(context.Background(), auxiliary.Request{Call: &aipapi.Call{}})
	if !errors.Is(err, auxiliary.ErrNotConfigured) {
		t.Fatalf("collect: %v", err)
	}
	_, err = c.Stream(context.Background(), auxiliary.Request{Call: &aipapi.Call{}})
	if !errors.Is(err, auxiliary.ErrNotConfigured) {
		t.Fatalf("stream: %v", err)
	}
}

func TestErrNotConfigured_isStable(t *testing.T) {
	t.Parallel()
	if auxiliary.ErrNotConfigured.Error() == "" {
		t.Fatal("empty error text")
	}
}

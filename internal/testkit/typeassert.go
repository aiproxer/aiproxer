package testkit

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// MustAIPCall returns v as aipapi.Call or fails the test.
func MustAIPCall(t *testing.T, v any) aipapi.Call {
	t.Helper()
	c, ok := v.(aipapi.Call)
	if !ok {
		t.Fatalf("expected aipapi.Call, got %T", v)
	}
	return c
}

// MustMapStringAny returns v as map[string]any or fails the test.
func MustMapStringAny(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", v)
	}
	return m
}

// MustSliceAny returns v as []any or fails the test.
func MustSliceAny(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", v)
	}
	return s
}

package backendplugin_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestRawJSONDepth_ignoresBracketsInsideStrings(t *testing.T) {
	t.Parallel()
	// Brackets inside a JSON string must not inflate structural depth.
	payload := backendplugin.RawJSONFromBytes([]byte(`{"note":"value with { brace [ bracket","n":1}`))
	if err := payload.Validate(backendplugin.DefaultMaxRawJSONBytes); err != nil {
		t.Fatalf("expected valid JSON with brackets in string, got %v", err)
	}
}

func TestRawJSONDepth_rejectsDeepStructure(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	payload := backendplugin.RawJSONFromBytes([]byte(deep))
	if err := payload.Validate(backendplugin.DefaultMaxRawJSONBytes); err == nil {
		t.Fatal("expected deep JSON rejection")
	}
}

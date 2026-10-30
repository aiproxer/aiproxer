package config

import (
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestParseToolReactorErrorPolicy(t *testing.T) {
	t.Parallel()
	if ParseToolReactorErrorPolicy("") != sdk.ToolReactorErrorsFailOpen {
		t.Fatal("default")
	}
	if ParseToolReactorErrorPolicy("fail_closed") != sdk.ToolReactorErrorsFailClosed {
		t.Fatal("closed")
	}
	if ParseToolReactorErrorPolicy("Swallow") != sdk.ToolReactorErrorsSwallowEvent {
		t.Fatal("swallow")
	}
}

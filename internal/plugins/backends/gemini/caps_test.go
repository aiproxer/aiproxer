package gemini_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestModelCapabilities_modernGemini(t *testing.T) {
	t.Parallel()
	c := gemini.ModelCapabilities("gemini-2.0-flash")
	if _, ok := c[aipapi.CapabilityVision]; !ok {
		t.Fatal("expected vision for gemini-2.0-flash")
	}
}

func TestModelCapabilities_gemini10ProText(t *testing.T) {
	t.Parallel()
	c := gemini.ModelCapabilities("gemini-1.0-pro")
	if _, ok := c[aipapi.CapabilityVision]; ok {
		t.Fatal("did not expect vision for gemini-1.0-pro text id")
	}
	if _, ok := c[aipapi.CapabilityTools]; !ok {
		t.Fatal("expected tools")
	}
}

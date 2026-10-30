//go:build integration

package conformance

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

// Task 13.1 / 13.3: bundled frontend IDs must stay aligned with wire_clients.go and frontend_server.go
// so conformance always exercises official reference clients against the proxy surface.
func TestSanityTask13_BundledFrontendsWiredForConformance(t *testing.T) {
	t.Parallel()
	wired := map[string]struct{}{
		"openai-responses": {},
		"openai-legacy":    {},
		"anthropic":        {},
		"gemini":           {},
		"openresponses":    {},
	}
	ids := BundledFrontendIDs()
	if len(ids) != len(wired) {
		t.Fatalf("BundledFrontendIDs count %d != wired frontends %d — update wire_clients.go MountFrontend and this map", len(ids), len(wired))
	}
	for _, id := range ids {
		if _, ok := wired[id]; !ok {
			t.Fatalf("BundledFrontendIDs contains %q not wired in wire_clients.go / MountFrontend", id)
		}
		delete(wired, id)
	}
	if len(wired) != 0 {
		t.Fatalf("extra wired frontend ids not in BundledFrontendIDs: %v", keysStringSet(wired))
	}
}

// Task 13.2 / 13.3: bundled backend IDs must match refparity NewSuccessRefBackend / BackendFor wiring.
func TestSanityTask13_BundledBackendsWiredForRefParity(t *testing.T) {
	t.Parallel()
	wired := map[string]struct{}{
		openairesponses.ID: {},
		openailegacy.ID:    {},
		anthropic.ID:       {},
		gemini.ID:          {},
		bedrock.ID:         {},
		"acp":              {},
		"openresponses":    {},
		"openrouter":       {},
		"nvidia":           {},
	}
	ids := BundledBackendIDs()
	if len(ids) != len(wired) {
		t.Fatalf("BundledBackendIDs count %d != wired backends %d — update refparity.go harness.go and plugin IDs", len(ids), len(wired))
	}
	for _, id := range ids {
		if _, ok := wired[id]; !ok {
			t.Fatalf("BundledBackendIDs contains %q not covered by ref-backend parity / BackendFor", id)
		}
		delete(wired, id)
	}
	if len(wired) != 0 {
		t.Fatalf("extra wired backend ids not in BundledBackendIDs: %v", keysStringSet(wired))
	}
}

func keysStringSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

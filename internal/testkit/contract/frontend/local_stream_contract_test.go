package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/localstream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// TestLocalStream_FrontendContract_StreamingAndNonStreaming proves the generic
// local canonical stream encodes legally on representative official frontends
// (streaming OpenResponses and non-streaming OpenAI legacy) and that
// replay/decoded assistant message identity equals the tagged canonical reply.
// It is intentionally bounded (2 frontends, not a Cartesian matrix) per task 3.4.
func TestLocalStream_FrontendContract_StreamingAndNonStreaming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		mount     funcFrontendMount
		path      string
		body      []byte
		streaming bool
		decodeFn  func(*testing.T, *httptest.ResponseRecorder) string
	}{
		{
			name:      "openresponses-streaming",
			mount:     openresponses.Mount,
			path:      "/openresponses/v1/responses",
			body:      []byte(`{"model":"m","input":"hi","stream":true}`),
			streaming: true,
			decodeFn: func(t *testing.T, rec *httptest.ResponseRecorder) string {
				t.Helper()
				body := rec.Body.String()
				if !strings.Contains(body, "response.output_text.delta") && !strings.Contains(body, "output_text") {
					t.Fatalf("streaming body missing text delta, got %q", body)
				}
				// Extract delta payloads: look for "delta":"...".
				// Simple containment check is sufficient for identity proof because
				// the canonical text is validated via aipapi.Collect below; wire
				// containment just ensures frontend emitted it.
				return extractDeltaFromSSE(t, body)
			},
		},
		{
			name:      "openai-legacy-non-streaming",
			mount:     openailegacy.Mount,
			path:      "/v1/chat/completions",
			body:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`),
			streaming: false,
			decodeFn: func(t *testing.T, rec *httptest.ResponseRecorder) string {
				t.Helper()
				var resp struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode legacy response: %v body=%q", err, rec.Body.String())
				}
				if len(resp.Choices) == 0 {
					t.Fatalf("no choices in legacy response: %q", rec.Body.String())
				}
				return resp.Choices[0].Message.Content
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replyText := "local-contract-" + tc.name + "-reply"
			// Tagged canonical identity (same construction as runtime tags).
			taggedMsg := localstream.CanonicalAssistantMessage(replyText)
			taggedID, err := conversationprojection.MessageIdentityOf(taggedMsg)
			if err != nil {
				t.Fatalf("tagged identity: %v", err)
			}
			// Validate generic stream is legal and replay-decodable to same identity.
			events := localstream.Events(replyText)
			if err := aipapi.ValidateEventSequence(events); err != nil {
				t.Fatalf("ValidateEventSequence: %v", err)
			}
			col, err := aipapi.Collect(context.Background(), localstream.NewTextStream(replyText))
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if col.Text.String() != replyText {
				t.Fatalf("collected %q want %q", col.Text.String(), replyText)
			}
			if col.InputTokens != 0 || col.OutputTokens != 0 || len(col.ToolArgs) != 0 {
				t.Fatalf("local stream must have zero usage/tool, got %+v", col)
			}
			if !col.FinishReceived {
				t.Fatalf("FinishReceived must be true")
			}
			replayMsg := localstream.CanonicalAssistantMessage(col.Text.String())
			replayID, err := conversationprojection.MessageIdentityOf(replayMsg)
			if err != nil {
				t.Fatalf("replay identity: %v", err)
			}
			if taggedID != replayID {
				t.Fatalf("replay identity %s != tagged %s", replayID, taggedID)
			}
			// Item-authority replay also equivalent.
			itemID, err := conversationprojection.ItemIdentityOf(localstream.CanonicalAssistantItem(col.Text.String()))
			if err != nil {
				t.Fatalf("item identity: %v", err)
			}
			if itemID != taggedID {
				t.Fatalf("item identity %s != tagged %s", itemID, taggedID)
			}

			// Frontend encoding: same stream must encode legally via official frontend.
			executor := &CapturingExecutor{Script: EventScript{Events: events}}
			subject := semantic.SubjectDescriptor{
				ID:           tc.name,
				Kind:         semantic.KindFrontend,
				Capabilities: getFrontendCapabilitiesForContract(tc.name),
				Dialects:     getFrontendDialects(tc.name),
				Transports:   []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming},
			}
			h := &MountedHarness{
				Descriptor:        subject,
				Mount:             tc.mount,
				Path:              func(semantic.ScenarioDescriptor) string { return tc.path },
				Body:              func(semantic.ScenarioDescriptor) []byte { return tc.body },
				ExecutorBoundary:  executor,
				ContinuationStore: aipcont.NewMemoryStore(),
				AuthProvider:      allowFrontendTCKAuth{},
			}
			if err := h.Reset(); err != nil {
				t.Fatalf("mount: %v", err)
			}
			// Direct ServeHTTP with scripted executor to capture wire response.
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer frontend-tck")
			req.Header.Set("X-AIP-Session-ID", "frontend-tck-session")
			rec := httptest.NewRecorder()
			h.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("frontend %s returned %d body=%q", tc.name, rec.Code, rec.Body.String())
			}
			wire := rec.Body.String()
			if strings.TrimSpace(wire) == "" {
				t.Fatal("empty wire response")
			}
			if tc.streaming && !bytes.Contains(rec.Body.Bytes(), []byte("data:")) {
				t.Fatalf("streaming frontend %s did not emit SSE", tc.name)
			}
			if !strings.Contains(wire, replyText) {
				t.Fatalf("wire response for %s does not contain reply text %q, body=%q", tc.name, replyText, wire)
			}
			// Decode wire back to assistant content and prove identity equality.
			decodedText := tc.decodeFn(t, rec)
			if decodedText != replyText {
				t.Fatalf("decoded text %q != reply %q", decodedText, replyText)
			}
			decodedMsg := localstream.CanonicalAssistantMessage(decodedText)
			decodedID, err := conversationprojection.MessageIdentityOf(decodedMsg)
			if err != nil {
				t.Fatalf("decoded identity: %v", err)
			}
			if decodedID != taggedID {
				t.Fatalf("decoded identity %s != tagged %s", decodedID, taggedID)
			}
			// Ensure CapturingExecutor saw canonical call (frontend accepted request).
			if len(executor.Calls) == 0 {
				t.Fatal("executor saw no calls")
			}
		})
	}
}

func getFrontendCapabilitiesForContract(name string) []aipapi.Capability {
	if name == "openresponses-streaming" {
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityOrderedItems,
		}
	}
	if name == "openai-legacy-non-streaming" {
		return []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools}
	}
	return getFrontendCapabilities(name)
}

func extractDeltaFromSSE(t *testing.T, body string) string {
	t.Helper()
	// Body contains lines like: data: {"type":"response.output_text.delta","delta":"local-contract-..."}
	// Extract first delta field that equals reply prefix.
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" || payload == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			continue
		}
		if raw, ok := m["delta"]; ok {
			var d string
			if err := json.Unmarshal(raw, &d); err == nil && d != "" {
				return d
			}
		}
		if raw, ok := m["text"]; ok {
			var d string
			if err := json.Unmarshal(raw, &d); err == nil && d != "" {
				return d
			}
		}
	}
	// Fallback: body contains replyText verbatim; caller already checked containment.
	// Return replyText extraction via direct substring search for known prefix.
	if idx := strings.Index(body, "local-contract-"); idx >= 0 {
		end := idx
		for end < len(body) && body[end] != '"' && body[end] != '\n' && body[end] != '}' {
			end++
		}
		if end > idx {
			// Try to extract quoted delta.
			start := strings.LastIndex(body[:idx], `"delta":"`)
			if start >= 0 {
				start += len(`"delta":"`)
				rest := body[start:]
				q := strings.Index(rest, `"`)
				if q > 0 {
					return rest[:q]
				}
			}
		}
	}
	t.Logf("could not extract delta, returning raw body contains check fallback; body=%q", body)
	// As fallback, return substring containing replyText.
	if idx := strings.Index(body, "local-contract-"); idx >= 0 {
		// find surrounding quotes
		rest := body[idx:]
		if nl := strings.IndexAny(rest, "\"\n,"); nl > 0 {
			return rest[:nl]
		}
		return rest
	}
	return ""
}

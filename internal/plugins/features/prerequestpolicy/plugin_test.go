package prerequestpolicy_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/prerequestpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

type collectText struct {
	text string
	reqs []auxiliary.Request
	err  error
}

func (a *collectText) Collect(_ context.Context, r auxiliary.Request) (aipapi.Collected, error) {
	a.reqs = append(a.reqs, r)
	if a.err != nil {
		return aipapi.Collected{}, a.err
	}
	var c aipapi.Collected
	_, _ = c.Text.WriteString(a.text)
	return c, nil
}

func (a *collectText) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, fmt.Errorf("unused")
}

func TestHandler_allowOnPatternDeniesUnlessAllowed(t *testing.T) {
	t.Parallel()
	cfg := prerequestpolicy.HandlerConfig{
		ID:                 "compliance",
		Prompt:             "Return ALLOW or DENY.",
		ModelRoutingString: "openai:gpt-4.1-mini",
		Policy:             prerequestpolicy.PolicyAllowOnPattern,
		AllowPattern:       `\bALLOW\b`,
		DenyMessage:        "blocked by corporate policy",
	}
	h, err := prerequestpolicy.NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	aux := &collectText{text: "DENY"}
	decision, err := h.Handle(context.Background(), validCall(), prerequest.Meta{TraceID: "tr1"}, prerequest.Services{Aux: aux})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Deny || decision.DenyMessage != "blocked by corporate policy" {
		t.Fatalf("decision = %+v", decision)
	}
	if len(aux.reqs) != 1 {
		t.Fatalf("aux requests: %d", len(aux.reqs))
	}
	if aux.reqs[0].Call.Route.Selector != "openai:gpt-4.1-mini" {
		t.Fatalf("route selector: %q", aux.reqs[0].Call.Route.Selector)
	}
	if got := aux.reqs[0].Call.Instructions[0].Parts[0].Text; got != cfg.Prompt {
		t.Fatalf("prompt = %q", got)
	}
	if strings.Contains(aux.reqs[0].Call.Messages[0].Parts[0].Text, "secret-token") {
		t.Fatal("auxiliary prompt leaked resume token")
	}
}

func TestHandler_denyOnPatternAllowsUnlessDenied(t *testing.T) {
	t.Parallel()
	h, err := prerequestpolicy.NewHandler(prerequestpolicy.HandlerConfig{
		ID:                 "sensitive-data",
		Prompt:             "Check sensitive data.",
		ModelRoutingString: "local:policy",
		Policy:             prerequestpolicy.PolicyDenyOnPattern,
		DenyPattern:        `\bDENY\b`,
		DenyMessage:        "contains sensitive data",
	})
	if err != nil {
		t.Fatal(err)
	}
	aux := &collectText{text: "ALLOW"}
	decision, err := h.Handle(context.Background(), validCall(), prerequest.Meta{}, prerequest.Services{Aux: aux})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Deny {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecodeConfig_loadsPromptFilesAndOrdersHandlers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compliance.md"), []byte("policy prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := prerequestpolicy.DecodeConfig(yamlNode(t, fmt.Sprintf(`
prompt_dir: %q
handlers:
  - id: later
    priority: 20
    prompt_filename: compliance.md
    model_routing_string: local:policy
    allow_pattern: ALLOW
    policy: allow_on_pattern
  - id: earlier
    priority: 1
    prompt_filename: compliance.md
    model_routing_string: local:policy
    deny_pattern: DENY
`, dir)))
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := prerequestpolicy.NewHandlers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(handlers) != 2 {
		t.Fatalf("handlers: %d", len(handlers))
	}
	if handlers[0].ID() != "earlier" || handlers[1].ID() != "later" {
		t.Fatalf("order: %s %s", handlers[0].ID(), handlers[1].ID())
	}
}

func TestDecodeConfig_rejectsUnsafePromptFilename(t *testing.T) {
	t.Parallel()
	_, err := prerequestpolicy.DecodeConfig(yamlNode(t, `
handlers:
  - prompt_filename: ../secret.md
    model_routing_string: local:policy
    deny_pattern: DENY
`))
	if err == nil || !strings.Contains(err.Error(), "prompt_filename") {
		t.Fatalf("expected prompt_filename error, got %v", err)
	}
}

func TestDecodeConfig_rejectsNonPositiveTimeout(t *testing.T) {
	t.Parallel()
	_, err := prerequestpolicy.DecodeConfig(yamlNode(t, `
handlers:
  - prompt_filename: policy.md
    model_routing_string: local:policy
    deny_pattern: DENY
    timeout: -1s
`))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout validation error, got %v", err)
	}
}

func validCall() *aipapi.Call {
	return &aipapi.Call{
		ID: "call",
		Session: aipapi.SessionRef{
			ResumeToken: "secret-token",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
}

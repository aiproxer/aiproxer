package prerequestpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

type handler struct {
	id       string
	order    int
	prompt   string
	route    string
	policy   string
	allow    *regexp.Regexp
	deny     *regexp.Regexp
	message  string
	timeout  string
	duration func(context.Context) (context.Context, context.CancelFunc)
}

var _ prerequest.Handler = (*handler)(nil)

// NewHandlers builds sorted pre-request handlers and loads configured prompt files.
func NewHandlers(cfg Config) ([]prerequest.Handler, error) {
	out := make([]prerequest.Handler, 0, len(cfg.Handlers))
	for i, hc := range cfg.Handlers {
		if hc.Prompt == "" {
			prompt, err := loadPrompt(cfg.PromptDir, hc.PromptFilename)
			if err != nil {
				return nil, err
			}
			hc.Prompt = prompt
		}
		h, err := NewHandler(hc)
		if err != nil {
			return nil, fmt.Errorf("%s: handlers[%d]: %w", ID, i, err)
		}
		out = append(out, h)
	}
	return prerequest.MaterializeSorted(out), nil
}

// NewHandler builds one policy handler. Tests may pass Prompt directly to avoid filesystem I/O.
func NewHandler(cfg HandlerConfig) (prerequest.Handler, error) {
	if err := normalizeHandlerConfig(&cfg, 0); err != nil {
		return nil, err
	}
	if cfg.Prompt == "" {
		return nil, fmt.Errorf("%s: prompt is required", ID)
	}
	var allow *regexp.Regexp
	var deny *regexp.Regexp
	var err error
	if cfg.AllowPattern != "" {
		allow, err = regexp.Compile(cfg.AllowPattern)
		if err != nil {
			return nil, fmt.Errorf("allow_pattern: %w", err)
		}
	}
	if cfg.DenyPattern != "" {
		deny, err = regexp.Compile(cfg.DenyPattern)
		if err != nil {
			return nil, fmt.Errorf("deny_pattern: %w", err)
		}
	}
	return &handler{
		id:      cfg.ID,
		order:   cfg.Priority,
		prompt:  cfg.Prompt,
		route:   cfg.ModelRoutingString,
		policy:  cfg.Policy,
		allow:   allow,
		deny:    deny,
		message: cfg.DenyMessage,
		duration: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, cfg.Timeout)
		},
	}, nil
}

func (h *handler) ID() string { return h.id }

func (h *handler) Order() int { return h.order }

func (h *handler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (h *handler) Handle(ctx context.Context, call *aipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	if svc.Aux == nil {
		return prerequest.Decision{}, auxiliary.ErrNotConfigured
	}
	auxCall, err := h.buildAuxCall(call)
	if err != nil {
		return prerequest.Decision{}, err
	}
	callCtx, cancel := h.duration(ctx)
	defer cancel()
	collected, err := svc.Aux.Collect(callCtx, auxiliary.Request{
		Role:           "pre_request",
		Visibility:     "internal",
		ParentTraceID:  meta.TraceID,
		ParentALegID:   meta.Session.ALegID,
		DisablePlugins: []string{h.ID()},
		Call:           auxCall,
	})
	if err != nil {
		return prerequest.Decision{}, err
	}
	text := collected.Text.String()
	switch h.policy {
	case PolicyAllowOnPattern:
		if h.allow == nil || !h.allow.MatchString(text) {
			return prerequest.Deny(h.message), nil
		}
	case PolicyDenyOnPattern:
		if h.deny != nil && h.deny.MatchString(text) {
			return prerequest.Deny(h.message), nil
		}
	}
	return prerequest.Allow(), nil
}

func (h *handler) buildAuxCall(call *aipapi.Call) (*aipapi.Call, error) {
	if call == nil {
		return nil, fmt.Errorf("%s: nil call", ID)
	}
	safe := aipapi.CloneCall(*call)
	safe.Session.ResumeToken = ""
	payload, err := json.Marshal(safe)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal canonical call: %w", ID, err)
	}
	return &aipapi.Call{
		ID:    safe.ID + ":pre-request:" + h.ID(),
		Route: aipapi.RouteIntent{Selector: h.route},
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(h.prompt)},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(string(payload))},
		}},
	}, nil
}

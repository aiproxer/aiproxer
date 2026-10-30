package reftool

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// ID is the feature plugin id for YAML registration.
const ID = "ref-tool-prefix"

const defaultOrder = 50

type hook struct {
	order  int
	prefix string
}

var _ sdk.ToolReactor = hook{}

// NewToolReactor prefixes tool argument deltas (rewrite path).
func NewToolReactor(cfg Config) sdk.ToolReactor {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	p := cfg.Prefix
	if p == "" {
		p = ">>"
	}
	return hook{order: o, prefix: p}
}

func (hook) ID() string   { return ID }
func (h hook) Order() int { return h.order }

func (h hook) HandleToolEvent(_ context.Context, te aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
	if te.Kind != aipapi.ToolEventArgsDelta || te.ArgsDelta == "" {
		return sdk.ToolPass, aipapi.ToolEvent{}, nil
	}
	out := te
	out.ArgsDelta = h.prefix + strings.TrimPrefix(te.ArgsDelta, h.prefix)
	return sdk.ToolRewrite, out, nil
}

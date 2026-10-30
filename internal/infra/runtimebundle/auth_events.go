package runtimebundle

import (
	"fmt"
	"log/slog"
	"strings"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/authevent"
)

func buildAuthEventDispatcher(cfg *config.Config, log *slog.Logger, opts *BuildOptions, wrapSink func(coreauth.EventSink) coreauth.EventSink) (*coreauth.EventDispatcher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	var injected coreauth.EventSink
	if opts != nil && opts.Auth.AuthEventSink != nil {
		injected = opts.Auth.AuthEventSink
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Auth.EventDelivery))
	if mode == "" {
		mode = "default"
	}
	if injected != nil && mode != "custom" {
		return nil, fmt.Errorf("%w: use auth.event_delivery: custom to wire BuildOptions.AuthEventSink (got %q)", ErrAuthEventSinkDisallowed, cfg.Auth.EventDelivery)
	}
	var sink coreauth.EventSink
	switch mode {
	case "default":
		s, err := authevent.NewSlogEventSink(log)
		if err != nil {
			return nil, err
		}
		sink = s
	case "disabled":
		sink = nil
	case "custom":
		if injected == nil {
			return nil, fmt.Errorf("%w", ErrAuthEventSinkRequired)
		}
		sink = injected
	default:
		return nil, fmt.Errorf("runtimebundle: invalid auth.event_delivery %q", cfg.Auth.EventDelivery)
	}
	if wrapSink != nil {
		sink = wrapSink(sink)
	}
	pol := coreauth.EventFailureBestEffort
	if strings.EqualFold(strings.TrimSpace(cfg.Auth.EventFailurePolicy), "fail_closed") {
		pol = coreauth.EventFailureFailClosed
	}
	return coreauth.NewEventDispatcher(sink, pol), nil
}

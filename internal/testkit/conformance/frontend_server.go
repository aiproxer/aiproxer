package conformance

import (
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	frontanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	frontgemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// MountFrontend registers the bundled frontend handler on mux for conformance tests.
func MountFrontend(mux *http.ServeMux, frontendID string, exec *runtime.Executor, routeSelector string) error {
	switch frontendID {
	case "openai-responses":
		mux.Handle("/v1/responses", &frontopenairesponses.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "openai-legacy":
		mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "anthropic":
		mux.Handle("/v1/messages", &frontanthropic.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "gemini":
		h := &frontgemini.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		}
		mux.Handle("/v1beta/", h)
		mux.Handle("/v1beta1/", h)
	case "openresponses":
		var cfg yaml.Node
		if err := yaml.Unmarshal([]byte("{}"), &cfg); err != nil {
			return fmt.Errorf("openresponses config: %w", err)
		}
		return frontopenresponses.Mount(mux, aipsdk.FrontendMountOptions{
			PluginCfg:            cfg,
			Exec:                 exec,
			DefaultRoute:         routeSelector,
			AllowUnauthenticated: true, // isolated conformance mount has no outer auth middleware
		})
	default:
		return fmt.Errorf("unknown frontend id %q", frontendID)
	}
	return nil
}

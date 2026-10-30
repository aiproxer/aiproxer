package codex

import (
	"strings"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// applyReasoningRequestPolicy decides the reasoning fields for a normal Codex
// request; dedicated compaction transport owns its own payload construction.
func applyReasoningRequestPolicy(call *aipapi.Call, model string, cfg Config) *reasoningSpec {
	if call == nil {
		return nil
	}
	marked := consumeNativeContinuityMarker(call)

	// Explicit client/route options retain the existing wire behavior, including
	// an effort that this connector's catalog does not know about.
	if effort := strings.TrimSpace(call.Options.ReasoningEffort); effort != "" {
		return &reasoningSpec{Effort: effort, Summary: "auto"}
	}

	requestEncrypted := marked && nativeReasoningRequestEnabled(cfg)
	// An internal compaction request follows the same explicit control as a
	// normal request. In particular, request_encrypted_reasoning:false is an
	// intentional evaluation mode and must not be forced back on here.
	if !requestEncrypted {
		if effort := supportedConfiguredEffort(cfg, model); effort != "" {
			return &reasoningSpec{Effort: effort, Summary: "auto"}
		}
		return nil
	}

	effort := supportedConfiguredEffort(cfg, model)
	if effort == "" {
		effort = catalogDefaultEffort(cfg.ModelCatalog, model)
	}
	// An empty effort is valid for the Codex Responses reasoning object. Do not
	// manufacture a level when neither configuration nor the catalog supports it.
	return &reasoningSpec{Effort: effort, Summary: "auto"}
}

func nativeContextEnabled(cfg Config) bool {
	return cfg.NativeContext != nil && cfg.NativeContext.Enabled
}

func nativeReasoningRequestEnabled(cfg Config) bool {
	return nativeContextEnabled(cfg) && cfg.NativeContext.RequestEncryptedReasoning
}

func supportedConfiguredEffort(cfg Config, model string) string {
	effort := strings.TrimSpace(cfg.DefaultReasoningEffort)
	if effort == "" {
		return ""
	}
	if cfg.ModelCatalog != nil {
		profile, ok := cfg.ModelCatalog.Profile(model)
		if !ok || !profile.Supports(effort) {
			return ""
		}
	}
	return effort
}

func catalogDefaultEffort(cat *catalog.Catalog, model string) string {
	profile, ok := cat.Profile(model)
	if !ok {
		return ""
	}
	if effort := strings.TrimSpace(profile.DefaultReasoningLevel); effort != "" && profile.Supports(effort) {
		return effort
	}
	return ""
}

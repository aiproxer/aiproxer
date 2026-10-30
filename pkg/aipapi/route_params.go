package aipapi

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// MergeRouteQueryIntoGenerationOptions overlays URL query parameters from a route
// primary onto base options. URI params are explicit routing directives and OVERRIDE
// any corresponding value already set on base (the per-request body/call settings).
// Keys absent from the query leave the base value unchanged.
//
// Recognized keys (first value wins per key): temperature, top_p, max_output_tokens,
// reasoning_effort, verbosity, parallel_tool_calls (true/false/1/0).
func MergeRouteQueryIntoGenerationOptions(base GenerationOptions, q url.Values) (GenerationOptions, error) {
	out := base
	if len(q) == 0 {
		if err := out.validate(); err != nil {
			return GenerationOptions{}, err
		}
		return out, nil
	}

	if s := firstQuery(q, "temperature"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return GenerationOptions{}, fmt.Errorf("route param temperature: %w", err)
		}
		out.Temperature = &v
	}
	if s := firstQuery(q, "top_p", "topP"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return GenerationOptions{}, fmt.Errorf("route param top_p: %w", err)
		}
		out.TopP = &v
	}
	if s := firstQuery(q, "max_output_tokens", "max_tokens"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			return GenerationOptions{}, fmt.Errorf("route param max_output_tokens: %w", err)
		}
		out.MaxOutputTokens = &v
	}
	if s := firstQuery(q, "reasoning_effort"); s != "" {
		out.ReasoningEffort = s
	}
	if s := firstQuery(q, "verbosity"); s != "" {
		v, err := ParseVerbosityLevel(s)
		if err != nil {
			return GenerationOptions{}, fmt.Errorf("route param verbosity: %w", err)
		}
		out.Verbosity = v
	}
	if s := firstQuery(q, "parallel_tool_calls"); s != "" {
		b, err := parseBoolParam(s)
		if err != nil {
			return GenerationOptions{}, fmt.Errorf("route param parallel_tool_calls: %w", err)
		}
		out.ParallelToolCalls = &b
	}

	if err := out.validate(); err != nil {
		return GenerationOptions{}, err
	}
	return out, nil
}

func firstQuery(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func parseBoolParam(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean, got %q", s)
	}
}

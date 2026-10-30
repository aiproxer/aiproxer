package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func resolveAppURL(cfg Config, captured string) string {
	captured = acceptURL(captured)
	if cfg.LegacyAppURL {
		if captured != "" {
			return captured
		}
		return cfg.StaticReferer
	}
	mode := strings.TrimSpace(cfg.AppURLMode)
	if mode == "" {
		mode = "proxy"
	}
	switch mode {
	case "proxy":
		return DefaultAppURL
	case "passthrough":
		return captured
	case "custom":
		return cfg.AppURLValue
	case "drop":
		return ""
	default:
		return ""
	}
}

func resolveAppTitle(cfg Config, captured string) string {
	captured = acceptTitle(captured)
	if cfg.LegacyAppTitle {
		if captured != "" {
			return captured
		}
		return cfg.StaticTitle
	}
	mode := strings.TrimSpace(cfg.AppTitleMode)
	if mode == "" {
		mode = "proxy"
	}
	switch mode {
	case "proxy":
		return DefaultAppTitle
	case "passthrough":
		return captured
	case "custom":
		return cfg.AppTitleValue
	case "drop":
		return ""
	default:
		return ""
	}
}

func acceptURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return raw
}

func acceptTitle(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !utf8.ValidString(raw) || len(raw) > 256 || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	return raw
}

func extString(call aipapi.Call, key string) string {
	raw := extRaw(call, key)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func extRaw(call aipapi.Call, key string) json.RawMessage {
	if call.Extensions == nil {
		return nil
	}
	return call.Extensions[key]
}

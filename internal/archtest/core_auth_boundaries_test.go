package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestInternalCoreAuthDoesNotImportConcretePluginsOrStdhttp keeps auth ports free of
// official protocol plugins, the HTTP server stack, and transport-layer SDKs
// (authentication-architecture-refactor, task 1.4).
func TestInternalCoreAuthDoesNotImportConcretePluginsOrStdhttp(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		name, sub, msg string
	}{
		{name: "no_internal_plugins", sub: "/internal/plugins/", msg: "internal/core/auth must not import concrete protocol plugins"},
		{name: "no_stdhttp", sub: "/internal/stdhttp", msg: "internal/core/auth must not import stdhttp; auth ports stay transport-agnostic"},
		{name: "no_aipsdk_transport", sub: "/pkg/aipsdk/transport/", msg: "internal/core/auth must not import pkg/aipsdk/transport (driving-adapter only)"},
		{name: "no_openai_sdk", sub: "github.com/openai/openai-go", msg: "internal/core/auth must not import OpenAI provider SDK"},
		{name: "no_anthropic_sdk", sub: "github.com/anthropics/anthropic-sdk-go", msg: "internal/core/auth must not import Anthropic SDK"},
		{name: "no_aws_sdk", sub: "github.com/aws/aws-sdk-go-v2", msg: "internal/core/auth must not import AWS SDKs"},
		{name: "no_slog", sub: "log/slog", msg: "internal/core/auth must not import log/slog; use EventSink wiring from infra"},
	}
	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/auth")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []goListPackage
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	for _, r := range forbidden {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			for _, pkg := range pkgs {
				if strings.HasSuffix(pkg.ImportPath, "_test") {
					continue
				}
				for _, imp := range pkg.Imports {
					if strings.Contains(imp, r.sub) {
						t.Fatalf("%s: forbidden import %q in %q", r.msg, imp, pkg.ImportPath)
					}
				}
			}
		})
	}
}

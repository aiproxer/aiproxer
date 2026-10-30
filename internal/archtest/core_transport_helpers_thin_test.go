package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestCoreTransportHelpersStayThin keeps the transport/diagnostic surfaces inside
// the core zone thin: internal/core/http, internal/core/admin, internal/core/diag,
// and the secure-session diag adapter must never directly import backend plugins,
// provider SDKs, internal/infra, or composition roots. Provider semantics live in
// plugins; wiring lives in composition roots — these helpers only translate
// transport/diagnostic concerns inward.
func TestCoreTransportHelpersStayThin(t *testing.T) {
	t.Parallel()
	out, err := cachedGoList(
		t, "-json", "-test=false",
		"./internal/core/http/...",
		"./internal/core/admin/...",
		"./internal/core/diag/...",
		"./internal/core/securesession/adapters/diag/...",
	)
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []goListPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages for core transport helper patterns")
	}
	forbidden := []struct {
		name, sub, msg string
	}{
		{name: "no_backend_plugins", sub: "/internal/plugins/backends", msg: "core transport helpers must not directly import backend plugins"},
		{name: "no_openai_sdk", sub: "github.com/openai/openai-go", msg: "core transport helpers must not directly import OpenAI provider SDK"},
		{name: "no_anthropic_sdk", sub: "github.com/anthropics/anthropic-sdk-go", msg: "core transport helpers must not directly import Anthropic SDK"},
		{name: "no_genai_sdk", sub: "google.golang.org/genai", msg: "core transport helpers must not directly import Google GenAI SDK"},
		{name: "no_aws_sdk", sub: "github.com/aws/aws-sdk-go-v2", msg: "core transport helpers must not directly import AWS SDK"},
		{name: "no_infra", sub: "/internal/infra/", msg: "core transport helpers must not directly import internal/infra"},
		{name: "no_standardplugins", sub: "/internal/standardplugins", msg: "core transport helpers must not directly import composition root standardplugins"},
		{name: "no_pluginreg", sub: "/internal/pluginreg", msg: "core transport helpers must not directly import composition root pluginreg"},
		{name: "no_stdhttp", sub: "/internal/stdhttp", msg: "core transport helpers must not directly import composition root stdhttp"},
		{name: "no_runtimebundle", sub: "/internal/infra/runtimebundle", msg: "core transport helpers must not directly import composition root runtimebundle"},
	}
	for _, r := range forbidden {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			for _, pkg := range pkgs {
				for _, imp := range pkg.Imports {
					if strings.Contains(imp, r.sub) {
						t.Fatalf("%s: %s directly imports %s", r.msg, pkg.ImportPath, imp)
					}
				}
			}
		})
	}
}

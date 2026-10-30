package archtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestInternalCoreRuntimeDoesNotImportProviderSDKsOrProtocolPlugins(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		name, sub, msg string
	}{
		{name: "no_internal_plugins", sub: "/internal/plugins/", msg: "internal/core/runtime must not import concrete protocol plugins"},
		{name: "no_openai_sdk", sub: "github.com/openai/openai-go", msg: "internal/core/runtime must not import OpenAI provider SDK"},
		{name: "no_anthropic_sdk", sub: "github.com/anthropics/anthropic-sdk-go", msg: "internal/core/runtime must not import Anthropic SDK"},
		{name: "no_genai_sdk", sub: "google.golang.org/genai", msg: "internal/core/runtime must not import Google GenAI SDK"},
		{name: "no_bedrock_sdk", sub: "github.com/aws/aws-sdk-go-v2/service/bedrockruntime", msg: "internal/core/runtime must not import Bedrock runtime SDK"},
	}
	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/runtime/...")
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
				for _, imp := range pkg.Imports {
					if strings.Contains(imp, r.sub) {
						t.Fatalf("%s: forbidden import %q in %q", r.msg, imp, pkg.ImportPath)
					}
				}
			}
		})
	}
}

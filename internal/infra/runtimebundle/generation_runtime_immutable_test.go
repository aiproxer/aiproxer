package runtimebundle_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

type sentinelHTTPAuth struct{}

func (sentinelHTTPAuth) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{}, nil
}

type otherHTTPAuth struct{}

func (otherHTTPAuth) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{}, nil
}

// TestImmutable_GroupAccessorsDefensiveCopies mutates every returned slice/map
// and nested config/registration snapshot and verifies subsequent reads are unchanged.
func TestImmutable_GroupAccessorsDefensiveCopies(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "imm2", "immutable-group", "imm2:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	// Nested YAML / config source must not leak into published snapshots.
	cand.Routing.DefaultRoute = "mutated:gone"
	cand.Plugins.Frontends = nil
	if len(cand.Plugins.Backends) > 0 {
		cand.Plugins.Backends[0].Config = genYAMLNode(t, `text: "mutated-after-compile"`)
	}

	routing := gb.Routing()
	routing.DefaultRoute = "mutated-route"
	routing.RoutePrefixes = append(routing.RoutePrefixes, "x")
	if gb.Routing().DefaultRoute != "imm2:stub-default" {
		t.Fatalf("routing not frozen: %q", gb.Routing().DefaultRoute)
	}

	prefixes := gb.RoutePrefixes()
	if len(prefixes) == 0 {
		t.Fatal("expected route prefixes")
	}
	origPrefix := prefixes[0]
	prefixes[0] = "mutated-prefix"
	if gb.RoutePrefixes()[0] != origPrefix {
		t.Fatal("RoutePrefixes not defensive")
	}

	frontends := gb.FrozenFrontends()
	if len(frontends) == 0 {
		t.Fatal("expected frontends")
	}
	origFE := frontends[0].ID
	frontends[0].ID = "mutated-frontend"
	frontends[0].Config = yaml.Node{Kind: yaml.ScalarNode, Value: "mutated"}
	if gb.FrozenFrontends()[0].ID != origFE {
		t.Fatal("FrozenFrontends not defensive")
	}

	regs := gb.Registrations()
	if len(regs) == 0 {
		t.Fatal("expected registrations")
	}
	origReg := regs[0].ID
	origNode := cloneYAMLForAssert(t, regs[0].Config.Node)
	regs[0].ID = "mutated-reg"
	regs[0].Enabled = !regs[0].Enabled
	regs[0].Config.Node = genYAMLNode(t, `poison: true`)
	if len(regs[0].Config.Node.Content) > 0 {
		regs[0].Config.Node.Content[0].Value = "nested-poison"
	}
	freshRegs := gb.Registrations()
	if freshRegs[0].ID != origReg {
		t.Fatal("Registrations not defensive")
	}
	if yamlNodesEqual(freshRegs[0].Config.Node, regs[0].Config.Node) {
		t.Fatal("Registrations Config.Node not defensively copied")
	}
	if !yamlNodesEqual(freshRegs[0].Config.Node, origNode) {
		t.Fatal("Registrations Config.Node changed after returned-slice mutation")
	}

	kinds := bundle.BackendFactoryKindCounts()
	if kinds != nil {
		for k := range kinds {
			kinds[k] = 999
		}
		kinds2 := bundle.BackendFactoryKindCounts()
		for _, v := range kinds2 {
			if v == 999 {
				t.Fatal("BackendFactoryKindCounts not defensive")
			}
		}
	}

	ids := gb.BackendIDs()
	if len(ids) == 0 {
		t.Fatal("expected backend IDs")
	}
	origID := ids[0]
	ids[0] = "mutated-backend"
	if gb.BackendIDs()[0] != origID {
		t.Fatal("BackendIDs not defensive")
	}

	assertHTTPAuthAndNestedRegistrationCopies(t)
}

func assertHTTPAuthAndNestedRegistrationCopies(t *testing.T) {
	t.Helper()
	sentinel := sentinelHTTPAuth{}
	other := otherHTTPAuth{}
	srcAuth := []httpauth.Provider{sentinel}
	srcRegs := []aipsdk.Registration{{
		ID:      "nested-reg",
		Enabled: true,
		Kind:    aipsdk.PluginKindBackend,
		Config: aipsdk.ConfigPayload{
			Node: genYAMLNode(t, "outer:\n  inner: keep-me\n"),
		},
	}}
	origInner := nestedYAMLValue(t, srcRegs[0].Config.Node, "inner")
	b := runtimebundle.NewGenerationBundleWithPublicationForTest(srcAuth, srcRegs)

	auth := b.HTTPAuthProviders()
	if len(auth) != 1 {
		t.Fatalf("expected one auth provider, got %d", len(auth))
	}
	origProvider := auth[0]
	if origProvider != sentinel {
		t.Fatal("expected sentinel auth provider identity")
	}
	auth[0] = other
	freshAuth := b.HTTPAuthProviders()
	if len(freshAuth) != 1 || freshAuth[0] != origProvider || freshAuth[0] != sentinel {
		t.Fatal("HTTPAuthProviders element identity not retained after returned-slice mutation")
	}

	srcAuth[0] = nil
	afterSrcAuth := b.HTTPAuthProviders()
	if len(afterSrcAuth) != 1 || afterSrcAuth[0] != sentinel {
		t.Fatal("mutating source auth after construction affected runtime snapshot")
	}

	regs := b.Registrations()
	if len(regs) != 1 {
		t.Fatalf("expected one registration, got %d", len(regs))
	}
	origID := regs[0].ID
	origNode := cloneYAMLForAssert(t, regs[0].Config.Node)
	regs[0].ID = "mutated-nested-reg"
	if len(regs[0].Config.Node.Content) > 0 {
		// Mutate nested mapping content in place where present.
		mutateNestedYAMLValue(t, &regs[0].Config.Node, "inner", "mutated-inner")
	}
	regs[0].Config.Node = genYAMLNode(t, "outer:\n  inner: replaced\n")
	fresh := b.Registrations()
	if fresh[0].ID != origID {
		t.Fatal("nested Registrations ID not defensive")
	}
	if nestedYAMLValue(t, fresh[0].Config.Node, "inner") != origInner {
		t.Fatalf("nested Config.Node inner=%q want %q", nestedYAMLValue(t, fresh[0].Config.Node, "inner"), origInner)
	}
	if !yamlNodesEqual(fresh[0].Config.Node, origNode) {
		t.Fatal("nested Config.Node changed after returned mutation")
	}

	srcRegs[0].ID = "source-mutated"
	mutateNestedYAMLValue(t, &srcRegs[0].Config.Node, "inner", "source-mutated-inner")
	afterSrc := b.Registrations()
	if afterSrc[0].ID != origID {
		t.Fatal("mutating source registration after construction affected runtime snapshot")
	}
	if nestedYAMLValue(t, afterSrc[0].Config.Node, "inner") != origInner {
		t.Fatal("mutating source nested config after construction affected runtime snapshot")
	}
}

func cloneYAMLForAssert(t *testing.T, n yaml.Node) yaml.Node {
	t.Helper()
	raw, err := yaml.Marshal(&n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out yaml.Node
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for out.Kind == yaml.DocumentNode {
		if len(out.Content) == 0 || out.Content[0] == nil {
			return yaml.Node{}
		}
		out = *out.Content[0]
	}
	return out
}

func yamlNodesEqual(a, b yaml.Node) bool {
	ra, err := yaml.Marshal(&a)
	if err != nil {
		return false
	}
	rb, err := yaml.Marshal(&b)
	if err != nil {
		return false
	}
	return string(ra) == string(rb)
}

func nestedYAMLValue(t *testing.T, n yaml.Node, key string) string {
	t.Helper()
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0] == nil {
			return ""
		}
		n = *n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		// Look one level down for nested maps (outer: {inner: ...}).
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i] != nil && n.Content[i].Value == key && n.Content[i+1] != nil {
				return n.Content[i+1].Value
			}
			if n.Content[i+1] != nil && n.Content[i+1].Kind == yaml.MappingNode {
				return nestedYAMLValue(t, *n.Content[i+1], key)
			}
		}
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i] != nil && n.Content[i].Value == key && n.Content[i+1] != nil {
			return n.Content[i+1].Value
		}
		if n.Content[i+1] != nil && n.Content[i+1].Kind == yaml.MappingNode {
			if v := nestedYAMLValue(t, *n.Content[i+1], key); v != "" {
				return v
			}
		}
	}
	return ""
}

func mutateNestedYAMLValue(t *testing.T, n *yaml.Node, key, value string) {
	t.Helper()
	if n == nil {
		return
	}
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0] == nil {
			return
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i] != nil && n.Content[i].Value == key && n.Content[i+1] != nil {
			n.Content[i+1].Value = value
			return
		}
		if n.Content[i+1] != nil && n.Content[i+1].Kind == yaml.MappingNode {
			mutateNestedYAMLValue(t, n.Content[i+1], key, value)
		}
	}
}

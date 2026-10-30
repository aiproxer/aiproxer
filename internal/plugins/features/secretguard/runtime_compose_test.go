package secretguard

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestEnabledRegistrations_rejectsMultipleEnabledSecretsGuardFactories(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: ID, Enabled: true},
		{Kind: aipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: ID, Enabled: true},
	}
	_, err := EnabledRegistrations(regs)
	if err == nil {
		t.Fatal("expected duplicate enabled secrets-guard registrations to fail")
	}
	if !strings.Contains(err.Error(), "multiple enabled secrets-guard registrations") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnabledRegistrations_ignoresExplicitOtherFactoryEvenWhenIDMatches(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: ID, FactoryKind: "other-feature", Enabled: true},
	}
	got, err := EnabledRegistrations(regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got=%+v", got)
	}
}

func TestComposeRuntimeConfig_acceptsEnabledAndDisabledSameFactory(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "enabled", FactoryKind: ID, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: redact\naudit_failure_policy: best_effort\n")}},
		{Kind: aipsdk.PluginKindFeature, ID: "disabled", FactoryKind: ID, Enabled: false, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: block\naudit_failure_policy: fail_closed\n")}},
	}
	got, err := ComposeRuntimeConfig("single_user", regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("expected enabled runtime config")
	}
	if got.Action != "redact" {
		t.Fatalf("action=%q", got.Action)
	}
	if got.AuditFailurePolicy != "best_effort" {
		t.Fatalf("audit_failure_policy=%q", got.AuditFailurePolicy)
	}
}

func TestComposeRuntimeConfig_factoryKindWinsOverInstanceID(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: ID, FactoryKind: "other-feature", Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: log\n")}},
	}
	got, err := ComposeRuntimeConfig("single_user", regs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatalf("expected unrelated feature to be ignored, got %#v", got)
	}
}

func TestComposeRuntimeConfig_acceptsExplicitSecretsGuardFactoryKindWithDifferentID(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "guard-one", FactoryKind: ID, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: log\n")}},
	}
	got, err := ComposeRuntimeConfig("single_user", regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Action != "log" {
		t.Fatalf("got=%#v", got)
	}
}

func TestComposeRuntimeConfig_duplicateEnabledErrorIsBounded(t *testing.T) {
	t.Parallel()
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "guard-one", FactoryKind: ID, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: log\n")}},
		{Kind: aipsdk.PluginKindFeature, ID: "guard-two", FactoryKind: ID, Enabled: true, Config: aipsdk.ConfigPayload{Node: mustNode(t, "action: redact\n")}},
	}
	_, err := ComposeRuntimeConfig("single_user", regs)
	if err == nil {
		t.Fatal("expected duplicate enabled registration error")
	}
	for _, bad := range []string{"action:", "redact", "log", "guard-one", "guard-two"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaked %q: %v", bad, err)
		}
	}
}

func mustNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

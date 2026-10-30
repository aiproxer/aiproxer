package service

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestServiceResolve_HTTPAdvertisesCompactionCapabilityAndDialect(t *testing.T) {
	t.Parallel()

	instance := &instance{kind: FactoryKindHTTP}
	profile, err := instance.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.Compaction {
		t.Fatal("HTTP Codex profile does not advertise compaction")
	}
	if len(profile.DialectSupport.CompactionDialects) != 1 || profile.DialectSupport.CompactionDialects[0].Implementor != "openai-codex" {
		t.Fatalf("compaction dialects = %#v", profile.DialectSupport.CompactionDialects)
	}
	if backendplugin.FeatureAccountingEvidence == "" {
		t.Fatal("accounting feature must remain a named negotiation gate")
	}
}

func TestServiceResolve_AppServerDoesNotAdvertiseNativeCompaction(t *testing.T) {
	t.Parallel()

	profile, err := (&instance{kind: FactoryKindAppServer}).Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Capabilities.Compaction || len(profile.DialectSupport.CompactionDialects) != 0 {
		t.Fatalf("app-server compaction profile = %#v", profile)
	}
}

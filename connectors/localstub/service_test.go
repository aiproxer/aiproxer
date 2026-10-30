package localstub_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/localstub/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestConformance_ServiceSuite(t *testing.T) {
	t.Parallel()
	svc := service.New()
	rep := conformance.Run(context.Background(), svc)
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestService_ConfigParityAndStream(t *testing.T) {
	t.Parallel()
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "dogfood-local",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("text: hello-stub\ninput_tokens: 3\noutput_tokens: 7\n"),
		Negotiation: backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.Streaming || len(profile.RoutePrefixes) == 0 || profile.RoutePrefixes[0] != service.FactoryKind {
		t.Fatalf("profile=%+v", profile)
	}
	models, err := inst.ListModels(context.Background(), 8)
	if err != nil || len(models.Models) != 1 || models.Models[0].NativeModelID != "stub-default" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	counter, ok := inst.(backendplugin.TokenCounter)
	if !ok {
		t.Fatal("expected TokenCounter")
	}
	count, err := counter.CountTokens(context.Background(), backendplugin.CountTokensRequest{InstanceID: "dogfood-local"})
	if err != nil || count.InputTokens == nil || *count.InputTokens != 3 {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "stub-default",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "dogfood-local", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		t.Fatal(err)
	}
	var sawText bool
	for _, fr := range ms.outbox {
		if fr.Kind == backendplugin.ServerFrameEvent && fr.Event != nil && fr.Event.Kind == backendplugin.EventTextDelta {
			if fr.Event.Delta == nil || !strings.Contains(*fr.Event.Delta, "hello-stub") {
				t.Fatalf("text=%v", fr.Event.Delta)
			}
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected text delta")
	}
}

func TestService_FinalizeBillingProvidesConnectorLevelShadowEvidence(t *testing.T) {
	t.Parallel()
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "shadow-local",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("input_tokens: 11\noutput_tokens: 5\n"),
		Negotiation: backendplugin.Negotiation{Compatible: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	finalizer, ok := inst.(backendplugin.BillingFinalizer)
	if !ok {
		t.Fatal("localstub must expose BillingFinalizer")
	}
	response, err := finalizer.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{
		InstanceID: "shadow-local", ALegID: "a-1", BLegID: "b-1", ModelID: "stub-default",
		Reason: "shadow_observe", IdempotencyKey: "shadow-local:a-1:b-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.EvidenceQuality != "local-stub" {
		t.Fatalf("evidence quality = %q", response.EvidenceQuality)
	}
	if response.Usage.InputTokens == nil || *response.Usage.InputTokens != 11 ||
		response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 5 ||
		response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 16 {
		t.Fatalf("finalize usage = %+v", response.Usage)
	}
	wantPresence := backendplugin.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true}
	if response.Usage.Presence != wantPresence {
		t.Fatalf("finalize presence = %+v, want %+v", response.Usage.Presence, wantPresence)
	}
}

func TestConfig_RejectsNegativeTokens(t *testing.T) {
	t.Parallel()
	_, err := service.ParseConfigYAML([]byte("input_tokens: -1\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPackage_ReleaseMetadataAndManifestTemplate(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	for _, rel := range []string{"release.yaml", "manifest/template.backendplugin.json"} {
		p := filepath.Join(root, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("empty %s", rel)
		}
	}
	body := string(mustRead(t, filepath.Join(root, "release.yaml")))
	for _, want := range []string{"factory_kind: local-stub", "plugin_id:", "manifest_template:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("release.yaml missing %q", want)
		}
	}
}

func TestIsolation_SourceOmitsRootInternal(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		const forbidden = "github.com/aiproxer/aiproxer/internal/"
		for line := range strings.SplitSeq(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "\"") && strings.Contains(trim, forbidden) {
				t.Errorf("%s imports root internal/: %s", path, trim)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }
func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

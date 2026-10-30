package compatibleparity_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/compatibleparity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestInstanceIsolation_sameKindIndependentEndpointCredTokenizerInventoryConcurrency(t *testing.T) {
	for _, pair := range compatibleparity.IsolationPairs() {
		t.Run(string(pair.Family), func(t *testing.T) {
			// Env credential roots require process env; keep sequential within the pair.
			runInstanceIsolationPair(t, pair)
		})
	}
}

func runInstanceIsolationPair(t *testing.T, pair compatibleparity.InstancePair) {
	t.Helper()

	srvA := newIsolationServer(t, "A")
	srvB := newIsolationServer(t, "B")
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)

	instA := pair.InstanceA
	instB := pair.InstanceB
	instA.BaseURL = isolationBaseURL(pair.Family, srvA.URL)
	instB.BaseURL = isolationBaseURL(pair.Family, srvB.URL)

	t.Setenv(instA.APIKeyEnvVarRoot, instA.EnvKeyValue)
	t.Setenv(instB.APIKeyEnvVarRoot, instB.EnvKeyValue)

	cfgA, err := compatibleparity.MustDecodeInstance(instA.InstanceID, pair.Factory, compatibleparity.CompatibleYAML(instA, instA.BaseURL))
	if err != nil {
		t.Fatalf("decode A: %v", err)
	}
	cfgB, err := compatibleparity.MustDecodeInstance(instB.InstanceID, pair.Factory, compatibleparity.CompatibleYAML(instB, instB.BaseURL))
	if err != nil {
		t.Fatalf("decode B: %v", err)
	}
	if cfgA.TokenizerID == cfgB.TokenizerID || cfgA.MaxConcurrentRequests == cfgB.MaxConcurrentRequests {
		t.Fatal("fixture pair must differ in tokenizer and concurrency")
	}
	if cfgA.APIKeyEnvVarRoot == cfgB.APIKeyEnvVarRoot || cfgA.BaseURL == cfgB.BaseURL {
		t.Fatal("fixture pair must differ in endpoint and credential root")
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	beA := buildCompatible(t, reg, pair.Factory, instA.InstanceID, compatibleparity.CompatibleYAML(instA, instA.BaseURL), srvA.Client())
	beB := buildCompatible(t, reg, pair.Factory, instB.InstanceID, compatibleparity.CompatibleYAML(instB, instB.BaseURL), srvB.Client())

	if len(beA.BackendPrefixes) != 1 || beA.BackendPrefixes[0] != instA.BackendPrefix {
		t.Fatalf("A prefixes=%v want [%s]", beA.BackendPrefixes, instA.BackendPrefix)
	}
	if len(beB.BackendPrefixes) != 1 || beB.BackendPrefixes[0] != instB.BackendPrefix {
		t.Fatalf("B prefixes=%v want [%s]", beB.BackendPrefixes, instB.BackendPrefix)
	}

	// Inventory isolation (static inline rows).
	assertInventoryNative(t, beA, instA.ModelNativeID)
	assertInventoryNative(t, beB, instB.ModelNativeID)

	// Endpoint + credential isolation via distinct httptest captures.
	call := isolationCall(pair.Family)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "iso-model"}}
	openCollect(t, beA, call, cand)
	openCollect(t, beB, call, cand)
	if got := srvA.LastAuth(); !strings.Contains(got, instA.EnvKeyValue) && !strings.Contains(got, "sk-test-a") {
		// Bearer or x-api-key must carry instance A's env secret, not B's.
		if strings.Contains(got, instB.EnvKeyValue) {
			t.Fatalf("A request used B credential: auth=%q", got)
		}
		t.Fatalf("A auth=%q does not carry A env credential %q", got, instA.EnvKeyValue)
	}
	if got := srvB.LastAuth(); strings.Contains(got, instA.EnvKeyValue) {
		t.Fatalf("B request leaked A credential: auth=%q", got)
	}
	if strings.Contains(srvA.LastAuth(), instB.EnvKeyValue) {
		t.Fatalf("A request leaked B credential: auth=%q", srvA.LastAuth())
	}

	// Tokenizer + concurrency attachment gaps (Tasks 3.1–3.3). Collect both so
	// the RED suite documents every missing CompatibleModeConfig policy seam.
	var gaps []string
	if !tokenizerAttached(beA, cfgA.TokenizerID) {
		gaps = append(gaps, fmt.Sprintf("RED: instance %s tokenizer %q from CompatibleModeConfig is not attached", instA.InstanceID, cfgA.TokenizerID))
	}
	if !tokenizerAttached(beB, cfgB.TokenizerID) {
		gaps = append(gaps, fmt.Sprintf("RED: instance %s tokenizer %q from CompatibleModeConfig is not attached", instB.InstanceID, cfgB.TokenizerID))
	}
	if sameTokenizerAttachment(beA, beB) && cfgA.TokenizerID != cfgB.TokenizerID {
		gaps = append(gaps, "RED: same-kind instances must not share tokenizer attachment state")
	}
	if gap := concurrencyGap(t, instA.InstanceID, cfgA.MaxConcurrentRequests, "A"); gap != "" {
		gaps = append(gaps, gap)
	}
	if gap := concurrencyGap(t, instB.InstanceID, cfgB.MaxConcurrentRequests, "B"); gap != "" {
		gaps = append(gaps, gap)
	}
	if len(gaps) > 0 {
		t.Fatal(strings.Join(gaps, "\n"))
	}
}

type isolationServer struct {
	*httptest.Server
	mu   sync.Mutex
	auth string
}

func newIsolationServer(t *testing.T, label string) *isolationServer {
	t.Helper()
	s := &isolationServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		if x := r.Header.Get("X-Api-Key"); x != "" {
			auth = "x-api-key:" + x
		}
		s.mu.Lock()
		s.auth = auth
		s.mu.Unlock()
		_ = body
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/responses"):
			_, _ = io.WriteString(w, `{"id":"iso","object":"response","created_at":1,"status":"completed","model":"iso-model","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"iso-`+label+`"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case strings.Contains(r.URL.Path, "/messages"):
			_, _ = io.WriteString(w, `{"id":"iso","type":"message","role":"assistant","model":"iso-model","content":[{"type":"text","text":"iso-`+label+`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		default:
			_, _ = io.WriteString(w, `{"id":"iso","object":"chat.completion","created":1,"model":"iso-model","choices":[{"index":0,"message":{"role":"assistant","content":"iso-`+label+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	return s
}

func (s *isolationServer) LastAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

func isolationBaseURL(family compatibleparity.Family, srvURL string) string {
	if family == compatibleparity.FamilyAnthropic {
		return srvURL
	}
	return srvURL + "/v1"
}

func buildCompatible(t *testing.T, reg *pluginreg.Registry, factory, instanceID, raw string, client *http.Client) execbackend.Backend {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(factory, instanceID, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle(%s): %v", factory, err)
	}
	return res.Backend
}

func assertInventoryNative(t *testing.T, be execbackend.Backend, nativeID string) {
	t.Helper()
	if be.ModelInventory == nil {
		t.Fatal("expected model inventory")
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("inventory source=%q models=%+v", snap.Source, snap.Models)
	}
	found := false
	for _, m := range snap.Models {
		if m.NativeID == nativeID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("native_id %q not in inventory %+v", nativeID, snap.Models)
	}
}

func isolationCall(family compatibleparity.Family) aipapi.Call {
	op := aipapi.OperationOpenAIChatCompletions
	if family == compatibleparity.FamilyOpenAIResponses {
		op = aipapi.OperationOpenAIResponses
	}
	if family == compatibleparity.FamilyAnthropic {
		op = ""
	}
	return aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("iso")}}},
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func openCollect(t *testing.T, be execbackend.Backend, call aipapi.Call, cand routing.AttemptCandidate) {
	t.Helper()
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = es.Close() }()
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
}

// tokenizerAttached reports whether the constructed backend reflects cfgTokenizer.
func tokenizerAttached(be execbackend.Backend, cfgTokenizer string) bool {
	if strings.TrimSpace(cfgTokenizer) == "" {
		return true
	}
	return strings.TrimSpace(be.TokenizerID) == strings.TrimSpace(cfgTokenizer)
}

func sameTokenizerAttachment(a, b execbackend.Backend) bool {
	return a.TokenizerID == b.TokenizerID
}

func concurrencyGap(t *testing.T, instanceID string, limit int, label string) string {
	t.Helper()
	if limit <= 0 {
		return fmt.Sprintf("%s: fixture max_concurrent_requests must be positive", label)
	}
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{instanceID: limit}, leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission-test"}))
	if err != nil {
		return fmt.Sprintf("%s: admission registration: %v", label, err)
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	block := make(chan struct{})
	var peak atomic.Int32
	var denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < limit+3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bleg := fmt.Sprintf("%s-b%d", label, i)
			d, err := coord.Admit(context.Background(), isolationAttemptAdmission(instanceID, bleg))
			if err != nil {
				if authoritycoord.IsDenied(err) {
					denied.Add(1)
				}
				return
			}
			cur := peak.Add(1)
			if cur > peak.Load() {
				peak.Store(cur)
			}
			<-block
			_ = coord.Release(context.Background(), d.Stack)
			peak.Add(-1)
		}(i)
	}
	time.Sleep(40 * time.Millisecond)
	close(block)
	wg.Wait()
	if peak.Load() > int32(limit) {
		return fmt.Sprintf("RED: instance %s max_concurrent_requests=%d not enforced (peak in-flight=%d, denied=%d)", label, limit, peak.Load(), denied.Load())
	}
	if denied.Load() == 0 {
		return fmt.Sprintf("RED: instance %s max_concurrent_requests=%d produced no overload denials", label, limit)
	}
	return ""
}

func isolationAttemptAdmission(backendID, bleg string) authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID:   "req",
		AttemptID:   bleg,
		BLegID:      bleg,
		BackendID:   backendID,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Perspective: metering.PerspectiveOperator,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}
